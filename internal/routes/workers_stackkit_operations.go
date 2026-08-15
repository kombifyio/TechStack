package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/kombifyio/go-common/runtimeexecutor"
	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	stackKitOperationsRequestSchema  = "stackkit.standard-execution-request/v1"
	stackKitOperationsResponseSchema = "stackkit.standard-execution-result/v1"
	stackKitOperationsChannelRef     = "host-channel-cloud-main"
	maxStackKitOperationsPayload     = 16 << 20
)

type WorkerStackKitOperations interface {
	Execute(context.Context, managedstackkit.OperationsRequest) (runtimeexecutor.ExecutionOutcome, error)
}

type workerStackKitOperationsRequest struct {
	SchemaVersion string                           `json:"schemaVersion"`
	ChannelRef    string                           `json:"channelRef"`
	Request       runtimeexecutor.ExecutionRequest `json:"request"`
}

type workerStackKitOperationsResponse struct {
	SchemaVersion string                           `json:"schemaVersion"`
	ChannelRef    string                           `json:"channelRef"`
	Outcome       runtimeexecutor.ExecutionOutcome `json:"outcome"`
}

func (h workerRouteHandlers) executeStackKitOperations(e *httpx.Event) error {
	if h.stackKitOperations == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Managed StackKits operations are unavailable", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if id == "" {
		return httpx.BadRequest(e, "runtime agent id is required", nil)
	}
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, maxStackKitOperationsPayload))
	decoder.DisallowUnknownFields()
	var envelope workerStackKitOperationsRequest
	if err := decoder.Decode(&envelope); err != nil {
		return httpx.BadRequest(e, "Invalid StackKits operations request", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return httpx.BadRequest(e, "Invalid StackKits operations request", nil)
	}
	if envelope.SchemaVersion != stackKitOperationsRequestSchema || envelope.ChannelRef != stackKitOperationsChannelRef {
		return httpx.BadRequest(e, "StackKits operations request is not bound to the managed Cloud channel", nil)
	}
	authCtx, authenticated := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{
		RuntimeAgentID: id,
		TenantID:       requestExplicitTenantID(e),
	})
	if !authenticated {
		return nil
	}
	outcome, err := h.stackKitOperations.Execute(e.Request.Context(), managedstackkit.OperationsRequest{
		TenantID: authCtx.TenantID, StackID: authCtx.StackID, RuntimeAgentID: authCtx.RuntimeAgentID, Request: envelope.Request,
	})
	if err != nil {
		return httpx.Error(e, http.StatusUnprocessableEntity, "STACKKIT_OPERATION_REJECTED", "Managed StackKits operation was rejected", managedstackkit.RejectionDetails(err))
	}
	return e.JSON(http.StatusOK, workerStackKitOperationsResponse{
		SchemaVersion: stackKitOperationsResponseSchema,
		ChannelRef:    stackKitOperationsChannelRef,
		Outcome:       outcome,
	})
}
