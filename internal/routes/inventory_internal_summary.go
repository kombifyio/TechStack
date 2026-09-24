package routes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	runtimeSummaryService     = "techstack"
	runtimeSummaryCallerCloud = "cloud"
	runtimeSummaryErrorPrefix = "techstack.runtime_summary."
	runtimeSummaryPageSize    = 50
)

// runtimeSummary is the private Cloud read model behind the dashboard
// Overview: the caller's servers and services in one round trip. It is a pure
// projection of the canonical inventory read model — same authorization
// policy, same sanitized shapes — so it can never expose more than the
// public inventory routes do.
type runtimeSummary struct {
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
	// "ok" means authorization succeeded, including an actually empty inventory.
	// Missing authorization is not evidence of an empty collection.
	ScopeState string               `json:"scope_state"`
	Servers    inventoryServerList  `json:"servers"`
	Services   inventoryServiceList `json:"services"`
}

// httpInternalRuntimeSummary is a Cloud servicecall boundary. The service
// token is verified first; owner and tenant come only from the signed
// on_behalf_of identity, never from the request.
func (h inventoryHandlers) httpInternalRuntimeSummary(e *httpx.Event) error {
	if h.serviceAuthSecret == "" {
		return writeRuntimeSummaryDenial(e, http.StatusServiceUnavailable, "runtime_summary_unavailable", true,
			"Retry after Techstack service authentication is configured.")
	}

	auth := servicecall.RequireServiceAuth(servicecall.Config{
		ServiceName:    runtimeSummaryService,
		Secret:         h.serviceAuthSecret,
		SecretNext:     h.serviceAuthNext,
		AllowedCallers: []string{runtimeSummaryCallerCloud},
		Enabled:        true,
	})
	auth(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		e.Request = request
		h.serveRuntimeSummary(e)
	})).ServeHTTP(e.Response, e.Request)
	return nil
}

func (h inventoryHandlers) serveRuntimeSummary(e *httpx.Event) {
	caller := servicecall.FromContext(e.Request.Context())
	if caller == nil || caller.Service != runtimeSummaryCallerCloud || caller.OnBehalfOf == nil ||
		strings.TrimSpace(caller.OnBehalfOf.Sub) == "" || strings.TrimSpace(caller.OnBehalfOf.OrgID) == "" {
		_ = writeRuntimeSummaryDenial(e, http.StatusForbidden, "tenant_context_required", false,
			"Open Techstack from the authenticated Kombify Cloud organization and retry.")
		return
	}
	if e.Request.URL.RawQuery != "" || e.Request.ContentLength != 0 {
		_ = writeRuntimeSummaryDenial(e, http.StatusForbidden, "principal_injection_denied", false,
			"Do not send principal or scope fields; Techstack derives them from the signed Cloud service identity.")
		return
	}

	scope := inventoryScope{
		tenantID: strings.TrimSpace(caller.OnBehalfOf.OrgID),
		ownerID:  strings.TrimSpace(caller.OnBehalfOf.Sub),
	}
	page := inventoryPageOptions{Limit: runtimeSummaryPageSize}
	ctx := e.Request.Context()
	if h.runtimeSummaryContext != nil {
		resolved, err := h.runtimeSummaryContext(ctx, scope.tenantID, scope.ownerID)
		if err != nil || resolved == nil {
			status, reason, retryable := http.StatusServiceUnavailable, "authorization_unavailable", true
			if errors.Is(err, ErrInventoryAccessDenied) {
				status, reason, retryable = http.StatusForbidden, "inventory_access_denied", false
			}
			_ = writeRuntimeSummaryDenial(e, status, reason, retryable, "Open Techstack in the same account and organization to refresh its access context.")
			return
		}
		ctx = resolved
	}

	now := h.app.now().UTC()
	e.Response.Header().Set("Cache-Control", "private, no-cache")
	e.Response.Header().Set("Vary", "Authorization, X-Kombify-Service-Auth")

	servers, err := h.app.listServers(ctx, scope, page)
	if err != nil {
		h.writeRuntimeSummaryError(e, err)
		return
	}
	services, err := h.app.listServices(ctx, scope, "", page)
	if err != nil {
		h.writeRuntimeSummaryError(e, err)
		return
	}

	_ = e.JSON(http.StatusOK, runtimeSummary{
		Version:     "1",
		GeneratedAt: now.Format(time.RFC3339Nano),
		ScopeState:  "ok",
		Servers:     servers,
		Services:    services,
	})
}

// writeRuntimeSummaryError always writes a response. Inside the servicecall
// middleware the router never sees a returned error, so the inventory denials
// that are normally rendered by the router (tenant guard, access denial) must
// be written here explicitly, in this surface's envelope. A denied collection
// must never be reported as a successful empty inventory.
func (h inventoryHandlers) writeRuntimeSummaryError(e *httpx.Event, err error) {
	var inventoryErr *inventoryError
	if errors.As(err, &inventoryErr) && inventoryErr.reasonCode == "inventory_access_denied" {
		_ = writeRuntimeSummaryDenial(e, http.StatusForbidden, "inventory_access_denied", false, "Refresh Techstack access for this account and organization before retrying.")
		return
	}
	if errors.As(err, &inventoryErr) && inventoryErr.reasonCode == "tenant_context_missing" {
		_ = writeRuntimeSummaryDenial(e, http.StatusForbidden, "tenant_context_required", false,
			"Open Techstack from the authenticated Kombify Cloud organization and retry.")
		return
	}
	// Every remaining branch of writeInventoryHTTPError writes through
	// httpx.Error itself.
	_ = writeInventoryHTTPError(e, err)
}

func writeRuntimeSummaryDenial(e *httpx.Event, status int, reasonCode string, retryable bool, nextStep string) error {
	return e.JSON(status, map[string]any{
		"error_code":        runtimeSummaryErrorPrefix + reasonCode,
		"reason_code":       reasonCode,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         retryable,
		"user_guidance": map[string]any{
			"title":      "Runtime summary unavailable",
			"body":       "Techstack could not resolve this runtime summary safely.",
			"next_steps": []string{nextStep},
		},
	})
}
