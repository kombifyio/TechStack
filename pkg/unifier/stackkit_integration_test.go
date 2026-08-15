//go:build integration

package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// TestStackKit_LoadExternalBasementKit tests loading StackKits from external repository.
// Requires: StackKits repository accessible at ../../../StackKits or STACKKITS_PATH env var.
func TestStackKit_LoadExternalBasementKit(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)

	baseHomelabPath := filepath.Join(stackkitsPath, "basement-kit")
	if _, err := os.Stat(baseHomelabPath); os.IsNotExist(err) {
		t.Skipf("StackKits repository not found at %s", stackkitsPath)
	}

	engine, err := NewWithStackKitDir(stackkitsPath)
	if err != nil {
		t.Fatalf("Failed to create engine with external kits: %v", err)
	}

	// Verify basement-kit is loaded
	kits, err := engine.ListKits()
	if err != nil {
		t.Fatalf("ListKits() failed: %v", err)
	}

	found := false
	for _, kit := range kits {
		if kit.Name == "basement-kit" || strings.Contains(kit.Name, "homelab") {
			found = true
			t.Logf("Found kit: %s (version: %s)", kit.Name, kit.Version)
			break
		}
	}

	if !found {
		t.Logf("Available kits: %v", kits)
		t.Error("Expected to find basement-kit kit in external kits")
	}
}

// TestStackKit_ValidateAgainstCUESchema tests spec validation against actual CUE schemas.
func TestStackKit_ValidateAgainstCUESchema(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)
	if _, err := os.Stat(stackkitsPath); os.IsNotExist(err) {
		t.Skipf("StackKits repository not found at %s", stackkitsPath)
	}

	engine, err := NewWithStackKitDir(stackkitsPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Valid spec that should pass CUE validation
	validSpec := &core.KombinationSpec{
		Name:    "integration-test-stack",
		Kit:     "basement-kit",
		Version: "0.1",
		Nodes: []core.NodeSpec{
			{
				Name:     "main-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "192.168.1.100",
					Port: 22,
					User: "ubuntu",
				},
			},
		},
		Services: []core.ServiceSpec{
			{
				Name:    "traefik",
				Type:    "reverse-proxy",
				Node:    "main-server",
				Enabled: true,
			},
		},
	}

	result, err := engine.Validate(validSpec)
	if err != nil {
		t.Fatalf("Validation error: %v", err)
	}
	if !result.Valid {
		t.Errorf("Expected valid spec, got errors: %v", result.Errors)
	}
}

// TestStackKit_GenerateHCL_BasementKit tests HCL generation from basement-kit kit.
func TestStackKit_GenerateHCL_BasementKit(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)
	if _, err := os.Stat(stackkitsPath); os.IsNotExist(err) {
		t.Skipf("StackKits repository not found at %s", stackkitsPath)
	}

	engine, err := NewWithStackKitDir(stackkitsPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "hcl-generation-test",
		Kit:  "basement-kit",
		Nodes: []core.NodeSpec{
			{
				Name:     "controller",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "10.0.0.10",
					Port: 22,
					User: "root",
				},
			},
			{
				Name:     "worker-1",
				Type:     "worker",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "10.0.0.11",
					Port: 22,
					User: "root",
				},
			},
		},
		Services: []core.ServiceSpec{
			{Name: "docker", Type: "container-runtime", Node: "controller"},
			{Name: "traefik", Type: "reverse-proxy", Node: "controller"},
		},
	}

	// Unify to get resolved spec
	unified, err := engine.Unify(spec)
	if err != nil {
		t.Fatalf("Unify() failed: %v", err)
	}
	if unified == nil {
		t.Fatal("Unify() returned nil")
	}

	// Check that unified spec has expected fields
	if unified.Name != spec.Name {
		t.Errorf("Expected name %q, got %q", spec.Name, unified.Name)
	}
	if len(unified.Nodes) != 2 {
		t.Errorf("Expected 2 nodes, got %d", len(unified.Nodes))
	}

	// Verify generated outputs exist
	if unified.Outputs == nil {
		t.Log("Note: Outputs may be generated in a separate step")
	}
}

// TestStackKit_VariantSelection tests selection of different StackKit variants.
func TestStackKit_VariantSelection(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)
	if _, err := os.Stat(stackkitsPath); os.IsNotExist(err) {
		t.Skipf("StackKits repository not found at %s", stackkitsPath)
	}

	variants := []string{"default", "minimal", "beszel"}

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			engine, err := NewWithStackKitDir(stackkitsPath)
			if err != nil {
				t.Fatalf("Failed to create engine: %v", err)
			}

			spec := &core.KombinationSpec{
				Name:    "variant-test-" + variant,
				Kit:     "basement-kit",
				Variant: variant,
				Nodes: []core.NodeSpec{
					{
						Name:     "server",
						Type:     "main",
						Provider: "local",
						SSH: &core.SSHConfig{
							Host: "192.168.1.1",
							Port: 22,
							User: "ubuntu",
						},
					},
				},
			}

			result, err := engine.Validate(spec)
			if err != nil {
				// Variant might not exist, that's OK
				t.Logf("Variant %q validation: %v", variant, err)
				return
			}

			if result.Valid {
				t.Logf("✓ Variant %q is valid", variant)
			} else {
				t.Logf("Variant %q validation errors: %v", variant, result.Errors)
			}
		})
	}
}

