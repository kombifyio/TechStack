package controlplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreApplyServerEventCommitsHeadTransitionsAndInventory(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	enrolled := applyTestEnrollment(t, store, now, "enrolling")
	if enrolled.Server.Revision != 1 || enrolled.Server.Generation != 1 || len(enrolled.Transitions) != 4 {
		t.Fatalf("enrollment result = %#v", enrolled)
	}

	heartbeatAt := now.Add(time.Minute)
	observed, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: heartbeatAt,
		Runtime: ServerRuntime{
			WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy",
			LastHeartbeatAt: &heartbeatAt, Metadata: map[string]any{"agent_version": "1.2.3"},
		},
		Inventory: &ServerInventoryEvent{Source: "guard-inventory", Inventory: map[string]any{"host": "observed"}},
	})
	if err != nil {
		t.Fatalf("ApplyServerEvent(Guard): %v", err)
	}
	if !observed.Applied || observed.Server.Revision != 2 || observed.Server.InventoryRevision != 1 {
		t.Fatalf("Guard result = %#v", observed)
	}
	if observed.Server.LifecycleState != "enrolling" {
		t.Fatalf("Guard changed lifecycle to %q", observed.Server.LifecycleState)
	}
	if observed.Server.SourceEpoch != "epoch-a" || observed.Server.SourceSequence != 1 || observed.Inventory == nil || observed.Inventory.Revision != 1 {
		t.Fatalf("source checkpoint/inventory = %#v", observed)
	}
	if len(observed.Transitions) != 2 {
		t.Fatalf("Guard transitions = %#v, want connection and health", observed.Transitions)
	}
	if len(store.serverInventory["server-1"]) != 1 || len(store.serverTransitions["server-1"]) != 6 || len(store.serverOutbox) != 2 {
		t.Fatalf("atomic children = inventory %#v transitions %#v outbox %#v", store.serverInventory["server-1"], store.serverTransitions["server-1"], store.serverOutbox)
	}
	if observed.Outbox == nil || observed.Outbox.AggregateRevision != observed.Server.Revision {
		t.Fatalf("outbox did not track aggregate revision: %#v", observed.Outbox)
	}
	if _, leaked := observed.Outbox.Payload["metadata"]; leaked {
		t.Fatalf("outbox leaked aggregate metadata: %#v", observed.Outbox.Payload)
	}
}

