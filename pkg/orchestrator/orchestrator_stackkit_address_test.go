package orchestrator

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// The authority adapter has no separate HTTP endpoint. Exercise its actual
// tenant store and canonical runtime boundary, rather than faking its decision.
func TestManagedRouteAuthorityRefusesStaleOrForeignRuntime(t *testing.T) {
	for _, scenario := range []string{"current", "wrong owner", "wrong worker", "stale heartbeat", "wrong spec", "inactive lease", "wrong provider address", "missing provider address", "unleased"} {
		t.Run(scenario, func(t *testing.T) {
			rollout := jobs.StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "bound.json"}
			orch, store := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()})
			now := time.Now().UTC()
			lease := managedDeployLeaseFixture("lease-1", "tenant-1", "owner-1", "stack-1", "foundation", "centron-managed", "203.0.113.10", now)
			serverID := runtimeidentity.LeaseServerID("lease-1")
			runtime := controlplane.ServerRuntime{ID: serverID, LeaseID: "lease-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", Generation: 1, LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now, Metadata: map[string]any{"host": map[string]any{"public_ip": "203.0.113.10"}}}
			req := jobs.StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "home", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", NodeID: serverID, StackKit: "cloud-kit", SpecPath: "bound.json"}
			if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "main", Approved: true, Capabilities: map[string]any{"runtime_agent_id": "agent-1", "server_id": serverID}}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong owner":
				runtime.OwnerSubjectID = "other-owner"
			case "wrong worker":
				runtime.WorkerID = "other-worker"
			case "stale heartbeat":
				now = now.Add(-time.Hour)
			case "wrong spec":
				req.SpecPath = "other.json"
			case "inactive lease":
				runtime.LeaseID = "unadmitted-lease"
			case "wrong provider address":
				lease.Metadata["runtime_public_ip"] = "203.0.113.11"
			case "missing provider address":
				delete(lease.Metadata, "runtime_public_ip")
			case "unleased":
				runtime.LeaseID = ""
			}
			orch.ConfigureManagedRuntimeLeases(fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}})
			if _, err := store.UpsertServerRuntime(t.Context(), runtime); err != nil {
				t.Fatal(err)
			}
			result, err := orch.managedChangeSetAddressAuthority(t.Context(), req)
			if scenario == "unleased" {
				if err != nil || result.PublicIP != "" || result.LeaseID != "" {
					t.Fatalf("unleased runtime acquired provider address authority: %+v, %v", result, err)
				}
				return
			}
			if scenario != "current" {
				if err == nil || result.PublicIP != "" {
					t.Fatalf("invalid authority yielded a registrable runtime: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.PublicIP != "203.0.113.10" || result.ServerID != runtime.ID || result.Rollout != rollout {
				t.Fatalf("current runtime not admitted: %+v, %v", result, err)
			}
		})
	}
}
