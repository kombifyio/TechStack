package db

import (
	"strings"
	"testing"
)

func TestManagedRuntimeSlotsMigrationPinsTenantBoundIdentityAndReleaseFacts(t *testing.T) {
	content := readDBFile(t, "migrations/061_managed_runtime_server_slots.sql")
	for _, required := range []string{
		"PRIMARY KEY (tenant_id, stack_id, slot_key)",
		"UNIQUE (tenant_id, slot_id)",
		"UNIQUE (tenant_id, slot_id, generation_ordinal)",
		"FOREIGN KEY (tenant_id, stack_id)",
		"REFERENCES stacks (tenant_id, id) ON DELETE RESTRICT",
		"FOREIGN KEY (tenant_id, lease_id)",
		"REFERENCES techstack_vm_leases (tenant_id, id) ON DELETE RESTRICT",
		"FOREIGN KEY (tenant_id, runtime_server_id)",
		"REFERENCES servers (tenant_id, id) ON DELETE RESTRICT",
		"occupancy ends only through the matching append-only capacity release fact",
		"Slot identity is deliberately provider-neutral",
		"active to quarantined",
		"BEFORE UPDATE OR DELETE ON managed_runtime_server_slots",
		"FORCE ROW LEVEL SECURITY",
		"not backfilled or adopted",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("managed runtime slot migration is missing %q", required)
		}
	}
}

func TestManagedRuntimeSlotsMigrationDoesNotDuplicateReleaseEvidence(t *testing.T) {
	content := readDBFile(t, "migrations/061_managed_runtime_server_slots.sql")
	for _, forbidden := range []string{
		"absence_evidence_ref",
		"absence_evidence_digest",
		"released_at",
		"state IN ('active', 'quarantined', 'released')",
		"REFERENCES techstack_vm_leases(id)",
		"REFERENCES servers(id)",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("managed runtime slot migration duplicates or weakens release custody with %q", forbidden)
		}
	}
}

func TestManagedRuntimeSlotsMigrationHasNoAutomaticAdoptionOrDeletion(t *testing.T) {
	content := strings.ToUpper(readDBFile(t, "migrations/061_managed_runtime_server_slots.sql"))
	for _, forbidden := range []string{
		"INSERT INTO MANAGED_RUNTIME_SERVER_SLOTS SELECT",
		"DELETE FROM TECHSTACK_VM_LEASES",
		"DELETE FROM PROVIDER_OPERATION_RESOURCES",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("expand-only slot migration contains forbidden mutation %q", forbidden)
		}
	}
}
