package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type workerProviderControl interface {
	PollWorkerExecution(context.Context, string, string) (*providerexecutor.WorkerExecution, error)
	SubmitWorkerExecutionResult(context.Context, string, string, providerexecutor.WorkerExecutionResult) error
}

type providerWorkerRequest struct {
	InvocationID string `json:"invocation_id,omitempty"`
	workerControlRequest
	ProviderResult *providerexecutor.WorkerExecutionResult `json:"provider_result,omitempty"`
}

func (h workerRouteHandlers) nextProviderCommand(e *httpx.Event) error {
	return h.providerCommand(e, false)
}
func (h workerRouteHandlers) submitProviderCommandResult(e *httpx.Event) error {
	return h.providerCommand(e, true)
}

func (h workerRouteHandlers) providerCommand(e *httpx.Event, result bool) error {
	control, ok := h.typedControl.(workerProviderControl)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Substrate command transport unavailable", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	var request providerWorkerRequest
	if id == "" || json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 5<<20)).Decode(&request) != nil {
		return httpx.BadRequest(e, "Invalid substrate command request", nil)
	}
	auth, ok := h.authenticateRuntimeAgent(e, id, request.authRequest(id))
	if !ok {
		return nil
	}
	if auth.Worker == nil || auth.Worker.Type != "substrate" {
		return httpx.Error(e, http.StatusForbidden, "FORBIDDEN", "An enrolled substrate Guard is required", nil)
	}
	if result {
		if request.ProviderResult == nil {
			return httpx.BadRequest(e, "Substrate result is required", nil)
		}
		if err := control.SubmitWorkerExecutionResult(e.Request.Context(), auth.TenantID, auth.RuntimeAgentID, *request.ProviderResult); err != nil {
			return httpx.Error(e, http.StatusConflict, "CONFLICT", "Substrate result does not match admitted dispatch", nil)
		}
		return httpx.Success(e, http.StatusAccepted, map[string]any{"accepted": true})
	}
	command, err := control.PollWorkerExecution(e.Request.Context(), auth.TenantID, auth.RuntimeAgentID)
	if err != nil {
		return httpx.Error(e, http.StatusConflict, "CONFLICT", "Substrate lease or execution claim is not current", nil)
	}
	if command == nil {
		e.Response.WriteHeader(http.StatusNoContent)
		return nil
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"command": command})
}

func (h workerRouteHandlers) providerBootstrap(e *httpx.Event) error {
	control, ok := h.typedControl.(interface {
		WorkerBootstrap(context.Context, string, string, string) ([]byte, error)
	})
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Substrate bootstrap unavailable", nil)
	}
	var request providerWorkerRequest
	if json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 16384)).Decode(&request) != nil || request.InvocationID == "" {
		return httpx.BadRequest(e, "Invalid bootstrap request", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	auth, ok := h.authenticateRuntimeAgent(e, id, request.authRequest(id))
	if !ok {
		return nil
	}
	if auth.Worker == nil || auth.Worker.Type != "substrate" {
		return httpx.RejectForbidden(e, "Enrolled substrate Guard required")
	}
	payload, err := control.WorkerBootstrap(e.Request.Context(), auth.TenantID, auth.RuntimeAgentID, request.InvocationID)
	if err != nil {
		return httpx.Error(e, http.StatusConflict, "CONFLICT", "Bootstrap dispatch is not current", nil)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{"cloud_init": string(payload)})
}
