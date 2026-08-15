package vmleases

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestLeaseExecutionAuthorityClosedValues(t *testing.T) {
	for _, authority := range []LeaseExecutionAuthority{
		LeaseExecutionAuthorityLegacySimulate,
		LeaseExecutionAuthorityTechStackProviderControl,
	} {
		if err := validateLeaseExecutionAuthority(authority); err != nil {
			t.Fatalf("validate %q: %v", authority, err)
		}
	}
	for _, authority := range []LeaseExecutionAuthority{"", "executor_ready", "simulate", "other"} {
		if err := validateLeaseExecutionAuthority(authority); !errors.Is(err, ErrLeaseExecutionAuthorityUnbound) {
			t.Fatalf("validate %q = %v, want unbound", authority, err)
		}
	}
}

func TestMemoryStoreExecutionAuthorityIsReadOnlyAndTenantScoped(t *testing.T) {
	store := NewMemoryStore()
	leaseID := vmlease.LeaseID("lease-authority")
	store.executionAuthorities[memoryExecutionAuthorityKey{tenantID: "tenant-a", leaseID: leaseID}] = LeaseExecutionAuthorityLegacySimulate

	got, err := store.ExecutionAuthority(context.Background(), "tenant-a", leaseID)
	if err != nil || got != LeaseExecutionAuthorityLegacySimulate {
		t.Fatalf("ExecutionAuthority = %q, %v", got, err)
	}
	if _, err := store.ExecutionAuthority(context.Background(), "tenant-b", leaseID); !errors.Is(err, ErrLeaseExecutionAuthorityUnbound) {
		t.Fatalf("cross-tenant authority error = %v, want unbound", err)
	}
}

func TestPostgresStoreExecutionAuthorityUsesTenantTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.tenant_id', \$1, true\)`).WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT execution_authority[\s\S]+FROM runtime_lease_execution_authorities[\s\S]+WHERE tenant_id = \$1 AND lease_id = \$2`).
		WithArgs("tenant-a", vmlease.LeaseID("lease-a")).
		WillReturnRows(sqlmock.NewRows([]string{"execution_authority"}).AddRow("techstack_provider_control"))
	mock.ExpectCommit()

	got, err := NewPostgresStore(db).ExecutionAuthority(context.Background(), "tenant-a", vmlease.LeaseID("lease-a"))
	if err != nil || got != LeaseExecutionAuthorityTechStackProviderControl {
		t.Fatalf("ExecutionAuthority = %q, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreExecutionAuthorityUnbound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app.tenant_id', \$1, true\)`).WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT execution_authority[\s\S]+FROM runtime_lease_execution_authorities[\s\S]+WHERE tenant_id = \$1 AND lease_id = \$2`).
		WithArgs("tenant-a", vmlease.LeaseID("lease-missing")).
		WillReturnRows(sqlmock.NewRows([]string{"execution_authority"}))
	mock.ExpectRollback()

	_, err = NewPostgresStore(db).ExecutionAuthority(context.Background(), "tenant-a", vmlease.LeaseID("lease-missing"))
	if !errors.Is(err, ErrLeaseExecutionAuthorityUnbound) {
		t.Fatalf("ExecutionAuthority error = %v, want unbound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
