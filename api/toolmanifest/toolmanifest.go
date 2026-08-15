package toolmanifest

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// Raw is the product-owned projection used by discovery, Gateway validation,
// and the native MCP server. It contains no tenant-specific data.
//
//go:embed tool-manifest.json
var Raw []byte

// Manifest is the immutable product-level MCP discovery document.
type Manifest struct {
	SchemaVersion string           `json:"schema_version"`
	Product       string           `json:"product"`
	Capability    string           `json:"capability"`
	MCP           MCP              `json:"mcp"`
	Tools         []ToolDefinition `json:"tools"`
}

// MCP identifies the supported transport endpoint and protocol version.
type MCP struct {
	Transport       string `json:"transport"`
	Endpoint        string `json:"endpoint"`
	ProtocolVersion string `json:"protocol_version"`
}

// ToolDefinition binds one inventory application operation to HTTP and MCP.
type ToolDefinition struct {
	Name               string         `json:"name"`
	Title              string         `json:"title,omitempty"`
	Description        string         `json:"description,omitempty"`
	RequiredCapability string         `json:"requiredCapability"`
	OperationID        string         `json:"operationId"`
	HTTP               HTTPBinding    `json:"http"`
	InputSchema        map[string]any `json:"inputSchema"`
	OutputSchema       map[string]any `json:"outputSchema"`
	Annotations        map[string]any `json:"annotations"`
}

// HTTPBinding names the REST operation backing a tool.
type HTTPBinding struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

var (
	parsed     Manifest
	parsedErr  error
	parsedOnce sync.Once
)

// Parse validates and returns the embedded manifest.
func Parse() (Manifest, error) {
	parsedOnce.Do(func() { parsedErr = json.Unmarshal(Raw, &parsed) })
	return parsed, parsedErr
}
