package stacks

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/specv2"
)

func TestPersistJoinServerIntentReservesPlannedNodeOnMultiNodeStack(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{serverStore: store}
	stack := &controlplane.Stack{ID: "stack-join", TenantID: "tenant-1"}
	projection := &specv2.Projection{NodeID: "worker-2"}

	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-existing", TenantID: "tenant-1", StackID: "stack-join", OwnerSubjectID: "auth0|user-1",
		WorkerID: "guard-existing", LifecycleState: "active",
	}); err != nil {
		t.Fatalf("seed existing server: %v", err)
	}

	serverID, err := h.persistJoinServerIntent(
		context.Background(),
		"auth0|user-1",
		"tenant-1",
		stack,
		projection,
		specv2.TransportConnectRemote,
		"82.165.251.178",
	)
	if err != nil {
		t.Fatalf("persistJoinServerIntent: %v", err)
	}
	wantID := runtimeidentity.StackServerID("stack-join", "worker-2")
	if serverID != wantID {
		t.Fatalf("serverID = %q, want %q", serverID, wantID)
	}

	servers, err := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", "stack-join")
	if err != nil {
		t.Fatalf("ListServerRuntimesByTenant: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %d, want existing + planned join node", len(servers))
	}
	planned, err := store.GetServerRuntime(t.Context(), "tenant-1", wantID)
	if err != nil {
		t.Fatalf("GetServerRuntime planned: %v", err)
	}
	if planned.LifecycleState != "planned" || planned.WorkerID != "" || planned.Name != "82.165.251.178" {
		t.Fatalf("planned join server = %#v", planned)
	}
}

func TestPersistJoinServerIntentIsIdempotent(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{serverStore: store}
	stack := &controlplane.Stack{ID: "stack-join", TenantID: "tenant-1"}
	projection := &specv2.Projection{NodeID: "worker-2"}

	first, err := h.persistJoinServerIntent(context.Background(), "auth0|user-1", "tenant-1", stack, projection, "install-command", "")
	if err != nil {
		t.Fatalf("first persistJoinServerIntent: %v", err)
	}
	second, err := h.persistJoinServerIntent(context.Background(), "auth0|user-1", "tenant-1", stack, projection, "install-command", "")
	if err != nil {
		t.Fatalf("second persistJoinServerIntent: %v", err)
	}
	if first != second {
		t.Fatalf("idempotent server ids = %q vs %q", first, second)
	}
	servers, err := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", "stack-join")
	if err != nil {
		t.Fatalf("ListServerRuntimesByTenant: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
}
