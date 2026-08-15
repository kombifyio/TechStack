package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestResumeEnrollmentDispatchesOneExactReplacementAndReplays(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-waiting", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Waiting", Status: persistentStateProvisioning,
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	nextResumeAt := time.Now().UTC().Add(-3 * time.Minute)
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-waiting", TenantID: "tenant-1", StackID: "stack-waiting", Type: "deploy", State: persistentStatePending,
		Progress: 82, Step: "resolve_managed_runtime", Message: "Managed VM enrollment is pending",
		Result: map[string]any{
			"lease_id": "lease-waiting",
			"job_wait": map[string]any{
				"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeEnrollment,
				"next_resume_at": nextResumeAt.Format(time.RFC3339Nano),
			},
		},
		ScheduledFor: nextResumeAt,
	}); err != nil {
		t.Fatal(err)
	}
	lease := enrollmentResumeTestLease("lease-waiting", "tenant-1", "owner-1", "stack-waiting")
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-waiting", "lease-waiting", time.Now().UTC())
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}, nil)
	defer orch.Stop()

	req := EnrollmentResumeRequest{
		RequestContext: ctx, StackID: "stack-waiting", TenantID: "tenant-1", OwnerID: "owner-1",
		StackName: "Waiting", SourceJobID: "job-waiting", LeaseID: "lease-waiting",
	}
	issuedBootstraps := 0
	req.IssueOwnerSpecBootstrap = func() (*jobs.OwnerSpecBootstrap, error) {
		issuedBootstraps++
		return &jobs.OwnerSpecBootstrap{Endpoint: "/owner-spec", Token: "one-time"}, nil
	}
	first, err := orch.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("ResumeEnrollment: %v", err)
	}
	if first.JobID == "" || first.JobID == req.SourceJobID || first.IdempotentReplay {
		t.Fatalf("first result = %#v", first)
	}
	if first.ServerID != runtimeidentity.LeaseServerID(req.LeaseID) {
		t.Fatalf("server = %q", first.ServerID)
	}
	queued, err := orch.GetJobStatus(first.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"lease_id":                  req.LeaseID,
		enrollmentResumeSourceField: req.SourceJobID,
		enrollmentResumeLeaseField:  req.LeaseID,
		enrollmentResumeServerField: first.ServerID,
	} {
		if got := queued.Payload[key]; got != want {
			t.Fatalf("payload[%s] = %#v, want %#v", key, got, want)
		}
	}

	second, err := orch.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("ResumeEnrollment replay: %v", err)
	}
	if second.JobID != first.JobID || !second.IdempotentReplay {
		t.Fatalf("replay = %#v, want job %s", second, first.JobID)
	}
	if issuedBootstraps != 1 {
		t.Fatalf("bootstrap issuances = %d, want one for dispatch and none for replay", issuedBootstraps)
	}
	stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-waiting", 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored jobs = %#v err=%v", stored, err)
	}
	sourceAfter := enrollmentResumeStoredJob(t, stored, req.SourceJobID)
	if canonicalEnrollmentJobState(sourceAfter.State) != string(jobs.JobStateCancelled) ||
		sourceAfter.Result[enrollmentResumeReplacementField] != first.JobID {
		t.Fatalf("source handover = %#v", sourceAfter)
	}
	replacementAfter := enrollmentResumeStoredJob(t, stored, first.JobID)
	if replacementAfter.Result[enrollmentResumeGenericLeaseField] != req.LeaseID ||
		replacementAfter.Result[enrollmentResumeGenericServerField] != first.ServerID {
		t.Fatalf("replacement exact result = %#v", replacementAfter.Result)
	}
}

