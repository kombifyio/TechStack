package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// parityFixture is one seeded canonical server plus the legacy `nodes` row the
// pre-aggregate projection was historically composed from.
type parityFixture struct {
	name string
	// runtime is the canonical serverregistry aggregate.
	runtime controlplane.ServerRuntime
	// node is the legacy row; a zero NodeID means the server exists only in the
	// canonical read model and must still appear in the legacy list.
	node *controlplane.Node
	// wantLegacyState is the documented collapsed legacy state.
	wantLegacyState  runtimehealth.ServerState
	wantRolloutReady bool
}

// TestServerReadModelParityBetweenCanonicalAndLegacyRoutes seeds one fixture
// set and asserts that /api/v1/servers (canonical) and /api/v1/registry/servers
// (legacy, UI-facing) agree on every shared semantic field. Where the legacy
// shape intentionally differs, the documented mapping is asserted explicitly.
// This is the phase-A guarantee for kombify-Techstack-nzy1.4: the Wave-2 UI
// cutover (nzy1.7) can switch routes without any data change.
func TestServerReadModelParityBetweenCanonicalAndLegacyRoutes(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-30 * time.Second)
	agedHeartbeat := now.Add(-42 * time.Minute)

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-parity", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Parity stack", Status: "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	fixtures := []parityFixture{
		{
			name: "connected healthy active",
			runtime: controlplane.ServerRuntime{
				ID: "server-connected", Name: "Connected primary", WorkerID: "agent-connected", LeaseID: "lease-connected",
				LifecycleState: "active", DesiredState: "running", ConnectionState: "connected", HealthState: "healthy",
				LastHeartbeatAt: &heartbeat,
			},
			node:             &controlplane.Node{ID: "server-connected", Name: "connected-host", Role: "foundation", WorkerID: "agent-connected"},
			wantLegacyState:  runtimehealth.ServerHealthy,
			wantRolloutReady: true,
		},
		{
			name: "connected but observed unhealthy collapses to degraded",
			runtime: controlplane.ServerRuntime{
				ID: "server-unhealthy", Name: "Unhealthy primary", WorkerID: "agent-unhealthy",
				LifecycleState: "active", DesiredState: "running", ConnectionState: "connected", HealthState: "unhealthy",
				LastHeartbeatAt: &heartbeat,
			},
			node:            &controlplane.Node{ID: "server-unhealthy", Name: "unhealthy-host", Role: "worker", WorkerID: "agent-unhealthy"},
			wantLegacyState: runtimehealth.ServerDegraded,
		},
		{
			name: "degraded connection",
			runtime: controlplane.ServerRuntime{
				ID: "server-degraded", Name: "Degraded primary", WorkerID: "agent-degraded",
				LifecycleState: "active", DesiredState: "running", ConnectionState: "degraded", HealthState: "degraded",
				LastHeartbeatAt: &heartbeat,
			},
			node:            &controlplane.Node{ID: "server-degraded", Name: "degraded-host", Role: "foundation", WorkerID: "agent-degraded"},
			wantLegacyState: runtimehealth.ServerDegraded,
		},
		{
			name: "sweeper demoted to stale",
			runtime: controlplane.ServerRuntime{
				ID: "server-stale", Name: "Stale primary", WorkerID: "agent-stale",
				LifecycleState: "active", DesiredState: "running", ConnectionState: "stale", HealthState: "unknown",
				ReasonCode: "heartbeat_stale", LastHeartbeatAt: &agedHeartbeat,
			},
			node:            &controlplane.Node{ID: "server-stale", Name: "stale-host", Role: "foundation", WorkerID: "agent-stale"},
			wantLegacyState: runtimehealth.ServerStale,
		},
		{
			name: "sweeper demoted to offline",
			runtime: controlplane.ServerRuntime{
				ID: "server-offline", Name: "Offline primary", WorkerID: "agent-offline",
				LifecycleState: "active", DesiredState: "running", ConnectionState: "offline", HealthState: "unknown",
				ReasonCode: "heartbeat_expired", LastHeartbeatAt: &agedHeartbeat,
			},
			node:            &controlplane.Node{ID: "server-offline", Name: "offline-host", Role: "foundation", WorkerID: "agent-offline"},
			wantLegacyState: runtimehealth.ServerOffline,
		},
		{
			name: "revoked enrollment has no legacy vocabulary and reports offline",
			runtime: controlplane.ServerRuntime{
				ID: "server-revoked", Name: "Revoked primary", WorkerID: "agent-revoked",
				LifecycleState: "failed", DesiredState: "running", ConnectionState: "revoked", HealthState: "unknown",
				ReasonCode: "enrollment_revoked",
			},
			node:            &controlplane.Node{ID: "server-revoked", Name: "revoked-host", Role: "foundation", WorkerID: "agent-revoked"},
			wantLegacyState: runtimehealth.ServerOffline,
		},
		{
			// No legacy node: the canonical read model alone must carry the row
			// into the legacy list, otherwise the UI cutover is not a pure
			// client change.
			name: "canonical only pending server",
			runtime: controlplane.ServerRuntime{
				ID: "server-pending", Name: "Pending primary", LeaseID: "lease-pending",
				LifecycleState: "planned", DesiredState: "running", ConnectionState: "pending", HealthState: "unknown",
				ReasonCode: "awaiting_server_allocation",
			},
			wantLegacyState: runtimehealth.ServerProvisioned,
		},
	}

	for _, fixture := range fixtures {
		runtime := fixture.runtime
		runtime.TenantID, runtime.StackID, runtime.OwnerSubjectID = "tenant-1", "stack-parity", "owner-1"
		if _, err := store.UpsertServerRuntime(ctx, runtime); err != nil {
			t.Fatalf("%s: UpsertServerRuntime: %v", fixture.name, err)
		}
		if fixture.node == nil {
			continue
		}
		node := *fixture.node
		node.TenantID, node.StackID = "tenant-1", "stack-parity"
		if _, err := store.UpsertNode(ctx, node); err != nil {
			t.Fatalf("%s: UpsertNode: %v", fixture.name, err)
		}
	}

	canonical := canonicalServersByID(t, store, now)
	legacy := legacyRegistryServersByID(t, store)

	if len(canonical) != len(fixtures) || len(legacy) != len(fixtures) {
		t.Fatalf("server set mismatch: canonical=%d legacy=%d fixtures=%d", len(canonical), len(legacy), len(fixtures))
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			id := fixture.runtime.ID
			canonicalServer, ok := canonical[id]
			if !ok {
				t.Fatalf("/api/v1/servers is missing %q", id)
			}
			legacyServer, ok := legacy[id]
			if !ok {
				t.Fatalf("/api/v1/registry/servers is missing %q", id)
			}

			// Identical fields: id, name, stack association, lease.
			if legacyServer.ID != canonicalServer.ID {
				t.Fatalf("id: legacy=%q canonical=%q", legacyServer.ID, canonicalServer.ID)
			}
			if legacyServer.Name != canonicalServer.Name {
				t.Fatalf("name: legacy=%q canonical=%q", legacyServer.Name, canonicalServer.Name)
			}
			if legacyServer.StackID != canonicalServer.TechstackID {
				t.Fatalf("techstack_id: legacy=%q canonical=%q", legacyServer.StackID, canonicalServer.TechstackID)
			}
			if legacyServer.LeaseID != canonicalServer.Provider.LeaseID {
				t.Fatalf("lease_id: legacy=%q canonical provider.lease_id=%q", legacyServer.LeaseID, canonicalServer.Provider.LeaseID)
			}
			// worker_id has no canonical wire field yet; it must still equal the
			// aggregate's WorkerID rather than a legacy node/worker guess.
			if legacyServer.WorkerID != fixture.runtime.WorkerID {
				t.Fatalf("worker_id: legacy=%q aggregate=%q", legacyServer.WorkerID, fixture.runtime.WorkerID)
			}

			// Documented format difference: the legacy shape renders the
			// heartbeat as an RFC3339Nano string in `last_seen`; the canonical
			// shape carries the same instant as connection.last_heartbeat_at.
			wantLastSeen := formatOptionalTime(canonicalServer.Connection.LastHeartbeatAt)
			if legacyServer.LastSeen != wantLastSeen {
				t.Fatalf("last_seen: legacy=%q canonical connection.last_heartbeat_at=%v", legacyServer.LastSeen, canonicalServer.Connection.LastHeartbeatAt)
			}

			// Documented state collapse: the legacy shape has one state field
			// (published twice as `status` and `health_state`) where the
			// canonical shape keeps connection and health orthogonal.
			wantState := string(serverregistry.LegacyServerState(canonicalServer.Connection.State, canonicalServer.Health.State))
			if wantState != string(fixture.wantLegacyState) {
				t.Fatalf("mapping drift: LegacyServerState(%q,%q)=%q want %q",
					canonicalServer.Connection.State, canonicalServer.Health.State, wantState, fixture.wantLegacyState)
			}
			if legacyServer.Status != wantState || legacyServer.HealthState != wantState {
				t.Fatalf("status/health_state: legacy=(%q,%q) want %q", legacyServer.Status, legacyServer.HealthState, wantState)
			}

			// Lifecycle parity: the legacy shape has no lifecycle field, so the
			// only lifecycle-derived legacy field is rollout_ready.
			if canonicalServer.Lifecycle.State != fixture.runtime.LifecycleState {
				t.Fatalf("lifecycle.state: canonical=%q seeded=%q", canonicalServer.Lifecycle.State, fixture.runtime.LifecycleState)
			}
			if legacyServer.RolloutReady != fixture.wantRolloutReady {
				t.Fatalf("rollout_ready = %v, want %v", legacyServer.RolloutReady, fixture.wantRolloutReady)
			}
			// rollout_ready is intentionally stricter than mutations_allowed:
			// mutations are also allowed on a degraded connection.
			if legacyServer.RolloutReady && !canonicalServer.MutationsAllowed {
				t.Fatalf("rollout_ready must imply canonical mutations_allowed: %#v", legacyServer)
			}
		})
	}
}

