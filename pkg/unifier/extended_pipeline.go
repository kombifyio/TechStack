// Package unifier provides the Unifier Pipeline orchestration.
package unifier

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// ExtendedPipelineStep represents a step type in the extended pipeline.
type ExtendedPipelineStep string

const (
	// Phase 1: Intent Loading
	StepLoadIntent     ExtendedPipelineStep = "load-intent"
	StepValidateIntent ExtendedPipelineStep = "validate-intent"

	// Phase 2: Requirements Analysis
	StepSelectStackKit      ExtendedPipelineStep = "select-stackkit"
	StepDetectAddons        ExtendedPipelineStep = "detect-addons"
	StepAnalyzeRequirements ExtendedPipelineStep = "analyze-requirements"
	StepPersistRequirements ExtendedPipelineStep = "persist-requirements"

	// Phase 3: Worker Validation
	StepValidateWorkers ExtendedPipelineStep = "validate-workers"
	StepMatchServices   ExtendedPipelineStep = "match-services"

	// Phase 4: Unification
	StepGenerateUnified ExtendedPipelineStep = "generate-unified"
	StepPersistUnified  ExtendedPipelineStep = "persist-unified"

	// Phase 5: IaC Generation
	StepGenerateIaC ExtendedPipelineStep = "generate-iac"
)

// ExtendedPipelineState holds the state during pipeline execution.
type ExtendedPipelineState struct {
	// Stack identification
	StackID string

	// Input
	IntentPath string
	IntentHash string
	IntentSpec *core.IntentSpec

	// After Phase 2
	RequirementsSpec *core.RequirementsSpec
	RequirementsPath string

	// After Phase 4
	UnifiedSpec *core.UnifiedSpec
	UnifiedPath string

	// After Phase 5
	IaCFiles []string
	TofuDir  string

	// Execution tracking
	CurrentStep    ExtendedPipelineStep
	CompletedSteps []string
	Warnings       []string
	StartTime      time.Time
}

// ExtendedPipelineResult contains the complete result of extended pipeline execution.
type ExtendedPipelineResult struct {
	*PipelineResult

	// State after execution
	State *ExtendedPipelineState `json:"state,omitempty"`

	// Persisted file paths
	IntentPath       string `json:"intentPath,omitempty"`
	RequirementsPath string `json:"requirementsPath,omitempty"`
	UnifiedPath      string `json:"unifiedPath,omitempty"`

	// Phase 5: IaC Generation
	IaCOutput *IaCOutput `json:"iacOutput,omitempty"`

	// Generated file paths
	OutputDir      string   `json:"outputDir,omitempty"`
	TofuDir        string   `json:"tofuDir,omitempty"`
	GeneratedFiles []string `json:"generatedFiles,omitempty"`
}

// ExtendedPipeline adds persistence and IaC generation to the standard Pipeline.
// It orchestrates the complete flow from user spec to deployable IaC files.
type ExtendedPipeline struct {
	*Pipeline
	persister    *SpecPersister
	iacGenerator *IaCGenerator
	stackID      string
	outputDir    string
	mode         string // "simple" or "advanced"
}

// NewExtendedPipeline creates a pipeline with persistence and IaC generation capabilities.
// For backward compatibility, if stackID is empty, only IaC generation is enabled.
func NewExtendedPipeline(engine *Engine, outputDir string) *ExtendedPipeline {
	basePipeline := NewPipeline(engine)
	if basePipeline == nil {
		return nil
	}

	return &ExtendedPipeline{
		Pipeline:     basePipeline,
		iacGenerator: NewIaCGenerator(outputDir),
		outputDir:    outputDir,
		mode:         "simple",
	}
}

// NewExtendedPipelineWithPersistence creates a new extended pipeline with persistence for a specific stack.
func NewExtendedPipelineWithPersistence(basePipeline *Pipeline, stackID string) (*ExtendedPipeline, error) {
	if basePipeline == nil {
		return nil, fmt.Errorf("base pipeline is required")
	}
	if stackID == "" {
		return nil, fmt.Errorf("stack ID is required")
	}

	persister, err := NewSpecPersister(stackID)
	if err != nil {
		return nil, fmt.Errorf("create persister: %w", err)
	}

	outputDir := persister.GetTofuDir()

	return &ExtendedPipeline{
		Pipeline:     basePipeline,
		persister:    persister,
		iacGenerator: NewIaCGenerator(outputDir),
		stackID:      stackID,
		outputDir:    outputDir,
		mode:         "simple",
	}, nil
}

// WithMode sets the deployment mode (simple or advanced).
func (p *ExtendedPipeline) WithMode(mode string) *ExtendedPipeline {
	if mode == "simple" || mode == "advanced" {
		p.mode = mode
		p.iacGenerator = p.iacGenerator.WithMode(mode)
	}
	return p
}

