package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func newAbandonFixture(t *testing.T, jobType string, silentFor time.Duration) (*Orchestrator, *controlplane.MemoryStore, AbandonStaleJobRequest) {
	return newAbandonFixtureWithResult(t, jobType, silentFor, nil)
}

func newAbandonFixtureWithResult(t *testing.T, jobType string, silentFor time.Duration, result map[string]any) (*Orchestrator, *controlplane.MemoryStore, AbandonStaleJobRequest) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	store.SetNow(func() time.Time { return now.Add(-silentFor) })

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-stranded", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stranded",
		Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-stranded", TenantID: "tenant-1", StackID: "stack-stranded", Type: jobType,
		State: "pending", Step: "generate_unified", Result: result,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-stranded", now.Add(-silentFor)); err != nil {
		t.Fatal(err)
	}
	store.SetNow(func() time.Time { return now })

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
	}, nil)
	t.Cleanup(orch.Stop)
	return orch, store, AbandonStaleJobRequest{
		RequestContext: ctx, TenantID: "tenant-1", StackID: "stack-stranded", JobID: "job-stranded",
	}
}

func TestAbandonStaleJobRetiresProvisionAfterPreparedLeaseGuardWait(t *testing.T) {
	orch, store, req := newAbandonFixtureWithResult(t, "provision", time.Minute, map[string]any{
		"lease_id":      "lease-prepared",
		"runtime_phase": string(jobs.RuntimePhaseLeaseReady),
		"job_wait": map[string]any{
			"state": "waiting", "reason": jobs.WaitReasonCanonicalGuardEvidence,
			"next_resume_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		},
	})

	if _, err := orch.AbandonStaleJob(req); err != nil {
		t.Fatalf("AbandonStaleJob prepared provision: %v", err)
	}
	job, err := store.GetJob(context.Background(), "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" || job.Result["lease_id"] != "lease-prepared" {
		t.Fatalf("prepared provision after abandon = %#v", job)
	}
}

// The abandon is a narrowly fenced release of one exact durable execution
// claim. Once the prepared provision has safely been retired, a destroy for
// that same stack must claim the lane. It must not release either another
// stack's active worker or the destroy's own newly acquired claim.
func TestAbandonStalePreparedProvisionReleasesOnlyExactDurableStackExecutionClaim(t *testing.T) {
	ctx := context.Background()
	orch, store, req := newAbandonFixtureWithResult(t, "provision", time.Minute, map[string]any{
		"lease_id":      "lease-prepared",
		"runtime_phase": string(jobs.RuntimePhaseLeaseReady),
		"job_wait": map[string]any{
			"state": "waiting", "reason": jobs.WaitReasonCanonicalGuardEvidence,
			"next_resume_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		},
	})

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-other", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Other", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-other-running", TenantID: "tenant-1", StackID: "stack-other", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-other-running", time.Now().UTC()); err != nil {
		t.Fatalf("start cross-stack worker: %v", err)
	}

	for _, jobID := range []string{"job-destroy", "job-follow-up"} {
		if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
			ID: jobID, TenantID: "tenant-1", StackID: req.StackID, Type: "destroy", State: "pending",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := orch.AbandonStaleJob(req); err != nil {
		t.Fatalf("AbandonStaleJob prepared provision: %v", err)
	}
	destroyStarted := make(chan struct{})
	releaseDestroy := make(chan struct{})
	defer close(releaseDestroy)
	orch.Queue().RegisterHandler(jobs.JobTypeDestroy, func(handlerCtx context.Context, _ *jobs.Job, _ *jobs.Queue) error {
		close(destroyStarted)
		select {
		case <-releaseDestroy:
			return nil
		case <-handlerCtx.Done():
			return handlerCtx.Err()
		}
	})
	destroy := &jobs.Job{
		ID: "job-destroy", Type: jobs.JobTypeDestroy, TargetType: targetTypeStack, TargetID: req.StackID,
		Payload: map[string]any{"tenant_id": "tenant-1"},
	}
	if err := orch.enqueueWithSync(destroy, nil, "tenant-1"); err != nil {
		t.Fatalf("enqueue destroy after abandon: %v", err)
	}
	orch.Start()
	select {
	case <-destroyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("destroy did not claim and start after the exact stale provision was abandoned")
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-follow-up", time.Now().UTC()); !errors.Is(err, controlplane.ErrStackExecutionBusy) {
		t.Fatalf("follow-up start error = %v, want ErrStackExecutionBusy behind current destroy", err)
	}

	other, err := store.GetJob(ctx, "tenant-1", "job-other-running")
	if err != nil {
		t.Fatal(err)
	}
	if other.State != "running" {
		t.Fatalf("cross-stack active worker was changed by exact abandon: %#v", other)
	}
}

// The case this exists for: a deploy that stopped reporting long ago must be
// retirable, and retiring it must move the stack into the status its documented
// recovery path requires.
func TestAbandonStaleJobRetiresASilentDeployAndUnlocksTheStack(t *testing.T) {
	orch, store, req := newAbandonFixture(t, "deploy", time.Minute)

	result, err := orch.AbandonStaleJob(req)
	if err != nil {
		t.Fatalf("AbandonStaleJob: %v", err)
	}
	if result.JobID != "job-stranded" || result.StackStatus != persistentStateError {
		t.Fatalf("result = %#v", result)
	}
	job, err := store.GetJob(context.Background(), "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" {
		t.Fatalf("job state = %q, want failed", job.State)
	}
	if job.Error == "" {
		t.Fatal("abandoned job must carry an operator-readable reason")
	}
	stack, err := store.GetStack(context.Background(), "tenant-1", "stack-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Status != persistentStateError {
		t.Fatalf("stack status = %q, want %q so retry-rollout becomes reachable", stack.Status, persistentStateError)
	}
}

// A generic provision or destroy may have a provider side effect still in
// flight, so it stays outside this narrow recovery.
func TestAbandonStaleJobRefusesProviderMutatingTypes(t *testing.T) {
	for _, jobType := range []string{"provision", "destroy"} {
		t.Run(jobType, func(t *testing.T) {
			orch, _, req := newAbandonFixture(t, jobType, time.Minute)
			if _, err := orch.AbandonStaleJob(req); !errors.Is(err, ErrAbandonJobInvalid) {
				t.Fatalf("error = %v, want ErrAbandonJobInvalid for a %s job", err, jobType)
			}
		})
	}
}

func TestAbandonStaleJobRefusesProvisionWithoutExactPreparedGuardWait(t *testing.T) {
	for name, result := range map[string]map[string]any{
		"no checkpoint": nil,
		"provider still pending": {
			"lease_id": "lease-pending", "runtime_phase": "lease_pending",
			"job_wait": map[string]any{"state": "waiting", "reason": jobs.WaitReasonCanonicalGuardEvidence},
		},
		"different wait": {
			"lease_id": "lease-ready", "runtime_phase": string(jobs.RuntimePhaseLeaseReady),
			"job_wait": map[string]any{"state": "waiting", "reason": jobs.WaitReasonManagedRuntimeProvider},
		},
	} {
		t.Run(name, func(t *testing.T) {
			orch, _, req := newAbandonFixtureWithResult(t, "provision", time.Minute, result)
			if _, err := orch.AbandonStaleJob(req); !errors.Is(err, ErrAbandonJobInvalid) {
				t.Fatalf("error = %v, want ErrAbandonJobInvalid", err)
			}
		})
	}
}

// Ownership is not negotiable: a job from another stack cannot be retired here.
func TestAbandonStaleJobRefusesAJobFromAnotherStack(t *testing.T) {
	orch, _, req := newAbandonFixture(t, "deploy", time.Minute)
	req.StackID = "stack-somebody-else"
	if _, err := orch.AbandonStaleJob(req); !errors.Is(err, ErrAbandonJobInvalid) {
		t.Fatalf("error = %v, want ErrAbandonJobInvalid for a foreign stack", err)
	}
}

// A terminal job is already retired; abandoning it must not rewrite history.
func TestAbandonStaleJobRefusesATerminalJob(t *testing.T) {
	orch, store, req := newAbandonFixture(t, "deploy", time.Minute)
	if _, err := store.FailJob(context.Background(), "tenant-1", "job-stranded", "already failed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.AbandonStaleJob(req); !errors.Is(err, ErrAbandonJobInvalid) {
		t.Fatalf("error = %v, want ErrAbandonJobInvalid for an already terminal job", err)
	}
}
