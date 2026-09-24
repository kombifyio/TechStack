package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestRegistryStoreProjectionExpiresInventoryWithoutHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-health", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Health stack", Status: "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	lastSeen := now.Add(-6 * time.Minute)
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "agent-1", TenantID: "tenant-1", StackID: "stack-health", OwnerSubjectID: "owner-1", Approved: true, Status: "connected", LastSeenAt: &lastSeen,
		Capabilities: map[string]any{"lease_id": "lease-1"},
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-health", WorkerID: "agent-1", Name: "managed-1", Status: "healthy",
		Metadata: map[string]any{"lease_id": "lease-1", "health_state": "healthy"},
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if _, err := store.UpsertService(ctx, controlplane.Service{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-health", NodeID: "server-1", ServiceKey: "home", Name: "home", Source: "stackkits-inventory", Status: "healthy",
		URL:      "https://home.example.test",
		Metadata: map[string]any{"observed_at": now.Format(time.RFC3339Nano), "reported_status": "healthy", "health": map[string]any{"status": "healthy"}},
	}); err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	h := registryRouteHandlers{stackStore: store, workerStore: store, registryStore: store}
	if err := h.services(event); err != nil {
		t.Fatalf("services: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Wave 2 collapse (kombify-Techstack-nzy1.7): this row exists only in the
	// `nodes`/`workers` satellites, so no canonical aggregate ever persisted a
	// connection or health state for it. The legacy projection no longer
	// recomputes one from `workers.last_seen_at` at read time — it reports
	// `provisioned` ("exists, no persisted connection evidence"). The safety
	// property the route must keep is unchanged and asserted here: a satellite
	// row is never healthy and never rollout-ready.
	if len(envelope.Data.Servers) != 1 {
		t.Fatalf("expected exactly one server: %#v", envelope.Data.Servers)
	}
	server := envelope.Data.Servers[0]
	if server.Status != "provisioned" || server.HealthState != "provisioned" || server.RolloutReady {
		t.Fatalf("satellite-only server must project as provisioned and not rollout-ready: %#v", server)
	}
	if server.LastSeen == "" {
		t.Fatalf("satellite heartbeat evidence must still be reported verbatim: %#v", server)
	}
	if len(envelope.Data.Services) != 1 || envelope.Data.Services[0].Status != "unknown" || envelope.Data.Services[0].HealthState != "unknown" || envelope.Data.Services[0].URL != "" {
		t.Fatalf("stale inventory must not render service healthy: %#v", envelope.Data.Services)
	}
}

// TestRegistryStoreProjectionNeverPromotesSatelliteHeartbeat is the Wave 2
// regression guard. Before the collapse a FRESH `workers.last_seen_at` was
// enough for the legacy read route to publish `healthy` + `rollout_ready` for a
// row with no canonical aggregate — a read-time verdict invented from satellite
// evidence. `nodes` and `workers` are read-only satellites now: only the
// aggregate may certify a server.
func TestRegistryStoreProjectionNeverPromotesSatelliteHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-fresh", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Fresh stack", Status: "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	lastSeen := now.Add(-5 * time.Second)
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "agent-fresh", TenantID: "tenant-1", StackID: "stack-fresh", OwnerSubjectID: "owner-1", Approved: true,
		Status: "connected", LastSeenAt: &lastSeen,
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if _, err := store.UpsertNode(ctx, controlplane.Node{
		ID: "server-fresh", TenantID: "tenant-1", StackID: "stack-fresh", WorkerID: "agent-fresh", Name: "fresh-1",
		Status: "healthy", Metadata: map[string]any{"health_state": "healthy"},
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	h := registryRouteHandlers{stackStore: store, workerStore: store, registryStore: store}
	if err := h.services(event); err != nil {
		t.Fatalf("services: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Servers) != 1 {
		t.Fatalf("expected exactly one server: %#v", envelope.Data.Servers)
	}
	server := envelope.Data.Servers[0]
	if server.Status == "healthy" || server.HealthState == "healthy" || server.RolloutReady {
		t.Fatalf("a fresh satellite heartbeat must not certify a server without a canonical aggregate: %#v", server)
	}
	if server.Status != "provisioned" || server.HealthState != "provisioned" {
		t.Fatalf("satellite-only server must project as provisioned: %#v", server)
	}
}

func TestRegistryImportCreatesObservedServiceForOwnedNode(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"})
	if err != nil {
		t.Fatalf("create stack: %v", err)
	}
	node, err := store.UpsertNode(ctx, controlplane.Node{ID: "server-1", TenantID: "tenant-1", StackID: stack.ID, Name: "foundation-1"})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	body := map[string]any{
		preCheckStackIDField: stack.ID,
		"server_id":          node.ID,
		routeNameField:       "custom-dashboard",
		"display_name":       "Custom Dashboard",
		"port":               8088,
		"url":                "http://foundation-1:8088",
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/import", "owner-1", "tenant-1", body)
	if routeErr := (registryRouteHandlers{stackStore: store, registryStore: store}).importUnmanagedService(event); routeErr != nil {
		t.Fatalf("import returned router error: %v", routeErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	serviceID := runtimeidentity.ServiceID(stack.ID, node.ID, "custom_dashboard", "default")
	service, err := store.GetService(ctx, "tenant-1", serviceID)
	if err != nil {
		t.Fatalf("find imported service: %v", err)
	}
	if service.Source != "observed" || service.ManagementState != registryObservedState {
		t.Fatalf("imported service ownership = source %q management %q", service.Source, service.ManagementState)
	}
}

func TestRegistryAttachCreatesManagedCatalogDeclaration(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-attach", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack"})
	if err != nil {
		t.Fatalf("create stack: %v", err)
	}
	node, err := store.UpsertNode(ctx, controlplane.Node{ID: "server-attach", TenantID: "tenant-1", StackID: stack.ID})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/attach", "owner-1", "tenant-1", map[string]any{
		"stack_id": stack.ID, "server_id": node.ID, "service_id": registryServiceVault,
	})
	if routeErr := (registryRouteHandlers{stackStore: store, registryStore: store}).attachService(event); routeErr != nil {
		t.Fatalf("attach returned router error: %v", routeErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	serviceID := runtimeidentity.ServiceID(stack.ID, node.ID, registryServiceVault, "default")
	service, err := store.GetService(ctx, "tenant-1", serviceID)
	if err != nil {
		t.Fatalf("find attached service: %v", err)
	}
	if service.Source != "techstack-registry" || service.ManagementState != registryManagedState || service.Status != preCheckStatusPending {
		t.Fatalf("attached service = %#v", service)
	}
}

func TestRegistryImportRejectsForeignStack(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"})
	if err != nil {
		t.Fatalf("create stack: %v", err)
	}
	node, err := store.UpsertNode(ctx, controlplane.Node{ID: "server-foreign", TenantID: "tenant-1", StackID: stack.ID})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	body := map[string]any{
		preCheckStackIDField: stack.ID,
		"server_id":          node.ID,
		routeNameField:       "custom-dashboard",
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/import", "owner-1", "tenant-1", body)
	if err := (registryRouteHandlers{stackStore: store, registryStore: store}).importUnmanagedService(event); err != nil {
		t.Fatalf("import returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func registryRouteStoreTestEvent(method, target, ownerID, tenantID string, body map[string]any) (*httpx.Event, *httptest.ResponseRecorder) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if ownerID != "" || tenantID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func TestRegistryMigrateServiceUsesCanonicalStore(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Demo Stack",
		Status:         "running",
	})
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	sourceNode, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-main",
		TenantID: "tenant-1",
		StackID:  stack.ID,
		Name:     "centron-main",
		Role:     "foundation",
		Status:   "online",
	})
	if err != nil {
		t.Fatalf("UpsertNode source: %v", err)
	}
	targetNode, err := store.UpsertNode(ctx, controlplane.Node{
		ID:       "node-worker",
		TenantID: "tenant-1",
		StackID:  stack.ID,
		Name:     "ionos-worker",
		Role:     "worker",
		Status:   "online",
	})
	if err != nil {
		t.Fatalf("UpsertNode target: %v", err)
	}
	service, err := store.UpsertService(ctx, controlplane.Service{
		ID:         "svc-vault",
		TenantID:   "tenant-1",
		StackID:    stack.ID,
		NodeID:     sourceNode.ID,
		ServiceKey: "vaultwarden",
		Name:       "Vaultwarden",
		Status:     registryStatusRunning,
		Source:     stackKitOutputKey,
		URL:        "https://vault.kombified.com",
		Metadata:   map[string]any{"display_name": "Vaultwarden", "type": "auth"},
	})
	if err != nil {
		t.Fatalf("UpsertService: %v", err)
	}

	handlers := registryRouteHandlers{
		stackStore:    store,
		registryStore: store,
		jobStore:      store,
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", "tenant-1", map[string]any{
		"service_id":       service.ID,
		"target_server_id": targetNode.ID,
	})
	if err := handlers.migrateService(event); err != nil {
		t.Fatalf("migrate service route error: %v", err)
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("migrate status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
	}
	updatedSource, err := store.GetService(ctx, "tenant-1", service.ID)
	if err != nil {
		t.Fatalf("GetService source: %v", err)
	}
	if updatedSource.Status != registryStatusRunning || updatedSource.URL == "" {
		t.Fatalf("source was mutated by unavailable migration: %#v", updatedSource)
	}
	services, err := store.ListServicesByStack(ctx, "tenant-1", stack.ID)
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	var targetService controlplane.Service
	for _, row := range services {
		if row.NodeID == targetNode.ID && row.ServiceKey == "vaultwarden" {
			targetService = row
			break
		}
	}
	if targetService.ID != "" {
		t.Fatalf("target service = %#v, want no synthetic target", targetService)
	}
}

func TestRegistryVerifyService(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/verify", "owner-1", "tenant-1", map[string]any{
		"service_id": "service-1",
	})
	if err := (registryRouteHandlers{stackStore: store, registryStore: store}).verifyService(event); err != nil {
		t.Fatalf("verify service route error: %v", err)
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
	}
}

func TestRegistryDeleteService(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := registryRouteStoreTestEvent(http.MethodDelete, "/api/v1/registry/services/service-1", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("id", "service-1")
	if err := (registryRouteHandlers{stackStore: store, registryStore: store}).deleteService(event); err != nil {
		t.Fatalf("delete service route error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503 until canonical server fencing is active", recorder.Code, recorder.Body.String())
	}
}
