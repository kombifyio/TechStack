package unifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// GenerateMultiNodeTFVars generates terraform.tfvars for multi-node StackKits.
func (g *IaCGenerator) GenerateMultiNodeTFVars(config *IaCConfig) (*MultiNodeTFVars, error) {
	vars := &MultiNodeTFVars{
		StackName:      config.StackName,
		StackKit:       config.StackKit,
		Variant:        config.Variant,
		ComputeTier:    config.ComputeTier,
		NetworkMode:    config.NetworkMode,
		Domain:         config.Domain,
		ACMEEmail:      config.ACMEEmail,
		VPNType:        config.VPNType,
		VPNEnabled:     config.VPNType != "",
		EnableFirewall: config.EnableFirewall,
		AllowedPorts:   config.AllowedPorts,
	}

	// Configure Swarm settings
	if config.SwarmConfig != nil && config.SwarmConfig.Enabled {
		vars.SwarmEnabled = true
		vars.SwarmMode = config.SwarmConfig.Mode
		vars.SwarmManagerCount = config.SwarmConfig.ManagerCount
		vars.SwarmEncrypted = config.SwarmConfig.EncryptOverlay
	} else if len(config.Nodes) > 1 {
		// Auto-enable Swarm for multi-node without explicit config
		vars.SwarmEnabled = true
		managers := config.GetManagerNodes()
		if len(managers) >= 3 {
			vars.SwarmMode = "ha"
		} else {
			vars.SwarmMode = iacSwarmModeSingleManager
		}
		vars.SwarmManagerCount = len(managers)
		vars.SwarmEncrypted = true
	}

	// Convert nodes
	for i, node := range config.Nodes {
		nodeVar := NodeTFVar{
			Name:       node.Name,
			IP:         node.IP,
			PrivateIP:  node.PrivateIP,
			PublicIP:   node.PublicIP,
			Role:       node.Role,
			SSHUser:    node.SSHUser,
			SSHPort:    node.SSHPort,
			SSHKeyPath: node.SSHKeyPath,
			IsFirst:    i == 0 && node.Role == iacNodeRoleManager,
		}

		// Apply defaults
		if nodeVar.SSHUser == "" {
			nodeVar.SSHUser = config.SSHUser
		}
		if nodeVar.SSHPort == 0 {
			nodeVar.SSHPort = config.SSHPort
		}
		if nodeVar.SSHKeyPath == "" {
			nodeVar.SSHKeyPath = config.SSHKeyPath
		}
		if nodeVar.IP == "" {
			nodeVar.IP = node.PrivateIP
			if nodeVar.IP == "" {
				nodeVar.IP = node.PublicIP
			}
		}

		vars.Nodes = append(vars.Nodes, nodeVar)
	}

	// TLS configuration
	vars.EnableHTTPS = config.TLSMode != iacTLSModeNone
	vars.EnableLetsEnc = config.TLSMode == iacTLSModeACME

	return vars, nil
}

// GenerateModernKitOutput generates IaC for modern-homelab (multi-node Docker Swarm).
func (g *IaCGenerator) GenerateModernKitOutput(config *IaCConfig, stackkitsDir string) (*IaCOutput, error) {
	output := &IaCOutput{Files: make(map[string]string), Mode: g.Mode, OutputDir: g.OutputDir}

	// Determine template source directory
	templateDir := filepath.Join(stackkitsDir, StackKitModernHomelab, "templates", iacModeSimple)
	if g.Mode == iacModeAdvanced {
		templateDir = filepath.Join(stackkitsDir, StackKitModernHomelab, "templates", iacModeAdvanced)
	}

	// Check if StackKits directory exists, fall back to basement-kit
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		output.Errors = append(output.Errors, "warning: modern-homelab templates not found, using basement-kit")
		return iacFallbackOutput(output, func() (*IaCOutput, error) { return g.GenerateBaseKitOutput(config, stackkitsDir) })
	}

	// Read and copy all .tf files from StackKits
	if err := g.copyTemplateFiles(templateDir, output); err != nil {
		// Fall back to basement-kit if modern-homelab templates are incomplete
		output.Errors = append(output.Errors, fmt.Sprintf("warning: modern-homelab templates incomplete: %v", err))
		return iacFallbackOutput(output, func() (*IaCOutput, error) { return g.GenerateBaseKitOutput(config, stackkitsDir) })
	}

	// Generate multi-node tfvars
	vars, err := g.GenerateMultiNodeTFVars(config)
	if err != nil {
		return nil, fmt.Errorf("failed to generate multi-node tfvars: %w", err)
	}

	tfvarsJSON, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tfvars: %w", err)
	}
	output.Files["terraform.tfvars.json"] = string(tfvarsJSON)
	output.TFVarsJSON = tfvarsJSON

	return output, nil
}

