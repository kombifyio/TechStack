package routes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeinventory"
	"github.com/kombifyio/techstack/api/toolmanifest"
	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	inventoryCleanupStateAbsent     = "absent"
	inventoryCleanupStateFailed     = "failed"
	inventoryCleanupStateUnverified = "unverified"
	inventoryFresh                  = "fresh"
	inventoryReasonCodeField        = "reason_code"
	inventoryServerIDField          = "server_id"
	inventoryStale                  = "stale"
	inventoryUnavailable            = "unavailable"
)

type InventoryRouteConfig struct {
	ReadStore controlplane.InventoryReadStore
	Policy    InventoryPolicy
	Now       func() time.Time
	Version   string
}

type inventoryHandlers struct {
	app     *inventoryApplication
	version string
}

type inventoryApplication struct {
	read   controlplane.InventoryReadStore
	policy InventoryPolicy
	now    func() time.Time
}

type inventoryScope struct {
	tenantID string
	ownerID  string
}

type inventoryError struct {
	status     int
	reasonCode string
	message    string
	cause      error
	// sessionTenantDenied marks an authorization denial of the SESSION tenant
	// itself (collection-scope check, no resource id) as opposed to a
	// resource-level denial. The HTTP surface maps it to the retryable
	// session_reprojection_required signal; the MCP surface keeps the plain
	// 403 (machine callers re-mint tokens, not browser sessions).
	sessionTenantDenied bool
	sessionTenantID     string
}

func (e *inventoryError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}

type inventoryFreshness = runtimeinventory.Freshness

type inventoryAddresses = runtimeinventory.Addresses

type inventoryPlatform = runtimeinventory.Platform

type inventoryStackKit = runtimeinventory.StackKit

type inventoryObservedState = runtimeinventory.ObservedState

type inventoryLifecycleProjection = runtimeinventory.LifecycleProjection

type inventoryDesiredProjection = runtimeinventory.DesiredProjection

type inventoryLeaseProjection = runtimeinventory.LeaseProjection

type inventoryCleanupProjection = runtimeinventory.CleanupProjection

type inventoryServer = runtimeinventory.Server

type inventoryServerList = runtimeinventory.ServerList

type inventoryServerSummary struct {
	Count      int64     `json:"count"`
	HasServers bool      `json:"has_servers"`
	ObservedAt time.Time `json:"observed_at"`
}

type inventoryServerHealth = runtimeinventory.ServerHealth

type inventoryServiceLink = runtimeinventory.ServiceLink

// inventoryService is the neutral contract service projection plus the
// Techstack-owned management dimension.
//
// `runtimeinventory.Service` is generated/mirrored in the separate
// kombify-runtime-contracts-go repository and cannot carry `management_state`
// without a cross-repo release, so the canonical read model exposes it here at
// the response layer instead of losing the distinction. The embedded contract
// struct keeps the wire shape strictly additive: every existing field is
// emitted unchanged and `management_state` is appended.
// Follow-up: promote the field into the contract release
// (kombify-Techstack-nzy1.15 PR body + follow-up bead).
type inventoryService struct {
	runtimeinventory.Service
	ManagementState string `json:"management_state"`
}

// inventoryServiceList mirrors runtimeinventory.ServiceList field for field and
// only widens the element type to the projection above.
type inventoryServiceList struct {
	ObservedAt        time.Time          `json:"observed_at"`
	Freshness         inventoryFreshness `json:"freshness"`
	InventoryRevision int64              `json:"inventory_revision"`
	CollectionCursor  string             `json:"collection_cursor"`
	NextCursor        string             `json:"next_cursor,omitempty"`
	PageSize          int                `json:"page_size"`
	Services          []inventoryService `json:"services"`
}

type inventoryChannel = runtimeinventory.Channel

type inventoryAccessServiceLink = runtimeinventory.AccessServiceLink

type inventoryAvailability = runtimeinventory.Availability

type inventoryServerAccessContext = runtimeinventory.ServerAccessContext

