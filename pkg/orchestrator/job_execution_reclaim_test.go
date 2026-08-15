package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const deadProcessOwnerID = "boot-that-never-came-back"

// newReclaimFixture builds the exact shape an OOM kill or a Render revision
// restart leaves behind: a durable job row still marked running, holding the
// per-stack execution claim, with no process behind it.
func newReclaimFixture(t *testing.T, jobType string, result map[string]any) (
	*Orchestrator, *controlplane.MemoryStore, *JobExecutionReclaimer, time.Time, time.Time,
) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	startedAt := now.Add(-30 * time.Minute)
	store.SetNow(func() time.Time { return startedAt })

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
	if _, err := store.StartJob(ctx, "tenant-1", "job-stranded", startedAt); err != nil {
		t.Fatal(err)
	}
	store.SetNow(func() time.Time { return now })

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		Now: func() time.Time { return now },
	}, nil)
	t.Cleanup(orch.Stop)

	reclaimer, err := NewJobExecutionReclaimer(JobExecutionReclaimConfig{
		Orchestrator: orch, Store: store,
	})
	if err != nil {
		t.Fatalf("NewJobExecutionReclaimer: %v", err)
	}
	return orch, store, reclaimer, now, startedAt
}

// heartbeat replays the orchestrator's 500ms progress sync, which is what a
// live execution does and what renews its lease.
func heartbeat(t *testing.T, store *controlplane.MemoryStore, jobType string, startedAt time.Time) {
	t.Helper()
	if _, err := store.SyncJobSnapshot(context.Background(), controlplane.SyncJobSnapshotRequest{
		Job: controlplane.UpsertJobRequest{
			ID: "job-stranded", TenantID: "tenant-1", StackID: "stack-stranded", Type: jobType,
			State: "running", Progress: 60, Step: "generate_unified",
		},
		ObservedState: "running", AttemptStartedAt: &startedAt,
	}); err != nil {
		t.Fatalf("progress heartbeat: %v", err)
	}
}

// killOwningProcess expresses "the process that claimed this execution is gone":
// its lease lapsed and it belongs to a boot that is not this one.
func killOwningProcess(store *controlplane.MemoryStore, jobID string, expiredAt time.Time) {
	store.SetJobExecutionLeaseOwner(jobID, deadProcessOwnerID)
	store.ExpireJobExecutionLease(jobID, expiredAt)
}

func reclaimReceipt(t *testing.T, job *controlplane.Job) map[string]any {
	t.Helper()
	receipt, ok := job.Result[jobExecutionReclaimResultKey].(map[string]any)
	if !ok {
		t.Fatalf("reclaimed job carries no durable receipt: %#v", job.Result)
	}
	return receipt
}

// A job whose lease is still valid is live work, not debris. The rollout here
// has been running for half an hour; what keeps it safe is its heartbeat, which
// is exactly the invariant that stops a slow multi-minute rollout from being
// killed by its own recovery mechanism.
func TestReclaimLeavesALiveExecutionUntouched(t *testing.T) {
	_, store, reclaimer, _, startedAt := newReclaimFixture(t, "deploy", nil)
	heartbeat(t, store, "deploy", startedAt)

	stats, err := reclaimer.ReclaimOnce(context.Background())
	if err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	if stats.Reclaimed != 0 || stats.Inspected != 0 {
		t.Fatalf("live execution was inspected or reclaimed: %#v", stats)
	}
	job, err := store.GetJob(context.Background(), "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" {
		t.Fatalf("live job state = %q, want running", job.State)
	}
}

// The case both beads describe: the owning process died, so the row is an
// impossible active state. It must be terminalized with a durable reason and
// its per-stack execution claim must be released.
func TestReclaimTerminalizesAnExpiredLeaseAndReleasesTheStackClaim(t *testing.T) {
	ctx := context.Background()
	_, store, reclaimer, now, _ := newReclaimFixture(t, "deploy", nil)
	killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

	stats, err := reclaimer.ReclaimOnce(ctx)
	if err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	if stats.Reclaimed != 1 {
		t.Fatalf("stats = %#v, want exactly one reclaim", stats)
	}

	job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" {
		t.Fatalf("job state = %q, want failed", job.State)
	}
	if job.Error == "" || job.ErrorDetails == "" {
		t.Fatalf("reclaimed job must carry an operator-readable reason: %#v", job)
	}

	// The claim is released: a follow-up operation on the same stack can start.
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-follow-up", TenantID: "tenant-1", StackID: "stack-stranded", Type: "destroy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-follow-up", now); err != nil {
		t.Fatalf("follow-up start after reclaim: %v, want the stack lane to be free", err)
	}

	// And the stack reaches the status its documented recovery path requires.
	stack, err := store.GetStack(ctx, "tenant-1", "stack-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Status != persistentStateError {
		t.Fatalf("stack status = %q, want %q so retry-rollout becomes reachable", stack.Status, persistentStateError)
	}
}

