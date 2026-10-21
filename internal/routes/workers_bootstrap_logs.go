package routes

import (
	"io"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
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
	operationID, cloudInitSHA256 := managedBootstrapBindingStrings(token.Metadata)
	fields := map[string]string{"phase": phase, "pairing_state": "pre-enrollment"}
	if operationID != "" {
		fields["provider_operation_id"] = operationID
	}
	if cloudInitSHA256 != "" {
		fields["cloud_init_sha256"] = cloudInitSHA256
	}
	entry := h.runtimeLogs.AppendRuntimeLog(grpcserver.AgentLogEntry{
		Timestamp: time.Now().UTC(), TenantID: token.TenantID, AgentID: agentID,
		Source: "installer", Level: level, Message: secrets.Redact(message), StackID: token.StackID,
		LeaseID: leaseID, ServerID: serverID, RuntimeActionID: operationID, RuntimeTargetID: agentID,
		Fields: fields,
	})
	// Ordinary installer errors are diagnostics, not terminal admission facts.
	// Only the bounded cloud-init failure reporter supplies a known reason;
	// its operation binding comes from the authenticated pairing capability.
	reason := strings.TrimSpace(e.Request.Header.Get("X-Kombify-Bootstrap-Reason"))
	terminal := false
	switch reason {
	case "bootstrap_tools_missing", "ssh_firewall_failed", "execution_channel_failed", "first_boot_deadline_exceeded", "installer_failed":
		terminal = true
	}
	if terminal && strings.EqualFold(level, "error") && phase == "cloud-init" && leaseID != "" && operationID != "" {
		if _, err := h.applyServerEvent(e.Request.Context(), controlplane.ServerEvent{
			TenantID: token.TenantID, ServerID: serverID,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    "managed-bootstrap", SourceID: operationID, ObservedAt: entry.Timestamp,
			Runtime: controlplane.ServerRuntime{Metadata: map[string]any{
				"guard_bootstrap_failure": map[string]any{
					"reason": reason, "operation_id": operationID,
					"observed_at": entry.Timestamp.Format(time.RFC3339Nano),
				},
			}},
		}); err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Bootstrap failure projection is unavailable", nil)
		}
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return e.JSON(http.StatusAccepted, map[string]any{"data": map[string]any{"id": entry.ID}})
}
