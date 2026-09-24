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
)

func (h inventoryHandlers) handleMCP(e *httpx.Event) error {
	return h.handleMCPWithStackOperations(e, nil)
}

func TestInventoryMCPAdvertisesAnnotatedStackOperationsTool(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newTestInventoryHandlers(store, time.Now)
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", "owner-1", "tenant-1", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	type advertisedTool struct {
		Name               string         `json:"name"`
		RequiredCapability string         `json:"x-kombify-capability"`
		InputSchema        map[string]any `json:"inputSchema"`
		Annotations        map[string]any `json:"annotations"`
	}
	var response struct {
		Result struct {
			Tools []advertisedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var operationsTool *advertisedTool
	for index := range response.Result.Tools {
		if response.Result.Tools[index].Name == inventoryMCPGetStackOperationsTool {
			operationsTool = &response.Result.Tools[index]
			break
		}
	}
	if operationsTool == nil {
		t.Fatalf("tools/list did not advertise %s: %#v", inventoryMCPGetStackOperationsTool, response.Result.Tools)
	}
	if operationsTool.RequiredCapability != "techstack.inventory.read" || operationsTool.Annotations["readOnlyHint"] != true || operationsTool.Annotations["idempotentHint"] != true || operationsTool.Annotations["destructiveHint"] != false || operationsTool.Annotations["openWorldHint"] != false {
		t.Fatalf("stack operations tool = %#v", operationsTool)
	}
	properties, _ := operationsTool.InputSchema["properties"].(map[string]any)
	if properties["tenant_id"] != nil || properties["owner_id"] != nil {
		t.Fatalf("stack operations tool accepts scope override: %#v", operationsTool)
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
	RegisterStackOperationsRoutesWithStores(router, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
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
	if readiness["required_servers"] != float64(1) || readiness["connected_servers"] != float64(0) || readiness["can_start"] != false {
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
	RegisterStackOperationsRoutesWithStores(router, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
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
	RegisterStackOperationsRoutesWithStores(registered, nil, MonitoringStatusMetadata{}, nil, nil, StackOperationsRouteStores{
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
