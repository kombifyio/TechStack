package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	migrationLockQueryPattern    = `SELECT pg_advisory_lock\(\$1\)`
	migrationUnlockQueryPattern  = `SELECT pg_advisory_unlock\(\$1\)`
	migrationTimeoutQueryPattern = `SELECT\s+set_config\('lock_timeout', '10s', false\),\s+set_config\('statement_timeout', '2min', false\)`
	migrationResetQueryPattern   = `SELECT\s+set_config\('lock_timeout', '0', false\),\s+set_config\('statement_timeout', '0', false\)`
	legacyTrackerQueryPattern    = `DO \$\$\s+BEGIN\s+IF EXISTS`
	requiredTableQueryPattern    = `SELECT EXISTS \(\s+SELECT 1\s+FROM`
)

// Sensitive migration boundary: do not create a real login credential or grant
// it cross-tenant authority merely to exercise the refusal path.
func TestPendingHouseholdMigrationRefusesLoginBeforeDDL(t *testing.T) {
	const version = "168_household_projection_bindings.sql"
	body, err := migrationsFS.ReadFile("migrations/" + version)
	if err != nil {
		t.Fatal(err)
	}
	for _, catalogUnavailable := range []bool{false, true} {
		sqlDB, mock, mockErr := sqlmock.New()
		if mockErr != nil {
			t.Fatal(mockErr)
		}
		mock.ExpectBegin()
		query := mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM pg_catalog.pg_roles\s*WHERE rolname = 'techstack_household_worker' AND rolcanlogin\s*\)`)
		want := "household migration refuses an existing login credential"
		if catalogUnavailable {
			query.WillReturnError(errors.New("catalog unavailable"))
			want = "household migration role posture unavailable"
		} else {
			query.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		}
		// Any migration execution or ledger write is an unexpected operation.
		mock.ExpectRollback()
		tx, beginErr := sqlDB.BeginTx(context.Background(), nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		got := executePendingMigration(context.Background(), tx, version, body)
		if got == nil || !strings.Contains(got.Error(), want) {
			t.Errorf("catalogUnavailable=%t: error=%v, want %s", catalogUnavailable, got, want)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Error(rollbackErr)
		}
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Error(expectationErr)
		}
		_ = sqlDB.Close()
	}
}

func TestMigrateDiscardsConnectionWhenAdvisoryLockAcquisitionIsAmbiguous(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	lockErr := errors.New("lock response lost")
	mock.ExpectExec(migrationLockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnError(lockErr)

	err = (&DB{DB: sqlDB}).Migrate(context.Background())
	if !errors.Is(err, lockErr) {
		t.Fatalf("Migrate error = %v, want wrapped lock error", err)
	}
	if !strings.Contains(err.Error(), "failed to acquire migration advisory lock within 30s") {
		t.Fatalf("Migrate error = %q, want advisory-lock context", err)
	}
	if got := sqlDB.Stats().OpenConnections; got != 0 {
		t.Fatalf("open connections = %d, want ambiguous lock connection discarded", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMigrateResetsSessionAndUnlocksAfterEarlyMigrationFailure(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	reconcileErr := errors.New("legacy tracker inspection failed")
	mock.ExpectExec(migrationLockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(migrationTimeoutQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(legacyTrackerQueryPattern).
		WillReturnError(reconcileErr)
	mock.ExpectExec(migrationResetQueryPattern).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(migrationUnlockQueryPattern).
		WithArgs(migrationAdvisoryLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	err = (&DB{DB: sqlDB}).Migrate(context.Background())
	if !errors.Is(err, reconcileErr) {
		t.Fatalf("Migrate error = %v, want wrapped reconciliation error", err)
	}
	if !strings.Contains(err.Error(), "failed to reconcile legacy schema_migrations table") {
		t.Fatalf("Migrate error = %q, want tracker-reconciliation context", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestVerifyRequiredControlPlaneTablesAcceptsCompleteSchema(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	requiredTables := []string{
		"schema_migrations",
		"techstack_tenants",
		"stacks",
		"jobs",
		"workers",
		"client_pairing_codes",
		"nodes",
		"services",
		"breakglass_admin",
		"ril_workflow_runs",
		"ril_workflow_timers",
		"techstack_notification_outbox",
		"provider_desired_spec_revisions",
		"provider_operations",
		"provider_operation_receipts",
		"provider_operation_resources",
		"provider_operation_evidence",
		"provider_operation_execution_claims",
		"provider_provision_dispatch_guards",
		"managed_runtime_server_slots",
		"managed_runtime_server_slot_generations",
	}
	for _, table := range requiredTables {
		mock.ExpectQuery(requiredTableQueryPattern).
			WithArgs(table).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}

	if err := verifyRequiredControlPlaneTables(context.Background(), sqlDB); err != nil {
		t.Fatalf("verifyRequiredControlPlaneTables returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestVerifyRequiredControlPlaneTablesRejectsMissingTable(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	mock.ExpectQuery(requiredTableQueryPattern).
		WithArgs("schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	err = verifyRequiredControlPlaneTables(context.Background(), sqlDB)
	if err == nil || !strings.Contains(err.Error(), "control-plane migration missing required table schema_migrations") {
		t.Fatalf("verifyRequiredControlPlaneTables error = %v, want missing-table error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
