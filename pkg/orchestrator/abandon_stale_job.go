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

var (
	// ErrAbandonJobInvalid means the request does not describe a job this owner
	// may abandon.
	ErrAbandonJobInvalid = errors.New("invalid stale job abandon")
	// ErrAbandonJobUnavailable means the durable authorities are missing.
	ErrAbandonJobUnavailable = errors.New("stale job abandon unavailable")
)

// admitStaleJobAbandon enforces every precondition before anything is written:
// the job exists, belongs to this stack, is still marked running, is a type
// whose re-drive is already the product contract, and is explicitly selected
// by its owner. The exact abandon request is the recovery authority; a fixed
// silence delay only strands jobs after a deployment replaces their worker.
func (o *Orchestrator) admitStaleJobAbandon(
	ctx context.Context,
	tenantID, stackID, jobID string,
	now time.Time,
) (*controlplane.Job, time.Duration, error) {
	job, err := o.jobStore.GetJob(ctx, tenantID, jobID)
	if err != nil || job == nil {
		return nil, 0, fmt.Errorf("%w: job is not available", ErrAbandonJobInvalid)
	}
	if strings.TrimSpace(job.StackID) != stackID {
		return nil, 0, fmt.Errorf("%w: job belongs to another stack", ErrAbandonJobInvalid)
	}
	if canonicalEnrollmentJobState(job.State) != string(jobs.JobStateRunning) {
		return nil, 0, fmt.Errorf("%w: only a running job can be abandoned, this one is %q", ErrAbandonJobInvalid, job.State)
	}
	if !staleJobHasSafeRecoveryBoundary(*job) {
		return nil, 0, fmt.Errorf("%w: job type %q has no safe durable recovery boundary", ErrAbandonJobInvalid, job.Type)
	}
	silentFor := now.Sub(job.UpdatedAt.UTC())
	return job, silentFor, nil
}

func staleJobHasSafeRecoveryBoundary(job controlplane.Job) bool {
	switch strings.ToLower(strings.TrimSpace(job.Type)) {
	case string(jobs.JobTypeDeploy):
		return true
	case string(jobs.JobTypeProvision):
		if strings.TrimSpace(stringFromAny(job.Result["lease_id"])) == "" ||
			strings.TrimSpace(stringFromAny(job.Result["runtime_phase"])) != string(jobs.RuntimePhaseLeaseReady) {
			return false
		}
		wait, ok := job.Result["job_wait"].(map[string]any)
		return ok &&
			strings.TrimSpace(stringFromAny(wait["state"])) == string(jobs.JobStateWaiting) &&
			strings.TrimSpace(stringFromAny(wait["reason"])) == jobs.WaitReasonCanonicalGuardEvidence &&
			strings.TrimSpace(stringFromAny(wait["next_resume_at"])) != ""
	default:
		return false
	}
}

// AbandonStaleJobRequest identifies exactly one stranded job.
type AbandonStaleJobRequest struct {
	RequestContext context.Context
	TenantID       string
	StackID        string
	JobID          string
}

// AbandonStaleJobResult reports what the abandon actually changed.
type AbandonStaleJobResult struct {
	JobID       string
	StackID     string
	SilentFor   time.Duration
	StackStatus string
}

// AbandonStaleJob lets the owner of a stack retire one job that is still marked
// running but has no process behind it.
//
// Without this there is no supported way out: a job that dies with its process
// keeps the per-stack execution claim forever, the stack never reaches the
// "error" status its recovery path needs, and every later operation defers.
//
// The boundary stays narrow. A deploy qualifies because re-driving a rollout
// is already the product contract behind retry-rollout. A provision qualifies
// only after its own durable receipt proves the provider lease is ready and it
// had yielded solely for canonical Guard evidence; generic provision and
// destroy jobs may still have provider side effects in flight and are refused.
// Everything is scoped to the caller's tenant, so it stays inside the
// row-level-security model rather than needing a cross-tenant sweep.
func (o *Orchestrator) AbandonStaleJob(req AbandonStaleJobRequest) (*AbandonStaleJobResult, error) {
	if o == nil || o.jobStore == nil {
		return nil, fmt.Errorf("%w: durable job store is not configured", ErrAbandonJobUnavailable)
	}
	tenantID := strings.TrimSpace(req.TenantID)
	stackID := strings.TrimSpace(req.StackID)
	jobID := strings.TrimSpace(req.JobID)
	if tenantID == "" || stackID == "" || jobID == "" {
		return nil, fmt.Errorf("%w: tenant, stack and job are required", ErrAbandonJobInvalid)
	}
	ctx := req.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}

	now := time.Now().UTC()
	job, silentFor, admitErr := o.admitStaleJobAbandon(ctx, tenantID, stackID, jobID, now)
	if admitErr != nil {
		return nil, admitErr
	}

	failed, failErr := o.jobStore.FailJob(ctx, tenantID, jobID,
		"Abandoned: the rollout stopped reporting and its process is gone",
		fmt.Sprintf("No progress for %s at step %q. Retry the rollout to start a fresh attempt.",
			silentFor.Round(time.Second), job.Step),
		now)
	if failErr != nil {
		if errors.Is(failErr, controlplane.ErrConflict) {
			return nil, fmt.Errorf("%w: job changed while being abandoned", ErrAbandonJobInvalid)
		}
		return nil, fmt.Errorf("%w: retire stale job: %v", ErrAbandonJobUnavailable, failErr)
	}

	// Project the terminal state onto the stack. This is the step that never
	// runs for a job that dies with its process, and it is what makes the
	// documented retry path reachable again.
	// The projection resolves its tenant from the snapshot result, and it copies
	// the result into the stack runtime summary, so carry the job's own result
	// forward rather than replacing it with a bare marker.
	result := map[string]any{}
	for key, value := range job.Result {
		result[key] = value
	}
	result["tenant_id"] = tenantID
	o.updateStackStatusSnapshot(jobs.JobSnapshot{
		ID:       jobID,
		Type:     jobs.JobType(strings.ToLower(strings.TrimSpace(job.Type))),
		TargetID: stackID,
		State:    jobs.JobStateFailed,
		Step:     job.Step,
		Result:   result,
	})

	status := persistentStateError
	if failed != nil {
		o.log.Warn("stale_job_abandoned",
			"job_id", jobID, "stack_id", stackID, "tenant_id", tenantID,
			"silent_for", silentFor.String(), "step", job.Step)
	}
	return &AbandonStaleJobResult{
		JobID: jobID, StackID: stackID, SilentFor: silentFor, StackStatus: status,
	}, nil
}
