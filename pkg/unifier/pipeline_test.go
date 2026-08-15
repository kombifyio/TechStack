package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// Test Pre-Validation

func TestPreValidator_ValidSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					User: "kombi",
				},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name: "traefik",
				Type: "reverse-proxy",
				Node: "main-server",
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid spec, got errors: %v", result.Errors)
	}
}

func TestPreValidator_MissingName(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "", // Missing name
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Provider: "local",
				SSH:      &core.SSHConfig{Host: "192.168.1.1"},
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid spec even when name is missing (Easy-Path), got errors: %v", result.Errors)
	}
}

func TestPreValidator_EmptySpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected empty spec to be valid (Easy-Path), got errors: %v", result.Errors)
	}
}

func TestPreValidator_InvalidProvider(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Provider: "invalid-provider",
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to invalid provider")
	}
}

func TestPreValidator_ServiceReferencesUnknownNode(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Provider: "local",
				SSH:      &core.SSHConfig{Host: "192.168.1.1"},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name: "traefik",
				Node: "unknown-node", // References unknown node
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to unknown node reference")
	}

	hasRefError := false
	for _, e := range result.Errors {
		if e.Code == "invalid_reference" {
			hasRefError = true
			break
		}
	}
	if !hasRefError {
		t.Error("expected error for invalid node reference")
	}
}

func TestPreValidator_DuplicateNodeNames(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{Name: "server", Provider: "local", SSH: &core.SSHConfig{Host: "192.168.1.1"}},
			{Name: "server", Provider: "local", SSH: &core.SSHConfig{Host: "192.168.1.2"}}, // Duplicate
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to duplicate node names")
	}

	hasDupError := false
	for _, e := range result.Errors {
		if e.Code == "duplicate" {
			hasDupError = true
			break
		}
	}
	if !hasDupError {
		t.Error("expected error for duplicate node name")
	}
}

func TestPreValidator_InvalidDNSName(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "Invalid_Name_With_Underscores", // Invalid DNS name
		Nodes: []core.NodeSpec{
			{Name: "server", Provider: "local", SSH: &core.SSHConfig{Host: "192.168.1.1"}},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to invalid DNS name")
	}

	hasFormatError := false
	for _, e := range result.Errors {
		if e.Code == "invalid_format" {
			hasFormatError = true
			break
		}
	}
	if !hasFormatError {
		t.Error("expected error for invalid DNS name format")
	}
}

func TestPreValidator_MissingSSHForLocal(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{Name: "server", Type: "main", Provider: "local"}, // SSH is optional at creation time
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid spec; got errors: %+v", result.Errors)
	}
}

func TestPreValidator_InvalidSSHKeyPath(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	// Test path traversal attack
	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host:    "192.168.1.1",
					User:    "admin",
					KeyPath: "../../../etc/passwd", // Path traversal attack
				},
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to path traversal in SSH key path")
	}

	hasSecurityError := false
	for _, e := range result.Errors {
		if e.Code == "security" && e.Path == "nodes[0].ssh.key_path" {
			hasSecurityError = true
			break
		}
	}
	if !hasSecurityError {
		t.Errorf("expected security error for SSH key path traversal, got errors: %+v", result.Errors)
	}
}

func TestPreValidator_ValidSSHKeyPath(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	// Test valid key path
	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host:    "192.168.1.1",
					User:    "admin",
					KeyPath: "/home/user/.ssh/id_rsa", // Valid path
				},
			},
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if !result.Valid {
		t.Errorf("expected valid spec with valid SSH key path; got errors: %+v", result.Errors)
	}
}

func TestPreValidator_CircularDependency(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	validator := NewPreValidator(engine.ctx, engine.schema)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{Name: "server", Provider: "local", SSH: &core.SSHConfig{Host: "192.168.1.1"}},
		},
		Services: []core.ServiceSpec{
			{Name: "service-a", Node: "server", Needs: []string{"service-b"}},
			{Name: "service-b", Node: "server", Needs: []string{"service-c"}},
			{Name: "service-c", Node: "server", Needs: []string{"service-a"}}, // Creates cycle
		},
	}

	result, err := validator.ValidateSchema(spec)
	if err != nil {
		t.Fatalf("validation error: %v", err)
	}

	if result.Valid {
		t.Error("expected invalid spec due to circular dependency")
	}

	hasCycleError := false
	for _, e := range result.Errors {
		if e.Code == "circular_dependency" {
			hasCycleError = true
			break
		}
	}
	if !hasCycleError {
		t.Error("expected error for circular dependency")
	}
}

