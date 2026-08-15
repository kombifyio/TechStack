package db

import (
	"strings"
	"testing"
)

func TestServerRegistryEventMigrationAddsCASAndSourceFences(t *testing.T) {
	content := readDBFile(t, "migrations/020_server_registry_events.sql")
	for _, required := range []string{
		"revision bigint NOT NULL DEFAULT 1",
		"generation bigint NOT NULL DEFAULT 1",
		"source_epoch text",
		"source_sequence bigint NOT NULL DEFAULT 0",
		"servers_source_checkpoint_valid",
		"idx_servers_guard_checkpoint",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}

func TestServerRegistryOutboxMigrationNormalizesIntentAndClaims(t *testing.T) {
	content := readDBFile(t, "migrations/022_server_registry_desired_outbox.sql")
	for _, required := range []string{
		"WHEN 'active' THEN 'running'",
		"WHEN 'decommissioned' THEN 'absent'",
		"CHECK (desired_state IN ('running', 'stopped', 'absent'))",
		"servers_decommission_tombstone_check",
		"lifecycle_reason_code text",
		"desired_reason_code text",
		"connection_reason_code text",
		"health_reason_code text",
		"lifecycle_changed_at timestamptz",
		"desired_changed_at timestamptz",
		"health_changed_at timestamptz",
		"servers_dimension_reason_codes_bounded",
		"t.dimension = 'lifecycle'",
		"t.dimension = 'connection'",
		"(lifecycle_state = 'decommissioned') = (decommissioned_at IS NOT NULL)",
		"CHECK (dimension IN ('lifecycle', 'desired', 'connection', 'health'))",
		"CREATE TABLE IF NOT EXISTS server_registry_outbox",
		"FOREIGN KEY (tenant_id, server_id)",
		"UNIQUE (tenant_id, server_id, aggregate_revision)",
		"claim_token_digest bytea",
		"octet_length(claim_token_digest) = 32",
		"ALTER TABLE server_registry_outbox FORCE ROW LEVEL SECURITY",
		"ALTER TABLE servers NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE servers DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE servers ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE servers FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
	dropLegacyConstraint := strings.Index(
		content,
		"ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_desired_state_check",
	)
	normalizeDesiredState := strings.Index(content, "SET desired_state = CASE desired_state")
	if dropLegacyConstraint < 0 || normalizeDesiredState < 0 || dropLegacyConstraint > normalizeDesiredState {
		t.Fatal("legacy desired-state constraint must be removed before normalized values are written")
	}
	enableRLS := strings.LastIndex(content, "ALTER TABLE servers ENABLE ROW LEVEL SECURITY")
	forceRLS := strings.LastIndex(content, "ALTER TABLE servers FORCE ROW LEVEL SECURITY")
	if enableRLS < normalizeDesiredState || forceRLS < enableRLS {
		t.Fatal("server tenant RLS must be restored after the historical backfill")
	}
}

func TestServerRegistryTenantBindingMigrationUsesCompositeKeys(t *testing.T) {
	content := readDBFile(t, "migrations/024_server_registry_tenant_bindings.sql")
	for _, required := range []string{
		"FOREIGN KEY (tenant_id, instance_id)",
		"FOREIGN KEY (tenant_id, stack_id)",
		"FOREIGN KEY (tenant_id, worker_id)",
		"FOREIGN KEY (tenant_id, node_id)",
		"FOREIGN KEY (tenant_id, lease_id)",
		"REFERENCES servers (tenant_id, id)",
		"uq_servers_tenant_worker_binding",
		"uq_servers_tenant_node_binding",
		"uq_servers_tenant_lease_binding",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}

func TestServerProviderResourceBindingsAreTenantAndGenerationBound(t *testing.T) {
	content := readDBFile(t, "migrations/025_server_provider_resource_bindings.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS server_provider_resource_bindings",
		"server_generation bigint NOT NULL",
		"FOREIGN KEY (tenant_id, server_id)",
		"FOREIGN KEY (tenant_id, lease_id)",
		"FOREIGN KEY (tenant_id, operation_id, lease_id)",
		"FOREIGN KEY (tenant_id, operation_id, binding_id)",
		"UNIQUE (tenant_id, operation_id, binding_id)",
		"generation = NEW.server_generation",
		"lease_id = NEW.lease_id",
		"project_server_provider_resource_bindings",
		"AFTER INSERT OR UPDATE OF lease_id, generation ON servers",
		"server provider resource bindings are immutable",
		"pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp'",
		"SET search_path FROM CURRENT",
		"FROM servers",
		"INSERT INTO server_provider_resource_bindings",
		"FROM provider_operations AS operation",
		"JOIN provider_operation_resources AS resource",
		"ALTER TABLE server_provider_resource_bindings FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("provider-resource binding migration is missing %q", required)
		}
	}
	functionCount := strings.Count(content, "CREATE OR REPLACE FUNCTION ")
	if got := strings.Count(content, "SET search_path FROM CURRENT"); got != functionCount {
		t.Fatalf("hardened provider-binding function search paths = %d, want %d", got, functionCount)
	}
	for _, forbidden := range []string{
		"SET LOCAL search_path = public",
		"public.servers",
		"public.server_provider_resource_bindings",
		"public.provider_operations",
		"public.provider_operation_resources",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("provider-resource binding migration hard-codes a schema %q", forbidden)
		}
	}
}

func TestServerGuardEpochHistoryMigrationFencesSupersededProcesses(t *testing.T) {
	content := readDBFile(t, "migrations/023_server_guard_epoch_history.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS server_guard_source_epochs",
		"PRIMARY KEY (tenant_id, server_id, generation, source_id, source_epoch)",
		"FOREIGN KEY (tenant_id, server_id)",
		"INSERT INTO server_guard_source_epochs",
		"source_authority = 'guard'",
		"ALTER TABLE server_guard_source_epochs FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}
