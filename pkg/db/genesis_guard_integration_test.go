package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// openIsolatedGuardTestDB opens a *DB scoped to a brand-new schema so genesis
// guard tests always observe a pristine (ledger-free) database, independent of
// the shared suite schema other integration tests migrate.
func openIsolatedGuardTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}

	schema := "techstack_guard_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	adminDB := stdlib.OpenDB(*adminConfig)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	if _, err := adminDB.ExecContext(setupCtx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create isolated guard schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		_, _ = adminDB.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = adminDB.Close()
	})

	scopedDSN, err := postgresDSNWithSearchPath(dsn, schema+",public")
	if err != nil {
		t.Fatalf("scope integration DSN: %v", err)
	}
	opened, err := Open(Config{
		Backend:    StoreBackendPostgres,
		DSN:        scopedDSN,
		DriverName: PostgresDriverName,
	})
	if err != nil {
		t.Fatalf("open isolated guard DB: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

func ledgerExistsInCurrentSchema(t *testing.T, d *DB) bool {
	t.Helper()
	var exists bool
	err := d.QueryRowContext(context.Background(), `
		SELECT pg_catalog.to_regclass(
			pg_catalog.format('%I.schema_migrations', pg_catalog.current_schema())
		) IS NOT NULL
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("inspect schema_migrations presence: %v", err)
	}
	return exists
}

func TestIntegrationSaaSMigrateRefusesImplicitGenesis(t *testing.T) {
	d := openIsolatedGuardTestDB(t)
	clearGuardEnv(t)
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")

	err := d.Migrate(context.Background())
	if !errors.Is(err, ErrImplicitGenesisRefused) {
		t.Fatalf("Migrate error = %v, want ErrImplicitGenesisRefused", err)
	}
	if !strings.Contains(err.Error(), "refusing implicit genesis") {
		t.Fatalf("Migrate error = %q, want operator-actionable genesis refusal", err)
	}
	if ledgerExistsInCurrentSchema(t, d) {
		t.Fatal("schema_migrations was created despite the genesis refusal; guard must run before any DDL")
	}
}

func TestIntegrationSaaSMigrateGenesisOptInAppliesChainOnce(t *testing.T) {
	d := openIsolatedGuardTestDB(t)
	clearGuardEnv(t)
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")
	t.Setenv(EnvAllowDBGenesis, "true")

	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate with genesis opt-in: %v", err)
	}

	// Once the ledger is populated, the one-shot opt-in is no longer needed.
	t.Setenv(EnvAllowDBGenesis, "")
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate against populated ledger without opt-in: %v", err)
	}
}

func TestIntegrationSelfHostMigrateGenesisStaysPermissive(t *testing.T) {
	d := openIsolatedGuardTestDB(t)
	clearGuardEnv(t)

	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("self-host Migrate against fresh database: %v", err)
	}
	if !ledgerExistsInCurrentSchema(t, d) {
		t.Fatal("self-host Migrate did not create the schema_migrations ledger")
	}
}

func TestIntegrationMigrateEnforcesExpectedDatabaseIdentityPin(t *testing.T) {
	d := openIsolatedGuardTestDB(t)
	clearGuardEnv(t)
	ctx := context.Background()

	var currentDatabase string
	if err := d.QueryRowContext(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		t.Fatalf("load current database: %v", err)
	}

	t.Setenv(EnvExpectedDatabaseIdentity, currentDatabase+"_other")
	err := d.Migrate(ctx)
	if !errors.Is(err, ErrDatabaseIdentityMismatch) || !strings.Contains(err.Error(), "env contract stale") {
		t.Fatalf("Migrate error = %v, want env-contract-stale identity mismatch", err)
	}
	if ledgerExistsInCurrentSchema(t, d) {
		t.Fatal("schema_migrations was created despite the identity mismatch; guard must run before any DDL")
	}

	var systemIdentifier string
	controlErr := d.QueryRowContext(ctx, "SELECT system_identifier::text FROM pg_catalog.pg_control_system()").Scan(&systemIdentifier)
	if controlErr == nil {
		t.Setenv(EnvExpectedDatabaseIdentity, currentDatabase+"@"+systemIdentifier+"0")
		err = d.Migrate(ctx)
		if !errors.Is(err, ErrDatabaseIdentityMismatch) || !strings.Contains(err.Error(), "database moved") {
			t.Fatalf("Migrate error = %v, want database-moved identity mismatch", err)
		}
		t.Setenv(EnvExpectedDatabaseIdentity, currentDatabase+"@"+systemIdentifier)
	} else if errors.Is(controlErr, sql.ErrNoRows) {
		t.Fatalf("read system_identifier: %v", controlErr)
	} else {
		// pg_control_system() may be revoked on hardened providers; the
		// name-only pin remains verifiable.
		t.Setenv(EnvExpectedDatabaseIdentity, currentDatabase)
	}

	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate with matching identity pin: %v", err)
	}
}
