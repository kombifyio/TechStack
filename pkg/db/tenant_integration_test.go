package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// integrationDSN returns the DSN for an integration Postgres instance, or ""
// when the integration suite should be skipped. The variable is named
// distinctly from DATABASE_URL so it does not accidentally point at a
// production database.
func integrationDSN() string {
	return strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	cfg := Config{
		Backend:    StoreBackendPostgres,
		DSN:        dsn,
		DriverName: PostgresDriverName,
	}
	d, err := Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestIntegrationWithTenantSetsGUC(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const tenantID = "tenant-integration-test-1"
	var got string
	err := d.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT current_setting($1, true)", TenantGUC)
		return row.Scan(&got)
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if got != tenantID {
		t.Fatalf("GUC value: got %q, want %q", got, tenantID)
	}

	// After the transaction commits, the GUC must not leak into a follow-up
	// session-level query (set_config(..., true) is local to the tx).
	row := d.QueryRowContext(ctx, "SELECT current_setting($1, true)", TenantGUC)
	var leaked string
	if err := row.Scan(&leaked); err != nil {
		t.Fatalf("post-tx query: %v", err)
	}
	if leaked == tenantID {
		t.Fatalf("GUC leaked into next session: %q", leaked)
	}
}

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
