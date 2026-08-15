package db

import (
	"strings"
	"testing"
)

func TestResourceFreeDecommissionGenerationMigrationKeepsAuthoritySpecificFences(t *testing.T) {
	content := readDBFile(t, "migrations/068_resource_free_decommission_generation.sql")
	for _, required := range []string{
		"NEW.release_authority NOT IN ('provider_absence', 'resource_free_teardown')",
		"operation_server_generation IS DISTINCT FROM NEW.server_generation",
		"NEW.server_generation < operation_server_generation",
		"live_server_generation IS DISTINCT FROM NEW.server_generation",
		"terminalization.server_generation = NEW.server_generation",
		"terminalization.resource_generation_id = NEW.resource_generation_id",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration lost required custody fence %q", required)
		}
	}
}
