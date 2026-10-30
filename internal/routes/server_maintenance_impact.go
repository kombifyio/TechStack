package routes

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// The preview has the same owner and tenant boundary as maintenance itself.
// Failure to read inventory is never serialized as a verified empty impact.
func (h serverRuntimeHandlers) maintenanceImpact(e *httpx.Event) error {
	server, ownerID, err := h.ownedMaintenanceServer(e)
	if err != nil {
		return err
	}
	action := strings.TrimSpace(e.Request.URL.Query().Get("action"))
	if action != controlplane.ServerMaintenanceActionReboot && action != controlplane.ServerMaintenanceActionUpdate {
		return httpx.BadRequest(e, "Action must be os_update or reboot", nil)
	}
	store, ok := h.maintenance.jobs.(controlplane.MaintenanceImpactStore)
	if !ok {
		return writeServerMaintenanceDenial(e, http.StatusServiceUnavailable, maintenanceReasonImpactUnavailable, nil)
	}
	impact, err := store.GetServerMaintenanceImpact(e.Request.Context(), server.TenantID, server.ID, action, h.now().UTC())
	if err != nil {
		return writeServerMaintenanceDenial(e, http.StatusServiceUnavailable, maintenanceReasonImpactUnavailable, nil)
	}
	if impact == nil || impact.OwnerSubjectID != ownerID {
		return httpx.NotFound(e, "Server not found")
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, impact)
}

// ownedMaintenanceServer writes every refusal itself and then returns a
// non-nil error, so a caller that stops on the error never reads a nil server.
func (h serverRuntimeHandlers) ownedMaintenanceServer(e *httpx.Event) (*controlplane.ServerRuntime, string, error) {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return nil, "", httpx.RejectUnauthorized(e, "Authentication required")
	}
	tenantID, err := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, ServerMaintenanceEntitlement)
	if err != nil {
		return nil, "", err
	}
	if !h.maintenance.entitlements.SignedGrant(e.Request.Context(), ServerMaintenanceEntitlement) {
		return nil, "", writeServerMaintenanceDenial(e, http.StatusForbidden, maintenanceReasonEntitlement, nil)
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return nil, "", httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Server ID is required", nil)
	}
	server, err := h.store.GetServerRuntime(e.Request.Context(), tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && (server == nil || !serverRuntimeOwnedBy(*server, ownerID))) {
		return nil, "", httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Server not found", nil)
	}
	if err != nil {
		return nil, "", httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	return server, ownerID, nil
}

// Cancellation races with claim through the existing queued-state CAS. Once
// claim wins, cancellation must not claim that an already sent command stopped.
func (h serverRuntimeHandlers) cancelMaintenanceJob(e *httpx.Event) error {
	server, ownerID, err := h.ownedMaintenanceServer(e)
	if err != nil {
		return err
	}
	ctx := e.Request.Context()
	jobID := strings.TrimSpace(e.Request.PathValue("jobId"))
	if jobID == "" {
		return httpx.BadRequest(e, "Job ID is required", nil)
	}
	job, err := h.maintenance.jobs.GetServerMaintenanceJob(ctx, server.TenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && (job.ServerID != server.ID || job.OwnerSubjectID != ownerID)) {
		return httpx.NotFound(e, "Maintenance job not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance is unavailable", nil)
	}
	// The authority to withdraw a queued job is the person who owns the server
	// and requested the job, checked above with the entitlement; the person's
	// agent acts with it. Cancelling only withdraws work that has not reached
	// the server, so it needs no delegated maintenance origin, which guards
	// running maintenance (run_server_maintenance_action) only.
	if job.State == controlplane.ServerMaintenanceStateQueued {
		job, err = h.maintenance.jobs.UpdateServerMaintenanceJob(ctx, server.TenantID, jobID, controlplane.ServerMaintenanceUpdate{
			ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateCancelled,
			ReasonCode: "owner_cancelled", At: h.now().UTC(),
		})
		if errors.Is(err, controlplane.ErrConflict) {
			// A concurrent cancel is a successful replay; a claim is not.
			job, err = h.maintenance.jobs.GetServerMaintenanceJob(ctx, server.TenantID, jobID)
		}
		if err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance is unavailable", nil)
		}
	}
	if job.State != controlplane.ServerMaintenanceStateCancelled {
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonNotCancellable, map[string]any{"job_id": job.ID})
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, serverMaintenanceJobResponse(*job, *server))
}
