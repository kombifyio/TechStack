package stacks

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// wizardRunNoticeStaleAfter bounds how long a non-terminal job may stay silent
// before its dashboard notice is withdrawn. Job execution leases renew every
// 90s (controlplane.JobExecutionLeaseTTL) and waiting jobs resume on their own
// schedule, so half an hour without a single job write means nothing is
// driving it; the job itself stays recoverable through the abandon route.
const wizardRunNoticeStaleAfter = 30 * time.Minute

// wizardRunSupersedeScan bounds the per-deployment job page read to decide
// whether a later rollout already finished.
const wizardRunSupersedeScan = 25

// wizardRunNoticeCurrent decides whether a run still deserves a dashboard
// notice. The server owns this answer so a stuck or orphaned job can never
// keep a notice up: dismissed, terminal, silent and superseded runs are over.
// Runs without a job (failed before dispatch, join awaiting pairing) keep the
// client's own time bounds.
func (h wizardRunHandlers) wizardRunNoticeCurrent(ctx context.Context, tenantID string, run *controlplane.WizardRun, job *controlplane.Job, now time.Time) bool {
	if run.DismissedAt != nil {
		return false
	}
	if job == nil {
		return true
	}
	if !wizardRunJobLive(job.State) {
		return false
	}
	if now.Sub(job.UpdatedAt) > wizardRunNoticeStaleAfter {
		return false
	}
	return !h.wizardRunSuperseded(ctx, tenantID, run, job)
}

func wizardRunJobLive(state string) bool {
	switch jobs.JobState(strings.ToLower(strings.TrimSpace(state))) {
	case jobs.JobStatePending, jobs.JobStateRunning, jobs.JobStateWaiting, "in_progress":
		return true
	default:
		return false
	}
}

// wizardRunSuperseded is true once a later deploy or provision job for the
// same deployment completed: the owner's lab moved on past this run.
func (h wizardRunHandlers) wizardRunSuperseded(ctx context.Context, tenantID string, run *controlplane.WizardRun, job *controlplane.Job) bool {
	stackID := strings.TrimSpace(run.StackID)
	if stackID == "" {
		stackID = strings.TrimSpace(job.StackID)
	}
	if stackID == "" || h.crud.jobStore == nil {
		return false
	}
	later, err := h.crud.jobStore.ListJobsByStack(ctx, tenantID, stackID, wizardRunSupersedeScan)
	if err != nil {
		return false
	}
	for _, candidate := range later {
		if candidate.ID == job.ID || jobs.JobState(candidate.State) != jobs.JobStateCompleted {
			continue
		}
		switch jobs.JobType(strings.ToLower(strings.TrimSpace(candidate.Type))) {
		case jobs.JobTypeDeploy, jobs.JobTypeProvision:
		default:
			continue
		}
		finished := candidate.UpdatedAt
		if candidate.CompletedAt != nil {
			finished = *candidate.CompletedAt
		}
		if finished.After(job.CreatedAt) {
			return true
		}
	}
	return false
}

// wizardRunTargetServerID names the one server the run's rollout belongs to,
// so the dashboard renders the notice at that Node. The job's own target wins;
// the run's node is the fallback. Empty means the client falls back to the
// deployment's Nodes.
func wizardRunTargetServerID(run *controlplane.WizardRun, job *controlplane.Job) string {
	if job != nil {
		for _, source := range []map[string]any{job.Payload, job.Result} {
			for _, key := range []string{"target_server_id", "server_id", "node_id", "agent_id", "worker_id"} {
				if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
	}
	return strings.TrimSpace(run.NodeID)
}

// dismissWizardRun hides one run's dashboard notice for its owner, on every
// device. It never touches the job: cancelling is the abandon route's job.
func (h wizardRunHandlers) dismissWizardRun(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.wizard.runs.write")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wizardRuns == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Wizard runs require the Postgres control plane", wizardRunDetails(
				"wizard_runs_require_control_plane", false,
				"Control plane unavailable",
				"Wizard runs persist through the Postgres control plane, which is not configured on this instance.",
			))
	}
	runID := strings.TrimSpace(e.Request.PathValue("runId"))
	if runID == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "A run id is required", nil)
	}
	if err := h.wizardRuns.DismissWizardRun(e.Request.Context(), tenantID, ownerID, runID, time.Now()); err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Wizard run not found", nil)
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to dismiss the wizard run", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"run_id": runID, "dismissed": true})
}
