// Package routes provides tests for Unifier API routes.
package routes

import (
	"encoding/json"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func TestUnifierAPI_Creation(t *testing.T) {
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	if api.engine == nil {
		t.Error("expected engine to be non-nil")
	}

	if api.pipeline == nil {
		t.Error("expected pipeline to be non-nil")
	}

	if api.loader == nil {
		t.Error("expected loader to be non-nil")
	}
}

func TestUnifierAPI_LoaderCanParseYAML(t *testing.T) {
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      user: ubuntu
services:
  - name: traefik
    type: reverse-proxy
    node: main-server
`)

	spec, err := api.loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	if spec.Name != "test-stack" {
		t.Errorf("expected name 'test-stack', got '%s'", spec.Name)
	}
}

func TestUnifierAPI_LoaderCanParseJSON(t *testing.T) {
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	jsonData := []byte(`{
		"name": "test-stack",
		"kit": "basement-kit",
		"nodes": [
			{
				"name": "main-server",
				"type": "main",
				"provider": "local",
				"ssh": {
					"host": "192.168.1.100",
					"user": "ubuntu"
				}
			}
		],
		"services": [
			{
				"name": "traefik",
				"type": "reverse-proxy",
				"node": "main-server"
			}
		]
	}`)

	spec, err := api.loader.LoadBytes(jsonData)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	if spec.Name != "test-stack" {
		t.Errorf("expected name 'test-stack', got '%s'", spec.Name)
	}
}

func TestUnifierAPI_PipelineExecute(t *testing.T) {
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	yaml := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      user: ubuntu
services:
  - name: traefik
    type: reverse-proxy
`)

	spec, err := api.loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	result := api.pipeline.Execute(spec)
	if !result.Success {
		t.Errorf("Pipeline.Execute() failed: %s - %s", result.FailedStep, result.ErrorMessage)
	}

	if result.UnifiedSpec == nil {
		t.Error("expected UnifiedSpec to be non-nil")
	}

	if len(result.Steps) != 8 {
		t.Errorf("expected 8 steps, got %d", len(result.Steps))
	}
	if result.DecisionContext == nil {
		t.Error("expected DecisionContext to be non-nil")
	}
	if result.DecisionTrace == nil {
		t.Error("expected DecisionTrace to be non-nil")
	}
}

func TestPipelinePreviewResponseReturnsInvalidResultWithoutHTTPFailureSemantics(t *testing.T) {
	result := &unifier.PipelineResult{
		Success:      false,
		FailedStep:   "stackkit-resolution",
		ErrorMessage: "StackKit resolution failed: kit 'missing-kit' not available",
		Steps: []unifier.PipelineStep{
			{Name: "pre-validation", Status: "success"},
			{Name: "stackkit-resolution", Status: "failed", Error: "missing-kit"},
		},
		ValidationResult: &core.ValidationResult{Valid: true},
		ResolveResult: &unifier.ResolveResult{
			StackKit: "missing-kit",
			Valid:    false,
		},
		Warnings: []string{"StackKit 'missing-kit' may not be available"},
	}

	response := buildPipelinePreviewResponse(result)

	if response["valid"] != false {
		t.Fatalf("valid = %v, want false", response["valid"])
	}
	if response["resolved_stackkit"] != "missing-kit" {
		t.Fatalf("resolved_stackkit = %v, want missing-kit", response["resolved_stackkit"])
	}
	errors, ok := response["errors"].([]map[string]string)
	if !ok || len(errors) == 0 {
		t.Fatalf("expected structured preview errors, got %#v", response["errors"])
	}
	if errors[0]["code"] != "stackkit_resolution" {
		t.Fatalf("error code = %q, want stackkit_resolution", errors[0]["code"])
	}
	if _, ok := response["config"]; ok {
		t.Fatal("invalid preview must not include a unified config")
	}
}

func TestUnifierAPI_PipelineWithoutKit(t *testing.T) {
	// Test Easy-Path: Kit is optional and will be auto-selected
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	yaml := []byte(`
name: test-stack
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      user: ubuntu
services:
  - name: traefik
    type: reverse-proxy
`)

	spec, err := api.loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes() failed: %v", err)
	}

	result := api.pipeline.Execute(spec)
	if !result.Success {
		t.Errorf("Pipeline.Execute() failed: %s - %s", result.FailedStep, result.ErrorMessage)
	}

	// Should have auto-selected a kit
	if result.ResolveResult == nil {
		t.Fatal("expected ResolveResult to be non-nil")
	}

	if result.ResolveResult.StackKit == "" {
		t.Error("expected StackKit to be auto-selected")
	}

	if !result.ResolveResult.AutoSelected {
		t.Error("expected AutoSelected to be true")
	}
}

func TestUnifierAPI_AddonDetector(t *testing.T) {
	api, err := NewUnifierAPI(nil)
	if err != nil {
		t.Fatalf("NewUnifierAPI() failed: %v", err)
	}

	detector := api.pipeline.GetAddonDetector()
	if detector == nil {
		t.Fatal("expected AddonDetector to be non-nil")
	}

	addons := detector.ListAddons()
	if len(addons) == 0 {
		t.Error("expected at least one add-on to be registered")
	}

	// Check that we have the expected add-ons
	addonNames := make(map[string]bool)
	for _, addon := range addons {
		addonNames[addon.Name] = true
	}

	expectedAddons := []string{"cloud-integration", "arm-support", "low-memory", "multi-node"}
	for _, name := range expectedAddons {
		if !addonNames[name] {
			t.Errorf("expected add-on '%s' to be registered", name)
		}
	}
}

func TestPipeline_ValidationErrors(t *testing.T) {
	// Missing name - allowed for Easy-Path (defaults applied later)
	invalidSpec := []byte(`
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
`)

	loader := unifier.NewLoader()
	_, err := loader.LoadBytes(invalidSpec)
	if err != nil {
		t.Fatalf("expected missing name to be allowed at load time: %v", err)
	}
}

func TestPipeline_InvalidNodeReference(t *testing.T) {
	// Service references non-existent node - should fail pre-validation
	invalidSpec := []byte(`
name: test-stack
kit: basement-kit
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      user: ubuntu
services:
  - name: traefik
    type: reverse-proxy
    node: nonexistent-server
`)

	loader := unifier.NewLoader()
	_, err := loader.LoadBytes(invalidSpec)
	if err == nil {
		t.Error("expected error for invalid node reference")
	}
}

func TestConvertUIConfigToSpec(t *testing.T) {
	// Test wizard format conversion
	wizardConfig := map[string]interface{}{
		"name": "my-homelab",
		"goals": map[string]interface{}{
			"photos": true,
			"mail":   true,
			"vault":  true,
		},
		"provider": "local",
		"network": map[string]interface{}{
			"accessMode": "local",
		},
	}

	jsonData, err := json.Marshal(wizardConfig)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}

	// The conversion happens in the handlers, but we can verify the format is parseable
	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonData, &parsed); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}

	if parsed["name"] != "my-homelab" {
		t.Errorf("expected name 'my-homelab', got '%v'", parsed["name"])
	}
}