func RegisterInventoryRoutes(r *httpx.Router, cfg InventoryRouteConfig) {
	if cfg.ReadStore == nil {
		panic("RegisterInventoryRoutes: authorization-scoped inventory read store required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Policy == nil {
		cfg.Policy = denyInventoryPolicy{}
	}
	h := inventoryHandlers{app: &inventoryApplication{read: cfg.ReadStore, policy: cfg.Policy, now: cfg.Now}, version: cfg.Version}
	r.GET("/api/v1/inventory/servers", h.httpListServers)
	// Public RIL is a compatibility view over the canonical inventory read
	// model. It must never enrich observations from live transport state.
	r.GET("/v1/ril/servers", h.httpListServers)
	r.GET("/api/v1/servers/summary", h.httpServerSummary)
	r.GET("/v1/ril/servers/summary", h.httpServerSummary)
	r.GET("/api/v1/inventory/servers/{serverId}/health", h.httpServerHealth)
	r.GET("/api/v1/inventory/services", h.httpListServices)
	r.GET("/api/v1/inventory/servers/{serverId}/access-context", h.httpServerAccessContext)
	r.GET("/api/v1/tool-manifest.json", serveToolManifest)
	registerInventoryMCPRoutes(r, h)
}

func (h inventoryHandlers) httpServerSummary(e *httpx.Event) error {
	if err := rejectInventorySummaryScopeOverrides(e); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	action := InventoryActionRead
	if e.Request.URL.Path == "/v1/ril/servers/summary" {
		action = InventoryActionRILRead
	}
	result, err := h.app.serverSummary(e.Request.Context(), scope, action)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	e.Response.Header().Set("Cache-Control", "private, max-age=60")
	return httpx.Success(e, http.StatusOK, result)
}

func serveToolManifest(e *httpx.Event) error {
	e.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
	e.Response.Header().Set("Cache-Control", "public, max-age=300")
	return e.Blob(http.StatusOK, "application/json; charset=utf-8", toolmanifest.Raw)
}

func (h inventoryHandlers) httpListServers(e *httpx.Event) error {
	if err := rejectInventoryScopeOverrides(e, map[string]bool{inventoryCursorField: true, inventoryLimitField: true}); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	page, err := inventoryPageFromQuery(e.Request.URL.Query())
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	result, err := h.app.listServers(e.Request.Context(), scope, page)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

func (h inventoryHandlers) httpServerHealth(e *httpx.Event) error {
	if err := rejectInventoryScopeOverrides(e, nil); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	result, err := h.app.serverHealth(e.Request.Context(), scope, e.Request.PathValue("serverId"))
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

func (h inventoryHandlers) httpListServices(e *httpx.Event) error {
	if err := rejectInventoryScopeOverrides(e, map[string]bool{inventoryServerIDField: true, inventoryCursorField: true, inventoryLimitField: true}); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	page, err := inventoryPageFromQuery(e.Request.URL.Query())
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	result, err := h.app.listServices(e.Request.Context(), scope, e.Request.URL.Query().Get(inventoryServerIDField), page)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

func (h inventoryHandlers) httpServerAccessContext(e *httpx.Event) error {
	if err := rejectInventoryScopeOverrides(e, nil); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	result, err := h.app.serverAccessContext(e.Request.Context(), scope, e.Request.PathValue("serverId"))
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

func inventoryScopeFromEvent(e *httpx.Event) (inventoryScope, error) {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return inventoryScope{}, &inventoryError{status: http.StatusUnauthorized, reasonCode: "authentication_required", message: "Authentication required"}
	}
	tenantID := requestTenantID(e, ownerID)
	if tenantguard.Active() {
		// SaaS: the owner-as-tenant fallback would silently scope the read to
		// an empty tenant (two-truths bug); only an explicit org scope counts.
		tenantID = requestExplicitTenantID(e)
	}
	if strings.TrimSpace(tenantID) == "" {
		return inventoryScope{}, &inventoryError{status: http.StatusForbidden, reasonCode: "tenant_context_missing", message: "Tenant context required"}
	}
	return inventoryScope{tenantID: strings.TrimSpace(tenantID), ownerID: strings.TrimSpace(ownerID)}, nil
}

func rejectInventoryScopeOverrides(e *httpx.Event, allowed map[string]bool) error {
	for key := range e.Request.URL.Query() {
		if allowed != nil && allowed[key] {
			continue
		}
		return &inventoryError{status: http.StatusBadRequest, reasonCode: "unsupported_query_parameter", message: "Unsupported inventory query parameter"}
	}
	return nil
}

func rejectInventorySummaryScopeOverrides(e *httpx.Event) error {
	for key, values := range e.Request.URL.Query() {
		if key != "tenant" || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return &inventoryError{status: http.StatusBadRequest, reasonCode: "unsupported_query_parameter", message: "Unsupported inventory query parameter"}
		}
		if values[0] != requestExplicitTenantID(e) {
			return &inventoryError{status: http.StatusForbidden, reasonCode: "tenant_scope_mismatch", message: "Tenant scope does not match authenticated context"}
		}
	}
	return nil
}

func writeInventoryHTTPError(e *httpx.Event, err error) error {
	var inventoryErr *inventoryError
	if !errors.As(err, &inventoryErr) {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Inventory unavailable", map[string]any{inventoryReasonCodeField: "inventory_unavailable"})
	}
	if inventoryErr.reasonCode == "tenant_context_missing" {
		// Full FEATURE-ENTITLEMENT-UX envelope; keeps reason_code stable.
		return tenantguard.Denial("techstack.inventory.read")
	}
	if inventoryErr.sessionTenantDenied {
		// The session tenant itself was denied (e.g. bound to a since-removed
		// tenant): answer with the retryable re-auth signal instead of a
		// dead-end 403 so the client's central interceptor re-enters SSO.
		return sessionreauth.Denial(e, inventoryErr.sessionTenantID, inventoryErr.cause)
	}
	details := map[string]any{inventoryReasonCodeField: inventoryErr.reasonCode}
	switch inventoryErr.status {
	case http.StatusUnauthorized:
		return httpx.Error(e, inventoryErr.status, ksapi.ErrCodeUnauthorized, inventoryErr.message, details)
	case http.StatusForbidden:
		return httpx.Error(e, inventoryErr.status, ksapi.ErrCodeForbidden, inventoryErr.message, details)
	case http.StatusNotFound:
		return httpx.Error(e, inventoryErr.status, ksapi.ErrCodeNotFound, inventoryErr.message, details)
	case http.StatusBadRequest:
		return httpx.Error(e, inventoryErr.status, ksapi.ErrCodeBadRequest, inventoryErr.message, details)
	case http.StatusServiceUnavailable:
		return httpx.Error(e, inventoryErr.status, ksapi.ErrCodeInternal, "Inventory unavailable", details)
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Inventory unavailable", details)
	}
}

func (a *inventoryApplication) listServers(ctx context.Context, scope inventoryScope, options inventoryPageOptions) (inventoryServerList, error) {
	readScope, err := a.authorize(ctx, scope, InventoryActionRead, controlplane.InventoryReadTargetServerCollection, "")
	if err != nil {
		return inventoryServerList{}, err
	}
	cursorScope := inventoryCursorScopeKey(readScope, "")
	request, err := inventoryPageRequest("servers", cursorScope, options)
	if err != nil {
		return inventoryServerList{}, err
	}
	page, err := a.read.ListInventoryServers(ctx, readScope, request)
	if err != nil {
		return inventoryServerList{}, inventoryStoreError(err)
	}
	now := a.now().UTC()
	servers := make([]inventoryServer, 0, len(page.Servers))
	for _, row := range page.Servers {
		rowScope, scopeErr := readScope.RestrictToServer(row.ID)
		if scopeErr != nil {
			return inventoryServerList{}, inventoryStoreError(scopeErr)
		}
		var stack *controlplane.Stack
		if strings.TrimSpace(row.StackID) != "" {
			stack, err = a.read.GetInventoryStack(ctx, rowScope, row.StackID)
			if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
				return inventoryServerList{}, inventoryStoreError(err)
			}
		}
		servers = append(servers, a.projectServer(row, stack, now))
	}
	freshness, revision := aggregateServerFreshness(servers)
	return inventoryServerList{
		ObservedAt: now, Freshness: freshness, InventoryRevision: revision,
		CollectionCursor: encodeInventoryCursor("servers", cursorScope, page.Watermark, nil),
		NextCursor:       encodeInventoryNextCursor("servers", cursorScope, page.Watermark, page.Next), PageSize: len(servers), Servers: servers,
	}, nil
}

func (a *inventoryApplication) serverSummary(ctx context.Context, scope inventoryScope, action InventoryAction) (inventoryServerSummary, error) {
	readScope, err := a.authorize(ctx, scope, action, controlplane.InventoryReadTargetServerCollection, "")
	if err != nil {
		return inventoryServerSummary{}, err
	}
	counter, ok := a.read.(controlplane.InventoryServerCounter)
	if !ok {
		return inventoryServerSummary{}, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "inventory_summary_unavailable", message: "Inventory summary unavailable"}
	}
	count, err := counter.CountInventoryServers(ctx, readScope)
	if err != nil {
		return inventoryServerSummary{}, inventoryStoreError(err)
	}
	return inventoryServerSummary{Count: count, HasServers: count > 0, ObservedAt: a.now().UTC()}, nil
}

func (a *inventoryApplication) serverHealth(ctx context.Context, scope inventoryScope, serverID string) (inventoryServerHealth, error) {
	serverID = strings.TrimSpace(serverID)
	readScope, err := a.authorize(ctx, scope, InventoryActionRead, controlplane.InventoryReadTargetServer, serverID)
	if err != nil {
		return inventoryServerHealth{}, err
	}
	server, err := a.authorizedServer(ctx, readScope, serverID)
	if err != nil {
		return inventoryServerHealth{}, err
	}
	projected := a.projectServer(*server, nil, a.now().UTC())
	return inventoryServerHealth{ServerID: projected.ID, ObservedAt: projected.ObservedAt, Freshness: projected.Freshness, InventoryRevision: projected.InventoryRevision, Connection: projected.Connection, Health: projected.Health}, nil
}

func (a *inventoryApplication) listServices(ctx context.Context, scope inventoryScope, serverID string, options inventoryPageOptions) (inventoryServiceList, error) {
	serverID = strings.TrimSpace(serverID)
	resourceType, resourceID := controlplane.InventoryReadTargetServiceCollection, ""
	if serverID != "" {
		resourceType, resourceID = controlplane.InventoryReadTargetServer, serverID
	}
	readScope, err := a.authorize(ctx, scope, InventoryActionRead, resourceType, resourceID)
	if err != nil {
		return inventoryServiceList{}, err
	}
	if serverID != "" {
		if _, serverErr := a.authorizedServer(ctx, readScope, serverID); serverErr != nil {
			return inventoryServiceList{}, serverErr
		}
	}
	cursorScope := inventoryCursorScopeKey(readScope, serverID)
	request, err := inventoryPageRequest("services", cursorScope, options)
	if err != nil {
		return inventoryServiceList{}, err
	}
	page, err := a.read.ListInventoryServices(ctx, readScope, serverID, request)
	if err != nil {
		return inventoryServiceList{}, inventoryStoreError(err)
	}
	now := a.now().UTC()
	services := make([]inventoryService, 0, len(page.Services))
	for _, row := range page.Services {
		rowScope, scopeErr := readScope.RestrictToServer(row.ServerID)
		if scopeErr != nil {
			return inventoryServiceList{}, inventoryStoreError(scopeErr)
		}
		server, serverErr := a.read.GetInventoryServer(ctx, rowScope, row.ServerID)
		if errors.Is(serverErr, controlplane.ErrNotFound) {
			continue
		}
		if serverErr != nil {
			return inventoryServiceList{}, inventoryStoreError(serverErr)
		}
		services = append(services, a.projectService(row, server, now))
	}
	freshness, revision := aggregateServiceFreshness(services)
	return inventoryServiceList{
		ObservedAt: now, Freshness: freshness, InventoryRevision: revision,
		CollectionCursor: encodeInventoryCursor("services", cursorScope, page.Watermark, nil),
		NextCursor:       encodeInventoryNextCursor("services", cursorScope, page.Watermark, page.Next), PageSize: len(services), Services: services,
	}, nil
}

func (a *inventoryApplication) serverAccessContext(ctx context.Context, scope inventoryScope, serverID string) (inventoryServerAccessContext, error) {
	serverID = strings.TrimSpace(serverID)
	readScope, err := a.authorize(ctx, scope, InventoryActionOperate, controlplane.InventoryReadTargetServer, serverID)
	if err != nil {
		return inventoryServerAccessContext{}, err
	}
	server, err := a.authorizedServer(ctx, readScope, serverID)
	if err != nil {
		return inventoryServerAccessContext{}, err
	}
	now := a.now().UTC()
	projected := a.projectServer(*server, nil, now)
	services, err := a.allServicesForServer(ctx, readScope, server.ID, now)
	if err != nil {
		return inventoryServerAccessContext{}, err
	}
	links := make([]inventoryAccessServiceLink, 0)
	for _, service := range services {
		for _, link := range service.Links {
			links = append(links, inventoryAccessServiceLink{ServiceID: service.ID, ServiceName: service.Name, URL: link.URL, Mode: link.Mode, Health: service.Health.State})
		}
	}
	channels := sanitizedInventoryChannels(server.Channels)
	availability := inventoryAvailability{State: "available"}
	if projected.Freshness.State != inventoryFresh {
		availability = inventoryAvailability{State: inventoryUnavailable, ReasonCode: projected.Freshness.ReasonCode}
	} else if projected.Connection.State != string(serverregistry.ConnectionConnected) && projected.Connection.State != string(serverregistry.ConnectionDegraded) {
		availability = inventoryAvailability{State: inventoryUnavailable, ReasonCode: "server_connection_" + safeReasonToken(projected.Connection.State)}
	} else if !hasInventoryAddress(projected.Addresses) && len(links) == 0 {
		availability = inventoryAvailability{State: inventoryUnavailable, ReasonCode: "no_registered_access"}
	}
	return inventoryServerAccessContext{ServerID: projected.ID, ObservedAt: projected.ObservedAt, Freshness: projected.Freshness, InventoryRevision: projected.InventoryRevision, Availability: availability, Connection: projected.Connection, Addresses: projected.Addresses, Channels: channels, ServiceLinks: links}, nil
}

func (a *inventoryApplication) allServicesForServer(ctx context.Context, readScope controlplane.InventoryReadScope, serverID string, now time.Time) ([]inventoryService, error) {
	request := controlplane.InventoryPageRequest{Limit: controlplane.MaxInventoryPageSize}
	result := make([]inventoryService, 0)
	for {
		page, err := a.read.ListInventoryServices(ctx, readScope, serverID, request)
		if err != nil {
			return nil, inventoryStoreError(err)
		}
		for _, row := range page.Services {
			rowScope, scopeErr := readScope.RestrictToServer(row.ServerID)
			if scopeErr != nil {
				return nil, inventoryStoreError(scopeErr)
			}
			server, serverErr := a.read.GetInventoryServer(ctx, rowScope, row.ServerID)
			if serverErr != nil {
				return nil, inventoryStoreError(serverErr)
			}
			result = append(result, a.projectService(row, server, now))
		}
		if page.Next == nil {
			return result, nil
		}
		request.Watermark, request.After = page.Watermark, *page.Next
	}
}

func (a *inventoryApplication) authorizedServer(ctx context.Context, readScope controlplane.InventoryReadScope, serverID string) (*controlplane.ServerRuntime, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, inventoryValidationError("server_id_required", "Server ID is required")
	}
	server, err := a.read.GetInventoryServer(ctx, readScope, serverID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, inventoryNotFound("server_not_found", "Server not found")
	}
	if err != nil {
		return nil, inventoryStoreError(err)
	}
	return server, nil
}

