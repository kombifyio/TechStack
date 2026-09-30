package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/gocommon/apisurface"
	"github.com/kombifyio/techstack/internal/gocommon/apisurface/mcpbind"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kombifyio/techstack/api/surface"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	inventoryMCPCodeField              = "code"
	inventoryMCPDevelopmentVersion     = "dev"
	inventoryMCPErrorField             = "error"
	inventoryMCPGetStackOperationsTool = "get_stack_operations"
	inventoryMCPJSONRPCField           = "jsonrpc"
	inventoryMCPJSONRPCVersion         = "2.0"
	inventoryMCPPrivateCacheScope      = "private"
	// inventoryMCPRequiredCapability names the per-tool entitlement key. The
	// official SDK only carries tool extensions in `_meta`, so each listed tool
	// publishes it there.
	inventoryMCPRequiredCapability = "x-kombify-capability"
	inventoryMCPServerName         = "kombify-techstack"
	inventoryMCPStackIDField       = "stack_id"
	inventoryMCPStatusField        = "status"
	// JSON-RPC server-error codes from the implementation-defined range
	// (-32000..-32019); the MCP-reserved range -32020..-32099 stays untouched.
	inventoryMCPCodeUnauthenticated = -32001
	inventoryMCPCodeForbidden       = -32003
)

// inventoryMCPStackOperationsPath is the canonical Operations route the
// get_stack_operations tool invokes directly.
const inventoryMCPStackOperationsPath = "/api/v1/stacks/{id}/operations"

// inventoryMCPRouteLookup resolves the leaf handler registered for an
// operation's method and path template (httpx.Router.LeafHandler). It is
// consulted per call, so routes registered after the MCP endpoint are found.
type inventoryMCPRouteLookup func(method, template string) (httpx.HandlerFunc, string, bool)

// inventoryMCPSurface is the embedded, generated API surface every tool is
// registered from.
var inventoryMCPSurface = sync.OnceValues(func() (*apisurface.Surface, error) {
	return apisurface.Parse(surface.Raw)
})

func registerInventoryMCPRoutes(r *httpx.Router, h inventoryHandlers) {
	handler := newInventoryMCPHandler(h, r.LeafHandler)
	methodNotAllowed := func(e *httpx.Event) error {
		e.Response.Header().Set("Allow", http.MethodPost)
		return e.NoContent(http.StatusMethodNotAllowed)
	}
	r.POST("/api/v1/mcp", handler)
	r.GET("/api/v1/mcp", methodNotAllowed)
	r.POST("/v1/mcp/public/techstack", handler)
	r.GET("/v1/mcp/public/techstack", methodNotAllowed)
}

// inventoryMCPCaller is the authenticated request context every MCP method
// re-derives from its own HTTP request; the stateless transport keeps no
// connection-scoped authorization state.
type inventoryMCPCaller struct {
	scope inventoryScope
	event *httpx.Event
}

type inventoryMCPCallerKey struct{}

func inventoryMCPCallerFromContext(ctx context.Context) (inventoryMCPCaller, bool) {
	caller, ok := ctx.Value(inventoryMCPCallerKey{}).(inventoryMCPCaller)
	return caller, ok
}

// newInventoryMCPHandler serves the inventory MCP on the official go-sdk in
// stateless mode: MCP 2026-07-28 with server/discover, plus the SDK's legacy
// handling of earlier revisions (initialize-based clients and header-less
// single tools/call requests such as the Gateway product-native backend call).
// Browser origins and unauthenticated callers are rejected before the SDK
// parses the body.
func newInventoryMCPHandler(h inventoryHandlers, routes inventoryMCPRouteLookup) httpx.HandlerFunc {
	server := h.newInventoryMCPServer(routes)
	transport := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
	})
	return func(e *httpx.Event) error {
		if !validMCPOrigin(e.Request) {
			return writeMCPHTTPError(e, http.StatusForbidden, inventoryMCPCodeForbidden, "Forbidden", "origin_not_allowed")
		}
		scope, err := inventoryScopeFromEvent(e)
		if err != nil {
			return writeMCPAuthError(e, err)
		}
		ctx := context.WithValue(e.Request.Context(), inventoryMCPCallerKey{}, inventoryMCPCaller{scope: scope, event: e})
		transport.ServeHTTP(e.Response, e.Request.WithContext(ctx))
		return nil
	}
}

