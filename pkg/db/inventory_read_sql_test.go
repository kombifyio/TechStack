package db

import (
	"strings"
	"testing"
)

func TestInventoryReadMigrationAddsOwnerAndCursorIndexes(t *testing.T) {
	content := readDBFile(t, "migrations/021_inventory_owner_cursor_indexes.sql")
	for _, required := range []string{
		"servers (tenant_id, owner_subject_id, created_at, id)",
		"servers (tenant_id, created_at, id)",
		"stacks (tenant_id, owner_subject_id, id)",
		"services (tenant_id, server_id, created_at, id)",
		"services (tenant_id, created_at, id)",
		"services (tenant_id, stack_id, node_id, created_at, id)",
		"WHERE deleted_at IS NULL",
		"WHERE server_id IS NOT NULL",
		"WHERE server_id IS NULL AND node_id IS NOT NULL",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}
