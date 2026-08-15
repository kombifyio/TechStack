package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// TestServerRuntimeRoutesReturnPersistedStateAndOwnerIsolation locks the
// honest read model: the API returns the PERSISTED connection/health
// dimensions. Heartbeat-freshness demotion is the registry sweeper's job (a
// durable write through ApplyServerEvent), never a read-time recompute.
func TestServerRuntimeRoutesReturnPersistedStateAndOwnerIsolation(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-2 * time.Minute)
	for _, server := range []controlplane.ServerRuntime{
		{ID: "owner-server", TenantID: "tenant-1", StackID: "techstack-owner", OwnerSubjectID: "owner-1", Name: "Owner", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &heartbeat},
		{ID: "removed-server", TenantID: "tenant-1", StackID: "techstack-owner", OwnerSubjectID: "owner-1", Name: "Removed", LifecycleState: "decommissioned", DesiredState: "absent"},
		{ID: "other-server", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Other", LifecycleState: "active"},
	} {
		if _, err := store.UpsertServerRuntime(context.Background(), server); err != nil {
			t.Fatalf("UpsertServerRuntime: %v", err)
		}
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers?techstack_id=techstack-owner", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: func() time.Time { return now }}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "owner-server" {
		t.Fatalf("owner projection leaked rows: %#v", envelope.Data)
	}
	if envelope.Data[0].TechstackID != "techstack-owner" {
		t.Fatalf("techstack_id = %q, want techstack-owner", envelope.Data[0].TechstackID)
	}
	if strings.Contains(recorder.Body.String(), `"stack_id"`) {
		t.Fatalf("canonical server response exposed ambiguous stack_id: %s", recorder.Body.String())
	}
	// Persisted connected/healthy is returned verbatim even though the
	// heartbeat aged past the fresh window; staleness_seconds still exposes
	// the raw evidence age for clients.
	got := envelope.Data[0]
	if got.Connection.State != "connected" || got.Health.State != "healthy" || !got.MutationsAllowed {
		t.Fatalf("read path overrode persisted state: %#v", got)
	}
	if got.Connection.StalenessSeconds == nil || *got.Connection.StalenessSeconds != 120 {
		t.Fatalf("staleness seconds = %#v, want 120", got.Connection.StalenessSeconds)
	}
}

// TestServerRuntimeRoutesReturnSweeperDemotedState: once the sweeper persisted
// a demotion, the API reflects it (no read-time promotion either).
func TestServerRuntimeRoutesReturnSweeperDemotedState(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-10 * time.Second)
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "owner-server", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner",
		LifecycleState: "active", ConnectionState: "stale", HealthState: "unknown",
		ReasonCode: "heartbeat_stale", LastHeartbeatAt: &heartbeat,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: func() time.Time { return now }}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("unexpected rows: %#v", envelope.Data)
	}
	got := envelope.Data[0]
	if got.Connection.State != "stale" || got.Health.State != "unknown" || got.MutationsAllowed {
		t.Fatalf("persisted demotion was not returned: %#v", got)
	}
}

func TestServerRuntimeDetailHidesForeignServer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign",
	}); err != nil {
		t.Fatal(err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/foreign", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "foreign")
	if err := (serverRuntimeHandlers{store: store, now: time.Now}).get(event); err != nil {
		t.Fatalf("get: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
