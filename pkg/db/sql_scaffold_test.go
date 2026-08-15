package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLCConfigTargetsPostgreSQL(t *testing.T) {
	content := readDBFile(t, "sqlc.yaml")

	if !strings.Contains(content, `engine: "postgresql"`) {
		t.Fatal("sqlc.yaml should target the PostgreSQL engine")
	}
	if strings.Contains(content, `engine: "sqlite"`) {
		t.Fatal("sqlc.yaml should not keep the dormant SQLite engine")
	}
}

func TestCanonicalNodeCanExistBeforeStackAssignment(t *testing.T) {
	content := readDBFile(t, "migrations/051_nodes_optional_stack_assignment.sql")
	for _, want := range []string{
		"ALTER TABLE nodes",
		"ALTER COLUMN stack_id DROP NOT NULL",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("pre-assignment node migration missing %q", want)
		}
	}
}

func TestActivityScopeMigrationBackfillsCanonicalKeys(t *testing.T) {
	content := readDBFile(t, "migrations/077_activity_scope_keys.sql")
	for _, want := range []string{
		"SET LOCAL lock_timeout = '5s'",
		"runtime_scope_key text",
		"server_scope_key text",
		"service_scope_key text",
		"correlation_id text",
		"details_json->>'server_id'",
		"details_json->>'service_id'",
		"'managed_target:'",
		"'server:'",
		"idx_activity_log_tenant_server_created",
		"idx_activity_log_tenant_service_created",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("activity scope migration missing %q", want)
		}
	}
}

func TestRuntimeTargetMigrationKeepsServerAndManagedWorkloadShapesDisjoint(t *testing.T) {
	content := readDBFile(t, "migrations/076_runtime_targets.sql")
	for _, want := range []string{
		"environment_class text NOT NULL DEFAULT 'unknown'",
		"offering text",
		"provider_target_ref text",
		"availability_owner text",
		"operations_owner text",
		"target_kind text NOT NULL DEFAULT 'unknown'",
		"managed_target_ref text",
		"provider_receipt_ref text",
		"sla_policy_ref text",
		"backup_policy_ref text",
		"offering = 'external_vps'",
		"offering = 'managed_vps'",
		"metadata_json->>'engine_vm_id'",
		"target_kind = 'managed_workload'",
		"server_id IS NULL",
		"services_runtime_target_shape",
		"servers_runtime_target_shape",
		"ALTER TABLE servers FORCE ROW LEVEL SECURITY",
		"ALTER TABLE services FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("runtime target migration missing %q", want)
		}
	}
}

func TestProviderExecutionClaimSchemaBindsOneLiveClaimToCurrentHead(t *testing.T) {
	content := readDBFile(t, "migrations/014_provider_execution_claims.sql")
	required := []string{
		"CREATE TABLE IF NOT EXISTS provider_operation_execution_claims",
		"head_sequence bigint NOT NULL CHECK (head_sequence > 0)",
		"head_receipt_digest text NOT NULL",
		"claim_token_digest text NOT NULL CHECK (claim_token_digest ~ '^[0-9a-f]{64}$')",
		"claim_owner text NOT NULL CHECK (claim_owner <> '')",
		"state IN ('active', 'released', 'consumed')",
		"lease_expires_at > claimed_at",
		"idx_provider_execution_claims_one_active",
		"WHERE state = 'active'",
		"CREATE OR REPLACE FUNCTION provider_execution_claim_update()",
		"provider execution claim identity is immutable",
		"invalid provider execution claim lifecycle transition",
		"CREATE TRIGGER provider_execution_claims_update",
		"CREATE TRIGGER provider_execution_claims_reject_delete",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Fatalf("provider execution claim schema missing %q", want)
		}
	}
}

func TestClientPairingSchemaEnforcesHashExpiryAndTenantIsolation(t *testing.T) {
	content := readDBFile(t, "migrations/010_client_pairing_codes.sql")
	required := []string{
		"CREATE TABLE IF NOT EXISTS client_pairing_codes",
		"code_hash text NOT NULL",
		"expires_at <= issued_at + interval '10 minutes'",
		"consumed_at IS NULL",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Fatalf("client pairing schema missing %q", want)
		}
	}
	for _, forbidden := range []string{"one_time_code", "raw_code", "session_token"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("client pairing schema must not persist %q", forbidden)
		}
	}
}

func TestVMLeaseResourceGenerationMigrationBackfillsAndPersistsProof(t *testing.T) {
	content := readDBFile(t, "migrations/016_vm_lease_resource_generation.sql")
	for _, want := range []string{
		"SET LOCAL lock_timeout = '5s'",
		"ALTER TABLE techstack_vm_leases NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE techstack_vm_leases DISABLE ROW LEVEL SECURITY",
		"UPDATE techstack_vm_leases",
		"jsonb_build_object('resource_generation_id', gen_random_uuid()::text)",
		"ADD CONSTRAINT techstack_vm_leases_resource_generation_uuid_check",
		"idx_techstack_vm_leases_resource_generation",
		"idx_techstack_vm_leases_tenant_id",
		"ALTER TABLE techstack_vm_leases ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE techstack_vm_leases FORCE ROW LEVEL SECURITY",
		"ADD COLUMN IF NOT EXISTS resource_generation_digest text",
		"resource_generation_digest ~ '^[0-9a-f]{64}$'",
		"OR resource_generation_digest IS NOT NULL",
		") NOT VALID",
		"ADD CONSTRAINT techstack_vm_lease_operation_journal_tenant_lease_fkey",
		"FOREIGN KEY (tenant_id, lease_id)",
		"REFERENCES techstack_vm_leases (tenant_id, id)",
		"ALTER TABLE techstack_vm_lease_operation_journal ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE techstack_vm_lease_operation_journal FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON techstack_vm_lease_operation_journal",
		"CREATE OR REPLACE FUNCTION techstack_vm_lease_operation_journal_reject_mutation()",
		"SET search_path = pg_catalog",
		"BEFORE UPDATE OR DELETE ON techstack_vm_lease_operation_journal",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("resource generation migration missing %q", want)
		}
	}
	if strings.Contains(content, "WHERE NULLIF(BTRIM(lease_json->'metadata'->>'resource_generation_id'), '') IS NULL") {
		t.Fatal("resource generation migration must replace every legacy caller-selected generation")
	}
}

func TestQueriesUsePostgreSQLPlaceholdersAndRepresentativeNames(t *testing.T) {
	content := readDBFile(t, "queries/queries.sql")

	required := []string{
		"-- name: GetStack :one",
		"-- name: ListPendingJobs :many",
		"-- name: UpsertWorkerHeartbeat :one",
		"-- name: EnqueueAgentCommand :one",
		"-- name: CreateDriftResult :one",
		"-- name: CreatePrecheckResult :one",
		"-- name: UpsertFeatureRollout :one",
		"-- name: UpsertIdentitySubjectLink :one",
		"-- name: UpsertWalletItem :one",
		"-- name: CreateAuditEvent :one",
		"-- name: UpsertTofuState :one",
		"$1",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Fatalf("queries missing %q", want)
		}
	}

	if strings.Contains(content, "?") {
		t.Fatal("queries should use PostgreSQL positional placeholders, not SQLite question marks")
	}
}

func readDBFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	// Migration files use LF in Git, but core.autocrlf may materialize CRLF in
	// a Windows worktree. Keep exact SQL assertions platform-independent.
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func readDBMigrationFiles(t *testing.T) string {
	t.Helper()

	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	var combined strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		combined.Write(content)
		combined.WriteString("\n")
	}

	return combined.String()
}
