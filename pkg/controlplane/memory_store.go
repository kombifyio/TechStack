package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

type MemoryStore struct {
	mu                sync.RWMutex
	now               func() time.Time
	homelabs          map[string]Homelab
	wizardRuns        map[string]WizardRun
	stacks            map[string]Stack
	jobs              map[string]Job
	jobLeases         map[string]memoryJobExecutionLease
	worker            map[string]Worker
	tokens            map[string]PairingToken
	nodes             map[string]Node
	svcs              map[string]Service
	serviceRuntime    map[string]ServiceRuntime
	rilSrv            map[string]RILServer
	rilCmd            map[string]RILCommand
	rilEvt            map[string]RILHealEvent
	rilCrd            map[string]RILActionCard
	servers           map[string]ServerRuntime
	serverTransitions map[string][]ServerStateTransition
	serverInventory   map[string][]ServerInventorySnapshot
	serverOutbox      []ServerRegistryOutboxItem
	serverGuardEpochs map[string]struct{}
	nextServerEventID int64
	wallet            map[string]WalletItem
	events            map[string]ActivityEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:               func() time.Time { return time.Now().UTC() },
		homelabs:          make(map[string]Homelab),
		wizardRuns:        make(map[string]WizardRun),
		stacks:            make(map[string]Stack),
		jobs:              make(map[string]Job),
		jobLeases:         make(map[string]memoryJobExecutionLease),
		worker:            make(map[string]Worker),
		tokens:            make(map[string]PairingToken),
		nodes:             make(map[string]Node),
		svcs:              make(map[string]Service),
		serviceRuntime:    make(map[string]ServiceRuntime),
		rilSrv:            make(map[string]RILServer),
		rilCmd:            make(map[string]RILCommand),
		rilEvt:            make(map[string]RILHealEvent),
		rilCrd:            make(map[string]RILActionCard),
		servers:           make(map[string]ServerRuntime),
		serverTransitions: make(map[string][]ServerStateTransition),
		serverInventory:   make(map[string][]ServerInventorySnapshot),
		serverGuardEpochs: make(map[string]struct{}),
		wallet:            make(map[string]WalletItem),
		events:            make(map[string]ActivityEvent),
	}
}

func (s *MemoryStore) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
		return
	}
	s.now = now
}

func (s *MemoryStore) CreateStack(_ context.Context, req CreateStackRequest) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.stacks[req.ID]; exists {
		return nil, ErrConflict
	}
	if existing := s.activeStackByNameLocked(req.TenantID, req.OwnerSubjectID, req.Name); existing != nil {
		return nil, ErrConflict
	}

	now := s.now()
	stack := Stack{
		ID:             req.ID,
		TenantID:       req.TenantID,
		InstanceID:     req.InstanceID,
		OwnerSubjectID: req.OwnerSubjectID,
		HomelabID:      strings.TrimSpace(req.HomelabID),
		Name:           req.Name,
		Description:    req.Description,
		Mode:           firstNonEmpty(req.Mode, "easy"),
		Status:         firstNonEmpty(req.Status, "draft"),
		Config:         cloneMap(req.Config),
		Services:       cloneSliceOfMaps(req.Services),
		RuntimeSummary: map[string]any{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.stacks[stack.ID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) GetStack(_ context.Context, tenantID, stackID string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return cloneStack(stack), nil
}

// GetStackIncludingDeleted returns one exact tenant-scoped stack for durable
// receipt authorization. Product inventory must continue to use GetStack so
// archived stacks remain invisible there.
func (s *MemoryStore) GetStackIncludingDeleted(_ context.Context, tenantID, stackID string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneStack(stack), nil
}

func (s *MemoryStore) GetActiveStackByName(_ context.Context, tenantID, ownerSubjectID, name string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack := s.activeStackByNameLocked(tenantID, ownerSubjectID, name)
	if stack == nil {
		return nil, ErrNotFound
	}
	return cloneStack(*stack), nil
}

func (s *MemoryStore) ListStacksByTenant(_ context.Context, tenantID string) ([]Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Stack, 0)
	for _, stack := range s.stacks {
		if stack.TenantID == tenantID && stack.DeletedAt == nil {
			out = append(out, *cloneStack(stack))
		}
	}
	return out, nil
}

func (s *MemoryStore) SoftDeleteStack(_ context.Context, tenantID, stackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return ErrNotFound
	}
	now := s.now()
	stack.DeletedAt = &now
	stack.UpdatedAt = now
	stack.Status = "stopped"
	s.stacks[stackID] = stack
	return nil
}

