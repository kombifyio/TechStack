package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// handleMCP serves one request through the SDK-backed MCP handler without a
// router-registered stack operations handler.
func (h inventoryHandlers) handleMCP(e *httpx.Event) error {
	return h.handleMCPWithStackOperations(e, nil)
}

func (h inventoryHandlers) handleMCPWithStackOperations(e *httpx.Event, stackOperations httpx.HandlerFunc) error {
	setMCPTestHeaders(e.Request)
	routes := func(method, template string) (httpx.HandlerFunc, string, bool) {
		if stackOperations == nil || method != http.MethodGet || template != inventoryMCPStackOperationsPath {
			return nil, "", false
		}
		return stackOperations, inventoryMCPStackOperationsPath, true
	}
	return newInventoryMCPHandler(h, routes)(e)
}

// mcpTestEvent is a Streamable HTTP POST as a conforming client sends it.
func mcpTestEvent(ownerID, tenantID string, body map[string]any) (*httpx.Event, *httptest.ResponseRecorder) {
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/mcp", ownerID, tenantID, body)
	setMCPTestHeaders(event.Request)
	return event, recorder
}

func setMCPTestHeaders(request *http.Request) {
	if request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json, text/event-stream")
	}
}

func TestInventoryMCPServesStatelessDiscoveryToOfficialClient(t *testing.T) {
	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: store, Policy: NewSelfHostedInventoryPolicy(), Now: time.Now, Version: "test"})

	var mu sync.Mutex
	var methods []string
	sessionIssued := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Header.Get("Mcp-Method"))
		mu.Unlock()
		// Stands in for the edge/session middleware that authenticates the caller.
		authenticated := r.WithContext(identity.NewContext(r.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
		router.ServeHTTP(w, authenticated)
		if w.Header().Get("Mcp-Session-Id") != "" {
			mu.Lock()
			sessionIssued = true
			mu.Unlock()
		}
	}))
	defer server.Close()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "techstack-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: server.URL + "/api/v1/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if version := session.InitializeResult().ProtocolVersion; version != "2026-07-28" {
		t.Fatalf("negotiated protocol = %q, want 2026-07-28 via server/discover", version)
	}
	var capability any
	for _, tool := range tools.Tools {
		if tool.Name == inventoryMCPGetStackOperationsTool {
			capability = tool.Meta[inventoryMCPRequiredCapability]
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 || methods[0] != "server/discover" || strings.Contains(strings.Join(methods, ","), "initialize") || sessionIssued {
		t.Fatalf("transport was not stateless discovery: methods=%v sessionIssued=%v", methods, sessionIssued)
	}
	if capability != "techstack.inventory.read" || tools.CacheScope != inventoryMCPPrivateCacheScope {
		t.Fatalf("tools/list capability=%v cacheScope=%q", capability, tools.CacheScope)
	}
}

func TestInventoryMCPAdvertisesAnnotatedStackOperationsTool(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newTestInventoryHandlers(store, time.Now)
	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	type advertisedTool struct {
		Name        string         `json:"name"`
		Meta        map[string]any `json:"_meta"`
		InputSchema map[string]any `json:"inputSchema"`
		Annotations map[string]any `json:"annotations"`
	}
	var response struct {
		Result struct {
			Tools []advertisedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("tools/list = %d %s: %v", recorder.Code, recorder.Body.String(), err)
	}
	var operationsTool *advertisedTool
	for index := range response.Result.Tools {
		if response.Result.Tools[index].Name == inventoryMCPGetStackOperationsTool {
			operationsTool = &response.Result.Tools[index]
			break
		}
	}
	if operationsTool == nil {
		t.Fatalf("tools/list did not advertise %s: %s", inventoryMCPGetStackOperationsTool, recorder.Body.String())
	}
	if operationsTool.Meta[inventoryMCPRequiredCapability] != "techstack.inventory.read" || operationsTool.Annotations["readOnlyHint"] != true || operationsTool.Annotations["idempotentHint"] != true || operationsTool.Annotations["destructiveHint"] != false || operationsTool.Annotations["openWorldHint"] != false {
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

	mcpEvent, mcpRecorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
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

	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
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
	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
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
	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
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
	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
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
	event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "list_servers", "arguments": map[string]any{"tenant_id": "tenant-2"}},
	})
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	var scoped struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &scoped); err != nil || !scoped.Result.IsError {
		t.Fatalf("scope override result = %s", recorder.Body.String())
	}

	event, recorder = mcpTestEvent("owner-1", "tenant-1", map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/list"})
	event.Request.Header.Set("Origin", "https://attacker.example")
	if err := h.handleMCP(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "origin_not_allowed") {
		t.Fatalf("cross-origin status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

// A derived contract tool reaches its registered route handler directly, with
// the caller's verified identity and the path value bound to the route's own
// wildcard name; a caller the inventory policy denies never reaches it. A
// cost-bearing tool needs the signed provision capability: an operate grant
// alone never reaches its route.
func TestInventoryMCPDerivedToolInvokesRouteHandlerWithCallerScope(t *testing.T) {
	fga := NewInventoryFGAPolicy(&fakeInventoryRelationshipChecker{allowed: true})
	for _, tc := range []struct {
		name         string
		policy       InventoryPolicy
		entitlements []string
		method, path string
		tool         string
		arguments    map[string]any
		allowed      bool
	}{
		{name: "granted", policy: NewSelfHostedInventoryPolicy(), method: http.MethodGet, path: "/api/v1/jobs/{jobId}", tool: "get_job", arguments: map[string]any{"id": "job-7"}, allowed: true},
		{name: "denied", policy: denyInventoryPolicy{}, method: http.MethodGet, path: "/api/v1/jobs/{jobId}", tool: "get_job", arguments: map[string]any{"id": "job-7"}},
		{name: "provision without provision grant", policy: fga, entitlements: []string{InventoryEntitlementOperate}, method: http.MethodPost, path: "/api/v1/stacks/{stackId}/provision", tool: "provision_stack", arguments: map[string]any{"stack_id": "job-7"}},
		{name: "provision granted", policy: fga, entitlements: []string{InventoryEntitlementProvision}, method: http.MethodPost, path: "/api/v1/stacks/{stackId}/provision", tool: "provision_stack", arguments: map[string]any{"stack_id": "job-7"}, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := httpx.NewRouter()
			RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: controlplane.NewMemoryStore(), Policy: tc.policy, Now: time.Now, Version: "test"})
			var seenOwner, seenTenant string
			called := false
			router.Route(tc.method, tc.path, func(e *httpx.Event) error {
				called = true
				if caller := identity.FromContext(e.Request.Context()); caller != nil {
					seenOwner, seenTenant = caller.UserID, caller.OrgID
				}
				id := e.Request.PathValue("jobId") + e.Request.PathValue("stackId")
				return httpx.Success(e, http.StatusOK, map[string]any{"id": id, "state": "running"})
			})

			event, recorder := mcpTestEvent("owner-1", "tenant-1", map[string]any{
				"jsonrpc": "2.0", "id": 9, "method": "tools/call",
				"params": map[string]any{"name": tc.tool, "arguments": tc.arguments},
			})
			request := event.Request
			if tc.entitlements != nil {
				request = request.WithContext(middleware.WithSignedEntitlements(request.Context(), tc.entitlements...))
			}
			router.ServeHTTP(recorder, request)
			var response struct {
				Result struct {
					IsError           bool           `json:"isError"`
					StructuredContent map[string]any `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("tools/call = %d %s: %v", recorder.Code, recorder.Body.String(), err)
			}
			if !tc.allowed {
				errorBody, _ := response.Result.StructuredContent["error"].(map[string]any)
				if called || !response.Result.IsError || errorBody["status"] != float64(http.StatusForbidden) {
					t.Fatalf("denied derived tool: called=%v result=%s", called, recorder.Body.String())
				}
				return
			}
			if response.Result.IsError || !called || seenOwner != "owner-1" || seenTenant != "tenant-1" {
				t.Fatalf("derived tool: called=%v owner=%q tenant=%q result=%s", called, seenOwner, seenTenant, recorder.Body.String())
			}
		})
	}
}
