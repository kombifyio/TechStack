package stacks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

type repairStackComponentRequest struct {
	Component string `json:"component"`
}

type repairStackComponentResponse struct {
	JobID     string `json:"job_id"`
	StackID   string `json:"stack_id"`
	Component string `json:"component"`
	Status    string `json:"status"`
}

// repairStackComponent starts the repair of one degraded component of a
// completed rollout on the stack's existing server. Only public_tls exists:
// it installs the managed kombify.me origin certificate the rollout could not.
func (h crudRouteHandlers) repairStackComponent(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stackkit.lifecycle")
	if tenantErr != nil {
		return tenantErr
	}
	if h.orch == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Component repair is unavailable", map[string]any{
			detailsKeyReasonCode: "component_repair_unavailable",
		})
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	var request repairStackComponentRequest
	if stackID == "" || decodeStrictJSONBody(e.Request.Body, &request) != nil {
		return httpx.BadRequest(e, "Invalid component repair request")
	}
	if strings.TrimSpace(request.Component) != "public_tls" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Unknown component", map[string]any{
			detailsKeyReasonCode: "component_repair_unknown_component",
		})
	}
	if maintenanceErr := h.refuseDuringServerMaintenance(e, tenantID, stackID, ""); maintenanceErr != nil {
		return maintenanceErr
	}
	jobID, err := h.orch.EnqueuePublicTLSRepair(e.Request.Context(), tenantID, ownerID, stackID)
	switch {
	case err == nil:
	case errors.Is(err, controlplane.ErrNotFound):
		return httpx.NotFound(e, "Stack not found")
	case errors.Is(err, orchestrator.ErrPublicTLSRepairNotNeeded):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Public TLS of this stack is not degraded", map[string]any{
			detailsKeyReasonCode: "component_repair_not_needed",
		})
	case errors.Is(err, orchestrator.ErrStackKitLifecycleUnavailable), errors.Is(err, orchestrator.ErrNoAssignedWorkers):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The stack's server is not reachable for the repair right now", map[string]any{
			detailsKeyReasonCode: "component_repair_unavailable", "retryable": true,
		})
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to start the component repair", map[string]any{
			detailsKeyReasonCode: "component_repair_internal",
		})
	}
	return httpx.Success(e, http.StatusAccepted, repairStackComponentResponse{
		JobID: jobID, StackID: stackID, Component: "public_tls", Status: "queued",
	})
}
