package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// TestRILServerResponsesReportPersistedObservation locks K2 acceptance item 5:
// the former enrichWithLiveStatus fabricated LastSeenAt=now and status=online
// whenever a gRPC agent was connected. RIL responses now report the persisted
// observed state only.
func TestRILServerResponsesReportPersistedObservation(t *testing.T) {
	store := controlplane.NewMemoryStore()
	lastSeen := time.Date(2026, 8, 12, 7, 30, 0, 0, time.UTC)
	if _, err := store.UpsertRILServer(context.Background(), controlplane.RILServer{
		ID: "server-1", TenantID: "tenant-1", NodeID: "server-1", Name: "runtime-1",
		Status: "stale", LastSeenAt: &lastSeen,
		Health: map[string]any{"runtime_agent_id": "guard-1"},
	}); err != nil {
		t.Fatalf("UpsertRILServer: %v", err)
	}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/ril/servers", "owner-1", "tenant-1", nil)
	if err := (rilServerHandler{store: store}).listServers(event); err != nil {
		t.Fatalf("listServers: %v", err)
	}
	var envelope struct {
		Data []RILServerResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("servers = %#v, want one row", envelope.Data)
	}
	got := envelope.Data[0]
	if got.Status != "stale" {
		t.Fatalf("status = %q, want the persisted %q", got.Status, "stale")
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(lastSeen) {
		t.Fatalf("last_seen_at = %v, want the persisted observation %v", got.LastSeenAt, lastSeen)
	}
}

// TestRILServerProjectionKeepsPersistedLastSeen guards the projection helper
// directly: no wall-clock substitution of the observation timestamp.
func TestRILServerProjectionKeepsPersistedLastSeen(t *testing.T) {
	lastSeen := time.Date(2026, 8, 12, 7, 30, 0, 0, time.UTC)
	resp := rilServerToResponse(controlplane.RILServer{
		ID: "server-1", NodeID: "server-1", Name: "runtime-1",
		Status: "offline", LastSeenAt: &lastSeen,
	})
	if resp.Status != "offline" || resp.LastSeenAt == nil || !resp.LastSeenAt.Equal(lastSeen) {
		t.Fatalf("projection fabricated liveness: %#v", resp)
	}
}
