// Package mcpbind registers a product's apisurface MCP tools on an official
// go-sdk server. Tool names, titles, descriptions, schemas and annotations
// come from the same builder as the generated tool-manifest.json, so the
// served tools and the committed manifest cannot drift.
//
// Registration fails closed: every exposed operation (explicit or derived
// from x-kombify-surface.mcp.defaults) needs a handler, and arguments are
// validated against the tool's input schema before Authorize and the handler
// run.
package mcpbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kombifyio/techstack/internal/gocommon/apisurface"
)

// Handler serves one operation. args are the JSON-decoded, schema-validated
// tool arguments keyed by surface argument name. An object result becomes
// structured content plus its JSON text; any other result becomes JSON text.
// An error becomes an IsError result carrying the error message and, when the
// error implements ToolErrorPayload, that payload as structured content.
type Handler func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (any, error)

// Options configure Register.
type Options struct {
	// Handlers are keyed by operationId.
	Handlers map[string]Handler
	// Authorize runs before every handler; a non-nil error denies the call
	// the same way a handler error does. Nil means no extra check.
	Authorize func(ctx context.Context, op *apisurface.Operation) error
	// Meta returns optional per-tool _meta, such as the capability key.
	Meta func(op *apisurface.Operation) mcp.Meta
}

// Register adds every MCP tool of s to server. It returns an error naming
// every exposed operationId without a handler, and registers nothing then.
func Register(server *mcp.Server, s *apisurface.Surface, opts Options) error {
	ops := make(map[string]*apisurface.Operation, len(s.Operations))
	for i := range s.Operations {
		ops[s.Operations[i].OperationID] = &s.Operations[i]
	}
	manifest := s.ToolManifest()
	var missing []string
	for _, def := range manifest.Tools {
		if opts.Handlers[def.OperationID] == nil {
			missing = append(missing, def.OperationID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("mcpbind: no handler for exposed operation(s): %s", strings.Join(missing, ", "))
	}
	type binding struct {
		tool   *mcp.Tool
		schema *jsonschema.Resolved
		op     *apisurface.Operation
	}
	bindings := make([]binding, 0, len(manifest.Tools))
	for _, def := range manifest.Tools {
		op := ops[def.OperationID]
		schema, err := resolve(def.InputSchema)
		if err != nil {
			return fmt.Errorf("mcpbind: tool %s input schema: %w", def.Name, err)
		}
		tool := &mcp.Tool{
			Name:        def.Name,
			Title:       def.Title,
			Description: def.Description,
			InputSchema: def.InputSchema,
			Annotations: annotations(def.Annotations),
		}
		if def.OutputSchema != nil {
			tool.OutputSchema = def.OutputSchema
		}
		if opts.Meta != nil {
			tool.Meta = opts.Meta(op)
		}
		bindings = append(bindings, binding{tool, schema, op})
	}
	for _, b := range bindings {
		server.AddTool(b.tool, toolHandler(b.op, b.schema, opts.Handlers[b.op.OperationID], opts.Authorize))
	}
	return nil
}

func toolHandler(op *apisurface.Operation, schema *jsonschema.Resolved, h Handler, authorize func(context.Context, *apisurface.Operation) error) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &args); err != nil || args == nil {
				return errorResult(errors.New("arguments must be a JSON object")), nil
			}
		}
		if err := schema.Validate(args); err != nil {
			return errorResult(fmt.Errorf("validating arguments: %w", err)), nil
		}
		if authorize != nil {
			if err := authorize(ctx, op); err != nil {
				return errorResult(err), nil
			}
		}
		out, err := h(ctx, req, args)
		if err != nil {
			return errorResult(err), nil
		}
		return successResult(out)
	}
}

func successResult(out any) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return errorResult(fmt.Errorf("encoding result: %w", err)), nil
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
	if trimmed := strings.TrimSpace(string(raw)); strings.HasPrefix(trimmed, "{") {
		res.StructuredContent = json.RawMessage(raw)
	}
	return res, nil
}

// payloadError is implemented by errors that carry a structured tool error.
type payloadError interface {
	ToolErrorPayload() map[string]any
}

func errorResult(err error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
	var pe payloadError
	if errors.As(err, &pe) {
		if payload := pe.ToolErrorPayload(); payload != nil {
			res.StructuredContent = payload
		}
	}
	return res
}

func annotations(a apisurface.Annotations) *mcp.ToolAnnotations {
	destructive, openWorld := a.DestructiveHint, a.OpenWorldHint
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    a.ReadOnlyHint,
		DestructiveHint: &destructive,
		IdempotentHint:  a.IdempotentHint,
		OpenWorldHint:   &openWorld,
	}
}

func resolve(schema map[string]any) (*jsonschema.Resolved, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}
