package unifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestGenerator_Generate(t *testing.T) {
	generator := NewGenerator("")

	unified := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test-stack",
			Kit:  "basement-kit",
		},
		ResolvedNodes: []core.ResolvedNode{
			{
				NodeSpec: core.NodeSpec{
					Name:     "main-server",
					Type:     "main",
					Provider: "local",
					SSH: &core.SSHConfig{
						Host: "192.168.1.100",
						Port: 22,
						User: "ubuntu",
					},
					Tags: map[string]string{"env": "prod"},
				},
				PrivateIP: "10.0.0.1",
			},
		},
		ResolvedServices: []core.ResolvedService{
			{
				ServiceSpec: core.ServiceSpec{
					Name:  "traefik",
					Type:  "reverse-proxy",
					Node:  "main-server",
					Needs: []string{"docker"},
				},
				Image: "traefik:latest",
				Port:  80,
			},
		},
		ResolvedNetwork: core.ResolvedNetwork{
			VPNType: "none",
			Domain:  "local",
		},
		StackKit: "basement-kit",
	}

	tfvars, err := generator.Generate(unified)
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Check stack metadata
	if tfvars.StackName != "test-stack" {
		t.Errorf("expected stack name 'test-stack', got '%s'", tfvars.StackName)
	}

	if tfvars.StackKit != "basement-kit" {
		t.Errorf("expected stack kit 'basement-kit', got '%s'", tfvars.StackKit)
	}

	// Check nodes
	if len(tfvars.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(tfvars.Nodes))
	}

	node := tfvars.Nodes[0]
	if node.Name != "main-server" {
		t.Errorf("expected node name 'main-server', got '%s'", node.Name)
	}
	if node.Host != "192.168.1.100" {
		t.Errorf("expected host '192.168.1.100', got '%s'", node.Host)
	}
	if node.SSHUser != "ubuntu" {
		t.Errorf("expected SSH user 'ubuntu', got '%s'", node.SSHUser)
	}
	if node.PrivateIP != "10.0.0.1" {
		t.Errorf("expected private IP '10.0.0.1', got '%s'", node.PrivateIP)
	}

	// Check services
	if len(tfvars.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(tfvars.Services))
	}

	svc := tfvars.Services[0]
	if svc.Name != "traefik" {
		t.Errorf("expected service name 'traefik', got '%s'", svc.Name)
	}
	if svc.Image != "traefik:latest" {
		t.Errorf("expected image 'traefik:latest', got '%s'", svc.Image)
	}

	// Check network
	if tfvars.Network.VPNType != "none" {
		t.Errorf("expected VPN type 'none', got '%s'", tfvars.Network.VPNType)
	}
	if tfvars.Network.Domain != "local" {
		t.Errorf("expected domain 'local', got '%s'", tfvars.Network.Domain)
	}
}

func TestGenerator_Generate_NilSpec(t *testing.T) {
	generator := NewGenerator("")

	_, err := generator.Generate(nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestGenerator_ToJSON(t *testing.T) {
	generator := NewGenerator("")

	tfvars := &TFVars{
		StackName: "test-stack",
		StackKit:  "basement-kit",
		Nodes: []TFNode{
			{Name: "main-server", Type: "main", Provider: "local"},
		},
		Services: []TFService{},
		Network:  TFNetwork{VPNType: "none", Domain: "local"},
	}

	data, err := generator.ToJSON(tfvars)
	if err != nil {
		t.Fatalf("ToJSON() failed: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("ToJSON() produced invalid JSON: %v", err)
	}

	if parsed["stack_name"] != "test-stack" {
		t.Errorf("expected stack_name 'test-stack' in JSON")
	}
}

func TestGenerator_WriteToFile(t *testing.T) {
	tmpDir := t.TempDir()
	generator := NewGenerator(tmpDir)

	tfvars := &TFVars{
		StackName: "test-stack",
		StackKit:  "basement-kit",
		Nodes: []TFNode{
			{Name: "main-server", Type: "main", Provider: "local"},
		},
		Services: []TFService{},
		Network:  TFNetwork{VPNType: "none", Domain: "local"},
	}

	err := generator.WriteToFile(tfvars)
	if err != nil {
		t.Fatalf("WriteToFile() failed: %v", err)
	}

	// Verify file exists
	expectedPath := filepath.Join(tmpDir, "terraform.tfvars.json")
	if _, statErr := os.Stat(expectedPath); os.IsNotExist(statErr) {
		t.Fatal("WriteToFile() did not create file")
	}

	// Verify content
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	var parsed TFVars
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Generated file is not valid JSON: %v", err)
	}

	if parsed.StackName != "test-stack" {
		t.Errorf("expected stack name 'test-stack' in file")
	}
}

func TestGenerator_GenerateAndWrite(t *testing.T) {
	tmpDir := t.TempDir()
	generator := NewGenerator(tmpDir)

	unified := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test-stack",
			Kit:  "basement-kit",
		},
		ResolvedNodes: []core.ResolvedNode{
			{
				NodeSpec: core.NodeSpec{
					Name:     "main-server",
					Type:     "main",
					Provider: "local",
				},
			},
		},
		ResolvedServices: []core.ResolvedService{},
		ResolvedNetwork: core.ResolvedNetwork{
			VPNType: "none",
			Domain:  "local",
		},
		StackKit: "basement-kit",
	}

	err := generator.GenerateAndWrite(unified)
	if err != nil {
		t.Fatalf("GenerateAndWrite() failed: %v", err)
	}

	// Verify file exists and has correct content
	expectedPath := filepath.Join(tmpDir, "terraform.tfvars.json")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	var parsed TFVars
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Generated file is not valid JSON: %v", err)
	}

	if parsed.StackName != "test-stack" {
		t.Errorf("expected stack name 'test-stack'")
	}
}
