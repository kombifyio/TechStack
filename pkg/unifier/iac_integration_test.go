//go:build integration
// +build integration

// Package unifier contains integration tests for IaC generation.
// These tests require external dependencies (StackKits repo, tofu binary).
package unifier

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_GenerateAndValidateWithTofu is an integration test that:
// 1. Loads templates from actual StackKits repository
// 2. Generates tfvars.json
// 3. Validates with `tofu validate`
//
// This test is skipped if:
// - TECHSTACK_STACKKITS_DIR env var is not set
// - tofu binary is not available
func TestIntegration_GenerateAndValidateWithTofu(t *testing.T) {
	// Check for StackKits directory
	stackkitsDir := os.Getenv("TECHSTACK_STACKKITS_DIR")
	if stackkitsDir == "" {
		// Try default relative path for workspace
		stackkitsDir = filepath.Join("..", "..", "..", "StackKits")
		if _, err := os.Stat(stackkitsDir); os.IsNotExist(err) {
			t.Skip("Skipping integration test: TECHSTACK_STACKKITS_DIR not set and StackKits repo not found")
		}
	}

	// Check for tofu binary
	tofuPath, err := exec.LookPath("tofu")
	if err != nil {
		t.Skip("Skipping integration test: tofu binary not found in PATH")
	}
	t.Logf("Using tofu at: %s", tofuPath)

	// Verify StackKits basement-kit exists
	baseHomelabDir := filepath.Join(stackkitsDir, "basement-kit", "templates", "simple")
	if _, err := os.Stat(baseHomelabDir); os.IsNotExist(err) {
		t.Fatalf("StackKits basement-kit not found at: %s", baseHomelabDir)
	}

	tests := []struct {
		name    string
		config  *IaCConfig
		wantErr bool
	}{
		{
			name: "default variant with ports mode",
			config: &IaCConfig{
				StackName:   "test-homelab",
				StackKit:    "basement-kit",
				Variant:     "default",
				ComputeTier: "standard",
				WorkerIP:    "192.168.1.100",
			},
		},
		{
			name: "beszel variant with proxy mode",
			config: &IaCConfig{
				StackName:   "test-homelab-beszel",
				StackKit:    "basement-kit",
				Variant:     "beszel",
				ComputeTier: "high",
				Domain:      "homelab.example.com",
				ACMEEmail:   "admin@example.com",
			},
		},
		{
			name: "minimal variant with low compute",
			config: &IaCConfig{
				StackName:   "test-homelab-minimal",
				StackKit:    "basement-kit",
				Variant:     "minimal",
				ComputeTier: "low",
				WorkerIP:    "10.0.0.1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp output directory
			tmpOutput := t.TempDir()
			gen := NewIaCGenerator(tmpOutput)

			// Generate output
			output, err := gen.GenerateBaseKitOutput(tt.config, stackkitsDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GenerateBaseKitOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			// Verify main.tf is from StackKits
			if mainContent, ok := output.Files["main.tf"]; !ok {
				t.Fatal("main.tf not found in output")
			} else {
				// Should contain StackKit header
				if !strings.Contains(mainContent, "Base Homelab") && !strings.Contains(mainContent, "basement-kit") {
					t.Logf("Warning: main.tf might not be from StackKits (first 100 chars): %s", mainContent[:min(100, len(mainContent))])
				}
			}

			// Verify tfvars.json has correct values
			var tfvars map[string]interface{}
			if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err != nil {
				t.Fatalf("Failed to parse tfvars.json: %v", err)
			}

			if tfvars["variant"] != tt.config.Variant {
				t.Errorf("variant = %v, want %s", tfvars["variant"], tt.config.Variant)
			}

			// Write files to temp directory
			if err := gen.WriteOutput(output); err != nil {
				t.Fatalf("WriteOutput failed: %v", err)
			}

			// Run tofu init + validate
			t.Logf("Running tofu init in %s", tmpOutput)
			initCmd := exec.Command(tofuPath, "init", "-backend=false")
			initCmd.Dir = tmpOutput
			if initOut, err := initCmd.CombinedOutput(); err != nil {
				t.Fatalf("tofu init failed: %v\nOutput: %s", err, initOut)
			}

			t.Logf("Running tofu validate in %s", tmpOutput)
			validateCmd := exec.Command(tofuPath, "validate")
			validateCmd.Dir = tmpOutput
			if validateOut, err := validateCmd.CombinedOutput(); err != nil {
				t.Fatalf("tofu validate failed: %v\nOutput: %s", err, validateOut)
			}

			t.Logf("✓ tofu validate passed for %s", tt.name)
		})
	}
}

// TestIntegration_EndToEndPipeline tests the full pipeline:
// kombination.yaml → IaCConfig → tfvars.json + templates
func TestIntegration_EndToEndPipeline(t *testing.T) {
	stackkitsDir := os.Getenv("TECHSTACK_STACKKITS_DIR")
	if stackkitsDir == "" {
		stackkitsDir = filepath.Join("..", "..", "..", "StackKits")
		if _, err := os.Stat(stackkitsDir); os.IsNotExist(err) {
			t.Skip("Skipping integration test: StackKits repo not found")
		}
	}

	// Simulate loading from kombination.yaml
	// In real pipeline, this would be: LoadSpec → ValidateWithCUE → ToIaCConfig
	config := &IaCConfig{
		StackName:   "my-homelab",
		StackKit:    "basement-kit",
		Mode:        "simple",
		Variant:     "default",
		WorkerIP:    "192.168.1.100",
		SSHUser:     "ubuntu",
		SSHPort:     22,
		OSType:      "ubuntu-24",
		ComputeTier: "standard",
		NetworkMode: "local",
		TLSMode:     "none",
	}

	tmpOutput := t.TempDir()
	gen := NewIaCGenerator(tmpOutput)

	// Generate full output
	err := gen.GenerateAndWrite(config, stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateAndWrite failed: %v", err)
	}

	// Verify files exist
	expectedFiles := []string{"main.tf", "terraform.tfvars.json"}
	for _, f := range expectedFiles {
		path := filepath.Join(tmpOutput, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Expected file not found: %s", f)
		}
	}

	// Read and verify tfvars
	tfvarsPath := filepath.Join(tmpOutput, "terraform.tfvars.json")
	tfvarsData, err := os.ReadFile(tfvarsPath)
	if err != nil {
		t.Fatalf("Failed to read tfvars: %v", err)
	}

	var tfvars map[string]interface{}
	if err := json.Unmarshal(tfvarsData, &tfvars); err != nil {
		t.Fatalf("Failed to parse tfvars: %v", err)
	}

	// Verify key values from config made it to tfvars
	if tfvars["access_mode"] != "ports" {
		t.Errorf("access_mode = %v, want ports", tfvars["access_mode"])
	}
	if tfvars["variant"] != "default" {
		t.Errorf("variant = %v, want default", tfvars["variant"])
	}

	t.Log("✓ End-to-end pipeline test passed")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
