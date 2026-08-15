// Package unifier provides IaC (Infrastructure as Code) generation from UnifiedSpec.
// This is the Phase 6 implementation for the IaC-First Architecture.
package unifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/kombifyio/techstack/pkg/core"
)

// IaCGenerator produces full OpenTofu/Terramate configurations from UnifiedSpec.
// It generates files from external StackKit templates; set StackKitDir before calling Generate.
type IaCGenerator struct {
	OutputDir        string
	StackKitDir      string
	Mode             string // "simple" or "advanced"
	ExternalStackKit string // Path to external StackKit (if not embedded)
	funcs            template.FuncMap
}

// NewIaCGenerator creates a new IaC generator.
func NewIaCGenerator(outputDir string) *IaCGenerator {
	g := &IaCGenerator{
		OutputDir: outputDir,
		Mode:      iacModeSimple,
	}
	g.initFuncMap()
	return g
}

// WithMode sets the deployment mode (simple or advanced).
func (g *IaCGenerator) WithMode(mode string) *IaCGenerator {
	if mode == iacModeAdvanced || mode == iacModeSimple {
		g.Mode = mode
	}
	return g
}

// WithStackKitDir sets the external StackKit directory.
func (g *IaCGenerator) WithStackKitDir(dir string) *IaCGenerator {
	g.StackKitDir = strings.TrimSpace(dir)
	return g
}

// initFuncMap initializes template helper functions.
func (g *IaCGenerator) initFuncMap() {
	g.funcs = newIaCTemplateFuncMap()
}

// GenerateFromUnifiedSpec creates IaC configuration from a UnifiedSpec.
func (g *IaCGenerator) GenerateFromUnifiedSpec(unified *core.UnifiedSpec) (*IaCOutput, error) {
	if unified == nil {
		return nil, fmt.Errorf("unified spec cannot be nil")
	}

	// Convert UnifiedSpec to IaCConfig
	config, err := g.unifiedToIaCConfig(unified)
	if err != nil {
		return nil, fmt.Errorf("failed to convert unified spec: %w", err)
	}

	return g.Generate(config)
}

// Generate creates all IaC files from an IaCConfig.
func (g *IaCGenerator) Generate(config *IaCConfig) (*IaCOutput, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if g.StackKitDir != "" {
		return g.GenerateStackKitOutput(config, g.StackKitDir)
	}

	return nil, fmt.Errorf("StackKits directory is required: TechStack no longer generates StackKit HCL from embedded templates; set TECHSTACK_STACKKITS_DIR or STACKKITS_REPO")
}

// WriteToDir writes all generated files to the output directory.
func (g *IaCGenerator) WriteToDir(output *IaCOutput) error {
	if err := os.MkdirAll(g.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	for filename, content := range output.Files {
		filePath := filepath.Join(g.OutputDir, filename)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", filename, err)
		}
	}

	return nil
}

// WriteOutput writes the generated IaC output to the output directory.
func (g *IaCGenerator) WriteOutput(output *IaCOutput) error {
	if output == nil {
		return fmt.Errorf("output is nil")
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(g.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write each file
	for filename, content := range output.Files {
		filePath := filepath.Join(g.OutputDir, filename)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", filename, err)
		}
	}

	return nil
}

// GenerateAndWrite is a convenience method that generates and writes in one call.
func (g *IaCGenerator) GenerateAndWrite(config *IaCConfig, stackkitsDir string) error {
	var output *IaCOutput
	var err error

	// Use StackKit-specific generation based on stack kit type
	if stackkitsDir != "" {
		output, err = g.GenerateStackKitOutput(config, stackkitsDir)
	} else {
		output, err = g.Generate(config)
	}

	if err != nil {
		return err
	}

	return g.WriteOutput(output)
}

// GenerateStackKitOutput generates output for any StackKit type.
// It auto-detects whether to use single-node or multi-node generation.
func (g *IaCGenerator) GenerateStackKitOutput(config *IaCConfig, stackkitsDir string) (*IaCOutput, error) {
	if strings.TrimSpace(stackkitsDir) == "" {
		return nil, fmt.Errorf("StackKits directory is required: TechStack no longer generates StackKit HCL from embedded templates; set TECHSTACK_STACKKITS_DIR or STACKKITS_REPO")
	}

	// Determine StackKit type using canonical StackKits slugs.
	stackKit := CanonicalStackKitName(strings.ToLower(config.StackKit))

	switch stackKit {
	case StackKitCloud:
		return g.GenerateCloudKitOutput(config, stackkitsDir)
	case StackKitModernHomelab:
		return g.GenerateModernKitOutput(config, stackkitsDir)
	case StackKitBasement:
		return g.GenerateBaseKitOutput(config, stackkitsDir)
	default:
		return nil, fmt.Errorf("unknown StackKit %q: TechStack rollouts support basement-kit, cloud-kit, or modern-homelab", config.StackKit)
	}
}
