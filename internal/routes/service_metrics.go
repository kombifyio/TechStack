package routes

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// Metrics use the existing admitted Guard telemetry and PromQL read backend.
// A service lookup supplies the server/container selectors; a caller cannot
// replace either selector or choose a different tenant through this endpoint.
func (h *serviceRuntimeHandlers) serviceMetrics(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, err := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.services.read")
	if err != nil {
		return err
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	service, err := h.loadServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service authorization is unavailable", nil)
	}
	if h.metrics == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service metrics are unavailable", nil)
	}
	if serviceregistry.NormalizePlacement(service.ServerID, service.Placement).TargetKind != serviceregistry.TargetKindServer {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service metrics require a measured server placement", nil)
	}
	identity := stringMapFromAny(service.Metadata["runtime_identity"])
	container := strings.TrimPrefix(firstNonEmptyString(stringFromAnyMap(service.Metadata, "container_name"), identity["container_name"]), "/")
	// Older discovery rows retained the measured Docker name in Name. A
	// declared application's display name never substitutes for that evidence.
	if container == "" && service.Source == serviceregistry.SourceObserved && stringFromAnyMap(service.Metadata, "platform_type") == "docker" {
		container = strings.TrimPrefix(service.Name, "/")
	}
	if container == "" || service.ObservedAt == nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service container metrics have no measured identity", nil)
	}
	query := `{__name__=~"container_(cpu_usage_percent|memory_usage_bytes|memory_limit_bytes|memory_usage_percent|running)",server_id=` + strconv.Quote(service.ServerID) + `,container_name=` + strconv.Quote(container) + `}`
	query, err = scopeMonitoringQuery(query, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Service metrics query is invalid", nil)
	}
	policy := defaultMonitoringQueryPolicy()
	ctx, cancel := policy.context(e.Request.Context())
	defer cancel()
	result, err := h.metrics.InstantQuery(ctx, query, h.now().UTC())
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service metrics are unavailable", nil)
	}
	return monitoringQuerySuccess(e, result)
}
