package db

import (
	"strings"
	"testing"
)

func TestLegacySimulateCapacityRetirementRequiresExactFakeOnlyCustody(t *testing.T) {
	content := readDBFile(t, "migrations/055_legacy_simulate_capacity_retirements.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS managed_runtime_capacity_quarantine_retirements",
		"legacy_simulate_no_provider_dispatch",
		"simulate_node_lifecycle = 'pvm'",
		"NULLIF(BTRIM(lease.engine_vm_id), '') IS NULL",
		"reservation.reservation_origin = 'migration_quarantine'",
		"reservation.reservation_mode = 'quarantine'",
		"reservation.operation_id IS NULL",
		"FROM runtime_lease_execution_authorities AS authority",
		"FROM provider_operations AS operation",
		"JOIN server_provider_resource_bindings AS binding",
		"managed_runtime_capacity_quarantine_retirement_digest(",
		"managed_runtime_capacity_quarantine_retirement_reject_mutation",
		"ALTER TABLE managed_runtime_capacity_quarantine_retirements FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("migration is missing %q", required)
		}
	}
}

func TestLegacySimulateRetirementRemainsDistinctFromProviderAbsence(t *testing.T) {
	content := readDBFile(t, "migrations/055_legacy_simulate_capacity_retirements.sql")
	upper := strings.ToUpper(content)
	for _, forbidden := range []string{
		"DELETE FROM MANAGED_RUNTIME_CAPACITY_RESERVATIONS",
		"UPDATE MANAGED_RUNTIME_CAPACITY_RESERVATIONS",
		"INSERT INTO MANAGED_RUNTIME_CAPACITY_RELEASE_FACTS",
		"INSERT INTO PROVIDER_ABSENCE_OBSERVATIONS",
	} {
		if strings.Contains(upper, forbidden) {
			t.Errorf("migration performs forbidden provider-custody mutation %q", forbidden)
		}
	}
	if strings.Count(content, "managed_runtime_capacity_quarantine_retirements AS retirement") < 1 {
		t.Fatal("insert-time capacity gate does not exclude exact retirement facts")
	}
}
