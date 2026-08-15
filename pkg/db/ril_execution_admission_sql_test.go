package db

import (
	"strings"
	"testing"
)

func TestRILExecutionAdmissionMigrationBindsCardAndOpaqueLedgerDigest(t *testing.T) {
	content := readDBFile(t, "migrations/047_ril_execution_admission.sql")
	for _, required := range []string{
		"admission_inventory_revision bigint",
		"admission_server_revision bigint",
		"admission_server_generation bigint",
		"admission_lease_id text",
		"admission_lease_revision bigint",
		"admission_resource_generation_id uuid",
		"execution_admission_digest text",
		"ril_action_cards_execution_admission_complete",
		"ril_action_cards_execution_status_requires_admission",
		"status NOT IN ('executing', 'verifying', 'completed', 'failed')",
		"ril_action_execution_ledger_admission_digest_valid",
		"execution_admission_digest ~ '^sha256:[a-f0-9]{64}$'",
		"execution_admission_digest IS NOT NULL",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"provider_id", "credential", "secret", "endpoint", "command_output", "raw_log"} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Fatalf("execution admission persistence exposes %q", forbidden)
		}
	}
}
