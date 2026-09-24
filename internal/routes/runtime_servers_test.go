package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type recordingSelfOwnedServerDetacher struct {
	request controlplane.SelfOwnedServerDetachRequest
}

func (d *recordingSelfOwnedServerDetacher) DetachSelfOwnedServer(_ context.Context, request controlplane.SelfOwnedServerDetachRequest) (*controlplane.SelfOwnedServerDetachReceipt, error) {
	d.request = request
	return &controlplane.SelfOwnedServerDetachReceipt{ServerID: request.ServerID, AgentID: "agent-1", Revision: 3, Generation: 1}, nil
}

func TestSelfOwnedServerDetachRejectsMissingOperateEntitlementBeforeDetach(t *testing.T) {
	detacher := &recordingSelfOwnedServerDetacher{}
	h := serverRuntimeHandlers{detacher: detacher, policy: denyInventoryPolicy{}}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/detach", "owner-1", "tenant-1", map[string]any{"confirm_server_id": "server-1"})
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.detach(event); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if recorder.Code != http.StatusForbidden || detacher.request.ServerID != "" {
		t.Fatalf("status=%d detacher=%+v body=%s, want fail-closed 403 before detach", recorder.Code, detacher.request, recorder.Body.String())
	}
}

func TestSelfOwnedServerDetachRouteBindsOwnerConfirmationAndDisconnect(t *testing.T) {
	detacher := &recordingSelfOwnedServerDetacher{}
	disconnected := ""
	h := serverRuntimeHandlers{
		detacher: detacher, policy: NewSelfHostedInventoryPolicy(),
		agentDisconnect: func(agentID string) error { disconnected = agentID; return nil },
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/detach", "owner-1", "tenant-1", map[string]any{"confirm_server_id": "server-1"})
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.detach(event); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if recorder.Code != http.StatusOK || detacher.request.TenantID != "tenant-1" || detacher.request.OwnerSubjectID != "owner-1" || detacher.request.ConfirmServerID != "server-1" {
		t.Fatalf("status=%d request=%+v body=%s", recorder.Code, detacher.request, recorder.Body.String())
	}
	if disconnected != "agent-1" {
		t.Fatalf("disconnected agent = %q, want agent-1", disconnected)
	}
}

// TestServerRuntimeRoutesReturnPersistedStateAndOwnerIsolation locks the
// honest read model: the API returns the PERSISTED connection/health
// dimensions. Heartbeat-freshness demotion is the registry sweeper's job (a
// durable write through ApplyServerEvent), never a read-time recompute.
func TestServerRuntimeRoutesReturnPersistedStateAndOwnerIsolation(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-2 * time.Minute)
	outcomeChangedAt := now.Add(-3 * time.Minute)
	pendingOutcome := &outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: "awaiting_guard_heartbeat", Capability: "techstack.server.connect",
		Retryable: false, OccurredAt: outcomeChangedAt,
		UserGuidance: &outcome.Guidance{
			Title: "Server connection is being verified", Body: "Techstack is waiting for Guard.",
			NextSteps: []outcome.Step{{ID: "wait", Label: "Keep the server online.", Kind: "note"}},
		},
	}
	for _, server := range []controlplane.ServerRuntime{
		{ID: "owner-server", TenantID: "tenant-1", StackID: "techstack-owner", OwnerSubjectID: "owner-1", Name: "Owner", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &heartbeat, LastOutcome: pendingOutcome, OutcomeChangedAt: &outcomeChangedAt},
		{ID: "removed-server", TenantID: "tenant-1", StackID: "techstack-owner", OwnerSubjectID: "owner-1", Name: "Removed", LifecycleState: "decommissioned", DesiredState: "absent"},
		{ID: "other-server", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Other", LifecycleState: "active"},
	} {
		if _, err := store.UpsertServerRuntime(context.Background(), server); err != nil {
			t.Fatalf("UpsertServerRuntime: %v", err)
		}
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers?kit_deployment_id=techstack-owner", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: func() time.Time { return now }}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "owner-server" || envelope.Data[0].NodeID != "owner-server" {
		t.Fatalf("owner projection leaked rows: %#v", envelope.Data)
	}
	if envelope.Data[0].KitDeploymentID != "techstack-owner" {
		t.Fatalf("kit deployment identity = %#v", envelope.Data[0])
	}
	// Persisted connected/healthy is returned verbatim even though the
	// heartbeat aged past the fresh window; staleness_seconds still exposes
	// the raw evidence age for clients.
	got := envelope.Data[0]
	if got.Connection.State != "connected" || got.Health.State != "healthy" || !got.MutationsAllowed {
		t.Fatalf("read path overrode persisted state: %#v", got)
	}
	if got.Connection.StalenessSeconds == nil || *got.Connection.StalenessSeconds != 120 {
		t.Fatalf("staleness seconds = %#v, want 120", got.Connection.StalenessSeconds)
	}
	if got.LastOutcome == nil || got.LastOutcome.ReasonCode != "awaiting_guard_heartbeat" || got.LastOutcome.UserGuidance == nil {
		t.Fatalf("last outcome was not returned: %#v", got.LastOutcome)
	}
}

