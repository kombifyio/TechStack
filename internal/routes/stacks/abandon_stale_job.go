package stacks

import (
	"errors"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// abandonStaleStackJob is the owner-facing way out of a rollout that stopped
// reporting. Until it existed there was no supported action at all: the job
// held the stack's execution claim forever and every recovery call correctly
// but uselessly replayed it.
func (h crudRouteHandlers) abandonStaleStackJob(e *httpx.Event) error {
	stack, err := h.requireRecoveryStack(e, abandonStaleJobUnavailable, "The stale job recovery service is not configured")
	if err != nil {
		return err
	}
	jobID := strings.TrimSpace(e.Request.PathValue("jobId"))
	if jobID == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "A job id is required", map[string]any{
			detailsKeyReasonCode: "abandon_job_target_required",
		})
	}
	result, abandonErr := h.orch.AbandonStaleJob(orchestrator.AbandonStaleJobRequest{
		RequestContext: e.Request.Context(),
		TenantID:       stack.TenantID,
		StackID:        stack.ID,
		JobID:          jobID,
	})
	if abandonErr != nil {
		return writeAbandonStaleJobError(e, abandonErr)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		recoveryResponseSuccessKey: true,
		specResponseMessageKey:     "Stale job abandoned; the stack is now retryable",
		specResponseStackIDKey:     result.StackID,
		creationJobIDField:         result.JobID,
		"silent_seconds":           int64(result.SilentFor.Seconds()),
		"stack_status":             result.StackStatus,
		"next_step":                "retry-rollout",
	})
}

func writeAbandonStaleJobError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, orchestrator.ErrAbandonJobInvalid):
		// Name the guard that refused. A single opaque reason is how the
		// original stall stayed undiagnosed for hours.
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This job cannot be abandoned", map[string]any{
			detailsKeyReasonCode: "abandon_job_conflict",
			"detail":             rolloutRetryConflictDetail(err),
		})
	case errors.Is(err, orchestrator.ErrAbandonJobUnavailable):
		return abandonStaleJobUnavailable(e, "Stale job recovery is temporarily unavailable")
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to abandon the stale job", map[string]any{
			detailsKeyReasonCode: "abandon_job_internal",
		})
	}
}

func abandonStaleJobUnavailable(e *httpx.Event, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, map[string]any{
		detailsKeyReasonCode: "abandon_job_unavailable",
	})
}
