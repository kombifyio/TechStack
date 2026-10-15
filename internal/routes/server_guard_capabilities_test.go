package routes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// The canonical server response publishes the Guard facts the control plane
// persisted for that server's worker, including the credential generation the
// fenced rotation must echo, and nothing that authenticates: no token hash, no
// key. Another tenant reads none of it, not even through a server row that
// names the first tenant's worker.
func TestServerReadModelPublishesGuardCapabilitiesWithinTheTenant(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "connected", Approved: true,
		Capabilities: map[string]any{
			"identity_binding": controlplane.IdentityBindingHostKey,
			"guard_operations": map[string]any{
				"contract": "kombify.guard-operations/v1", "journal": "ok", "durable": false, "unresolved_outcome_unknown": 2,
				"unacked_results": 1, "in_flight": 0, "journal_bytes": 4096, "journal_budget_bytes": 1048576,
			},
			"guard_posture": map[string]any{"mode": "split", "reported": true, "deny_mutations": false, "update_state": "update_rolled_back", "update_version": "v9.9.9"},
		},
		Resources: map[string]any{
			controlplane.WorkerCredentialGenerationResourceKey:  3,
			controlplane.WorkerCredentialTokenSHA256ResourceKey: strings.Repeat("a", 64),
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", Name: "Node", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy"},
		// A server of another tenant that names the first tenant's worker.
		{ID: "server-x", TenantID: "tenant-2", OwnerSubjectID: "owner-2", WorkerID: "worker-1", Name: "Probe", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy"},
	} {
		if _, err := store.UpsertServerRuntime(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	h := serverRuntimeHandlers{store: store, workers: store, now: func() time.Time { return now }}
	read := func(owner, tenant, path, id string) (int, string) {
		event, recorder := registryRouteStoreTestEvent(http.MethodGet, path, owner, tenant, nil)
		if id != "" {
			event.Request.SetPathValue("serverId", id)
			_ = h.get(event)
		} else {
			_ = h.list(event)
		}
		return recorder.Code, recorder.Body.String()
	}

	code, body := read("owner-1", "tenant-1", "/api/v1/servers/server-1", "server-1")
	if code != http.StatusOK {
		t.Fatalf("owner read %d %s", code, body)
	}
	var envelope struct {
		Data serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	c := envelope.Data.Capabilities
	if c == nil || c.IdentityBinding != serverIdentityKeyBound || c.CredentialGeneration != 3 {
		t.Fatalf("capabilities = %+v", c)
	}
	if c.GuardOperations == nil || c.GuardOperations.State != "ok" || c.GuardOperations.Durable || c.GuardOperations.UnresolvedOutcomeUnknown != 2 || c.GuardOperations.UnackedResults != 1 {
		t.Fatalf("guard operations = %+v", c.GuardOperations)
	}
	if c.GuardPosture == nil || c.GuardPosture.Mode != "split" || c.GuardPosture.UpdateState != "update_rolled_back" || c.GuardPosture.UpdateVersion != "v9.9.9" {
		t.Fatalf("guard posture = %+v", c.GuardPosture)
	}
	if strings.Contains(body, "agent_token") || strings.Contains(body, strings.Repeat("a", 64)) {
		t.Fatalf("the response carries credential material: %s", body)
	}

	// Another tenant: its own list carries no capabilities for the foreign
	// worker id, and it cannot read the first tenant's server at all.
	code, body = read("owner-2", "tenant-2", "/api/v1/servers", "")
	if code != http.StatusOK || strings.Contains(body, "identity_binding") || strings.Contains(body, "credential_generation\":3") {
		t.Fatalf("another tenant's list read the first tenant's worker: %d %s", code, body)
	}
	if code, _ = read("owner-2", "tenant-2", "/api/v1/servers/server-x", "server-x"); code != http.StatusOK {
		t.Fatalf("own server read %d", code)
	}
	if _, body = read("owner-2", "tenant-2", "/api/v1/servers/server-x", "server-x"); strings.Contains(body, "identity_binding") {
		t.Fatalf("another tenant's detail read the first tenant's worker: %s", body)
	}
	if code, _ = read("owner-2", "tenant-2", "/api/v1/servers/server-1", "server-1"); code != http.StatusNotFound {
		t.Fatalf("another tenant read the first tenant's server: %d", code)
	}
}
