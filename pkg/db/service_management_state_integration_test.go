package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const serviceManagementTenant = "integration-service-management-tenant"

// seedLegacyServiceRow inserts a row the way it existed BEFORE migration 074:
// no management_state, a free-text source, and the legacy status/type markers
// the deleted read-time derivations used to inspect.
func seedLegacyServiceRow(t *testing.T, database *DB, serviceID, source, status string, metadata string) {
	t.Helper()
	ctx := context.Background()
	if err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stacks (id, tenant_id, name) VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
		`, "integration-service-management-stack", serviceManagementTenant, "management"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO services (
				id, tenant_id, stack_id, service_key, name, status, source,
				desired_state, observed_state, health_state, metadata_json
			) VALUES ($1, $2, 'integration-service-management-stack', 'vaultwarden',
				'Vaultwarden', $3, $4, 'running', 'running', 'healthy', $5::jsonb)
			ON CONFLICT (id) DO NOTHING
		`, serviceID, serviceManagementTenant, status, source, metadata)
		return err
	}); err != nil {
		t.Fatalf("seed legacy service %s: %v", serviceID, err)
	}
}

func readManagementState(t *testing.T, database *DB, serviceID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var management, source string
	if err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT management_state, source FROM services WHERE id = $1`, serviceID,
		).Scan(&management, &source)
	}); err != nil {
		t.Fatalf("read management state of %s: %v", serviceID, err)
	}
	return management, source
}

// rewindServiceManagementMigration removes the 074 artifacts so legacy rows can
// be seeded exactly as they existed before it, and the real migration file can
// then be replayed against them.
func rewindServiceManagementMigration(t *testing.T, database *DB) {
	t.Helper()
	ctx := context.Background()
	if err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			ALTER TABLE services DROP CONSTRAINT IF EXISTS services_source_check;
			ALTER TABLE services DROP CONSTRAINT IF EXISTS services_management_state_check;
			ALTER TABLE services DROP COLUMN IF EXISTS management_state;
		`)
		return err
	}); err != nil {
		t.Fatalf("rewind 074: %v", err)
	}
}

func replayServiceManagementMigration(t *testing.T, database *DB) {
	t.Helper()
	ctx := context.Background()
	migration := readDBFile(t, serviceManagementStateMigration)
	if err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, migration)
		return err
	}); err != nil {
		t.Fatalf("replay 074: %v", err)
	}
}

// The backfill must reproduce the historical read-time derivation exactly, so
// no stored row changes meaning when the column lands. This replays the real
// migration file against rows seeded in their pre-074 shape.
func TestIntegrationServiceManagementBackfillPreservesRowMeaning(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rewindServiceManagementMigration(t, database)

	tests := []struct {
		name, serviceID, source, status, metadata string
		wantManagement, wantSource                string
	}{
		{
			name: "observed source stays observed", serviceID: "mgmt-observed-source",
			source: "observed", status: "running", metadata: `{}`,
			wantManagement: "observed", wantSource: "observed",
		},
		{
			name: "legacy observed status stays observed", serviceID: "mgmt-observed-status",
			source: "stackkit_outputs", status: "observed", metadata: `{}`,
			wantManagement: "observed", wantSource: "stackkit_outputs",
		},
		{
			name: "hand imported custom type stays observed", serviceID: "mgmt-custom-type",
			source: "stackkit_outputs", status: "running", metadata: `{"type":"custom"}`,
			wantManagement: "observed", wantSource: "stackkit_outputs",
		},
		{
			name: "stackkits inventory is managed", serviceID: "mgmt-inventory",
			source: "stackkits-inventory", status: "running", metadata: `{}`,
			wantManagement: "managed", wantSource: "stackkits-inventory",
		},
		{
			name: "legacy backfill provenance is managed", serviceID: "mgmt-legacy-backfill",
			source: "legacy-registry-backfill", status: "running", metadata: `{}`,
			wantManagement: "managed", wantSource: "legacy-registry-backfill",
		},
		{
			// An unrecognized provenance derived `managed` before 074. Folding
			// the source must not flip that answer.
			name: "unknown provenance keeps its managed meaning", serviceID: "mgmt-unknown-source",
			source: "some-retired-pipeline", status: "running", metadata: `{}`,
			wantManagement: "managed", wantSource: "observed",
		},
		{
			name: "case and padding are folded", serviceID: "mgmt-padded-source",
			source: "  StackKits-Inventory ", status: "running", metadata: `{}`,
			wantManagement: "managed", wantSource: "stackkits-inventory",
		},
	}
	for _, test := range tests {
		seedLegacyServiceRow(t, database, test.serviceID, test.source, test.status, test.metadata)
	}

	replayServiceManagementMigration(t, database)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			management, source := readManagementState(t, database, test.serviceID)
			if management != test.wantManagement || source != test.wantSource {
				t.Fatalf("management=%q source=%q, want management=%q source=%q",
					management, source, test.wantManagement, test.wantSource)
			}
		})
	}
}

