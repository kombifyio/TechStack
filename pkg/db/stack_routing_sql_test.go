package db

import (
	"strings"
	"testing"
)

func TestStackRoutingMigrationIsTenantScopedAndSupportsLocalTargets(t *testing.T) {
	content := readDBFile(t, "migrations/015_stack_routing_desired.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS stack_routing_desired",
		"CREATE TABLE IF NOT EXISTS stack_routing_idempotency",
		"PRIMARY KEY (tenant_id, stack_id)",
		"PRIMARY KEY (tenant_id, owner_subject_id, idempotency_key)",
		"FOREIGN KEY (tenant_id, stack_id, owner_subject_id)",
		"FOREIGN KEY (tenant_id, server_id, stack_id, owner_subject_id)",
		"FOREIGN KEY (tenant_id, server_id, lease_id)",
		"ALTER TABLE %I ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE %I FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation",
		"lease_id text CHECK (lease_id IS NULL OR lease_id <> '')",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(content, "lease_id text NOT NULL") {
		t.Fatal("local/user-owned routing must not require a fake lease")
	}
}