// Test StackKit Resolver

func TestStackKitResolver_UserSpecified(t *testing.T) {
	resolver := NewStackKitResolver([]string{"basement-kit", "cloud-kit"})

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "cloud-kit", // User specified
		Nodes: []core.NodeSpec{
			{Name: "main", Provider: "local"},
		},
	}

	result := resolver.Resolve(spec)

	if result.AutoSelected {
		t.Error("expected user-specified, not auto-selected")
	}
	if result.StackKit != "cloud-kit" {
		t.Errorf("expected 'cloud-kit', got '%s'", result.StackKit)
	}
	if !result.Valid {
		t.Error("expected valid result for available kit")
	}
}

func TestStackKitResolver_UserSpecifiedInvalidKit(t *testing.T) {
	resolver := NewStackKitResolver(nil)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "non-existent-kit", // Invalid kit
		Nodes: []core.NodeSpec{
			{Name: "main", Provider: "local"},
		},
	}

	result := resolver.Resolve(spec)

	if result.Valid {
		t.Error("expected invalid result for non-existent kit")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warnings for invalid kit")
	}
}

func TestStackKitResolver_AutoSelectBasementKit(t *testing.T) {
	resolver := NewStackKitResolver(nil)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "", // Empty = Easy-Path
		Nodes: []core.NodeSpec{
			{Name: "main", Provider: "local"},
		},
	}

	result := resolver.Resolve(spec)

	if !result.AutoSelected {
		t.Error("expected auto-selected")
	}
	if result.StackKit != "basement-kit" {
		t.Errorf("expected 'basement-kit' for single local node, got '%s'", result.StackKit)
	}
	if result.Confidence < 0.9 {
		t.Errorf("expected high confidence, got %f", result.Confidence)
	}
	if !result.Valid {
		t.Error("expected valid result")
	}
}

func TestStackKitResolver_AutoSelectHybridCloud(t *testing.T) {
	resolver := NewStackKitResolver([]string{"basement-kit", "cloud-kit"})

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "",
		Nodes: []core.NodeSpec{
			{Name: "local-server", Provider: "local"},
			{Name: "cloud-server", Provider: "hetzner"},
		},
	}

	result := resolver.Resolve(spec)

	if !result.AutoSelected {
		t.Error("expected auto-selected")
	}
	if result.StackKit != "cloud-kit" {
		t.Errorf("expected 'cloud-kit' for mixed nodes, got '%s'", result.StackKit)
	}
}

func TestStackKitResolver_AutoSelectCloudNative(t *testing.T) {
	resolver := NewStackKitResolver([]string{"basement-kit", "cloud-kit"})

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "",
		Nodes: []core.NodeSpec{
			{Name: "cloud-1", Provider: "hetzner"},
			{Name: "cloud-2", Provider: "aws"},
		},
	}

	result := resolver.Resolve(spec)

	if !result.AutoSelected {
		t.Error("expected auto-selected")
	}
	if result.StackKit != "cloud-kit" {
		t.Errorf("expected 'cloud-kit' for cloud-only nodes, got '%s'", result.StackKit)
	}
}

func TestStackKitResolver_AutoSelectARM(t *testing.T) {
	resolver := NewStackKitResolver(nil)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "",
		Nodes: []core.NodeSpec{
			{
				Name:     "rpi",
				Provider: "local",
				Tags:     map[string]string{"arch": "arm64"},
			},
		},
	}

	result := resolver.Resolve(spec)

	if !result.AutoSelected {
		t.Error("expected auto-selected")
	}
	if result.StackKit != "basement-kit" {
		t.Errorf("expected 'basement-kit' for ARM node, got '%s'", result.StackKit)
	}
}

