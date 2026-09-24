package routes

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// lockableServiceFixture builds one server-bound, freshly observed service that
// satisfies every precondition of the agent-executed actions, so a refusal in
// these tests can only come from the owner guardrail.
func lockableServiceFixture(t *testing.T) (*controlplane.MemoryStore, time.Time) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-10 * time.Second)
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		StackKitInstanceID: "family-main", Name: "Stack",
		Config: map[string]any{"stackkit": "family-lab"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		WorkerID: "agent-1", InventoryRevision: 7,
		ConnectionState: string(serverregistry.ConnectionConnected), LastHeartbeatAt: &observedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "auth", StackKitVersion: "family-lab@v0.1.0", ObservedAt: &observedAt,
		Capabilities: []string{"restart", "logs"},
		Metadata:     map[string]any{"inventory_revision": int64(7)},
	}); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func postServiceAction(
	t *testing.T,
	h *serviceRuntimeHandlers,
	body map[string]any,
	idempotencyKey string,
) int {
	t.Helper()
	event, recorder := registryRouteStoreTestEvent(
		http.MethodPost, "/api/v1/registry/services/service-1/actions", "owner-1", "tenant-1", body)
	event.Request.SetPathValue("serviceId", "service-1")
	event.Request.Header.Set("Idempotency-Key", idempotencyKey)
	if err := h.action(event); err != nil {
		t.Fatalf("action %v: %v", body["action"], err)
	}
	return recorder.Code
}

// TestServiceLockRefusesMutationsAndSurvivesUnlock is the behavioural contract
// of the owner guardrail at the API boundary: a locked service refuses every
// agent-executed mutation, keeps its read path, and returns to normal after an
// unlock. The lock is deliberately not bound to an inventory revision.
func TestServiceLockRefusesMutationsAndSurvivesUnlock(t *testing.T) {
	store, now := lockableServiceFixture(t)
	orch := &recordingServiceActionOrchestrator{store: store}
	handler := func() *serviceRuntimeHandlers {
		return &serviceRuntimeHandlers{
			store: store, stacks: store, servers: store, jobs: store,
			now: func() time.Time { return now }, orch: orch,
		}
	}

	if code := postServiceAction(t, handler(),
		map[string]any{"action": "freeze", "owner_approved": true}, "lock-1"); code != http.StatusOK {
		t.Fatalf("freeze status = %d, want 200", code)
	}
	locked, err := store.GetServiceRuntime(t.Context(), "tenant-1", "service-1")
	if err != nil {
		t.Fatal(err)
	}
	if !locked.MutationLock.Locked() || locked.MutationLock.Actor != "owner-1" || locked.MutationLock.ChangedAt == nil {
		t.Fatalf("stored lock = %#v", locked.MutationLock)
	}

	if code := postServiceAction(t, handler(),
		map[string]any{"action": "restart", "expected_inventory_revision": 7, "owner_approved": true},
		"restart-1"); code != http.StatusConflict {
		t.Fatalf("locked restart status = %d, want 409", code)
	}
	if len(orch.requests) != 0 {
		t.Fatalf("a locked service dispatched %d agent operations", len(orch.requests))
	}

	if code := postServiceAction(t, handler(),
		map[string]any{"action": "unfreeze", "owner_approved": true}, "unlock-1"); code != http.StatusOK {
		t.Fatalf("unfreeze status = %d, want 200", code)
	}
	unlocked, err := store.GetServiceRuntime(t.Context(), "tenant-1", "service-1")
	if err != nil {
		t.Fatal(err)
	}
	if unlocked.MutationLock.Locked() || unlocked.MutationLock.Actor != "" || unlocked.MutationLock.ChangedAt != nil {
		t.Fatalf("unlock left residue: %#v", unlocked.MutationLock)
	}
	if code := postServiceAction(t, handler(),
		map[string]any{"action": "restart", "expected_inventory_revision": 7, "owner_approved": true},
		"restart-2"); code != http.StatusAccepted {
		t.Fatalf("restart after unlock status = %d, want 202", code)
	}
}

