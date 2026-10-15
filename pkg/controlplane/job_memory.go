package controlplane

import (
	"context"
	"fmt"
	"path"
	"reflect"
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
		Payload:      cloneMap(req.Payload),
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
		if _, admitted := existing.Result[RestoreActivationAdmissionResultKey]; admitted {
			return nil, ErrConflict // Admitted restores use the fenced snapshot path.
		}
		job.CreatedAt = existing.CreatedAt
		job.StartedAt = cloneTime(existing.StartedAt)
		job.CompletedAt = cloneTime(existing.CompletedAt)
	}
	s.jobs[job.ID] = job
	return cloneJob(job), nil
}

func (s *MemoryStore) CreateJob(ctx context.Context, req UpsertJobRequest) (*Job, error) {
	return s.createJob(ctx, req, "")
}

func (s *MemoryStore) CreateServiceOperationJob(ctx context.Context, req UpsertJobRequest, serviceID string) (*Job, error) {
	if err := serviceJobReservation(req, serviceID); err != nil {
		return nil, err
	}
	return s.createJob(ctx, req, serviceID)
}

func (s *MemoryStore) createJob(_ context.Context, req UpsertJobRequest, serviceID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if jobWriteBypassesExecutionClaim(req.State) {
		return nil, ErrConflict
	}
	if serviceID != "" {
		for _, existing := range s.jobs {
			if unresolvedServiceJob(existing, req.TenantID, req.StackID, serviceID, req.ID) {
				return nil, ErrServiceOperationBusy
			}
		}
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
		Payload:      cloneMap(req.Payload),
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
	if !restoreDenialMatchesExecution(syncReq) {
		return nil, ErrConflict
	}
	if !restoreAdmissionDirectoryMatchesPayload(req.Result, job.Payload) {
		return nil, ErrConflict
	}
	result, err := retainOriginalRestoreAdmission(job.Result, req.Result, job.Payload)
	if err != nil {
		return nil, err
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
	job.Result = result
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

func retainOriginalRestoreAdmission(stored, incoming, payload map[string]any) (map[string]any, error) {
	if backup, ok := incoming["backup"].(map[string]any); ok && backup["mode"] == "not-admitted" {
		if _, present := stored[RestoreActivationAdmissionResultKey]; present {
			return nil, ErrConflict
		}
		if _, present := incoming[RestoreActivationAdmissionResultKey]; present {
			return nil, ErrConflict
		}
	}
	result := cloneMap(incoming)
	for _, key := range []string{RestoreActivationAdmissionResultKey, RestoreActivationRequestResultKey} {
		next, supplied := incoming[key]
		if supplied {
			if value, ok := next.(map[string]any); !ok || len(value) == 0 {
				return nil, ErrConflict
			}
		}
		prior, exists := stored[key]
		if !exists {
			continue
		}
		value, ok := prior.(map[string]any)
		equivalent := reflect.DeepEqual(prior, next)
		if key == RestoreActivationAdmissionResultKey && supplied && ok {
			if _, bound := value["working_directory"]; !bound {
				if projected, valid := next.(map[string]any); valid && restoreAdmissionDirectoryMatchesPayload(incoming, payload) {
					withoutDirectory := cloneMap(projected)
					delete(withoutDirectory, "working_directory")
					equivalent = reflect.DeepEqual(value, withoutDirectory)
				}
			}
		}
		if !ok || len(value) == 0 || (supplied && !equivalent) {
			return nil, ErrConflict
		}
		if result == nil {
			result = map[string]any{}
		}
		result[key] = cloneMap(value)
	}
	return result, nil
}

func restoreDenialMatchesExecution(req SyncJobSnapshotRequest) bool {
	backup, ok := req.Job.Result["backup"].(map[string]any)
	if !ok || backup["mode"] != "not-admitted" {
		return true
	}
	if backup["job_id"] != req.Job.ID || req.AttemptStartedAt == nil {
		return false
	}
	value, ok := backup["attempt_started_at"].(string)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && at.Equal(req.AttemptStartedAt.UTC().Truncate(time.Microsecond))
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

func (s *MemoryStore) GetOriginalRestoreActivation(_ context.Context, tenantID, stackID, jobID string) (*OriginalRestoreActivation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID || job.StackID != stackID {
		return nil, ErrNotFound
	}
	return originalRestoreActivationFromJob(job)
}

func originalRestoreActivationFromJob(job Job) (*OriginalRestoreActivation, error) {
	admission, ok := job.Result[RestoreActivationAdmissionResultKey].(map[string]any)
	directory, directoryOK := job.Payload["working_directory"].(string)
	// These are Linux node paths, independent of the controller's host OS.
	if job.Type != "stackkit_lifecycle" || !ok || len(admission) == 0 ||
		job.Payload["operation"] != "backup_restore_activate" || !directoryOK ||
		!path.IsAbs(directory) || path.Clean(directory) != directory ||
		strings.TrimSpace(directory) != directory || strings.ContainsAny(directory, "\\\x00") ||
		admission["tenant_id"] != job.TenantID || admission["stack_id"] != job.StackID ||
		admission["activation_operation_id"] != "restore-"+job.ID ||
		!restoreAdmissionDirectoryMatchesPayload(job.Result, job.Payload) {
		return nil, ErrConflict
	}
	return &OriginalRestoreActivation{JobID: job.ID, TenantID: job.TenantID, StackID: job.StackID,
		WorkingDirectory: directory, Admission: cloneMap(admission)}, nil
}

func restoreAdmissionDirectoryMatchesPayload(result, payload map[string]any) bool {
	admission, ok := result[RestoreActivationAdmissionResultKey].(map[string]any)
	if !ok {
		return true
	}
	directory, supplied := admission["working_directory"]
	if !supplied { // Legacy proof is resolved only by the exact-row read above.
		return true
	}
	value, valid := directory.(string)
	return valid && value != "" && payload["working_directory"] == value
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
	if _, admitted := job.Result[RestoreActivationAdmissionResultKey]; admitted {
		directory, valid := job.Payload["working_directory"].(string)
		if !valid || directory == "" || payload["working_directory"] != directory ||
			payload["operation"] != job.Payload["operation"] || !restoreAdmissionDirectoryMatchesPayload(job.Result, payload) {
			return ErrConflict
		}
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
	agentID, _ := job.Payload["agent_id"].(string)
	if s.memoryNodeUnderMaintenanceLocked(tenantID, agentID, job.StackID) {
		return nil, ErrNodeUnderMaintenance
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
	if state == jobStateCompleted {
		if _, admitted := job.Result[RestoreActivationAdmissionResultKey]; admitted {
			return nil, ErrConflict // Restore completion uses SyncJobSnapshot.
		}
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

func (s *MemoryStore) FindUnresolvedServiceJob(_ context.Context, tenantID, stackID, serviceID, excludeJobID string) (string, error) {
	if err := serviceJobScope(tenantID, stackID, serviceID); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, candidate := range s.jobs {
		if unresolvedServiceJob(candidate, tenantID, stackID, serviceID, excludeJobID) {
			return candidate.ID, nil
		}
	}
	return "", nil
}

func (s *MemoryStore) GetServiceOperationJob(_ context.Context, tenantID, stackID, serviceID, jobID string) (*Job, error) {
	if err := serviceJobScope(tenantID, stackID, serviceID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID || job.StackID != stackID || !serviceJobTarget(job.Result, job.Payload, serviceID) {
		return nil, ErrNotFound
	}
	return cloneJob(job), nil
}