func (a *inventoryApplication) authorize(ctx context.Context, scope inventoryScope, action InventoryAction, resourceType, resourceID string) (controlplane.InventoryReadScope, error) {
	resourceID = strings.TrimSpace(resourceID)
	decision, err := a.policy.AuthorizeInventory(ctx, InventoryAuthorization{
		TenantID: scope.tenantID, SubjectID: scope.ownerID,
		ResourceType: resourceType, ResourceID: resourceID, Action: action,
	})
	principalScopeMatches := decision.ReadScope.TenantID() == strings.TrimSpace(scope.tenantID) &&
		(!decision.ReadScope.IsOwnerScoped() || decision.ReadScope.OwnerSubjectID() == strings.TrimSpace(scope.ownerID))
	if err == nil && principalScopeMatches && decision.ReadScope.AuthorizesTarget(resourceType, resourceID) {
		return decision.ReadScope, nil
	}
	if err == nil {
		err = errInventoryPolicyUnavailable
	}
	if errors.Is(err, ErrInventoryAccessDenied) {
		return controlplane.InventoryReadScope{}, &inventoryError{
			status: http.StatusForbidden, reasonCode: "inventory_access_denied", message: "Inventory access denied",
			// No resource id means the check denied the session tenant's own
			// collection scope - the recovery class, not a resource denial.
			sessionTenantDenied: resourceID == "",
			sessionTenantID:     scope.tenantID,
			cause:               err,
		}
	}
	return controlplane.InventoryReadScope{}, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "inventory_policy_unavailable", message: "Inventory policy unavailable", cause: err}
}

