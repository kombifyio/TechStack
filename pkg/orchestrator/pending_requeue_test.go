package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func newPendingRequeueFixture(t *testing.T) (*Orchestrator, *controlplane.MemoryStore) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-recover", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Recover", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	orch := New(&Config{Workers: 1, StackStore: store, JobStore: store, WorkerStore: store}, nil)
	t.Cleanup(orch.Stop)
	return orch, store
}

func TestRequeuePendingJobsRecoversAndExecutesRecentPendingJob(t *testing.T) {
	ctx := context.Background()
	orch, store := newPendingRequeueFixture(t)
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-recover", TenantID: "tenant-1", StackID: "stack-recover",
		Type: string(jobs.JobTypeProvision), State: "pending", Step: "queued", Message: "queued",
		Payload: map[string]any{"spec": map[string]any{"kit": "base"}},
	}); err != nil {
		t.Fatal(err)
	}

	handled := make(chan string, 1)
	orch.queue.RegisterHandler(jobs.JobTypeProvision, func(_ context.Context, job *jobs.Job, _ *jobs.Queue) error {
		handled <- job.ID
		return nil
	})
	orch.Start()

	requeued, err := orch.RequeuePendingJobs(ctx)
	if err != nil {
		t.Fatalf("RequeuePendingJobs: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued = %d, want 1", requeued)
	}
	select {
	case id := <-handled:
		if id != "job-recover" {
			t.Fatalf("handler ran for %q, want job-recover", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovered pending job was never executed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, err := store.GetJob(ctx, "tenant-1", "job-recover")
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if row.State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered job state = %q, want completed", row.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRequeuePendingJobsLeavesSupersededJobsAlone(t *testing.T) {
	ctx := context.Background()
	orch, store := newPendingRequeueFixture(t)
	now := time.Now().UTC()
	store.SetNow(func() time.Time { return now.Add(-2 * time.Minute) })
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-old", TenantID: "tenant-1", StackID: "stack-recover",
		Type: string(jobs.JobTypeProvision), State: "pending", Step: "queued", Message: "queued",
		Payload: map[string]any{"spec": map[string]any{"kit": "base"}},
	}); err != nil {
		t.Fatal(err)
	}
	store.SetNow(func() time.Time { return now })
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-new", TenantID: "tenant-1", StackID: "stack-recover",
		Type: string(jobs.JobTypeProvision), State: "completed", Step: "done", Message: "done",
	}); err != nil {
		t.Fatal(err)
	}

	ran := make(chan string, 1)
	orch.queue.RegisterHandler(jobs.JobTypeProvision, func(_ context.Context, job *jobs.Job, _ *jobs.Queue) error {
		ran <- job.ID
		return nil
	})
	orch.Start()

	requeued, err := orch.RequeuePendingJobs(ctx)
	if err != nil {
		t.Fatalf("RequeuePendingJobs: %v", err)
	}
	if requeued != 0 {
		t.Fatalf("requeued = %d, want 0 for a superseded job", requeued)
	}
	select {
	case id := <-ran:
		t.Fatalf("superseded job %q was executed", id)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRequeuePendingJobsSkipsRedactedPayloads(t *testing.T) {
	ctx := context.Background()
	orch, store := newPendingRequeueFixture(t)
	payload := redactedJobPayloadForRecovery(map[string]any{
		"spec":                 map[string]any{"kit": "base"},
		"owner_spec_bootstrap": map[string]any{"token": "one-time-capability"},
		"intent_raw":           "secrets: may be here",
	})
	if _, redacted := payload["owner_spec_bootstrap"]; redacted {
		t.Fatal("owner_spec_bootstrap must not survive payload redaction")
	}
	if _, redacted := payload["intent_raw"]; redacted {
		t.Fatal("intent_raw must not survive payload redaction")
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-redacted", TenantID: "tenant-1", StackID: "stack-recover",
		Type: string(jobs.JobTypeProvision), State: "pending", Step: "queued", Message: "queued",
		Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	ran := make(chan string, 1)
	orch.queue.RegisterHandler(jobs.JobTypeProvision, func(_ context.Context, job *jobs.Job, _ *jobs.Queue) error {
		ran <- job.ID
		return nil
	})
	orch.Start()

	requeued, err := orch.RequeuePendingJobs(ctx)
	if err != nil {
		t.Fatalf("RequeuePendingJobs: %v", err)
	}
	if requeued != 0 {
		t.Fatalf("requeued = %d, want 0 for a redacted payload", requeued)
	}
	select {
	case id := <-ran:
		t.Fatalf("redacted job %q was auto-resumed", id)
	case <-time.After(200 * time.Millisecond):
	}
}

type recordingRemoteEnrollmentExecutor struct {
	calls int32
}

func (r *recordingRemoteEnrollmentExecutor) ExecuteRemoteEnrollment(
	_ context.Context,
	_ jobs.RemoteEnrollmentRequest,
	progress func(step, message string, percent int),
) (jobs.RemoteEnrollmentOutcome, error) {
	atomic.AddInt32(&r.calls, 1)
	if progress != nil {
		progress("remote_ssh_connect", "Connecting to your Node over SSH…", 10)
	}
	return jobs.RemoteEnrollmentOutcome{ServerID: "server-recovered"}, nil
}

func TestRequeuePendingJobsExecutesRegisteredRemoteEnrollment(t *testing.T) {
	ctx := context.Background()
	orch, store := newPendingRequeueFixture(t)
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-enroll", TenantID: "tenant-1", StackID: "stack-recover",
		Type: "remote_enrollment", State: "pending", Step: "remote_ssh_connect",
		Message: "Connecting to your Node over SSH…",
		Payload: map[string]any{
			"tenant_id": "tenant-1", "owner_id": "owner-1",
			"remote_enrollment_recovery": true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	executor := &recordingRemoteEnrollmentExecutor{}
	orch.ConfigureRemoteEnrollment(executor)
	orch.Start()

	requeued, err := orch.RequeuePendingJobs(ctx)
	if err != nil {
		t.Fatalf("RequeuePendingJobs: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued = %d, want 1", requeued)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, err := store.GetJob(ctx, "tenant-1", "job-enroll")
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if row.State == "completed" {
			if row.Result["server_id"] != "server-recovered" {
				t.Fatalf("completed enrollment result = %#v", row.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("enrollment job state = %q, want completed", row.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
}
