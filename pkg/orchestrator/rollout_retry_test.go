package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestRetryRolloutDispatchesOneDeterministicExactDeploy(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-failed", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Failed", Status: "error",
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-failed", TenantID: "tenant-1", StackID: "stack-failed", Type: "deploy", State: "failed",
		Step: "prepare_rollout", Error: "StackKit artifact generation failed",
		Result: map[string]any{"lease_id": "lease-failed"},
	}); err != nil {
		t.Fatal(err)
	}
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-failed", "lease-failed", time.Now().UTC())
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease("lease-failed", "tenant-1", "owner-1", "stack-failed"),
		}},
	}, nil)
	defer orch.Stop()
	req := RolloutRetryRequest{
		RequestContext: ctx, StackID: "stack-failed", TenantID: "tenant-1", OwnerID: "owner-1",
		StackName: "Failed", SourceJobID: "job-failed", LeaseID: "lease-failed",
	}
	issuedBootstraps := 0
	req.IssueOwnerSpecBootstrap = func() (*jobs.OwnerSpecBootstrap, error) {
		issuedBootstraps++
		return &jobs.OwnerSpecBootstrap{Endpoint: "/owner-spec", Token: "one-time"}, nil
	}

	first, err := orch.RetryRollout(req)
	if err != nil {
		t.Fatalf("RetryRollout: %v", err)
	}
	if first.JobID == "" || first.JobID == req.SourceJobID || first.IdempotentReplay {
		t.Fatalf("first = %#v", first)
	}
	queued, err := orch.GetJobStatus(first.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Type != jobs.JobTypeDeploy || queued.Payload["lease_id"] != req.LeaseID ||
		queued.Result[rolloutRetrySourceField] != req.SourceJobID {
		t.Fatalf("exact retry job = %#v", queued)
	}
	second, err := orch.RetryRollout(req)
	if err != nil {
		t.Fatalf("RetryRollout replay: %v", err)
	}
	if second.JobID != first.JobID || !second.IdempotentReplay {
		t.Fatalf("second = %#v, first=%#v", second, first)
	}
	if issuedBootstraps != 1 {
		t.Fatalf("bootstrap issuances = %d, want one for dispatch and none for replay", issuedBootstraps)
	}
	stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-failed", 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored = %#v err=%v", stored, err)
	}
	if _, createErr := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-failed-older", TenantID: "tenant-1", StackID: "stack-failed", Type: "deploy", State: "failed",
		Result: map[string]any{"lease_id": "lease-failed"},
	}); createErr != nil {
		t.Fatal(createErr)
	}
	older := req
	older.SourceJobID = "job-failed-older"
	if _, retryErr := orch.RetryRollout(older); !errors.Is(retryErr, ErrRolloutRetryInvalid) {
		t.Fatalf("second historical retry error = %v, want ErrRolloutRetryInvalid", retryErr)
	}
	if issuedBootstraps != 1 {
		t.Fatalf("rejected historical retry minted bootstrap; issuances=%d", issuedBootstraps)
	}
	stored, err = store.ListJobsByStack(ctx, "tenant-1", "stack-failed", 10)
	if err != nil || len(stored) != 3 {
		t.Fatalf("rejected historical retry created another replacement: %#v err=%v", stored, err)
	}
}

func TestRetryRolloutRejectsNonFailedSource(t *testing.T) {
	err := validateRolloutRetrySource(controlplane.Job{
		StackID: "stack-1", Type: "deploy", State: "running", Result: map[string]any{"lease_id": "lease-1"},
	}, "stack-1", "lease-1", "server")
	if !errors.Is(err, ErrRolloutRetryInvalid) {
		t.Fatalf("error = %v", err)
	}
}