func (a *inventoryApplication) projectServer(server controlplane.ServerRuntime, stack *controlplane.Stack, now time.Time) inventoryServer {
	base := (serverRuntimeHandlers{now: func() time.Time { return now }}).response(server)
	freshness := inventoryFreshnessAt(now, server.LastHeartbeatAt)
	connectionReason := safeReasonToken(server.ConnectionReasonCode)
	if connectionReason == "" && server.SourceAuthority == controlplane.ServerEventAuthorityGuard {
		connectionReason = safeReasonToken(server.ReasonCode)
	}
	if freshness.State != inventoryFresh {
		connectionReason = freshness.ReasonCode
	}
	healthReason := safeReasonToken(server.HealthReasonCode)
	if healthReason == "" && server.SourceAuthority == controlplane.ServerEventAuthorityGuard {
		healthReason = safeReasonToken(server.ReasonCode)
	}
	if base.Health.State == string(serverregistry.HealthUnknown) {
		healthReason = firstNonEmptyString(freshness.ReasonCode, connectionReason, "health_unknown")
	}
	metadata := safeAnyMap(server.Metadata)
	host := safeAnyMap(metadata["host"])
	deployment := safeAnyMap(metadata["deployment"])
	addresses := inventoryAddressesFromMetadata(metadata, host)
	stackKit := inventoryStackKit{
		Name:    safeLabel(firstNonEmptyString(stringFromAnyMap(metadata, "stackkit"), stringFromAnyMap(deployment, "stackkit"), stackString(stack, "stackkit_catalog_ref"), stackString(stack, "stackkit_foundation"), stackString(stack, "stackkit")), 96),
		Version: safeLabel(firstNonEmptyString(stringFromAnyMap(metadata, "stackkit_version"), stringFromAnyMap(deployment, "stackkit_version")), 96),
		Variant: safeLabel(firstNonEmptyString(stringFromAnyMap(metadata, "stackkit_mode"), stringFromAnyMap(deployment, "stackkit_mode")), 64),
	}
	lifecycleReason := safeReasonToken(server.LifecycleReasonCode)
	if lifecycleReason == "" && server.SourceAuthority == controlplane.ServerEventAuthorityControlPlane {
		lifecycleReason = safeReasonToken(server.ReasonCode)
	}
	lifecycle := inventoryLifecycleProjection{
		State: safeState(server.LifecycleState, monitoringStatusUnknown), ReasonCode: lifecycleReason,
		ChangedAt: inventoryDimensionChangedAt(server.LifecycleChangedAt),
	}
	if lifecycle.ChangedAt == nil && server.DecommissionedAt != nil && lifecycle.State == string(serverregistry.LifecycleDecommissioned) {
		changedAt := server.DecommissionedAt.UTC()
		lifecycle.ChangedAt = &changedAt
	}
	desiredReason := safeReasonToken(server.DesiredReasonCode)
	if desiredReason == "" && server.SourceAuthority == controlplane.ServerEventAuthorityControlPlane {
		desiredReason = safeReasonToken(server.ReasonCode)
	}
	desired := inventoryDesiredProjection{
		State: canonicalInventoryDesiredState(server.DesiredState), ReasonCode: desiredReason,
		ChangedAt: inventoryDimensionChangedAt(server.DesiredChangedAt),
	}
	lease := inventoryLeaseProjection{State: string(vmleases.LeaseAuthorityStateUnbound)}
	if strings.TrimSpace(server.LeaseID) != "" {
		lease.ID = safeLabel(server.LeaseID, 256)
		lease.State = safeState(stringFromAnyMap(metadata, "lease_state"), "bound")
	}
	cleanup := inventoryCleanupFromServer(server, metadata)
	return inventoryServer{
		ID: safeLabel(server.ID, 256), StackID: safeLabel(server.StackID, 256), Name: safeLabel(firstNonEmptyString(server.Name, server.ID), 256),
		ObservedAt: server.LastHeartbeatAt, Freshness: freshness, InventoryRevision: maxInt64(server.InventoryRevision, 0), Addresses: addresses,
		Platform: inventoryPlatform{OS: safeLabel(firstNonEmptyString(stringFromAnyMap(host, "os"), stringFromAnyMap(metadata, "os")), 96), OSVersion: safeLabel(firstNonEmptyString(stringFromAnyMap(host, "os_version"), stringFromAnyMap(metadata, "os_version")), 128), Arch: safeLabel(firstNonEmptyString(stringFromAnyMap(host, "arch"), stringFromAnyMap(metadata, "arch")), 64)},
		StackKit: stackKit, Provider: safeProviderLabel(firstNonEmptyString(stringFromAnyMap(metadata, "provider_id"), stringFromAnyMap(metadata, "provider"))),
		Lifecycle: lifecycle, Desired: desired, Lease: lease, Cleanup: cleanup,
		Connection: inventoryObservedState{State: safeState(base.Connection.State, monitoringStatusUnknown), ReasonCode: connectionReason, ChangedAt: inventoryDimensionChangedAt(server.ConnectionChangedAt), ObservedAt: server.LastHeartbeatAt},
		Health:     inventoryObservedState{State: safeState(base.Health.State, monitoringStatusUnknown), ReasonCode: healthReason, ChangedAt: inventoryDimensionChangedAt(server.HealthChangedAt), ObservedAt: server.LastHeartbeatAt},
	}
}

