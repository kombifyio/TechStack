package db

import (
	"strings"
	"testing"
)

func TestTeardownSafeProvisionCandidateAdoptionRemainsNarrow(t *testing.T) {
	content := readDBFile(t, "migrations/058_teardown_safe_provision_candidate_adoption.sql")
	for _, required := range []string{
		"provider_provision_discovery_validate_insert()",
		"provider_provision_resolution_validate_insert()",
		"provider_execution_immutable_update()",
		"AND NOT operator_adoption",
		"OLD.phase = 'accepted'",
		"NEW.phase = 'resources_bound'",
		"live_cancelled_at IS NOT NULL",
		"teardown_requested",
		"did not match its expected predecessor",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("teardown-safe adoption migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"DROP TRIGGER",
		"DISABLE TRIGGER",
		"DELETE FROM provider_provision_dispatch_guards",
		"UPDATE provider_provision_dispatch_guards",
	} {
		if strings.Contains(strings.ToUpper(content), strings.ToUpper(forbidden)) {
			t.Fatalf("teardown-safe adoption migration contains forbidden widening %q", forbidden)
		}
	}
}
