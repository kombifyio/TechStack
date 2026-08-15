package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	// DefaultJobExecutionReclaimInterval is how often the reclaimer looks for
	// executions whose lease lapsed. It matches the registry sweeper's cadence:
	// frequent enough that a stack unsticks itself within a minute of the lease
	// expiring, cheap enough to run against a directory of tenant IDs.
	DefaultJobExecutionReclaimInterval = 30 * time.Second

	defaultJobExecutionReclaimTenantPage   = 50
	defaultJobExecutionReclaimMaxTenants   = 500
	defaultJobExecutionReclaimTenantBatch  = 50
	maxJobExecutionReclaimBatchesPerTenant = 20

	// JobReclaimReasonProcessRestart marks an execution whose recorded owner is
	// not the process observing it: the control plane restarted (or the row
	// belongs to a replica that never came back) and the process-local command
	// rendezvous behind that job no longer exists.
	JobReclaimReasonProcessRestart = "orphaned_by_process_restart"
	// JobReclaimReasonLeaseExpired marks an execution owned by this very
	// process whose lease still lapsed: the worker behind it died without
	// unwinding, so the row is just as orphaned.
	JobReclaimReasonLeaseExpired = "orphaned_execution_lease_expired"

	// ProviderEffectRecoverable means re-driving this job is already the
	// product contract, so the failure is presented as retryable.
	ProviderEffectRecoverable = "recoverable"
	// ProviderEffectRequiresOperatorResolution means the attempt may have left
	// a provider side effect whose outcome nobody observed. The execution is
	// terminalized honestly, but nothing is re-dispatched and the provider
	// operation stays in its documented operator-resolution state.
	ProviderEffectRequiresOperatorResolution = "requires_operator_resolution"

	jobExecutionReclaimResultKey = "job_execution_reclaim"
	jobExecutionReclaimSchema    = "techstack.job-execution-reclaim/v1"
)

// JobExecutionReclaimStats reports one reclaim pass for logging and metrics.
type JobExecutionReclaimStats struct {
	TenantsSwept int
	Inspected    int
	Reclaimed    int
	Resumable    int
	Conflicts    int
}

// JobExecutionReclaimConfig wires the reclaimer.
type JobExecutionReclaimConfig struct {
	Orchestrator *Orchestrator
	Store        controlplane.JobExecutionReclaimStore
	Interval     time.Duration
	TenantPage   int
	MaxTenants   int
	TenantBatch  int
	Now          func() time.Time
	OnError      func(error)
	RecordStats  func(JobExecutionReclaimStats)
}

// JobExecutionReclaimer terminalizes durable job rows whose owning process is
// provably gone and releases the per-stack execution claim they were holding.
//
// Without it a job row left 'running' by an OOM kill or a process restart is an
// impossible state that nothing can clear: controlplane.StartJob refuses a
// second running job on the same stack, so every later operation on that stack
// defers with waiting_stack_execution forever, the stack can never be torn
// down, and its VM keeps billing.
//
// Multi-instance safety needs no extra locking, for the same reason the
// server-registry sweeper needs none: every reclaim is a conditional UPDATE
// carrying the owner and lease deadline the scan observed, so an execution that
// renewed its lease between the read and the write wins and the reclaim is
// refused as a benign conflict.
type JobExecutionReclaimer struct {
	orch        *Orchestrator
	store       controlplane.JobExecutionReclaimStore
	interval    time.Duration
	tenantPage  int
	maxTenants  int
	tenantBatch int
	now         func() time.Time
	onError     func(error)
	recordStats func(JobExecutionReclaimStats)
}

