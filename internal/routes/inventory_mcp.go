package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/api/toolmanifest"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	inventoryMCPAnnotationsField       = "annotations"
	inventoryMCPCapabilitiesField      = "capabilities"
	inventoryMCPCodeField              = "code"
	inventoryMCPContentField           = "content"
	inventoryMCPDataField              = "data"
	inventoryMCPDescriptionField       = "description"
	inventoryMCPDevelopmentVersion     = "dev"
	inventoryMCPErrorField             = "error"
	inventoryMCPIDField                = "id"
	inventoryMCPInputSchemaField       = "inputSchema"
	inventoryMCPInstructionsField      = "instructions"
	inventoryMCPIsErrorField           = "isError"
	inventoryMCPJSONRPCField           = "jsonrpc"
	inventoryMCPJSONRPCVersion         = "2.0"
	inventoryMCPListChangedField       = "listChanged"
	inventoryMCPMessageField           = "message"
	inventoryMCPNameField              = "name"
	inventoryMCPGetStackOperationsTool = "get_stack_operations"
	inventoryMCPOutputSchemaField      = "outputSchema"
	inventoryMCPProtocolVersion        = "2025-11-25"
	inventoryMCPProtocolVersionField   = "protocolVersion"
	inventoryMCPRequiredCapability     = "x-kombify-capability"
	inventoryMCPResultField            = "result"
	inventoryMCPServerInfoField        = "serverInfo"
	inventoryMCPServerName             = "kombify-techstack"
	inventoryMCPStackIDField           = "stack_id"
	inventoryMCPStatusField            = "status"
	inventoryMCPStructuredContentField = "structuredContent"
	inventoryMCPTextField              = "text"
	inventoryMCPTitleField             = "title"
	inventoryMCPToolsField             = "tools"
	inventoryMCPTypeField              = "type"
	inventoryMCPVersionField           = "version"
)

type inventoryMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type inventoryMCPToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Router-scoped binding keeps MCP on the canonical Operations handler without
// replaying request-bound edge authentication through a synthetic HTTP call.
var inventoryMCPStackOperationsHandlers sync.Map // map[*httpx.Router]httpx.HandlerFunc

func registerInventoryMCPStackOperationsHandler(r *httpx.Router, handler httpx.HandlerFunc) {
	if r == nil || handler == nil {
		return
	}
	inventoryMCPStackOperationsHandlers.Store(r, handler)
}

func inventoryMCPStackOperationsHandler(r *httpx.Router) httpx.HandlerFunc {
	if r == nil {
		return nil
	}
	handler, _ := inventoryMCPStackOperationsHandlers.Load(r)
	result, _ := handler.(httpx.HandlerFunc)
	return result
}

func registerInventoryMCPRoutes(r *httpx.Router, h inventoryHandlers) {
	handler := func(e *httpx.Event) error {
		return h.handleMCPWithStackOperations(e, inventoryMCPStackOperationsHandler(r))
	}
	methodNotAllowed := func(e *httpx.Event) error {
		e.Response.Header().Set("Allow", http.MethodPost)
		return e.NoContent(http.StatusMethodNotAllowed)
	}
	r.POST("/api/v1/mcp", handler)
	r.GET("/api/v1/mcp", methodNotAllowed)
	r.POST("/v1/mcp/public/techstack", handler)
	r.GET("/v1/mcp/public/techstack", methodNotAllowed)
}

func (h inventoryHandlers) handleMCP(e *httpx.Event) error {
	return h.handleMCPWithStackOperations(e, nil)
}

