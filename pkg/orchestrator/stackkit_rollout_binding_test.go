package orchestrator

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func newRolledOutStack(t *testing.T, runtimeSummary map[string]any) (*Orchestrator, *controlplane.MemoryStore) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Home",
		StackKitInstanceID: "home", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateStackRuntime(ctx, "tenant-1", "stack-1", controlplane.RuntimeUpdate{
		Status: "running", RuntimeSummary: runtimeSummary,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "main",
		Approved: true, Capabilities: map[string]any{"runtime_agent_id": "agent-1", "server_id": "server-1"},
	}); err != nil {
		t.Fatal(err)
	}
	orch := New(&Config{Workers: 0, StackStore: store, JobStore: store, WorkerStore: store}, nil)
	t.Cleanup(orch.Stop)
	return orch, store
}

func enqueuedLifecyclePayload(t *testing.T, orch *Orchestrator, operation string) map[string]interface{} {
	t.Helper()
	jobID, err := orch.EnqueueStackKitLifecycle(context.Background(), jobs.StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: operation,
	})
	if err != nil {
		t.Fatalf("EnqueueStackKitLifecycle(%s): %v", operation, err)
	}
	job, ok := orch.Queue().Get(jobID)
	if !ok {
		t.Fatalf("job %s is not queued", jobID)
	}
	return job.Payload
}

// Regression (managed Basement lab lane, attempt ceb2bf3e): verify after a
// managed rollout read the CLI default stack-spec.yaml, which the rollout never
// wrote, and failed with "canonical StackSpec v2 is required".
func TestVerifyAfterManagedRolloutUsesTheRolloutSpecPath(t *testing.T) {
	orch, _ := newRolledOutStack(t, nil)
	binding := jobs.StackKitRolloutBinding{StackKit: "basement-kit", SpecPath: "stack-spec.v2.json"}
	orch.updateStackStatusSnapshot((&jobs.Job{
		ID: "job-deploy", Type: jobs.JobTypeDeploy, TargetID: "stack-1", State: jobs.JobStateCompleted,
		Payload: map[string]interface{}{stackTenantIDField: "tenant-1", stackOwnerIDField: "owner-1"},
		Result:  map[string]interface{}{jobs.StackKitRolloutBindingResultField: binding.Map()},
	}).Snapshot())
	// A later failed operation replaces the runtime summary; the binding must survive it.
	orch.updateStackStatusSnapshot((&jobs.Job{
		ID: "job-verify-failed", Type: jobs.JobTypeStackKitLifecycle, TargetID: "stack-1", State: jobs.JobStateFailed,
		Payload: map[string]interface{}{stackTenantIDField: "tenant-1", stackOwnerIDField: "owner-1"},
		Result:  map[string]interface{}{"operation": "verify"},
	}).Snapshot())

	payload := enqueuedLifecyclePayload(t, orch, jobs.StackKitLifecycleVerify)
	if payload["spec_path"] != "stack-spec.v2.json" {
		t.Fatalf("verify spec_path = %v, want the rollout's stack-spec.v2.json", payload["spec_path"])
	}
}

// Regression (same lane): an operation request without "stackkit" failed with
// `no local execution binding is known for StackKit ""`.
func TestOperationWithoutStackKitUsesTheStackRecordKit(t *testing.T) {
	orch, _ := newRolledOutStack(t, map[string]any{"stackkit_catalog_ref": "basement-kit"})
	payload := enqueuedLifecyclePayload(t, orch, jobs.StackKitLifecycleDriftDetect)
	if payload["stackkit"] != "basement-kit" {
		t.Fatalf("drift_detect stackkit = %v, want basement-kit", payload["stackkit"])
	}
}
