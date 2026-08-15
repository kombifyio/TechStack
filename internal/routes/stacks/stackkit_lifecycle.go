package stacks

import (
	"errors"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

type stackKitLifecycleRequest struct {
	AgentID       string `json:"agent_id"`
	Operation     string `json:"operation"`
	TargetRelease string `json:"target_release,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
	Offline       bool   `json:"offline,omitempty"`
	OwnerApproved bool   `json:"owner_approved,omitempty"`
	// StackKit names the local execution binding. Apply refuses to infer it,
	// so the caller has to say which kit's Site, node, and channel it owns.
	StackKit string `json:"stackkit,omitempty"`
}

type stackKitLifecycleResponse struct {
	JobID     string `json:"job_id"`
	StackID   string `json:"stack_id"`
	AgentID   string `json:"agent_id"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
}

func (h crudRouteHandlers) startStackKitLifecycle(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}
	if h.orch == nil {
		return httpx.Error(
			e,
			http.StatusServiceUnavailable,
			ksapi.ErrCodeUnavailable,
			"Typed StackKits lifecycle is unavailable",
			nil,
		)
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	tenantID := tenantIDFromRequest(e)
	if stackID == "" || tenantID == "" {
		return httpx.BadRequest(e, "Stack and tenant are required")
	}
	var request stackKitLifecycleRequest
	if err := decodeStrictJSONBody(e.Request.Body, &request); err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	normalized, err := jobs.NormalizeStackKitLifecycleRequest(jobs.StackKitLifecycleRequest{
		StackID:       stackID,
		TenantID:      tenantID,
		OwnerID:       ownerID,
		AgentID:       request.AgentID,
		Operation:     request.Operation,
		TargetRelease: request.TargetRelease,
		DryRun:        request.DryRun,
		Offline:       request.Offline,
		OwnerApproved: request.OwnerApproved,
		StackKit:      request.StackKit,
	})
	if err != nil {
		return httpx.BadRequest(e, err.Error())
	}
	jobID, err := h.orch.EnqueueStackKitLifecycle(e.Request.Context(), normalized)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "Stack or agent not found")
		}
		if errors.Is(err, orchestrator.ErrStackKitLifecycleUnavailable) {
			return httpx.Error(
				e,
				http.StatusServiceUnavailable,
				ksapi.ErrCodeUnavailable,
				"Typed StackKits lifecycle is unavailable",
				map[string]interface{}{"reason": err.Error()},
			)
		}
		return httpx.Error(
			e,
			http.StatusInternalServerError,
			ksapi.ErrCodeInternal,
			"Failed to enqueue typed StackKits lifecycle operation",
			nil,
		)
	}
	return httpx.Success(e, http.StatusAccepted, stackKitLifecycleResponse{
		JobID:     jobID,
		StackID:   stackID,
		AgentID:   normalized.AgentID,
		Operation: normalized.Operation,
		Status:    "queued",
	})
}
