package db

import (
	"strings"
	"testing"
)

func TestProviderProvisionSuccessorResolutionDropsOnlyRedundantAttestationRefUniqueness(t *testing.T) {
	content := readDBFile(t, "migrations/079_provider_provision_successor_resolution.sql")
	for _, required := range []string{
		"ALTER TABLE provider_provision_resolution_decisions",
		"DROP CONSTRAINT IF EXISTS provider_provision_resolution_tenant_id_operation_id_operat_key",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("successor resolution migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"DROP TABLE",
		"DELETE FROM",
		"UPDATE provider_provision_resolution_decisions",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("successor resolution migration weakens append-only evidence via %q", forbidden)
		}
	}
	if count := strings.Count(content, "DROP CONSTRAINT"); count != 1 {
		t.Fatalf("successor resolution migration drops %d constraints, want exactly one", count)
	}
}
