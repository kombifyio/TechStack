package routes

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

func TestStackOperationsServiceObservationEvidenceContract(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339Nano)

	tests := []struct {
		name       string
		item       map[string]any
		observedAt string
		status     string
		host       any
		docker     any
		want       string
	}{
		{
			name:       "fresh healthy observation",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       "healthy",
		},
		{
			name:       "fresh starting observation",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "starting",
			want:       "starting",
		},
		{
			name:       "stale observation",
			observedAt: now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "offline observation",
			observedAt: now.Add(-runtimehealth.StaleHeartbeatWindow - time.Second).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "future observation outside skew allowance",
			observedAt: now.Add(time.Minute + time.Nanosecond).Format(time.RFC3339Nano),
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "invalid observation timestamp",
			observedAt: "not-a-timestamp",
			status:     "healthy",
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable host downgrades healthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			host:       false,
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable docker downgrades healthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "healthy",
			docker:     false,
			want:       registryUnknownStatus,
		},
		{
			name:       "unreachable host preserves measured unhealthy service",
			observedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			status:     "unhealthy",
			host:       false,
			want:       "unhealthy",
		},
		{name: "ordinary action output", item: map[string]any{"status": "healthy", "observed_at": fresh}, want: registryUnknownStatus},
		{name: "observation flag is false", item: map[string]any{"runtime_observation": false, "observation_version": stackKitRuntimeObservationV1, "observed_at": fresh, "status": "healthy"}, want: registryUnknownStatus},
		{name: "unknown observation version", item: map[string]any{"runtime_observation": true, "observation_version": "stackkit.runtime-observation/v3", "observed_at": fresh, "status": "healthy"}, want: registryUnknownStatus},
		{name: "unsupported service status", item: map[string]any{"runtime_observation": true, "observation_version": stackKitRuntimeObservationV1, "observed_at": fresh, "status": "running"}, want: registryUnknownStatus},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := tt.item
			if item == nil {
				item = map[string]any{
					"runtime_observation": true,
					"observation_version": stackKitRuntimeObservationV1,
					"observed_at":         tt.observedAt,
					"status":              tt.status,
				}
			}
			if tt.host != nil {
				item["observation_host_reachable"] = tt.host
			}
			if tt.docker != nil {
				item["observation_docker_reachable"] = tt.docker
			}

			if got := stackKitObservedServiceStatus(item, now); got != tt.want {
				t.Fatalf("stackKitObservedServiceStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStackOperationsServiceRegistryHeartbeatAndSourceHonesty(t *testing.T) {
	servers := []stackOperationServer{
		{ID: "node-healthy", AgentID: "agent-healthy", Hostname: "healthy", Health: stackServerHealth{State: "healthy"}},
		{ID: "node-degraded", AgentID: "agent-degraded", Hostname: "degraded", Health: stackServerHealth{State: "degraded"}},
		{ID: "node-stale", AgentID: "agent-stale", Hostname: "stale", Health: stackServerHealth{State: "stale"}},
	}

	for _, serverID := range []string{"node-healthy", "agent-healthy", "node-degraded", "agent-degraded"} {
		if !operationServerHasCurrentHeartbeat(servers, serverID) {
			t.Fatalf("operationServerHasCurrentHeartbeat(%q) = false, want true", serverID)
		}
	}
	for _, serverID := range []string{"node-stale", "agent-stale", "missing"} {
		if operationServerHasCurrentHeartbeat(servers, serverID) {
			t.Fatalf("operationServerHasCurrentHeartbeat(%q) = true, want false", serverID)
		}
	}

	node := controlplane.Node{ID: "node-healthy", WorkerID: "agent-healthy", Name: "healthy"}
	base := controlplane.Service{
		ID: "service-1", NodeID: node.ID, ServiceKey: "immich", Status: "healthy",
		URL: "https://immich.example.test", Metadata: map[string]any{"observed_at": time.Now().UTC().Format(time.RFC3339Nano)},
	}

	actionOutput := base
	actionOutput.Source = stackKitOutputKey
	if got := operationServiceFromControlPlane(actionOutput, node, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("action-output projection = %#v, want unknown without access", got)
	}

	inventory := base
	inventory.Source = stackKitsInventorySource
	got := operationServiceFromControlPlane(inventory, node, servers)
	if got.Status != "healthy" || got.URL != "https://immich.example.test" || got.TargetServerID != node.ID || got.TargetServer != node.Name {
		t.Fatalf("current inventory projection = %#v, want live status and resolved target", got)
	}

	staleNode := controlplane.Node{ID: "node-stale", Name: "stale"}
	inventory.NodeID = staleNode.ID
	if got := operationServiceFromControlPlane(inventory, staleNode, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("stale inventory projection = %#v, want unknown without access", got)
	}

	staleObservation := inventory
	staleObservation.NodeID = node.ID
	staleObservation.Metadata = map[string]any{"observed_at": time.Now().UTC().Add(-runtimehealth.FreshHeartbeatWindow - time.Second).Format(time.RFC3339Nano)}
	if got := operationServiceFromControlPlane(staleObservation, node, servers); got.Status != registryUnknownStatus || got.URL != "" {
		t.Fatalf("stale service observation projection = %#v, want unknown without access", got)
	}
}

type stackOperationsRegistryStoreStub struct {
	controlplane.RegistryStore
	nodes       []controlplane.Node
	services    []controlplane.Service
	nodesErr    error
	servicesErr error
}

func (s stackOperationsRegistryStoreStub) ListNodesByStack(context.Context, string, string) ([]controlplane.Node, error) {
	return s.nodes, s.nodesErr
}

func (s stackOperationsRegistryStoreStub) ListServicesByStack(context.Context, string, string) ([]controlplane.Service, error) {
	return s.services, s.servicesErr
}