// NewJobExecutionReclaimer validates the wiring and clamps every bound.
func NewJobExecutionReclaimer(cfg JobExecutionReclaimConfig) (*JobExecutionReclaimer, error) {
	if cfg.Orchestrator == nil {
		return nil, fmt.Errorf("orchestrator: job execution reclaimer requires an orchestrator")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("orchestrator: job execution reclaimer requires a reclaim store")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultJobExecutionReclaimInterval
	}
	if cfg.TenantPage <= 0 || cfg.TenantPage > 100 {
		cfg.TenantPage = defaultJobExecutionReclaimTenantPage
	}
	if cfg.MaxTenants <= 0 {
		cfg.MaxTenants = defaultJobExecutionReclaimMaxTenants
	}
	if cfg.TenantBatch <= 0 {
		cfg.TenantBatch = defaultJobExecutionReclaimTenantBatch
	}
	if cfg.Now == nil {
		cfg.Now = cfg.Orchestrator.durableRecoveryNow
	}
	return &JobExecutionReclaimer{
		orch:        cfg.Orchestrator,
		store:       cfg.Store,
		interval:    cfg.Interval,
		tenantPage:  cfg.TenantPage,
		maxTenants:  cfg.MaxTenants,
		tenantBatch: cfg.TenantBatch,
		now:         cfg.Now,
		onError:     cfg.OnError,
		recordStats: cfg.RecordStats,
	}, nil
}

// Run reconciles immediately and then on every interval tick until ctx ends.
// The immediate pass is the startup reconciliation: any row this process finds
// 'running' behind an expired lease belongs to a boot that is over.
func (r *JobExecutionReclaimer) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.runPass(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runPass(ctx)
		}
	}
}

func (r *JobExecutionReclaimer) runPass(ctx context.Context) {
	stats, err := r.ReclaimOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		r.reportError(err)
	}
	if r.recordStats != nil {
		r.recordStats(stats)
	}
	if stats.Reclaimed > 0 || stats.Resumable > 0 {
		r.orch.log.Warn("job_execution_reclaim_pass",
			"tenants", stats.TenantsSwept,
			"inspected", stats.Inspected,
			"reclaimed", stats.Reclaimed,
			"left_to_resume", stats.Resumable,
			"conflicts", stats.Conflicts,
		)
	}
}

// ReclaimOnce runs one full bounded reconciliation pass across the due tenants.
func (r *JobExecutionReclaimer) ReclaimOnce(ctx context.Context) (JobExecutionReclaimStats, error) {
	var stats JobExecutionReclaimStats
	if r == nil || r.store == nil {
		return stats, fmt.Errorf("orchestrator: job execution reclaimer is not configured")
	}
	if ctx == nil {
		ctx = r.orch.ctx
	}
	now := r.now().UTC()
	afterTenant := ""
	var passErrors []error
	for stats.TenantsSwept < r.maxTenants {
		limit := r.tenantPage
		if remaining := r.maxTenants - stats.TenantsSwept; limit > remaining {
			limit = remaining
		}
		tenants, err := r.store.ListJobExecutionReclaimTenants(ctx, afterTenant, limit, now)
		if err != nil {
			passErrors = append(passErrors, fmt.Errorf("list job execution reclaim tenants: %w", err))
			break
		}
		if len(tenants) == 0 {
			break
		}
		for _, tenantID := range tenants {
			afterTenant = tenantID
			stats.TenantsSwept++
			if err := r.reclaimTenant(ctx, tenantID, now, &stats); err != nil {
				if errors.Is(err, context.Canceled) {
					return stats, err
				}
				passErrors = append(passErrors, fmt.Errorf("reclaim tenant %s: %w", tenantID, err))
			}
			// Compaction recomputes the tenant's true earliest lease, so a
			// tenant with nothing leased stops appearing in the directory.
			if err := r.store.CompactJobExecutionReclaimTenant(ctx, tenantID); err != nil && !errors.Is(err, context.Canceled) {
				passErrors = append(passErrors, fmt.Errorf("compact reclaim tenant %s: %w", tenantID, err))
			}
		}
		if len(tenants) < limit {
			break
		}
	}
	return stats, errors.Join(passErrors...)
}