// GenerateHAKitOutput generates IaC for ha-kit (Docker Swarm HA).
func (g *IaCGenerator) GenerateHAKitOutput(config *IaCConfig, stackkitsDir string) (*IaCOutput, error) {
	output := &IaCOutput{Files: make(map[string]string), Mode: g.Mode, OutputDir: g.OutputDir}

	// Validate HA requirements
	managers := config.GetManagerNodes()
	if len(managers) < 3 {
		output.Errors = append(output.Errors, fmt.Sprintf("ha-kit requires at least 3 manager nodes for HA quorum, got %d", len(managers)))
	}

	// Determine template source directory
	templateDir := filepath.Join(stackkitsDir, "ha-kit", "templates", iacModeSimple)
	if g.Mode == iacModeAdvanced {
		templateDir = filepath.Join(stackkitsDir, "ha-kit", "templates", iacModeAdvanced)
	}

	// Check if StackKits directory exists, fall back to modern-homelab
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		output.Errors = append(output.Errors, "warning: ha-kit templates not found, using modern-homelab")
		return iacFallbackOutput(output, func() (*IaCOutput, error) { return g.GenerateModernKitOutput(config, stackkitsDir) })
	}

	// Read and copy all .tf files from StackKits
	if err := g.copyTemplateFiles(templateDir, output); err != nil {
		// Fall back to modern-homelab if ha-kit templates are incomplete
		output.Errors = append(output.Errors, fmt.Sprintf("warning: ha-kit templates incomplete: %v", err))
		return iacFallbackOutput(output, func() (*IaCOutput, error) { return g.GenerateModernKitOutput(config, stackkitsDir) })
	}

	// Ensure Swarm is configured for HA
	if config.SwarmConfig == nil {
		config.SwarmConfig = &SwarmConfig{
			Enabled:        true,
			Mode:           "ha",
			ManagerCount:   len(managers),
			EncryptOverlay: true,
		}
	}

	// Generate multi-node tfvars
	vars, err := g.GenerateMultiNodeTFVars(config)
	if err != nil {
		return nil, fmt.Errorf("failed to generate HA tfvars: %w", err)
	}

	tfvarsJSON, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tfvars: %w", err)
	}
	output.Files["terraform.tfvars.json"] = string(tfvarsJSON)
	output.TFVarsJSON = tfvarsJSON

	return output, nil
}

func iacFallbackOutput(attempt *IaCOutput, fallback func() (*IaCOutput, error)) (*IaCOutput, error) {
	output, err := fallback()
	if err == nil {
		attempt.Errors = slices.DeleteFunc(attempt.Errors, func(err string) bool { return !strings.HasPrefix(strings.ToLower(err), "warning:") })
		output.Errors, output.Warnings = append(attempt.Errors, output.Errors...), append(attempt.Warnings, output.Warnings...)
	}
	return output, err
}

// copyTemplateFiles copies .tf files and .tpl files from a template directory to output.
func (g *IaCGenerator) copyTemplateFiles(templateDir string, output *IaCOutput) error {
	entries, err := os.ReadDir(templateDir)
	if err != nil {
		return fmt.Errorf("failed to read template directory: %w", err)
	}

	for _, entry := range entries {
		if isInertTemplateEntry(entry) {
			continue
		}
		if err := copyTemplateEntry(templateDir, entry.Name(), output); err != nil {
			return err
		}
	}

	if !hasTerraformOrTerramateOutput(output.Files) {
		return fmt.Errorf("no template files found in directory: %s", templateDir)
	}

	return nil
}

func copyTemplateEntry(templateDir, name string, output *IaCOutput) error {
	if !isTemplateOutputFile(name) {
		return nil
	}

	content, err := os.ReadFile(filepath.Join(templateDir, name))
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", name, err)
	}

	output.Files[templateOutputName(name)] = string(content)
	return nil
}

func isInertTemplateEntry(entry os.DirEntry) bool {
	return entry.IsDir() || strings.HasSuffix(entry.Name(), ".example") || strings.HasSuffix(entry.Name(), ".md")
}

func templateOutputName(name string) string { return strings.TrimSuffix(name, ".tpl") }

func isTemplateOutputFile(name string) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tpl")
}

func hasTerraformOrTerramateOutput(files map[string]string) bool {
	for name := range files {
		if strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tm.hcl") {
			return true
		}
	}
	return false
}