func (h inventoryHandlers) newInventoryMCPServer(routes inventoryMCPRouteLookup) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    inventoryMCPServerName,
		Title:   "Kombify Techstack",
		Version: firstNonEmptyString(h.version, inventoryMCPDevelopmentVersion),
	}, &mcpsdk.ServerOptions{
		Instructions: "Policy-scoped Techstack tools generated from the OpenAPI contract: reads (techstack.inventory.read), writes and validations (techstack.inventory.write), confirmation-gated runtime operations (techstack.inventory.operate) and cost-bearing provisioning (techstack.inventory.provision). Tenant and subject identity are derived from authenticated context and must never be supplied as tool arguments.",
		Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}},
	})
	// The catalog is the embedded, generated API surface; a build that cannot
	// bind every exposed operation fails at route registration instead of
	// serving a partial catalog.
	s, err := inventoryMCPSurface()
	if err != nil {
		panic(fmt.Errorf("inventory MCP surface: %w", err))
	}
	handlers := h.inventoryMCPApplicationHandlers(routes)
	applicationBound := make(map[string]bool, len(handlers))
	for operationID := range handlers {
		applicationBound[operationID] = true
	}
	for i := range s.Operations {
		op := &s.Operations[i]
		if op.MCP != nil && handlers[op.OperationID] == nil {
			handlers[op.OperationID] = inventoryMCPRouteTool(op, s.Envelope, routes)
		}
	}
	if err := mcpbind.Register(server, s, mcpbind.Options{
		Handlers:  handlers,
		Authorize: h.authorizeInventoryMCPTool(applicationBound),
		Meta: func(op *apisurface.Operation) mcpsdk.Meta {
			return mcpsdk.Meta{inventoryMCPRequiredCapability: op.MCP.RequiredCapability}
		},
	}); err != nil {
		panic(fmt.Errorf("inventory MCP tools: %w", err))
	}
	server.AddReceivingMiddleware(h.authorizeInventoryMCPDiscovery)
	return server
}

// inventoryMCPApplicationHandlers binds the reviewed inventory tools to their
// application operations, which authorize the exact resource themselves.
func (h inventoryHandlers) inventoryMCPApplicationHandlers(routes inventoryMCPRouteLookup) map[string]mcpbind.Handler {
	serverRead := func(read func(context.Context, inventoryScope, string) (any, error)) inventoryMCPApplicationCall {
		return func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			if err := validateMCPArguments(arguments, map[string]bool{inventoryServerIDField: true}, map[string]bool{inventoryServerIDField: true}); err != nil {
				return nil, err
			}
			return read(ctx, caller.scope, stringArgument(arguments, inventoryServerIDField))
		}
	}
	calls := map[string]inventoryMCPApplicationCall{
		"listInventoryServers": func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			page, err := inventoryMCPPage(arguments, map[string]bool{inventoryCursorField: true, inventoryLimitField: true})
			if err != nil {
				return nil, err
			}
			return h.app.listServers(ctx, caller.scope, page)
		},
		"getInventoryServerHealth": serverRead(func(ctx context.Context, scope inventoryScope, serverID string) (any, error) {
			return h.app.serverHealth(ctx, scope, serverID)
		}),
		"getInventoryServerPorts": serverRead(func(ctx context.Context, scope inventoryScope, serverID string) (any, error) {
			return h.app.serverPorts(ctx, scope, serverID)
		}),
		"getInventoryServerAccessContext": serverRead(func(ctx context.Context, scope inventoryScope, serverID string) (any, error) {
			return h.app.serverAccessContext(ctx, scope, serverID)
		}),
		"listInventoryServices": func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			page, err := inventoryMCPPage(arguments, map[string]bool{inventoryServerIDField: true, inventoryCursorField: true, inventoryLimitField: true})
			if err != nil {
				return nil, err
			}
			return h.app.listServices(ctx, caller.scope, stringArgument(arguments, inventoryServerIDField), page)
		},
		"renameInventoryServer": func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			allowed := map[string]bool{inventoryServerIDField: true, inventoryDisplayNameField: true}
			if err := validateMCPArguments(arguments, allowed, map[string]bool{inventoryServerIDField: true}); err != nil {
				return nil, err
			}
			// An empty display_name clears the rename, so presence is checked
			// instead of a non-empty value.
			if _, ok := arguments[inventoryDisplayNameField]; !ok {
				return nil, inventoryValidationError("display_name_required", "Required tool argument missing")
			}
			displayName, _ := arguments[inventoryDisplayNameField].(string)
			return h.app.renameServer(ctx, caller.scope, stringArgument(arguments, inventoryServerIDField), displayName)
		},
		"renameHomelab": func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			if err := validateMCPArguments(arguments, map[string]bool{inventoryHomelabNameField: true}, map[string]bool{inventoryHomelabNameField: true}); err != nil {
				return nil, err
			}
			name, _ := arguments[inventoryHomelabNameField].(string)
			return h.app.renameHomelab(ctx, caller.scope, name)
		},
		"getStackOperations": func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error) {
			stackID, err := inventoryMCPStackID(arguments)
			if err != nil {
				return nil, err
			}
			var stackOperations httpx.HandlerFunc
			if routes != nil {
				stackOperations, _, _ = routes(http.MethodGet, inventoryMCPStackOperationsPath)
			}
			return h.callStackOperationsTool(ctx, caller.scope, stackID, stackOperations, caller.event)
		},
	}
	handlers := make(map[string]mcpbind.Handler, len(calls))
	for operationID, call := range calls {
		handlers[operationID] = inventoryMCPApplicationHandler(call)
	}
	return handlers
}

