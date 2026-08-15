package db

import (
	"strings"
	"testing"
)

// TestServerRegistryObservationSweepMigrationPosture locks the wake-up
// directory pattern: the sweeper may learn tenant IDs and scheduling
// timestamps cross-tenant, but every payload read or write stays behind the
// tenant-scoped FORCE RLS boundary.
func TestServerRegistryObservationSweepMigrationPosture(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/072_server_registry_observation_sweep.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS server_registry_sweep_tenants",
		"CREATE TABLE IF NOT EXISTS server_registry_outbox_prune_tenants",
		"REVOKE ALL ON TABLE server_registry_sweep_tenants FROM PUBLIC",
		"REVOKE ALL ON TABLE server_registry_outbox_prune_tenants FROM PUBLIC",
		"CREATE OR REPLACE FUNCTION server_registry_wake_sweep_tenant()",
		"CREATE OR REPLACE FUNCTION server_registry_wake_outbox_prune_tenant()",
		"SECURITY INVOKER",
		"AND NEW.connection_state IN ('connected', 'degraded', 'stale')",
		"AND NEW.lifecycle_state NOT IN ('decommissioning', 'decommissioned')",
		"ALTER TABLE servers NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE servers FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_registry_outbox NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_registry_outbox FORCE ROW LEVEL SECURITY",
		"idx_server_registry_outbox_tenant_created",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing posture invariant %q", required)
		}
	}
	if strings.Contains(source, "SECURITY DEFINER") {
		t.Fatal("sweep wake-up directories must not need definer rights; payload reads stay tenant-scoped")
	}
	for _, forbidden := range []string{"payload_json", "metadata_json", "channels_json", "evidence_json"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("sweep directory migration must stay secret-free, found %q", forbidden)
		}
	}
}
