package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreClaimPairingTokenAllowsExactlyOneConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	claimedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	expiresAt := claimedAt.Add(time.Hour)
	if _, err := store.UpsertPairingToken(ctx, PairingToken{
		ID: "pair-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		TokenHash: "hash-1", Status: "active", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatal(err)
	}

	const attempts = 16
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.ClaimPairingToken(ctx, "tenant-1", "hash-1", claimedAt)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ClaimPairingToken error = %v, want ErrNotFound", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful claims = %d, want exactly 1", successes)
	}
	claimed, err := store.GetPairingTokenByHash(ctx, "tenant-1", "hash-1")
	if err != nil || claimed.Status != "used" || claimed.UsedAt == nil || !claimed.UsedAt.Equal(claimedAt) {
		t.Fatalf("claimed token = %#v, %v", claimed, err)
	}
}

func TestMemoryStoreStacksAreTenantScopedAndConflictOnActiveName(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	stack, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-a",
		OwnerSubjectID: "user-1",
		Name:           "TechStack",
		Config:         map[string]any{"mode": "easy"},
	})
	if err != nil {
		t.Fatalf("CreateStack() error = %v", err)
	}
	if stack.Status != "draft" || stack.Mode != "easy" {
		t.Fatalf("stack defaults = status %q mode %q", stack.Status, stack.Mode)
	}

	if _, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-2",
		TenantID:       "tenant-a",
		OwnerSubjectID: "user-1",
		Name:           "techstack",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateStack() error = %v, want ErrConflict", err)
	}

	if _, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-3",
		TenantID:       "tenant-b",
		OwnerSubjectID: "user-1",
		Name:           "techstack",
	}); err != nil {
		t.Fatalf("cross-tenant CreateStack() error = %v", err)
	}

	tenantA, err := store.ListStacksByTenant(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListStacksByTenant() error = %v", err)
	}
	if len(tenantA) != 1 || tenantA[0].ID != "stack-1" {
		t.Fatalf("tenant-a stacks = %#v, want only stack-1", tenantA)
	}
}

func TestMemoryStoreSoftDeleteAllowsNameReuse(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-a",
		OwnerSubjectID: "user-1",
		Name:           "techstack",
	}); err != nil {
		t.Fatalf("CreateStack() error = %v", err)
	}
	if err := store.SoftDeleteStack(ctx, "tenant-a", "stack-1"); err != nil {
		t.Fatalf("SoftDeleteStack() error = %v", err)
	}
	if _, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-2",
		TenantID:       "tenant-a",
		OwnerSubjectID: "user-1",
		Name:           "techstack",
	}); err != nil {
		t.Fatalf("CreateStack() after delete error = %v", err)
	}
}

func TestMemoryStoreClonesMutableStackPayloads(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	created, err := store.CreateStack(ctx, CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-a",
		OwnerSubjectID: "user-1",
		Name:           "techstack",
		Config:         map[string]any{"keep": "original"},
		Services:       []map[string]any{{"name": "svc"}},
	})
	if err != nil {
		t.Fatalf("CreateStack() error = %v", err)
	}

	created.Config["keep"] = "mutated"
	created.Services[0]["name"] = "changed"

	reloaded, err := store.GetStack(ctx, "tenant-a", "stack-1")
	if err != nil {
		t.Fatalf("GetStack() error = %v", err)
	}
	if reloaded.Config["keep"] != "original" || reloaded.Services[0]["name"] != "svc" {
		t.Fatalf("store leaked mutable stack payload: %#v", reloaded)
	}
}

func TestMemoryStoreJobLifecycleIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	job, err := store.UpsertJob(ctx, UpsertJobRequest{
		ID:       "job-1",
		TenantID: "tenant-a",
		StackID:  "stack-1",
		Type:     "provision",
	})
	if err != nil {
		t.Fatalf("UpsertJob() error = %v", err)
	}
	if job.State != "pending" {
		t.Fatalf("job.State = %q, want pending", job.State)
	}

	if _, err := store.GetJob(ctx, "tenant-b", "job-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant GetJob() error = %v, want ErrNotFound", err)
	}

	startedAt := now.Add(time.Minute)
	started, err := store.StartJob(ctx, "tenant-a", "job-1", startedAt)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	if started.State != "running" || started.StartedAt == nil || !started.StartedAt.Equal(startedAt) {
		t.Fatalf("started job = %#v", started)
	}
	if _, secondStartErr := store.StartJob(ctx, "tenant-a", "job-1", startedAt.Add(time.Second)); !errors.Is(secondStartErr, ErrConflict) {
		t.Fatalf("second StartJob() error = %v, want ErrConflict", secondStartErr)
	}

	completedAt := now.Add(2 * time.Minute)
	completed, err := store.CompleteJob(ctx, "tenant-a", "job-1", map[string]any{"ok": true}, completedAt)
	if err != nil {
		t.Fatalf("CompleteJob() error = %v", err)
	}
	if completed.State != "completed" || completed.Progress != 100 || completed.Result["ok"] != true {
		t.Fatalf("completed job = %#v", completed)
	}

	tenantJobs, err := store.ListJobsByTenant(ctx, "tenant-a", 10)
	if err != nil {
		t.Fatalf("ListJobsByTenant() error = %v", err)
	}
	if len(tenantJobs) != 1 || tenantJobs[0].ID != "job-1" {
		t.Fatalf("ListJobsByTenant() = %#v, want job-1", tenantJobs)
	}
}

