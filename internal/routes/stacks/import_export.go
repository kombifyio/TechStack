// Package stacks provides import/export handlers for kombifyTechstack configurations.
// This file handles exporting StackKits stack-spec.yaml payloads and importing
// StackKits stack-spec.yaml or legacy kombination.yaml configuration files.
package stacks

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"gopkg.in/yaml.v3"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/unifier"
)

const (
	importExportStackIDKey          = "stack_id"
	importValidationErrorCodeKey    = "code"
	importValidationErrorPathKey    = "path"
	importValidationErrorMessageKey = "message"
)

func RegisterImportExportRoutesWithModeAndFeatures(r *httpx.Router, app core.App, orch *orchestrator.Orchestrator, mode config.DeploymentMode, featureChecker managedRuntimeFeatureChecker) {
	if !mode.IsValid() {
		mode = config.ModeSelfHosted
	}
	stores := currentControlPlaneStores()
	h := importExportRouteHandlers{
		app: app,
		create: crudRouteHandlers{
			app:             app,
			orch:            orch,
			deploymentMode:  mode,
			runtimeFeatures: featureChecker,
			stackStore:      stores.Stacks,
			homelabStore:    stores.Homelabs,
			jobStore:        stores.Jobs,
			walletStore:     stores.Wallet,
			serverStore:     stores.Servers,
		},
	}

	// GET /api/v1/stacks/{id}/export - Export stack configuration as stack-spec.yaml.
	// Returns the user's configuration in YAML format for editing or version control.
	r.GET("/api/v1/stacks/{id}/export", h.exportStack)

	// POST /api/v1/stacks/import - Import stack-spec.yaml or legacy kombination.yaml and create a new stack.
	// This triggers the same flow as the config wizard (animated creating page).
	r.POST("/api/v1/stacks/import", h.importStack)

	// POST /api/v1/stacks/import/validate - Validate stack-spec.yaml or legacy kombination.yaml without importing.
	// Useful for previewing what would happen before committing.
	r.POST("/api/v1/stacks/import/validate", h.validateImport)
}

type importExportRouteHandlers struct {
	app    core.App
	create crudRouteHandlers
}

func (h importExportRouteHandlers) exportStack(e *httpx.Event) error {
	stackId := e.Request.PathValue("id")

	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}

	stack, err := h.app.FindRecordById("stacks", stackId)
	if err != nil {
		return httpx.NotFound(e, "Stack not found")
	}
	if stack.GetString("owner_id") != ownerID {
		return httpx.Forbidden(e, "Not your stack")
	}

	exportData := buildStackSpecExport(stack)
	acceptHeader := e.Request.Header.Get("Accept")
	if acceptHeader == "application/yaml" || acceptHeader == "text/yaml" {
		return h.exportStackYAML(e, stackId, exportData)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"stack_spec":           exportData,
		"kombination":          exportData, // legacy response alias for older UI/API clients
		"format":               "json",
		importExportStackIDKey: stackId,
		"exported_at":          stack.GetDateTime("updated").String(),
	})
}

func (h importExportRouteHandlers) exportStackYAML(e *httpx.Event, stackId string, exportData map[string]interface{}) error {
	if persister, pErr := unifier.NewSpecPersister(stackId); pErr == nil && persister.IntentExists() {
		if data, lErr := persister.LoadIntentBytes(); lErr == nil {
			e.Response.Header().Set("Content-Type", "application/yaml")
			e.Response.Header().Set("Content-Disposition", "attachment; filename=\"stack-spec.yaml\"")
			return e.Blob(http.StatusOK, "application/yaml", data)
		}
	}

	yamlBytes, err := marshalToYAML(exportData)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to generate YAML", nil)
	}
	e.Response.Header().Set("Content-Type", "application/yaml")
	e.Response.Header().Set("Content-Disposition", "attachment; filename=\"stack-spec.yaml\"")
	return e.Blob(http.StatusOK, "application/yaml", yamlBytes)
}

func (h importExportRouteHandlers) importStack(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}

	body, bodyErr := readNonEmptyImportBody(e)
	if bodyErr != nil {
		return bodyErr
	}

	req, msg := importCreateStackRequestFromBody(body, e.Request.Header.Get("Content-Type"))
	if msg != "" {
		return httpx.BadRequest(e, msg)
	}
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		return httpx.BadRequest(e, msg)
	}
	if laneMsg := validateDeploymentLane(normalized, h.create.deploymentMode); laneMsg != "" {
		return httpx.BadRequest(e, laneMsg)
	}
	if rejected, entitlementErr := h.create.rejectUnauthorizedManagedRuntime(e, ownerID, normalized); rejected || entitlementErr != nil {
		return entitlementErr
	}
	normalized, denial := resolveCreateOwnerBootstrap(normalized, h.create.ownerBootstrapContextForCreate(e, ownerID, normalized))
	if denial != nil {
		return denial.write(e)
	}
	return h.create.createNormalizedStack(e, ownerID, normalized)
}