// WithStackKitDir sets the external StackKit directory for templates.
func (p *ExtendedPipeline) WithStackKitDir(dir string) *ExtendedPipeline {
	p.iacGenerator = p.iacGenerator.WithStackKitDir(dir)
	return p
}

// ExecuteWithIaC runs the complete pipeline including IaC generation.
func (p *ExtendedPipeline) ExecuteWithIaC(spec *core.KombinationSpec) *ExtendedPipelineResult {
	return p.ExecuteWithIaCContext(context.Background(), spec)
}

// Execute runs the complete extended pipeline with persistence.
func (p *ExtendedPipeline) Execute(spec *core.KombinationSpec) *ExtendedPipelineResult {
	return p.ExecuteWithContext(context.Background(), spec)
}

// ExecuteWithContext runs the extended pipeline with context support and persistence.
func (p *ExtendedPipeline) ExecuteWithContext(ctx context.Context, spec *core.KombinationSpec) *ExtendedPipelineResult {
	startTime := time.Now()

	state := &ExtendedPipelineState{
		StackID:        p.stackID,
		StartTime:      startTime,
		CompletedSteps: make([]string, 0),
		Warnings:       make([]string, 0),
	}

	// Run the base pipeline first
	baseResult := p.Pipeline.ExecuteWithContext(ctx, spec)

	result := &ExtendedPipelineResult{
		PipelineResult: baseResult,
		State:          state,
		OutputDir:      p.outputDir,
	}

	if !baseResult.Success {
		return result
	}

	// Persist specs if persister is available
	if p.persister != nil && baseResult.UnifiedSpec != nil {
		reqSpec := baseResult.RequirementsSpec
		if reqSpec == nil {
			reqSpec = &core.RequirementsSpec{
				APIVersion: unifierAPIVersion,
				Kind:       "RequirementsSpec",
				StackKit:   baseResult.UnifiedSpec.StackKit,
				IntentName: spec.Name,
			}
			if baseResult.DetectionResult != nil {
				// Extract addon names from the detection result
				for _, addon := range baseResult.DetectionResult.Addons {
					reqSpec.DetectedAddons = append(reqSpec.DetectedAddons, addon.Name)
				}
			}
		}

		// Persist requirements
		reqPath, err := p.persister.SaveRequirementsSpec(reqSpec, "")
		if err != nil {
			state.Warnings = append(state.Warnings, fmt.Sprintf("failed to persist requirements: %v", err))
		} else {
			state.RequirementsPath = reqPath
			result.RequirementsPath = reqPath
			state.CompletedSteps = append(state.CompletedSteps, string(StepPersistRequirements))
		}

		// Persist UnifiedSpec
		unifiedPath, err := p.persister.SaveUnifiedSpec(baseResult.UnifiedSpec, reqPath)
		if err != nil {
			state.Warnings = append(state.Warnings, fmt.Sprintf("failed to persist unified spec: %v", err))
		} else {
			state.UnifiedPath = unifiedPath
			result.UnifiedPath = unifiedPath
			state.CompletedSteps = append(state.CompletedSteps, string(StepPersistUnified))
		}

		state.UnifiedSpec = baseResult.UnifiedSpec
		state.RequirementsSpec = reqSpec
		result.TofuDir = p.persister.GetTofuDir()
	}

	return result
}

// ExecuteWithIaCContext runs the complete pipeline with context support and IaC generation.
func (p *ExtendedPipeline) ExecuteWithIaCContext(ctx context.Context, spec *core.KombinationSpec) *ExtendedPipelineResult {
	// First run the extended pipeline with persistence
	result := p.ExecuteWithContext(ctx, spec)

	// If base pipeline failed or no unified spec, return early
	if !result.Success || result.UnifiedSpec == nil {
		return result
	}

	// Phase 5: IaC Generation
	step5 := p.runIaCStep(result.UnifiedSpec)
	result.Steps = append(result.Steps, step5)

	if step5.Status == "failed" {
		result.Success = false
		result.FailedStep = "iac-generation"
		result.ErrorMessage = step5.Error
		return result
	}

	return result
}

// runIaCStep executes the IaC generation phase.
func (p *ExtendedPipeline) runIaCStep(unified *core.UnifiedSpec) PipelineStep {
	step := PipelineStep{
		Name:      "iac-generation",
		Status:    "running",
		StartedAt: time.Now(),
	}

	// Generate IaC files
	output, err := p.iacGenerator.GenerateFromUnifiedSpec(unified)
	if err != nil {
		step.Status = "failed"
		step.Error = fmt.Sprintf("IaC generation failed: %v", err)
		step.Duration = time.Since(step.StartedAt)
		return step
	}

	// Check for generation errors
	// Separate hard errors from warnings using the IaCOutput helper.
	if output.HasHardErrors() {
		step.Status = "failed"
		step.Error = fmt.Sprintf("IaC generation had errors: %v", output.HardErrors())
		step.Duration = time.Since(step.StartedAt)
		return step
	}

	// Write files to disk
	if err := p.iacGenerator.WriteToDir(output); err != nil {
		step.Status = "failed"
		step.Error = fmt.Sprintf("Failed to write IaC files: %v", err)
		step.Duration = time.Since(step.StartedAt)
		return step
	}

	step.Status = "success"
	step.Duration = time.Since(step.StartedAt)
	return step
}

