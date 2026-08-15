package routes

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/tests"
)

func TestInventoryMCPListsExactlyFiveAnnotatedTools(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newTestInventoryHandlers(store, time.Now)
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name               string         `json:"name"`
				RequiredCapability string         `json:"x-kombify-capability"`
				InputSchema        map[string]any `json:"inputSchema"`
				Annotations        map[string]any `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := []string{"list_servers", "server_health", "list_services", "server_access_context", inventoryMCPGetStackOperationsTool}
	wantCapability := []string{"techstack.inventory.read", "techstack.inventory.read", "techstack.inventory.read", "techstack.inventory.operate", "techstack.inventory.read"}
	if len(response.Result.Tools) != len(want) {
		t.Fatalf("tools = %#v", response.Result.Tools)
	}
	for index, tool := range response.Result.Tools {
		if tool.Name != want[index] || tool.Annotations["readOnlyHint"] != true || tool.Annotations["idempotentHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("tool[%d] = %#v", index, tool)
		}
		if tool.RequiredCapability != wantCapability[index] {
			t.Fatalf("tool %s capability = %q, want %q", tool.Name, tool.RequiredCapability, wantCapability[index])
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		if properties["tenant_id"] != nil || properties["owner_id"] != nil {
			t.Fatalf("tool accepts scope override: %#v", tool)
		}
	}
}

func TestInventoryMCPStackOperationsDelegatesToCanonicalHTTPReadModel(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack one", Status: "pending",
		Config: map[string]any{"min_servers": 1},
	}); err != nil {
		t.Fatal(err)
	}

	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	RegisterStackOperationsRoutesWithStores(router, nil, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
		Stacks: store, Servers: store, Services: store, Workers: store, Registry: store, Jobs: store,
	}, fakeManagedRuntimeLeaseLister{})

	directEvent, directRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/stacks/stack-1/operations", "owner-1", "tenant-1", nil)
	router.ServeHTTP(directRecorder, directEvent.Request)
	if directRecorder.Code != http.StatusOK {
		t.Fatalf("direct operations = %d %s", directRecorder.Code, directRecorder.Body.String())
	}
	var direct struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(directRecorder.Body.Bytes(), &direct); err != nil {
		t.Fatal(err)
	}

	mcpEvent, mcpRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": "stack-1"}},
	})
	router.ServeHTTP(mcpRecorder, mcpEvent.Request)
	if mcpRecorder.Code != http.StatusOK {
		t.Fatalf("MCP operations = %d %s", mcpRecorder.Code, mcpRecorder.Body.String())
	}
	var mcp struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
			IsError           bool           `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpRecorder.Body.Bytes(), &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp.Result.IsError || !reflect.DeepEqual(mcp.Result.StructuredContent, direct.Data) {
		t.Fatalf("MCP operations differ from canonical HTTP data\nMCP: %#v\nHTTP: %#v", mcp.Result.StructuredContent, direct.Data)
	}
	readiness, _ := mcp.Result.StructuredContent["readiness"].(map[string]any)
	for _, field := range []string{"status", "can_start", "required_servers", "approved_servers", "connected_servers", "pending_servers", "assigned_servers", "available_servers", "unassigned_servers", "message", "review_required"} {
		if _, ok := readiness[field]; !ok {
			t.Fatalf("MCP readiness is missing %s: %#v", field, readiness)
		}
	}
	if readiness["connected_servers"] != float64(0) || readiness["can_start"] != false {
		t.Fatalf("MCP readiness = %#v", readiness)
	}
}

func TestInventoryMCPStackOperationsPreservesOwnerFence(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "foreign-stack", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign stack"}); err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	RegisterStackOperationsRoutesWithStores(router, nil, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
		Stacks: store, Servers: store, Services: store, Workers: store, Registry: store, Jobs: store,
	}, fakeManagedRuntimeLeaseLister{})

	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": "foreign-stack"}},
	})
	router.ServeHTTP(recorder, event.Request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"isError":true`) || !strings.Contains(recorder.Body.String(), `"status":403`) || strings.Contains(recorder.Body.String(), "Foreign stack") {
		t.Fatalf("foreign stack MCP result = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInventoryMCPStackOperationsLegacyFallbackDoesNotCrossTenantBoundary(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("name", "Tenant B MCP private stack")
	stack.Set("tenant_id", "tenant-b")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save tenant B stack: %v", err)
	}

	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	RegisterStackOperationsRoutesWithStores(router, app, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{Stacks: store}, fakeManagedRuntimeLeaseLister{})
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-a", map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": stack.Id}},
	})
	router.ServeHTTP(recorder, event.Request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"isError":true`) || !strings.Contains(recorder.Body.String(), `"status":404`) {
		t.Fatalf("cross-tenant MCP result = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Tenant B MCP private stack") {
		t.Fatalf("cross-tenant legacy stack leaked through MCP: %s", recorder.Body.String())
	}
}

