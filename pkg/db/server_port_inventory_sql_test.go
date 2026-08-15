package db

import (
	"strings"
	"testing"
)

func TestServerPortInventoryMigrationDefinesGenerationFencedTenantAuthority(t *testing.T) {
	content := readDBFile(t, "migrations/064_server_port_inventory.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS server_port_reservations",
		"CREATE TABLE IF NOT EXISTS server_port_reservation_claims",
		"CREATE TABLE IF NOT EXISTS server_port_claim_generations",
		"claim_set_digest text NOT NULL",
		"server_generation bigint NOT NULL CHECK (server_generation > 0)",
		"resolved_plan_hash text NOT NULL",
		"requirement_id text NOT NULL",
		"state text NOT NULL DEFAULT 'pending'",
		"CHECK (state IN ('pending', 'mutating', 'active', 'uncertain', 'released'))",
		"UNIQUE (tenant_id, server_id, server_generation, stack_id, resolved_plan_hash, requirement_id)",
		"CREATE TABLE IF NOT EXISTS server_port_runtime_observations",
		"listeners_complete boolean NOT NULL",
		"exposures_complete boolean NOT NULL",
		"CREATE TABLE IF NOT EXISTS server_port_runtime_facts",
		"fact_kind text NOT NULL CHECK (fact_kind IN ('observed', 'exposed'))",
		"inventory_revision bigint NOT NULL CHECK (inventory_revision > 0)",
		"source_epoch text NOT NULL",
		"source_sequence bigint NOT NULL CHECK (source_sequence > 0)",
		"ALTER TABLE server_port_reservations ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_reservations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_reservation_claims ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_reservation_claims FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_claim_generations ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_claim_generations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_runtime_observations ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_runtime_observations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_runtime_facts ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE server_port_runtime_facts FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
		"FOREIGN KEY (tenant_id, server_id)",
		"FOREIGN KEY (tenant_id, observation_id, server_id, server_generation)",
		"FOREIGN KEY (tenant_id, server_id, server_generation, stack_id, resolved_plan_hash)",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("port inventory migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"provider_id",
		"ionos",
		"centron",
		"docker",
		"target_port",
		"route_url",
	} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Errorf("port inventory migration must remain provider/runtime neutral; found %q", forbidden)
		}
	}
}

func TestServerPortInventoryExposureVocabularyMatchesCompilerContract(t *testing.T) {
	content := readDBFile(t, "migrations/065_align_server_port_exposure_contract.sql")
	for _, required := range []string{
		"server_port_reservation_claims_exposure_check",
		"server_port_runtime_facts_exposure_check",
		"CHECK (exposure IN ('local', 'remote-private', 'public'))",
		"WHEN 'loopback' THEN 'local'",
		"WHEN 'private' THEN 'remote-private'",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("port exposure alignment migration missing %q", required)
		}
	}
}
