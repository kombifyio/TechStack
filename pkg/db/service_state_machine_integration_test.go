package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	serviceStateTenantA = "integration-service-state-tenant-a"
	serviceStateTenantB = "integration-service-state-tenant-b"
)

func seedServiceStateAggregate(t *testing.T, database *DB, tenantID, stackID, serviceID string) {
	t.Helper()
	ctx := context.Background()
	if err := database.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stacks (id, tenant_id, name) VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
		`, stackID, tenantID, stackID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO services (
				id, tenant_id, stack_id, service_key, name, status,
				desired_state, observed_state, health_state
			) VALUES ($1, $2, $3, 'vaultwarden', 'Vaultwarden', 'running',
				'running', 'running', 'healthy')
			ON CONFLICT (id) DO NOTHING
		`, serviceID, tenantID, stackID)
		return err
	}); err != nil {
		t.Fatalf("seed service aggregate for %s: %v", tenantID, err)
	}
}

func TestIntegrationServiceStateColumnsRejectValuesOutsideTheVocabulary(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedServiceStateAggregate(t, database, serviceStateTenantA, "integration-service-state-stack", "integration-service-state-1")

	for _, test := range []struct {
		column, value, constraint string
	}{
		{column: "desired_state", value: "absent", constraint: "services_desired_state_check"},
		{column: "observed_state", value: "reachable", constraint: "services_observed_state_check"},
		{column: "health_state", value: "degraded", constraint: "services_health_state_check"},
	} {
		t.Run(test.column, func(t *testing.T) {
			err := database.WithTenant(ctx, serviceStateTenantA, func(tx *sql.Tx) error {
				// The column name comes from this table-driven fixture, never
				// from input; the value stays a bound parameter.
				//nolint:gosec // G202: fixed column identifiers from the test table.
				statement := `UPDATE services SET ` + test.column + ` = $2 WHERE id = $1`
				_, execErr := tx.ExecContext(ctx, statement, "integration-service-state-1", test.value)
				return execErr
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != test.constraint {
				t.Fatalf("%s = %q was accepted: %v", test.column, test.value, err)
			}
		})
	}
}

func TestIntegrationServiceStatusStaysIndependentFromHealth(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedServiceStateAggregate(t, database, serviceStateTenantA, "integration-service-state-stack", "integration-service-health-1")

	if err := database.WithTenant(ctx, serviceStateTenantA, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			UPDATE services SET observed_state = 'running', health_state = 'unhealthy',
				status = 'running', revision = revision + 1
			WHERE id = $1
		`, "integration-service-health-1")
		return execErr
	}); err != nil {
		t.Fatalf("record running-but-unhealthy service: %v", err)
	}

	var status, observed, health string
	var revision int64
	if err := database.WithTenant(ctx, serviceStateTenantA, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, observed_state, health_state, revision FROM services WHERE id = $1
		`, "integration-service-health-1").Scan(&status, &observed, &health, &revision)
	}); err != nil {
		t.Fatalf("read service aggregate: %v", err)
	}
	if status != observed || status == health || health != "unhealthy" {
		t.Fatalf("status=%q observed=%q health=%q, want status to track observed only", status, observed, health)
	}
	if revision < 1 {
		t.Fatalf("revision = %d, want a compare-and-swap fence above zero", revision)
	}
}

func TestIntegrationServiceStateTransitionsAreTenantIsolatedAndChecked(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedServiceStateAggregate(t, database, serviceStateTenantA, "integration-service-transition-stack-a", "integration-service-transition-a")
	seedServiceStateAggregate(t, database, serviceStateTenantB, "integration-service-transition-stack-b", "integration-service-transition-b")

	insert := func(tenantID, serviceID, dimension, from, to string) error {
		return database.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, `
				INSERT INTO service_state_transitions (
					tenant_id, service_id, dimension, from_state, to_state, source, observed_at
				) VALUES ($1, $2, $3, NULLIF($4, ''), $5, 'stackkits-inventory', now())
			`, tenantID, serviceID, dimension, from, to)
			return execErr
		})
	}

	if err := insert(serviceStateTenantA, "integration-service-transition-a", "health", "healthy", "unhealthy"); err != nil {
		t.Fatalf("append tenant-a transition: %v", err)
	}
	if err := insert(serviceStateTenantB, "integration-service-transition-b", "observed", "", "running"); err != nil {
		t.Fatalf("append tenant-b transition: %v", err)
	}

	// A no-op transition is not history and must be rejected by the schema.
	if err := insert(serviceStateTenantA, "integration-service-transition-a", "health", "healthy", "healthy"); err == nil {
		t.Fatal("a no-op transition was accepted")
	}
	if err := insert(serviceStateTenantA, "integration-service-transition-a", "connection", "", "connected"); err == nil {
		t.Fatal("a dimension outside the service vocabulary was accepted")
	}
	// Forced RLS must reject a cross-tenant write even from the table owner.
	if err := insert(serviceStateTenantA, "integration-service-transition-b", "observed", "", "stopped"); err == nil {
		t.Fatal("a cross-tenant transition was accepted")
	}

	// The suite connection may be a superuser, which bypasses row security, so
	// the posture itself is asserted from the catalog: enabled, forced, and
	// carrying the app.tenant_id isolation policy.
	var enabled, forced bool
	var policies int
	if err := database.WithTenant(ctx, serviceStateTenantA, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT relrowsecurity, relforcerowsecurity FROM pg_class
			WHERE oid = 'service_state_transitions'::regclass
		`).Scan(&enabled, &forced); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_policies
			WHERE tablename = 'service_state_transitions' AND policyname = 'tenant_isolation'
				AND qual LIKE '%app.tenant_id%' AND with_check LIKE '%app.tenant_id%'
		`).Scan(&policies)
	}); err != nil {
		t.Fatalf("read transition RLS posture: %v", err)
	}
	if !enabled || !forced || policies != 1 {
		t.Fatalf("transition RLS posture: enabled=%v forced=%v policies=%d", enabled, forced, policies)
	}
}
