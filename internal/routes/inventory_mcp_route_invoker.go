package routes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/apisurface"
	"github.com/kombifyio/techstack/internal/gocommon/apisurface/mcpbind"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// inventoryMCPForwardedHeaders are the request-scoped, non-credential headers
// a route handler may read from the verified MCP request. Authorization,
// cookies and edge signatures are never copied: the verified identity,
// signed entitlements and step-up evidence travel in the request context.
var inventoryMCPForwardedHeaders = []string{
	"X-Request-ID",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-IP",
	"User-Agent",
}

// inventoryMCPRouteTool serves one contract operation by invoking its
// registered route handler directly with the verified MCP request context.
// The edge signature is bound to the MCP request's method and path, so the
// synthetic request never passes through the router or its middleware.
func inventoryMCPRouteTool(op *apisurface.Operation, envelope string, routes inventoryMCPRouteLookup) mcpbind.Handler {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, arguments map[string]any) (any, error) {
		caller, ok := inventoryMCPCallerFromContext(ctx)
		if !ok {
			return nil, inventoryMCPFailure(inventoryMCPAuthenticationRequired())
		}
		var handler httpx.HandlerFunc
		var pattern string
		if routes != nil {
			handler, pattern, _ = routes(op.Method, op.Path)
		}
		if handler == nil {
			return nil, inventoryMCPFailure(&inventoryError{status: http.StatusServiceUnavailable, reasonCode: "operation_unavailable", message: "Operation unavailable"})
		}
		request, err := inventoryMCPRouteRequest(ctx, op, pattern, arguments, caller.event)
		if err != nil {
			return nil, inventoryMCPFailure(inventoryValidationError("invalid_tool_argument", "Invalid tool argument"))
		}
		recorder := httptest.NewRecorder()
		var auth *httpx.Principal
		if caller.event != nil {
			auth = caller.event.Auth
		}
		httpx.Invoke(recorder, request, auth, handler)
		return inventoryMCPRouteResult(op, envelope, recorder.Result())
	}
}

// inventoryMCPRouteRequest builds the route request from the surface
// arguments: path values bound by position to the registered pattern's
// wildcard names, query and header arguments by wire name, and the JSON body.
func inventoryMCPRouteRequest(ctx context.Context, op *apisurface.Operation, pattern string, arguments map[string]any, source *httpx.Event) (*http.Request, error) {
	wire := func(argument apisurface.Argument) string {
		return firstNonEmptyString(argument.WireName, argument.Name)
	}
	templateSegments := strings.Split(op.Path, "/")
	patternSegments := strings.Split(pattern, "/")
	if len(templateSegments) != len(patternSegments) {
		return nil, fmt.Errorf("route pattern %q does not match %q", pattern, op.Path)
	}
	pathValues := map[string]string{}
	pathSegments := make([]string, len(templateSegments))
	for i, segment := range templateSegments {
		if !strings.HasPrefix(segment, "{") {
			pathSegments[i] = segment
			continue
		}
		name := strings.Trim(segment, "{}")
		var value string
		for _, argument := range op.Arguments {
			if argument.In == apisurface.InPath && wire(argument) == name {
				value = inventoryMCPArgumentString(arguments[argument.Name])
			}
		}
		if value == "" {
			return nil, fmt.Errorf("path argument %s missing", name)
		}
		pathSegments[i] = url.PathEscape(value)
		pathValues[strings.TrimSuffix(strings.Trim(patternSegments[i], "{}"), "...")] = value
	}

	query := url.Values{}
	header := http.Header{}
	bodyProperties := map[string]any{}
	for _, argument := range op.Arguments {
		value, present := arguments[argument.Name]
		if !present {
			continue
		}
		switch argument.In {
		case apisurface.InQuery:
			if items, ok := value.([]any); ok {
				for _, item := range items {
					query.Add(wire(argument), inventoryMCPArgumentString(item))
				}
				continue
			}
			query.Set(wire(argument), inventoryMCPArgumentString(value))
		case apisurface.InHeader:
			header.Set(wire(argument), inventoryMCPArgumentString(value))
		case apisurface.InBody:
			bodyProperties[wire(argument)] = value
		}
	}

	var body io.Reader = http.NoBody
	if op.Body != nil {
		var payload any
		switch op.Body.Mode {
		case apisurface.BodyProperties:
			payload = bodyProperties
		case apisurface.BodyJSON:
			if value, ok := arguments["body"]; ok {
				payload = value
			}
		default:
			return nil, fmt.Errorf("body mode %q is not served over MCP", op.Body.Mode)
		}
		if payload != nil {
			raw, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(raw)
			header.Set("Content-Type", firstNonEmptyString(op.Body.ContentType, "application/json"))
		}
	}

	target := strings.Join(pathSegments, "/")
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, op.Method, target, body)
	if err != nil {
		return nil, err
	}
	for name, value := range pathValues {
		request.SetPathValue(name, value)
	}
	if source != nil && source.Request != nil {
		for _, name := range inventoryMCPForwardedHeaders {
			if value := source.Request.Header.Get(name); value != "" {
				request.Header.Set(name, value)
			}
		}
		request.Host = source.Request.Host
		request.RemoteAddr = source.Request.RemoteAddr
	}
	for name, values := range header {
		request.Header[name] = values
	}
	request.Header.Set("Accept", "application/json")
	if op.Mutating || (op.Method != http.MethodGet && op.Method != http.MethodHead) {
		request.Header.Set("Idempotency-Key", inventoryMCPIdempotencyKey(source))
	}
	return request, nil
}

