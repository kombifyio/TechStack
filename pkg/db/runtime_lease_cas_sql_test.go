package db

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeLeaseCASMigrationKeepsSingleGenerationAuthority(t *testing.T) {
	payload, err := os.ReadFile("migrations/026_runtime_lease_cas.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(payload)
	for _, required := range []string{
		"resource_generation_id uuid",
		"lease_revision bigint",
		"runtime_lease_generation_projection_guard",
		"runtime lease contains conflicting generation projections",
		"top_level_generation IS DISTINCT FROM metadata_generation",
		"NEW.resource_generation_id::text IS DISTINCT FROM json_generation",
		"runtime_lease_idempotency_records",
		"PRIMARY KEY (tenant_id, operation_scope, idempotency_key)",
		"octet_length(request_digest) = 32",
		"runtime lease idempotency records are immutable",
		"FOREIGN KEY (tenant_id, server_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"gen_random_uuid()", "legacy_simulate'::text", "provisioning-executor/v1",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration introduced a second or legacy authority via %q", forbidden)
		}
	}
}