func TestResumeEnrollmentRejectsMissingGuardBeforeHandover(t *testing.T) {
	store, req := seedEnrollmentResumeTestState(t, persistentStateProvisioning, "stack-no-guard", "job-no-guard", "lease-no-guard")
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
		}},
	}, nil)
	defer orch.Stop()
	bootstrapCalls := 0
	blockedBootstrapToken := strings.Join([]string{"must", "not", "be", "issued"}, "-")
	req.IssueOwnerSpecBootstrap = func() (*jobs.OwnerSpecBootstrap, error) {
		bootstrapCalls++
		return &jobs.OwnerSpecBootstrap{Endpoint: "/owner-spec", Token: blockedBootstrapToken}, nil
	}

	result, err := orch.ResumeEnrollment(req)
	if !errors.Is(err, ErrNoAssignedWorkers) || result != nil {
		t.Fatalf("ResumeEnrollment = (%#v, %v), want nil and ErrNoAssignedWorkers", result, err)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap calls = %d, want zero before Guard admission", bootstrapCalls)
	}
	stored, listErr := store.ListJobsByStack(context.Background(), req.TenantID, req.StackID, 10)
	if listErr != nil || len(stored) != 1 || canonicalEnrollmentJobState(stored[0].State) != persistentStatePending {
		t.Fatalf("missing-Guard recovery mutated source: %#v err=%v", stored, listErr)
	}
	if _, exists := stored[0].Result[enrollmentResumeReplacementField]; exists {
		t.Fatalf("missing-Guard recovery persisted replacement receipt: %#v", stored[0].Result)
	}
}

func TestResumeEnrollmentRehydratesSameDeterministicReplacementAfterRestart(t *testing.T) {
	ctx := context.Background()
	store, req := seedEnrollmentResumeTestState(t, persistentStateProvisioning, "stack-restart", "job-restart", "lease-restart")
	seedManagedDeployEligibleServerRuntime(t, store, req.TenantID, req.OwnerID, req.StackID, req.LeaseID, time.Now().UTC())
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
	}}
	firstOrch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	first, err := firstOrch.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("first ResumeEnrollment: %v", err)
	}
	// Simulate a process crash before the local queue can run. Graceful shutdown
	// also leaves unclaimed durable work pending, but this path exercises abrupt
	// replica loss directly.
	firstOrch.cancel()
	firstOrch.wg.Wait()

	secondOrch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	defer secondOrch.Stop()
	second, err := secondOrch.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("restart ResumeEnrollment: %v", err)
	}
	if second.JobID != first.JobID || !second.IdempotentReplay {
		t.Fatalf("restart result = %#v, first=%#v", second, first)
	}
	if queued, queuedErr := secondOrch.GetJobStatus(first.JobID); queuedErr != nil || queued.Type != jobs.JobTypeDeploy {
		t.Fatalf("rehydrated queue job = %#v err=%v", queued, queuedErr)
	}
	stored, _ := store.ListJobsByStack(ctx, req.TenantID, req.StackID, 10)
	if len(stored) != 2 {
		t.Fatalf("restart created duplicate durable jobs: %#v", stored)
	}
}

func TestResumeEnrollmentRejectsStackThatStartedStopping(t *testing.T) {
	store, req := seedEnrollmentResumeTestState(t, "stopping", "stack-stopping", "job-stopping", "lease-stopping")
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
		}},
	}, nil)
	defer orch.Stop()

	_, err := orch.ResumeEnrollment(req)
	if !errors.Is(err, ErrEnrollmentResumeInvalid) {
		t.Fatalf("error = %v, want ErrEnrollmentResumeInvalid", err)
	}
	stored, _ := store.ListJobsByStack(context.Background(), req.TenantID, req.StackID, 10)
	if len(stored) != 1 || canonicalEnrollmentJobState(stored[0].State) != persistentStatePending {
		t.Fatalf("stopping recovery mutated jobs: %#v", stored)
	}
}

type enrollmentResumeClaimRaceStore struct {
	*controlplane.MemoryStore
	once sync.Once
}

func (s *enrollmentResumeClaimRaceStore) ClaimWaitingJobResume(ctx context.Context, req controlplane.ClaimWaitingJobResumeRequest) (*controlplane.Job, error) {
	s.once.Do(func() {
		job, err := s.GetJob(ctx, req.TenantID, req.JobID)
		if err != nil {
			return
		}
		_, _ = s.UpsertJob(ctx, controlplane.UpsertJobRequest{
			ID: job.ID, TenantID: job.TenantID, InstanceID: job.InstanceID, StackID: job.StackID,
			Type: job.Type, State: "cancelled", Priority: job.Priority, Progress: job.Progress,
			Step: job.Step, Message: "Canceled concurrently", Result: job.Result, ScheduledFor: job.ScheduledFor,
		})
	})
	return s.MemoryStore.ClaimWaitingJobResume(ctx, req)
}