func (h inventoryHandlers) handleMCPWithStackOperations(e *httpx.Event, stackOperations httpx.HandlerFunc) error {
	e.Response.Header().Set("MCP-Protocol-Version", inventoryMCPProtocolVersion)
	if !validMCPOrigin(e.Request) {
		return writeMCPHTTPError(e, http.StatusForbidden, nil, -32003, "Forbidden", "origin_not_allowed")
	}
	if header := strings.TrimSpace(e.Request.Header.Get("MCP-Protocol-Version")); header != "" && header != inventoryMCPProtocolVersion {
		return writeMCPHTTPError(e, http.StatusBadRequest, nil, -32600, "Unsupported MCP protocol version", "unsupported_protocol_version")
	}
	var request inventoryMCPRequest
	decoder := json.NewDecoder(e.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.JSONRPC != inventoryMCPJSONRPCVersion || strings.TrimSpace(request.Method) == "" {
		return writeMCPHTTPError(e, http.StatusBadRequest, nil, -32600, "Invalid JSON-RPC request", "invalid_request")
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeMCPAuthError(e, request.ID, err)
	}
	if len(request.ID) == 0 {
		if request.Method == "notifications/initialized" || strings.HasPrefix(request.Method, "notifications/") {
			return e.NoContent(http.StatusAccepted)
		}
		return writeMCPHTTPError(e, http.StatusBadRequest, nil, -32600, "Request id required", "request_id_required")
	}
	return h.dispatchMCPRequest(e, request, scope, stackOperations)
}

func (h inventoryHandlers) dispatchMCPRequest(e *httpx.Event, request inventoryMCPRequest, scope inventoryScope, stackOperations httpx.HandlerFunc) error {
	switch request.Method {
	case "initialize":
		return writeMCPResult(e, request.ID, map[string]any{
			inventoryMCPProtocolVersionField: inventoryMCPProtocolVersion,
			inventoryMCPCapabilitiesField:    map[string]any{inventoryMCPToolsField: map[string]any{inventoryMCPListChangedField: false}},
			inventoryMCPServerInfoField: map[string]any{
				inventoryMCPNameField: inventoryMCPServerName, inventoryMCPTitleField: "Kombify Techstack", inventoryMCPVersionField: firstNonEmptyString(h.version, inventoryMCPDevelopmentVersion),
			},
			inventoryMCPInstructionsField: "Read-only, policy-scoped Techstack inventory. Tenant and subject identity are derived from authenticated context and must never be supplied as tool arguments.",
		})
	case "ping":
		return writeMCPResult(e, request.ID, map[string]any{})
	case "tools/list":
		if _, err := h.app.authorize(e.Request.Context(), scope, InventoryActionRead, controlplane.InventoryReadTargetTools, ""); err != nil {
			return writeMCPToolError(e, request.ID, err)
		}
		manifest, parseErr := toolmanifest.Parse()
		if parseErr != nil {
			return writeMCPProtocolError(e, request.ID, -32603, "Tool catalog unavailable", map[string]any{inventoryReasonCodeField: "tool_catalog_unavailable"})
		}
		tools := make([]map[string]any, 0, len(manifest.Tools))
		for _, tool := range manifest.Tools {
			tools = append(tools, map[string]any{
				inventoryMCPNameField: tool.Name, inventoryMCPTitleField: tool.Title, inventoryMCPDescriptionField: tool.Description,
				inventoryMCPInputSchemaField: tool.InputSchema, inventoryMCPOutputSchemaField: tool.OutputSchema, inventoryMCPAnnotationsField: tool.Annotations,
				inventoryMCPRequiredCapability: tool.RequiredCapability,
			})
		}
		return writeMCPResult(e, request.ID, map[string]any{inventoryMCPToolsField: tools})
	case "tools/call":
		var call inventoryMCPToolCall
		if len(request.Params) == 0 || json.Unmarshal(request.Params, &call) != nil || strings.TrimSpace(call.Name) == "" {
			return writeMCPProtocolError(e, request.ID, -32602, "Invalid tool call", map[string]any{inventoryReasonCodeField: "invalid_tool_call"})
		}
		result, callErr := h.callInventoryTool(e.Request.Context(), scope, call, stackOperations, e)
		if callErr != nil {
			return writeMCPToolError(e, request.ID, callErr)
		}
		return writeMCPToolResult(e, request.ID, result)
	default:
		return writeMCPProtocolError(e, request.ID, -32601, "Method not found", nil)
	}
}

func (h inventoryHandlers) callInventoryTool(ctx context.Context, scope inventoryScope, call inventoryMCPToolCall, stackOperations httpx.HandlerFunc, sourceEvent *httpx.Event) (any, error) {
	arguments := call.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	switch call.Name {
	case "list_servers":
		if err := validateMCPArguments(arguments, map[string]bool{inventoryCursorField: true, inventoryLimitField: true}, nil); err != nil {
			return nil, err
		}
		page, err := inventoryPageFromMCPArguments(arguments)
		if err != nil {
			return nil, err
		}
		return h.app.listServers(ctx, scope, page)
	case "server_health":
		if err := validateMCPArguments(arguments, map[string]bool{inventoryServerIDField: true}, map[string]bool{inventoryServerIDField: true}); err != nil {
			return nil, err
		}
		return h.app.serverHealth(ctx, scope, stringArgument(arguments, inventoryServerIDField))
	case "list_services":
		if err := validateMCPArguments(arguments, map[string]bool{inventoryServerIDField: true, inventoryCursorField: true, inventoryLimitField: true}, nil); err != nil {
			return nil, err
		}
		page, err := inventoryPageFromMCPArguments(arguments)
		if err != nil {
			return nil, err
		}
		return h.app.listServices(ctx, scope, stringArgument(arguments, inventoryServerIDField), page)
	case "server_access_context":
		if err := validateMCPArguments(arguments, map[string]bool{inventoryServerIDField: true}, map[string]bool{inventoryServerIDField: true}); err != nil {
			return nil, err
		}
		return h.app.serverAccessContext(ctx, scope, stringArgument(arguments, inventoryServerIDField))
	case inventoryMCPGetStackOperationsTool:
		if err := validateMCPArguments(arguments, map[string]bool{inventoryMCPStackIDField: true}, map[string]bool{inventoryMCPStackIDField: true}); err != nil {
			return nil, err
		}
		stackID := stringArgument(arguments, inventoryMCPStackIDField)
		if len(stackID) > 256 {
			return nil, &inventoryError{status: http.StatusBadRequest, reasonCode: "stack_id_invalid", message: "Stack ID is invalid"}
		}
		return h.callStackOperationsTool(ctx, scope, stackID, stackOperations, sourceEvent)
	default:
		return nil, &inventoryError{status: http.StatusBadRequest, reasonCode: "tool_not_found", message: "Tool not found"}
	}
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

func writeMCPToolResult(e *httpx.Event, id json.RawMessage, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return writeMCPProtocolError(e, id, -32603, "Failed to encode tool result", nil)
	}
	return writeMCPResult(e, id, map[string]any{
		inventoryMCPContentField:           []map[string]any{{inventoryMCPTypeField: inventoryMCPTextField, inventoryMCPTextField: string(raw)}},
		inventoryMCPStructuredContentField: value,
		inventoryMCPIsErrorField:           false,
	})
}

func writeMCPToolError(e *httpx.Event, id json.RawMessage, err error) error {
	status, code, reason, message := inventoryErrorContract(err)
	structured := map[string]any{inventoryMCPErrorField: map[string]any{inventoryMCPCodeField: code, inventoryMCPStatusField: status, inventoryReasonCodeField: reason}}
	return writeMCPResult(e, id, map[string]any{
		inventoryMCPContentField:           []map[string]any{{inventoryMCPTypeField: inventoryMCPTextField, inventoryMCPTextField: message}},
		inventoryMCPStructuredContentField: structured,
		inventoryMCPIsErrorField:           true,
	})
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

func writeMCPAuthError(e *httpx.Event, id json.RawMessage, err error) error {
	status, _, reason, message := inventoryErrorContract(err)
	protocolCode := -32001
	if status == http.StatusForbidden {
		protocolCode = -32003
	}
	return writeMCPHTTPError(e, status, id, protocolCode, message, reason)
}

func writeMCPHTTPError(e *httpx.Event, status int, id json.RawMessage, code int, message, reason string) error {
	e.Response.Header().Set("Content-Type", "application/json")
	return e.JSON(status, map[string]any{
		inventoryMCPJSONRPCField: inventoryMCPJSONRPCVersion, inventoryMCPIDField: rawMCPID(id),
		inventoryMCPErrorField: map[string]any{inventoryMCPCodeField: code, inventoryMCPMessageField: message, inventoryMCPDataField: map[string]any{inventoryMCPStatusField: status, inventoryReasonCodeField: reason}},
	})
}

func writeMCPProtocolError(e *httpx.Event, id json.RawMessage, code int, message string, data any) error {
	return e.JSON(http.StatusOK, map[string]any{inventoryMCPJSONRPCField: inventoryMCPJSONRPCVersion, inventoryMCPIDField: rawMCPID(id), inventoryMCPErrorField: map[string]any{inventoryMCPCodeField: code, inventoryMCPMessageField: message, inventoryMCPDataField: data}})
}

func writeMCPResult(e *httpx.Event, id json.RawMessage, result any) error {
	e.Response.Header().Set("Content-Type", "application/json")
	return e.JSON(http.StatusOK, map[string]any{inventoryMCPJSONRPCField: inventoryMCPJSONRPCVersion, inventoryMCPIDField: rawMCPID(id), inventoryMCPResultField: result})
}

func rawMCPID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
