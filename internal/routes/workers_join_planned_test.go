package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestWorkerRegisterAdoptsJoinPlannedServerOnMultiNodeStack(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	plannedID := runtimeidentity.StackServerID("stack-1", "worker-2")
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-existing", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		WorkerID: "guard-existing", LifecycleState: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: plannedID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		NodeID: "worker-2", Name: "82.165.251.178", LifecycleState: "planned",
		ConnectionState: "pending", HealthState: "unknown",
	}); err != nil {
		t.Fatal(err)
	}

	rawToken, tokenHash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "pair-join", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		TokenHash: tokenHash, Status: "active", ExpiresAt: &expiresAt,
		Metadata: map[string]any{
			"spec_node_id":       "worker-2",
			"planned_server_id":  plannedID,
			"server_remote_host": "82.165.251.178",
		},
	}); err != nil {
		t.Fatal(err)
	}

	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"`+rawToken+`","hostname":"remote-node","os":"linux","arch":"amd64"}`,
	)
	if err := (workerRouteHandlers{wst: store, serverStore: store}).register(event); err != nil {
		t.Fatalf("register: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}

	planned, err := store.GetServerRuntime(context.Background(), "tenant-1", plannedID)
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if planned.WorkerID == "" || planned.LifecycleState != "enrolling" {
		t.Fatalf("planned server must bind to the registering worker: %#v", planned)
	}
	servers, err := store.ListServerRuntimesByTenant(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %d, want existing + joined node", len(servers))
	}
}
