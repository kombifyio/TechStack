package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// newTerminalFlushFixture seeds a durable job that is running and claimed, the
// exact shape a rollout has at the moment a shutdown cancels it.
func newTerminalFlushFixture(t *testing.T) (*Orchestrator, *controlplane.MemoryStore, time.Time) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	store.SetNow(func() time.Time { return now })

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-flush", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Flush",
		Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-flush", TenantID: "tenant-1", StackID: "stack-flush", Type: "deploy",
		State: "pending", Step: "generate_unified",
	}); err != nil {
		t.Fatal(err)
	}
	started, err := store.StartJob(ctx, "tenant-1", "job-flush", now)
	if err != nil {
		t.Fatal(err)
	}
	if started.State != "running" {
		t.Fatalf("seed state = %q, want running", started.State)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
	}, nil)
	t.Cleanup(orch.Stop)
	return orch, store, now
}

// enqueueTerminalQueueJob puts a job into the in-memory queue already in a
// terminal state, mirroring what cancelJobInternal does on shutdown.
func enqueueTerminalQueueJob(t *testing.T, orch *Orchestrator, state jobs.JobState, startedAt time.Time) {
	t.Helper()
	job := &jobs.Job{
		ID:         "job-flush",
		Type:       jobs.JobTypeDeploy,
		TargetID:   "stack-flush",
		TargetType: "stack",
		Result:     map[string]interface{}{"tenant_id": "tenant-1"},
	}
	if err := orch.queue.Enqueue(job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job.State = state
	job.StartedAt = &startedAt
	completed := startedAt.Add(time.Second)
	job.CompletedAt = &completed
	job.Step = "generate_unified"
}

// The bug: shutdown cancels the job and the sync loop at the same instant, so
// the terminal state never reaches the row and it stays running forever,
// holding the stack's execution claim.
func TestTerminalStateIsPersistedWhenShutdownCancelsTheSyncLoop(t *testing.T) {
	orch, store, now := newTerminalFlushFixture(t)
	enqueueTerminalQueueJob(t, orch, jobs.JobStateCancelled, now)

	orch.flushTerminalJobStateAfterShutdown("job-flush", "tenant-1")

	job, err := store.GetJob(context.Background(), "tenant-1", "job-flush")
	if err != nil {
		t.Fatal(err)
	}
	if job.State == "running" {
		t.Fatal("cancelled job is still running in the durable store; the stack stays claimed forever")
	}
	if job.State != "cancelled" && job.State != "canceled" {
		t.Fatalf("state = %q, want a cancelled projection", job.State)
	}
}

// A failure that lands during shutdown must survive the same way.
func TestTerminalFlushPersistsAFailedJob(t *testing.T) {
	orch, store, now := newTerminalFlushFixture(t)
	enqueueTerminalQueueJob(t, orch, jobs.JobStateFailed, now)

	orch.flushTerminalJobStateAfterShutdown("job-flush", "tenant-1")

	job, err := store.GetJob(context.Background(), "tenant-1", "job-flush")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" {
		t.Fatalf("state = %q, want failed", job.State)
	}
}

// Work that is genuinely still mid-flight is NOT terminal and must be left
// alone. Reclaiming unknown state is a separate decision with provider
// side-effect safety attached to it.
func TestTerminalFlushLeavesNonTerminalWorkAlone(t *testing.T) {
	orch, store, now := newTerminalFlushFixture(t)
	enqueueTerminalQueueJob(t, orch, jobs.JobStateRunning, now)

	orch.flushTerminalJobStateAfterShutdown("job-flush", "tenant-1")

	job, err := store.GetJob(context.Background(), "tenant-1", "job-flush")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" {
		t.Fatalf("state = %q, want running to be left untouched", job.State)
	}
}

// The flush must not depend on the cancelled orchestrator context, which is the
// very thing that made the write impossible before.
func TestTerminalFlushWorksAfterTheOrchestratorContextIsCancelled(t *testing.T) {
	orch, store, now := newTerminalFlushFixture(t)
	enqueueTerminalQueueJob(t, orch, jobs.JobStateCancelled, now)

	orch.cancel()

	orch.flushTerminalJobStateAfterShutdown("job-flush", "tenant-1")

	job, err := store.GetJob(context.Background(), "tenant-1", "job-flush")
	if err != nil {
		t.Fatal(err)
	}
	if job.State == "running" {
		t.Fatal("flush relied on the cancelled orchestrator context and lost the terminal state")
	}
}