func TestResumeEnrollmentCASDoesNotResurrectConcurrentCancellation(t *testing.T) {
	base, req := seedEnrollmentResumeTestState(t, persistentStateProvisioning, "stack-cas", "job-cas", "lease-cas")
	seedManagedDeployEligibleServerRuntime(t, base, req.TenantID, req.OwnerID, req.StackID, req.LeaseID, time.Now().UTC())
	store := &enrollmentResumeClaimRaceStore{MemoryStore: base}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
		}},
	}, nil)
	defer orch.Stop()

	_, err := orch.ResumeEnrollment(req)
	if !errors.Is(err, ErrEnrollmentResumeInvalid) {
		t.Fatalf("error = %v, want fail-closed invalid conflict", err)
	}
	source, getErr := store.GetJob(context.Background(), req.TenantID, req.SourceJobID)
	if getErr != nil || canonicalEnrollmentJobState(source.State) != string(jobs.JobStateCancelled) {
		t.Fatalf("concurrent cancellation was resurrected: %#v err=%v", source, getErr)
	}
	if _, exists := source.Result[enrollmentResumeKeyField]; exists {
		t.Fatalf("losing recovery wrote a receipt: %#v", source.Result)
	}
	stored, _ := store.ListJobsByStack(context.Background(), req.TenantID, req.StackID, 10)
	if len(stored) != 1 {
		t.Fatalf("losing recovery created replacement: %#v", stored)
	}
}

func TestResumeEnrollmentFencesStaleWaitingSnapshotFromAnotherReplica(t *testing.T) {
	store, req := seedEnrollmentResumeTestState(t, persistentStateProvisioning, "stack-stale-sync", "job-stale-sync", "lease-stale-sync")
	seedManagedDeployEligibleServerRuntime(t, store, req.TenantID, req.OwnerID, req.StackID, req.LeaseID, time.Now().UTC())
	sourceBefore, err := store.GetJob(context.Background(), req.TenantID, req.SourceJobID)
	if err != nil {
		t.Fatal(err)
	}
	wait, _ := sourceBefore.Result["job_wait"].(map[string]any)
	nextResumeAt, err := time.Parse(time.RFC3339Nano, stringFromAny(wait["next_resume_at"]))
	if err != nil {
		t.Fatal(err)
	}
	stale := jobs.JobSnapshot{
		ID: req.SourceJobID, Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: req.StackID,
		State: jobs.JobStateWaiting, Progress: sourceBefore.Progress, Step: sourceBefore.Step,
		Message: sourceBefore.Message, Result: sourceBefore.Result,
		WaitReason: jobs.WaitReasonManagedRuntimeEnrollment, NextResumeAt: &nextResumeAt,
	}
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
	}}
	replicaA := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	defer replicaA.Stop()
	replicaB := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	defer replicaB.Stop()

	result, err := replicaB.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("ResumeEnrollment: %v", err)
	}
	if syncErr := replicaA.syncControlPlaneJobSnapshot(stale, req.TenantID); !errors.Is(syncErr, controlplane.ErrConflict) {
		t.Fatalf("stale replica sync error = %v, want ErrConflict", syncErr)
	}
	sourceAfter, err := store.GetJob(context.Background(), req.TenantID, req.SourceJobID)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalEnrollmentJobState(sourceAfter.State) != string(jobs.JobStateCancelled) ||
		sourceAfter.Result[enrollmentResumeReplacementField] != result.JobID {
		t.Fatalf("stale replica resurrected source or erased receipt: %#v", sourceAfter)
	}
	if _, startErr := store.StartJob(context.Background(), req.TenantID, req.SourceJobID, time.Now().UTC()); !errors.Is(startErr, controlplane.ErrConflict) {
		t.Fatalf("source StartJob after handover = %v, want ErrConflict", startErr)
	}
	stored, err := store.ListJobsByStack(context.Background(), req.TenantID, req.StackID, 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("jobs after stale sync = %#v err=%v", stored, err)
	}
}

type enrollmentResumeStaleSnapshotStore struct {
	*controlplane.MemoryStore
	sourceJobID     string
	snapshotRead    chan struct{}
	releaseSnapshot chan struct{}
	readOnce        sync.Once
	releaseOnce     sync.Once
}

func newEnrollmentResumeStaleSnapshotStore(
	store *controlplane.MemoryStore,
	sourceJobID string,
) *enrollmentResumeStaleSnapshotStore {
	return &enrollmentResumeStaleSnapshotStore{
		MemoryStore:     store,
		sourceJobID:     sourceJobID,
		snapshotRead:    make(chan struct{}),
		releaseSnapshot: make(chan struct{}),
	}
}

