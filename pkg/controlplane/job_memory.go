package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *MemoryStore) UpsertJob(_ context.Context, req UpsertJobRequest) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobWriteBypassesExecutionClaim(req.State) {
		return nil, ErrConflict
	}

	now := s.now()
	job := Job{
		ID:           req.ID,
		TenantID:     req.TenantID,
		InstanceID:   req.InstanceID,
		StackID:      req.StackID,
		Type:         req.Type,
		State:        firstNonEmpty(req.State, jobStatePending),
		Priority:     req.Priority,
		Progress:     req.Progress,
		Step:         req.Step,
		Message:      req.Message,
		Error:        req.Error,
		ErrorDetails: req.ErrorDetails,
		Logs:         cloneSliceOfMaps(req.Logs),
		Result:       cloneMap(req.Result),
		ScheduledFor: req.ScheduledFor,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if job.ScheduledFor.IsZero() {
		job.ScheduledFor = now
	}
	if existing, ok := s.jobs[job.ID]; ok {
		if existing.State == jobStateRunning {
			return nil, ErrConflict
		}
		job.CreatedAt = existing.CreatedAt
		job.StartedAt = cloneTime(existing.StartedAt)
		job.CompletedAt = cloneTime(existing.CompletedAt)
	}
	s.jobs[job.ID] = job
	return cloneJob(job), nil
}

func (s *MemoryStore) CreateJob(_ context.Context, req UpsertJobRequest) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobWriteBypassesExecutionClaim(req.State) {
		return nil, ErrConflict
	}
	if _, exists := s.jobs[strings.TrimSpace(req.ID)]; exists {
		return nil, ErrConflict
	}
	job := memoryJobFromUpsertRequest(req, s.now())
	s.jobs[job.ID] = job
	return cloneJob(job), nil
}

func memoryJobFromUpsertRequest(req UpsertJobRequest, now time.Time) Job {
	job := Job{
		ID: req.ID, TenantID: req.TenantID, InstanceID: req.InstanceID, StackID: req.StackID,
		Type: req.Type, State: firstNonEmpty(req.State, jobStatePending), Priority: req.Priority,
		Progress: req.Progress, Step: req.Step, Message: req.Message, Error: req.Error,
		ErrorDetails: req.ErrorDetails, Logs: cloneSliceOfMaps(req.Logs), Result: cloneMap(req.Result),
		Payload: cloneMap(req.Payload),
		ScheduledFor: req.ScheduledFor, CreatedAt: now, UpdatedAt: now,
	}
	if job.ScheduledFor.IsZero() {
		job.ScheduledFor = now
	}
	return job
}

func (s *MemoryStore) SyncJobSnapshot(_ context.Context, syncReq SyncJobSnapshotRequest) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := syncReq.Job
	if !validJobSnapshotProjection(syncReq.ObservedState, req.State) {
		return nil, ErrConflict
	}
	job, ok := s.jobs[strings.TrimSpace(req.ID)]
	if !ok || job.TenantID != strings.TrimSpace(req.TenantID) ||
		job.StackID != strings.TrimSpace(req.StackID) || !memoryJobTypeTransitionAllowed(job.Type, req.Type) {
		return nil, ErrConflict
	}
	if !memoryJobSnapshotTransitionAllowed(job, syncReq) {
		return nil, ErrConflict
	}

	job.State = firstNonEmpty(req.State, jobStatePending)
	job.Type = strings.TrimSpace(req.Type)
	job.Priority = req.Priority
	job.Progress = req.Progress
	job.Step = req.Step
	job.Message = req.Message
	job.Error = req.Error
	job.ErrorDetails = req.ErrorDetails
	job.Logs = cloneSliceOfMaps(req.Logs)
	job.Result = cloneMap(req.Result)
	job.ScheduledFor = req.ScheduledFor
	if job.ScheduledFor.IsZero() {
		job.ScheduledFor = s.now()
	}
	job.CompletedAt = cloneTime(syncReq.CompletedAt)
	job.UpdatedAt = s.now()
	s.jobs[job.ID] = job
	// The progress heartbeat is the lease renewal. A snapshot that projects the
	// row out of 'running' surrenders the lease with it.
	if job.State == jobStateRunning {
		s.renewJobExecutionLeaseLocked(job.ID, job.UpdatedAt)
	} else {
		s.releaseJobExecutionLeaseLocked(job.ID)
	}
	return cloneJob(job), nil
}