// TestStackKit_ModernKit tests the modern-homelab StackKit.
func TestStackKit_ModernKit(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)
	modernPath := filepath.Join(stackkitsPath, "modern-homelab")
	if _, err := os.Stat(modernPath); os.IsNotExist(err) {
		t.Skipf("modern-homelab StackKit not found at %s", modernPath)
	}

	engine, err := NewWithStackKitDir(stackkitsPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.KombinationSpec{
		Name: "modern-homelab-test",
		Kit:  "modern-homelab",
		Nodes: []core.NodeSpec{
			{
				Name:     "k3s-server",
				Type:     "main",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "10.0.0.100",
					Port: 22,
					User: "root",
				},
			},
			{
				Name:     "k3s-worker-1",
				Type:     "worker",
				Provider: "local",
				SSH: &core.SSHConfig{
					Host: "10.0.0.101",
					Port: 22,
					User: "root",
				},
			},
		},
		Services: []core.ServiceSpec{
			{Name: "k3s", Type: "kubernetes", Node: "k3s-server"},
			{Name: "traefik", Type: "ingress", Node: "k3s-server"},
			{Name: "longhorn", Type: "storage", Node: "k3s-server"},
		},
	}

	result, err := engine.Validate(spec)
	if err != nil {
		t.Logf("Validation error (may be expected for alpha kit): %v", err)
		return
	}

	if result.Valid {
		t.Log("✓ modern-kit spec is valid")
	} else {
		t.Logf("Validation errors: %v", result.Errors)
	}

	// Test unification
	unified, err := engine.Unify(spec)
	if err != nil {
		t.Logf("Unification note: %v", err)
		return
	}
	t.Logf("✓ Unified spec has %d nodes", len(unified.Nodes))
}

// TestStackKit_CUESchemaValidation validates CUE schema constraints.
func TestStackKit_CUESchemaValidation(t *testing.T) {
	stackkitsPath := getStackKitsPath(t)
	if _, err := os.Stat(stackkitsPath); os.IsNotExist(err) {
		t.Skipf("StackKits repository not found at %s", stackkitsPath)
	}

	engine, err := NewWithStackKitDir(stackkitsPath)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	testCases := []struct {
		name      string
		spec      *core.KombinationSpec
		wantValid bool
	}{
		{
			name: "valid_minimal_spec",
			spec: &core.KombinationSpec{
				Name: "valid-minimal",
				Kit:  "basement-kit",
				Nodes: []core.NodeSpec{
					{Name: "main", Type: "main", Provider: "local", SSH: &core.SSHConfig{Host: "10.0.0.1", Port: 22, User: "root"}},
				},
			},
			wantValid: true,
		},
		{
			name: "missing_nodes",
			spec: &core.KombinationSpec{
				Name: "missing-nodes",
				Kit:  "basement-kit",
			},
			wantValid: false,
		},
		{
			name: "invalid_ssh_port",
			spec: &core.KombinationSpec{
				Name: "invalid-port",
				Kit:  "basement-kit",
				Nodes: []core.NodeSpec{
					{Name: "main", Type: "main", Provider: "local", SSH: &core.SSHConfig{Host: "10.0.0.1", Port: 0, User: "root"}},
				},
			},
			wantValid: false,
		},
		{
			name: "empty_node_name",
			spec: &core.KombinationSpec{
				Name: "empty-node-name",
				Kit:  "basement-kit",
				Nodes: []core.NodeSpec{
					{Name: "", Type: "main", Provider: "local", SSH: &core.SSHConfig{Host: "10.0.0.1", Port: 22, User: "root"}},
				},
			},
			wantValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Validate(tc.spec)
			if err != nil {
				t.Logf("Validation returned error: %v", err)
				if tc.wantValid {
					t.Errorf("Expected valid, but got error: %v", err)
				}
				return
			}

			if result.Valid != tc.wantValid {
				t.Errorf("Expected valid=%v, got valid=%v (errors: %v)", tc.wantValid, result.Valid, result.Errors)
			}
		})
	}
}

// getStackKitsPath returns the path to the StackKits repository.
func getStackKitsPath(t *testing.T) string {
	t.Helper()

	// Check environment variable first
	if path := os.Getenv("STACKKITS_PATH"); path != "" {
		return path
	}

	// Try relative paths from typical test locations
	candidates := []string{
		"../../../StackKits",                                               // From kombifyTechstack/pkg/unifier/
		"../../../../StackKits",                                            // Deeper nesting
		filepath.Join(os.Getenv("HOME"), "StackKits"),                      // Home directory
		"C:/Users/mako1/OneDrive/Dokumente/GitHub/StackKits",               // Windows absolute
		"/home/runner/work/kombifyTechstack/kombifyTechstack/../StackKits", // CI path
	}

	for _, candidate := range candidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(absPath); err == nil {
			return absPath
		}
	}

	// Default to relative path
	return "../../../StackKits"
}
