package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Server maintenance is an owner-requested node operation (OS update plan, OS
// update, reboot) executed by the node's agent through the pinned StackKits
// CLI (`stackkit host ...`). Its ledger is separate from the stack job table:
// a node operation has no stack, a different blast radius and its own fence.
const (
	ServerMaintenanceActionPlan   = "os_update_plan"
	ServerMaintenanceActionUpdate = "os_update"
	ServerMaintenanceActionReboot = "reboot"

	ServerMaintenanceStateQueued             = "queued"
	ServerMaintenanceStateRunning            = "running"
	ServerMaintenanceStateWaiting            = "waiting"
	ServerMaintenanceStateAwaitingNodeReturn = "awaiting_node_return"
	ServerMaintenanceStateCompleted          = "completed"
	ServerMaintenanceStateFailed             = "failed"
	ServerMaintenanceStateCancelled          = "cancelled"
)

// ErrServerMaintenanceActive is the durable per-server fence. A server holds
// two slots: at most one active reboot or OS update, and separately at most one
// active update plan, so repeated plans can never starve a reboot or update.
// Postgres enforces both with unique partial indexes.
var ErrServerMaintenanceActive = errors.New("controlplane: server maintenance already active")

// ServerMaintenanceJob is one durable maintenance request and its progress.
// The raw Idempotency-Key is never stored; the id is derived from it.
type ServerMaintenanceJob struct {
	ID                string
	TenantID          string
	ServerID          string
	AgentID           string
	StackID           string
	OwnerSubjectID    string
	Action            string
	RequestDigest     string
	State             string
	InventoryRevision int64
	PlanDigest        string
	ReasonCode        string
	Result            map[string]any
	DeadlineAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

// ServerMaintenanceScope selects active maintenance by any bound identity. A
// service action knows its server, a stack action knows its stack and maybe an
// agent; each must see a maintenance job on the same node.
type ServerMaintenanceScope struct {
	ServerID string
	AgentID  string
	StackID  string
}

// ServerMaintenanceUpdate is a compare-and-set progress write by the job
// runner. It applies only while the job is still in ExpectedState.
type ServerMaintenanceUpdate struct {
	ExpectedState string
	State         string
	PlanDigest    string
	ReasonCode    string
	Result        map[string]any
	DeadlineAt    *time.Time
	At            time.Time
}

type ServerMaintenanceStore interface {
	// CreateServerMaintenanceJob inserts a queued job. It returns ErrConflict
	// when the id already exists (an idempotent replay race) and
	// ErrServerMaintenanceActive when the server already has an active job.
	CreateServerMaintenanceJob(ctx context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error)
	// AdmitServerMaintenanceJob is CreateServerMaintenanceJob behind the node
	// check: in one transaction under the node's advisory lock it returns a
	// *NodeBusyError when a pending or running stack or service job targets
	// the job's agent (or names no agent and belongs to its stack), and
	// otherwise inserts the queued job.
	AdmitServerMaintenanceJob(ctx context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error)
	// ClaimServerMaintenanceJob moves a queued job to running with deadline,
	// after the same node check in the same transaction, and records the
	// typed agent command id the runner is about to send (result key
	// command_id), so a restarted runner re-attaches instead of guessing. It
	// returns a *NodeBusyError while the node is busy and ErrConflict when the
	// job is no longer queued.
	ClaimServerMaintenanceJob(ctx context.Context, tenantID, jobID, commandID string, deadline, at time.Time) (*ServerMaintenanceJob, error)
	GetServerMaintenanceJob(ctx context.Context, tenantID, jobID string) (*ServerMaintenanceJob, error)
	// ActiveServerMaintenanceJob returns the active reboot or OS update matching
	// the scope whose deadline has not passed, or ErrNotFound. Plans do not
	// hold the node for other work, and a job past its deadline releases the
	// node even while its runner is stalled.
	ActiveServerMaintenanceJob(ctx context.Context, tenantID string, scope ServerMaintenanceScope) (*ServerMaintenanceJob, error)
	// LatestServerMaintenancePlan returns the newest completed plan for the
	// server whose digest equals planDigest, or ErrNotFound.
	LatestServerMaintenancePlan(ctx context.Context, tenantID, serverID, planDigest string) (*ServerMaintenanceJob, error)
	UpdateServerMaintenanceJob(ctx context.Context, tenantID, jobID string, update ServerMaintenanceUpdate) (*ServerMaintenanceJob, error)
}

// ErrNodeUnderMaintenance defers a stack or service job whose node is being
// rebooted or updated (a claimed reboot or OS update holds it). A queued
// maintenance job does not block: it waits behind the stack job instead.
var ErrNodeUnderMaintenance = errors.New("controlplane: node is under server maintenance")

// serverMaintenanceHoldsNode reports a claimed reboot or OS update whose
// deadline has not passed at now.
func serverMaintenanceHoldsNode(job ServerMaintenanceJob, now time.Time) bool {
	if job.Action == ServerMaintenanceActionPlan || !serverMaintenanceBeforeDeadline(job, now) {
		return false
	}
	switch job.State {
	case ServerMaintenanceStateRunning, ServerMaintenanceStateWaiting, ServerMaintenanceStateAwaitingNodeReturn:
		return true
	default:
		return false
	}
}

// memoryNodeUnderMaintenanceLocked matches a job's resolved agent, or its
// stack when the job names no agent. s.mu must be held.
func (s *MemoryStore) memoryNodeUnderMaintenanceLocked(tenantID, agentID, stackID string) bool {
	agentID, stackID = strings.TrimSpace(agentID), strings.TrimSpace(stackID)
	now := s.now().UTC()
	for _, job := range s.serverMaintenance {
		if job.TenantID != tenantID || !serverMaintenanceHoldsNode(job, now) {
			continue
		}
		if (agentID != "" && job.AgentID == agentID) || (agentID == "" && stackID != "" && job.StackID == stackID) {
			return true
		}
	}
	return false
}

func serverMaintenanceBeforeDeadline(job ServerMaintenanceJob, now time.Time) bool {
	return job.DeadlineAt == nil || job.DeadlineAt.After(now)
}

// NodeBusyError refuses maintenance admission or a claim because a stack or
// service job occupies the node.
type NodeBusyError struct {
	JobID string
}

func (e *NodeBusyError) Error() string {
	return "controlplane: node busy with job " + e.JobID
}

// nodeAdmissionKeys orders the lock keys agent first, then stack. The tenant
// is length-prefixed so no two scopes share a key.
func nodeAdmissionKeys(tenantID, agentID, stackID string) []string {
	tenantID, agentID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(agentID), strings.TrimSpace(stackID)
	keys := make([]string, 0, 2)
	if agentID != "" {
		keys = append(keys, fmt.Sprintf("node-admission:%d:%s:agent:%s", len(tenantID), tenantID, agentID))
	}
	if stackID != "" {
		keys = append(keys, fmt.Sprintf("node-admission:%d:%s:stack:%s", len(tenantID), tenantID, stackID))
	}
	return keys
}

func nodeJobActive(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "pending", "running", "waiting":
		return true
	default:
		return false
	}
}

// activeNodeJobLocked returns the oldest pending or running job whose payload
// targets agentID, or that names no agent and belongs to stackID. s.mu must
// be held.
func (s *MemoryStore) activeNodeJobLocked(tenantID, agentID, stackID string) *Job {
	agentID, stackID = strings.TrimSpace(agentID), strings.TrimSpace(stackID)
	var oldest *Job
	for _, job := range s.jobs {
		if job.TenantID != tenantID || !nodeJobActive(job.State) {
			continue
		}
		target, _ := job.Payload["agent_id"].(string)
		target = strings.TrimSpace(target)
		if (agentID != "" && target == agentID) || (stackID != "" && target == "" && job.StackID == stackID) {
			if oldest == nil || job.CreatedAt.Before(oldest.CreatedAt) {
				oldest = cloneJob(job)
			}
		}
	}
	return oldest
}

// ServerMaintenanceSweepStore lets the maintenance runner find active jobs
// across tenants through a tenant wake-up directory.
type ServerMaintenanceSweepStore interface {
	ListServerMaintenanceTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error)
	ListActiveServerMaintenanceJobs(ctx context.Context, tenantID string, limit int) ([]ServerMaintenanceJob, error)
	// CompactServerMaintenanceTenant retires the tenant's directory entry once
	// it holds no active job.
	CompactServerMaintenanceTenant(ctx context.Context, tenantID string) error
}

