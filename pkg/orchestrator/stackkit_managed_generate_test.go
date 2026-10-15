package orchestrator

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// The public enqueue path must replace caller-supplied managed claims with the
// approved canonical server, and bind its PLAN to the stored kit instance.
func TestEnqueueManagedGenerationUsesCanonicalRuntimeCustody(t *testing.T) {
	for _, scenario := range []string{"leased", "unleased", "foreign-owner"} {
		t.Run(scenario, func(t *testing.T) {
			rollout := jobs.StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "bound.json"}
			orch, store := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()})
			serverID := runtimeidentity.LeaseServerID("lease-1")
			if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "main", Approved: true, Capabilities: map[string]any{"runtime_agent_id": "agent-1", "server_id": serverID, "node_id": "cloud-main"}}); err != nil {
				t.Fatal(err)
			}
			runtime := controlplane.ServerRuntime{ID: serverID, LeaseID: "lease-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", Generation: 1}
			if scenario == "unleased" {
				runtime.LeaseID = ""
			}
			if scenario == "foreign-owner" {
				runtime.OwnerSubjectID = "other-owner"
			}
			if _, err := store.UpsertServerRuntime(t.Context(), runtime); err != nil {
				t.Fatal(err)
			}
			id, err := orch.EnqueueStackKitLifecycle(t.Context(), jobs.StackKitLifecycleRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: jobs.StackKitLifecycleGenerate, ManagedRuntimeLeaseID: "caller-lease", ManagedRuntimeServerID: "caller-server"})
			if scenario == "foreign-owner" {
				if err == nil {
					t.Fatal("foreign server ownership reached generation enqueue")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			job, ok := orch.Queue().Get(id)
			if !ok {
				t.Fatal("accepted generation was not queued")
			}
			if job.Payload["managed_runtime_lease_id"] != runtime.LeaseID || job.Payload["managed_runtime_server_id"] != runtime.ID || job.Payload["node_id"] != "cloud-main" || job.Payload["spec_path"] != rollout.SpecPath {
				t.Fatal("generation did not retain canonical runtime custody and node document")
			}
			if scenario == "leased" && job.Payload["stackkit_instance_id"] != "home" {
				t.Fatal("managed generation did not bind the stored kit for its inspected PLAN")
			}
			if scenario == "unleased" && job.Payload["stackkit_instance_id"] != "" {
				t.Fatal("unleased generation changed its existing kit selection")
			}
		})
	}
}