func inventoryDimensionChangedAt(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	changedAt := value.UTC()
	return &changedAt
}

func canonicalInventoryDesiredState(value string) string {
	switch safeState(value, monitoringStatusUnknown) {
	case string(serverregistry.LifecycleActive), string(serverregistry.DesiredRunning):
		return string(serverregistry.DesiredRunning)
	case string(serverregistry.DesiredStopped):
		return string(serverregistry.DesiredStopped)
	case string(serverregistry.DesiredAbsent), string(serverregistry.LifecycleDecommissioned):
		return string(serverregistry.DesiredAbsent)
	default:
		return monitoringStatusUnknown
	}
}

func inventoryCleanupFromServer(server controlplane.ServerRuntime, metadata map[string]any) inventoryCleanupProjection {
	state := safeState(stringFromAnyMap(metadata, "cleanup_state"), "not_requested")
	verified, _ := metadata["provider_absence_verified"].(bool)
	cleanupComplete, _ := metadata["cleanup_complete"].(bool)
	verifiedAt := inventoryMetadataTime(metadata, "provider_absence_verified_at")
	switch state {
	case "delete_accepted", "absence_pending", "cleanup_required", inventoryCleanupStateFailed, inventoryCleanupStateUnverified:
		verified = false
	case inventoryCleanupStateAbsent:
		// A lifecycle tombstone is not provider absence evidence. Terminal
		// absence is exposed only when the provider-control projection carries
		// both definitive evidence time and complete cleanup-graph custody.
		if !verified || !cleanupComplete || verifiedAt == nil {
			state = inventoryCleanupStateUnverified
			verified = false
		}
	case "not_requested":
		if server.DecommissionedAt != nil || safeState(server.LifecycleState, "") == "decommissioned" {
			state = inventoryCleanupStateUnverified
		}
	default:
		state, verified = inventoryCleanupStateUnverified, false
	}
	result := inventoryCleanupProjection{State: state, ProviderAbsenceVerified: verified}
	if state == inventoryCleanupStateAbsent && verified {
		result.VerifiedAt = verifiedAt
	}
	return result
}

