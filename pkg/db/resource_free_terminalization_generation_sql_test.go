package db

import (
	"strings"
	"testing"
)

func TestResourceFreeTerminalizationGenerationMigrationKeepsExactHeadAndForwardGenerationFence(t *testing.T) {
	content := readDBFile(t, "migrations/081_resource_free_terminalization_generation.sql")
	for _, required := range []string{
		"provider_resource_free_terminalization_validate_insert()",
		"current_predicate constant text",
		"generation_fenced_predicate constant text",
		"operation_server_generation IS NULL",
		"operation_server_generation < 1",
		"NEW.server_generation < operation_server_generation",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("resource-free terminalization generation migration missing %q", required)
		}
	}
}
