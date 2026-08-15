package controlplane

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMemoryStoreGuardInventoryProjectionCommitsOneRevisionAtomically(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")

	result, err := store.ApplyGuardInventoryProjection(context.Background(), command)
	if err != nil {
		t.Fatalf("ApplyGuardInventoryProjection: %v", err)
	}
	if result.Replayed || result.ServerEvent == nil || !result.ServerEvent.Applied || result.ServerEvent.Inventory == nil {
		t.Fatalf("projection result = %#v", result)
	}
	revision := result.ServerEvent.Inventory.Revision
	if revision != 1 || result.ServerEvent.Server.InventoryRevision != revision {
		t.Fatalf("inventory revision = snapshot %d server %d", revision, result.ServerEvent.Server.InventoryRevision)
	}

	node, err := store.GetNode(context.Background(), "tenant-1", "server-1")
	if err != nil || inventoryRevisionFromTestMetadata(node.Metadata) != revision {
		t.Fatalf("node revision = %#v, err %v", node, err)
	}
	legacy, err := store.GetService(context.Background(), "tenant-1", "svc-a")
	if err != nil || inventoryRevisionFromTestMetadata(legacy.Metadata) != revision {
		t.Fatalf("legacy revision = %#v, err %v", legacy, err)
	}
	runtime, err := store.GetServiceRuntime(context.Background(), "tenant-1", "svc-a")
	if err != nil || inventoryRevisionFromTestMetadata(runtime.Metadata) != revision || runtime.ObservedAt == nil || !runtime.ObservedAt.Equal(command.Event.ObservedAt) {
		t.Fatalf("runtime revision/observation = %#v, err %v", runtime, err)
	}
	if _, ok := result.ServerEvent.Inventory.Inventory[guardProjectionEnvelopeKey]; ok {
		t.Fatal("repository envelope escaped to the top-level inventory schema")
	}
	if digest, ok := guardInventoryProjectionDigestFromInventory(result.ServerEvent.Inventory.Inventory); !ok || digest == "" {
		t.Fatalf("durable projection digest missing from deployment: %#v", result.ServerEvent.Inventory.Inventory["deployment"])
	}
}

func TestMemoryStoreGuardInventoryProjectionRejectsMalformedChildWithoutMutation(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	before, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	command.Services[0].Runtime.Metadata["api_token"] = "must-not-enter-state"

	if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyGuardInventoryProjection error = %v, want conflict", err)
	}
	after, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil || after.Revision != before.Revision || after.SourceSequence != before.SourceSequence || after.InventoryRevision != 0 {
		t.Fatalf("server mutated on rejected child: before %#v after %#v err %v", before, after, err)
	}
	if _, err := store.GetNode(context.Background(), "tenant-1", "server-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("node exists after rejected child: %v", err)
	}
	if len(store.serverInventory["server-1"]) != 0 || len(store.serviceRuntime) != 0 || len(store.svcs) != 0 {
		t.Fatalf("children mutated on rejection: inventory=%d legacy=%d runtime=%d", len(store.serverInventory["server-1"]), len(store.svcs), len(store.serviceRuntime))
	}
}

func TestMemoryStoreGuardInventoryProjectionRejectsIncompleteCanonicalBinding(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	current := store.servers["server-1"]
	current.LeaseID = ""
	store.servers["server-1"] = current

	_, err := store.ApplyGuardInventoryProjection(
		context.Background(), guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a"),
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyGuardInventoryProjection error = %v, want canonical binding conflict", err)
	}
	if len(store.nodes) != 0 || len(store.svcs) != 0 || len(store.serviceRuntime) != 0 {
		t.Fatalf("rejected canonical binding mutated children")
	}
}

func TestMemoryStoreGuardInventoryProjectionExactReplayAndChangedPayload(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-b", "svc-a")
	first, err := store.ApplyGuardInventoryProjection(context.Background(), command)
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}

	// Reversing both input sets is the same canonical projection and the stale
	// ExpectedRevision is deliberately excluded from its digest.
	replay := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a", "svc-b")
	replayed, err := store.ApplyGuardInventoryProjection(context.Background(), replay)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed || replayed.ServerEvent == nil || replayed.ServerEvent.Applied || replayed.ServerEvent.Server.Revision != first.ServerEvent.Server.Revision {
		t.Fatalf("replay result = %#v", replayed)
	}
	if len(store.serverInventory["server-1"]) != 1 {
		t.Fatalf("replay appended inventory: %d rows", len(store.serverInventory["server-1"]))
	}

	changed := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a", "svc-b")
	changed.Services[0].Runtime.HealthState = "unhealthy"
	if _, err := store.ApplyGuardInventoryProjection(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed same-position projection error = %v, want conflict", err)
	}
}