func (s *enrollmentResumeStaleSnapshotStore) GetJob(
	ctx context.Context,
	tenantID string,
	jobID string,
) (*controlplane.Job, error) {
	job, err := s.MemoryStore.GetJob(ctx, tenantID, jobID)
	if err == nil && jobID == s.sourceJobID {
		s.readOnce.Do(func() {
			close(s.snapshotRead)
			<-s.releaseSnapshot
		})
	}
	return job, err
}

func (s *enrollmentResumeStaleSnapshotStore) release() {
	s.releaseOnce.Do(func() {
		close(s.releaseSnapshot)
	})
}

func TestResumeEnrollmentRefreshesStaleSourceAfterReplicaCreatesReplacement(t *testing.T) {
	base, req := seedEnrollmentResumeTestState(
		t,
		persistentStateProvisioning,
		"stack-stale-replica",
		"job-stale-replica",
		"lease-stale-replica",
	)
	seedManagedDeployEligibleServerRuntime(
		t,
		base,
		req.TenantID,
		req.OwnerID,
		req.StackID,
		req.LeaseID,
		time.Now().UTC(),
	)
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
	}}
	staleStore := newEnrollmentResumeStaleSnapshotStore(base, req.SourceJobID)
	defer staleStore.release()
	winner := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: base, JobStore: base, WorkerStore: base, LeaseLister: lister,
	}, nil)
	defer winner.Stop()
	staleReplica := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: staleStore, JobStore: staleStore, WorkerStore: staleStore, LeaseLister: lister,
	}, nil)
	defer staleReplica.Stop()

	type outcome struct {
		result *EnrollmentResumeResult
		err    error
	}
	staleOutcome := make(chan outcome, 1)
	go func() {
		result, err := staleReplica.ResumeEnrollment(req)
		staleOutcome <- outcome{result: result, err: err}
	}()

	select {
	case <-staleStore.snapshotRead:
	case <-time.After(time.Second):
		t.Fatal("stale replica did not read the source snapshot")
	}
	winnerResult, err := winner.ResumeEnrollment(req)
	if err != nil {
		t.Fatalf("winner ResumeEnrollment: %v", err)
	}
	staleStore.release()

	var replay outcome
	select {
	case replay = <-staleOutcome:
	case <-time.After(time.Second):
		t.Fatal("stale replica did not finish")
	}
	if replay.err != nil || replay.result == nil {
		t.Fatalf("stale replica outcome = %#v err=%v", replay.result, replay.err)
	}
	if replay.result.JobID != winnerResult.JobID || !replay.result.IdempotentReplay {
		t.Fatalf("stale replica result = %#v, winner=%#v", replay.result, winnerResult)
	}
}

func TestResumeEnrollmentConcurrentOrchestratorsExecuteOneDeterministicReplacement(t *testing.T) {
	store, req := seedEnrollmentResumeTestState(t, persistentStateProvisioning, "stack-concurrent", "job-concurrent", "lease-concurrent")
	seedManagedDeployEligibleServerRuntime(t, store, req.TenantID, req.OwnerID, req.StackID, req.LeaseID, time.Now().UTC())
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		enrollmentResumeTestLease(req.LeaseID, req.TenantID, req.OwnerID, req.StackID),
	}}
	one := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister}, nil)
	two := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister}, nil)
	defer one.Stop()
	defer two.Stop()

	type outcome struct {
		result *EnrollmentResumeResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for _, orch := range []*Orchestrator{one, two} {
		go func(candidate *Orchestrator) {
			defer callers.Done()
			result, err := candidate.ResumeEnrollment(req)
			outcomes <- outcome{result: result, err: err}
		}(orch)
	}
	callers.Wait()
	close(outcomes)
	jobID := ""
	for got := range outcomes {
		if got.err != nil || got.result == nil {
			t.Fatalf("concurrent outcome = %#v err=%v", got.result, got.err)
		}
		if jobID == "" {
			jobID = got.result.JobID
		} else if got.result.JobID != jobID {
			t.Fatalf("deterministic IDs differ: %q vs %q", jobID, got.result.JobID)
		}
	}
	stored, _ := store.ListJobsByStack(context.Background(), req.TenantID, req.StackID, 10)
	if len(stored) != 2 {
		t.Fatalf("concurrent calls created duplicate durable jobs: %#v", stored)
	}

	var executions atomic.Int32
	handled := make(chan struct{}, 1)
	for _, orch := range []*Orchestrator{one, two} {
		orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error {
			if executions.Add(1) == 1 {
				handled <- struct{}{}
			}
			return nil
		})
		orch.Start()
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("deterministic replacement did not execute")
	}
	time.Sleep(100 * time.Millisecond)
	if got := executions.Load(); got != 1 {
		t.Fatalf("handler executions = %d, want exactly 1 durable claim winner", got)
	}
}

