package db

import (
	"strings"
	"testing"
)

func TestTerminalTombstoneContinuationMigrationIsReadOnlyAndProvisionBound(t *testing.T) {
	content := readDBFile(t, "migrations/082_terminal_tombstone_readonly_continuation.sql")
	for _, required := range []string{
		"provider_execution_claim_credential_guard()",
		"provider_execution_claim_runtime_generation_guard()",
		"provider_operation_head_update_guard()",
		"command_operation = ''provision''",
		"command_phase = ''resources_bound''",
		"NEW.claim_access = ''read_only''",
		"operation_kind = ''provision''",
		"operation_phase = ''resources_bound''",
		"OLD.operation = ''provision''",
		"OLD.phase = ''resources_bound''",
		"claimed_result_append",
		"[[:space:]]+",
		"regexp_replace(",
		"updated_definition IS NOT DISTINCT FROM function_definition",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("terminal tombstone continuation migration missing %q", required)
		}
	}
}