// TestLegacyServerStateIsExactInverseOfDeriveObservedState proves the mapping
// used by the legacy routes is not a second, independently drifting derivation:
// collapsing the canonical dimensions reproduces exactly what the pre-aggregate
// routes used to compute directly from the heartbeat.
func TestLegacyServerStateIsExactInverseOfDeriveObservedState(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-2 * time.Minute)
	offline := now.Add(-30 * time.Minute)
	for _, testCase := range []struct {
		name          string
		heartbeatAt   *time.Time
		observedState string
	}{
		{name: "no heartbeat", heartbeatAt: nil},
		{name: "fresh heartbeat", heartbeatAt: &fresh},
		{name: "fresh heartbeat degraded host", heartbeatAt: &fresh, observedState: "degraded"},
		{name: "stale heartbeat", heartbeatAt: &stale},
		{name: "expired heartbeat", heartbeatAt: &offline},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			want := runtimehealth.DeriveServerState(runtimehealth.ServerInput{
				Now: now, HeartbeatAt: testCase.heartbeatAt, ObservedState: testCase.observedState,
			})
			connection, health := serverregistry.DeriveObservedState(now, testCase.heartbeatAt, testCase.observedState)
			if got := serverregistry.LegacyServerState(string(connection), string(health)); got != want {
				t.Fatalf("LegacyServerState(%q,%q) = %q, want %q", connection, health, got, want)
			}
		})
	}
}

func canonicalServersByID(t *testing.T, store *controlplane.MemoryStore, now time.Time) map[string]serverRuntimeResponse {
	t.Helper()
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: func() time.Time { return now }}).list(event); err != nil {
		t.Fatalf("canonical list: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("canonical status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode canonical response: %v", err)
	}
	byID := make(map[string]serverRuntimeResponse, len(envelope.Data))
	for _, server := range envelope.Data {
		byID[server.ID] = server
	}
	return byID
}

func legacyRegistryServersByID(t *testing.T, store *controlplane.MemoryStore) map[string]registryServer {
	t.Helper()
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/servers", "owner-1", "tenant-1", nil)
	handlers := registryRouteHandlers{stackStore: store, workerStore: store, registryStore: store, serverStore: store}
	if err := handlers.servers(event); err != nil {
		t.Fatalf("legacy servers: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Servers []registryServer `json:"servers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode legacy response: %v", err)
	}
	byID := make(map[string]registryServer, len(envelope.Data.Servers))
	for _, server := range envelope.Data.Servers {
		byID[server.ID] = server
	}
	return byID
}
