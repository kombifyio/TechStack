package stacks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
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
	JobID           string `json:"job_id"`
	KitDeploymentID string `json:"kit_deployment_id"`
	AgentID         string `json:"agent_id"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
}

// stackLockGuardedOperations are the stack-scoped operations that would write
// to a service the owner has locked. A stack apply that silently overrode a
// service lock would make the lock a lie, so the whole operation is refused and
// the offending services are named. Read-only operations (plan, verify,
// drift_detect) stay available: an owner has to be able to inspect a stack that
// carries a lock.
var stackLockGuardedOperations = map[string]bool{
	jobs.StackKitLifecycleApply:          true,
	jobs.StackKitLifecycleDriftReconcile: true,
	jobs.StackKitLifecycleUpgrade:        true,
}

// lockedStackServices returns the ids of the stack's locked services for an
// operation that would mutate them. It fails closed: if the service projection
// cannot be read, the operation is refused rather than run past an unknown
// guardrail.
func (h crudRouteHandlers) lockedStackServices(
	e *httpx.Event,
	tenantID, stackID, operation string,
) ([]string, error) {
	if !stackLockGuardedOperations[operation] {
		return nil, nil
	}
	if h.serviceStore == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Service lock state is unavailable", nil)
	}
	services, err := h.serviceStore.ListServiceRuntimes(e.Request.Context(), tenantID, stackID, "")
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Failed to read service lock state", nil)
	}
	locked := make([]string, 0, len(services))
	for i := range services {
		if services[i].MutationLock.Locked() {
			locked = append(locked, services[i].ID)
		}
	}
	return locked, nil
}

func (h crudRouteHandlers) startStackKitLifecycle(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stackkit.lifecycle")
	if tenantErr != nil {
		return tenantErr
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
	if stackID == "" {
		return httpx.BadRequest(e, "Stack is required")
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
	if locked, lockErr := h.lockedStackServices(e, tenantID, stackID, normalized.Operation); lockErr != nil {
		return lockErr
	} else if len(locked) > 0 {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Stack contains locked services", map[string]interface{}{
				"reason": "service_locked", "locked_service_ids": locked,
			})
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
		JobID:           jobID,
		KitDeploymentID: stackID,
		AgentID:         normalized.AgentID,
		Operation:       normalized.Operation,
		Status:          "queued",
	})
}