func inventoryMetadataTime(metadata map[string]any, key string) *time.Time {
	raw := strings.TrimSpace(stringFromAnyMap(metadata, key))
	if raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || parsed.IsZero() {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func (a *inventoryApplication) projectService(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime, now time.Time) inventoryService {
	base := (serviceRuntimeHandlers{now: func() time.Time { return now }}).response(service, server)
	freshness := inventoryFreshnessAt(now, service.ObservedAt)
	if strings.HasPrefix(base.Health.ReasonCode, "server_connection_") {
		freshness.State, freshness.ReasonCode = inventoryUnavailable, safeReasonToken(base.Health.ReasonCode)
	}
	// Access links are an operate surface, not a read model: they stay
	// fail-closed on stale service evidence even though the persisted service
	// state itself is returned honestly (the inventory Freshness projection
	// labels the evidence age explicitly).
	links := make([]inventoryServiceLink, 0, 1)
	if freshness.State == inventoryFresh {
		if rawURL := stringFromAnyMap(base.Access, serviceAccessURLKey); rawURL != "" {
			if sanitized, ok := sanitizedInventoryServiceURL(rawURL); ok {
				links = append(links, inventoryServiceLink{URL: sanitized, Mode: safeState(stringFromAnyMap(base.Access, serviceAccessModeKey), serviceAccessUnavailable)})
			}
		}
	}
	revision := int64(0)
	if server != nil {
		revision = maxInt64(server.InventoryRevision, 0)
	}
	reason := safeReasonToken(base.Health.ReasonCode)
	if reason == "" && base.Health.State == monitoringStatusUnknown {
		reason = firstNonEmptyString(freshness.ReasonCode, "health_unknown")
	}
	return inventoryService{
		Service: runtimeinventory.Service{
			ID: safeLabel(service.ID, 256), StackID: safeLabel(service.StackID, 256), ServerID: safeLabel(service.ServerID, 256), Key: safeLabel(service.ServiceKey, 128), Name: safeLabel(firstNonEmptyString(service.Name, service.ServiceKey, service.ID), 256),
			DesiredState:  safeState(base.DesiredState, monitoringStatusUnknown),
			ObservedState: safeState(base.ObservedState, monitoringStatusUnknown), ObservedAt: service.ObservedAt, Freshness: freshness, InventoryRevision: revision,
			Health:   inventoryObservedState{State: safeState(base.Health.State, monitoringStatusUnknown), ReasonCode: reason, ObservedAt: service.ObservedAt},
			StackKit: inventoryStackKit{Version: safeLabel(service.StackKitVersion, 96)}, Links: links,
			AllowedActions: append([]string(nil), base.AllowedActions...), EvidenceRef: safeEvidenceRef(base.EvidenceRef), Source: safeLabel(service.Source, 96),
		},
		// Read straight from the persisted aggregate dimension. Nothing here
		// re-derives ownership from source, status, or service type.
		ManagementState: base.ManagementState,
	}
}

func safeEvidenceRef(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 || (!strings.HasPrefix(value, "stackkit-evidence://") && value != "") {
		return ""
	}
	return value
}

func sanitizedInventoryServiceURL(raw string) (string, bool) {
	parsed, ok := parseServiceURL(raw)
	if !ok {
		return "", false
	}
	// Access URLs are read projections, not bearer-token transport. Query and
	// fragment material can contain signed tokens or one-time secrets.
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), true
}

func inventoryFreshnessAt(now time.Time, observedAt *time.Time) inventoryFreshness {
	staleAfter := int64(runtimehealth.FreshHeartbeatWindow.Seconds())
	if observedAt == nil {
		return inventoryFreshness{State: inventoryUnavailable, StaleAfterSeconds: staleAfter, ReasonCode: "observation_unavailable"}
	}
	age := now.Sub(observedAt.UTC())
	if age < -30*time.Second {
		return inventoryFreshness{State: inventoryUnavailable, StaleAfterSeconds: staleAfter, ReasonCode: "observation_time_invalid"}
	}
	if age < 0 {
		age = 0
	}
	ageSeconds := int64(age.Seconds())
	if age > runtimehealth.FreshHeartbeatWindow {
		return inventoryFreshness{State: inventoryStale, AgeSeconds: &ageSeconds, StaleAfterSeconds: staleAfter, ReasonCode: "observation_stale"}
	}
	return inventoryFreshness{State: inventoryFresh, AgeSeconds: &ageSeconds, StaleAfterSeconds: staleAfter}
}

func aggregateServerFreshness(items []inventoryServer) (inventoryFreshness, int64) {
	states := make([]inventoryFreshness, 0, len(items))
	revision := int64(0)
	for _, item := range items {
		states = append(states, item.Freshness)
		revision = maxInt64(revision, item.InventoryRevision)
	}
	return aggregateFreshness(states), revision
}

func aggregateServiceFreshness(items []inventoryService) (inventoryFreshness, int64) {
	states := make([]inventoryFreshness, 0, len(items))
	revision := int64(0)
	for _, item := range items {
		states = append(states, item.Freshness)
		revision = maxInt64(revision, item.InventoryRevision)
	}
	return aggregateFreshness(states), revision
}

func aggregateFreshness(states []inventoryFreshness) inventoryFreshness {
	staleAfter := int64(runtimehealth.FreshHeartbeatWindow.Seconds())
	if len(states) == 0 {
		return inventoryFreshness{State: inventoryUnavailable, StaleAfterSeconds: staleAfter, ReasonCode: "no_inventory"}
	}
	result := inventoryFreshness{State: inventoryFresh, StaleAfterSeconds: staleAfter}
	for _, state := range states {
		if state.State == inventoryUnavailable {
			return inventoryFreshness{State: inventoryUnavailable, StaleAfterSeconds: staleAfter, ReasonCode: "inventory_observation_unavailable"}
		}
		if state.State == inventoryStale {
			result.State, result.ReasonCode = inventoryStale, "inventory_observation_stale"
		}
	}
	return result
}

func inventoryAddressesFromMetadata(metadata, host map[string]any) inventoryAddresses {
	result := inventoryAddresses{PublicIPs: []string{}, PrivateIPs: []string{}, LocalIPs: []string{}, Domains: []string{}}
	result.PublicIPs = appendSafeIP(result.PublicIPs, firstNonEmptyString(stringFromAnyMap(host, "public_ip"), stringFromAnyMap(metadata, "public_ip")), false)
	result.PrivateIPs = appendSafeIP(result.PrivateIPs, firstNonEmptyString(stringFromAnyMap(host, "private_ip"), stringFromAnyMap(metadata, "private_ip")), true)
	result.LocalIPs = appendSafeIP(result.LocalIPs, firstNonEmptyString(stringFromAnyMap(host, "local_ip"), stringFromAnyMap(metadata, "local_ip")), true)
	for _, raw := range []string{stringFromAnyMap(metadata, "domain"), stringFromAnyMap(host, "domain")} {
		result.Domains = appendSafeDomain(result.Domains, raw)
	}
	for _, endpoint := range safeMapSlice(metadata["endpoints"]) {
		result.Domains = appendSafeDomain(result.Domains, firstNonEmptyString(stringFromAnyMap(endpoint, "url"), stringFromAnyMap(endpoint, "address")))
	}
	sort.Strings(result.PublicIPs)
	sort.Strings(result.PrivateIPs)
	sort.Strings(result.LocalIPs)
	sort.Strings(result.Domains)
	return result
}

func appendSafeIP(values []string, raw string, allowPrivate bool) []string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || (!allowPrivate && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())) {
		return values
	}
	value := ip.String()
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendSafeDomain(values []string, raw string) []string {
	value := normalizeInventoryDomain(raw)
	if !validInventoryDomain(value) {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalizeInventoryDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		raw = parsed.Hostname()
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
}

func validInventoryDomain(value string) bool {
	if net.ParseIP(value) != nil || len(value) == 0 || len(value) > 253 || strings.ContainsAny(value, "@/\\ ") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validInventoryDomainLabel(label) {
			return false
		}
	}
	return true
}