func (s *MemoryStore) UpdateStackRuntime(_ context.Context, tenantID, stackID string, runtime RuntimeUpdate) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if runtime.Status != "" {
		stack.Status = runtime.Status
	}
	stack.RuntimeSummary = cloneMap(runtime.RuntimeSummary)
	stack.DriftStatus = runtime.DriftStatus
	stack.DriftCheckedAt = cloneTime(runtime.DriftCheckedAt)
	stack.UpdatedAt = s.now()
	s.stacks[stackID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) UpdateStackConfig(_ context.Context, tenantID, stackID string, config map[string]any) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	stack.Config = cloneMap(config)
	stack.UpdatedAt = s.now()
	s.stacks[stackID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) SetStackHomelab(_ context.Context, tenantID, stackID, homelabID string) (*Stack, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	homelabID = strings.TrimSpace(homelabID)
	if homelabID == "" {
		return nil, fmt.Errorf("controlplane: homelab id required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	stack.HomelabID = homelabID
	stack.UpdatedAt = s.now()
	s.stacks[stackID] = stack
	return cloneStack(stack), nil
}

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

func (s *MemoryStore) UpsertWorkerHeartbeat(_ context.Context, worker Worker) (*Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	key := workerKey(worker.TenantID, worker.ID)
	if existing, ok := s.worker[key]; ok {
		worker.CreatedAt = existing.CreatedAt
		if existing.Approved {
			worker.Approved = true
			worker.ApprovedAt = cloneTime(existing.ApprovedAt)
			if worker.Status == "" || worker.Status == "pending" {
				worker.Status = existing.Status
			}
		}
		if existing.OwnerSubjectID != "" {
			worker.OwnerSubjectID = existing.OwnerSubjectID
		}
		worker.Resources = preserveWorkerCredentialResources(existing.Resources, worker.Resources)
	} else {
		worker.CreatedAt = now
	}
	if worker.LastSeenAt == nil {
		worker.LastSeenAt = &now
	}
	if worker.Status == "" {
		worker.Status = "pending"
	}
	worker.UpdatedAt = now
	worker.Tags = cloneMap(worker.Tags)
	worker.Capabilities = cloneMap(worker.Capabilities)
	worker.Resources = cloneMap(worker.Resources)
	s.worker[key] = worker
	return cloneWorker(worker), nil
}

func (s *MemoryStore) CompareAndSwapWorkerCredential(_ context.Context, command WorkerCredentialCAS) (*Worker, error) {
	prepared, err := normalizeWorkerCredentialCAS(command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := workerKey(prepared.TenantID, prepared.WorkerID)
	worker, ok := s.worker[key]
	if !ok {
		return nil, ErrNotFound
	}
	current, err := WorkerCredentialStateFromWorker(worker)
	if err != nil || !workerCredentialStateEqual(current, prepared.Expected) {
		return nil, ErrConflict
	}
	worker.Resources = cloneMap(worker.Resources)
	if worker.Resources == nil {
		worker.Resources = map[string]any{}
	}
	for key, value := range workerCredentialResources(prepared.Next) {
		worker.Resources[key] = value
	}
	worker.UpdatedAt = s.now()
	s.worker[key] = worker
	return cloneWorker(worker), nil
}

func (s *MemoryStore) GetWorker(_ context.Context, tenantID, workerID string) (*Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	worker, ok := s.worker[workerKey(tenantID, workerID)]
	if !ok || worker.TenantID != tenantID || worker.ID != workerID {
		return nil, ErrNotFound
	}
	return cloneWorker(worker), nil
}

func (s *MemoryStore) ListWorkersByTenant(_ context.Context, tenantID string) ([]Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Worker, 0)
	for _, worker := range s.worker {
		if worker.TenantID == tenantID {
			out = append(out, *cloneWorker(worker))
		}
	}
	return out, nil
}

func (s *MemoryStore) ApproveWorker(_ context.Context, tenantID, workerID, ownerSubjectID string, approvedAt time.Time) (*Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := workerKey(tenantID, workerID)
	worker, ok := s.worker[key]
	if !ok || worker.TenantID != tenantID || worker.ID != workerID || worker.OwnerSubjectID != ownerSubjectID {
		return nil, ErrNotFound
	}
	worker.Approved = true
	worker.Status = "approved"
	worker.ApprovedAt = &approvedAt
	worker.UpdatedAt = approvedAt
	s.worker[key] = worker
	return cloneWorker(worker), nil
}

func (s *MemoryStore) UpsertPairingToken(_ context.Context, token PairingToken) (*PairingToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	key := pairingTokenKey(token.TenantID, token.TokenHash)
	if existingID, ok := s.tokenIDByTenantHashLocked(key); ok {
		token.ID = existingID
	}
	if existing, ok := s.tokens[token.ID]; ok {
		token.CreatedAt = existing.CreatedAt
	} else {
		token.CreatedAt = now
	}
	if token.Status == "" {
		token.Status = "active"
	}
	token.UpdatedAt = now
	token.Metadata = cloneMap(token.Metadata)
	s.tokens[token.ID] = token
	return clonePairingToken(token), nil
}

func (s *MemoryStore) GetPairingTokenByHash(_ context.Context, tenantID, tokenHash string) (*PairingToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, token := range s.tokens {
		if token.TokenHash != tokenHash {
			continue
		}
		if tenantID != "" && token.TenantID != tenantID {
			continue
		}
		return clonePairingToken(token), nil
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) ClaimPairingToken(_ context.Context, tenantID, tokenHash string, claimedAt time.Time) (*PairingToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" || claimedAt.IsZero() {
		return nil, ErrNotFound
	}
	claimedAt = claimedAt.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, token := range s.tokens {
		if token.TenantID != tenantID || token.TokenHash != tokenHash {
			continue
		}
		if token.Status != "active" || token.UsedAt != nil || (token.ExpiresAt != nil && !token.ExpiresAt.After(claimedAt)) {
			return nil, ErrNotFound
		}
		usedAt := claimedAt
		token.Status = "used"
		token.UsedAt = &usedAt
		token.UpdatedAt = claimedAt
		s.tokens[id] = token
		return clonePairingToken(token), nil
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) RevokePairingToken(_ context.Context, tenantID, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.tokens[tokenID]
	if !ok || token.TenantID != tenantID {
		return ErrNotFound
	}
	token.Status = "revoked"
	token.UpdatedAt = s.now()
	s.tokens[tokenID] = token
	return nil
}

func (s *MemoryStore) UpsertNode(_ context.Context, node Node) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.nodes[node.ID]; ok {
		node.CreatedAt = existing.CreatedAt
	} else {
		node.CreatedAt = now
	}
	if node.Role == "" {
		node.Role = "foundation"
	}
	if node.Status == "" {
		node.Status = "pending"
	}
	node.UpdatedAt = now
	node.Metadata = cloneMap(node.Metadata)
	s.nodes[node.ID] = node
	return cloneNode(node), nil
}

func (s *MemoryStore) UpsertService(_ context.Context, service Service) (*Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.svcs[service.ID]; ok {
		service.CreatedAt = existing.CreatedAt
	} else {
		service.CreatedAt = now
	}
	// Provenance and ownership are resolved by the same canonical rule the
	// Postgres store and the 074 backfill use, so the memory adapter can never
	// disagree about who owns a service.
	service = resolvedLegacyServiceOwnership(service)
	service.UpdatedAt = now
	service.Metadata = cloneMap(service.Metadata)
	s.svcs[service.ID] = service
	return cloneService(service), nil
}

func (s *MemoryStore) ListNodesByStack(_ context.Context, tenantID, stackID string) ([]Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Node, 0)
	for _, node := range s.nodes {
		if node.TenantID == tenantID && node.StackID == stackID {
			out = append(out, *cloneNode(node))
		}
	}
	return out, nil
}

func (s *MemoryStore) GetNode(_ context.Context, tenantID, nodeID string) (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok || node.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneNode(node), nil
}

func (s *MemoryStore) GetService(_ context.Context, tenantID, serviceID string) (*Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	service, ok := s.svcs[serviceID]
	if !ok || service.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneService(service), nil
}

func (s *MemoryStore) ListServicesByStack(_ context.Context, tenantID, stackID string) ([]Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Service, 0)
	for _, service := range s.svcs {
		if service.TenantID == tenantID && service.StackID == stackID {
			out = append(out, *cloneService(service))
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteService(_ context.Context, tenantID, serviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	service, ok := s.svcs[serviceID]
	if !ok || service.TenantID != tenantID {
		return ErrNotFound
	}
	delete(s.svcs, serviceID)
	delete(s.serviceRuntime, serviceID)
	return nil
}

func (s *MemoryStore) UpsertServiceRuntime(_ context.Context, service ServiceRuntime) (*ServiceRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	placementOmitted := strings.TrimSpace(service.ServerID) == "" && !serviceregistry.PlacementIntentPresent(service.Placement)
	if existing, ok := s.serviceRuntime[service.ID]; ok {
		service.CreatedAt = existing.CreatedAt
		if placementOmitted {
			service.ServerID = existing.ServerID
			service.Placement = serviceregistry.ClonePlacement(existing.Placement)
		}
	} else {
		service.CreatedAt = now
	}
	service.ServiceInstance = firstNonEmpty(service.ServiceInstance, "default")
	service = canonicalServiceRuntimeStates(service)
	if err := serviceregistry.ValidatePlacement(service.ServerID, service.Placement); err != nil {
		return nil, fmt.Errorf("controlplane: invalid service placement: %w", err)
	}
	if existing, ok := s.serviceRuntime[service.ID]; ok {
		// A measured observation never overwrites stored user intent. This
		// mirrors the aggregate write boundary used by the Postgres store.
		service.DesiredState = existing.DesiredState
		// Ownership is sticky unless the provenance itself changed, exactly as
		// resolveServiceManagementState decides it in the aggregate.
		if strings.EqualFold(existing.Source, service.Source) {
			service.ManagementState = string(serviceregistry.CanonicalManagementState(existing.ManagementState))
		}
	}
	service.UpdatedAt = now
	service.Access = cloneMap(service.Access)
	service.Metadata = cloneMap(service.Metadata)
	service.Capabilities = append([]string(nil), service.Capabilities...)
	s.serviceRuntime[service.ID] = service
	if legacy, ok := s.svcs[service.ID]; ok {
		legacy.Status = derivedServiceStatus(legacy, service.ObservedState)
		legacy.Source = service.Source
		legacy.ManagementState = service.ManagementState
		legacy.URL, _ = service.Access["url"].(string)
		mergedMetadata := cloneMap(legacy.Metadata)
		for key, value := range service.Metadata {
			mergedMetadata[key] = value
		}
		legacy.Metadata = mergedMetadata
		legacy.UpdatedAt = now
		s.svcs[service.ID] = legacy
	}
	return cloneServiceRuntime(service), nil
}

func (s *MemoryStore) GetServiceRuntime(_ context.Context, tenantID, serviceID string) (*ServiceRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	service, ok := s.serviceRuntime[serviceID]
	if ok && service.TenantID == tenantID {
		return cloneServiceRuntime(service), nil
	}
	legacy, ok := s.svcs[serviceID]
	if !ok || legacy.TenantID != tenantID {
		return nil, ErrNotFound
	}
	serverID := legacyServiceServerID(legacy, s.servers)
	if serverID == "" {
		return nil, ErrNotFound
	}
	return cloneServiceRuntime(backfilledServiceRuntime(legacy, serverID)), nil
}

func (s *MemoryStore) ListServiceRuntimes(_ context.Context, tenantID, stackID, serverID string) ([]ServiceRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServiceRuntime, 0)
	canonicalIdentities := make(map[string]bool, len(s.serviceRuntime))
	canonicalIDs := make(map[string]bool, len(s.serviceRuntime))
	for _, service := range s.serviceRuntime {
		if service.TenantID != tenantID {
			continue
		}
		canonicalIDs[service.ID] = true
		canonicalIdentities[serviceRuntimeIdentity(service.StackID, service.ServerID, service.ServiceKey, service.ServiceInstance)] = true
		if (stackID != "" && service.StackID != stackID) || (serverID != "" && service.ServerID != serverID) {
			continue
		}
		out = append(out, *cloneServiceRuntime(service))
	}
	for _, legacy := range s.svcs {
		if legacy.TenantID != tenantID || canonicalIDs[legacy.ID] || (stackID != "" && legacy.StackID != stackID) {
			continue
		}
		mappedServerID := legacyServiceServerID(legacy, s.servers)
		if mappedServerID == "" || (serverID != "" && mappedServerID != serverID) {
			continue
		}
		backfilled := backfilledServiceRuntime(legacy, mappedServerID)
		if canonicalIdentities[serviceRuntimeIdentity(backfilled.StackID, backfilled.ServerID, backfilled.ServiceKey, backfilled.ServiceInstance)] {
			continue
		}
		out = append(out, *cloneServiceRuntime(backfilled))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StackID != out[j].StackID {
			return out[i].StackID < out[j].StackID
		}
		if out[i].ServiceKey != out[j].ServiceKey {
			return out[i].ServiceKey < out[j].ServiceKey
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemoryStore) UpsertServerRuntime(_ context.Context, server ServerRuntime) (*ServerRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(server.ID) == "" || strings.TrimSpace(server.TenantID) == "" {
		return nil, ErrNotFound
	}
	now := s.now()
	targetOmitted := !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget)
	if existing, ok := s.servers[server.ID]; ok {
		if existing.TenantID != server.TenantID {
			return nil, ErrConflict
		}
		server.CreatedAt = existing.CreatedAt
		if server.LifecycleReasonCode == "" {
			server.LifecycleReasonCode = existing.LifecycleReasonCode
		}
		if server.DesiredReasonCode == "" {
			server.DesiredReasonCode = existing.DesiredReasonCode
		}
		if server.ConnectionReasonCode == "" {
			server.ConnectionReasonCode = existing.ConnectionReasonCode
		}
		if server.HealthReasonCode == "" {
			server.HealthReasonCode = existing.HealthReasonCode
		}
		if server.LifecycleChangedAt.IsZero() {
			server.LifecycleChangedAt = existing.LifecycleChangedAt
		}
		if server.DesiredChangedAt.IsZero() {
			server.DesiredChangedAt = existing.DesiredChangedAt
		}
		if server.ConnectionChangedAt.IsZero() {
			server.ConnectionChangedAt = existing.ConnectionChangedAt
		}
		if server.HealthChangedAt.IsZero() {
			server.HealthChangedAt = existing.HealthChangedAt
		}
		server.Revision = existing.Revision + 1
		if server.Generation <= 0 {
			server.Generation = existing.Generation
		}
		if server.SourceEpoch == "" {
			server.SourceAuthority = existing.SourceAuthority
			server.SourceID = existing.SourceID
			server.SourceEpoch = existing.SourceEpoch
			server.SourceSequence = existing.SourceSequence
			server.SourceObservedAt = cloneTime(existing.SourceObservedAt)
		}
		if targetOmitted {
			server.RuntimeTarget = serverregistry.CloneRuntimeTarget(existing.RuntimeTarget)
		}
	} else {
		server.CreatedAt = now
		server.Revision = 1
	}
	serverRuntimeDefaults(&server, now)
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	server.UpdatedAt = now
	s.servers[server.ID] = server
	return cloneServerRuntime(server), nil
}

func (s *MemoryStore) EnsureServerRuntimeProjection(_ context.Context, server ServerRuntime) (*ServerRuntime, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(server.ID) == "" || strings.TrimSpace(server.TenantID) == "" {
		return nil, false, ErrNotFound
	}
	if existing, ok := s.servers[server.ID]; ok {
		if existing.TenantID != server.TenantID {
			return nil, false, ErrConflict
		}
		return cloneServerRuntime(existing), false, nil
	}
	now := s.now()
	serverRuntimeDefaults(&server, now)
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, false, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	server.CreatedAt = now
	server.UpdatedAt = now
	s.servers[server.ID] = server
	return cloneServerRuntime(server), true, nil
}

func (s *MemoryStore) GetServerRuntime(_ context.Context, tenantID, serverID string) (*ServerRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	server, ok := s.servers[serverID]
	if !ok || server.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneServerRuntime(server), nil
}

func (s *MemoryStore) ListServerRuntimesByTenant(_ context.Context, tenantID, stackID string) ([]ServerRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerRuntime, 0)
	for _, server := range s.servers {
		if server.TenantID != tenantID || (stackID != "" && server.StackID != stackID) {
			continue
		}
		out = append(out, *cloneServerRuntime(server))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *MemoryStore) AppendServerTransition(_ context.Context, transition ServerStateTransition) (*ServerStateTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[transition.ServerID]
	if !ok || server.TenantID != transition.TenantID {
		return nil, ErrNotFound
	}
	s.nextServerEventID++
	transition.ID = s.nextServerEventID
	if transition.ObservedAt.IsZero() {
		transition.ObservedAt = s.now()
	}
	transition.CreatedAt = s.now()
	transition.Evidence = cloneMap(transition.Evidence)
	s.serverTransitions[transition.ServerID] = append(s.serverTransitions[transition.ServerID], transition)
	copy := transition
	copy.Evidence = cloneMap(transition.Evidence)
	return &copy, nil
}

func (s *MemoryStore) ListServerTransitions(_ context.Context, tenantID, serverID string, limit int) ([]ServerStateTransition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	server, ok := s.servers[serverID]
	if !ok || server.TenantID != tenantID {
		return nil, ErrNotFound
	}
	rows := s.serverTransitions[serverID]
	if limit <= 0 {
		limit = 100
	}
	out := make([]ServerStateTransition, 0, len(rows))
	for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
		item := rows[i]
		item.Evidence = cloneMap(item.Evidence)
		out = append(out, item)
	}
	return out, nil
}

func (s *MemoryStore) RecordServerInventory(_ context.Context, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[snapshot.ServerID]
	if !ok || server.TenantID != snapshot.TenantID {
		return nil, ErrNotFound
	}
	if snapshot.Revision <= server.InventoryRevision {
		return nil, ErrConflict
	}
	s.nextServerEventID++
	snapshot.ID = s.nextServerEventID
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = s.now()
	}
	snapshot.CreatedAt = s.now()
	snapshot.Inventory = cloneMap(snapshot.Inventory)
	s.serverInventory[snapshot.ServerID] = append(s.serverInventory[snapshot.ServerID], snapshot)
	server.InventoryRevision = snapshot.Revision
	server.Revision++
	server.UpdatedAt = s.now()
	s.servers[server.ID] = server
	copy := snapshot
	copy.Inventory = cloneMap(snapshot.Inventory)
	return &copy, nil
}

func serverRuntimeDefaults(server *ServerRuntime, now time.Time) {
	server.LifecycleState = firstNonEmpty(server.LifecycleState, "planned")
	server.DesiredState = firstNonEmpty(server.DesiredState, string(serverregistry.DesiredRunning))
	server.ConnectionState = firstNonEmpty(server.ConnectionState, "pending")
	server.HealthState = firstNonEmpty(server.HealthState, "unknown")
	if server.Revision <= 0 {
		server.Revision = 1
	}
	if server.Generation <= 0 {
		server.Generation = 1
	}
	if server.ConnectionChangedAt.IsZero() {
		server.ConnectionChangedAt = now
	}
	if server.LifecycleChangedAt.IsZero() {
		server.LifecycleChangedAt = now
	}
	if server.DesiredChangedAt.IsZero() {
		server.DesiredChangedAt = now
	}
	if server.HealthChangedAt.IsZero() {
		server.HealthChangedAt = now
	}
	server.LastHeartbeatAt = cloneTime(server.LastHeartbeatAt)
	server.SourceObservedAt = cloneTime(server.SourceObservedAt)
	server.DecommissionedAt = cloneTime(server.DecommissionedAt)
	if !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget) {
		if target, ok := serverregistry.HostingerExternalVPSTarget(server.ProviderRef, now); ok {
			server.RuntimeTarget = target
		} else {
			server.RuntimeTarget = serverregistry.UnknownRuntimeTarget()
		}
	} else {
		server.RuntimeTarget = serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	}
	server.Metadata = cloneMap(server.Metadata)
	server.Channels = cloneServerChannels(server.Channels)
}

