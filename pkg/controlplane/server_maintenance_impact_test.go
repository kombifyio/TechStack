package controlplane

import (
	"testing"
	"time"
)

// Confirmation protects the critical invariant that a different affected
// service, operation, or coverage status cannot reuse the owner's approval.
func TestMaintenanceImpactConfirmationBindsSemanticChanges(t *testing.T) {
	now := time.Now().UTC()
	server := ServerRuntime{ID: "server", TenantID: "tenant", OwnerSubjectID: "owner", WorkerID: "agent", InventoryRevision: 1, LastHeartbeatAt: &now}
	services := []ServiceRuntime{{ID: "service", Name: "App", ServerID: "server", StackID: "stack", ObservedState: "running"}}
	stacks := []Stack{{ID: "stack", Name: "Home"}}
	build := func() *ServerMaintenanceImpact {
		return buildServerMaintenanceImpact(server, ServerMaintenanceActionReboot, &now, services, stacks, true, now)
	}
	original := build()
	server.InventoryRevision++
	server.Revision++
	if refreshed := build(); original.SnapshotDigest != refreshed.SnapshotDigest {
		t.Fatal("unchanged inventory refresh invalidated confirmation")
	}
	services[0].ID = "replacement"
	if changed := build(); original.SnapshotDigest == changed.SnapshotDigest {
		t.Fatal("replacement service reused confirmation")
	}
	services[0].ID = "service"
	if changed := buildServerMaintenanceImpact(server, ServerMaintenanceActionUpdate, &now, services, stacks, true, now); original.SnapshotDigest == changed.SnapshotDigest {
		t.Fatal("different operation reused confirmation")
	}
	if incomplete := buildServerMaintenanceImpact(server, ServerMaintenanceActionReboot, &now, nil, nil, false, now); incomplete.Confirmable || incomplete.Inventory.Status != "incomplete" {
		t.Fatal("missing inventory was accepted as no impact")
	}
	if empty := buildServerMaintenanceImpact(server, ServerMaintenanceActionReboot, &now, nil, nil, true, now); !empty.Confirmable {
		t.Fatal("complete current empty inventory could not be confirmed")
	}
	otherStack := ServiceRuntime{ID: "other-stack-app", Name: "Other app", ServerID: server.ID, StackID: "other-stack", ObservedState: "running", Metadata: map[string]any{"inventory_revision": server.InventoryRevision}}
	combined, consistent := mergeMaintenanceImpactServices(nil, []ServiceRuntime{otherStack}, server.InventoryRevision)
	preview := buildServerMaintenanceImpact(server, ServerMaintenanceActionReboot, &now, combined, nil, consistent, now)
	if preview.Confirmable || len(preview.Services) == 0 {
		t.Fatal("filtered cross-stack placement was hidden as confirmed empty impact")
	}
}
