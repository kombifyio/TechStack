package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const guardCommandPollTimeout = 10 * time.Second

type WorkerTypedControl interface {
	Poll(context.Context, string, []string) (*agentpb.StackKitCommand, bool, error)
	SubmitResult(string, *agentpb.StackKitResult) error
}

type tenantWorkerTypedControl interface {
	PollForTenant(context.Context, string, string, []string) (*agentpb.StackKitCommand, bool, error)
	SubmitResultForTenant(context.Context, string, string, *agentpb.StackKitResult) error
}

type workerControlRequest struct {
	RuntimeAgentID string                  `json:"runtime_agent_id"`
	TenantID       string                  `json:"tenant_id"`
	OwnerID        string                  `json:"owner_id"`
	StackID        string                  `json:"stack_id"`
	LeaseID        string                  `json:"lease_id"`
	ServerID       string                  `json:"server_id"`
	Capabilities   []string                `json:"capabilities,omitempty"`
	Result         *agentpb.StackKitResult `json:"result,omitempty"`
}

func (request workerControlRequest) authRequest(id string) workerInventoryRequest {
	return workerInventoryRequest{
		RuntimeAgentID: firstNonEmpty(request.RuntimeAgentID, id),
		TenantID:       request.TenantID, OwnerID: request.OwnerID, StackID: request.StackID,
		LeaseID: request.LeaseID, ServerID: request.ServerID,
	}
}

func (h workerRouteHandlers) nextTypedCommand(e *httpx.Event) error {
	if h.typedControl == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Typed HTTPS agent control is unavailable", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	var request workerControlRequest
	if id == "" || json.NewDecoder(e.Request.Body).Decode(&request) != nil {
		return httpx.BadRequest(e, "Invalid typed control poll", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, request.authRequest(id))
	if !ok {
		return nil
	}
	pollCtx, cancel := context.WithTimeout(e.Request.Context(), guardCommandPollTimeout)
	defer cancel()
	var command *agentpb.StackKitCommand
	var found bool
	var err error
	if scoped, ok := h.typedControl.(tenantWorkerTypedControl); ok {
		command, found, err = scoped.PollForTenant(pollCtx, authCtx.TenantID, authCtx.RuntimeAgentID, request.Capabilities)
	} else {
		command, found, err = h.typedControl.Poll(pollCtx, authCtx.RuntimeAgentID, request.Capabilities)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			e.Response.WriteHeader(http.StatusNoContent)
			return nil
		}
		return httpx.Error(e, http.StatusConflict, "CAPABILITY_REQUIRED", err.Error(), nil)
	}
	if !found {
		e.Response.WriteHeader(http.StatusNoContent)
		return nil
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"command": command})
}

func (h workerRouteHandlers) submitTypedCommandResult(e *httpx.Event) error {
	if h.typedControl == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Typed HTTPS agent control is unavailable", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	var request workerControlRequest
	if id == "" || json.NewDecoder(e.Request.Body).Decode(&request) != nil || request.Result == nil {
		return httpx.BadRequest(e, "Invalid typed control result", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, request.authRequest(id))
	if !ok {
		return nil
	}
	var submitErr error
	if scoped, ok := h.typedControl.(tenantWorkerTypedControl); ok {
		submitErr = scoped.SubmitResultForTenant(e.Request.Context(), authCtx.TenantID, authCtx.RuntimeAgentID, request.Result)
	} else {
		submitErr = h.typedControl.SubmitResult(authCtx.RuntimeAgentID, request.Result)
	}
	if err := submitErr; err != nil {
		if errors.Is(err, agentcontrol.ErrCommandNotPending) || errors.Is(err, agentcontrol.ErrResultRejected) {
			return httpx.Error(e, http.StatusConflict, "CONFLICT", "Typed command result does not match a pending command", nil)
		}
		return httpx.Error(e, http.StatusInternalServerError, "INTERNAL", "Failed to accept typed command result", nil)
	}
	return httpx.Success(e, http.StatusAccepted, map[string]any{"command_id": request.Result.GetCommandId(), "accepted": true})
}
