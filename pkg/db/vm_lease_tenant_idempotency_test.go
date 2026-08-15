package db

import (
	"strings"
	"testing"
)

func TestVMLeaseIdempotencyMigrationScopesClientKeysToTenant(t *testing.T) {
	content := readDBFile(t, "migrations/018_vm_lease_tenant_idempotency.sql")
	for _, want := range []string{
		"SET LOCAL lock_timeout = '10s'",
		"DROP CONSTRAINT IF EXISTS techstack_vm_leases_idempotency_key_key",
		"UNIQUE (tenant_id, idempotency_key)",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("tenant idempotency migration missing %q", want)
		}
	}
}