type inventoryMCPApplicationCall func(ctx context.Context, caller inventoryMCPCaller, arguments map[string]any) (any, error)

func inventoryMCPApplicationHandler(call inventoryMCPApplicationCall) mcpbind.Handler {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, arguments map[string]any) (any, error) {
		caller, ok := inventoryMCPCallerFromContext(ctx)
		if !ok {
			return nil, inventoryMCPFailure(inventoryMCPAuthenticationRequired())
		}
		result, err := call(ctx, caller, arguments)
		if err != nil {
			return nil, inventoryMCPFailure(err)
		}
		return result, nil
	}
}

// authorizeInventoryMCPTool is the pre-handler authorization hook. Tools bound
// to an application operation are authorized there against their exact
// resource; every route-invoked tool needs the inventory action its required
// capability names on the tenant tool surface before its route handler runs.
func (h inventoryHandlers) authorizeInventoryMCPTool(applicationBound map[string]bool) func(context.Context, *apisurface.Operation) error {
	return func(ctx context.Context, op *apisurface.Operation) error {
		if applicationBound[op.OperationID] {
			return nil
		}
		caller, ok := inventoryMCPCallerFromContext(ctx)
		if !ok {
			return inventoryMCPFailure(inventoryMCPAuthenticationRequired())
		}
		action, ok := inventoryMCPActionForCapability(op.MCP.RequiredCapability)
		if !ok {
			return inventoryMCPFailure(&inventoryError{status: http.StatusForbidden, reasonCode: "inventory_access_denied", message: "Inventory access denied"})
		}
		if _, err := h.app.authorize(ctx, caller.scope, action, controlplane.InventoryReadTargetTools, ""); err != nil {
			return inventoryMCPFailure(err)
		}
		return nil
	}
}

func inventoryMCPActionForCapability(capability string) (InventoryAction, bool) {
	switch capability {
	case InventoryEntitlementRead:
		return InventoryActionRead, true
	case InventoryEntitlementWrite:
		return InventoryActionWrite, true
	case InventoryEntitlementOperate:
		return InventoryActionOperate, true
	case InventoryEntitlementProvision:
		return InventoryActionProvision, true
	default:
		return "", false
	}
}

func inventoryMCPAuthenticationRequired() *inventoryError {
	return &inventoryError{status: http.StatusUnauthorized, reasonCode: "authentication_required", message: "Authentication required"}
}

// authorizeInventoryMCPDiscovery applies the inventory tool-catalog policy to
// discovery (server/discover and tools/list), so a principal the policy denies
// learns nothing, and marks the principal-gated results as privately cacheable.
func (h inventoryHandlers) authorizeInventoryMCPDiscovery(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
		if method != "server/discover" && method != "tools/list" {
			return next(ctx, method, request)
		}
		caller, ok := inventoryMCPCallerFromContext(ctx)
		if !ok {
			return nil, inventoryMCPDenial(&inventoryError{status: http.StatusUnauthorized, reasonCode: "authentication_required", message: "Authentication required"})
		}
		if _, err := h.app.authorize(ctx, caller.scope, InventoryActionRead, controlplane.InventoryReadTargetTools, ""); err != nil {
			return nil, inventoryMCPDenial(err)
		}
		result, err := next(ctx, method, request)
		switch typed := result.(type) {
		case *mcpsdk.ListToolsResult:
			typed.CacheScope = inventoryMCPPrivateCacheScope
		case *mcpsdk.DiscoverResult:
			typed.CacheScope = inventoryMCPPrivateCacheScope
		}
		return result, err
	}
}

