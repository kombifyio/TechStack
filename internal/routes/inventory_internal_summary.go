package routes

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/controlplane"
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
// projection of the canonical inventory read model with the same sanitized
// shapes. Authorization differs on purpose: a verified active membership
// reads the signed owner's own rows (ownerInventoryReadPolicy), without the
// agent inventory entitlement and FGA surface relation that /api/v1/inventory
// requires; it never reaches another owner's rows.
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
	// queryKeys are the only query parameters the surface accepts, each once.
	// They carry a read option (a range, a limit), never scope: a principal
	// field is rejected like any other unknown key. Empty means no query.
	queryKeys []string
}

// queryAllowed reports whether the request carries no query at all or only the
// surface's own read options, each exactly once.
func (s cloudReadSurface) queryAllowed(request *http.Request) bool {
	if request.URL.RawQuery == "" {
		return true
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, found := range values {
		if len(found) != 1 || !slices.Contains(s.queryKeys, key) {
			return false
		}
	}
	return true
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
		if !surface.queryAllowed(request) || request.ContentLength != 0 {
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
			resolved, tenantID, err := h.runtimeSummaryContext(ctx, scope.tenantID, scope.ownerID)
			if err != nil || resolved == nil || strings.TrimSpace(tenantID) == "" {
				status, reason, retryable := http.StatusServiceUnavailable, "authorization_unavailable", true
				if errors.Is(err, ErrInventoryAccessDenied) {
					status, reason, retryable = http.StatusForbidden, "inventory_access_denied", false
				}
				_ = surface.deny(e, status, reason, retryable, "Open Techstack in the same account and organization to refresh its access context.")
				return
			}
			ctx = resolved
			scope.tenantID = strings.TrimSpace(tenantID)
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

	// The Overview is the owner looking at their own homelab, an account
	// feature in every plan. Once the signed subject's active membership in
	// the tenant is verified, the read is that owner's own rows only: the
	// agent/MCP inventory entitlement (which no plan grants by itself) and its
	// FGA surface relation do not apply here.
	app := h.ownerReadApplication()

	servers, err := app.listServers(ctx, scope, page)
	if err != nil {
		h.writeRuntimeSummaryError(e, err)
		return
	}
	services, err := app.listServices(ctx, scope, "", page)
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

// ownerInventoryReadPolicy admits only collection reads, scoped to the
// requesting owner's own rows in the verified tenant. Anything else (an
// object read, operate, write, provision, admin) is denied.
//
// The private monitoring reads (history, events, ports, backups; see
// inventory_internal_monitoring.go) run under this same policy without
// widening it: each is a read of the owner's own server collection, so it
// needs no new target, action or capability, and nothing writable or
// object-level becomes reachable through them.
type ownerInventoryReadPolicy struct{}

func (ownerInventoryReadPolicy) AuthorizeInventory(_ context.Context, authorization InventoryAuthorization) (InventoryDecision, error) {
	if authorization.Action != InventoryActionRead {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	switch authorization.ResourceType {
	case controlplane.InventoryReadTargetServerCollection, controlplane.InventoryReadTargetServiceCollection:
	default:
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	readScope, err := controlplane.NewOwnerInventoryReadScope(authorization.TenantID, authorization.SubjectID)
	if err != nil {
		return InventoryDecision{}, ErrInventoryAccessDenied
	}
	return InventoryDecision{ReadScope: readScope}, nil
}

// writeRuntimeSummaryError always writes a response. Inside the servicecall
// middleware the router never sees a returned error, so the inventory denials
// that are normally rendered by the router (tenant guard, access denial) must
// be written here explicitly, in this surface's envelope. A denied collection
// must never be reported as a successful empty inventory.
func (h inventoryHandlers) writeRuntimeSummaryError(e *httpx.Event, err error) {
	h.writeCloudReadError(e, runtimeSummarySurface, err)
}

// writeCloudReadError is writeRuntimeSummaryError for any private Cloud read
// surface: the denials are written in that surface's envelope.
func (h inventoryHandlers) writeCloudReadError(e *httpx.Event, surface cloudReadSurface, err error) {
	var inventoryErr *inventoryError
	if errors.As(err, &inventoryErr) && inventoryErr.reasonCode == "inventory_access_denied" {
		_ = surface.deny(e, http.StatusForbidden, "inventory_access_denied", false, "Refresh Techstack access for this account and organization before retrying.")
		return
	}
	if errors.As(err, &inventoryErr) && inventoryErr.reasonCode == "tenant_context_missing" {
		_ = surface.deny(e, http.StatusForbidden, "tenant_context_required", false,
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