func memoryJobSnapshotTransitionAllowed(job Job, req SyncJobSnapshotRequest) bool {
	if job.State != jobStatePending && job.State != jobStateRunning {
		return false
	}
	if !sameOptionalTime(job.StartedAt, req.AttemptStartedAt) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(req.ObservedState)) {
	case jobStatePending:
		// A retryable handler failure deliberately returns the current execution
		// to pending. A non-nil matching generation proves this is not a stale
		// pre-claim pending snapshot trying to overwrite a newer runner.
		return job.State == jobStatePending || (job.State == jobStateRunning && req.AttemptStartedAt != nil)
	case jobStateRunning:
		return job.State == jobStateRunning
	case jobStateWaiting, jobStateCompleted, jobStateFailed, jobStateCanceled, jobStateCancelled:
		return true
	default:
		return false
	}
}

func (s *MemoryStore) CancelPendingStackOperations(
	_ context.Context,
	tenantID, stackID string,
	cancelledAt time.Time,
) ([]string, error) {
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return nil, fmt.Errorf("controlplane: tenant and stack id required")
	}
	if cancelledAt.IsZero() {
		cancelledAt = s.now()
	}
	cancelledAt = cancelledAt.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	var cancelled []string
	for id, job := range s.jobs {
		if job.TenantID != tenantID || job.StackID != stackID || job.State != jobStatePending ||
			(job.Type != jobTypeProvision && job.Type != jobTypeDeploy && job.Type != jobTypeDestroy) {
			continue
		}
		if job.Result == nil {
			job.Result = map[string]any{}
		}
		if job.Type == jobTypeDestroy {
			job.Result["destroy_supersession"] = map[string]any{
				"schema":        "techstack.stack-destroy-supersession/v1",
				"stack_id":      stackID,
				"superseded_at": cancelledAt.Format(time.RFC3339Nano),
				"authority":     "new_destroy_admission",
			}
			job.Message = "Superseded by newer stack destroy"
			job.Error = "Job superseded by newer stack destroy"
		} else {
			job.Result["destroy_cancellation"] = map[string]any{
				"schema":       "techstack.stack-rollout-destroy-cancellation/v1",
				"stack_id":     stackID,
				"cancelled_at": cancelledAt.Format(time.RFC3339Nano),
			}
			job.Message = "Superseded by stack destroy"
			job.Error = "Job superseded by stack destroy"
		}
		job.State = jobStateCancelled
		job.ErrorDetails = ""
		job.CompletedAt = &cancelledAt
		job.UpdatedAt = cancelledAt
		s.jobs[id] = job
		s.releaseJobExecutionLeaseLocked(id)
		cancelled = append(cancelled, id)
	}
	sort.Strings(cancelled)
	return cancelled, nil
}

