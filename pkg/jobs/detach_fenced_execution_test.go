package jobs

import (
	"context"
	"testing"
	"time"
)

// A durable fence means another executor owns the row. This process must stop
// driving the job, not merely stop reporting on it - otherwise two processes
// act on one job against real providers, and updated_at freezes on a job that
// is still alive.
func TestDetachFencedExecutionCancelsTheRunningHandler(t *testing.T) {
	q := NewQueue(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{ID: "job-fenced", State: JobStateRunning, cancelFunc: cancel}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	if !q.DetachFencedExecution(job.ID, "claimed elsewhere") {
		t.Fatal("DetachFencedExecution reported no running execution to detach")
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the handler context was not cancelled, so the zombie executor keeps running")
	}

	job.mu.RLock()
	defer job.mu.RUnlock()
	if !job.cancellationRequested {
		t.Fatal("cancellationRequested must be set so the handler can unwind")
	}
	if job.cancellationReason != "claimed elsewhere" {
		t.Fatalf("cancellationReason = %q, want the fence reason", job.cancellationReason)
	}
	if !job.suppressPersistence {
		t.Fatal("persistence must be suppressed so the unwinding handler cannot write over the new owner")
	}
	if job.State != JobStateRunning {
		t.Fatalf("state = %s: the handler owns the terminal transition, detaching must not force one", job.State)
	}
}

// Detaching must never invent a running execution, and must still suppress
// persistence for a non-terminal job this process no longer owns.
func TestDetachFencedExecutionOnWaitingJobSuppressesWithoutClaimingADetach(t *testing.T) {
	q := NewQueue(1, nil)
	job := &Job{ID: "job-waiting", State: JobStateWaiting}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	if q.DetachFencedExecution(job.ID, "") {
		t.Fatal("a waiting job has no running execution to detach")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if !job.suppressPersistence {
		t.Fatal("a fenced waiting job must stop persisting over the new owner")
	}
}

func TestDetachFencedExecutionIsSafeForAnUnknownJob(t *testing.T) {
	q := NewQueue(1, nil)
	if q.DetachFencedExecution("absent", "") {
		t.Fatal("an unknown job must not report a detach")
	}
}

func TestDetachFencedExecutionIfUnchangedDoesNotCancelNewResumeClaim(t *testing.T) {
	q := NewQueue(1, nil)
	waiting := JobSnapshot{ID: "job-resumed", Type: JobTypeProvision, State: JobStateWaiting}
	startedAt := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &Job{
		ID: waiting.ID, Type: waiting.Type, State: JobStateRunning,
		StartedAt: &startedAt, cancelFunc: cancel,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	if q.DetachFencedExecutionIfUnchanged(job.ID, waiting, "claimed elsewhere") {
		t.Fatal("stale waiting snapshot detached the newly claimed execution")
	}
	select {
	case <-ctx.Done():
		t.Fatal("new resume claim was canceled by a stale sync snapshot")
	default:
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.suppressPersistence || job.cancellationRequested {
		t.Fatalf("new execution was mutated: suppressed=%v cancellation=%v", job.suppressPersistence, job.cancellationRequested)
	}
}