// A provider operation can become present after the replica that admitted the
// provision job lost its local queue fence. Recovery must continue with the
// exact existing lease as a deploy; it must never re-run the provision job,
// because that is the only path which can request another provider VM.
func TestResumeEnrollmentRecoversProviderProvisionWaitAcrossReplicasWithoutCreate(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-provider-wait", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		Name: "Provider wait", Status: persistentStateProvisioning,
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	nextResumeAt := time.Now().UTC().Add(-3 * time.Minute)
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-provider-wait", TenantID: "tenant-1", StackID: "stack-provider-wait",
		Type: string(jobs.JobTypeProvision), State: persistentStatePending,
		Progress: 82, Step: "create_lease", Message: "Managed provider operation is still provisioning",
		Result: map[string]any{
			"lease_id":      "lease-provider-wait",
			"runtime_phase": string(jobs.RuntimePhaseLeasePending),
			"job_wait": map[string]any{
				"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeProvider,
				"next_resume_at": nextResumeAt.Format(time.RFC3339Nano),
			},
		},
		ScheduledFor: nextResumeAt,
	}); err != nil {
		t.Fatal(err)
	}
	lease := enrollmentResumeTestLease("lease-provider-wait", "tenant-1", "owner-1", "stack-provider-wait")
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-provider-wait", "lease-provider-wait", time.Now().UTC())
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}}
	replicaA := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	replicaB := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: lister,
	}, nil)
	defer replicaA.Stop()
	defer replicaB.Stop()

	req := EnrollmentResumeRequest{
		RequestContext: ctx, StackID: "stack-provider-wait", TenantID: "tenant-1", OwnerID: "owner-1",
		StackName: "Provider wait", SourceJobID: "job-provider-wait", LeaseID: "lease-provider-wait",
	}
	type outcome struct {
		result *EnrollmentResumeResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var callers sync.WaitGroup
	for _, candidate := range []*Orchestrator{replicaA, replicaB} {
		callers.Add(1)
		go func(orch *Orchestrator) {
			defer callers.Done()
			result, err := orch.ResumeEnrollment(req)
			outcomes <- outcome{result: result, err: err}
		}(candidate)
	}
	callers.Wait()
	close(outcomes)

	replacementID := ""
	for got := range outcomes {
		if got.err != nil || got.result == nil {
			t.Fatalf("provider-wait recovery = %#v err=%v", got.result, got.err)
		}
		if replacementID == "" {
			replacementID = got.result.JobID
		} else if got.result.JobID != replacementID {
			t.Fatalf("replacement IDs differ: %q vs %q", replacementID, got.result.JobID)
		}
	}

	stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-provider-wait", 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("durable jobs = %#v err=%v, want source plus one replacement", stored, err)
	}
	source := enrollmentResumeStoredJob(t, stored, req.SourceJobID)
	if canonicalEnrollmentJobState(source.State) != string(jobs.JobStateCancelled) ||
		source.Result[enrollmentResumeKindField] != jobs.WaitReasonManagedRuntimeProvider {
		t.Fatalf("provider source handover = %#v", source)
	}
	replacement := enrollmentResumeStoredJob(t, stored, replacementID)
	if replacement.Type != string(jobs.JobTypeDeploy) ||
		replacement.Result[enrollmentResumeKindField] != jobs.WaitReasonManagedRuntimeProvider {
		t.Fatalf("replacement = %#v, want exact deploy bound to provider wait", replacement)
	}

	var provisionExecutions atomic.Int32
	var deployExecutions atomic.Int32
	handled := make(chan struct{}, 1)
	for _, orch := range []*Orchestrator{replicaA, replicaB} {
		orch.Queue().RegisterHandler(jobs.JobTypeProvision, func(context.Context, *jobs.Job, *jobs.Queue) error {
			provisionExecutions.Add(1)
			return nil
		})
		orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error {
			if deployExecutions.Add(1) == 1 {
				handled <- struct{}{}
			}
			return nil
		})
		orch.Start()
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("provider-wait replacement did not execute")
	}
	time.Sleep(100 * time.Millisecond)
	if got := deployExecutions.Load(); got != 1 {
		t.Fatalf("deploy executions = %d, want one durable winner", got)
	}
	if got := provisionExecutions.Load(); got != 0 {
		t.Fatalf("provision executions = %d, provider create path must remain at zero", got)
	}
}

