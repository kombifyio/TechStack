// Package unifier provides OpenTofu configuration generation.
package unifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/pkg/core"
)

// Generator produces OpenTofu-compatible configurations from unified specs.
type Generator struct {
	// OutputDir is the directory where generated files are written
	OutputDir string
}

// NewGenerator creates a new Generator instance.
func NewGenerator(outputDir string) *Generator {
	return &Generator{
		OutputDir: outputDir,
	}
}

// TFVars represents the structure of terraform.tfvars.json
type TFVars struct {
	// Stack metadata
	StackName string `json:"stack_name"`
	StackKit  string `json:"stack_kit"`

	// Node configurations
	Nodes []TFNode `json:"nodes"`

	// Service configurations
	Services []TFService `json:"services"`

	// Network configuration
	Network TFNetwork `json:"network"`
}

// TFNode represents a node in tfvars format
type TFNode struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Provider  string            `json:"provider"`
	Host      string            `json:"host,omitempty"`
	SSHUser   string            `json:"ssh_user,omitempty"`
	SSHPort   int               `json:"ssh_port,omitempty"`
	SSHKey    string            `json:"ssh_key,omitempty"`
	PrivateIP string            `json:"private_ip,omitempty"`
	PublicIP  string            `json:"public_ip,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// TFService represents a service in tfvars format
type TFService struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Node        string            `json:"node"`
	Image       string            `json:"image,omitempty"`
	Port        int               `json:"port,omitempty"`
	Needs       []string          `json:"needs,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

// TFNetwork represents network config in tfvars format
type TFNetwork struct {
	VPNType     string   `json:"vpn_type"`
	Domain      string   `json:"domain"`
	Subnet      string   `json:"subnet,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}

// Generate creates terraform.tfvars.json from a unified spec.
func (g *Generator) Generate(unified *core.UnifiedSpec) (*TFVars, error) {
	if unified == nil {
		return nil, fmt.Errorf("unified spec cannot be nil")
	}

	tfvars := &TFVars{
		StackName: unified.Name,
		StackKit:  unified.StackKit,
		Nodes:     make([]TFNode, 0, len(unified.ResolvedNodes)),
		Services:  make([]TFService, 0, len(unified.ResolvedServices)),
	}

	// Convert nodes
	for _, node := range unified.ResolvedNodes {
		tfNode := TFNode{
			Name:      node.Name,
			Type:      node.Type,
			Provider:  node.Provider,
			PrivateIP: node.PrivateIP,
			PublicIP:  node.PublicIP,
			Tags:      node.Tags,
		}

		if node.SSH != nil {
			tfNode.Host = node.SSH.Host
			tfNode.SSHUser = node.SSH.User
			tfNode.SSHPort = node.SSH.Port
			tfNode.SSHKey = node.SSH.KeyPath
		}

		tfvars.Nodes = append(tfvars.Nodes, tfNode)
	}

	// Convert services
	for _, svc := range unified.ResolvedServices {
		tfService := TFService{
			Name:        svc.Name,
			Type:        svc.Type,
			Node:        svc.Node,
			Image:       svc.Image,
			Port:        svc.Port,
			Needs:       svc.Needs,
			Environment: svc.Environment,
		}
		tfvars.Services = append(tfvars.Services, tfService)
	}

	// Convert network
	tfvars.Network = TFNetwork{
		VPNType:     unified.ResolvedNetwork.VPNType,
		Domain:      unified.ResolvedNetwork.Domain,
		Subnet:      unified.ResolvedNetwork.Subnet,
		Gateway:     unified.ResolvedNetwork.Gateway,
		Nameservers: unified.ResolvedNetwork.Nameservers,
	}

	return tfvars, nil
}

// WriteToFile writes TFVars to terraform.tfvars.json
func (g *Generator) WriteToFile(tfvars *TFVars) error {
	if err := os.MkdirAll(g.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	outputPath := filepath.Join(g.OutputDir, "terraform.tfvars.json")

	data, err := json.MarshalIndent(tfvars, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize tfvars: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write tfvars file: %w", err)
	}

	return nil
}

// GenerateAndWrite is a convenience method that generates and writes tfvars.
func (g *Generator) GenerateAndWrite(unified *core.UnifiedSpec) error {
	tfvars, err := g.Generate(unified)
	if err != nil {
		return err
	}

	return g.WriteToFile(tfvars)
}

// ToJSON returns the TFVars as a JSON byte slice.
func (g *Generator) ToJSON(tfvars *TFVars) ([]byte, error) {
	return json.MarshalIndent(tfvars, "", "  ")
}
