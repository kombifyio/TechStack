package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

// The classification split for authorization denials (kombify-Techstack-nzy1.14):
// a denial of the SESSION tenant's own collection scope maps to the retryable
// 401 session_reprojection_required signal, while a genuine resource-level
// denial keeps its fail-closed 403 inventory_access_denied semantics.
func TestInventorySessionTenantDenialMapsToSessionReprojectionSignal(t *testing.T) {
	sessionreauth.Configure("techstack_session", false)
	store := controlplane.NewMemoryStore()
	h := inventoryHandlers{app: &inventoryApplication{read: store, policy: denyInventoryPolicy{}, now: time.Now}, version: "test"}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers", "owner-1", "tenant-1", nil)
	err := h.httpListServers(event)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("collection denial = %v, want 401 session_reprojection_required", err)
	}
	details, ok := apiErr.Details.(map[string]any)
	if !ok || details["reason_code"] != sessionreauth.ReasonCode || details["retryable"] != true {
		t.Fatalf("collection denial details = %#v, want reason_code=%q retryable=true", apiErr.Details, sessionreauth.ReasonCode)
	}
	cookieCleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "techstack_session" && cookie.Value == "" && cookie.MaxAge == -1 {
			cookieCleared = true
		}
	}
	if !cookieCleared {
		t.Fatalf("session cookie not cleared on session-tenant denial: %v", recorder.Result().Cookies())
	}
}

func TestInventoryResourceLevelDenialStaysForbidden(t *testing.T) {
	sessionreauth.Configure("techstack_session", false)
	store := controlplane.NewMemoryStore()
	h := inventoryHandlers{app: &inventoryApplication{read: store, policy: denyInventoryPolicy{}, now: time.Now}, version: "test"}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers/server-1/health", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.httpServerHealth(event); err != nil {
		t.Fatalf("resource denial handler error = %v, want rendered 403 envelope", err)
	}
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "inventory_access_denied") {
		t.Fatalf("resource denial = %d %s, want fail-closed 403 inventory_access_denied", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "techstack_session" {
			t.Fatalf("session cookie mutated on resource-level denial: %+v", cookie)
		}
	}
}

