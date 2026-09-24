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

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

const (
	importExportKitDeploymentIDKey  = "kit_deployment_id"
	importExportLegacyStackIDKey    = "stack_id"
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
			activityStore:   stores.Activity,
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

type stackExport struct {
	ID        string
	UpdatedAt string
	Spec      map[string]interface{}
}

func (h importExportRouteHandlers) exportStack(e *httpx.Event) error {
	if h.create.stackStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack export authority is temporarily unavailable", map[string]any{
				detailsKeyReasonCode: "stack_export_authority_unavailable",
				detailsKeyRetryable:  true,
			})
	}
	stack, err := h.create.findOwnedStoreStack(e, e.Request.PathValue("id"))
	if err != nil {
		return err
	}
	return h.writeStackExport(e, stackExport{
		ID: stack.ID, UpdatedAt: formatAPITime(stack.UpdatedAt),
		Spec: buildStoreStackSpecExport(stack),
	})
}

func (h importExportRouteHandlers) writeStackExport(e *httpx.Event, export stackExport) error {
	acceptHeader := e.Request.Header.Get("Accept")
	if acceptHeader == "application/yaml" || acceptHeader == "text/yaml" {
		return h.exportStackYAML(e, export)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"stack_spec":                   export.Spec,
		"kombination":                  export.Spec, // legacy response alias for older UI/API clients
		"format":                       "json",
		importExportKitDeploymentIDKey: export.ID,
		"exported_at":                  export.UpdatedAt,
	})
}

func (h importExportRouteHandlers) exportStackYAML(e *httpx.Event, export stackExport) error {
	yamlBytes, err := marshalToYAML(export.Spec)
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
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.import")
	if tenantErr != nil {
		return tenantErr
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
	return h.create.createNormalizedStack(e, ownerID, tenantID, normalized)
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
		return nil, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to read request body", nil)
	}
	if len(body) == 0 {
		return nil, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Empty request body", nil)
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

// buildStoreStackSpecExport exports the durable control-plane authority. A
// native-v2 wizard run owns an already CLI-validated StackSpec under
// config_json.stack_spec_v2, so exporting the legacy user_config sibling would
// silently discard its nodes, workloads, placement, and StackKits identity.
func buildStoreStackSpecExport(stack *controlplane.Stack) map[string]interface{} {
	if stack == nil {
		return map[string]interface{}{}
	}
	if spec, ok := stackSpecMapFromValue(stack.Config[stackConfigKeySpecV2]); ok {
		return cloneMapForMutation(spec)
	}

	export := map[string]interface{}{}
	if userConfig, ok := stackSpecMapFromValue(stack.Config["user_config"]); ok {
		export = cloneMapForMutation(userConfig)
	}
	return finalizeStackSpecExport(export, stack.Name, stack.ID)
}

func finalizeStackSpecExport(export map[string]interface{}, name, techstackID string) map[string]interface{} {
	if _, hasName := export["name"]; !hasName {
		export["name"] = name
	}
	// Legacy TechStack KombinationSpec exports still carry version. StackKits
	// StackSpec does not, so preserve that canonical document byte-shape.
	if _, hasVersion := export["version"]; !hasVersion && !isStackKitsStackSpec(export) {
		export["version"] = "1.0"
	}
	if isStackKitsStackSpec(export) {
		return export
	}
	if _, hasMeta := export["metadata"]; !hasMeta {
		export["metadata"] = make(map[string]interface{})
	}
	if meta, ok := export["metadata"].(map[string]interface{}); ok {
		meta["exported_from"] = "techstack"
		meta[importExportLegacyStackIDKey] = techstackID
	}
	return export
}

func isStackKitsStackSpec(spec map[string]interface{}) bool {
	if apiVersion, _ := spec["apiVersion"].(string); strings.HasPrefix(strings.TrimSpace(apiVersion), "stackkit/") {
		return true
	}
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
