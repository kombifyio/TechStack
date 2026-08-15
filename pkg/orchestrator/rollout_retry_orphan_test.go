package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// newOrphanedRolloutFixture builds the shape a stack is left in when its deploy
// job dies with the process: the job is terminal, but nothing projected that
// onto the stack, so the stack status is still the in-progress one.
func newOrphanedRolloutFixture(t *testing.T, stackStatus string) (*Orchestrator, *controlplane.MemoryStore, RolloutRetryRequest) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-orphaned", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Orphaned",
		Status: stackStatus,
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-orphaned", TenantID: "tenant-1", StackID: "stack-orphaned", Type: "deploy", State: "failed",
		Step: "generate_unified", Error: "orphaned by process restart",
		Result: map[string]any{"lease_id": "lease-orphaned"},
	}); err != nil {
		t.Fatal(err)
	}
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-orphaned", "lease-orphaned", time.Now().UTC())
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease("lease-orphaned", "tenant-1", "owner-1", "stack-orphaned"),
		}},
	}, nil)
	t.Cleanup(orch.Stop)
	return orch, store, RolloutRetryRequest{
		RequestContext: ctx, StackID: "stack-orphaned", TenantID: "tenant-1", OwnerID: "owner-1",
		StackName: "Orphaned", SourceJobID: "job-orphaned", LeaseID: "lease-orphaned",
	}
}

// A job that dies with its process never projects "error" onto its stack, which
// left the stack pinned at "provisioning". Gating the retry on that projection
// made the documented recovery path unreachable for exactly the failures it
// exists to recover. The terminal source job plus no in-flight work is enough.
func TestRetryRolloutRecoversStackLeftInProvisioningByAnOrphanedJob(t *testing.T) {
	orch, _, req := newOrphanedRolloutFixture(t, "provisioning")

	result, err := orch.RetryRollout(req)
	if err != nil {
		t.Fatalf("RetryRollout on an orphan-stranded stack: %v", err)
	}
	if result == nil || result.JobID == "" || result.JobID == req.SourceJobID {
		t.Fatalf("result = %#v", result)
	}
	if result.LeaseID != req.LeaseID {
		t.Fatalf("lease = %q, want %q", result.LeaseID, req.LeaseID)
	}
}

// The invariant the old status gate was really protecting: never fan a second
// rollout out beside one that is still in flight.
func TestRetryRolloutRefusesWhileAnotherJobIsInFlight(t *testing.T) {
	for _, state := range []string{"pending", "waiting", "running"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			orch, store, req := newOrphanedRolloutFixture(t, "provisioning")
			// The store only admits a job into "running" through the claim, so
			// seed it the way the queue would rather than writing the state.
			seedState := state
			if state == "running" {
				seedState = "pending"
			}
			jobID := "job-in-flight-" + state
			if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
				ID: jobID, TenantID: "tenant-1", StackID: "stack-orphaned",
				Type: "deploy", State: seedState, Result: map[string]any{"lease_id": "lease-orphaned"},
			}); err != nil {
				t.Fatal(err)
			}
			if state == "running" {
				if _, err := store.StartJob(ctx, "tenant-1", jobID, time.Now().UTC()); err != nil {
					t.Fatalf("start job: %v", err)
				}
			}
			if _, err := orch.RetryRollout(req); !errors.Is(err, ErrRolloutRetryInvalid) {
				t.Fatalf("error = %v, want ErrRolloutRetryInvalid while a %s job is in flight", err, state)
			}
		})
	}
}

// A stack in a status that is neither error nor provisioning is still refused by
// validateExactRetryTarget, so relaxing the projection gate did not open the
// retry to running or stopped stacks.
func TestRetryRolloutStillRefusesUnrelatedStackStatus(t *testing.T) {
	for _, status := range []string{"running", "stopped"} {
		t.Run(status, func(t *testing.T) {
			orch, _, req := newOrphanedRolloutFixture(t, status)
			if _, err := orch.RetryRollout(req); !errors.Is(err, ErrRolloutRetryInvalid) {
				t.Fatalf("error = %v, want ErrRolloutRetryInvalid for stack status %q", err, status)
			}
		})
	}
}
