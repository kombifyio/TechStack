package vmleases

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/google/uuid"
)

func TestPrepareNativeAdmissionLeaseCompatibility(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	input := nativeAdmissionLeaseFixture(now)

	lease, generationID, err := PrepareNativeAdmissionLease(input, now, 0)
	if err != nil {
		t.Fatalf("PrepareNativeAdmissionLease() error = %v", err)
	}
	parsed, err := uuid.Parse(generationID)
	if err != nil || parsed == uuid.Nil || parsed.String() != generationID {
		t.Fatalf("resource generation = %q, want canonical non-nil UUID", generationID)
	}
	if got := ResourceGenerationID(lease); got != generationID {
		t.Fatalf("lease generation = %q, want %q", got, generationID)
	}
	if got := ResourceGenerationID(input); got != "" {
		t.Fatalf("input mutated with generation %q", got)
	}
}

func TestPostgresStoreAdmitNativeLeaseTxCreatesAuthorityOwnedLease(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	input := nativeAdmissionLeaseFixture(now)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT clock_timestamp()`)).
		WillReturnRows(sqlmock.NewRows([]string{"database_time"}).AddRow(now))
	mock.ExpectExec(`INSERT INTO techstack_vm_leases`).
		WithArgs(
			vmlease.LeaseID("lease-native-1"), "tenant-1", "org-1", "tenant-1", "ionos",
			vmlease.DesiredStateRunning, "native-request-1", leaseJSONDesiredStateMatcher{desiredState: vmlease.DesiredStateRunning},
			int64(NativeAdmissionLeaseRevision), "owner-1", "server-1", sqlmock.AnyArg(),
			now, now.Add(DefaultNativeAdmissionValidity), now, now,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewPostgresStore(db).AdmitNativeLeaseTx(t.Context(), tx, NativeAdmissionRequest{
		Lease: input, OwnerSubjectID: "owner-1", ServerID: "server-1",
		IdempotencyKey: " native-request-1 ",
	})
	if err != nil {
		t.Fatalf("AdmitNativeLeaseTx() error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.Parse(result.ResourceGenerationID)
	if err != nil || parsed == uuid.Nil || parsed.String() != result.ResourceGenerationID {
		t.Fatalf("resource generation = %q, want canonical non-nil UUID", result.ResourceGenerationID)
	}
	if got := ResourceGenerationID(result.Lease); got != result.ResourceGenerationID {
		t.Fatalf("lease generation = %q, want %q", got, result.ResourceGenerationID)
	}
	if got := ResourceGenerationID(input); got != "" {
		t.Fatalf("input mutated with generation %q", got)
	}
	if result.LeaseRevision != NativeAdmissionLeaseRevision || !result.DatabaseTime.Equal(now) {
		t.Fatalf("authority result = %+v", result)
	}
	if !result.Lease.RenewedAt.Equal(now) || !result.Lease.ValidFrom.Equal(now) ||
		!result.Lease.ValidUntil.Equal(now.Add(DefaultNativeAdmissionValidity)) {
		t.Fatalf("lease validity = [%s,%s], renewed %s", result.Lease.ValidFrom, result.Lease.ValidUntil, result.Lease.RenewedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreAdmitNativeLeaseTxRejectsCallerCustody(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*vmlease.Lease)
		want error
	}{
		{
			name: "caller generation",
			edit: func(lease *vmlease.Lease) {
				lease.Metadata = map[string]string{MetadataKeyResourceGenerationID: uuid.NewString()}
			},
			want: ErrResourceGenerationImmutable,
		},
		{
			name: "provider handle",
			edit: func(lease *vmlease.Lease) { lease.Resource.EngineVMID = "provider-vm-1" },
			want: ErrProviderRefRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			lease := nativeAdmissionLeaseFixture(now)
			test.edit(&lease)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
				WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT clock_timestamp()`)).
				WillReturnRows(sqlmock.NewRows([]string{"database_time"}).AddRow(now))
			mock.ExpectRollback()
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, admitErr := NewPostgresStore(db).AdmitNativeLeaseTx(t.Context(), tx, NativeAdmissionRequest{
				Lease: lease, OwnerSubjectID: "owner-1", ServerID: "server-1",
			})
			if !errors.Is(admitErr, test.want) {
				t.Fatalf("AdmitNativeLeaseTx() error = %v, want %v", admitErr, test.want)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresStoreAdmitNativeLeaseTxReturnsLeaseConflict(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("tenant-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT clock_timestamp()`)).
		WillReturnRows(sqlmock.NewRows([]string{"database_time"}).AddRow(now))
	mock.ExpectExec(`INSERT INTO techstack_vm_leases`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, admitErr := NewPostgresStore(db).AdmitNativeLeaseTx(t.Context(), tx, NativeAdmissionRequest{
		Lease: nativeAdmissionLeaseFixture(now), OwnerSubjectID: "owner-1", ServerID: "server-1",
	})
	if !errors.Is(admitErr, ErrLeaseIdentityConflict) {
		t.Fatalf("AdmitNativeLeaseTx() error = %v, want ErrLeaseIdentityConflict", admitErr)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func nativeAdmissionLeaseFixture(now time.Time) vmlease.Lease {
	return vmlease.Lease{
		ID:      "lease-native-1",
		Subject: vmlease.Subject{Kind: vmlease.SubjectOrg, ID: "org-1", OrgID: "tenant-1"},
		Resource: vmlease.ResourceRef{
			ProviderID: " IONOS ",
		},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
	}
}