// inventoryMCPIdempotencyKey reuses the caller's key for its single stateless
// tool call and otherwise mints a fresh one, as the generated CLI does.
func inventoryMCPIdempotencyKey(source *httpx.Event) string {
	if source != nil && source.Request != nil {
		if key := strings.TrimSpace(source.Request.Header.Get("Idempotency-Key")); key != "" {
			return key
		}
	}
	var random [16]byte
	_, _ = rand.Read(random[:])
	return "mcp-" + hex.EncodeToString(random[:])
}

func inventoryMCPArgumentString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

// inventoryMCPRouteResult unwraps the success envelope into the tool result
// and turns a non-2xx answer into a tool error carrying the product's
// structured error body.
func inventoryMCPRouteResult(op *apisurface.Operation, envelope string, response *http.Response) (any, error) {
	body, _ := io.ReadAll(response.Body)
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	var decoded any
	isJSON := strings.HasSuffix(mediaType, "json") && len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &decoded) == nil
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, inventoryMCPRouteFailure(response.StatusCode, decoded, body)
	}
	if !isJSON {
		if len(bytes.TrimSpace(body)) == 0 {
			return map[string]any{inventoryMCPStatusField: response.StatusCode}, nil
		}
		return map[string]any{"content_type": response.Header.Get("Content-Type"), "body": string(body)}, nil
	}
	if op.Output != nil {
		envelope = op.Output.Envelope
	}
	if object, ok := decoded.(map[string]any); ok && envelope != "" {
		if payload, ok := object[envelope]; ok {
			return payload, nil
		}
	}
	return decoded, nil
}

func inventoryMCPRouteFailure(status int, decoded any, body []byte) error {
	detail := map[string]any{}
	switch typed := decoded.(type) {
	case map[string]any:
		if productError, ok := typed[inventoryMCPErrorField].(map[string]any); ok {
			detail = productError
		} else {
			detail = typed
		}
	case nil:
		if text := strings.TrimSpace(string(body)); text != "" {
			detail[routeMessageField] = text
		}
	default:
		detail["body"] = typed
	}
	detail[inventoryMCPStatusField] = status
	if _, ok := detail[inventoryReasonCodeField]; !ok {
		if details, ok := detail["details"].(map[string]any); ok {
			if reason, ok := details[inventoryReasonCodeField].(string); ok && reason != "" {
				detail[inventoryReasonCodeField] = reason
			}
		}
	}
	message, _ := detail[routeMessageField].(string)
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	return &inventoryMCPToolFailure{message: message, payload: map[string]any{inventoryMCPErrorField: detail}}
}