// ApplyServerEvent commits the aggregate head, transition timeline, and
// optional inventory snapshot while holding the store's single write lock.
func (s *MemoryStore) ApplyServerEvent(_ context.Context, event ServerEvent) (*ServerEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyServerEventLocked(event)
}

func (s *MemoryStore) applyServerEventLocked(event ServerEvent) (*ServerEventResult, error) {
	var current *ServerRuntime
	if stored, ok := s.servers[event.ServerID]; ok {
		if stored.TenantID != event.TenantID {
			return nil, ErrConflict
		}
		current = cloneServerRuntime(stored)
	}
	now := s.now()
	epochKey := serverGuardEpochKey(event)
	_, sourceEpochSeen := s.serverGuardEpochs[epochKey]
	prepared, err := prepareServerEvent(current, event, now, sourceEpochSeen)
	if err != nil {
		return nil, err
	}
	if !prepared.applied {
		return &ServerEventResult{Server: cloneServerRuntime(prepared.server), Applied: false}, nil
	}

	prepared.server.CreatedAt = now
	if current != nil {
		prepared.server.CreatedAt = current.CreatedAt
	}
	prepared.server.UpdatedAt = now
	s.servers[event.ServerID] = *cloneServerRuntime(prepared.server)

	transitions := make([]ServerStateTransition, 0, len(prepared.transitions))
	for _, transition := range prepared.transitions {
		s.nextServerEventID++
		transition.ID = s.nextServerEventID
		transition.CreatedAt = now
		transition.Evidence = cloneMap(transition.Evidence)
		s.serverTransitions[event.ServerID] = append(s.serverTransitions[event.ServerID], transition)
		transitions = append(transitions, transition)
	}

	var inventory *ServerInventorySnapshot
	if prepared.inventory != nil {
		s.nextServerEventID++
		item := *prepared.inventory
		item.ID = s.nextServerEventID
		item.CreatedAt = now
		item.Inventory = cloneMap(item.Inventory)
		s.serverInventory[event.ServerID] = append(s.serverInventory[event.ServerID], item)
		copy := item
		copy.Inventory = cloneMap(item.Inventory)
		inventory = &copy
	}

	s.nextServerEventID++
	outbox := *prepared.outbox
	outbox.ID = s.nextServerEventID
	outbox.CreatedAt = now
	outbox.Payload = cloneMap(outbox.Payload)
	s.serverOutbox = append(s.serverOutbox, outbox)
	outboxCopy := outbox
	outboxCopy.Payload = cloneMap(outbox.Payload)
	if event.Authority == ServerEventAuthorityGuard {
		s.serverGuardEpochs[epochKey] = struct{}{}
	}

	return &ServerEventResult{
		Server: cloneServerRuntime(prepared.server), Transitions: transitions,
		Inventory: inventory, Outbox: &outboxCopy, Applied: true,
	}, nil
}

