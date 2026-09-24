package controlplane

import (
	"context"
	"testing"
	"time"
)

// One Guard observation carries both halves of the service model: services a
// StackKit declared and services merely discovered on the host. The batch
// source is the default, not a uniform one, or the unmanaged half could never
// be published at all.
func TestGuardInventoryProjectionKeepsPerServiceProvenance(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	observedAt := now.Add(time.Minute)
	command := guardInventoryProjectionTestCommand(observedAt, 1, true, "svc-managed", "svc-observed")
	for index := range command.Services {
		if command.Services[index].Legacy.ID != "svc-observed" {
			continue
		}
		command.Services[index].Legacy.Source = "observed"
		command.Services[index].Runtime.Source = "observed"
		// A discovered service declares no target state.
		command.Services[index].Runtime.DesiredState = ""
	}

	if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); err != nil {
		t.Fatalf("ApplyGuardInventoryProjection: %v", err)
	}
	for _, test := range []struct{ id, source, management string }{
		{id: "svc-managed", source: "stackkits-inventory", management: "managed"},
		{id: "svc-observed", source: "observed", management: "observed"},
	} {
		runtime, err := store.GetServiceRuntime(context.Background(), "tenant-1", test.id)
		if err != nil {
			t.Fatalf("GetServiceRuntime(%s): %v", test.id, err)
		}
		if runtime.Source != test.source || runtime.ManagementState != test.management {
			t.Fatalf("%s = source %q management %q, want %q/%q",
				test.id, runtime.Source, runtime.ManagementState, test.source, test.management)
		}
		legacy, err := store.GetService(context.Background(), "tenant-1", test.id)
		if err != nil {
			t.Fatalf("GetService(%s): %v", test.id, err)
		}
		if legacy.Source != runtime.Source || legacy.ManagementState != runtime.ManagementState {
			t.Fatalf("%s projections disagree on ownership: legacy=%#v runtime source=%q management=%q",
				test.id, legacy, runtime.Source, runtime.ManagementState)
		}
	}
}

// The authoritative prune belongs to the managed half. An observed row is not
// declared by the manifest, so a manifest that no longer lists it proves
// nothing about it and must not delete it.
func TestGuardInventoryManifestPruneLeavesObservedServicesAlone(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	first := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-managed", "svc-observed")
	for index := range first.Services {
		if first.Services[index].Legacy.ID != "svc-observed" {
			continue
		}
		first.Services[index].Legacy.Source = "observed"
		first.Services[index].Runtime.Source = "observed"
	}
	applied, err := store.ApplyGuardInventoryProjection(context.Background(), first)
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}

	// The manifest now lists only the managed service. The observed row must
	// survive: nothing in the manifest ever claimed it.
	second := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true, "svc-managed")
	second.Event.ExpectedRevision = applied.ServerEvent.Server.Revision
	if _, err := store.ApplyGuardInventoryProjection(context.Background(), second); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	if _, err := store.GetService(context.Background(), "tenant-1", "svc-observed"); err != nil {
		t.Fatalf("manifest prune deleted an observed service it never declared: %v", err)
	}
}

// A successful discovery is authoritative for its own observed half, while a
// refused probe carries no absence evidence. Vanished services retain their
// history as stopped rows instead of staying optimistically running or being
// hard-deleted with their transition timeline.
func TestGuardInventoryDiscoveryEvidenceControlsObservedServiceRetirement(t *testing.T) {
	t.Run("successful discovery retires a vanished service", func(t *testing.T) {
		store, now := newGuardInventoryProjectionTestStore(t)
		seedGuardInventoryTestStack(t, store)
		first := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-managed", "svc-old")
		markGuardInventoryTestServiceObserved(&first, "svc-old")
		first.DiscoveryObserved, first.DiscoveredServiceCount = true, 1
		applied, err := store.ApplyGuardInventoryProjection(context.Background(), first)
		if err != nil {
			t.Fatalf("first projection: %v", err)
		}

		second := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true, "svc-managed", "svc-new")
		markGuardInventoryTestServiceObserved(&second, "svc-new")
		second.DiscoveryObserved, second.DiscoveredServiceCount = true, 1
		second.Event.ExpectedRevision = applied.ServerEvent.Server.Revision
		result, err := store.ApplyGuardInventoryProjection(context.Background(), second)
		if err != nil {
			t.Fatalf("second projection: %v", err)
		}

		services := guardInventoryTestServicesByID(t, store)
		retired := services["svc-old"]
		if retired.ObservedState != "stopped" || retired.HealthState != "unknown" ||
			retired.Access["mode"] != "unavailable" || retired.Access["reason_code"] != guardServiceDiscoveryAbsentReason {
			t.Fatalf("retired observed service = %#v", retired)
		}
		if revision, ok := inventoryMetadataInt64(retired.Metadata, guardInventoryRevisionKey); !ok || revision != result.ServerEvent.Inventory.Revision {
			t.Fatalf("retired service revision = %d, %v", revision, ok)
		}
	})

	t.Run("refused discovery retains the last measured state", func(t *testing.T) {
		store, now := newGuardInventoryProjectionTestStore(t)
		seedGuardInventoryTestStack(t, store)
		first := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-managed", "svc-observed")
		markGuardInventoryTestServiceObserved(&first, "svc-observed")
		first.DiscoveryObserved, first.DiscoveredServiceCount = true, 1
		applied, err := store.ApplyGuardInventoryProjection(context.Background(), first)
		if err != nil {
			t.Fatalf("first projection: %v", err)
		}

		refused := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true, "svc-managed")
		refused.Event.ExpectedRevision = applied.ServerEvent.Server.Revision
		result, err := store.ApplyGuardInventoryProjection(context.Background(), refused)
		if err != nil {
			t.Fatalf("refused discovery projection: %v", err)
		}

		retained := guardInventoryTestServicesByID(t, store)["svc-observed"]
		if retained.ObservedState != "running" || retained.HealthState != "healthy" || retained.Access["mode"] != "direct" {
			t.Fatalf("retained observed service = %#v", retained)
		}
		if revision, ok := inventoryMetadataInt64(retained.Metadata, guardInventoryRevisionKey); !ok || revision != result.ServerEvent.Inventory.Revision {
			t.Fatalf("retained service revision = %d, %v", revision, ok)
		}
	})
}

func seedGuardInventoryTestStack(t *testing.T, store *MemoryStore) {
	t.Helper()
	if _, err := store.CreateStack(context.Background(), CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", InstanceID: "instance-1", OwnerSubjectID: "owner-1", Name: "Stack 1",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
}

func markGuardInventoryTestServiceObserved(command *GuardInventoryProjection, serviceID string) {
	for index := range command.Services {
		if command.Services[index].Legacy.ID != serviceID {
			continue
		}
		command.Services[index].Legacy.Source = serviceSourceObserved
		command.Services[index].Runtime.Source = serviceSourceObserved
		command.Services[index].Runtime.DesiredState = ""
	}
}

func guardInventoryTestServicesByID(t *testing.T, store *MemoryStore) map[string]ServiceRuntime {
	t.Helper()
	page, err := store.ListInventoryServices(
		context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-1", InventoryPageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatalf("ListInventoryServices: %v", err)
	}
	services := make(map[string]ServiceRuntime, len(page.Services))
	for _, service := range page.Services {
		services[service.ID] = service
	}
	return services
}
