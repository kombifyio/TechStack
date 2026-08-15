package db

import (
	"os"
	"strings"
	"testing"
)

func TestStaleManagedCapacityRecoveryMigrationIsBoundedAndReadOnly(t *testing.T) {
	raw, err := os.ReadFile("migrations/054_stale_managed_capacity_recovery.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION provider_control_list_stale_capacity_recovery_candidates(",
		"RETURNS TABLE (",
		"SECURITY DEFINER",
		"STABLE",
		"requested_limit > 101",
		"LIMIT requested_limit",
		"managed_runtime_capacity_reservations",
		"managed_runtime_capacity_release_facts",
		"techstack_vm_leases",
		"runtime_lease_execution_authorities",
		"authority.execution_authority = 'techstack_provider_control'",
		"lease.cancelled_at IS NOT NULL",
		"lease.desired_state = 'absent'",
		"SET search_path TO pg_catalog, public, pg_temp",
		"REVOKE ALL ON FUNCTION provider_control_list_stale_capacity_recovery_candidates(text, text, integer)",
		"FROM PUBLIC",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration is missing %q", required)
		}
	}
	upper := strings.ToUpper(sql)
	for _, forbidden := range []string{"DELETE FROM", "UPDATE MANAGED_RUNTIME_CAPACITY", "INSERT INTO MANAGED_RUNTIME_CAPACITY_RELEASE_FACTS"} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("recovery directory performs forbidden mutation %q", forbidden)
		}
	}
}