// A terminal row is already retired. Reclaim must not rewrite history, and it
// must not even see it: FailJob surrenders the lease with the state.
func TestReclaimIgnoresTerminalJobs(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []string{"failed", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			_, store, reclaimer, now, _ := newReclaimFixture(t, "deploy", nil)
			var err error
			if terminal == "failed" {
				_, err = store.FailJob(ctx, "tenant-1", "job-stranded", "already failed", "by the handler", now)
			} else {
				_, err = store.CompleteJob(ctx, "tenant-1", "job-stranded", map[string]any{"ok": true}, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			store.ExpireJobExecutionLease("job-stranded", now.Add(-time.Hour))

			stats, reclaimErr := reclaimer.ReclaimOnce(ctx)
			if reclaimErr != nil {
				t.Fatalf("ReclaimOnce: %v", reclaimErr)
			}
			if stats.Reclaimed != 0 || stats.Inspected != 0 {
				t.Fatalf("terminal job was touched: %#v", stats)
			}
			job, getErr := store.GetJob(ctx, "tenant-1", "job-stranded")
			if getErr != nil {
				t.Fatal(getErr)
			}
			if job.State != terminal {
				t.Fatalf("job state = %q, want the untouched %q", job.State, terminal)
			}
			if _, rewritten := job.Result[jobExecutionReclaimResultKey]; rewritten {
				t.Fatalf("terminal job history was rewritten: %#v", job.Result)
			}
		})
	}
}

// The restart case from kombify-Techstack-1gh2: a live job stayed projected
// running after the process restarted onto a new revision. Reconciliation must
// record WHY, not just flip a state column.
func TestRestartReconciliationWritesADurableReason(t *testing.T) {
	ctx := context.Background()
	_, store, reclaimer, now, _ := newReclaimFixture(t, "deploy", nil)
	killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

	if _, err := reclaimer.ReclaimOnce(ctx); err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	receipt := reclaimReceipt(t, job)
	if receipt["schema"] != jobExecutionReclaimSchema {
		t.Fatalf("receipt schema = %v", receipt["schema"])
	}
	if receipt["reason_code"] != JobReclaimReasonProcessRestart {
		t.Fatalf("reason_code = %v, want %q", receipt["reason_code"], JobReclaimReasonProcessRestart)
	}
	if receipt["previous_owner_id"] != deadProcessOwnerID {
		t.Fatalf("previous_owner_id = %v, want the dead boot id", receipt["previous_owner_id"])
	}
	if receipt["reclaimed_by"] != controlplane.ProcessExecutionOwnerID() {
		t.Fatalf("reclaimed_by = %v, want this process", receipt["reclaimed_by"])
	}
	if receipt["provider_effect"] != ProviderEffectRecoverable || receipt["retryable"] != true {
		t.Fatalf("a deploy re-drive is the product contract: %#v", receipt)
	}
	if guidance, _ := receipt["user_guidance"].(string); guidance == "" {
		t.Fatalf("receipt must carry user guidance: %#v", receipt)
	}
	if receipt["lease_expired_at"] == nil || receipt["last_progress_at"] == nil {
		t.Fatalf("receipt must show the evidence it acted on: %#v", receipt)
	}
}

// An execution owned by this very process whose lease still lapsed is reported
// as a lapsed lease rather than a restart, so the two failure modes stay
// distinguishable in the ledger.
func TestReclaimDistinguishesALapsedLeaseFromARestart(t *testing.T) {
	ctx := context.Background()
	_, store, reclaimer, now, _ := newReclaimFixture(t, "deploy", nil)
	store.ExpireJobExecutionLease("job-stranded", now.Add(-time.Minute))

	if _, err := reclaimer.ReclaimOnce(ctx); err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if reason := reclaimReceipt(t, job)["reason_code"]; reason != JobReclaimReasonLeaseExpired {
		t.Fatalf("reason_code = %v, want %q", reason, JobReclaimReasonLeaseExpired)
	}
}

// Fail-closed: a job whose provider side effect nobody observed is still moved
// out of the impossible running state, but it is never presented as retryable
// and never re-dispatched. The provider operation stays where operator
// resolution can find it.
func TestReclaimKeepsAmbiguousProviderWorkInOperatorResolution(t *testing.T) {
	ctx := context.Background()
	for _, jobType := range []string{"provision", "destroy"} {
		t.Run(jobType, func(t *testing.T) {
			_, store, reclaimer, now, _ := newReclaimFixture(t, jobType, nil)
			killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

			if _, err := reclaimer.ReclaimOnce(ctx); err != nil {
				t.Fatalf("ReclaimOnce: %v", err)
			}
			job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
			if err != nil {
				t.Fatal(err)
			}
			if job.State != "failed" {
				t.Fatalf("job state = %q: an orphan must never stay in an impossible active state", job.State)
			}
			receipt := reclaimReceipt(t, job)
			if receipt["provider_effect"] != ProviderEffectRequiresOperatorResolution {
				t.Fatalf("provider_effect = %v, want operator resolution for a %s", receipt["provider_effect"], jobType)
			}
			if receipt["retryable"] != false {
				t.Fatalf("ambiguous provider work must not be advertised as retryable: %#v", receipt)
			}
		})
	}
}

