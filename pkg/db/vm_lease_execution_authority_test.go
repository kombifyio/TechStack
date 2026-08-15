package db

import (
	"strings"
	"testing"
)

func TestVMLeaseExecutionAuthorityMigrationBindsImmutableTenantAuthority(t *testing.T) {
	content := readDBFile(t, "migrations/019_vm_lease_execution_authority.sql")
	for _, want := range []string{
		"SET LOCAL lock_timeout = '5s'",
		"LOCK TABLE techstack_vm_lease_enrollment_outbox IN ACCESS EXCLUSIVE MODE",
		"CREATE TABLE IF NOT EXISTS runtime_lease_execution_authorities",
		"PRIMARY KEY (tenant_id, lease_id)",
		"FOREIGN KEY (tenant_id, lease_id)",
		"REFERENCES techstack_vm_leases (tenant_id, id)",
		"execution_authority IN ('legacy_simulate', 'techstack_provider_control')",
		"evidence_resource_generation_id text",
		"evidence_json jsonb",
		"execution_authority = 'legacy_simulate'",
		"CREATE OR REPLACE FUNCTION runtime_lease_execution_authority_reject_legacy_insert()",
		"BEFORE INSERT ON runtime_lease_execution_authorities",
		"CREATE OR REPLACE FUNCTION runtime_lease_execution_authority_reject_mutation()",
		"SET search_path = pg_catalog",
		"BEFORE UPDATE OR DELETE ON runtime_lease_execution_authorities",
		"ALTER TABLE runtime_lease_execution_authorities ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE runtime_lease_execution_authorities FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON runtime_lease_execution_authorities",
		"current_setting('app.tenant_id', true)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("execution-authority migration missing %q", want)
		}
	}

	tableStart := strings.Index(content, "CREATE TABLE IF NOT EXISTS runtime_lease_execution_authorities")
	if tableStart < 0 {
		t.Fatal("execution-authority table definition is missing")
	}
	tableEnd := strings.Index(content[tableStart:], ");")
	if tableEnd < 0 {
		t.Fatal("execution-authority table definition is unterminated")
	}
	tableDefinition := content[tableStart : tableStart+tableEnd]
	if strings.Count(tableDefinition, "PRIMARY KEY") != 1 {
		t.Fatalf("execution-authority table must have exactly one primary key: %s", tableDefinition)
	}
	if strings.Contains(tableDefinition, "PRIMARY KEY (tenant_id, lease_id,") ||
		strings.Contains(tableDefinition, "PRIMARY KEY (tenant_id, lease_id, resource_generation_id)") {
		t.Fatalf("execution-authority identity must not include a resource generation: %s", tableDefinition)
	}
}

func TestVMLeaseExecutionAuthorityMigrationRequiresDrainedNonRollingCutoverAndFencesLegacyState(t *testing.T) {
	content := readDBFile(t, "migrations/019_vm_lease_execution_authority.sql")
	for _, want := range []string{
		"status IN ('pending', 'retrying')",
		"updated_at > clock_timestamp() - INTERVAL '15 minutes'",
		"next_attempt_at > clock_timestamp()",
		"CREATE OR REPLACE FUNCTION techstack_vm_lease_enrollment_outbox_reject_mutation()",
		"BEFORE INSERT OR UPDATE OR DELETE ON techstack_vm_lease_enrollment_outbox",
		"CREATE OR REPLACE FUNCTION techstack_vm_lease_reject_legacy_execution_metadata()",
		"metadata.key ~ '^(executor_|runtime_enrollment_)'",
		"runtime_execution_authority_provenance",
		"new_legacy_metadata IS DISTINCT FROM old_legacy_metadata",
		"NEW.engine_vm_id IS DISTINCT FROM OLD.engine_vm_id",
		"evidence_resource_generation_id",
		"evidence_json",
		"BEFORE INSERT OR UPDATE OF lease_json, engine_vm_id ON techstack_vm_leases",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("execution-authority cutover fence missing %q", want)
		}
	}
	if strings.Contains(content, "pg_sleep") {
		t.Fatal("execution-authority cutover must fail fast instead of sleeping inside the migration")
	}

	lockAt := strings.Index(content, "LOCK TABLE techstack_vm_lease_enrollment_outbox IN ACCESS EXCLUSIVE MODE")
	drainAt := strings.Index(content, "legacy enrollment cutover requires a quiet window")
	backfillAt := strings.Index(content, "INSERT INTO runtime_lease_execution_authorities")
	quarantineAt := strings.Index(content, "UPDATE techstack_vm_lease_enrollment_outbox AS outbox")
	outboxFenceAt := strings.Index(content, "CREATE OR REPLACE FUNCTION techstack_vm_lease_enrollment_outbox_reject_mutation()")
	metadataFenceAt := strings.Index(content, "CREATE OR REPLACE FUNCTION techstack_vm_lease_reject_legacy_execution_metadata()")
	if lockAt < 0 || drainAt <= lockAt || backfillAt <= drainAt || quarantineAt <= backfillAt ||
		outboxFenceAt <= quarantineAt || metadataFenceAt <= outboxFenceAt {
		t.Fatalf("execution-authority cutover order is unsafe: lock=%d drain=%d backfill=%d quarantine=%d outbox-fence=%d metadata-fence=%d",
			lockAt, drainAt, backfillAt, quarantineAt, outboxFenceAt, metadataFenceAt)
	}
}