func TestMemoryStoreListsOnlyExactProviderProvisionWait(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	for _, job := range []UpsertJobRequest{
		{
			ID: "provider-wait", TenantID: "tenant-a", StackID: "stack-a",
			Type: "provision", State: "pending", Result: map[string]any{
				"operation_id": "op-a",
				"job_wait":     map[string]any{"state": "waiting", "reason": "waiting_provider_provision"},
			},
		},
		{
			ID: "other-operation", TenantID: "tenant-a", StackID: "stack-a",
			Type: "provision", State: "pending", Result: map[string]any{
				"operation_id": "op-b",
				"job_wait":     map[string]any{"state": "waiting", "reason": "waiting_provider_provision"},
			},
		},
		{
			ID: "other-wait", TenantID: "tenant-a", StackID: "stack-a",
			Type: "provision", State: "pending", Result: map[string]any{
				"operation_id": "op-a",
				"job_wait":     map[string]any{"state": "waiting", "reason": "waiting_provider_enrollment"},
			},
		},
		{
			ID: "other-tenant", TenantID: "tenant-b", StackID: "stack-b",
			Type: "provision", State: "pending", Result: map[string]any{
				"operation_id": "op-a",
				"job_wait":     map[string]any{"state": "waiting", "reason": "waiting_provider_provision"},
			},
		},
	} {
		if _, err := store.CreateJob(ctx, job); err != nil {
			t.Fatalf("CreateJob(%s): %v", job.ID, err)
		}
	}

	jobs, err := store.ListProviderProvisionRecoveryCandidates(ctx, "tenant-a", "op-a", 10)
	if err != nil {
		t.Fatalf("ListProviderProvisionRecoveryCandidates: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "provider-wait" {
		t.Fatalf("provider recovery candidates = %#v, want exact operation wait", jobs)
	}
}

func TestMemoryStoreRequiresExecutionClaimForRunningAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)

	for _, write := range []struct {
		name string
		run  func() error
	}{
		{name: "create running", run: func() error {
			_, err := store.CreateJob(ctx, UpsertJobRequest{ID: "job-create-running", TenantID: "tenant-1", StackID: "stack-1", State: "running"})
			return err
		}},
		{name: "upsert running", run: func() error {
			_, err := store.UpsertJob(ctx, UpsertJobRequest{ID: "job-upsert-running", TenantID: "tenant-1", StackID: "stack-1", State: "running"})
			return err
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); !errors.Is(err, ErrConflict) {
				t.Fatalf("write error = %v, want ErrConflict", err)
			}
		})
	}

	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-claimed", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if _, err := store.CompleteJob(ctx, "tenant-1", "job-claimed", nil, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("CompleteJob(pending) error = %v, want ErrConflict", err)
	}
	if _, err := store.FailJob(ctx, "tenant-1", "job-claimed", "failed", "", now); !errors.Is(err, ErrConflict) {
		t.Fatalf("FailJob(pending) error = %v, want ErrConflict", err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-claimed", now); err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	if _, err := store.UpsertJob(ctx, UpsertJobRequest{
		ID: "job-claimed", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpsertJob(running job) error = %v, want ErrConflict", err)
	}
	if _, err := store.CompleteJob(ctx, "tenant-1", "job-claimed", map[string]any{"ok": true}, now.Add(time.Minute)); err != nil {
		t.Fatalf("CompleteJob(running) error = %v", err)
	}
	if _, err := store.FailJob(ctx, "tenant-1", "job-claimed", "stale", "", now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("FailJob(completed) error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreStartJobSerializesExecutionsWithinStack(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	startedAt := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)

	for _, jobID := range []string{"job-first", "job-second"} {
		if _, err := store.CreateJob(ctx, UpsertJobRequest{
			ID: jobID, TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
		}); err != nil {
			t.Fatalf("CreateJob(%q) error = %v", jobID, err)
		}
	}

	if _, err := store.StartJob(ctx, "tenant-1", "job-first", startedAt); err != nil {
		t.Fatalf("StartJob(first) error = %v", err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-second", startedAt.Add(time.Second)); !errors.Is(err, ErrStackExecutionBusy) {
		t.Fatalf("StartJob(second while first runs) error = %v, want ErrStackExecutionBusy", err)
	}

	blocked, err := store.GetJob(ctx, "tenant-1", "job-second")
	if err != nil {
		t.Fatalf("GetJob(second) error = %v", err)
	}
	if blocked.State != "pending" || blocked.StartedAt != nil {
		t.Fatalf("blocked second job = %#v, want unchanged pending job", blocked)
	}

	completedAt := startedAt.Add(2 * time.Second)
	if _, completeErr := store.CompleteJob(ctx, "tenant-1", "job-first", map[string]any{"ok": true}, completedAt); completeErr != nil {
		t.Fatalf("CompleteJob(first) error = %v", completeErr)
	}
	secondStartedAt := completedAt.Add(time.Second)
	second, err := store.StartJob(ctx, "tenant-1", "job-second", secondStartedAt)
	if err != nil {
		t.Fatalf("StartJob(second after first completed) error = %v", err)
	}
	if second.State != "running" || second.StartedAt == nil || !second.StartedAt.Equal(secondStartedAt) {
		t.Fatalf("started second job = %#v", second)
	}
}

func TestMemoryStoreClaimsExactWaitingResumeOnce(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	nextResumeAt := "2026-07-18T10:00:00Z"
	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-wait", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
		Result: map[string]any{"lease_id": "lease-1", "job_wait": map[string]any{
			"state": "waiting", "reason": "waiting_enrollment", "next_resume_at": nextResumeAt,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, UpsertJobRequest{ID: "job-wait", TenantID: "tenant-1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateJob() error = %v, want ErrConflict", err)
	}
	claimedAt := time.Date(2026, 7, 18, 10, 3, 0, 0, time.UTC)
	claimed, err := store.ClaimWaitingJobResume(ctx, ClaimWaitingJobResumeRequest{
		TenantID: "tenant-1", JobID: "job-wait", StackID: "stack-1", JobType: "deploy",
		WaitReason: "waiting_enrollment", NextResumeAt: nextResumeAt, LeaseID: "lease-1", ServerID: "server-1",
		ResultPatch: map[string]any{"enrollment_resume_key": "resume-1"}, ClaimedAt: claimedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State != "cancelled" || claimed.CompletedAt == nil || !claimed.CompletedAt.Equal(claimedAt) ||
		claimed.Result["enrollment_resume_key"] != "resume-1" {
		t.Fatalf("claimed job = %#v", claimed)
	}
	if _, err := store.ClaimWaitingJobResume(ctx, ClaimWaitingJobResumeRequest{
		TenantID: "tenant-1", JobID: "job-wait", StackID: "stack-1", JobType: "deploy",
		WaitReason: "waiting_enrollment", NextResumeAt: nextResumeAt, LeaseID: "lease-1", ServerID: "server-1",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreReclaimStaleManagedDestroyRecoveryRequiresExactMarkerAndStaleHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	markerKey := "managed_provider_decommission_recovery"
	markerSchema := "techstack.managed-provider-decommission-recovery/v1"
	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-stale-destroy", TenantID: "tenant-1", StackID: "stack-1", Type: "destroy", State: "pending",
		Result: map[string]any{markerKey: map[string]any{
			"schema": markerSchema, "tenant_id": "tenant-1", "stack_id": "stack-1",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-stale-destroy", now); err != nil {
		t.Fatal(err)
	}
	freshRequest := ReclaimStaleManagedDestroyRecoveryRequest{
		TenantID: "tenant-1", JobID: "job-stale-destroy", StackID: "stack-1",
		RecoveryMarkerKey: markerKey, RecoveryMarkerSchema: markerSchema,
		StaleBefore: now.Add(-time.Second), ReclaimedAt: now,
	}
	if _, err := store.ReclaimStaleManagedDestroyRecovery(ctx, freshRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh heartbeat reclaim error = %v, want ErrConflict", err)
	}
	staleRequest := freshRequest
	staleRequest.StaleBefore = now.Add(time.Second)
	staleRequest.ReclaimedAt = now.Add(4 * time.Second)
	staleRequest.RecoveryMarkerSchema = "other-schema"
	if _, err := store.ReclaimStaleManagedDestroyRecovery(ctx, staleRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-marker reclaim error = %v, want ErrConflict", err)
	}
	staleRequest.RecoveryMarkerSchema = markerSchema
	reclaimed, err := store.ReclaimStaleManagedDestroyRecovery(ctx, staleRequest)
	if err != nil {
		t.Fatalf("exact stale reclaim: %v", err)
	}
	if reclaimed.State != jobStatePending || reclaimed.StartedAt != nil || !reclaimed.ScheduledFor.Equal(staleRequest.ReclaimedAt) {
		t.Fatalf("reclaimed job = %#v", reclaimed)
	}
}

func TestMemoryStoreSyncJobSnapshotFencesTerminalAndNewerExecutions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	startedAt := time.Date(2026, 7, 18, 12, 0, 0, 123000, time.UTC)
	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-fenced", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-fenced", startedAt); err != nil {
		t.Fatal(err)
	}

	pendingSnapshot := SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-fenced", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
		},
		ObservedState: "pending",
	}
	if _, err := store.SyncJobSnapshot(ctx, pendingSnapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale pending sync error = %v, want ErrConflict", err)
	}

	waitResult := map[string]any{"lease_id": "lease-fenced", "job_wait": map[string]any{
		"state": "waiting", "reason": "waiting_enrollment", "next_resume_at": "2026-07-18T12:01:00Z",
	}}
	waitingSnapshot := SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-fenced", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
			Result: waitResult,
		},
		ObservedState: "waiting", AttemptStartedAt: &startedAt,
	}
	if _, err := store.SyncJobSnapshot(ctx, waitingSnapshot); err != nil {
		t.Fatalf("current waiting sync: %v", err)
	}
	claimedAt := startedAt.Add(2 * time.Minute)
	if _, err := store.ClaimWaitingJobResume(ctx, ClaimWaitingJobResumeRequest{
		TenantID: "tenant-1", JobID: "job-fenced", StackID: "stack-1", JobType: "deploy",
		WaitReason: "waiting_enrollment", NextResumeAt: "2026-07-18T12:01:00Z", LeaseID: "lease-other", ServerID: "server-fenced",
		ResultPatch: map[string]any{"enrollment_resume_key": "wrong-target"}, ClaimedAt: claimedAt,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-lease claim error = %v, want ErrConflict", err)
	}
	if _, err := store.ClaimWaitingJobResume(ctx, ClaimWaitingJobResumeRequest{
		TenantID: "tenant-1", JobID: "job-fenced", StackID: "stack-1", JobType: "deploy",
		WaitReason: "waiting_enrollment", NextResumeAt: "2026-07-18T12:01:00Z", LeaseID: "lease-fenced", ServerID: "server-fenced",
		ResultPatch: map[string]any{"enrollment_resume_key": "resume-exact"}, ClaimedAt: claimedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncJobSnapshot(ctx, waitingSnapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale waiting sync error = %v, want ErrConflict", err)
	}
	stored, err := store.GetJob(ctx, "tenant-1", "job-fenced")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "cancelled" || stored.Result["enrollment_resume_key"] != "resume-exact" {
		t.Fatalf("terminal source was resurrected or receipt lost: %#v", stored)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-fenced", claimedAt.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("StartJob after terminal handover = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreSyncJobSnapshotReturnsCurrentGenerationToPendingForRetry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	firstStartedAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-retry", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-retry", firstStartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncJobSnapshot(ctx, SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-retry", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
			Message: "Retry scheduled",
		},
		ObservedState: "pending", AttemptStartedAt: &firstStartedAt,
	}); err != nil {
		t.Fatalf("current-generation retry sync: %v", err)
	}
	secondStartedAt := firstStartedAt.Add(time.Second)
	second, err := store.StartJob(ctx, "tenant-1", "job-retry", secondStartedAt)
	if err != nil {
		t.Fatalf("second StartJob: %v", err)
	}
	if second.StartedAt == nil || !second.StartedAt.Equal(secondStartedAt) {
		t.Fatalf("second execution generation = %v, want %v", second.StartedAt, secondStartedAt)
	}
}

func TestMemoryStoreSyncJobSnapshotTransitionsProvisionToDeployWithinGeneration(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	startedAt := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)
	if _, err := store.CreateJob(ctx, UpsertJobRequest{
		ID: "job-auto-deploy", TenantID: "tenant-1", StackID: "stack-1", Type: "provision", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", "job-auto-deploy", startedAt); err != nil {
		t.Fatal(err)
	}
	transitioned, err := store.SyncJobSnapshot(ctx, SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-auto-deploy", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "pending",
			Result: map[string]any{"job_wait": map[string]any{"state": "waiting"}},
		},
		ObservedState: "waiting", AttemptStartedAt: &startedAt,
	})
	if err != nil {
		t.Fatalf("provision-to-deploy waiting sync: %v", err)
	}
	if transitioned.Type != "deploy" || transitioned.State != "pending" || transitioned.StartedAt == nil || !transitioned.StartedAt.Equal(startedAt) {
		t.Fatalf("transitioned job = %#v", transitioned)
	}
	if _, err := store.SyncJobSnapshot(ctx, SyncJobSnapshotRequest{
		Job: UpsertJobRequest{
			ID: "job-auto-deploy", TenantID: "tenant-1", StackID: "stack-1", Type: "provision", State: "pending",
		},
		ObservedState: "pending", AttemptStartedAt: &startedAt,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("deploy-to-provision regression error = %v, want ErrConflict", err)
	}
}

func TestMemoryStoreListActivityFiltersAndSortsBeforeLimit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	events := []ActivityEvent{
		{ID: "activity-z", TenantID: "tenant-a", StackID: "stack-1", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "other-tenant", TenantID: "tenant-b", StackID: "stack-1", CreatedAt: base.Add(10 * time.Minute)},
		{ID: "activity-next", TenantID: "tenant-a", StackID: "stack-1", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "other-stack", TenantID: "tenant-a", StackID: "stack-2", CreatedAt: base.Add(10 * time.Minute)},
		{ID: "activity-a", TenantID: "tenant-a", StackID: "stack-1", CreatedAt: base.Add(3 * time.Minute)},
	}
	for index := range 49 {
		events = append(events, ActivityEvent{
			ID:        fmt.Sprintf("activity-filler-%02d", index),
			TenantID:  "tenant-a",
			StackID:   "stack-1",
			CreatedAt: base.Add(-time.Duration(index) * time.Minute),
		})
	}
	for _, event := range events {
		if _, err := store.AppendActivity(ctx, event); err != nil {
			t.Fatalf("AppendActivity(%q) error = %v", event.ID, err)
		}
	}

	limited, err := store.ListActivity(ctx, " tenant-a ", " stack-1 ", 3)
	if err != nil {
		t.Fatalf("ListActivity(limit=3) error = %v", err)
	}
	wantIDs := []string{"activity-z", "activity-a", "activity-next"}
	if len(limited) != len(wantIDs) {
		t.Fatalf("ListActivity(limit=3) length = %d, want %d: %#v", len(limited), len(wantIDs), limited)
	}
	for index, wantID := range wantIDs {
		if limited[index].ID != wantID {
			t.Fatalf("ListActivity(limit=3)[%d].ID = %q, want %q", index, limited[index].ID, wantID)
		}
	}

	for _, limit := range []int{0, -1} {
		got, err := store.ListActivity(ctx, "tenant-a", "stack-1", limit)
		if err != nil {
			t.Fatalf("ListActivity(limit=%d) error = %v", limit, err)
		}
		if len(got) != 50 {
			t.Fatalf("ListActivity(limit=%d) length = %d, want default 50", limit, len(got))
		}
		if got[0].ID != "activity-z" || got[1].ID != "activity-a" {
			t.Fatalf("ListActivity(limit=%d) starts with %q, %q; want deterministic tie order", limit, got[0].ID, got[1].ID)
		}
	}
}