func inventoryMCPPage(arguments map[string]any, allowed map[string]bool) (inventoryPageOptions, error) {
	if err := validateMCPArguments(arguments, allowed, nil); err != nil {
		return inventoryPageOptions{}, err
	}
	return inventoryPageFromMCPArguments(arguments)
}

func inventoryMCPStackID(arguments map[string]any) (string, error) {
	if err := validateMCPArguments(arguments, map[string]bool{inventoryMCPStackIDField: true}, map[string]bool{inventoryMCPStackIDField: true}); err != nil {
		return "", err
	}
	stackID := stringArgument(arguments, inventoryMCPStackIDField)
	if len(stackID) > 256 {
		return "", &inventoryError{status: http.StatusBadRequest, reasonCode: "stack_id_invalid", message: "Stack ID is invalid"}
	}
	return stackID, nil
}

func (h inventoryHandlers) callStackOperationsTool(ctx context.Context, scope inventoryScope, stackID string, stackOperations httpx.HandlerFunc, sourceEvent *httpx.Event) (any, error) {
	if _, err := h.app.authorize(ctx, scope, InventoryActionRead, controlplane.InventoryReadTargetServerCollection, ""); err != nil {
		return nil, err
	}
	if stackOperations == nil {
		return nil, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "stack_operations_unavailable", message: "Stack operations unavailable"}
	}

	target := "/api/v1/stacks/" + url.PathEscape(stackID) + "/operations"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "stack_operations_unavailable", message: "Stack operations unavailable", cause: err}
	}
	request.SetPathValue("id", stackID)
	if sourceEvent != nil && sourceEvent.Request != nil {
		request.Header.Set("X-Request-ID", sourceEvent.Request.Header.Get("X-Request-ID"))
		request.Host = sourceEvent.Request.Host
		request.RemoteAddr = sourceEvent.Request.RemoteAddr
	}
	request.Header.Set("Accept", "application/json")

	recorder := httptest.NewRecorder()
	operationEvent := &httpx.Event{Request: request, Response: recorder}
	if sourceEvent != nil {
		operationEvent.Auth = sourceEvent.Auth
	}
	if err := stackOperations(operationEvent); err != nil {
		var apiErr *httpx.APIError
		if errors.As(err, &apiErr) {
			return nil, inventoryMCPStackOperationsError(apiErr.Status, apiErr.Message, apiErr.Details, nil)
		}
		return nil, inventoryMCPStackOperationsError(http.StatusServiceUnavailable, "Stack operations unavailable", nil, err)
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		return nil, inventoryMCPStackOperationsError(http.StatusServiceUnavailable, "Stack operations unavailable", nil, err)
	}
	if recorder.Code != http.StatusOK {
		return nil, inventoryMCPStackOperationsError(recorder.Code, envelope.Error.Message, envelope.Error.Details, nil)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, inventoryMCPStackOperationsError(http.StatusServiceUnavailable, "Stack operations unavailable", nil, nil)
	}
	var result map[string]any
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return nil, inventoryMCPStackOperationsError(http.StatusServiceUnavailable, "Stack operations unavailable", nil, err)
	}
	return result, nil
}

func inventoryMCPStackOperationsError(status int, message string, details any, cause error) *inventoryError {
	reason := "stack_operations_unavailable"
	if values, ok := details.(map[string]any); ok {
		if value, ok := values[inventoryReasonCodeField].(string); ok && strings.TrimSpace(value) != "" {
			reason = strings.TrimSpace(value)
		}
	}
	if strings.TrimSpace(message) == "" {
		message = "Stack operations unavailable"
	}
	return &inventoryError{status: status, reasonCode: reason, message: strings.TrimSpace(message), cause: cause}
}

func validateMCPArguments(arguments map[string]any, allowed, required map[string]bool) error {
	for key, value := range arguments {
		if allowed == nil || !allowed[key] {
			return &inventoryError{status: http.StatusBadRequest, reasonCode: "unsupported_tool_argument", message: "Unsupported tool argument"}
		}
		if key == inventoryLimitField {
			number, ok := value.(float64)
			if !ok || number != float64(int(number)) || number <= 0 || number > controlplane.MaxInventoryPageSize {
				return &inventoryError{status: http.StatusBadRequest, reasonCode: "invalid_page_limit", message: "Inventory page limit is invalid"}
			}
			continue
		}
		if _, ok := value.(string); !ok {
			return &inventoryError{status: http.StatusBadRequest, reasonCode: "invalid_tool_argument", message: "Invalid tool argument"}
		}
	}
	for key := range required {
		if stringArgument(arguments, key) == "" {
			return &inventoryError{status: http.StatusBadRequest, reasonCode: key + "_required", message: "Required tool argument missing"}
		}
	}
	return nil
}

