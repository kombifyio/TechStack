package stacks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

type resumeStackEnrollmentRequest struct {
	JobID   string `json:"job_id"`
	LeaseID string `json:"lease_id"`
}

const (
	recoveryResponseSuccessKey          = "success"
	recoveryResponseSourceJobIDKey      = "source_job_id"
	recoveryResponseIdempotentReplayKey = "idempotent_replay"
	recoveryResponseProviderVMCreateKey = "provider_vm_create_requested"
)

type recoveryAcceptedResponse struct {
	Message     string
	StackID     string
	JobID       string
	SourceJobID string
	LeaseID     string
	ServerID    string
	Replay      bool
}

//nolint:dupl // Keep the typed resume contract explicit; authorization, bootstrap, and response projection are shared below.
func (h crudRouteHandlers) resumeStackEnrollment(e *httpx.Event) error {
	stack, err := h.requireRecoveryStack(e, enrollmentResumeUnavailable, "The durable enrollment recovery service is not configured")
	if err != nil {
		return err
	}
	var req resumeStackEnrollmentRequest
	if decodeErr := decodeStrictJSONBody(e.Request.Body, &req); decodeErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid enrollment resume request", map[string]any{
			detailsKeyReasonCode: "invalid_enrollment_resume_body",
		})
	}
	req.JobID = strings.TrimSpace(req.JobID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	if req.JobID == "" || req.LeaseID == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "job_id and lease_id are required", map[string]any{
			detailsKeyReasonCode: "enrollment_resume_target_required",
		})
	}

	var ownerSpecAccess ownerSpecBootstrapAccess
	result, resumeErr := h.orch.ResumeEnrollment(orchestrator.EnrollmentResumeRequest{
		RequestContext:          e.Request.Context(),
		StackID:                 stack.ID,
		TenantID:                stack.TenantID,
		OwnerID:                 stack.OwnerSubjectID,
		StackName:               stack.Name,
		SourceJobID:             req.JobID,
		LeaseID:                 req.LeaseID,
		IssueOwnerSpecBootstrap: h.recoveryOwnerSpecIssuer(stack, &ownerSpecAccess),
	})
	if resumeErr != nil {
		return writeEnrollmentResumeError(e, resumeErr)
	}
	return writeRecoveryAccepted(e, recoveryAcceptedResponse{
		Message: "Managed rollout recovery accepted", StackID: stack.ID, JobID: result.JobID,
		SourceJobID: req.JobID, LeaseID: result.LeaseID, ServerID: result.ServerID, Replay: result.IdempotentReplay,
	}, ownerSpecAccess)
}

func (h crudRouteHandlers) requireRecoveryStack(
	e *httpx.Event,
	unavailable func(*httpx.Event, string) error,
	message string,
) (*controlplane.Stack, error) {
	if h.stackStore == nil {
		if _, authErr := requireStackAuth(e); authErr != nil {
			return nil, authErr
		}
		return nil, unavailable(e, message)
	}
	stack, err := h.findOwnedStoreStack(e, strings.TrimSpace(e.Request.PathValue("id")))
	if err != nil {
		return nil, err
	}
	if h.jobStore == nil || h.orch == nil {
		return nil, unavailable(e, message)
	}
	return stack, nil
}

func (h crudRouteHandlers) recoveryOwnerSpecIssuer(
	stack *controlplane.Stack,
	ownerSpecAccess *ownerSpecBootstrapAccess,
) func() (*jobs.OwnerSpecBootstrap, error) {
	return func() (*jobs.OwnerSpecBootstrap, error) {
		access, err := h.ownerSpecBootstrapAccessForStoreDeploy(stack)
		if err != nil {
			return nil, err
		}
		*ownerSpecAccess = access
		return ownerSpecRuntimeBootstrap(access), nil
	}
}

func writeRecoveryAccepted(e *httpx.Event, response recoveryAcceptedResponse, access ownerSpecBootstrapAccess) error {
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		recoveryResponseSuccessKey:          true,
		specResponseMessageKey:              response.Message,
		specResponseStackIDKey:              response.StackID,
		creationJobIDField:                  response.JobID,
		recoveryResponseSourceJobIDKey:      response.SourceJobID,
		routingLeaseIDKey:                   response.LeaseID,
		routingServerIDKey:                  response.ServerID,
		recoveryResponseIdempotentReplayKey: response.Replay,
		recoveryResponseProviderVMCreateKey: false,
	}, access))
}

func decodeStrictJSONBody(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func writeEnrollmentResumeError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, orchestrator.ErrDeployRuntimeEvidenceUnavailable):
		return enrollmentResumeUnavailable(e, "Canonical Guard runtime evidence is temporarily unavailable")
	case errors.Is(err, orchestrator.ErrNoAssignedWorkers):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The managed server is not currently connected through kombify Guard", map[string]any{
			detailsKeyReasonCode: "enrollment_resume_guard_not_connected",
		})
	case errors.Is(err, orchestrator.ErrEnrollmentResumeNotReady):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The planned enrollment check is not overdue yet", map[string]any{
			detailsKeyReasonCode: "enrollment_resume_not_overdue",
		})
	case errors.Is(err, orchestrator.ErrEnrollmentResumeInvalid), errors.Is(err, stackrouting.ErrInvalid), errors.Is(err, stackrouting.ErrIdempotencyConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The waiting job no longer matches this stack and exact lease", map[string]any{
			detailsKeyReasonCode: "enrollment_resume_conflict",
		})
	case errors.Is(err, orchestrator.ErrEnrollmentResumeUnavailable), errors.Is(err, stackrouting.ErrUnavailable):
		return enrollmentResumeUnavailable(e, "Enrollment recovery is temporarily unavailable")
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resume enrollment rollout", map[string]any{
			detailsKeyReasonCode: "enrollment_resume_internal",
		})
	}
}

func enrollmentResumeUnavailable(e *httpx.Event, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, map[string]any{
		detailsKeyReasonCode: "enrollment_resume_unavailable",
	})
}