// TestServiceLockKeepsTheReadPathOpen proves the guardrail is a mutation guard,
// not a quarantine: an owner has to be able to inspect a service they froze.
// It runs on its own fixture because the endpoint admits only one active
// service action at a time.
func TestServiceLockKeepsTheReadPathOpen(t *testing.T) {
	store, now := lockableServiceFixture(t)
	h := &serviceRuntimeHandlers{
		store: store, stacks: store, servers: store, jobs: store,
		now: func() time.Time { return now }, orch: &recordingServiceActionOrchestrator{store: store},
	}
	if code := postServiceAction(t, h,
		map[string]any{"action": "freeze", "owner_approved": true}, "lock-1"); code != http.StatusOK {
		t.Fatalf("freeze status = %d, want 200", code)
	}
	if code := postServiceAction(t, h,
		map[string]any{"action": "logs", "expected_inventory_revision": 7, "limit": 10},
		"logs-1"); code != http.StatusAccepted {
		t.Fatalf("logs on a locked service status = %d, want 202", code)
	}
}

// TestServiceLockIsIdempotentAndOwnerApproved proves the two governance
// properties the endpoint promises: repeating a lock is a no-op rather than a
// conflict, and changing a guardrail always needs explicit owner approval.
func TestServiceLockIsIdempotentAndOwnerApproved(t *testing.T) {
	store, now := lockableServiceFixture(t)
	h := &serviceRuntimeHandlers{
		store: store, stacks: store, servers: store, jobs: store,
		now: func() time.Time { return now }, orch: &recordingServiceActionOrchestrator{store: store},
	}
	if code := postServiceAction(t, h,
		map[string]any{"action": "freeze", "owner_approved": false}, "lock-0"); code != http.StatusBadRequest {
		t.Fatalf("unapproved freeze status = %d, want 400", code)
	}
	if code := postServiceAction(t, h,
		map[string]any{"action": "freeze", "owner_approved": true, "expected_inventory_revision": 7},
		"lock-rev"); code != http.StatusBadRequest {
		t.Fatalf("freeze bound to an inventory revision status = %d, want 400", code)
	}
	for _, key := range []string{"lock-1", "lock-2"} {
		if code := postServiceAction(t, h,
			map[string]any{"action": "freeze", "owner_approved": true}, key); code != http.StatusOK {
			t.Fatalf("repeated freeze %s status = %d, want 200", key, code)
		}
	}
}

// TestServiceReadModelNarrowsAllowedActionsWhileLocked pins the invariant that
// advertised and enforced capabilities cannot drift: the read model offers
// exactly what the endpoint accepts, and always offers the way out of a lock.
func TestServiceReadModelNarrowsAllowedActionsWhileLocked(t *testing.T) {
	store, now := lockableServiceFixture(t)
	h := &serviceRuntimeHandlers{
		store: store, stacks: store, servers: store, jobs: store,
		now: func() time.Time { return now },
	}
	read := func() serviceRuntimeResponse {
		t.Helper()
		event, recorder := registryRouteStoreTestEvent(
			http.MethodGet, "/api/v1/registry/services/service-1", "owner-1", "tenant-1", nil)
		event.Request.SetPathValue("serviceId", "service-1")
		if err := h.get(event); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data serviceRuntimeResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", recorder.Body.String(), err)
		}
		return envelope.Data
	}

	before := read()
	if !slices.Equal(before.AllowedActions, []string{"freeze", "logs", "restart"}) ||
		before.MutationLock.State != string(serviceregistry.MutationUnlocked) {
		t.Fatalf("unlocked projection = %#v", before)
	}
	if _, err := store.SetServiceMutationLock(t.Context(), "tenant-1", "service-1",
		controlplane.ServiceMutationLock{
			State: serviceregistry.MutationLocked, ReasonCode: "owner_locked", Actor: "owner-1",
		}, "owner_locked"); err != nil {
		t.Fatal(err)
	}
	after := read()
	if !slices.Equal(after.AllowedActions, []string{"logs", "unfreeze"}) {
		t.Fatalf("locked allowed_actions = %#v", after.AllowedActions)
	}
	if after.MutationLock.State != string(serviceregistry.MutationLocked) ||
		after.MutationLock.Actor != "owner-1" || after.MutationLock.ChangedAt == nil {
		t.Fatalf("locked projection lost its explanation: %#v", after.MutationLock)
	}
	// The measured dimensions stay untouched: a locked service keeps running.
	if after.ObservedState != before.ObservedState || after.Health.State != before.Health.State {
		t.Fatalf("lock collapsed a measured dimension: %#v -> %#v", before, after)
	}
}