func TestVMLeaseExecutionAuthorityMigrationUsesOnlyExactCustodyEvidence(t *testing.T) {
	content := readDBFile(t, "migrations/019_vm_lease_execution_authority.sql")
	backfillStart := strings.Index(content, "WITH exact_evidence AS (")
	if backfillStart < 0 {
		t.Fatal("execution-authority exact-evidence backfill is missing")
	}
	backfillEnd := strings.Index(content[backfillStart:], "ON CONFLICT (tenant_id, lease_id) DO NOTHING;")
	if backfillEnd < 0 {
		t.Fatal("execution-authority backfill has no tenant+lease conflict boundary")
	}
	backfill := content[backfillStart : backfillStart+backfillEnd]

	for _, want := range []string{
		"outbox.resource_generation_id = lease.lease_json->'metadata'->>'resource_generation_id'",
		"executor_claim_status' IN ('active', 'blocked')",
		"executor_claim_resource_generation_id'",
		"executor_claim_operation_id'",
		"executor_claim_operation'",
		"executor_last_resource_generation_id'",
		"executor_last_operation_id'",
		"executor_last_operation'",
		"executor_last_status' = 'succeeded'",
		"executor_last_status' = 'failed'",
		"executor_last_provider_resource_ref'",
		"NULLIF(BTRIM(lease.engine_vm_id), '') IS NOT NULL",
		"lease.lease_json->'resource'->>'engine_vm_id'",
		"outbox.request_json->>'engine_vm_id'",
	} {
		if !strings.Contains(backfill, want) {
			t.Fatalf("execution-authority evidence backfill missing %q", want)
		}
	}
	const operations = "IN ('provision', 'reconcile', 'decommission')"
	if got := strings.Count(backfill, operations); got != 3 {
		t.Fatalf("execution-authority backfill operation bindings = %d, want exact succeeded-result, failed-result, and claim bindings", got)
	}
	for _, want := range []string{
		"FROM exact_evidence AS evidence",
		"'evidence_kind', evidence.evidence_kind",
		"'operation', evidence.operation",
		"'operation_id', evidence.operation_id",
		"'provider_resource_ref', evidence.provider_resource_ref",
	} {
		if !strings.Contains(backfill, want) {
			t.Fatalf("execution-authority receipt is not derived from its exact evidence branch: missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"executor_ready",
		"runtime_enrollment_status",
		"outbox.status",
		"outbox.attempts",
		"outbox.next_attempt_at",
		"outbox.created_at",
		"outbox.updated_at",
		"= 'denied'",
		"IN ('succeeded', 'failed', 'denied')",
		"IN ('plan', 'provision', 'observe', 'reconcile', 'decommission')",
	} {
		if strings.Contains(backfill, forbidden) {
			t.Fatalf("execution-authority backfill must not infer custody from %q", forbidden)
		}
	}

	quarantineStart := strings.Index(content, "UPDATE techstack_vm_lease_enrollment_outbox AS outbox")
	quarantineEnd := strings.Index(content, "CREATE OR REPLACE FUNCTION techstack_vm_lease_enrollment_outbox_reject_mutation()")
	if quarantineStart < 0 || quarantineEnd <= quarantineStart {
		t.Fatal("execution-authority quarantine statement is missing")
	}
	quarantine := content[quarantineStart:quarantineEnd]
	for _, want := range []string{
		"SET status = 'failed'",
		"last_error",
		"execution authority",
		"WHERE outbox.status IN ('pending', 'retrying')",
	} {
		if !strings.Contains(quarantine, want) {
			t.Fatalf("execution-authority quarantine missing %q", want)
		}
	}
	for _, forbidden := range []string{"NOT EXISTS"} {
		if strings.Contains(quarantine, forbidden) {
			t.Fatalf("historical legacy custody must not exempt executable outbox work via %q", forbidden)
		}
	}
}
