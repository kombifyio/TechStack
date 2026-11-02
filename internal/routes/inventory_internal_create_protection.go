package routes

import (
	"context"
	"errors"
	"net/http"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

var createProtectionSurface = cloudReadSurface{
	errorPrefix: "techstack.create_protection.", unavailableReason: "create_protection_unavailable",
	title: "VM creation protection is unavailable", body: "The account's authoritative provider create state could not be read.",
	queryKeys: []string{"provider_id", "region"},
}

func (h inventoryHandlers) httpInternalCreateProtection(e *httpx.Event) error {
	return h.serveCloudRead(e, createProtectionSurface, func(e *httpx.Event, ctx context.Context, scope inventoryScope) {
		if h.createProtection == nil {
			_ = createProtectionSurface.deny(e, http.StatusServiceUnavailable, "create_protection_unavailable", true, "Retry after provider control becomes available.")
			return
		}
		query := e.Request.URL.Query()
		protection, err := h.createProtection(ctx, scope.tenantID, query.Get("provider_id"), query.Get("region"))
		if err != nil {
			status, code := http.StatusServiceUnavailable, "create_protection_unavailable"
			if errors.Is(err, providercontrol.ErrInvalidRequest) {
				status, code = http.StatusBadRequest, "invalid_create_scope"
			}
			_ = createProtectionSurface.deny(e, status, code, status == http.StatusServiceUnavailable, "Select a provider and region, then refresh the protection state.")
			return
		}
		e.Response.Header().Set("Cache-Control", "private, no-store")
		_ = e.JSON(http.StatusOK, protection)
	})
}

// httpCreateProtection is the signed-in user's read of the same projection in
// the tenant the managed create admits under, so the creation wizard shows why
// creation is paused and when it recovers before the user submits.
func (h inventoryHandlers) httpCreateProtection(e *httpx.Event) error {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.create_protection.read")
	if tenantErr != nil {
		return tenantErr
	}
	unavailable := map[string]any{"reason_code": "create_protection_unavailable", "retryable": true}
	if h.createProtection == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, createProtectionSurface.title, unavailable)
	}
	query := e.Request.URL.Query()
	protection, err := h.createProtection(e.Request.Context(), tenantID, query.Get("provider_id"), query.Get("region"))
	if errors.Is(err, providercontrol.ErrInvalidRequest) {
		return httpx.BadRequest(e, "Select a provider and region, then refresh the protection state.",
			map[string]any{"reason_code": "invalid_create_scope"})
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, createProtectionSurface.title, unavailable)
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, protection)
}