func TestMemoryStoreGuardReasonProjectionCannotEraseControlPlaneDimensions(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 10, 30, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	created, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 0, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "enrollment", SourceID: "enrollment-controller", ObservedAt: now,
		Runtime: ServerRuntime{
			OwnerSubjectID: "owner-1", WorkerID: "guard-1", Name: "runtime-1",
			LifecycleState: "enrolling", DesiredState: "running", ConnectionState: "pending", HealthState: "unknown",
			LifecycleReasonCode: "awaiting_guard", DesiredReasonCode: "requested_running",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Transitions) != 4 || created.Transitions[0].ReasonCode != "awaiting_guard" ||
		created.Transitions[1].ReasonCode != "requested_running" || created.Transitions[2].ReasonCode != "" || created.Transitions[3].ReasonCode != "" {
		t.Fatalf("creation transition reasons = %#v", created.Transitions)
	}
	observedAt := now.Add(time.Minute)
	observed, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: created.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: observedAt,
		Runtime: ServerRuntime{
			WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &observedAt,
			ConnectionReasonCode: "guard_connected", HealthReasonCode: "guard_healthy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Server.LifecycleReasonCode != "awaiting_guard" || observed.Server.DesiredReasonCode != "requested_running" {
		t.Fatalf("Guard overwrote control-plane reasons: %#v", observed.Server)
	}
	if len(observed.Transitions) != 2 || observed.Transitions[0].Dimension != "connection" || observed.Transitions[0].ReasonCode != "guard_connected" ||
		observed.Transitions[1].Dimension != "health" || observed.Transitions[1].ReasonCode != "guard_healthy" {
		t.Fatalf("Guard transition reasons = %#v", observed.Transitions)
	}

	clearedAt := observedAt.Add(time.Minute)
	cleared, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: observed.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 2, ObservedAt: clearedAt,
		ClearConnectionReason: true, ClearHealthReason: true,
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &clearedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Server.LifecycleReasonCode != "awaiting_guard" || cleared.Server.DesiredReasonCode != "requested_running" ||
		cleared.Server.ConnectionReasonCode != "" || cleared.Server.HealthReasonCode != "" {
		t.Fatalf("dimension-scoped Guard clear = %#v", cleared.Server)
	}
	if !cleared.Server.ConnectionChangedAt.Equal(clearedAt) || !cleared.Server.HealthChangedAt.Equal(clearedAt) {
		t.Fatalf("reason-only changed_at was not advanced: %#v", cleared.Server)
	}
	if len(cleared.Transitions) != 2 || cleared.Transitions[0].FromState != cleared.Transitions[0].ToState ||
		cleared.Transitions[0].Evidence["reason_changed"] != true {
		t.Fatalf("reason-only transitions = %#v", cleared.Transitions)
	}

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: cleared.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 3, ObservedAt: clearedAt.Add(time.Minute),
		Runtime: ServerRuntime{WorkerID: "guard-1", LifecycleReasonCode: "guard_owned_lifecycle"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Guard lifecycle reason error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreControlPlaneReasonProjectionCannotEraseGuardDimensions(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 10, 45, 0, 0, time.UTC)
	created := applyTestEnrollment(t, store, now, "enrolling")
	guardAt := now.Add(time.Minute)
	guard, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: created.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: guardAt,
		Runtime: ServerRuntime{
			WorkerID: "guard-1", ConnectionState: "connected", HealthState: "degraded", LastHeartbeatAt: &guardAt,
			ConnectionReasonCode: "guard_connected", HealthReasonCode: "disk_pressure",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	controlAt := guardAt.Add(time.Minute)
	controlled, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: guard.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "enrollment-controller", SourceID: "enrollment-controller", ObservedAt: controlAt,
		Runtime: ServerRuntime{LifecycleState: "active", LifecycleReasonCode: "enrollment_complete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if controlled.Server.ConnectionReasonCode != "guard_connected" || controlled.Server.HealthReasonCode != "disk_pressure" ||
		!controlled.Server.ConnectionChangedAt.Equal(guardAt) || !controlled.Server.HealthChangedAt.Equal(guardAt) {
		t.Fatalf("control plane overwrote Guard reasons: %#v", controlled.Server)
	}
	if len(controlled.Transitions) != 1 || controlled.Transitions[0].Dimension != "lifecycle" ||
		controlled.Transitions[0].ReasonCode != "enrollment_complete" {
		t.Fatalf("control-plane transition reasons = %#v", controlled.Transitions)
	}

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: controlled.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller", ObservedAt: controlAt.Add(time.Minute),
		Runtime: ServerRuntime{ConnectionReasonCode: "control_owned_connection"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("control-plane connection reason error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreGuardCannotMutateLifecycleOrBindings(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	applyTestEnrollment(t, store, now, "enrolling")

	for _, patch := range []ServerRuntime{
		{LifecycleState: "active", WorkerID: "guard-1"},
		{DesiredState: "absent", WorkerID: "guard-1"},
		{WorkerID: "guard-other", ConnectionState: "connected"},
		{LeaseID: "lease-other", WorkerID: "guard-1", ConnectionState: "connected"},
		{Name: "guard-controlled-name", WorkerID: "guard-1", ConnectionState: "connected"},
	} {
		_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
			TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 1, Generation: 1,
			Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
			SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now.Add(time.Minute), Runtime: patch,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Guard patch %#v error = %v, want ErrConflict", patch, err)
		}
	}
	server, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil || server.Revision != 1 || server.LifecycleState != "enrolling" || server.WorkerID != "guard-1" {
		t.Fatalf("rejected Guard writes changed head: %#v, %v", server, err)
	}
}

func TestMemoryStoreGuardRejectsAuthorityMetadataAndSecrets(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 11, 15, 0, 0, time.UTC)
	applyTestEnrollment(t, store, now, "enrolling")

	for _, event := range []ServerEvent{
		{Runtime: ServerRuntime{WorkerID: "guard-1", Metadata: map[string]any{"cleanup_state": "absent"}}},
		{Runtime: ServerRuntime{WorkerID: "guard-1", Channels: []ServerChannel{{Type: "ssh", EndpointRef: "ssh://root:secret@example.test"}}}}, // #nosec G101 -- intentional credential-bearing URL rejection fixture
		{Runtime: ServerRuntime{WorkerID: "guard-1"}, Inventory: &ServerInventoryEvent{Inventory: map[string]any{
			"endpoints": []map[string]any{{"url": "https://example.test/path?token=secret"}},
		}}},
	} {
		event.TenantID = "tenant-1"
		event.ServerID = "server-1"
		event.ExpectedRevision = 1
		event.Generation = 1
		event.Authority = ServerEventAuthorityGuard
		event.Source = "guard-inventory"
		event.SourceID = "guard-1"
		event.SourceEpoch = "epoch-a"
		event.SourceSequence = 1
		event.ObservedAt = now.Add(time.Minute)
		if _, err := store.ApplyServerEvent(context.Background(), event); !errors.Is(err, ErrConflict) {
			t.Fatalf("secret/authority event %#v error = %v, want ErrConflict", event, err)
		}
	}
}

func TestMemoryStoreGuardCannotCreateServerAuthorityState(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 0, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now,
		Runtime: ServerRuntime{
			OwnerSubjectID: "owner-1", WorkerID: "guard-1", LeaseID: "lease-1",
			ProviderRef: "provider-resource-1", Name: "runtime-1",
			ConnectionState: "connected", HealthState: "healthy",
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Guard-first event error = %v, want ErrConflict", err)
	}
	if _, getErr := store.GetServerRuntime(context.Background(), "tenant-1", "server-1"); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("Guard-first event created authority state: %v", getErr)
	}
}

func TestMemoryStoreServerEventRejectsUnknownStateWithoutPartialWrite(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 0, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "test", SourceID: "test",
		Runtime: ServerRuntime{
			OwnerSubjectID: "owner-1", Name: "runtime-1", LifecycleState: "resurrected",
			DesiredState: "running", ConnectionState: "connected", HealthState: "healthy",
		},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown state error = %v, want ErrConflict", err)
	}
	if _, getErr := store.GetServerRuntime(context.Background(), "tenant-1", "server-1"); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("invalid event partially wrote a server: %v", getErr)
	}
}