func TestMemoryStoreGuardInventoryProjectionExactPositionRequiresDurableDigest(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	if _, err := store.ApplyServerEvent(context.Background(), guardInventoryServerObservationEvent(command.Event)); err != nil {
		t.Fatalf("seed unbound inventory snapshot: %v", err)
	}
	if _, err := store.ApplyGuardInventoryProjection(context.Background(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("exact position without durable digest error = %v, want conflict", err)
	}
	if len(store.serviceRuntime) != 0 || len(store.svcs) != 0 {
		t.Fatalf("missing-digest replay wrote services: legacy=%d runtime=%d", len(store.svcs), len(store.serviceRuntime))
	}
}

func TestMemoryStoreGuardInventoryProjectionLateSnapshotCannotDowngradeOrDelete(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	n := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-old")
	first, err := store.ApplyGuardInventoryProjection(context.Background(), n)
	if err != nil {
		t.Fatalf("projection N: %v", err)
	}
	nPlusOne := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true, "svc-new")
	nPlusOne.Event.ExpectedRevision = first.ServerEvent.Server.Revision
	second, err := store.ApplyGuardInventoryProjection(context.Background(), nPlusOne)
	if err != nil {
		t.Fatalf("projection N+1: %v", err)
	}
	if _, err := store.GetServiceRuntime(context.Background(), "tenant-1", "svc-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("N+1 did not prune old service: %v", err)
	}

	late, err := store.ApplyGuardInventoryProjection(context.Background(), n)
	if err != nil {
		t.Fatalf("late N: %v", err)
	}
	if late.Replayed || late.ServerEvent == nil || late.ServerEvent.Applied {
		t.Fatalf("late N result = %#v", late)
	}
	current, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil || current.SourceSequence != 2 || current.InventoryRevision != second.ServerEvent.Server.InventoryRevision {
		t.Fatalf("late N changed server head: %#v err %v", current, err)
	}
	if _, err := store.GetServiceRuntime(context.Background(), "tenant-1", "svc-new"); err != nil {
		t.Fatalf("late N deleted N+1 service: %v", err)
	}
	if _, err := store.GetServiceRuntime(context.Background(), "tenant-1", "svc-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late N resurrected old service: %v", err)
	}
}

