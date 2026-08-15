package routes

import (
	"io"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

const maxBootstrapLogPayload int64 = 16 << 10

// ingestBootstrapLog is the narrow pre-enrollment observability seam. The
// still-active one-use pairing capability scopes the record without claiming
// it, so a failed installer remains retryable and visible on its planned
// server before the Guard can send its first heartbeat.
func (h workerRouteHandlers) ingestBootstrapLog(e *httpx.Event) error {
	if h.runtimeLogs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Runtime log ingestion is unavailable", nil)
	}
	rawToken := bearerToken(e.Request)
	token, tokenHash, err := h.resolveStorePairingToken(e, rawToken)
	if err != nil || token == nil {
		return err
	}
	payload, readErr := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, maxBootstrapLogPayload))
	if readErr != nil {
		return httpx.BadRequest(e, "Invalid bootstrap log record", nil)
	}
	message := strings.TrimSpace(string(payload))
	if message == "" {
		return httpx.BadRequest(e, "bootstrap log message is required", nil)
	}
	leaseID := runtimeLeaseIDFromMetadata(token.Metadata)
	serverID := firstNonEmpty(runtimeidentity.LeaseServerID(leaseID), runtimeServerIDForWorker(workerStoreID(token.TenantID, tokenHash, "bootstrap")))
	agentID := workerStoreID(token.TenantID, tokenHash, "bootstrap")
	if leaseID != "" {
		agentID = workerStoreIDForLease(token.TenantID, leaseID)
	}
	phase := strings.TrimSpace(e.Request.Header.Get("X-Kombify-Log-Phase"))
	level := strings.TrimSpace(e.Request.Header.Get("X-Kombify-Log-Level"))
	entry := h.runtimeLogs.AppendRuntimeLog(grpcserver.AgentLogEntry{
		Timestamp: time.Now().UTC(), TenantID: token.TenantID, AgentID: agentID,
		Source: "installer", Level: level, Message: message, StackID: token.StackID,
		LeaseID: leaseID, ServerID: serverID, RuntimeTargetID: agentID,
		Fields: map[string]string{"phase": phase, "pairing_state": "pre-enrollment"},
	})
	e.Response.Header().Set("Cache-Control", "no-store")
	return e.JSON(http.StatusAccepted, map[string]any{"data": map[string]any{"id": entry.ID}})
}
