package db

import (
	"strings"
	"testing"
)

func TestTerminalServerCapacityRecoveryReentersNativeCustodyWithoutReleasing(t *testing.T) {
	content := readDBFile(t, "migrations/078_terminal_server_capacity_recovery.sql")
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION provider_control_list_stale_capacity_recovery_candidates(",
		"managed_runtime_capacity_reservations AS reservation",
		"runtime_lease_execution_authorities AS authority",
		"authority.execution_authority = 'techstack_provider_control'",
		"server.id = lease.server_id",
		"server.lease_id = lease.id",
		"server.desired_state = 'absent'",
		"server.lifecycle_state IN ('decommissioning', 'decommissioned')",
		"server.decommissioned_at IS NOT NULL",
		"NOT EXISTS (",
		"managed_runtime_capacity_release_facts AS release",
		"LIMIT requested_limit",
		"SECURITY DEFINER",
		"STABLE",
		"REVOKE ALL ON FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("terminal server recovery lost required fence %q", required)
		}
	}

	upper := strings.ToUpper(content)
	for _, forbidden := range []string{
		"DELETE FROM",
		"UPDATE TECHSTACK_VM_LEASES",
		"UPDATE SERVERS",
		"INSERT INTO MANAGED_RUNTIME_CAPACITY_RELEASE_FACTS",
	} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("terminal server recovery directory performs forbidden mutation %q", forbidden)
		}
	}
}
