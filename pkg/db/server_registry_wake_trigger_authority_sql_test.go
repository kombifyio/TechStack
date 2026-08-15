package db

import (
	"strings"
	"testing"
)

func TestServerRegistryWakeTriggerAuthorityMigration(t *testing.T) {
	content := readDBFile(t, "migrations/083_server_registry_wake_trigger_authority.sql")
	for _, required := range []string{
		"server_registry_wake_sweep_tenant",
		"server_registry_wake_outbox_prune_tenant",
		"SECURITY DEFINER",
		"SET search_path TO pg_catalog, %I",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("wake-trigger authority migration missing %q", required)
		}
	}
	if strings.Contains(content, "GRANT INSERT") {
		t.Fatal("wake-directory table authority must not be granted to the application role")
	}
}