func TestMemoryStoreGuardInventoryProjectionPreservesActiveMigration(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	_, err := store.UpsertNode(context.Background(), Node{
		ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", WorkerID: "guard-1",
		Name: "operator-name", Role: "storage", Status: "pending", Address: "192.0.2.1",
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	_, err = store.UpsertServiceRuntime(context.Background(), ServiceRuntime{
		ID: "svc-a", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "svc-a", ServiceInstance: "default", Name: "Service A", DesiredState: "stopped",
		ObservedState: "running", HealthState: "unknown", Source: "stackkits-inventory",
		Access: map[string]any{"mode": "direct", "url": "https://old.example.test"}, Metadata: map[string]any{"workflow_id": "move-1"},
	})
	if err != nil {
		t.Fatalf("seed runtime migration: %v", err)
	}
	_, err = store.UpsertService(context.Background(), Service{
		ID: "svc-a", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", NodeID: "server-1",
		ServiceKey: "svc-a", Name: "Service A", Status: "migrating", Source: "stackkits-inventory",
		URL: "https://old.example.test", MigrationStatus: "migrating", Metadata: map[string]any{"workflow_id": "move-1"},
	})
	if err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	command := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	command.Services[0].Legacy.Status = "healthy"
	command.Services[0].Legacy.URL = "https://new.example.test"
	command.Services[0].Runtime.HealthState = "healthy"
	command.Services[0].Runtime.Access = map[string]any{"mode": "direct", "url": "https://new.example.test"}
	result, err := store.ApplyGuardInventoryProjection(context.Background(), command)
	if err != nil {
		t.Fatalf("projection during migration: %v", err)
	}

	legacy, _ := store.GetService(context.Background(), "tenant-1", "svc-a")
	runtime, _ := store.GetServiceRuntime(context.Background(), "tenant-1", "svc-a")
	node, _ := store.GetNode(context.Background(), "tenant-1", "server-1")
	if node.Name != "operator-name" || node.Role != "storage" || node.InstanceID != "instance-1" || node.WorkerID != "guard-1" {
		t.Fatalf("Guard rewrote control-plane node fields: %#v", node)
	}
	if legacy.Status != "migrating" || legacy.MigrationStatus != "migrating" || legacy.URL != "" || legacy.Metadata["workflow_id"] != "move-1" {
		t.Fatalf("legacy migration was not preserved: %#v", legacy)
	}
	if runtime.DesiredState != "stopped" || runtime.HealthState != "healthy" || runtime.Access["mode"] != "unavailable" || runtime.Access["url"] != nil || runtime.Metadata["workflow_id"] != "move-1" {
		t.Fatalf("runtime measurement/access = %#v", runtime)
	}
	if inventoryRevisionFromTestMetadata(legacy.Metadata) != result.ServerEvent.Inventory.Revision || inventoryRevisionFromTestMetadata(runtime.Metadata) != result.ServerEvent.Inventory.Revision {
		t.Fatalf("migration rows did not receive accepted revision: legacy=%#v runtime=%#v", legacy.Metadata, runtime.Metadata)
	}
}

func TestMemoryStoreGuardInventoryProjectionRetainsArchivedPruneAtCurrentRevision(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	if _, err := store.CreateStack(context.Background(), CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", InstanceID: "instance-1", OwnerSubjectID: "owner-1", Name: "Stack 1",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	first, err := store.ApplyGuardInventoryProjection(context.Background(), guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a"))
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}
	legacy := store.svcs["svc-a"]
	legacy.Status = guardServiceStateArchived
	legacy.URL = "https://stale.example.test"
	store.svcs["svc-a"] = legacy
	runtime := store.serviceRuntime["svc-a"]
	runtime.DesiredState = "stopped"
	store.serviceRuntime["svc-a"] = runtime

	empty := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, true)
	empty.Event.ExpectedRevision = first.ServerEvent.Server.Revision
	result, err := store.ApplyGuardInventoryProjection(context.Background(), empty)
	if err != nil {
		t.Fatalf("empty projection: %v", err)
	}
	page, err := store.ListInventoryServices(context.Background(), mustOwnerInventoryScope(t, "tenant-1", "owner-1"), "server-1", InventoryPageRequest{Limit: 10})
	if err != nil || len(page.Services) != 1 {
		t.Fatalf("ListInventoryServices = %#v, %v", page, err)
	}
	retained := page.Services[0]
	if retained.DesiredState != "stopped" || retained.Access["reason_code"] != "service_archived" ||
		inventoryRevisionFromTestMetadata(retained.Metadata) != result.ServerEvent.Inventory.Revision {
		t.Fatalf("retained archived projection = %#v server=%#v", retained, result.ServerEvent.Server.Metadata)
	}
	expected, ok := inventoryMetadataInt64(result.ServerEvent.Server.Metadata, "service_projection_expected")
	if !ok || expected != 1 {
		t.Fatalf("service_projection_expected = %v, %v", expected, ok)
	}
}

func TestMemoryStoreGuardInventoryProjectionManifestControlsAuthoritativePrune(t *testing.T) {
	store, now := newGuardInventoryProjectionTestStore(t)
	first := guardInventoryProjectionTestCommand(now.Add(time.Minute), 1, true, "svc-a")
	result, err := store.ApplyGuardInventoryProjection(context.Background(), first)
	if err != nil {
		t.Fatalf("initial manifest: %v", err)
	}

	nonAuthoritative := guardInventoryProjectionTestCommand(now.Add(2*time.Minute), 2, false, "svc-b")
	nonAuthoritative.Event.ExpectedRevision = result.ServerEvent.Server.Revision
	result, err = store.ApplyGuardInventoryProjection(context.Background(), nonAuthoritative)
	if err != nil {
		t.Fatalf("non-authoritative manifest: %v", err)
	}
	assertGuardInventoryTestServices(t, store, "svc-a", "svc-b")

	authoritative := guardInventoryProjectionTestCommand(now.Add(3*time.Minute), 3, true, "svc-b")
	authoritative.Event.ExpectedRevision = result.ServerEvent.Server.Revision
	result, err = store.ApplyGuardInventoryProjection(context.Background(), authoritative)
	if err != nil {
		t.Fatalf("authoritative manifest: %v", err)
	}
	assertGuardInventoryTestServices(t, store, "svc-b")

	// Absence of service evidence is not evidence of absence: a manifest that
	// reports zero services (e.g. written by a failed or partial apply) keeps
	// existing rows and flips their access to unavailable instead of deleting
	// observed history.
	empty := guardInventoryProjectionTestCommand(now.Add(4*time.Minute), 4, true)
	empty.Event.ExpectedRevision = result.ServerEvent.Server.Revision
	if _, err := store.ApplyGuardInventoryProjection(context.Background(), empty); err != nil {
		t.Fatalf("empty authoritative manifest: %v", err)
	}
	assertGuardInventoryTestServices(t, store, "svc-b")
	retained, err := store.ListServiceRuntimes(context.Background(), "tenant-1", "stack-1", "server-1")
	if err != nil {
		t.Fatalf("ListServiceRuntimes: %v", err)
	}
	if got := fmt.Sprint(retained[0].Access["mode"]); got != "unavailable" {
		t.Fatalf("retained service access mode = %q, want unavailable", got)
	}
	if got := fmt.Sprint(retained[0].Access["reason_code"]); got != "inventory_evidence_missing" {
		t.Fatalf("retained service access reason = %q, want inventory_evidence_missing", got)
	}
}