// TestServerRuntimeRoutesReturnSweeperDemotedState: once the sweeper persisted
// a demotion, the API reflects it (no read-time promotion either).
func TestServerRuntimeRoutesReturnSweeperDemotedState(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-10 * time.Second)
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "owner-server", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owner",
		LifecycleState: "active", ConnectionState: "stale", HealthState: "unknown",
		ReasonCode: "heartbeat_stale", LastHeartbeatAt: &heartbeat,
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: func() time.Time { return now }}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("unexpected rows: %#v", envelope.Data)
	}
	got := envelope.Data[0]
	if got.Connection.State != "stale" || got.Health.State != "unknown" || got.MutationsAllowed {
		t.Fatalf("persisted demotion was not returned: %#v", got)
	}
}

func TestServerRuntimeDetailHidesForeignServer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign",
	}); err != nil {
		t.Fatal(err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/foreign", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "foreign")
	if err := (serverRuntimeHandlers{store: store, now: time.Now}).get(event); err != nil {
		t.Fatalf("get: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServerRuntimePortInventoryUsesCanonicalOwnerScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "owned", TenantID: "tenant-1", OwnerSubjectID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	reader := &recordingPortInventoryReader{result: portinventory.Inventory{
		ServerID: "owned", Allocations: []portinventory.Allocation{{ID: "port-1", StackID: "stack-1"}},
	}}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/owned/ports", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "owned")
	if err := (serverRuntimeHandlers{store: store, ports: reader, now: time.Now}).portInventory(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, no-store" || reader.request.TenantID != "tenant-1" || reader.request.ServerID != "owned" || reader.request.OwnerSubjectID != "owner-1" {
		t.Fatalf("owner-scoped server ports = status %d cache %q request %#v", recorder.Code, recorder.Header().Get("Cache-Control"), reader.request)
	}
	var envelope struct {
		Data portinventory.Inventory `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || len(envelope.Data.Allocations) != 1 || envelope.Data.Allocations[0].StackID != "stack-1" {
		t.Fatalf("port inventory response = %s, want canonical allocation ownership", recorder.Body.String())
	}
}

// TestServerRuntimeRoutesKeepOwnerIsolationForAdminCallers pins the boundary
// that a shared tenant makes load-bearing: an operator role must not turn the
// customer inventory surface into a tenant-wide view. Production tenants have
// held several unrelated owners at once, so "admin sees everything here" is
// indistinguishable from a cross-owner leak.
func TestServerRuntimeRoutesKeepOwnerIsolationForAdminCallers(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{ID: "own", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Own", LifecycleState: "active"},
		{ID: "foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign", LifecycleState: "active"},
	} {
		if _, err := store.UpsertServerRuntime(context.Background(), server); err != nil {
			t.Fatalf("UpsertServerRuntime: %v", err)
		}
	}
	handlers := serverRuntimeHandlers{store: store, now: time.Now}

	event, recorder := adminServerRuntimeTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1")
	if err := handlers.list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "own" {
		t.Fatalf("admin caller received foreign inventory: %#v", envelope.Data)
	}

	detailEvent, detailRecorder := adminServerRuntimeTestEvent(http.MethodGet, "/api/v1/servers/foreign", "owner-1", "tenant-1")
	detailEvent.Request.SetPathValue("serverId", "foreign")
	if err := handlers.get(detailEvent); err != nil {
		t.Fatalf("get: %v", err)
	}
	if detailRecorder.Code != http.StatusNotFound {
		t.Fatalf("admin detail status = %d, want 404; body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
}

// TestServerRuntimeListUsesHostingerSRVDisplayName keeps a cloned Linux
// hostname from collapsing a Hostinger VPS into the same operator label as a
// local node that actually owns that hostname.
func TestServerRuntimeListUsesHostingerSRVDisplayName(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, server := range []controlplane.ServerRuntime{
		{
			ID: "hostinger-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
			Name: "t3-code-ryzen", ProviderRef: "hostinger-vps:srv1161760",
			LifecycleState: string(serverregistry.LifecycleActive),
		},
		{
			ID: "local-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
			Name: "t3-code-ryzen", LifecycleState: string(serverregistry.LifecycleActive),
		},
	} {
		if _, err := store.UpsertServerRuntime(context.Background(), server); err != nil {
			t.Fatalf("UpsertServerRuntime: %v", err)
		}
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: time.Now}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]string{}
	for _, item := range envelope.Data {
		byID[item.ID] = item.Name
	}
	if byID["hostinger-1"] != "srv1161760" {
		t.Fatalf("hostinger display name = %q, want srv1161760", byID["hostinger-1"])
	}
	if byID["local-1"] != "t3-code-ryzen" {
		t.Fatalf("local display name = %q, want enrolled hostname", byID["local-1"])
	}
}

// TestServerRuntimeListDropsOwnerlessRows keeps the owner filter fail-closed: a
// row that records no owner belongs to nobody and must not fall through to
// every caller in the tenant.
func TestServerRuntimeListDropsOwnerlessRows(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "ownerless", TenantID: "tenant-1", Name: "Ownerless", LifecycleState: "active",
	}); err != nil {
		t.Fatalf("UpsertServerRuntime: %v", err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", "owner-1", "tenant-1", nil)
	if err := (serverRuntimeHandlers{store: store, now: time.Now}).list(event); err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Data []serverRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 0 {
		t.Fatalf("ownerless row served to caller: %#v", envelope.Data)
	}
}

func adminServerRuntimeTestEvent(method, target, ownerID, tenantID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: ownerID, OrgID: tenantID, Roles: []string{"global_admin"},
	}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}
