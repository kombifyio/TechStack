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
	"github.com/pocketbase/pocketbase/tests"
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

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/servers", "owner-1", "tenant-1", nil)
	h := registryRouteHandlers{stackStore: store, workerStore: store, registryStore: store}
	if err := h.servers(event); err != nil {
		t.Fatalf("servers: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Servers []registryServer `json:"servers"`
		} `json:"data"`
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

func TestRegistryStoreProjectionIgnoresMissingLegacyCollection(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	if _, err := app.FindCollectionByNameOrId(registryCollectionStacks); err == nil {
		t.Fatalf("test fixture unexpectedly contains %q collection", registryCollectionStacks)
	}

	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store-only",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Canonical Stack",
		Status:         "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	h := registryRouteHandlers{
		app:           app,
		stackStore:    store,
		registryStore: store,
	}
	if err := h.services(event); err != nil {
		t.Fatalf("services: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Catalog) == 0 {
		t.Fatal("expected service catalog entries")
	}
	if len(envelope.Data.Stacks) != 1 || envelope.Data.Stacks[0].ID != "stack-store-only" {
		t.Fatalf("stacks = %#v, want canonical store stack only", envelope.Data.Stacks)
	}
}

func TestRegistryStoreProjectionFailsOnLegacyDatabaseError(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	db, ok := app.ConcurrentDB().(interface{ Close() error })
	if !ok {
		t.Fatalf("concurrent database = %T, want closable database", app.ConcurrentDB())
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close concurrent database: %v", closeErr)
	}

	store := controlplane.NewMemoryStore()
	event, _ := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	h := registryRouteHandlers{
		app:           app,
		stackStore:    store,
		registryStore: store,
	}
	_, err = h.registryPayload(event)
	if err == nil {
		t.Fatal("registryPayload returned nil error after the legacy database was closed")
	}
	apiErr, ok := err.(*httpx.APIError)
	if !ok {
		t.Fatalf("error = %T %v, want *httpx.APIError", err, err)
	}
	if apiErr.Status != http.StatusInternalServerError || apiErr.Message != "Failed to fetch stacks" {
		t.Fatalf("error = %#v, want 500 Failed to fetch stacks", apiErr)
	}
}

func TestRegistryServicesListsCatalogAndObservedInventory(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	node := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")
	service := createStackOperationsTestService(t, app, node.Id, "custom-dashboard")
	service.Set(preCheckStatusField, registryObservedState)
	service.Set(featureResponseTypeKey, registryCustomService)
	if err := app.Save(service); err != nil {
		t.Fatalf("save observed service: %v", err)
	}

	event, recorder := registryRouteTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", nil)
	if err := (registryRouteHandlers{app: app}).services(event); err != nil {
		t.Fatalf("services returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Catalog) == 0 {
		t.Fatal("expected service catalog entries")
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want 1", got)
	}
	if got := envelope.Data.Services[0].ManagementState; got != registryObservedState {
		t.Fatalf("management_state = %q, want observed", got)
	}
	if envelope.Data.Services[0].MoveAllowed {
		t.Fatal("observed unmanaged service must not be movable")
	}
	if got := envelope.Data.Services[0].ApplicationName; got == "" {
		t.Fatal("expected application_name to be populated")
	}
	if got := envelope.Data.Servers[0].RoleLabel; got != "Foundation Node" {
		t.Fatalf("server role label = %q, want Foundation Node", got)
	}
}

func TestRegistryServicesIncludesLegacyStacksWhenStoreConfigured(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-store",
		TenantID:       "default",
		OwnerSubjectID: "owner-1",
		Name:           "Store Stack",
		Status:         "running",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	legacyStack := createStackOperationsTestStack(t, app, "owner-1", "running")
	legacyNode := createStackOperationsTestNode(t, app, legacyStack.Id, "legacy-main")
	legacyService := createStackOperationsTestService(t, app, legacyNode.Id, "legacy-dashboard")
	legacyService.Set(preCheckStatusField, registryObservedState)
	if err := app.Save(legacyService); err != nil {
		t.Fatalf("save legacy service: %v", err)
	}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "default", nil)
	if err := (registryRouteHandlers{
		app:           app,
		stackStore:    store,
		registryStore: store,
		jobStore:      store,
	}).services(event); err != nil {
		t.Fatalf("services returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	seenStacks := map[string]bool{}
	for _, stack := range envelope.Data.Stacks {
		seenStacks[stack.ID] = true
	}
	if !seenStacks["stack-store"] || !seenStacks[legacyStack.Id] {
		t.Fatalf("stacks = %#v, want store and legacy stack", seenStacks)
	}
	if got := len(envelope.Data.Services); got != 1 {
		t.Fatalf("services = %d, want legacy observed service", got)
	}
}

func TestRegistryImportCreatesObservedServiceForOwnedNode(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	node := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")

	body := map[string]any{
		preCheckStackIDField: stack.Id,
		"server_id":          node.Id,
		backupNamePathKey:    "custom-dashboard",
		"display_name":       "Custom Dashboard",
		"port":               8088,
		"url":                "http://foundation-1:8088",
	}
	event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/import", "owner-1", body)
	if routeErr := (registryRouteHandlers{app: app}).importUnmanagedService(event); routeErr != nil {
		t.Fatalf("import returned router error: %v", routeErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	records, err := app.FindRecordsByFilter(
		"services",
		"node_id = {:nodeId} && name = {:name}",
		"",
		10,
		0,
		map[string]any{registryNodeIDParam: node.Id, backupNamePathKey: "custom_dashboard"},
	)
	if err != nil {
		t.Fatalf("find imported service: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("imported services = %d, want 1", len(records))
	}
	if got := records[0].GetString(preCheckStatusField); got != registryObservedState {
		t.Fatalf("status = %q, want observed", got)
	}
}

func TestRegistryImportRejectsForeignStack(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-2", "running")
	node := createStackOperationsTestNode(t, app, stack.Id, "foreign")

	body := map[string]any{
		preCheckStackIDField: stack.Id,
		"server_id":          node.Id,
		backupNamePathKey:    "custom-dashboard",
	}
	event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/import", "owner-1", body)
	if err := (registryRouteHandlers{app: app}).importUnmanagedService(event); err != nil {
		t.Fatalf("import returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func registryRouteTestEvent(method, target, ownerID string, body map[string]any) (*httpx.Event, *httptest.ResponseRecorder) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
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

func TestRegistryMigrateService(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	nodeSource := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")
	nodeTarget := createStackOperationsTestNode(t, app, stack.Id, "worker-1")
	service := createStackOperationsTestService(t, app, nodeSource.Id, "custom-dashboard")
	service.Set(preCheckStatusField, "running")
	if err := app.Save(service); err != nil {
		t.Fatalf("save source service: %v", err)
	}

	body := map[string]any{
		"service_id":       service.Id,
		"target_server_id": nodeTarget.Id,
	}

	event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", body)
	if err := (registryRouteHandlers{app: app}).migrateService(event); err != nil {
		t.Fatalf("migrate service route error: %v", err)
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
	}

	// A missing runtime executor must not mutate the source or manufacture a
	// target service/job that the UI could mistake for a real migration.
	updatedSrc, _ := app.FindRecordById("services", service.Id)
	if got := updatedSrc.GetString("status"); got != registryStatusRunning {
		t.Errorf("source status = %q, want running", got)
	}
	records, err := app.FindRecordsByFilter(
		"services",
		"node_id = {:nodeId} && name = {:name}",
		"",
		10,
		0,
		map[string]any{"nodeId": nodeTarget.Id, "name": "custom-dashboard"},
	)
	if err != nil || len(records) != 0 {
		t.Fatalf("expected no synthetic target service, got %d records and error %v", len(records), err)
	}
}

func TestRegistryMigrateVerifyDeleteServiceUsesStore(t *testing.T) {
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

func TestRegistryMigrateServiceGuards(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	nodeSource := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")
	nodeTarget := createStackOperationsTestNode(t, app, stack.Id, "worker-1")

	t.Run("rejects same server moves", func(t *testing.T) {
		service := createStackOperationsTestService(t, app, nodeSource.Id, "vaultwarden")
		event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", map[string]any{
			"service_id":       service.Id,
			"target_server_id": nodeSource.Id,
		})

		if err := (registryRouteHandlers{app: app}).migrateService(event); err != nil {
			t.Fatalf("migrate service route error: %v", err)
		}
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("rejects unmanaged observed services", func(t *testing.T) {
		service := createStackOperationsTestService(t, app, nodeSource.Id, "custom-dashboard")
		service.Set(preCheckStatusField, registryObservedState)
		service.Set(featureResponseTypeKey, registryCustomService)
		if err := app.Save(service); err != nil { // pocketbase-migration-compat: legacy registry fixture
			t.Fatalf("save observed service: %v", err)
		}
		event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", map[string]any{
			"service_id":       service.Id,
			"target_server_id": nodeTarget.Id,
		})

		if err := (registryRouteHandlers{app: app}).migrateService(event); err != nil {
			t.Fatalf("migrate service route error: %v", err)
		}
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("rejects duplicate active target services", func(t *testing.T) {
		service := createStackOperationsTestService(t, app, nodeSource.Id, "immich")
		createStackOperationsTestService(t, app, nodeTarget.Id, "immich")
		event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", map[string]any{
			"service_id":       service.Id,
			"target_server_id": nodeTarget.Id,
		})

		if err := (registryRouteHandlers{app: app}).migrateService(event); err != nil {
			t.Fatalf("migrate service route error: %v", err)
		}
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("rejects pending rollout services", func(t *testing.T) {
		service := createStackOperationsTestService(t, app, nodeSource.Id, "pending-catalog-app")
		service.Set(preCheckStatusField, preCheckStatusPending)
		if err := app.Save(service); err != nil { // pocketbase-migration-compat: legacy registry fixture
			t.Fatalf("save pending service: %v", err)
		}
		event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/migrate", "owner-1", map[string]any{
			"service_id":       service.Id,
			"target_server_id": nodeTarget.Id,
		})

		if err := (registryRouteHandlers{app: app}).migrateService(event); err != nil {
			t.Fatalf("migrate service route error: %v", err)
		}
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
		}
	})
}

func TestRegistryVerifyService(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	nodeSource := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")
	nodeTarget := createStackOperationsTestNode(t, app, stack.Id, "worker-1")

	srcService := createStackOperationsTestService(t, app, nodeSource.Id, "custom-dashboard")
	srcService.Set(preCheckStatusField, registryStatusMigrating)
	_ = app.Save(srcService)

	targetService := createStackOperationsTestService(t, app, nodeTarget.Id, "custom-dashboard")
	targetService.Set(preCheckStatusField, registryStatusPendingVerification)
	_ = app.Save(targetService)

	body := map[string]any{
		"service_id": targetService.Id,
	}

	event, recorder := registryRouteTestEvent(http.MethodPost, "/api/v1/registry/services/verify", "owner-1", body)
	if err := (registryRouteHandlers{app: app}).verifyService(event); err != nil {
		t.Fatalf("verify service route error: %v", err)
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body=%s, want 501", recorder.Code, recorder.Body.String())
	}

	// A manual button may not promote a synthetic target or archive the source.
	updatedTarget, _ := app.FindRecordById("services", targetService.Id)
	if got := updatedTarget.GetString("status"); got != registryStatusPendingVerification {
		t.Errorf("target status = %q, want pending_verification", got)
	}

	updatedSrc, _ := app.FindRecordById("services", srcService.Id)
	if got := updatedSrc.GetString("status"); got != registryStatusMigrating {
		t.Errorf("source status = %q, want migrating", got)
	}
}

func TestRegistryDeleteService(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	node := createStackOperationsTestNode(t, app, stack.Id, "foundation-1")
	service := createStackOperationsTestService(t, app, node.Id, "custom-dashboard")
	service.Set(preCheckStatusField, registryStatusArchived)
	if saveErr := app.Save(service); saveErr != nil { // pocketbase-migration-compat: legacy registry fixture
		t.Fatalf("save archived service: %v", saveErr)
	}

	event, recorder := registryRouteTestEvent(http.MethodDelete, "/api/v1/registry/services/"+service.Id, "owner-1", nil)
	// mock path param for testing
	event.Request.SetPathValue("id", service.Id)

	if err := (registryRouteHandlers{app: app}).deleteService(event); err != nil {
		t.Fatalf("delete service route error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	// Verify service is deleted
	_, err = app.FindRecordById("services", service.Id)
	if err == nil {
		t.Error("expected service to be deleted, but it still exists")
	}
}