// GenerateIaCOnly generates IaC files from an existing UnifiedSpec.
// Use this when you already have a UnifiedSpec and just need to regenerate files.
func (p *ExtendedPipeline) GenerateIaCOnly(unified *core.UnifiedSpec) (*IaCOutput, error) {
	if unified == nil {
		return nil, fmt.Errorf("unified spec cannot be nil")
	}

	output, err := p.iacGenerator.GenerateFromUnifiedSpec(unified)
	if err != nil {
		return nil, err
	}

	// Check for hard errors (non-warning entries)
	if output.HasHardErrors() {
		return output, fmt.Errorf("generation had errors: %v", output.HardErrors())
	}

	if err := p.iacGenerator.WriteToDir(output); err != nil {
		return output, err
	}

	return output, nil
}

// GetStackOutputDir returns the output directory for a specific stack.
func (p *ExtendedPipeline) GetStackOutputDir(stackID string) string {
	return filepath.Join(p.outputDir, stackID)
}

// PreviewIaC generates IaC files without writing them to disk.
// Useful for previewing what would be generated.
func (p *ExtendedPipeline) PreviewIaC(unified *core.UnifiedSpec) (*IaCOutput, error) {
	if unified == nil {
		return nil, fmt.Errorf("unified spec cannot be nil")
	}

	return p.iacGenerator.GenerateFromUnifiedSpec(unified)
}

// ValidateAndPreviewIaC runs validation and returns an IaC preview.
// This is useful for the UI wizard to show users what will be generated.
func (p *ExtendedPipeline) ValidateAndPreviewIaC(spec *core.KombinationSpec) (*ExtendedPipelineResult, error) {
	// Run base pipeline
	baseResult := p.Pipeline.Execute(spec)

	extResult := &ExtendedPipelineResult{
		PipelineResult: baseResult,
		OutputDir:      p.outputDir,
	}

	if !baseResult.Success || baseResult.UnifiedSpec == nil {
		return extResult, nil
	}

	// Preview IaC (don't write to disk)
	output, err := p.PreviewIaC(baseResult.UnifiedSpec)
	if err != nil {
		extResult.Warnings = append(extResult.Warnings,
			fmt.Sprintf("IaC preview failed: %v", err))
		return extResult, nil
	}

	extResult.IaCOutput = output

	// List generated files
	for filename := range output.Files {
		extResult.GeneratedFiles = append(extResult.GeneratedFiles, filename)
	}

	return extResult, nil
}

// ExecuteFromInput runs the extended pipeline from raw input.
func (p *ExtendedPipeline) ExecuteFromInput(input *core.InputSpec) *ExtendedPipelineResult {
	spec := NormalizeInputSpec(input)
	if spec == nil {
		baseResult := p.Pipeline.Execute(nil)
		return &ExtendedPipelineResult{
			PipelineResult: baseResult,
			OutputDir:      p.outputDir,
		}
	}

	return p.ExecuteWithIaC(spec)
}

// GetPersister returns the underlying SpecPersister.
func (p *ExtendedPipeline) GetPersister() *SpecPersister {
	return p.persister
}

// GetStackID returns the stack ID.
func (p *ExtendedPipeline) GetStackID() string {
	return p.stackID
}

// HasPersistedRequirements checks if requirements have been persisted.
func (p *ExtendedPipeline) HasPersistedRequirements() bool {
	if p.persister == nil {
		return false
	}
	return p.persister.RequirementsSpecExists()
}

// HasPersistedUnified checks if unified spec has been persisted.
func (p *ExtendedPipeline) HasPersistedUnified() bool {
	if p.persister == nil {
		return false
	}
	return p.persister.UnifiedSpecExists()
}

// LoadPersistedRequirements loads the persisted requirements spec.
func (p *ExtendedPipeline) LoadPersistedRequirements() (*core.RequirementsSpec, error) {
	if p.persister == nil {
		return nil, fmt.Errorf("no persister configured")
	}
	return p.persister.LoadRequirementsSpec()
}

// LoadPersistedUnified loads the persisted unified spec.
func (p *ExtendedPipeline) LoadPersistedUnified() (*core.UnifiedSpec, error) {
	if p.persister == nil {
		return nil, fmt.Errorf("no persister configured")
	}
	return p.persister.LoadUnifiedSpec()
}