func TestMemoryStoreLateGuardObservationCannotResurrectDecommissioningServer(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")
	decommissioning, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(time.Minute), Runtime: ServerRuntime{LifecycleState: "decommissioning", DesiredState: "absent"},
	})
	if err != nil {
		t.Fatalf("decommission: %v", err)
	}
	late, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: decommissioning.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
		SourceEpoch: "epoch-old", SourceSequence: 99, ObservedAt: now.Add(2 * time.Minute),
		Runtime: ServerRuntime{
			WorkerID: "other-worker", OwnerSubjectID: "other-owner",
			ConnectionState: "connected", HealthState: "healthy",
			Metadata: map[string]any{"credential": "must-never-be-evaluated-after-tombstone"}, // #nosec G101 -- intentional stale secret-marker fixture
		},
		Inventory: &ServerInventoryEvent{Inventory: map[string]any{"late": true}},
	})
	if err != nil {
		t.Fatalf("late Guard event: %v", err)
	}
	if late.Applied || late.Server.LifecycleState != "decommissioning" || late.Server.Revision != decommissioning.Server.Revision || late.Server.InventoryRevision != 0 {
		t.Fatalf("late Guard event changed terminal head: %#v", late)
	}
	if len(store.serverInventory["server-1"]) != 0 {
		t.Fatalf("late Guard inventory was persisted: %#v", store.serverInventory["server-1"])
	}
}

func TestMemoryStoreGuardEpochAndSequenceFenceStaleProcesses(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")

	first := applyTestGuard(t, store, active.Server.Revision, 1, "epoch-a", 7, now.Add(time.Minute))
	replay := applyTestGuard(t, store, first.Server.Revision, 1, "epoch-a", 7, now.Add(time.Minute))
	if replay.Applied || replay.Server.Revision != first.Server.Revision {
		t.Fatalf("same sequence replay was not idempotent: %#v", replay)
	}
	restarted := applyTestGuard(t, store, first.Server.Revision, 1, "epoch-b", 1, now.Add(2*time.Minute))
	if !restarted.Applied || restarted.Server.SourceEpoch != "epoch-b" {
		t.Fatalf("new process epoch was not accepted: %#v", restarted)
	}
	delayedOld := applyTestGuard(t, store, restarted.Server.Revision, 1, "epoch-a", 1, now.Add(3*time.Minute))
	if delayedOld.Applied || delayedOld.Server.SourceEpoch != "epoch-b" || delayedOld.Server.Revision != restarted.Server.Revision {
		t.Fatalf("superseded process retook source ownership: %#v", delayedOld)
	}
}