func (s *MemoryStore) ListServerMaintenanceTenants(_ context.Context, afterTenantID string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	tenants := make([]string, 0)
	for _, job := range s.serverMaintenance {
		if ServerMaintenanceActiveState(job.State) && job.TenantID > afterTenantID && !seen[job.TenantID] {
			seen[job.TenantID] = true
			tenants = append(tenants, job.TenantID)
		}
	}
	sort.Strings(tenants)
	if limit > 0 && len(tenants) > limit {
		tenants = tenants[:limit]
	}
	return tenants, nil
}

func (s *MemoryStore) ListActiveServerMaintenanceJobs(_ context.Context, tenantID string, limit int) ([]ServerMaintenanceJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]ServerMaintenanceJob, 0)
	for _, job := range s.serverMaintenance {
		if job.TenantID == tenantID && ServerMaintenanceActiveState(job.State) {
			jobs = append(jobs, *cloneServerMaintenanceJob(job))
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	if limit > 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

func (s *MemoryStore) CompactServerMaintenanceTenant(context.Context, string) error { return nil }

// ServerMaintenanceActiveState reports whether a job in state holds the fence.
func ServerMaintenanceActiveState(state string) bool {
	switch state {
	case ServerMaintenanceStateQueued, ServerMaintenanceStateRunning,
		ServerMaintenanceStateWaiting, ServerMaintenanceStateAwaitingNodeReturn:
		return true
	default:
		return false
	}
}

// serverMaintenanceSlot names the fence slot an action occupies.
func serverMaintenanceSlot(action string) string {
	if action == ServerMaintenanceActionPlan {
		return "plan"
	}
	return "mutation"
}

func serverMaintenanceTerminalState(state string) bool {
	switch state {
	case ServerMaintenanceStateCompleted, ServerMaintenanceStateFailed, ServerMaintenanceStateCancelled:
		return true
	default:
		return false
	}
}

func validServerMaintenanceState(state string) bool {
	return ServerMaintenanceActiveState(state) || serverMaintenanceTerminalState(state)
}

func (scope ServerMaintenanceScope) matches(job ServerMaintenanceJob) bool {
	return (scope.ServerID != "" && job.ServerID == scope.ServerID) ||
		(scope.AgentID != "" && job.AgentID == scope.AgentID) ||
		(scope.StackID != "" && job.StackID == scope.StackID)
}

func normalizeServerMaintenanceScope(scope ServerMaintenanceScope) ServerMaintenanceScope {
	return ServerMaintenanceScope{
		ServerID: strings.TrimSpace(scope.ServerID), AgentID: strings.TrimSpace(scope.AgentID), StackID: strings.TrimSpace(scope.StackID),
	}
}

func validateNewServerMaintenanceJob(job ServerMaintenanceJob) error {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.TenantID) == "" || strings.TrimSpace(job.ServerID) == "" ||
		strings.TrimSpace(job.AgentID) == "" || strings.TrimSpace(job.OwnerSubjectID) == "" || strings.TrimSpace(job.RequestDigest) == "" {
		return errors.New("controlplane: complete server maintenance identity required")
	}
	switch job.Action {
	case ServerMaintenanceActionPlan, ServerMaintenanceActionUpdate, ServerMaintenanceActionReboot:
	default:
		return errors.New("controlplane: unsupported server maintenance action")
	}
	if job.State != ServerMaintenanceStateQueued {
		return errors.New("controlplane: a server maintenance job starts queued")
	}
	return nil
}

func cloneServerMaintenanceJob(job ServerMaintenanceJob) *ServerMaintenanceJob {
	job.Result = cloneMap(job.Result)
	if job.DeadlineAt != nil {
		deadline := *job.DeadlineAt
		job.DeadlineAt = &deadline
	}
	if job.CompletedAt != nil {
		completed := *job.CompletedAt
		job.CompletedAt = &completed
	}
	return &job
}

func serverMaintenanceMemoryKey(tenantID, jobID string) string {
	return strings.TrimSpace(tenantID) + "\x00" + strings.TrimSpace(jobID)
}

func (s *MemoryStore) CreateServerMaintenanceJob(_ context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error) {
	if err := validateNewServerMaintenanceJob(job); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createServerMaintenanceJobLocked(job)
}

func (s *MemoryStore) AdmitServerMaintenanceJob(_ context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error) {
	if err := validateNewServerMaintenanceJob(job); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if busy := s.activeNodeJobLocked(job.TenantID, job.AgentID, job.StackID); busy != nil {
		return nil, &NodeBusyError{JobID: busy.ID}
	}
	return s.createServerMaintenanceJobLocked(job)
}

func (s *MemoryStore) ClaimServerMaintenanceJob(_ context.Context, tenantID, jobID, commandID string, deadline, at time.Time) (*ServerMaintenanceJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := serverMaintenanceMemoryKey(tenantID, jobID)
	job, ok := s.serverMaintenance[key]
	if !ok {
		return nil, ErrNotFound
	}
	if job.State != ServerMaintenanceStateQueued {
		return nil, ErrConflict
	}
	if busy := s.activeNodeJobLocked(job.TenantID, job.AgentID, job.StackID); busy != nil {
		return nil, &NodeBusyError{JobID: busy.ID}
	}
	deadline = deadline.UTC()
	job.State, job.DeadlineAt, job.UpdatedAt = ServerMaintenanceStateRunning, &deadline, at.UTC()
	job.Result = cloneMap(job.Result)
	if job.Result == nil {
		job.Result = map[string]any{}
	}
	job.Result["command_id"] = strings.TrimSpace(commandID)
	s.serverMaintenance[key] = job
	return cloneServerMaintenanceJob(job), nil
}

func (s *MemoryStore) createServerMaintenanceJobLocked(job ServerMaintenanceJob) (*ServerMaintenanceJob, error) {
	if s.serverMaintenance == nil {
		s.serverMaintenance = make(map[string]ServerMaintenanceJob)
	}
	if _, exists := s.serverMaintenance[serverMaintenanceMemoryKey(job.TenantID, job.ID)]; exists {
		return nil, ErrConflict
	}
	for _, existing := range s.serverMaintenance {
		if existing.TenantID == job.TenantID && existing.ServerID == job.ServerID && ServerMaintenanceActiveState(existing.State) &&
			serverMaintenanceSlot(existing.Action) == serverMaintenanceSlot(job.Action) {
			return nil, ErrServerMaintenanceActive
		}
	}
	now := s.now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	job.Result = cloneMap(job.Result)
	s.serverMaintenance[serverMaintenanceMemoryKey(job.TenantID, job.ID)] = job
	return cloneServerMaintenanceJob(job), nil
}

func (s *MemoryStore) GetServerMaintenanceJob(_ context.Context, tenantID, jobID string) (*ServerMaintenanceJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.serverMaintenance[serverMaintenanceMemoryKey(tenantID, jobID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneServerMaintenanceJob(job), nil
}

func (s *MemoryStore) ActiveServerMaintenanceJob(_ context.Context, tenantID string, scope ServerMaintenanceScope) (*ServerMaintenanceJob, error) {
	scope = normalizeServerMaintenanceScope(scope)
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UTC()
	for _, job := range s.serverMaintenance {
		if job.TenantID == tenantID && ServerMaintenanceActiveState(job.State) && job.Action != ServerMaintenanceActionPlan &&
			serverMaintenanceBeforeDeadline(job, now) && scope.matches(job) {
			return cloneServerMaintenanceJob(job), nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) LatestServerMaintenancePlan(_ context.Context, tenantID, serverID, planDigest string) (*ServerMaintenanceJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]ServerMaintenanceJob, 0, 1)
	for _, job := range s.serverMaintenance {
		if job.TenantID == tenantID && job.ServerID == serverID && job.Action == ServerMaintenanceActionPlan &&
			job.State == ServerMaintenanceStateCompleted && job.PlanDigest == planDigest && job.CompletedAt != nil {
			matches = append(matches, job)
		}
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].CompletedAt.After(*matches[j].CompletedAt) })
	return cloneServerMaintenanceJob(matches[0]), nil
}

func (s *MemoryStore) UpdateServerMaintenanceJob(_ context.Context, tenantID, jobID string, update ServerMaintenanceUpdate) (*ServerMaintenanceJob, error) {
	if !validServerMaintenanceState(update.State) {
		return nil, errors.New("controlplane: unsupported server maintenance state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := serverMaintenanceMemoryKey(tenantID, jobID)
	job, ok := s.serverMaintenance[key]
	if !ok {
		return nil, ErrNotFound
	}
	if job.State != update.ExpectedState || serverMaintenanceTerminalState(job.State) {
		return nil, ErrConflict
	}
	at := update.At.UTC()
	if at.IsZero() {
		at = s.now().UTC()
	}
	job.State, job.ReasonCode, job.UpdatedAt = update.State, update.ReasonCode, at
	if update.PlanDigest != "" {
		job.PlanDigest = update.PlanDigest
	}
	if update.Result != nil {
		job.Result = cloneMap(update.Result)
	}
	job.DeadlineAt = update.DeadlineAt
	if serverMaintenanceTerminalState(job.State) {
		job.CompletedAt = &at
	}
	s.serverMaintenance[key] = job
	return cloneServerMaintenanceJob(job), nil
}