func serverGuardEpochKey(event ServerEvent) string {
	return strings.Join([]string{
		strings.TrimSpace(event.TenantID), strings.TrimSpace(event.ServerID),
		fmt.Sprintf("%d", event.Generation), strings.TrimSpace(event.SourceID),
		strings.TrimSpace(event.SourceEpoch),
	}, "\x00")
}

func (s *MemoryStore) UpsertRILServer(_ context.Context, server RILServer) (*RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilSrv[server.ID]; ok {
		server.CreatedAt = existing.CreatedAt
	} else {
		server.CreatedAt = now
	}
	if server.Status == "" {
		server.Status = "unknown"
	}
	server.UpdatedAt = now
	server.Health = cloneMap(server.Health)
	server.Inventory = cloneMap(server.Inventory)
	server.LastSeenAt = cloneTime(server.LastSeenAt)
	s.rilSrv[server.ID] = server
	return cloneRILServer(server), nil
}

func (s *MemoryStore) ListRILServersByTenant(_ context.Context, tenantID string) ([]RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]RILServer, 0)
	for _, server := range s.rilSrv {
		if server.TenantID != tenantID {
			continue
		}
		out = append(out, *cloneRILServer(server))
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].LastSeenAt, out[j].LastSeenAt
		switch {
		case left != nil && right != nil && !left.Equal(*right):
			return left.After(*right)
		case (left != nil) != (right != nil):
			return left != nil
		default:
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
	})
	return out, nil
}

