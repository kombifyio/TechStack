package db

import (
	"strings"
	"testing"
)

func TestWorkerHeartbeatQueryConflictsOnTenantAndID(t *testing.T) {
	content := readDBFile(t, "queries/queries.sql")
	start := strings.Index(content, "-- name: UpsertWorkerHeartbeat :one")
	if start < 0 {
		t.Fatal("queries are missing UpsertWorkerHeartbeat")
	}
	block := content[start:]
	if next := strings.Index(block[1:], "\n-- name: "); next >= 0 {
		block = block[:next+1]
	}
	if !strings.Contains(block, "ON CONFLICT (tenant_id, id) DO UPDATE") {
		t.Fatal("UpsertWorkerHeartbeat must use the composite worker identity")
	}
}

func TestWorkerTenantIdentityMigrationRekeysEveryWorkerForeignKey(t *testing.T) {
	content := readDBFile(t, "migrations/036_workers_tenant_identity.sql")

	for _, required := range []string{
		"SET LOCAL lock_timeout = '5s'",
		"ALTER TABLE workers NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE workers DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE agent_commands NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE precheck_results NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE nodes NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE servers NO FORCE ROW LEVEL SECURITY",
		"worker tenant identity migration found cross-tenant references",
		"DROP CONSTRAINT IF EXISTS agent_commands_worker_id_fkey",
		"DROP CONSTRAINT IF EXISTS precheck_results_worker_id_fkey",
		"DROP CONSTRAINT IF EXISTS nodes_worker_id_fkey",
		"DROP CONSTRAINT IF EXISTS servers_worker_tenant_fk",
		"DROP CONSTRAINT workers_pkey",
		"ADD CONSTRAINT workers_pkey PRIMARY KEY USING INDEX uq_workers_tenant_id",
		"ADD CONSTRAINT agent_commands_worker_tenant_fk",
		"ADD CONSTRAINT precheck_results_worker_tenant_fk",
		"ADD CONSTRAINT nodes_worker_tenant_fk",
		"ADD CONSTRAINT servers_worker_tenant_fk",
		"FOREIGN KEY (tenant_id, worker_id)",
		"REFERENCES workers (tenant_id, id)",
		"ON DELETE CASCADE NOT VALID",
		"ON DELETE SET NULL (worker_id) NOT VALID",
		"ON DELETE RESTRICT NOT VALID",
		"VALIDATE CONSTRAINT agent_commands_worker_tenant_fk",
		"VALIDATE CONSTRAINT precheck_results_worker_tenant_fk",
		"VALIDATE CONSTRAINT nodes_worker_tenant_fk",
		"VALIDATE CONSTRAINT servers_worker_tenant_fk",
		"ALTER TABLE workers ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE workers FORCE ROW LEVEL SECURITY",
		"ALTER TABLE agent_commands FORCE ROW LEVEL SECURITY",
		"ALTER TABLE precheck_results FORCE ROW LEVEL SECURITY",
		"ALTER TABLE nodes FORCE ROW LEVEL SECURITY",
		"ALTER TABLE servers FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("worker tenant identity migration is missing %q", required)
		}
	}

	if got := strings.Count(content, "REFERENCES workers (tenant_id, id)"); got != 4 {
		t.Fatalf("composite worker foreign keys = %d, want all 4", got)
	}
	if strings.Contains(content, "REFERENCES workers (id)") {
		t.Fatal("migration must not retain a globally-keyed worker foreign key")
	}
}
