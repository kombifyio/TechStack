package stacks

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

type putStackRoutingRequest struct {
	ServerID      string                  `json:"server_id"`
	LeaseID       string                  `json:"lease_id,omitempty"`
	Mode          string                  `json:"mode"`
	Domain        string                  `json:"domain"`
	Provenance    stackrouting.Provenance `json:"provenance"`
	EnsureRollout bool                    `json:"ensure_rollout"`
}

func (h crudRouteHandlers) getStackRouting(e *httpx.Event) error {
	principal, ok := routingPrincipal(e)
	if !ok {
		return nil
	}
	view, err := h.routingService().Get(e.Request.Context(), principal, strings.TrimSpace(e.Request.PathValue("id")))
	if err != nil {
		return writeRoutingError(e, err)
	}
	writeRoutingHeaders(e, view.Desired.Revision)
	return httpx.Success(e, http.StatusOK, view)
}

func (h crudRouteHandlers) listStackRoutingTargets(e *httpx.Event) error {
	principal, ok := routingPrincipal(e)
	if !ok {
		return nil
	}
	targets, err := h.routingService().ListTargets(e.Request.Context(), principal, strings.TrimSpace(e.Request.PathValue("id")))
	if err != nil {
		return writeRoutingError(e, err)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{"targets": targets})
}

func (h crudRouteHandlers) putStackRouting(e *httpx.Event) error {
	principal, ok := routingPrincipal(e)
	if !ok {
		return nil
	}
	var req putStackRoutingRequest
	if err := e.BindBody(&req); err != nil {
		return httpx.BadRequest(e, "Invalid routing request body")
	}
	expectedRevision, err := parseRoutingIfMatch(e.Request.Header.Get("If-Match"))
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, err.Error(), map[string]any{detailsKeyReasonCode: "invalid_if_match"})
	}
	view, err := h.routingService().Ensure(e.Request.Context(), principal, strings.TrimSpace(e.Request.PathValue("id")), stackrouting.EnsureInput{
		ServerID:      req.ServerID,
		LeaseID:       req.LeaseID,
		Mode:          req.Mode,
		Domain:        req.Domain,
		Provenance:    req.Provenance,
		EnsureRollout: req.EnsureRollout,
	}, stackrouting.MutationOptions{
		IdempotencyKey:   e.Request.Header.Get("Idempotency-Key"),
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return writeRoutingError(e, err)
	}
	writeRoutingHeaders(e, view.Desired.Revision)
	status := http.StatusOK
	if req.EnsureRollout && view.Rollout.Status == stackrouting.RolloutPending {
		status = http.StatusAccepted
	}
	return httpx.Success(e, status, view)
}

func (h crudRouteHandlers) routingService() stackrouting.Service {
	leases := h.routingLeases
	if leases == nil {
		leases = currentManagedRuntimeLeaseLister()
	}
	dispatcher := h.routingDispatch
	if dispatcher == nil && h.orch != nil {
		dispatcher = h.orch
	}
	return stackrouting.Service{Stacks: h.stackStore, Servers: h.serverStore, Store: h.routingStore, Leases: leases, Dispatcher: dispatcher}
}

func routingPrincipal(e *httpx.Event) (stackrouting.Principal, bool) {
	ownerID, authenticated := authenticatedStackUserID(e)
	if !authenticated {
		_ = httpx.Unauthorized(e, "")
		return stackrouting.Principal{}, false
	}
	tenantID := tenantIDFromRequest(e)
	if tenantID == "" {
		_ = httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Tenant identity is required for stack routing", map[string]any{detailsKeyReasonCode: "tenant_unresolved"})
		return stackrouting.Principal{}, false
	}
	return stackrouting.Principal{TenantID: tenantID, OwnerSubjectID: ownerID}, true
}

func parseRoutingIfMatch(value string) (*int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "W/") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "W/"))
	}
	value = strings.Trim(value, `"`)
	value = strings.TrimPrefix(value, "routing-")
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return nil, fmt.Errorf("If-Match must contain a non-negative routing revision")
	}
	return &revision, nil
}

func writeRoutingHeaders(e *httpx.Event, revision int64) {
	e.Response.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
	e.Response.Header().Set("Cache-Control", "no-store")
}

func writeRoutingError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, stackrouting.ErrNotFound):
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Stack routing was not found", map[string]any{detailsKeyReasonCode: "routing_not_found"})
	case errors.Is(err, stackrouting.ErrForbidden):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "Not your stack routing target", map[string]any{detailsKeyReasonCode: "routing_target_forbidden"})
	case errors.Is(err, stackrouting.ErrIdempotencyConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different routing request", map[string]any{detailsKeyReasonCode: "idempotency_conflict"})
	case errors.Is(err, stackrouting.ErrRevisionConflict):
		return httpx.Error(e, http.StatusPreconditionFailed, ksapi.ErrCodeConflict, "Routing revision does not match If-Match", map[string]any{detailsKeyReasonCode: "routing_revision_conflict"})
	case errors.Is(err, stackrouting.ErrRolloutInProgress):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A rollout for the current routing revision is still in progress", map[string]any{detailsKeyReasonCode: "routing_rollout_in_progress"})
	case errors.Is(err, stackrouting.ErrInvalid):
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation, err.Error(), map[string]any{detailsKeyReasonCode: "routing_validation_failed"})
	case errors.Is(err, stackrouting.ErrUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Stack routing is temporarily unavailable", map[string]any{detailsKeyReasonCode: "routing_unavailable"})
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update stack routing", nil)
	}
}