func memoryJobTypeTransitionAllowed(stored, observed string) bool {
	stored = strings.ToLower(strings.TrimSpace(stored))
	observed = strings.ToLower(strings.TrimSpace(observed))
	return stored == observed || (stored == jobTypeProvision && observed == jobTypeDeploy)
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func (s *MemoryStore) ClaimWaitingJobResume(_ context.Context, req ClaimWaitingJobResumeRequest) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[strings.TrimSpace(req.JobID)]
	if !ok || job.TenantID != strings.TrimSpace(req.TenantID) ||
		job.StackID != strings.TrimSpace(req.StackID) ||
		job.Type != strings.TrimSpace(req.JobType) || job.State != jobStatePending ||
		!memoryJobExactRuntimeBinding(job.Result, req.LeaseID, req.ServerID) {
		return nil, ErrConflict
	}
	wait, ok := job.Result[jobWaitResultKey].(map[string]any)
	if !ok || strings.TrimSpace(memoryJobResultString(wait["state"])) != jobStateWaiting ||
		strings.TrimSpace(memoryJobResultString(wait["reason"])) != strings.TrimSpace(req.WaitReason) ||
		strings.TrimSpace(memoryJobResultString(wait["next_resume_at"])) != strings.TrimSpace(req.NextResumeAt) {
		return nil, ErrConflict
	}
	if job.Result == nil {
		job.Result = map[string]any{}
	}
	for key, value := range cloneMap(req.ResultPatch) {
		job.Result[key] = value
	}
	claimedAt := req.ClaimedAt.UTC()
	if claimedAt.IsZero() {
		claimedAt = s.now()
	}
	job.State = jobStateCancelled
	job.CompletedAt = &claimedAt
	job.Message = "Superseded by deterministic managed rollout recovery"
	job.UpdatedAt = claimedAt
	s.jobs[job.ID] = job
	return cloneJob(job), nil
}

func (s *MemoryStore) ReclaimStaleManagedDestroyRecovery(
	_ context.Context,
	req ReclaimStaleManagedDestroyRecoveryRequest,
) (*Job, error) {
	req = normalizeReclaimStaleManagedDestroyRecoveryRequest(req)
	if !validReclaimStaleManagedDestroyRecoveryRequest(req) {
		return nil, fmt.Errorf("controlplane: exact stale managed destroy recovery identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[req.JobID]
	if !ok || job.TenantID != req.TenantID || job.StackID != req.StackID ||
		job.Type != jobTypeDestroy || job.State != jobStateRunning || job.StartedAt == nil ||
		job.UpdatedAt.After(req.StaleBefore) ||
		!memoryManagedDestroyRecoveryMarkerMatches(job.Result, req) {
		return nil, ErrConflict
	}

	reclaimedAt := req.ReclaimedAt
	if reclaimedAt.IsZero() {
		reclaimedAt = s.now()
	}
	reclaimedAt = reclaimedAt.UTC()
	job.State = jobStatePending
	job.StartedAt = nil
	job.CompletedAt = nil
	job.ScheduledFor = reclaimedAt
	job.Message = "Recovering stale managed provider decommission execution"
	job.Error = ""
	job.ErrorDetails = ""
	job.UpdatedAt = reclaimedAt
	s.jobs[job.ID] = job
	s.releaseJobExecutionLeaseLocked(job.ID)
	return cloneJob(job), nil
}

func normalizeReclaimStaleManagedDestroyRecoveryRequest(req ReclaimStaleManagedDestroyRecoveryRequest) ReclaimStaleManagedDestroyRecoveryRequest {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.JobID = strings.TrimSpace(req.JobID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.RecoveryMarkerKey = strings.TrimSpace(req.RecoveryMarkerKey)
	req.RecoveryMarkerSchema = strings.TrimSpace(req.RecoveryMarkerSchema)
	req.StaleBefore = req.StaleBefore.UTC()
	req.ReclaimedAt = req.ReclaimedAt.UTC()
	return req
}

func validReclaimStaleManagedDestroyRecoveryRequest(req ReclaimStaleManagedDestroyRecoveryRequest) bool {
	return req.TenantID != "" && req.JobID != "" && req.StackID != "" &&
		req.RecoveryMarkerKey != "" && req.RecoveryMarkerSchema != "" &&
		!req.StaleBefore.IsZero()
}

func memoryManagedDestroyRecoveryMarkerMatches(
	result map[string]any,
	req ReclaimStaleManagedDestroyRecoveryRequest,
) bool {
	return memoryManagedDestroyRecoveryMarkerFieldsMatch(
		result,
		req.RecoveryMarkerKey,
		req.RecoveryMarkerSchema,
		req.TenantID,
		req.StackID,
	)
}

func memoryManagedDestroyRecoveryMarkerFieldsMatch(result map[string]any, markerKey, markerSchema, tenantID, stackID string) bool {
	marker, ok := result[markerKey].(map[string]any)
	if !ok {
		return false
	}
	return strings.TrimSpace(memoryJobResultString(marker["schema"])) == markerSchema &&
		strings.TrimSpace(memoryJobResultString(marker["tenant_id"])) == tenantID &&
		strings.TrimSpace(memoryJobResultString(marker["stack_id"])) == stackID
}

func memoryJobExactRuntimeBinding(result map[string]any, leaseID, serverID string) bool {
	leaseID = strings.TrimSpace(leaseID)
	serverID = strings.TrimSpace(serverID)
	if leaseID == "" || serverID == "" {
		return false
	}
	foundLease := false
	for _, field := range []string{"lease_id", "runtime_lease_id", "enrollment_resume_lease_id"} {
		candidate := strings.TrimSpace(memoryJobResultString(result[field]))
		if candidate == "" {
			continue
		}
		foundLease = true
		if candidate != leaseID {
			return false
		}
	}
	if !foundLease {
		return false
	}
	for _, field := range []string{jobServerIDKey, "runtime_server_id", "enrollment_resume_server_id"} {
		candidate := strings.TrimSpace(memoryJobResultString(result[field]))
		if candidate != "" && candidate != serverID {
			return false
		}
	}
	return true
}

func memoryJobResultString(value any) string {
	text, _ := value.(string)
	return text
}

func (s *MemoryStore) GetJob(_ context.Context, tenantID, jobID string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneJob(job), nil
}

func (s *MemoryStore) ListJobsByTenant(_ context.Context, tenantID string, limit int) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID == tenantID {
			out = append(out, *cloneJob(job))
		}
	}
	return limitJobs(out, limit), nil
}

func (s *MemoryStore) ListProviderProvisionRecoveryCandidates(
	_ context.Context,
	tenantID, operationID string,
	limit int,
) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID != tenantID ||
			job.Type != jobTypeProvision ||
			job.State != jobStatePending ||
			strings.TrimSpace(memoryJobResultString(job.Result["operation_id"])) != operationID ||
			!memoryProviderProvisionWaitFieldsMatch(job.Result) {
			continue
		}
		out = append(out, *cloneJob(job))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScheduledFor.Equal(out[j].ScheduledFor) {
			return out[i].ID < out[j].ID
		}
		return out[i].ScheduledFor.Before(out[j].ScheduledFor)
	})
	return limitJobs(out, limit), nil
}

