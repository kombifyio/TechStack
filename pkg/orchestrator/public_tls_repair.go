package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// ErrPublicTLSRepairNotNeeded means the stack carries no degraded public TLS
// component to repair.
var ErrPublicTLSRepairNotNeeded = errors.New("stack has no degraded public TLS to repair")

// publicTLSRepairMaxAttempts bounds the queue's backoff retries of one repair
// job; the owner or a later rollout can always start another.
const publicTLSRepairMaxAttempts = 5

// publicTLSRepairStarted remembers the rollouts whose automatic repair this
// process already started: a terminal snapshot can be synced more than once.
var publicTLSRepairStarted sync.Map

// EnqueuePublicTLSRepair installs the managed origin certificate on the node
// of a stack whose rollout completed with public TLS degraded. The hostnames
// come from that rollout's recorded component, never from the caller.
func (o *Orchestrator) EnqueuePublicTLSRepair(ctx context.Context, tenantID, ownerID, stackID string) (string, error) {
	if o == nil {
		return "", ErrStackKitLifecycleUnavailable
	}
	stack, err := o.findStackForJob(ctx, stackID, ProvisionStackOptions{RequestContext: ctx, TenantID: tenantID, OwnerID: ownerID})
	if err != nil {
		return "", err
	}
	if stack.tenantID != tenantID || stack.ownerID != ownerID {
		return "", controlplane.ErrNotFound
	}
	hosts := jobs.DegradedPublicTLSHosts(stack.runtimeSummary)
	if len(hosts) == 0 {
		return "", ErrPublicTLSRepairNotNeeded
	}
	return o.EnqueueStackKitLifecycle(ctx, jobs.StackKitLifecycleRequest{
		StackID: stack.id, TenantID: tenantID, OwnerID: ownerID,
		Operation: jobs.StackKitLifecyclePublicTLSRepair, OwnerApproved: true, OriginHosts: hosts,
	})
}

// repairDegradedPublicTLS starts the automatic repair once a rollout has
// completed with public TLS degraded. Its failures are logged; the stack keeps
// the degraded component, so the owner can still start the repair.
func (o *Orchestrator) repairDegradedPublicTLS(tenantID string, job jobs.JobSnapshot) {
	if job.Type != jobs.JobTypeDeploy || job.State != jobs.JobStateCompleted || len(jobs.DegradedPublicTLSHosts(job.Result)) == 0 {
		return
	}
	ownerID := firstNonEmptyJobString(job, stackOwnerIDField)
	if ownerID == "" {
		return
	}
	if _, started := publicTLSRepairStarted.LoadOrStore(job.ID, struct{}{}); started {
		return
	}
	repairJobID, err := o.EnqueuePublicTLSRepair(o.ctx, tenantID, ownerID, job.TargetID)
	if err != nil {
		o.log.Warn("public_tls_repair_enqueue_failed", "stack_id", job.TargetID, "job_id", job.ID, "error", err)
		return
	}
	o.log.Info("public_tls_repair_enqueued", "stack_id", job.TargetID, "job_id", repairJobID, "source_job_id", job.ID)
}

// projectPublicTLSRepair replaces the stack's degraded components with the
// outcome of a terminal repair job, leaving the rest of the projection as is.
func (o *Orchestrator) projectPublicTLSRepair(tenantID string, job jobs.JobSnapshot) {
	if job.Type != jobs.JobTypeStackKitLifecycle || job.Result == nil ||
		!strings.EqualFold(stringFromAny(job.Payload["operation"]), jobs.StackKitLifecyclePublicTLSRepair) {
		return
	}
	if job.State != jobs.JobStateCompleted && job.State != jobs.JobStateFailed {
		return
	}
	components, provided := job.Result[jobs.DegradedComponentsResultField]
	store := o.effectiveStackStore()
	if !provided || store == nil {
		return
	}
	current, err := store.GetStack(o.ctx, tenantID, job.TargetID)
	if err != nil {
		o.log.Warn("public_tls_repair_projection_failed", "stack_id", job.TargetID, "error", err)
		return
	}
	summary := map[string]any{}
	for key, value := range current.RuntimeSummary {
		summary[key] = value
	}
	summary[jobs.DegradedComponentsResultField] = components
	if _, err := store.UpdateStackRuntime(o.ctx, tenantID, job.TargetID, controlplane.RuntimeUpdate{
		RuntimeSummary: summary, DriftStatus: current.DriftStatus, DriftCheckedAt: current.DriftCheckedAt,
	}); err != nil {
		o.log.Warn("public_tls_repair_projection_failed", "stack_id", job.TargetID, "error", err)
	}
}
