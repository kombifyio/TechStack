package routes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
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

// cloudReadSurface names one private Cloud servicecall read model and the
// InternalErrorEnvelope (techstack-internal-v1.yaml) its denials carry.
type cloudReadSurface struct {
	errorPrefix       string
	unavailableReason string
	title             string
	body              string
}

var runtimeSummarySurface = cloudReadSurface{
	errorPrefix: runtimeSummaryErrorPrefix, unavailableReason: "runtime_summary_unavailable",
	title: "Runtime summary unavailable", body: "Techstack could not resolve this runtime summary safely.",
}

type internalErrorEnvelope struct {
	ErrorCode        string              `json:"error_code"`
	ReasonCode       string              `json:"reason_code"`
	RequiredFeatures []string            `json:"required_features"`
	MissingFeatures  []string            `json:"missing_features"`
	Retryable        bool                `json:"retryable"`
	UserGuidance     denial.UserGuidance `json:"user_guidance"`
}

func (s cloudReadSurface) deny(e *httpx.Event, status int, reasonCode string, retryable bool, nextStep string) error {
	return e.JSON(status, internalErrorEnvelope{
		ErrorCode: s.errorPrefix + reasonCode, ReasonCode: reasonCode,
		RequiredFeatures: []string{}, MissingFeatures: []string{}, Retryable: retryable,
		UserGuidance: denial.UserGuidance{Title: s.title, Body: s.body, NextSteps: []string{nextStep}},
	})
}

// serveCloudRead is the Cloud servicecall boundary shared by the private read
// models. The service token is verified first; owner and tenant come only
// from the signed on_behalf_of identity, never from the request, and the
// stored membership authorization is resolved before serve runs. Inside the
// servicecall middleware the router never sees a returned error, so every
// branch writes its own response.
func (h inventoryHandlers) serveCloudRead(
	e *httpx.Event,
	surface cloudReadSurface,
	serve func(e *httpx.Event, ctx context.Context, scope inventoryScope),
) error {
	if h.serviceAuthSecret == "" {
		return surface.deny(e, http.StatusServiceUnavailable, surface.unavailableReason, true,
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
		caller := servicecall.FromContext(request.Context())
		if caller == nil || caller.Service != runtimeSummaryCallerCloud || caller.OnBehalfOf == nil ||
			strings.TrimSpace(caller.OnBehalfOf.Sub) == "" || strings.TrimSpace(caller.OnBehalfOf.OrgID) == "" {
			_ = surface.deny(e, http.StatusForbidden, "tenant_context_required", false,
				"Open Techstack from the authenticated Kombify Cloud organization and retry.")
			return
		}
		if request.URL.RawQuery != "" || request.ContentLength != 0 {
			_ = surface.deny(e, http.StatusForbidden, "principal_injection_denied", false,
				"Do not send principal or scope fields; Techstack derives them from the signed Cloud service identity.")
			return
		}
		scope := inventoryScope{
			tenantID: strings.TrimSpace(caller.OnBehalfOf.OrgID),
			ownerID:  strings.TrimSpace(caller.OnBehalfOf.Sub),
		}
		ctx := request.Context()
		if h.runtimeSummaryContext != nil {
			resolved, err := h.runtimeSummaryContext(ctx, scope.tenantID, scope.ownerID)
			if err != nil || resolved == nil {
				status, reason, retryable := http.StatusServiceUnavailable, "authorization_unavailable", true
				if errors.Is(err, ErrInventoryAccessDenied) {
					status, reason, retryable = http.StatusForbidden, "inventory_access_denied", false
				}
				_ = surface.deny(e, status, reason, retryable, "Open Techstack in the same account and organization to refresh its access context.")
				return
			}
			ctx = resolved
		}
		e.Response.Header().Set("Cache-Control", "private, no-cache")
		e.Response.Header().Set("Vary", "Authorization, X-Kombify-Service-Auth")
		serve(e, ctx, scope)
	})).ServeHTTP(e.Response, e.Request)
	return nil
}

func (h inventoryHandlers) httpInternalRuntimeSummary(e *httpx.Event) error {
	return h.serveCloudRead(e, runtimeSummarySurface, h.serveRuntimeSummary)
}

func (h inventoryHandlers) serveRuntimeSummary(e *httpx.Event, ctx context.Context, scope inventoryScope) {
	page := inventoryPageOptions{Limit: runtimeSummaryPageSize}
	now := h.app.now().UTC()

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
	return runtimeSummarySurface.deny(e, status, reasonCode, retryable, nextStep)
}
