package db

import (
	"strings"
	"testing"
)

func TestProviderProvisionDiscoveryClockSkewMigrationIsBounded(t *testing.T) {
	content := readDBFile(t, "migrations/080_provider_provision_discovery_clock_skew.sql")
	for _, required := range []string{
		"pg_get_functiondef",
		"NEW.collected_at > clock_timestamp() + interval ''30 seconds''",
		"function_definition NOT LIKE",
		"EXECUTE function_definition",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("clock-skew migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"DROP TRIGGER",
		"DISABLE TRIGGER",
		"interval ''1 minute''",
		"DELETE FROM provider_provision_dispatch_guards",
		"UPDATE provider_provision_dispatch_guards",
	} {
		if strings.Contains(strings.ToUpper(content), strings.ToUpper(forbidden)) {
			t.Fatalf("clock-skew migration contains forbidden widening %q", forbidden)
		}
	}
}