func TestMemoryStoreBindingGenerationFencesPreviousGuard(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 0, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")
	guardAt := now.Add(30 * time.Second)
	guard, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: guardAt,
		Runtime: ServerRuntime{
			WorkerID: "guard-1", ConnectionState: "connected", HealthState: "degraded", LastHeartbeatAt: &guardAt,
			ConnectionReasonCode: "guard_connected", HealthReasonCode: "disk_pressure",
		},
	})
	if err != nil {
		t.Fatalf("seed Guard reasons: %v", err)
	}
	rebound, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: guard.Server.Revision, Generation: 2,
		Authority: ServerEventAuthorityControlPlane, Source: "reenrollment", SourceID: "enrollment-controller",
		ObservedAt: now.Add(time.Minute), Runtime: ServerRuntime{WorkerID: "guard-2"},
	})
	if err != nil || rebound.Server.Generation != 2 || rebound.Server.SourceEpoch != "" {
		t.Fatalf("rebind result = %#v, %v", rebound, err)
	}
	if rebound.Server.ConnectionState != "pending" || rebound.Server.HealthState != "unknown" ||
		rebound.Server.ConnectionReasonCode != "" || rebound.Server.HealthReasonCode != "" ||
		rebound.Server.LastHeartbeatAt != nil || len(rebound.Server.Channels) != 0 {
		t.Fatalf("rebind retained stale Guard observations: %#v", rebound.Server)
	}
	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: rebound.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "old", SourceSequence: 1, ObservedAt: now.Add(2 * time.Minute),
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("old generation error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreControlPlaneCannotWriteGuardObservationState(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 15, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")
	heartbeatAt := now.Add(time.Minute)

	for _, event := range []ServerEvent{
		{
			Runtime: ServerRuntime{ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &heartbeatAt},
		},
		{
			Runtime:   ServerRuntime{LifecycleState: "decommissioning", DesiredState: "absent"},
			Inventory: &ServerInventoryEvent{Inventory: map[string]any{"source": "control-plane"}},
		},
	} {
		event.TenantID = "tenant-1"
		event.ServerID = "server-1"
		event.ExpectedRevision = active.Server.Revision
		event.Generation = active.Server.Generation
		event.Authority = ServerEventAuthorityControlPlane
		event.Source = "lifecycle-controller"
		event.SourceID = "lifecycle-controller"
		event.ObservedAt = heartbeatAt
		if _, err := store.ApplyServerEvent(context.Background(), event); !errors.Is(err, ErrConflict) {
			t.Fatalf("control-plane observation event %#v error = %v, want ErrConflict", event, err)
		}
	}

	got, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil || got.Revision != active.Server.Revision || got.ConnectionState != "pending" || got.HealthState != "unknown" {
		t.Fatalf("rejected control-plane observation changed aggregate: %#v, %v", got, err)
	}
}

func TestMemoryStoreDecommissionLifecycleRequiresAndPreservesTombstone(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 20, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")

	_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(time.Minute), Runtime: ServerRuntime{LifecycleState: "decommissioning"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("decommissioning without absent tombstone error = %v, want ErrConflict", err)
	}

	tombstone, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(time.Minute), Runtime: ServerRuntime{LifecycleState: "decommissioning", DesiredState: "absent"},
	})
	if err != nil {
		t.Fatalf("create decommission tombstone: %v", err)
	}
	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: tombstone.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(2 * time.Minute), Runtime: ServerRuntime{LifecycleState: "failed"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("decommission tombstone -> failed error = %v, want ErrConflict", err)
	}

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: tombstone.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: now.Add(3 * time.Minute), Runtime: ServerRuntime{LifecycleState: "decommissioned"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("decommissioned without terminal timestamp error = %v, want ErrConflict", err)
	}
	finishedAt := now.Add(3 * time.Minute)
	finished, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: tombstone.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
		ObservedAt: finishedAt, Runtime: ServerRuntime{LifecycleState: "decommissioned", DecommissionedAt: &finishedAt},
	})
	if err != nil || finished.Server.LifecycleState != "decommissioned" || finished.Server.DecommissionedAt == nil {
		t.Fatalf("terminal decommission result = %#v, %v", finished, err)
	}
}

func TestMemoryStoreGuardObservationRejectsNestedSecretsAndUnboundedChannelMetadata(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 25, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")

	for _, runtimePatch := range []ServerRuntime{
		{WorkerID: "guard-1", Metadata: map[string]any{"host": map[string]string{"api_token": "secret"}}},
		{WorkerID: "guard-1", Channels: []ServerChannel{{Type: "credential-token", Role: "primary", State: "connected"}}},
		{WorkerID: "guard-1", Channels: []ServerChannel{{Type: "ssh", Metadata: map[string]any{"authenticated": "secret"}}}},
		{WorkerID: "guard-1", Channels: []ServerChannel{{Type: "ssh", Metadata: map[string]any{"provenance": "arbitrary-secret-value"}}}},
	} {
		_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
			TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
			Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
			SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: now.Add(time.Minute), Runtime: runtimePatch,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("unsafe Guard patch %#v error = %v, want ErrConflict", runtimePatch, err)
		}
	}
	got, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil || got.Revision != active.Server.Revision {
		t.Fatalf("rejected secret observation changed aggregate: %#v, %v", got, err)
	}
}