func TestInventoryAPIIsOwnerScopedFreshnessQualifiedAndSanitized(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	observedAt := now.Add(-15 * time.Second)
	lifecycleChangedAt := now.Add(-4 * time.Minute)
	desiredChangedAt := now.Add(-3 * time.Minute)
	connectionChangedAt := now.Add(-2 * time.Minute)
	healthChangedAt := now.Add(-time.Minute)
	for _, stack := range []controlplane.CreateStackRequest{
		{ID: "stack-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner", Config: map[string]any{"stackkit_catalog_ref": "cloud-kit"}},
		{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"},
	} {
		if _, err := store.CreateStack(t.Context(), stack); err != nil {
			t.Fatal(err)
		}
	}
	credentialEndpoint := (&url.URL{
		Scheme: "ssh",
		Host:   "example.test",
		User:   url.UserPassword("root", "never-return"),
	}).String()
	ownerServer := controlplane.ServerRuntime{
		ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "owner-1", Name: "Cloud One",
		ProviderRef: "ionos-managed", LeaseID: "lease-1", LifecycleState: "active", DesiredState: "stopped", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &observedAt, InventoryRevision: 7,
		LifecycleReasonCode: "runtime_ready", DesiredReasonCode: "operator_stopped", ConnectionReasonCode: "guard_connected", HealthReasonCode: "checks_passing",
		LifecycleChangedAt: lifecycleChangedAt, DesiredChangedAt: desiredChangedAt, ConnectionChangedAt: connectionChangedAt, HealthChangedAt: healthChangedAt,
		Channels: []controlplane.ServerChannel{{Type: "ssh", Role: "fallback", State: "connected", EndpointRef: credentialEndpoint, ObservedAt: &observedAt, Metadata: map[string]any{"token": "never-return"}}},
		Metadata: map[string]any{
			"credential_ref": "never-return", "provider_id": "ionos", "stackkit": "cloud-kit", "stackkit_version": "0.6.2", "stackkit_mode": "main",
			"service_projection_expected": 1, "cleanup_state": "absence_pending",
			"host":   map[string]any{"os": "ubuntu", "os_version": "24.04", "arch": "amd64", "public_ip": "85.215.38.99", "private_ip": "10.0.0.4", "local_ip": "192.168.178.155"},
			"domain": "base.demo.kombified.com",
		},
	}
	for _, server := range []controlplane.ServerRuntime{ownerServer, {ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-foreign", OwnerSubjectID: "owner-2", Name: "Foreign", LastHeartbeatAt: &observedAt}} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-owner", TenantID: "tenant-1", StackID: "stack-owner", ServerID: "server-owner", ServiceKey: "coolify", Name: "Coolify",
		ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt, StackKitVersion: "0.6.2", Source: "stackkits-inventory",
		Access:   map[string]any{"mode": "relay", "url": "https://coolify.demo.kombify.me?access_token=never-return#secret", "route_id": "secret-route", "auth_hint": "secret-auth"},
		Metadata: map[string]any{"logs": "never-return", "token": "never-return", "inventory_revision": int64(7)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-confused-owner", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-owner", ServiceKey: "foreign", Name: "Foreign stack service", ObservedAt: &observedAt,
	}); err != nil {
		t.Fatal(err)
	}
	h := newTestInventoryHandlers(store, func() time.Time { return now })

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers", "owner-1", "tenant-1", nil)
	if err := h.httpListServers(event); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"server-foreign", "never-return", "credential_ref", "endpoint_ref", "lease_id", "ssh://", "route_id", "auth_hint", "logs"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	var serversEnvelope struct {
		Data inventoryServerList `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &serversEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(serversEnvelope.Data.Servers) != 1 || serversEnvelope.Data.Servers[0].Freshness.State != inventoryFresh || serversEnvelope.Data.InventoryRevision != 7 {
		t.Fatalf("server inventory = %#v", serversEnvelope.Data)
	}
	server := serversEnvelope.Data.Servers[0]
	if server.Platform.OS != "ubuntu" || server.StackKit.Name != "cloud-kit" || server.Provider != "ionos" || len(server.Addresses.PublicIPs) != 1 || len(server.Addresses.Domains) != 1 {
		t.Fatalf("server metadata = %#v", server)
	}
	if server.Lifecycle.State != "active" || server.Desired.State != "stopped" || server.Lease.ID != "lease-1" || server.Cleanup.State != "absence_pending" || server.Cleanup.ProviderAbsenceVerified {
		t.Fatalf("server authority projections = %#v", server)
	}
	if server.Lifecycle.ReasonCode != "runtime_ready" || server.Lifecycle.ChangedAt == nil || !server.Lifecycle.ChangedAt.Equal(lifecycleChangedAt) ||
		server.Desired.ReasonCode != "operator_stopped" || server.Desired.ChangedAt == nil || !server.Desired.ChangedAt.Equal(desiredChangedAt) ||
		server.Connection.ReasonCode != "guard_connected" || server.Connection.ChangedAt == nil || !server.Connection.ChangedAt.Equal(connectionChangedAt) ||
		server.Health.ReasonCode != "checks_passing" || server.Health.ChangedAt == nil || !server.Health.ChangedAt.Equal(healthChangedAt) {
		t.Fatalf("server dimension reasons/timestamps = %#v", server)
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/services", "owner-1", "tenant-1", nil)
	if err := h.httpListServices(event); err != nil {
		t.Fatal(err)
	}
	var servicesEnvelope struct {
		Data inventoryServiceList `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &servicesEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(servicesEnvelope.Data.Services) != 1 || len(servicesEnvelope.Data.Services[0].Links) != 1 || servicesEnvelope.Data.Services[0].Links[0].URL != "https://coolify.demo.kombify.me" {
		t.Fatalf("service inventory = %#v", servicesEnvelope.Data)
	}
	for _, forbidden := range []string{"secret-route", "secret-auth", "never-return"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("service response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestInventoryCleanupRequiresTimestampedAbsenceAndCompleteCleanupGraph(t *testing.T) {
	verifiedAt := time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)
	server := controlplane.ServerRuntime{LifecycleState: "decommissioned", DecommissionedAt: &verifiedAt}
	for _, test := range []struct {
		name     string
		metadata map[string]any
		state    string
		verified bool
	}{
		{name: "boolean alone", metadata: map[string]any{"cleanup_state": "absent", "provider_absence_verified": true, "cleanup_complete": true}, state: "unverified"},
		{name: "incomplete graph", metadata: map[string]any{"cleanup_state": "absent", "provider_absence_verified": true, "provider_absence_verified_at": verifiedAt.Format(time.RFC3339Nano)}, state: "unverified"},
		{name: "full proof", metadata: map[string]any{"cleanup_state": "absent", "provider_absence_verified": true, "provider_absence_verified_at": verifiedAt.Format(time.RFC3339Nano), "cleanup_complete": true}, state: "absent", verified: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := inventoryCleanupFromServer(server, test.metadata)
			if got.State != test.state || got.ProviderAbsenceVerified != test.verified {
				t.Fatalf("cleanup = %#v", got)
			}
			if test.verified && (got.VerifiedAt == nil || !got.VerifiedAt.Equal(verifiedAt)) {
				t.Fatalf("verified_at = %v", got.VerifiedAt)
			}
			if !test.verified && got.VerifiedAt != nil {
				t.Fatalf("unverified cleanup exposed timestamp %v", got.VerifiedAt)
			}
		})
	}
}

func TestInventoryAPIForeignResourcesAreNotFound(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "foreign-stack", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "foreign-server", TenantID: "tenant-1", StackID: "foreign-stack", OwnerSubjectID: "owner-2", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "other-tenant-stack", TenantID: "tenant-2", OwnerSubjectID: "owner-1", Name: "Other tenant"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "other-tenant-server", TenantID: "tenant-2", StackID: "other-tenant-stack", OwnerSubjectID: "owner-1", Name: "Other tenant"}); err != nil {
		t.Fatal(err)
	}
	h := newTestInventoryHandlers(store, time.Now)

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers/foreign-server/health", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "foreign-server")
	if err := h.httpServerHealth(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "owner-2") {
		t.Fatalf("foreign server status/body = %d %s", recorder.Code, recorder.Body.String())
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/services?server_id=foreign-server", "owner-1", "tenant-1", nil)
	if err := h.httpListServices(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign filter status/body = %d %s", recorder.Code, recorder.Body.String())
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers/other-tenant-server/health", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "other-tenant-server")
	if err := h.httpServerHealth(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "tenant-2") {
		t.Fatalf("cross-tenant status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInventoryAPIRejectsCallerOwnerAndTenantOverridesBeforeRead(t *testing.T) {
	read := &countingInventoryReadStore{InventoryReadStore: controlplane.NewMemoryStore()}
	h := inventoryHandlers{app: &inventoryApplication{read: read, policy: NewSelfHostedInventoryPolicy(), now: time.Now}, version: "test"}

	for _, query := range []string{"owner_id=owner-2", "tenant_id=tenant-2"} {
		event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/inventory/servers?"+query, "owner-1", "tenant-1", nil)
		if err := h.httpListServers(event); err != nil {
			t.Fatalf("override %q: %v", query, err)
		}
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "unsupported_query_parameter") {
			t.Fatalf("override %q response = %d %s", query, recorder.Code, recorder.Body.String())
		}
	}
	if read.reads != 0 {
		t.Fatalf("scope override reached store %d times", read.reads)
	}
}

func TestInventoryServerSummaryCountsOnlyAuthenticatedOwnerScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{ID: "owned", TenantID: "tenant-1", OwnerSubjectID: "owner-1"},
		{ID: "foreign-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-2"},
		{ID: "foreign-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	h := newTestInventoryHandlers(store, func() time.Time {
		return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	})

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/v1/ril/servers/summary", "owner-1", "tenant-1", nil)
	if err := h.httpServerSummary(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Fatalf("summary status/cache = %d %q: %s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	var envelope struct {
		Data inventoryServerSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Count != 1 || !envelope.Data.HasServers {
		t.Fatalf("summary = %#v, want one owned server", envelope.Data)
	}
	for _, forbidden := range []string{"owned", "foreign-owner", "foreign-tenant", "tenant-1", "owner-1"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestInventoryServerSummaryFailsClosedForTenantOverride(t *testing.T) {
	read := &countingInventoryReadStore{InventoryReadStore: controlplane.NewMemoryStore()}
	h := inventoryHandlers{app: &inventoryApplication{read: read, policy: NewSelfHostedInventoryPolicy(), now: time.Now}, version: "test"}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/summary?tenant=tenant-2", "owner-1", "tenant-1", nil)
	if err := h.httpServerSummary(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "tenant_scope_mismatch") {
		t.Fatalf("mismatch response = %d %s", recorder.Code, recorder.Body.String())
	}
	if read.reads != 0 {
		t.Fatalf("tenant mismatch reached store %d times", read.reads)
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/summary?tenant=tenant-1", "owner-1", "tenant-1", nil)
	if err := h.httpServerSummary(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "inventory_summary_unavailable") {
		t.Fatalf("missing counter response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInventoryAccessContextDropsEndpointReferencesAndDisablesStaleLinks(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	stale := now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second)
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Name: "Server", LastHeartbeatAt: &stale, ConnectionState: "connected", HealthState: "healthy", Channels: []controlplane.ServerChannel{{Type: "ssh", Role: "fallback", State: "connected", EndpointRef: "root@192.168.1.2"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "app", Name: "App", ObservedAt: &stale, Access: map[string]any{"mode": "direct", "url": "https://app.example.test"}}); err != nil {
		t.Fatal(err)
	}
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	result, err := h.app.serverAccessContext(t.Context(), inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}, "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Availability.State != inventoryUnavailable || result.Freshness.State != inventoryStale || len(result.ServiceLinks) != 0 {
		t.Fatalf("stale access context = %#v", result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "root@") || strings.Contains(string(raw), "endpoint_ref") {
		t.Fatalf("access context leaked endpoint material: %s", raw)
	}
}

func TestInventoryServicesFailClosedUntilSnapshotRevisionProjectionCompletes(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		InventoryRevision: 4, Metadata: map[string]any{"service_projection_expected": 1},
	}); err != nil {
		t.Fatal(err)
	}
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	_, err := h.app.listServices(t.Context(), inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}, "", inventoryPageOptions{Limit: 10})
	var inventoryErr *inventoryError
	if !errors.As(err, &inventoryErr) || inventoryErr.status != http.StatusServiceUnavailable || inventoryErr.reasonCode != "inventory_projection_pending" {
		t.Fatalf("incomplete service projection error = %#v, want fail-closed pending", err)
	}
	if _, upsertErr := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "app",
		Metadata: map[string]any{"inventory_revision": int64(4)},
	}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	result, err := h.app.listServices(t.Context(), inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}, "", inventoryPageOptions{Limit: 10})
	if err != nil || len(result.Services) != 1 {
		t.Fatalf("completed service projection = %#v err=%v", result, err)
	}
}

func TestInventoryCollectionCursorKeepsOneBoundedTraversal(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	for _, id := range []string{"server-a", "server-b", "server-c"} {
		if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: id, TenantID: "tenant-1", OwnerSubjectID: "owner-1"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	scope := inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}

	first, err := h.app.listServers(t.Context(), scope, inventoryPageOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Servers) != 1 || first.Servers[0].ID != "server-a" || first.NextCursor == "" || first.CollectionCursor == "" {
		t.Fatalf("first cursor page = %#v", first)
	}
	if _, upsertErr := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "server-later", TenantID: "tenant-1", OwnerSubjectID: "owner-1"}); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	second, err := h.app.listServers(t.Context(), scope, inventoryPageOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Servers) != 1 || second.Servers[0].ID != "server-b" || second.CollectionCursor != first.CollectionCursor || second.NextCursor == "" {
		t.Fatalf("second cursor page = %#v", second)
	}
	third, err := h.app.listServers(t.Context(), scope, inventoryPageOptions{Limit: 1, Cursor: second.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Servers) != 1 || third.Servers[0].ID != "server-c" || third.CollectionCursor != first.CollectionCursor || third.NextCursor != "" {
		t.Fatalf("final cursor page = %#v", third)
	}
	if _, err := h.app.listServices(t.Context(), scope, "", inventoryPageOptions{Limit: 1, Cursor: first.NextCursor}); err == nil {
		t.Fatal("server cursor was accepted for the service collection")
	}
}

func TestInventoryCursorIsBoundToAuthorizedPrincipalAndServiceFilter(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, stack := range []controlplane.CreateStackRequest{
		{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "One"},
		{ID: "stack-2", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Two"},
	} {
		if _, err := store.CreateStack(t.Context(), stack); err != nil {
			t.Fatal(err)
		}
	}
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"},
		{ID: "server-2", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"},
		{ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-2", OwnerSubjectID: "owner-2"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []controlplane.ServiceRuntime{
		{ID: "service-a", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "a"},
		{ID: "service-b", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "b"},
	} {
		if _, err := store.UpsertServiceRuntime(t.Context(), service); err != nil {
			t.Fatal(err)
		}
	}
	app := newTestInventoryHandlers(store, time.Now).app
	ownerOne := inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}
	servers, err := app.listServers(t.Context(), ownerOne, inventoryPageOptions{Limit: 1})
	if err != nil || servers.NextCursor == "" {
		t.Fatalf("server cursor = %#v err=%v", servers, err)
	}
	if _, otherOwnerErr := app.listServers(t.Context(), inventoryScope{tenantID: "tenant-1", ownerID: "owner-2"}, inventoryPageOptions{Limit: 1, Cursor: servers.NextCursor}); otherOwnerErr == nil {
		t.Fatal("owner-bound server cursor was accepted for another principal")
	}

	services, err := app.listServices(t.Context(), ownerOne, "server-1", inventoryPageOptions{Limit: 1})
	if err != nil || services.NextCursor == "" {
		t.Fatalf("service cursor = %#v err=%v", services, err)
	}
	if _, err := app.listServices(t.Context(), ownerOne, "server-2", inventoryPageOptions{Limit: 1, Cursor: services.NextCursor}); err == nil {
		t.Fatal("server-filter-bound service cursor was accepted for another filter")
	}
}

func TestInventoryEmptyCollectionCursorRemainsFrozen(t *testing.T) {
	store := controlplane.NewMemoryStore()
	app := newTestInventoryHandlers(store, time.Now).app
	scope := inventoryScope{tenantID: "tenant-1", ownerID: "owner-1"}
	first, err := app.listServers(t.Context(), scope, inventoryPageOptions{Limit: 10})
	if err != nil || len(first.Servers) != 0 || first.CollectionCursor == "" {
		t.Fatalf("empty collection = %#v err=%v", first, err)
	}
	if _, upsertErr := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "server-later", TenantID: "tenant-1", OwnerSubjectID: "owner-1"}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	replayed, err := app.listServers(t.Context(), scope, inventoryPageOptions{Limit: 10, Cursor: first.CollectionCursor})
	if err != nil || len(replayed.Servers) != 0 || replayed.CollectionCursor != first.CollectionCursor {
		t.Fatalf("replayed empty collection = %#v err=%v", replayed, err)
	}
}

func newTestInventoryHandlers(store controlplane.InventoryReadStore, now func() time.Time) inventoryHandlers {
	return inventoryHandlers{app: &inventoryApplication{read: store, policy: NewSelfHostedInventoryPolicy(), now: now}, version: "test"}
}