func memoryProviderProvisionWaitFieldsMatch(result map[string]any) bool {
	wait, ok := result["job_wait"].(map[string]any)
	if !ok {
		return false
	}
	return strings.TrimSpace(memoryJobResultString(wait["state"])) == "waiting" &&
		strings.TrimSpace(memoryJobResultString(wait["reason"])) == "waiting_provider_provision"
}

func (s *MemoryStore) ListManagedDestroyRecoveryCandidates(
	_ context.Context,
	tenantID, markerKey, markerSchema string,
	limit int,
) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID != tenantID || job.Type != jobTypeDestroy ||
			(job.State != jobStatePending && job.State != jobStateRunning) ||
			!memoryManagedDestroyRecoveryMarkerFieldsMatch(job.Result, markerKey, markerSchema, tenantID, job.StackID) {
			continue
		}
		if job.State == jobStatePending && job.ScheduledFor.After(now) {
			continue
		}
		out = append(out, *cloneJob(job))
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := managedDestroyRecoveryCandidateAt(out[i]), managedDestroyRecoveryCandidateAt(out[j])
		if left.Equal(right) {
			return out[i].ID < out[j].ID
		}
		return left.Before(right)
	})
	return limitJobs(out, limit), nil
}

func managedDestroyRecoveryCandidateAt(job Job) time.Time {
	if job.State == jobStateRunning {
		return job.UpdatedAt
	}
	return job.ScheduledFor
}

