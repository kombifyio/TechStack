package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedProviderBoundGuardServer mirrors a managed provider server: the control
// plane owns a provider ref that Guard never observes and therefore never
// echoes back in its inventory event.
func seedProviderBoundGuardServer(t *testing.T, providerRef string) (*MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "enrollment",
		SourceID: "enrollment-controller", ObservedAt: now,
		Runtime: ServerRuntime{
			ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
			OwnerSubjectID: "owner-1", WorkerID: "guard-1", NodeID: "server-1", LeaseID: "lease-1",
			ProviderRef: providerRef, Name: "runtime-1",
			LifecycleState: "enrolling", DesiredState: "running",
			ConnectionState: "pending", HealthState: "unknown",
		},
	})
	if err != nil {
		t.Fatalf("seed provider-bound server: %v", err)
	}
	return store, now
}

// A provider-bound server must still accept Guard inventory. Guard observes the
// host, not control-plane placement, so it sends no provider ref at all. Reading
// that absence as a binding change rejected every managed server's inventory
// forever while its heartbeat kept succeeding, leaving the read model with no
// addresses, no platform, and no services.
func TestGuardInventoryAcceptsUnassertedProviderBinding(t *testing.T) {
	store, now := seedProviderBoundGuardServer(t, "ionos-managed")

	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	if command.Event.Runtime.ProviderRef != "" {
		t.Fatalf("fixture must not assert a provider ref, got %q", command.Event.Runtime.ProviderRef)
	}

	result, err := store.ApplyGuardInventoryProjection(context.Background(), command)
	if err != nil {
		t.Fatalf("apply Guard inventory on a provider-bound server: %v", err)
	}
	if result == nil || result.ServerEvent == nil || result.ServerEvent.Server == nil {
		t.Fatal("Guard inventory returned no server projection")
	}
	if !result.ServerEvent.Applied {
		t.Fatal("Guard inventory was not applied")
	}
	if got := result.ServerEvent.Server.InventoryRevision; got <= 0 {
		t.Fatalf("inventory revision = %d, want a durable revision above 0", got)
	}
	if got := result.ServerEvent.Server.ProviderRef; got != "ionos-managed" {
		t.Fatalf("provider ref = %q, want the canonical binding to survive Guard inventory", got)
	}
}

// The absence of a value is not permission to change it: a Guard event that
// actively asserts a different provider is still a conflict.
func TestGuardInventoryRejectsDifferentProviderBinding(t *testing.T) {
	store, now := seedProviderBoundGuardServer(t, "ionos-managed")

	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	command.Event.Runtime.ProviderRef = "centron-managed"

	if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for a contradicting provider binding", err)
	}
}

// Bindings the control plane must own stay strictly required: an unbound
// canonical server cannot be adopted by Guard inventory.
func TestGuardInventoryStillRequiresBoundStackOwnerAndNode(t *testing.T) {
	for _, test := range []struct {
		name  string
		mutth func(*GuardInventoryProjection)
	}{
		{"stack", func(c *GuardInventoryProjection) { c.Event.Runtime.StackID = "other-stack" }},
		{"owner", func(c *GuardInventoryProjection) { c.Event.Runtime.OwnerSubjectID = "other-owner" }},
		{"node", func(c *GuardInventoryProjection) { c.Event.Runtime.NodeID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, now := seedProviderBoundGuardServer(t, "ionos-managed")
			command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
			test.mutth(&command)
			if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v, want ErrConflict for an unbound %s", err, test.name)
			}
		})
	}
}