func TestInventoryMCPStackOperationsLegacyWorkersStayTenantScoped(t *testing.T) {
	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	ensureStackOperationsTestCollections(t, app)

	stack := createStackOperationsTestStack(t, app, "owner-1", "running")
	stack.Set("tenant_id", "tenant-a")
	if err := app.Save(stack); err != nil {
		t.Fatalf("save tenant A stack: %v", err)
	}
	workerA := createStackOperationsTestWorker(t, app, "owner-1", "", "tenant-a-mcp-worker", true)
	workerA.Set("tenant_id", "tenant-a")
	if err := app.Save(workerA); err != nil {
		t.Fatalf("save tenant A worker: %v", err)
	}
	workerB := createStackOperationsTestWorker(t, app, "owner-1", "", "tenant-b-mcp-worker", true)
	workerB.Set("tenant_id", "tenant-b")
	if err := app.Save(workerB); err != nil {
		t.Fatalf("save tenant B worker: %v", err)
	}
	createStackOperationsTestWorker(t, app, "owner-1", "", "tenantless-mcp-worker", true)

	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	RegisterStackOperationsRoutesWithStores(router, app, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{Stacks: store}, fakeManagedRuntimeLeaseLister{})
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-a", map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": stack.Id}},
	})
	router.ServeHTTP(recorder, event.Request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || strings.Contains(body, `"isError":true`) || !strings.Contains(body, "tenant-a-mcp-worker") {
		t.Fatalf("tenant A MCP operations = %d %s", recorder.Code, body)
	}
	if strings.Contains(body, "tenant-b-mcp-worker") || strings.Contains(body, "tenantless-mcp-worker") {
		t.Fatalf("cross-tenant or tenantless worker leaked through MCP: %s", body)
	}
}

func TestInventoryMCPStackOperationsRequiresInventoryReadPolicy(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := inventoryHandlers{app: &inventoryApplication{read: store, policy: denyInventoryPolicy{}, now: time.Now}, version: "test"}
	called := false
	stackOperations := func(*httpx.Event) error {
		called = true
		return nil
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": "stack-1"}},
	})
	if err := h.handleMCPWithStackOperations(event, stackOperations); err != nil {
		t.Fatal(err)
	}
	if called || recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"isError":true`) || !strings.Contains(recorder.Body.String(), `"status":403`) || !strings.Contains(recorder.Body.String(), "inventory_access_denied") {
		t.Fatalf("denied stack operations = called:%v status:%d body:%s", called, recorder.Code, recorder.Body.String())
	}
}

func TestInventoryMCPStackOperationsHandlerIsRouterScopedAndMissingFailsClosed(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack one"}); err != nil {
		t.Fatal(err)
	}

	registered := httpx.NewRouter()
	RegisterInventoryRoutes(registered, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	RegisterStackOperationsRoutesWithStores(registered, nil, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
		Stacks: store, Servers: store, Services: store, Workers: store, Registry: store, Jobs: store,
	}, fakeManagedRuntimeLeaseLister{})

	unregistered := httpx.NewRouter()
	RegisterInventoryRoutes(unregistered, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "tools/call",
		"params": map[string]any{"name": inventoryMCPGetStackOperationsTool, "arguments": map[string]any{"stack_id": "stack-1"}},
	})
	unregistered.ServeHTTP(recorder, event.Request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"isError":true`) || !strings.Contains(recorder.Body.String(), `"status":503`) || !strings.Contains(recorder.Body.String(), "stack_operations_unavailable") || strings.Contains(recorder.Body.String(), "Stack one") {
		t.Fatalf("unregistered router stack operations = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInventoryMCPForeignServerReturnsGenericToolNotFound(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "foreign", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	h := newTestInventoryHandlers(store, time.Now)
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "server_health", "arguments": map[string]any{"server_id": "foreign"}},
	})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "owner-2") || !strings.Contains(recorder.Body.String(), `"isError":true`) || !strings.Contains(recorder.Body.String(), `"status":404`) || !strings.Contains(recorder.Body.String(), `"reason_code":"server_not_found"`) {
		t.Fatalf("foreign MCP result = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInventoryMCPRejectsScopeArgumentsAndCrossOrigin(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newTestInventoryHandlers(store, time.Now)
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "list_servers", "arguments": map[string]any{"tenant_id": "tenant-2"}},
	})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `"status":400`) || !strings.Contains(recorder.Body.String(), "unsupported_tool_argument") {
		t.Fatalf("scope override result = %s", recorder.Body.String())
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{"jsonrpc": "2.0", "id": 4, "method": "ping"})
	event.Request.Header.Set("Origin", "https://attacker.example")
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "origin_not_allowed") {
		t.Fatalf("cross-origin status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}
