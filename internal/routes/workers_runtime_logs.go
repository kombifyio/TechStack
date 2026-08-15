package routes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const maxWorkerRuntimeLogPayload = 128 << 10

type workerRuntimeLogRequest struct {
	RuntimeAgentID string            `json:"runtime_agent_id"`
	TenantID       string            `json:"tenant_id,omitempty"`
	OwnerID        string            `json:"owner_id,omitempty"`
	StackID        string            `json:"stack_id,omitempty"`
	LeaseID        string            `json:"lease_id,omitempty"`
	ServerID       string            `json:"server_id,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	Log            *agentpb.LogEntry `json:"log"`
}

func (h workerRouteHandlers) ingestRuntimeLog(e *httpx.Event) error {
	if h.runtimeLogs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, "UNAVAILABLE", "Runtime log ingestion is unavailable", nil)
	}
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if id == "" {
		return httpx.BadRequest(e, "runtime agent id is required", nil)
	}
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, maxWorkerRuntimeLogPayload))
	decoder.DisallowUnknownFields()
	var request workerRuntimeLogRequest
	if err := decoder.Decode(&request); err != nil {
		return httpx.BadRequest(e, "Invalid runtime log record", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return httpx.BadRequest(e, "Invalid runtime log record", nil)
	}
	auth, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{
		RuntimeAgentID: request.RuntimeAgentID,
		TenantID:       request.TenantID,
		OwnerID:        request.OwnerID,
		StackID:        request.StackID,
		LeaseID:        request.LeaseID,
		ServerID:       request.ServerID,
	})
	if !ok {
		return nil
	}
	if request.Log == nil || strings.TrimSpace(request.Log.Message) == "" {
		return httpx.BadRequest(e, "runtime log message is required", nil)
	}
	timestamp := time.Unix(request.Log.TimestampUnix, 0).UTC()
	if request.Log.TimestampUnix <= 0 {
		timestamp = time.Now().UTC()
	}
	entry := h.runtimeLogs.AppendRuntimeLog(grpcserver.AgentLogEntry{
		Timestamp: timestamp, TenantID: auth.TenantID, AgentID: auth.RuntimeAgentID,
		Source: "stackkits", Level: request.Log.Level, Message: request.Log.Message,
		StackID: auth.StackID, LeaseID: auth.LeaseID, ServerID: auth.ServerID,
		RuntimeTargetID: auth.RuntimeAgentID, Fields: request.Log.Fields,
	})
	return e.JSON(http.StatusAccepted, map[string]any{"data": map[string]any{"id": entry.ID}})
}