func validInventoryDomainLabel(label string) bool {
	if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func sanitizedInventoryChannels(channels []controlplane.ServerChannel) []inventoryChannel {
	result := make([]inventoryChannel, 0, len(channels))
	for _, channel := range channels {
		kind := safeLabel(channel.Type, 64)
		if kind == "" {
			continue
		}
		result = append(result, inventoryChannel{Type: kind, Role: safeLabel(channel.Role, 32), State: safeState(channel.State, monitoringStatusUnknown), ObservedAt: channel.ObservedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type+result[i].Role < result[j].Type+result[j].Role })
	return result
}

func hasInventoryAddress(addresses inventoryAddresses) bool {
	return len(addresses.PublicIPs)+len(addresses.PrivateIPs)+len(addresses.LocalIPs)+len(addresses.Domains) > 0
}

func safeAnyMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if result, ok := value.(map[string]any); ok {
		return result
	}
	if result, ok := mapFromJSONAny(value); ok {
		return result
	}
	return map[string]any{}
}

func safeMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row := safeAnyMap(item); len(row) > 0 {
				result = append(result, row)
			}
		}
		return result
	default:
		return nil
	}
}

func stackString(stack *controlplane.Stack, key string) string {
	if stack == nil {
		return ""
	}
	return firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, key), stringFromAnyMap(stack.Config, key))
}

func safeProviderLabel(value string) string {
	value = strings.ToLower(safeLabel(value, 64))
	if strings.Contains(value, "secret") || strings.Contains(value, "token") || strings.Contains(value, "credential") {
		return ""
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._:-", r) {
			return ""
		}
	}
	return value
}

func safeReasonToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 96 {
		value = value[:96]
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return ""
		}
	}
	return value
}

func safeState(value, fallback string) string {
	if value = safeReasonToken(value); value != "" {
		return value
	}
	return fallback
}

func safeLabel(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func inventoryNotFound(reason, message string) *inventoryError {
	return &inventoryError{status: http.StatusNotFound, reasonCode: reason, message: message}
}

func inventoryStoreError(err error) *inventoryError {
	if errors.Is(err, controlplane.ErrInventoryProjectionPending) {
		return &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "inventory_projection_pending", message: "Inventory projection is still reconciling", cause: err}
	}
	return &inventoryError{status: http.StatusInternalServerError, reasonCode: "inventory_store_unavailable", message: "Inventory unavailable", cause: err}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