func (s *MemoryStore) GetRILServer(_ context.Context, tenantID, serverID string) (*RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if server, ok := s.rilSrv[serverID]; ok && server.TenantID == tenantID {
		return cloneRILServer(server), nil
	}
	for _, server := range s.rilSrv {
		if server.TenantID == tenantID && server.NodeID == serverID {
			return cloneRILServer(server), nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) GetRILCommand(_ context.Context, tenantID, commandID string) (*RILCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	command, ok := s.rilCmd[commandID]
	if !ok || command.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneRILCommand(command), nil
}

func (s *MemoryStore) EnqueueRILCommand(_ context.Context, command RILCommand) (*RILCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilCmd[command.ID]; ok {
		command.CreatedAt = existing.CreatedAt
	} else {
		command.CreatedAt = now
	}
	if command.Status == "" {
		command.Status = "queued"
	}
	command.UpdatedAt = now
	command.Request = cloneMap(command.Request)
	command.Result = cloneMap(command.Result)
	command.CompletedAt = cloneTime(command.CompletedAt)
	s.rilCmd[command.ID] = command
	return cloneRILCommand(command), nil
}

func (s *MemoryStore) UpsertActionCard(_ context.Context, card RILActionCard) (*RILActionCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilCrd[card.ID]; ok {
		card.CreatedAt = existing.CreatedAt
	} else {
		card.CreatedAt = now
	}
	if card.Status == "" {
		card.Status = "open"
	}
	if card.Severity == "" {
		card.Severity = "info"
	}
	card.UpdatedAt = now
	card.Action = cloneMap(card.Action)
	card.Decision = cloneMap(card.Decision)
	card.ResolvedAt = cloneTime(card.ResolvedAt)
	s.rilCrd[card.ID] = card
	return cloneRILActionCard(card), nil
}

func (s *MemoryStore) RecordHealEvent(_ context.Context, event RILHealEvent) (*RILHealEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilEvt[event.ID]; ok {
		event.CreatedAt = existing.CreatedAt
	} else {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	event.Details = cloneMap(event.Details)
	s.rilEvt[event.ID] = event
	return cloneRILHealEvent(event), nil
}

func (s *MemoryStore) UpsertWalletItem(_ context.Context, item WalletItem) (*WalletItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.wallet[item.ID]; ok {
		item.CreatedAt = existing.CreatedAt
	} else {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	item.Metadata = cloneMap(item.Metadata)
	s.wallet[item.ID] = item
	return cloneWalletItem(item), nil
}

func (s *MemoryStore) GetWalletItem(_ context.Context, tenantID, itemID string) (*WalletItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.wallet[itemID]
	if !ok || item.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneWalletItem(item), nil
}

func (s *MemoryStore) ListWalletItems(_ context.Context, tenantID, stackID string) ([]WalletItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]WalletItem, 0)
	for _, item := range s.wallet {
		if item.TenantID != tenantID {
			continue
		}
		if stackID != "" && item.StackID != stackID {
			continue
		}
		out = append(out, *cloneWalletItem(item))
	}
	return out, nil
}

func (s *MemoryStore) DeleteWalletItem(_ context.Context, tenantID, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.wallet[itemID]
	if !ok || item.TenantID != tenantID {
		return ErrNotFound
	}
	delete(s.wallet, itemID)
	return nil
}

func (s *MemoryStore) AppendActivity(_ context.Context, event ActivityEvent) (*ActivityEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	event = normalizeActivityEvent(event)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now()
	}
	event.Details = cloneMap(event.Details)
	s.events[event.ID] = event
	return cloneActivityEvent(event), nil
}

func (s *MemoryStore) ListActivity(ctx context.Context, tenantID, stackID string, limit int) ([]ActivityEvent, error) {
	return s.ListActivityScoped(ctx, tenantID, ActivityFilter{StackID: stackID, Limit: limit})
}

func (s *MemoryStore) ListActivityScoped(_ context.Context, tenantID string, filter ActivityFilter) ([]ActivityEvent, error) {
	tenantID = strings.TrimSpace(tenantID)
	filter.StackID = strings.TrimSpace(filter.StackID)
	filter.RuntimeScopeKey = strings.TrimSpace(filter.RuntimeScopeKey)
	filter.ServerScopeKey = strings.TrimSpace(filter.ServerScopeKey)
	filter.ServiceScopeKey = strings.TrimSpace(filter.ServiceScopeKey)
	if filter.Limit <= 0 {
		filter.Limit = defaultActivityLimit
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	s.mu.RLock()
	out := make([]ActivityEvent, 0)
	for _, event := range s.events {
		if event.TenantID != tenantID {
			continue
		}
		if filter.StackID != "" && event.StackID != filter.StackID {
			continue
		}
		if filter.RuntimeScopeKey != "" && event.RuntimeScopeKey != filter.RuntimeScopeKey {
			continue
		}
		if filter.ServerScopeKey != "" && event.ServerScopeKey != filter.ServerScopeKey {
			continue
		}
		if filter.ServiceScopeKey != "" && event.ServiceScopeKey != filter.ServiceScopeKey {
			continue
		}
		if !filter.CursorCreatedAt.IsZero() && (event.CreatedAt.After(filter.CursorCreatedAt) || (event.CreatedAt.Equal(filter.CursorCreatedAt) && event.ID >= filter.CursorID)) {
			continue
		}
		out = append(out, *cloneActivityEvent(event))
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > filter.Limit {
		return out[:filter.Limit], nil
	}
	return out, nil
}

func (s *MemoryStore) activeStackByNameLocked(tenantID, ownerSubjectID, name string) *Stack {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, stack := range s.stacks {
		if stack.DeletedAt == nil &&
			stack.TenantID == tenantID &&
			stack.OwnerSubjectID == ownerSubjectID &&
			strings.ToLower(strings.TrimSpace(stack.Name)) == needle {
			found := stack
			return &found
		}
	}
	return nil
}

func cloneStack(stack Stack) *Stack {
	stack.Config = cloneMap(stack.Config)
	stack.Services = cloneSliceOfMaps(stack.Services)
	stack.RuntimeSummary = cloneMap(stack.RuntimeSummary)
	stack.DriftCheckedAt = cloneTime(stack.DriftCheckedAt)
	stack.DeletedAt = cloneTime(stack.DeletedAt)
	return &stack
}

func cloneJob(job Job) *Job {
	job.Logs = cloneSliceOfMaps(job.Logs)
	job.Result = cloneMap(job.Result)
	job.StartedAt = cloneTime(job.StartedAt)
	job.CompletedAt = cloneTime(job.CompletedAt)
	return &job
}

func cloneWorker(worker Worker) *Worker {
	worker.ApprovedAt = cloneTime(worker.ApprovedAt)
	worker.LastSeenAt = cloneTime(worker.LastSeenAt)
	worker.Tags = cloneMap(worker.Tags)
	worker.Capabilities = cloneMap(worker.Capabilities)
	worker.Resources = cloneMap(worker.Resources)
	return &worker
}

func clonePairingToken(token PairingToken) *PairingToken {
	token.ExpiresAt = cloneTime(token.ExpiresAt)
	token.UsedAt = cloneTime(token.UsedAt)
	token.Metadata = cloneMap(token.Metadata)
	return &token
}

func cloneNode(node Node) *Node {
	node.Metadata = cloneMap(node.Metadata)
	return &node
}

func cloneService(service Service) *Service {
	service.Metadata = cloneMap(service.Metadata)
	return &service
}

func cloneServiceRuntime(service ServiceRuntime) *ServiceRuntime {
	service.ObservedAt = cloneTime(service.ObservedAt)
	service.Placement = serviceregistry.ClonePlacement(service.Placement)
	service.Access = cloneMap(service.Access)
	service.Metadata = cloneMap(service.Metadata)
	service.Capabilities = append([]string(nil), service.Capabilities...)
	return &service
}

func cloneRILServer(server RILServer) *RILServer {
	server.Health = cloneMap(server.Health)
	server.Inventory = cloneMap(server.Inventory)
	server.LastSeenAt = cloneTime(server.LastSeenAt)
	return &server
}

func cloneServerRuntime(server ServerRuntime) *ServerRuntime {
	server.LastHeartbeatAt = cloneTime(server.LastHeartbeatAt)
	server.DecommissionedAt = cloneTime(server.DecommissionedAt)
	server.RuntimeTarget = serverregistry.CloneRuntimeTarget(server.RuntimeTarget)
	server.Metadata = cloneMap(server.Metadata)
	server.Channels = cloneServerChannels(server.Channels)
	return &server
}

func cloneServerChannels(in []ServerChannel) []ServerChannel {
	if len(in) == 0 {
		return nil
	}
	out := make([]ServerChannel, len(in))
	for i, channel := range in {
		out[i] = channel
		out[i].ObservedAt = cloneTime(channel.ObservedAt)
		out[i].Metadata = cloneMap(channel.Metadata)
	}
	return out
}

func cloneRILCommand(command RILCommand) *RILCommand {
	command.Request = cloneMap(command.Request)
	command.Result = cloneMap(command.Result)
	command.CompletedAt = cloneTime(command.CompletedAt)
	return &command
}

func cloneRILActionCard(card RILActionCard) *RILActionCard {
	card.Action = cloneMap(card.Action)
	card.Decision = cloneMap(card.Decision)
	card.ResolvedAt = cloneTime(card.ResolvedAt)
	return &card
}

func cloneRILHealEvent(event RILHealEvent) *RILHealEvent {
	event.Details = cloneMap(event.Details)
	return &event
}

func cloneWalletItem(item WalletItem) *WalletItem {
	item.Metadata = cloneMap(item.Metadata)
	return &item
}

func cloneActivityEvent(event ActivityEvent) *ActivityEvent {
	event.Details = cloneMap(event.Details)
	return &event
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneSliceOfMaps(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i, item := range in {
		out[i] = cloneMap(item)
	}
	return out
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func limitJobs(jobs []Job, limit int) []Job {
	if limit <= 0 || len(jobs) <= limit {
		return jobs
	}
	return jobs[:limit]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pairingTokenKey(tenantID, tokenHash string) string {
	return tenantID + "\x00" + tokenHash
}

func workerKey(tenantID, workerID string) string {
	return tenantID + "\x00" + workerID
}

func (s *MemoryStore) tokenIDByTenantHashLocked(key string) (string, bool) {
	for _, token := range s.tokens {
		if pairingTokenKey(token.TenantID, token.TokenHash) == key {
			return token.ID, true
		}
	}
	return "", false
}

var _ StackStore = (*MemoryStore)(nil)
var _ JobStore = (*MemoryStore)(nil)
var _ WorkerStore = (*MemoryStore)(nil)
var _ WorkerCredentialStore = (*MemoryStore)(nil)
var _ RegistryStore = (*MemoryStore)(nil)
var _ ServerRuntimeStore = (*MemoryStore)(nil)
var _ RILStore = (*MemoryStore)(nil)
var _ WalletStore = (*MemoryStore)(nil)
var _ ActivityStore = (*MemoryStore)(nil)