func (s *MemoryStore) ListJobsByStack(_ context.Context, tenantID, stackID string, limit int) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID == tenantID && job.StackID == stackID {
			out = append(out, *cloneJob(job))
		}
	}
	return limitJobs(out, limit), nil
}

func (s *MemoryStore) ListPendingJobs(_ context.Context, tenantID string, limit int) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID == tenantID && job.State == "pending" && !job.ScheduledFor.After(s.now()) {
			out = append(out, *cloneJob(job))
		}
	}
	return limitJobs(out, limit), nil
}

// ListPendingJobTenants pages the tenants that currently hold a pending job.
func (s *MemoryStore) ListPendingJobTenants(_ context.Context, afterTenantID string, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: pending job tenant limit from 1 to 100 is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	tenants := make(map[string]struct{})
	for _, job := range s.jobs {
		if job.State == "pending" && !job.ScheduledFor.After(s.now()) {
			tenants[job.TenantID] = struct{}{}
		}
	}
	afterTenantID = strings.TrimSpace(afterTenantID)
	out := make([]string, 0, len(tenants))
	for tenantID := range tenants {
		if tenantID > afterTenantID {
			out = append(out, tenantID)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CompactPendingJobTenant is a no-op for the memory store: the directory is
// derived from the live job map on every call.
func (s *MemoryStore) CompactPendingJobTenant(context.Context, string) error { return nil }

// SetJobPayload replaces the recovery payload of a pending job.
func (s *MemoryStore) SetJobPayload(_ context.Context, tenantID, jobID string, payload map[string]any) error {
	if len(payload) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[strings.TrimSpace(jobID)]
	if !ok || job.TenantID != strings.TrimSpace(tenantID) || job.State != jobStatePending {
		return fmt.Errorf("%w: job %s is missing or no longer pending", ErrConflict, jobID)
	}
	job.Payload = cloneMap(payload)
	job.UpdatedAt = s.now()
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryStore) StartJob(_ context.Context, tenantID, jobID string, at time.Time) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return nil, ErrNotFound
	}
	if job.State != jobStatePending {
		return nil, ErrConflict
	}
	if strings.TrimSpace(job.StackID) != "" {
		for candidateID, candidate := range s.jobs {
			if candidateID != jobID && candidate.TenantID == tenantID && candidate.StackID == job.StackID && candidate.State == jobStateRunning {
				return nil, ErrStackExecutionBusy
			}
		}
	}
	job.State = jobStateRunning
	job.StartedAt = &at
	job.UpdatedAt = at
	s.jobs[jobID] = job
	// StartJob is the only transition into 'running', so it is the only place
	// an execution lease is issued.
	s.issueJobExecutionLeaseLocked(jobID, at)
	return cloneJob(job), nil
}

func (s *MemoryStore) CompleteJob(_ context.Context, tenantID, jobID string, result map[string]any, at time.Time) (*Job, error) {
	return s.setJobState(tenantID, jobID, "completed", at, func(job *Job) {
		job.Progress = 100
		job.Result = cloneMap(result)
		job.CompletedAt = &at
	})
}

func (s *MemoryStore) FailJob(_ context.Context, tenantID, jobID string, message, details string, at time.Time) (*Job, error) {
	return s.setJobState(tenantID, jobID, "failed", at, func(job *Job) {
		job.Error = message
		job.ErrorDetails = details
		job.CompletedAt = &at
	})
}

func (s *MemoryStore) setJobState(tenantID, jobID, state string, at time.Time, mutate func(*Job)) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return nil, ErrNotFound
	}
	if job.State != jobStateRunning {
		return nil, ErrConflict
	}
	job.State = state
	job.UpdatedAt = at
	mutate(&job)
	s.jobs[jobID] = job
	s.releaseJobExecutionLeaseLocked(jobID)
	return cloneJob(job), nil
}

func limitJobs(jobs []Job, limit int) []Job {
	if limit <= 0 || len(jobs) <= limit {
		return jobs
	}
	return jobs[:limit]
}

var _ JobStore = (*MemoryStore)(nil)
