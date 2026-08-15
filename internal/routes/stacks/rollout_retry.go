package stacks

import (
	"errors"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

type retryStackRolloutRequest struct {
	SourceJobID string `json:"source_job_id"`
	LeaseID     string `json:"lease_id"`
}

//nolint:dupl // Keep the typed retry contract explicit; authorization, bootstrap, and response projection are shared with enrollment recovery.
func (h crudRouteHandlers) retryStackRollout(e *httpx.Event) error {
	stack, err := h.requireRecoveryStack(e, rolloutRetryUnavailable, "The durable exact rollout retry service is not configured")
	if err != nil {
		return err
	}
	var req retryStackRolloutRequest
	if decodeErr := decodeStrictJSONBody(e.Request.Body, &req); decodeErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid exact rollout retry request", map[string]any{
			detailsKeyReasonCode: "invalid_rollout_retry_body",
		})
	}
	req.SourceJobID = strings.TrimSpace(req.SourceJobID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	if req.SourceJobID == "" || req.LeaseID == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "source_job_id and lease_id are required", map[string]any{
			detailsKeyReasonCode: "rollout_retry_target_required",
		})
	}
	var ownerSpecAccess ownerSpecBootstrapAccess
	result, retryErr := h.orch.RetryRollout(orchestrator.RolloutRetryRequest{
		RequestContext: e.Request.Context(), StackID: stack.ID, TenantID: stack.TenantID,
		OwnerID: stack.OwnerSubjectID, StackName: stack.Name, SourceJobID: req.SourceJobID,
		LeaseID:                 req.LeaseID,
		IssueOwnerSpecBootstrap: h.recoveryOwnerSpecIssuer(stack, &ownerSpecAccess),
	})
	if retryErr != nil {
		return writeRolloutRetryError(e, retryErr)
	}
	return writeRecoveryAccepted(e, recoveryAcceptedResponse{
		Message: "Exact rollout retry accepted", StackID: stack.ID, JobID: result.JobID,
		SourceJobID: req.SourceJobID, LeaseID: result.LeaseID, ServerID: result.ServerID, Replay: result.IdempotentReplay,
	}, ownerSpecAccess)
}

func writeRolloutRetryError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, orchestrator.ErrDeployRuntimeEvidenceUnavailable):
		return rolloutRetryUnavailable(e, "Canonical Guard runtime evidence is temporarily unavailable")
	case errors.Is(err, orchestrator.ErrNoAssignedWorkers):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The managed server is not currently connected through kombify Guard", map[string]any{
			detailsKeyReasonCode: "rollout_retry_guard_not_connected",
		})
	case errors.Is(err, orchestrator.ErrRolloutRetryInvalid), errors.Is(err, stackrouting.ErrInvalid), errors.Is(err, stackrouting.ErrIdempotencyConflict):
		// One reason code covered a dozen distinct rejections, so an operator
		// could not tell "wrong lease" from "wrong job type" from "stack in the
		// wrong state". The bounded detail names the invariant that refused.
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The failed rollout no longer matches this stack and exact active lease", map[string]any{
			detailsKeyReasonCode: "rollout_retry_conflict",
			"detail":             rolloutRetryConflictDetail(err),
		})
	case errors.Is(err, orchestrator.ErrRolloutRetryUnavailable), errors.Is(err, stackrouting.ErrUnavailable):
		return rolloutRetryUnavailable(e, "Exact rollout retry is temporarily unavailable")
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to retry rollout", map[string]any{
			detailsKeyReasonCode: "rollout_retry_internal",
		})
	}
}

// rolloutRetryConflictDetail reduces a rejection to the bounded invariant name
// the orchestrator reported. These messages name a binding or a state, never an
// observed secret or a caller-supplied value, so they are safe to return to the
// owner who asked for the retry.
func rolloutRetryConflictDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	if _, remainder, found := strings.Cut(detail, ": "); found {
		detail = remainder
	}
	detail = strings.TrimSpace(detail)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return detail
}

func rolloutRetryUnavailable(e *httpx.Event, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, map[string]any{
		detailsKeyReasonCode: "rollout_retry_unavailable",
	})
}