// A provision that already proved its lease is ready and had yielded only for
// Guard evidence has a documented safe re-drive, so it is classified the same
// way the existing abandon path classifies it.
func TestReclaimTreatsAPreparedProvisionAsRecoverable(t *testing.T) {
	ctx := context.Background()
	_, store, reclaimer, now, _ := newReclaimFixture(t, "provision", map[string]any{
		"lease_id":      "lease-prepared",
		"runtime_phase": string(jobs.RuntimePhaseLeaseReady),
		"job_wait": map[string]any{
			"state": "waiting", "reason": jobs.WaitReasonCanonicalGuardEvidence,
			"next_resume_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		},
	})
	killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

	if _, err := reclaimer.ReclaimOnce(ctx); err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	receipt := reclaimReceipt(t, job)
	if receipt["provider_effect"] != ProviderEffectRecoverable {
		t.Fatalf("provider_effect = %v, want recoverable for a prepared provision", receipt["provider_effect"])
	}
	if job.Result["lease_id"] != "lease-prepared" {
		t.Fatalf("reclaim must not discard the job's own receipt: %#v", job.Result)
	}
}

// A marker-bound managed decommission is genuinely resumable: the existing
// recovery path moves it back to pending instead of failing it. Reclaiming it
// here would replace a resume with a worse outcome.
func TestReclaimLeavesAResumableManagedDecommissionToItsOwnRecovery(t *testing.T) {
	ctx := context.Background()
	_, store, reclaimer, now, _ := newReclaimFixture(t, "destroy", map[string]any{
		managedDecommissionRecoveryMarkerKey: managedProviderDecommissionRecoveryMarker("tenant-1", "stack-stranded"),
	})
	killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

	stats, err := reclaimer.ReclaimOnce(ctx)
	if err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	if stats.Reclaimed != 0 || stats.Resumable != 1 {
		t.Fatalf("stats = %#v, want the resumable job left to its own recovery", stats)
	}
	job, err := store.GetJob(ctx, "tenant-1", "job-stranded")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" {
		t.Fatalf("job state = %q, want the resume path to keep ownership", job.State)
	}
}

// The regression the whole change exists for: a stack whose claim was held by
// an orphan must be able to run queued work again, end to end through the
// queue's durable claim rather than through a direct store write.
func TestReclaimedStackLetsItsDeferredJobStart(t *testing.T) {
	ctx := context.Background()
	orch, store, reclaimer, now, _ := newReclaimFixture(t, "deploy", nil)
	killOwningProcess(store, "job-stranded", now.Add(-time.Minute))

	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-destroy", TenantID: "tenant-1", StackID: "stack-stranded", Type: "destroy", State: "pending",
	}); err != nil {
		t.Fatal(err)
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
		ID: "job-destroy", Type: jobs.JobTypeDestroy, TargetType: targetTypeStack, TargetID: "stack-stranded",
		Payload: map[string]any{"tenant_id": "tenant-1"},
	}
	if err := orch.enqueueWithSync(destroy, nil, "tenant-1"); err != nil {
		t.Fatalf("enqueue destroy behind the orphan: %v", err)
	}
	orch.Start()

	// While the orphan still holds the claim the destroy can only defer.
	select {
	case <-destroyStarted:
		t.Fatal("destroy started while the orphaned execution still held the stack claim")
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := reclaimer.ReclaimOnce(ctx); err != nil {
		t.Fatalf("ReclaimOnce: %v", err)
	}
	select {
	case <-destroyStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("deferred destroy never started after its stack was reclaimed")
	}
}

// A reclaimer without a store is a configuration error, not a silent no-op.
func TestNewJobExecutionReclaimerRequiresItsAuthorities(t *testing.T) {
	if _, err := NewJobExecutionReclaimer(JobExecutionReclaimConfig{}); err == nil {
		t.Fatal("reclaimer without an orchestrator was accepted")
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1}, nil)
	t.Cleanup(orch.Stop)
	if _, err := NewJobExecutionReclaimer(JobExecutionReclaimConfig{Orchestrator: orch}); err == nil {
		t.Fatal("reclaimer without a store was accepted")
	}
}

// An execution that renewed its lease between the scan and the write must win.
// This is the whole multi-instance safety argument: no locking, just a
// conditional write carrying every fact the scan observed.
func TestReclaimRefusesAnExecutionThatRenewedItsLease(t *testing.T) {
	ctx := context.Background()
	_, store, _, now, _ := newReclaimFixture(t, "deploy", nil)

	_, err := store.ReclaimExpiredJobExecution(ctx, controlplane.ReclaimExpiredJobExecutionRequest{
		TenantID: "tenant-1", JobID: "job-stranded", StackID: "stack-stranded",
		ExpectedOwnerID: deadProcessOwnerID, LeaseExpiredBefore: now,
		Error: "stale observation", ReclaimedAt: now,
	})
	if !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict for a stale observation", err)
	}
	job, getErr := store.GetJob(ctx, "tenant-1", "job-stranded")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if job.State != "running" {
		t.Fatalf("job state = %q, want the live execution untouched", job.State)
	}
}