func TestMemoryStoreControlPlaneMetadataAndEvidenceMustBeSecretFree(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 27, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")

	for _, event := range []ServerEvent{
		{Runtime: ServerRuntime{Metadata: map[string]any{"access_token": "secret"}}},
		{Evidence: map[string]any{"provider_url": "https://user:password@provider.example.test"}}, // #nosec G101 -- intentional credential-bearing URL rejection fixture
	} {
		event.TenantID = "tenant-1"
		event.ServerID = "server-1"
		event.ExpectedRevision = active.Server.Revision
		event.Generation = active.Server.Generation
		event.Authority = ServerEventAuthorityControlPlane
		event.Source = "lifecycle-controller"
		event.SourceID = "lifecycle-controller"
		event.ObservedAt = now.Add(time.Minute)
		if _, err := store.ApplyServerEvent(context.Background(), event); !errors.Is(err, ErrConflict) {
			t.Fatalf("unsafe control-plane event %#v error = %v, want ErrConflict", event, err)
		}
	}
}

func TestMemoryStoreOwnerTransferIsControlPlaneOwnedAndDoesNotRebindRuntime(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 14, 30, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")
	transferred, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "owner-transfer", SourceID: "access-control",
		ObservedAt: now.Add(time.Minute), Runtime: ServerRuntime{OwnerSubjectID: "owner-2"},
	})
	if err != nil {
		t.Fatalf("owner transfer: %v", err)
	}
	if transferred.Server.OwnerSubjectID != "owner-2" || transferred.Server.Generation != 1 {
		t.Fatalf("owner transfer changed the wrong dimensions: %#v", transferred.Server)
	}

	guardAt := now.Add(2 * time.Minute)
	guard, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: transferred.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 1, ObservedAt: guardAt,
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &guardAt},
	})
	if err != nil {
		t.Fatalf("post-transfer Guard observation: %v", err)
	}
	if guard.Server.OwnerSubjectID != "owner-2" {
		t.Fatalf("Guard overwrote transferred owner: %#v", guard.Server)
	}

	_, err = store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: guard.Server.Revision, Generation: 1,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: 2, ObservedAt: now.Add(3 * time.Minute),
		Runtime: ServerRuntime{WorkerID: "guard-1", OwnerSubjectID: "owner-1", ConnectionState: "connected"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Guard owner rollback error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreServerEventCASAllowsOneConcurrentWinner(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	active := applyTestEnrollment(t, store, now, "active")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, lifecycle := range []string{"failed", "decommissioning"} {
		wg.Add(1)
		go func(state string) {
			defer wg.Done()
			<-start
			runtimePatch := ServerRuntime{LifecycleState: state}
			if state == "decommissioning" {
				runtimePatch.DesiredState = "absent"
			}
			_, err := store.ApplyServerEvent(context.Background(), ServerEvent{
				TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: active.Server.Revision, Generation: 1,
				Authority: ServerEventAuthorityControlPlane, Source: "lifecycle-controller", SourceID: "lifecycle-controller",
				ObservedAt: now.Add(time.Minute), Runtime: runtimePatch,
			})
			errs <- err
		}(lifecycle)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected CAS error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS outcomes successes=%d conflicts=%d", successes, conflicts)
	}
}

func applyTestEnrollment(t *testing.T, store *MemoryStore, at time.Time, lifecycle string) *ServerEventResult {
	t.Helper()
	result, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: 0, Generation: 1,
		Authority: ServerEventAuthorityControlPlane, Source: "enrollment", SourceID: "enrollment-controller", ObservedAt: at,
		Runtime: ServerRuntime{
			ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", WorkerID: "guard-1",
			LeaseID: "lease-1", Name: "runtime-1", LifecycleState: lifecycle,
			DesiredState: "running", ConnectionState: "pending", HealthState: "unknown",
		},
	})
	if err != nil {
		t.Fatalf("initial enrollment: %v", err)
	}
	return result
}

func applyTestGuard(t *testing.T, store *MemoryStore, revision, generation int64, epoch string, sequence int64, at time.Time) *ServerEventResult {
	t.Helper()
	result, err := store.ApplyServerEvent(context.Background(), ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: revision, Generation: generation,
		Authority: ServerEventAuthorityGuard, Source: "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: epoch, SourceSequence: sequence, ObservedAt: at,
		Runtime: ServerRuntime{WorkerID: "guard-1", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &at},
	})
	if err != nil {
		t.Fatalf("Guard event %s/%d: %v", epoch, sequence, err)
	}
	return result
}