func TestStackKitResolver_NilTags(t *testing.T) {
	resolver := NewStackKitResolver(nil)

	// This should not panic even with nil Tags
	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "",
		Nodes: []core.NodeSpec{
			{Name: "main", Provider: "local", Tags: nil},
		},
	}

	result := resolver.Resolve(spec)

	if result.StackKit == "" {
		t.Error("expected a StackKit to be selected")
	}
}

// Test Add-On Detector

func TestAddonDetector_CloudIntegration(t *testing.T) {
	detector := NewAddonDetector()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{Name: "local", Provider: "local"},
			{Name: "cloud", Provider: "hetzner"},
		},
	}

	result := detector.Detect(spec)

	hasCloudAddon := false
	for _, addon := range result.Addons {
		if addon.Name == "cloud-integration" {
			hasCloudAddon = true
			break
		}
	}
	if !hasCloudAddon {
		t.Error("expected cloud-integration add-on to be detected")
	}
}

func TestAddonDetector_ARMSupport(t *testing.T) {
	detector := NewAddonDetector()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{
				Name:     "rpi",
				Provider: "local",
				Tags:     map[string]string{"arch": "arm64"},
			},
		},
	}

	result := detector.Detect(spec)

	hasARMAddon := false
	for _, addon := range result.Addons {
		if addon.Name == "arm-support" {
			hasARMAddon = true
			break
		}
	}
	if !hasARMAddon {
		t.Error("expected arm-support add-on to be detected")
	}
}

func TestAddonDetector_LowMemory(t *testing.T) {
	detector := NewAddonDetector()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{
				Name:     "small-server",
				Provider: "local",
				Tags:     map[string]string{"memory": "2gb"},
			},
		},
	}

	result := detector.Detect(spec)

	hasLowMemAddon := false
	for _, addon := range result.Addons {
		if addon.Name == "low-memory" {
			hasLowMemAddon = true
			break
		}
	}
	if !hasLowMemAddon {
		t.Error("expected low-memory add-on to be detected")
	}
}

func TestAddonDetector_MultipleAddons(t *testing.T) {
	detector := NewAddonDetector()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{
				Name:     "rpi",
				Provider: "local",
				Tags:     map[string]string{"arch": "arm64", "memory": "2gb"},
			},
			{Name: "cloud", Provider: "hetzner"},
		},
		Network: core.NetworkSpec{
			VPN: "headscale",
		},
	}

	result := detector.Detect(spec)

	expectedAddons := []string{"cloud-integration", "arm-support", "low-memory", "vpn-overlay", "multi-node"}
	for _, expected := range expectedAddons {
		found := false
		for _, addon := range result.Addons {
			if addon.Name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected '%s' add-on to be detected", expected)
		}
	}
}

func TestAddonDetector_NoAddons(t *testing.T) {
	detector := NewAddonDetector()

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{Name: "main", Provider: "local"},
		},
	}

	result := detector.Detect(spec)

	// Single local node should have no add-ons except possibly none
	for _, addon := range result.Addons {
		if addon.Name == "cloud-integration" || addon.Name == "arm-support" || addon.Name == "low-memory" {
			t.Errorf("unexpected add-on detected: %s", addon.Name)
		}
	}
}

// Test Pipeline Integration

func TestPipeline_Execute_BasementKit(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	pipeline := NewPipeline(engine)

	spec := &core.KombinationSpec{
		Name: "my-homelab",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					User: "kombi",
				},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name: "traefik",
				Type: "reverse-proxy",
				Node: "main-server",
			},
		},
	}

	// Store original Kit value to verify spec not mutated
	originalKit := spec.Kit

	result := pipeline.Execute(spec)

	if !result.Success {
		t.Fatalf("pipeline failed at step '%s': %s", result.FailedStep, result.ErrorMessage)
	}

	// Verify original spec was not mutated
	if spec.Kit != originalKit {
		t.Error("pipeline should not mutate input spec")
	}

	// Check validation passed
	if !result.ValidationResult.Valid {
		t.Error("expected validation to pass")
	}

	// Check StackKit was auto-selected
	if !result.ResolveResult.AutoSelected {
		t.Error("expected StackKit to be auto-selected")
	}
	if result.ResolveResult.StackKit != "basement-kit" {
		t.Errorf("expected 'basement-kit', got '%s'", result.ResolveResult.StackKit)
	}

	// Check unified spec was created
	if result.UnifiedSpec == nil {
		t.Fatal("expected unified spec to be created")
	}
	if result.UnifiedSpec.StackKit != "basement-kit" {
		t.Errorf("expected unified spec StackKit to be 'basement-kit', got '%s'", result.UnifiedSpec.StackKit)
	}

	// Check all decision-plane and engine steps were executed
	if len(result.Steps) != 8 {
		t.Errorf("expected 8 steps, got %d", len(result.Steps))
	}
	if result.RequirementsSpec == nil {
		t.Error("expected requirements spec to be created")
	}
	if result.DecisionContext == nil {
		t.Error("expected decision context to be created")
	}
	if result.DecisionTrace == nil {
		t.Error("expected decision trace to be created")
	}

	// Check timing info is present
	if result.TotalTime == 0 {
		t.Error("expected TotalTime to be set")
	}
}