func (r *JobExecutionReclaimer) reclaimTenant(
	ctx context.Context,
	tenantID string,
	now time.Time,
	stats *JobExecutionReclaimStats,
) error {
	var tenantErrors []error
	for batch := 0; batch < maxJobExecutionReclaimBatchesPerTenant; batch++ {
		expired, err := r.store.ListExpiredJobExecutionLeases(ctx, tenantID, now, r.tenantBatch)
		if err != nil {
			return err
		}
		if len(expired) == 0 {
			return errors.Join(tenantErrors...)
		}
		progressed := false
		for _, lease := range expired {
			stats.Inspected++
			outcome, err := r.reclaimOne(ctx, lease, now)
			switch {
			case errors.Is(err, context.Canceled):
				return err
			case err != nil:
				tenantErrors = append(tenantErrors, err)
			}
			switch outcome {
			case reclaimOutcomeReclaimed:
				stats.Reclaimed++
				progressed = true
			case reclaimOutcomeLeftToResume:
				stats.Resumable++
			case reclaimOutcomeConflict:
				stats.Conflicts++
			}
		}
		// Rows this pass deliberately left alone stay in the expired page, so
		// stop as soon as a batch produced no terminal transition.
		if !progressed || len(expired) < r.tenantBatch {
			return errors.Join(tenantErrors...)
		}
	}
	return errors.Join(tenantErrors...)
}

type reclaimOutcome int

const (
	reclaimOutcomeNone reclaimOutcome = iota
	reclaimOutcomeReclaimed
	reclaimOutcomeLeftToResume
	reclaimOutcomeConflict
)