func TestResumeEnrollmentRejectsWaitInsideGraceWithoutDispatch(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-fresh-wait", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Fresh", Status: persistentStateProvisioning,
	}); err != nil {
		t.Fatal(err)
	}
	nextResumeAt := time.Now().UTC().Add(-time.Minute)
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-fresh-wait", TenantID: "tenant-1", StackID: "stack-fresh-wait", Type: "deploy", State: persistentStatePending,
		Result: map[string]any{"lease_id": "lease-fresh-wait", "job_wait": map[string]any{
			"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeEnrollment,
			"next_resume_at": nextResumeAt.Format(time.RFC3339Nano),
		}}, ScheduledFor: nextResumeAt,
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			enrollmentResumeTestLease("lease-fresh-wait", "tenant-1", "owner-1", "stack-fresh-wait"),
		}},
	}, nil)
	defer orch.Stop()

	_, err := orch.ResumeEnrollment(EnrollmentResumeRequest{
		StackID: "stack-fresh-wait", TenantID: "tenant-1", OwnerID: "owner-1",
		SourceJobID: "job-fresh-wait", LeaseID: "lease-fresh-wait",
	})
	if !errors.Is(err, ErrEnrollmentResumeNotReady) {
		t.Fatalf("error = %v, want ErrEnrollmentResumeNotReady", err)
	}
	stored, listErr := store.ListJobsByStack(ctx, "tenant-1", "stack-fresh-wait", 10)
	if listErr != nil || len(stored) != 1 {
		t.Fatalf("stored jobs = %#v err=%v", stored, listErr)
	}
}

func enrollmentResumeTestLease(id, tenantID, ownerID, stackID string) vmlease.Lease {
	now := time.Now().UTC()
	return vmlease.Lease{
		ID:           vmlease.LeaseID(id),
		Subject:      vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:     vmlease.ResourceRef{ProviderID: "ionos-managed", Region: "de-fra"},
		DesiredState: vmlease.DesiredStateRunning, BillingMode: vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription, RestartPolicy: vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), RenewedAt: now,
		Metadata: map[string]string{
			"runtime_lane": "monthly-runtime", "stack_id": stackID, "role": "foundation",
			"lease_provider": "ionos-managed", "runtime_ssh_host": "198.51.100.44",
			"runtime_ssh_user": "root", "runtime_ssh_port": "22",
		},
	}
}

func seedEnrollmentResumeTestState(
	t *testing.T,
	stackStatus, stackID, jobID, leaseID string,
) (*controlplane.MemoryStore, EnrollmentResumeRequest) {
	t.Helper()
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: stackID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Waiting", Status: stackStatus,
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	nextResumeAt := time.Now().UTC().Add(-3 * time.Minute)
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: jobID, TenantID: "tenant-1", StackID: stackID, Type: "deploy", State: persistentStatePending,
		Progress: 82, Step: "resolve_managed_runtime", Message: "Managed VM enrollment is pending",
		Result: map[string]any{
			"lease_id": leaseID,
			"job_wait": map[string]any{
				"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeEnrollment,
				"next_resume_at": nextResumeAt.Format(time.RFC3339Nano),
			},
		},
		ScheduledFor: nextResumeAt,
	}); err != nil {
		t.Fatal(err)
	}
	return store, EnrollmentResumeRequest{
		RequestContext: ctx, StackID: stackID, TenantID: "tenant-1", OwnerID: "owner-1",
		StackName: "Waiting", SourceJobID: jobID, LeaseID: leaseID,
	}
}

func enrollmentResumeStoredJob(t *testing.T, stored []controlplane.Job, jobID string) controlplane.Job {
	t.Helper()
	for _, job := range stored {
		if job.ID == jobID {
			return job
		}
	}
	t.Fatalf("job %s not found in %#v", jobID, stored)
	return controlplane.Job{}
}