func newGuardInventoryProjectionTestStore(t *testing.T) (*MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "enrollment", SourceID: "enrollment-controller", ObservedAt: now,
		Runtime: ServerRuntime{
			ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
			OwnerSubjectID: "owner-1", WorkerID: "guard-1", NodeID: "server-1", LeaseID: "lease-1", Name: "runtime-1",
			LifecycleState: "enrolling", DesiredState: "running", ConnectionState: "pending", HealthState: "unknown",
		},
	})
	if err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return store, now
}

func guardInventoryProjectionTestCommand(observedAt time.Time, sequence int64, manifest bool, serviceIDs ...string) GuardInventoryProjection {
	services := make([]GuardInventoryServiceProjection, 0, len(serviceIDs))
	rawServices := make([]any, 0, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		services = append(services, GuardInventoryServiceProjection{
			Legacy: Service{
				ID: serviceID, TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", NodeID: "server-1",
				ServiceKey: serviceID, Name: serviceID, Status: "healthy", Source: "stackkits-inventory",
				URL: "https://" + serviceID + ".example.test", Metadata: map[string]any{"target_server": "server-1"},
			},
			Runtime: ServiceRuntime{
				ID: serviceID, TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", ServerID: "server-1",
				ServiceKey: serviceID, ServiceInstance: "default", Name: serviceID, DesiredState: "running",
				ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt,
				Access:       map[string]any{"mode": "direct", "url": "https://" + serviceID + ".example.test"},
				Capabilities: []string{"restart", "stop"}, Source: "stackkits-inventory", Metadata: map[string]any{"reported_service_id": serviceID},
			},
		})
		rawServices = append(rawServices, map[string]any{"id": serviceID, "status": "healthy"})
	}
	return GuardInventoryProjection{
		Event: ServerEvent{
			TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
			Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
			SourceEpoch: "epoch-a", SourceSequence: sequence, ObservedAt: observedAt,
			ClearConnectionReason: true, ClearHealthReason: true,
			Evidence: map[string]any{"runtime_agent_id": "guard-1"},
			Runtime: ServerRuntime{
				ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
				OwnerSubjectID: "owner-1", WorkerID: "guard-1", NodeID: "server-1", LeaseID: "lease-1", Name: "reported-hostname",
				ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &observedAt,
				Metadata: map[string]any{"runtime_agent_id": "guard-1", "inventory_source": "guard-inventory"},
			},
			Inventory: &ServerInventoryEvent{Source: "guard-inventory", Inventory: map[string]any{
				"host": map[string]any{"hostname": "reported-hostname"}, "services": rawServices,
				"channels": []any{}, "endpoints": []any{}, "deployment": map[string]any{"stackkit": "cloud-kit"},
			}},
		},
		Node: Node{
			ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", WorkerID: "guard-1",
			Name: "reported-hostname", Role: "foundation", Status: "healthy", Address: "192.0.2.10",
			Metadata: map[string]any{"inventory_source": "guard-inventory"},
		},
		Services: services, ManifestObserved: manifest, ServiceSource: "stackkits-inventory",
	}
}

func inventoryRevisionFromTestMetadata(metadata map[string]any) int64 {
	revision, _ := inventoryMetadataInt64(metadata, "inventory_revision")
	return revision
}

func assertGuardInventoryTestServices(t *testing.T, store *MemoryStore, expected ...string) {
	t.Helper()
	rows, err := store.ListServiceRuntimes(context.Background(), "tenant-1", "stack-1", "server-1")
	if err != nil {
		t.Fatalf("ListServiceRuntimes: %v", err)
	}
	if len(rows) != len(expected) {
		t.Fatalf("services = %#v, want %v", rows, expected)
	}
	for index, serviceID := range expected {
		if rows[index].ID != serviceID {
			t.Fatalf("service[%d] = %q, want %q", index, rows[index].ID, serviceID)
		}
	}
}
