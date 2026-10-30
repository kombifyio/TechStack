package db

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
)

func TestWithTenantEmptyTenant(t *testing.T) {
	d := &DB{}
	if err := d.WithTenant(context.Background(), "", nil); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("empty: got %v, want ErrNoTenant", err)
	}
	if err := d.WithTenant(context.Background(), "   ", nil); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("whitespace: got %v, want ErrNoTenant", err)
	}
}

func TestWithTenantNilHandle(t *testing.T) {
	var d *DB
	if err := d.WithTenant(context.Background(), "tenant-1", nil); err == nil {
		t.Fatal("expected error for nil DB handle")
	}
}

// Runtime-selected identifiers cannot be supplied through the public user
// journey. These narrow auth-boundary cases exercise that internal failure;
// managed Stage journeys separately prove actual policies and persistence.
func TestTenantContextRejectsRuntimeRebinding(t *testing.T) {
	for _, scenario := range []struct {
		name, requestedTenant, requestSubject string
	}{
		{"foreign tenant", "tenant-b", "user-a"},
		{"foreign user", "tenant-a", "user-b"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: scenario.requestSubject, OrgID: "tenant-a"})
			ctx = tenantdb.NewContextWithToken(ctx, "opaque-gateway-context-fixture")
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('kombify.ctx_token', $1, true)")).
				WithArgs("opaque-gateway-context-fixture").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT subject, org_id FROM kombify_ctx.verified_context()")).
				WillReturnRows(sqlmock.NewRows([]string{"subject", "org_id"}).AddRow("user-a", "tenant-a"))
			mock.ExpectRollback()
			_, err = BeginTenantTransaction(ctx, pool, scenario.requestedTenant)
			if !errors.Is(err, tenantdb.ErrContextMismatch) {
				t.Fatalf("runtime rebinding was not refused: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserContextRejectsRuntimeChosenSubject(t *testing.T) {
	pool, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := tenantdb.NewContextWithToken(context.Background(), "opaque-gateway-context-fixture")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('kombify.ctx_token', $1, true)")).
		WithArgs("opaque-gateway-context-fixture").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT subject, org_id FROM kombify_ctx.verified_context()")).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "org_id"}).AddRow("user-a", "tenant-a"))
	mock.ExpectRollback()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tenantcontext.ApplyUser(ctx, tx, "user-b")
	_ = tx.Rollback()
	if !errors.Is(err, tenantdb.ErrContextMismatch) {
		t.Fatalf("foreign self-account lookup was not refused: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
