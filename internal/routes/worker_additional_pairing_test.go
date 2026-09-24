package routes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// Regression: adding a second machine adopted the incumbent worker and returned
// a new credential which was not that worker's persisted runtime credential.
func TestAdditionalNodePairingPreservesIncumbentIdentity(t *testing.T) {
	store := controlplane.NewMemoryStore()
	const oldWorker, oldServer, oldToken = "incumbent-worker", "incumbent-server", "incumbent-runtime-secret"
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: oldWorker, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Hostname: "incumbent", Status: "approved", Approved: true,
		Capabilities: map[string]any{"server_id": oldServer},
		Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(oldToken)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: oldServer, WorkerID: oldWorker, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Name: "incumbent", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	raw, hash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "additional-pairing", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		TokenHash: hash, Status: "active", ExpiresAt: &expires,
	}); err != nil {
		t.Fatal(err)
	}
	h := workerRouteHandlers{wst: store, serverStore: store}
	event, response := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register",
		`{"token":"`+raw+`","hostname":"additional-node","os":"linux","arch":"amd64"}`)
	if err := h.register(event); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("registration status = %d", response.Code)
	}
	var envelope struct {
		Data struct {
			WorkerID   string `json:"worker_id"`
			ServerID   string `json:"server_id"`
			AgentToken string `json:"agent_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.WorkerID == "" || envelope.Data.WorkerID == oldWorker || envelope.Data.ServerID == "" || envelope.Data.ServerID == oldServer {
		t.Fatal("additional machine did not receive its own worker and server identity")
	}
	for _, credential := range []struct{ id, token string }{
		{oldWorker, oldToken}, {envelope.Data.WorkerID, envelope.Data.AgentToken},
	} {
		event, response := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+credential.id+"/heartbeat",
			`{"source_epoch":"additional-pairing-test","source_sequence":1,"observed_at":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
		event.Request.SetPathValue("id", credential.id)
		event.Request.Header.Set("Authorization", "Bearer "+credential.token)
		event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
		if err := h.heartbeat(event); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("heartbeat for %s returned %d", credential.id, response.Code)
		}
	}
}