func inventoryPageFromMCPArguments(arguments map[string]any) (inventoryPageOptions, error) {
	options := inventoryPageOptions{Limit: controlplane.DefaultInventoryPageSize, Cursor: stringArgument(arguments, inventoryCursorField)}
	if len(options.Cursor) > maxInventoryCursor {
		return inventoryPageOptions{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	if raw, ok := arguments[inventoryLimitField].(float64); ok {
		options.Limit = int(raw)
	}
	return options, nil
}

func stringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}

func validMCPOrigin(request *http.Request) bool {
	// Product UI uses the REST inventory. The MCP endpoint is deliberately
	// headless/server-to-server only, so any browser Origin is denied. This is
	// fail-closed for DNS rebinding and avoids trusting forwarded host headers.
	return strings.TrimSpace(request.Header.Get("Origin")) == ""
}

// inventoryMCPToolFailure keeps the stable denial envelope in the tool result
// (mcpbind structured error content) so an agent sees the reason instead of a
// transport failure, without exposing an internal cause in the message.
type inventoryMCPToolFailure struct {
	message string
	payload map[string]any
}

func (f *inventoryMCPToolFailure) Error() string { return f.message }

// ToolErrorPayload is the structured content mcpbind attaches to the result.
func (f *inventoryMCPToolFailure) ToolErrorPayload() map[string]any { return f.payload }

func inventoryMCPFailure(err error) error {
	status, code, reason, message := inventoryErrorContract(err)
	return &inventoryMCPToolFailure{message: message, payload: map[string]any{inventoryMCPErrorField: map[string]any{
		inventoryMCPCodeField: code, inventoryMCPStatusField: status, inventoryReasonCodeField: reason,
	}}}
}

// inventoryMCPDenial is the protocol-level form of the same denial envelope for
// discovery methods, which have no tool result to carry it.
func inventoryMCPDenial(err error) error {
	status, _, reason, message := inventoryErrorContract(err)
	code := int64(jsonrpc.CodeInternalError)
	switch status {
	case http.StatusUnauthorized:
		code = inventoryMCPCodeUnauthenticated
	case http.StatusForbidden:
		code = inventoryMCPCodeForbidden
	}
	return &jsonrpc.Error{Code: code, Message: message, Data: inventoryMCPErrorData(status, reason)}
}

func inventoryMCPErrorData(status int, reason string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{inventoryMCPStatusField: status, inventoryReasonCodeField: reason})
	return data
}

func inventoryErrorContract(err error) (int, string, string, string) {
	var inventoryErr *inventoryError
	if !errors.As(err, &inventoryErr) {
		return http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", "inventory_unavailable", "Inventory unavailable"
	}
	switch inventoryErr.status {
	case http.StatusNotFound:
		return http.StatusNotFound, "NOT_FOUND", inventoryErr.reasonCode, inventoryErr.message
	case http.StatusBadRequest:
		return http.StatusBadRequest, "VALIDATION_FAILED", inventoryErr.reasonCode, inventoryErr.message
	case http.StatusUnauthorized:
		return http.StatusUnauthorized, "UNAUTHENTICATED", inventoryErr.reasonCode, inventoryErr.message
	case http.StatusForbidden:
		return http.StatusForbidden, "FORBIDDEN", inventoryErr.reasonCode, inventoryErr.message
	default:
		return http.StatusServiceUnavailable, "UPSTREAM_UNAVAILABLE", inventoryErr.reasonCode, "Inventory unavailable"
	}
}

func writeMCPAuthError(e *httpx.Event, err error) error {
	status, _, reason, message := inventoryErrorContract(err)
	code := inventoryMCPCodeUnauthenticated
	if status == http.StatusForbidden {
		code = inventoryMCPCodeForbidden
	}
	return writeMCPHTTPError(e, status, code, message, reason)
}

// writeMCPHTTPError rejects a request before the MCP transport parses it, so
// the JSON-RPC id is unknown and stays null.
func writeMCPHTTPError(e *httpx.Event, status, code int, message, reason string) error {
	return e.JSON(status, map[string]any{
		inventoryMCPJSONRPCField: inventoryMCPJSONRPCVersion, "id": nil,
		inventoryMCPErrorField: map[string]any{inventoryMCPCodeField: code, routeMessageField: message, "data": map[string]any{inventoryMCPStatusField: status, inventoryReasonCodeField: reason}},
	})
}
