package db

import (
	"os"
	"strings"
	"testing"
)

func TestNativeProviderAdmissionMigrationBindsOneAtomicAggregate(t *testing.T) {
	payload, err := os.ReadFile("migrations/028_native_provider_admission.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(payload)
	for _, required := range []string{
		"servers_lease_tenant_fk",
		"techstack_vm_leases_server_tenant_fk",
		"DEFERRABLE INITIALLY DEFERRED NOT VALID",
		"ADD COLUMN IF NOT EXISTS operation_id text",
		"runtime_lease_idempotency_operation_required",
		"operation_scope <> 'providercontrol.provision' OR operation_id IS NOT NULL",
		"runtime_lease_idempotency_operation_tenant_fk",
		"FOREIGN KEY (tenant_id, operation_id, lease_id)",
		"REFERENCES provider_operations (tenant_id, operation_id, lease_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"gen_random_uuid()",
		"legacy_simulate",
		"provisioning-executor/v1",
		"ON DELETE CASCADE",
		"UPDATE runtime_lease_idempotency_records",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration introduced forbidden admission behavior via %q", forbidden)
		}
	}
}