// reclaimOne terminalizes exactly one orphaned execution.
//
// The transition is always running -> failed. A reclaim can never report
// success, never re-dispatch, and never touch a provider operation: an
// execution whose outcome nobody observed is recorded as unresolved and stays
// in the operator-resolution path that owns it.
func (r *JobExecutionReclaimer) reclaimOne(
	ctx context.Context,
	lease controlplane.JobExecutionLease,
	now time.Time,
) (reclaimOutcome, error) {
	if strings.TrimSpace(lease.StackID) == "" {
		// A job with no stack holds no per-stack execution claim, so there is
		// nothing stuck behind it and nothing this reclaim would unblock.
		return reclaimOutcomeNone, nil
	}
	observed := controlplane.Job{
		ID: lease.JobID, TenantID: lease.TenantID, StackID: lease.StackID,
		Type: lease.Type, State: persistentStateRunning, Step: lease.Step,
		Result: lease.Result,
	}
	if isManagedProviderDecommissionRecovery(observed) {
		// A durable managed-provider wait is genuinely resumable: native provider
		// lifecycle is generation-bound idempotent and the existing
		// recovery paths move it back to pending instead of failing it.
		// Reclaiming it here would race and destroy that durable resume.
		return reclaimOutcomeLeftToResume, nil
	}
	if isManagedProviderProvisionRecovery(observed) {
		persisted, err := r.store.ResumeExpiredJobExecution(ctx, controlplane.ResumeExpiredJobExecutionRequest{
			TenantID: lease.TenantID, JobID: lease.JobID, StackID: lease.StackID,
			ExpectedOwnerID: strings.TrimSpace(lease.OwnerID), LeaseExpiredBefore: now, ResumedAt: now,
		})
		if err != nil {
			if errors.Is(err, controlplane.ErrConflict) {
				return reclaimOutcomeConflict, nil
			}
			return reclaimOutcomeNone, fmt.Errorf("resume orphaned provider wait %s: %w", lease.JobID, err)
		}
		if err := r.orch.rehydrateProviderProvisionWait(ctx, lease.TenantID, *persisted); err != nil {
			return reclaimOutcomeNone, fmt.Errorf("rehydrate orphaned provider wait %s: %w", lease.JobID, err)
		}
		return reclaimOutcomeLeftToResume, nil
	}

	reasonCode := JobReclaimReasonProcessRestart
	if strings.TrimSpace(lease.OwnerID) == controlplane.ProcessExecutionOwnerID() {
		reasonCode = JobReclaimReasonLeaseExpired
	}
	providerEffect := ProviderEffectRequiresOperatorResolution
	if staleJobHasSafeRecoveryBoundary(observed) {
		providerEffect = ProviderEffectRecoverable
	}
	silentFor := now.Sub(lease.UpdatedAt.UTC()).Round(time.Second)
	if silentFor < 0 {
		silentFor = 0
	}

	message, details, guidance := jobExecutionReclaimNarrative(providerEffect, lease.Step, silentFor)
	patch := map[string]any{
		jobExecutionReclaimResultKey: map[string]any{
			"schema":            jobExecutionReclaimSchema,
			"reason_code":       reasonCode,
			"provider_effect":   providerEffect,
			"retryable":         providerEffect == ProviderEffectRecoverable,
			"job_type":          strings.ToLower(strings.TrimSpace(lease.Type)),
			"step":              lease.Step,
			"previous_owner_id": strings.TrimSpace(lease.OwnerID),
			"reclaimed_by":      controlplane.ProcessExecutionOwnerID(),
			"reclaimed_at":      now.Format(time.RFC3339Nano),
			"lease_expired_at":  lease.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
			"last_progress_at":  lease.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"silent_for":        silentFor.String(),
			"user_guidance":     guidance,
		},
	}

	failed, err := r.store.ReclaimExpiredJobExecution(ctx, controlplane.ReclaimExpiredJobExecutionRequest{
		TenantID:           lease.TenantID,
		JobID:              lease.JobID,
		StackID:            lease.StackID,
		ExpectedOwnerID:    strings.TrimSpace(lease.OwnerID),
		LeaseExpiredBefore: now,
		Error:              message,
		ErrorDetails:       details,
		ResultPatch:        patch,
		ReclaimedAt:        now,
	})
	if err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			// The owner renewed its lease, or another replica reclaimed first.
			// Either way this row is not ours to terminalize.
			return reclaimOutcomeConflict, nil
		}
		return reclaimOutcomeNone, fmt.Errorf("reclaim orphaned job %s: %w", lease.JobID, err)
	}

	// Project the terminal state onto the stack aggregate. This is the step
	// that never runs for a job that dies with its process, and it is what
	// makes the documented retry path reachable again.
	result := map[string]any{}
	if failed != nil {
		for key, value := range failed.Result {
			result[key] = value
		}
	}
	result["tenant_id"] = lease.TenantID
	r.orch.updateStackStatusSnapshot(jobs.JobSnapshot{
		ID:       lease.JobID,
		Type:     jobs.JobType(strings.ToLower(strings.TrimSpace(lease.Type))),
		TargetID: lease.StackID,
		State:    jobs.JobStateFailed,
		Step:     lease.Step,
		Result:   result,
	})
	r.orch.log.Warn("job_execution_reclaimed",
		"job_id", lease.JobID, "stack_id", lease.StackID, "tenant_id", lease.TenantID,
		"reason_code", reasonCode, "provider_effect", providerEffect,
		"previous_owner_id", lease.OwnerID, "step", lease.Step, "silent_for", silentFor.String())
	return reclaimOutcomeReclaimed, nil
}

func jobExecutionReclaimNarrative(providerEffect, step string, silentFor time.Duration) (message, details, guidance string) {
	step = strings.TrimSpace(step)
	if step == "" {
		step = "unknown"
	}
	message = "Orphaned by process restart: the control-plane process running this job is gone"
	if providerEffect == ProviderEffectRecoverable {
		guidance = "Retry the rollout to start a fresh attempt; this stack is no longer blocked."
		details = fmt.Sprintf(
			"No progress for %s at step %q and the execution lease expired, so no process is running this job. %s",
			silentFor, step, guidance)
		return message, details, guidance
	}
	guidance = "This attempt is not re-driven automatically because a provider side effect may have landed unobserved. " +
		"Resolve the provider operation through operator resolution before starting new work on this stack."
	details = fmt.Sprintf(
		"No progress for %s at step %q and the execution lease expired, so no process is running this job. %s",
		silentFor, step, guidance)
	return message, details, guidance
}

func (r *JobExecutionReclaimer) reportError(err error) {
	if r.onError != nil {
		r.onError(err)
		return
	}
	r.orch.log.Error("job_execution_reclaim_failed", "error", err)
}