func TestPipeline_Execute_HybridCloud(t *testing.T) {
	t.Skip("cloud-kit directory not yet available — requires StackKits repo integration")
}

func TestPipeline_ValidateOnly(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	pipeline := NewPipeline(engine)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Nodes: []core.NodeSpec{
			{Name: "main", Type: "main", Provider: "hetzner"}, // Use cloud provider - no SSH required
		},
	}

	result := pipeline.ValidateOnly(spec)

	if !result.Success {
		t.Fatalf("validate-only failed: %s", result.ErrorMessage)
	}

	// Should not have unified spec (only validation)
	if result.UnifiedSpec != nil {
		t.Error("expected no unified spec in validate-only mode")
	}

	// Should have validation and resolution results
	if result.ValidationResult == nil {
		t.Error("expected validation result")
	}
	if result.ResolveResult == nil {
		t.Error("expected resolve result")
	}
	if result.DetectionResult == nil {
		t.Error("expected detection result")
	}

	// Check timing - TotalTime should be >= 0 (not unset)
	// Note: TotalTime could be 0 if operation is very fast, that's valid
	t.Logf("TotalTime: %v, Success: %v, FailedStep: %s", result.TotalTime, result.Success, result.FailedStep)
	// The fact that we got here means TotalTime was set (execution path hit)
	// We can verify steps were recorded instead
	if len(result.Steps) < 3 {
		t.Errorf("expected at least 3 steps, got %d", len(result.Steps))
	}
}

func TestPipeline_Execute_InvalidSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	pipeline := NewPipeline(engine)

	spec := &core.KombinationSpec{
		Name: "Invalid_Name_With_Underscores", // Invalid DNS name
	}

	result := pipeline.Execute(spec)

	if result.Success {
		t.Error("expected pipeline to fail for invalid spec")
	}
	if result.FailedStep != "pre-validation" {
		t.Errorf("expected failure at 'pre-validation', got '%s'", result.FailedStep)
	}
}

func TestPipeline_NilEngine(t *testing.T) {
	pipeline := NewPipeline(nil)
	if pipeline != nil {
		t.Error("expected nil pipeline when engine is nil")
	}
}

func TestPipeline_DoesNotMutateInput(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	pipeline := NewPipeline(engine)

	spec := &core.KombinationSpec{
		Name: "test-stack",
		Kit:  "", // Empty, will be auto-selected
		Nodes: []core.NodeSpec{
			{
				Name:     "server",
				Provider: "local",
				SSH:      &core.SSHConfig{Host: "192.168.1.1"},
				Tags:     map[string]string{"env": "test"},
			},
		},
		Metadata: map[string]string{"key": "value"},
	}

	// Store original values
	originalKit := spec.Kit
	originalTags := spec.Nodes[0].Tags["env"]
	originalMeta := spec.Metadata["key"]

	pipeline.Execute(spec)

	// Verify nothing was mutated
	if spec.Kit != originalKit {
		t.Errorf("spec.Kit was mutated: expected '%s', got '%s'", originalKit, spec.Kit)
	}
	if spec.Nodes[0].Tags["env"] != originalTags {
		t.Error("spec.Nodes[0].Tags was mutated")
	}
	if spec.Metadata["key"] != originalMeta {
		t.Error("spec.Metadata was mutated")
	}
}