func TestIntegrationServiceManagementAndSourceRejectValuesOutsideTheVocabulary(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedLegacyServiceRow(t, database, "mgmt-vocabulary", "stackkits-inventory", "running", `{}`)

	for _, test := range []struct {
		column, value, constraint string
	}{
		{column: "management_state", value: "adopted", constraint: "services_management_state_check"},
		{column: "management_state", value: "", constraint: "services_management_state_check"},
		{column: "source", value: "some-retired-pipeline", constraint: "services_source_check"},
		{column: "source", value: "verified-apply-evidence", constraint: "services_source_check"},
	} {
		t.Run(test.column+"_"+test.value, func(t *testing.T) {
			err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
				// The column name comes from this table-driven fixture, never
				// from input; the value stays a bound parameter.
				//nolint:gosec // G202: fixed column identifiers from the test table.
				statement := `UPDATE services SET ` + test.column + ` = $2 WHERE id = $1`
				_, execErr := tx.ExecContext(ctx, statement, "mgmt-vocabulary", test.value)
				return execErr
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != test.constraint {
				t.Fatalf("%s = %q was accepted: %v", test.column, test.value, err)
			}
		})
	}
}

// StackKits' evidence-provenance vocabulary is a different axis and must never
// be accepted into the inventory source column.
func TestIntegrationServiceSourceStaysSeparateFromStackKitsEvidenceProvenance(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedLegacyServiceRow(t, database, "mgmt-evidence-axis", "stackkits-inventory", "running", `{}`)

	for _, evidence := range []string{"local-runtime", "standard-process", "verified-apply-evidence"} {
		err := database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx,
				`UPDATE services SET source = $2 WHERE id = $1`, "mgmt-evidence-axis", evidence)
			return execErr
		})
		if err == nil {
			t.Fatalf("evidence provenance %q was accepted as an inventory source", evidence)
		}
	}
}

func TestIntegrationServiceStateTransitionsAcceptTheManagementDimension(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedLegacyServiceRow(t, database, "mgmt-transition", "stackkits-inventory", "running", `{}`)

	insert := func(dimension, from, to string) error {
		return database.WithTenant(ctx, serviceManagementTenant, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, `
				INSERT INTO service_state_transitions (
					tenant_id, service_id, dimension, from_state, to_state, source, observed_at
				) VALUES ($1, $2, $3, NULLIF($4, ''), $5, 'stackkits-inventory', now())
			`, serviceManagementTenant, "mgmt-transition", dimension, from, to)
			return execErr
		})
	}

	if err := insert("management", "observed", "managed"); err != nil {
		t.Fatalf("append management transition: %v", err)
	}
	// The timeline stays change-only and closed over the four dimensions.
	if err := insert("management", "managed", "managed"); err == nil {
		t.Fatal("a no-op management transition was accepted")
	}
	if err := insert("ownership", "", "managed"); err == nil {
		t.Fatal("a dimension outside the service vocabulary was accepted")
	}
}