func (h importExportRouteHandlers) validateImport(e *httpx.Event) error {
	if _, authErr := requireStackAuth(e); authErr != nil {
		return authErr
	}

	body, bodyErr := readNonEmptyImportBody(e)
	if bodyErr != nil {
		return bodyErr
	}

	spec, validationErr := parseImportValidationSpec(body, e.Request.Header.Get("Content-Type"))
	if validationErr != nil {
		return httpx.Success(e, http.StatusOK, validationErr)
	}

	validation := ValidateKombinationSpec(spec)
	return httpx.Success(e, http.StatusOK, validation)
}

func readNonEmptyImportBody(e *httpx.Event) ([]byte, error) {
	body, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return nil, httpx.BadRequest(e, "Failed to read request body")
	}
	if len(body) == 0 {
		return nil, httpx.BadRequest(e, "Empty request body")
	}
	return body, nil
}

func parseImportValidationSpec(body []byte, contentType string) (map[string]interface{}, map[string]any) {
	if strings.Contains(contentType, "yaml") || strings.Contains(contentType, "text/plain") {
		parsed, err := ParseYAMLOrJSON(body)
		if err != nil {
			return nil, map[string]any{
				"valid":  false,
				"errors": []map[string]string{{importValidationErrorPathKey: "", importValidationErrorCodeKey: "YAML_PARSE_ERROR", importValidationErrorMessageKey: err.Error()}},
			}
		}
		return parsed, nil
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(body, &spec); err != nil {
		parsed, yamlErr := ParseYAMLOrJSON(body)
		if yamlErr != nil {
			return nil, map[string]any{
				"valid":  false,
				"errors": []map[string]string{{importValidationErrorPathKey: "", importValidationErrorCodeKey: "PARSE_ERROR", importValidationErrorMessageKey: err.Error()}},
			}
		}
		return parsed, nil
	}
	return spec, nil
}

func importCreateStackRequestFromBody(body []byte, contentType string) (createStackRequest, string) {
	var spec map[string]interface{}
	var rawYAML string

	if strings.Contains(contentType, "yaml") || strings.Contains(contentType, "text/plain") {
		rawYAML = string(body)
		parsed, err := ParseYAMLOrJSON(body)
		if err != nil {
			return createStackRequest{}, "Invalid YAML: " + err.Error()
		}
		spec = parsed
	} else if err := json.Unmarshal(body, &spec); err != nil {
		parsed, yamlErr := ParseYAMLOrJSON(body)
		if yamlErr != nil {
			return createStackRequest{}, "Invalid JSON or YAML: " + err.Error()
		}
		spec = parsed
		rawYAML = string(body)
	}

	name := "techstack"
	if n, ok := spec["name"].(string); ok && strings.TrimSpace(n) != "" {
		name = n
	}
	req := createStackRequest{
		Name:      name,
		Mode:      stackModeTechie,
		StackSpec: spec,
	}
	if rawYAML != "" {
		req.UserConfigRaw = rawYAML
		req.UserConfigFormat = "yaml"
	}
	return req, ""
}

// buildStackSpecExport creates the external stack specification structure from a stack record.
func buildStackSpecExport(stack *core.Record) map[string]interface{} {
	export := make(map[string]interface{})

	// Start with user_config if available (this is the original input)
	if userConfig := stack.Get("user_config"); userConfig != nil {
		if configMap, ok := mapFromAny(userConfig); ok {
			for k, v := range configMap {
				export[k] = v
			}
		}
	}

	// Ensure name is set
	if _, hasName := export["name"]; !hasName {
		export["name"] = stack.GetString("name")
	}

	// Legacy TechStack KombinationSpec exports still carry version. StackKits
	// stack-spec.yaml does not require it, so do not inject it into canonical
	// stack_spec exports.
	if _, hasVersion := export["version"]; !hasVersion && !isStackKitsStackSpec(export) {
		export["version"] = "1.0"
	}

	if isStackKitsStackSpec(export) {
		return export
	}

	// Add metadata about the export for legacy KombinationSpec payloads.
	if _, hasMeta := export["metadata"]; !hasMeta {
		export["metadata"] = make(map[string]interface{})
	}
	if meta, ok := export["metadata"].(map[string]interface{}); ok {
		meta["exported_from"] = "techstack"
		meta[importExportStackIDKey] = stack.Id
	}

	return export
}

func isStackKitsStackSpec(spec map[string]interface{}) bool {
	if stackkit, ok := spec["stackkit"].(string); ok && strings.TrimSpace(stackkit) != "" {
		return true
	}
	if metadata, ok := mapFromAny(spec["metadata"]); ok {
		if format, ok := metadata["spec_format"].(string); ok && strings.TrimSpace(format) == "stack-spec" {
			return true
		}
	}
	return false
}

// marshalToYAML converts a map to YAML format.
func marshalToYAML(data map[string]interface{}) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(data)
	if err != nil {
		return nil, err
	}

	header := []byte("# kombify-TechStack Stack Spec Export\n# Exported from kombify-TechStack UI\n# Edit this file and re-import to update your configuration\n---\n")
	return append(header, yamlBytes...), nil
}
