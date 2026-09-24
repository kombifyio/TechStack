// Package unifier provides the Unifier Pipeline orchestration.
package unifier

import (
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// Pipeline orchestrates the complete Unifier Pipeline.
//
// Pipeline Steps:
//  1. Pre-Validation - Schema validation
//  2. Intent Analysis - RequirementsSpec and baseline decision context
//  3. StackKit Resolution - Auto-select or validate StackKit
//  4. Add-On Detection - Detect required add-ons
//  5. Environment Eligibility - hard feasibility checks from inventory
//  6. Capability Guidance - UI/CLI guidance from operator profile
//  7. StackKit/Profile Resolution - final profile and policy bundle selection
//  8. Unifier Engine - Transform + Resolve + Defaults
//
// The pipeline ensures that specs are fully validated and resolved
// before being passed to the execution layer (Terramate/OpenTofu).
//
// IMPORTANT: The pipeline does NOT mutate the input spec.
// All modifications are made on a copy.
type Pipeline struct {
	preValidator  *PreValidator
	resolver      *StackKitResolver
	addonDetector *AddonDetector
	engine        *Engine
}

// PipelineStep represents a single step in the pipeline.
type PipelineStep struct {
	Name      string        `json:"name"`
	Status    string        `json:"status"` // "pending", "running", "success", "failed", "skipped"
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	StartedAt time.Time     `json:"startedAt,omitempty"`
}

const failureStatus = "failed"

// PipelineResult contains the complete result of pipeline execution.
type PipelineResult struct {
	// Step 1: Pre-Validation
	ValidationResult *core.ValidationResult `json:"validationResult"`

	// Step 2: StackKit Resolution
	ResolveResult *ResolveResult `json:"resolveResult"`

	// Step 2b: Add-On Detection
	DetectionResult *DetectionResult `json:"detectionResult"`

	// Decision-plane artifacts produced from Intent + Environment + Operator capability.
	RequirementsSpec *core.RequirementsSpec `json:"requirementsSpec,omitempty"`
	DecisionContext  *core.DecisionContext  `json:"decisionContext,omitempty"`
	DecisionTrace    *core.DecisionTrace    `json:"decisionTrace,omitempty"`

	// Step 3: Unified Spec (only present if all steps succeed)
	UnifiedSpec *core.UnifiedSpec `json:"unifiedSpec,omitempty"`

	// Pipeline metadata
	Success      bool           `json:"success"`
	FailedStep   string         `json:"failedStep,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Steps        []PipelineStep `json:"steps"`
	TotalTime    time.Duration  `json:"totalTime"`

	// Warnings collected during execution
	Warnings []string `json:"warnings,omitempty"`
}

type pipelineRunOptions struct {
	runEngine            bool
	includeSkippedEngine bool
	strictResolution     bool
}

// NewPipeline creates a new Unifier Pipeline with an existing engine.
func NewPipeline(engine *Engine) *Pipeline {
	if engine == nil {
		return nil
	}

	// Create pre-validator with engine's context and schema
	preValidator := NewPreValidator(engine.ctx, engine.schema)

	// Create resolver with available kits from engine
	availableKits := engine.ListStackKits()
	resolver := NewStackKitResolver(availableKits)

	// Create add-on detector
	addonDetector := NewAddonDetector()

	return &Pipeline{
		preValidator:  preValidator,
		resolver:      resolver,
		addonDetector: addonDetector,
		engine:        engine,
	}
}

func (p *Pipeline) executeSpecWithDecisionContext(spec *core.KombinationSpec, decisionContext *core.DecisionContext) *PipelineResult {
	return p.executeWithDecisionContext(spec, decisionContext, pipelineRunOptions{
		runEngine:        true,
		strictResolution: true,
	})
}

func (p *Pipeline) executeWithDecisionContext(spec *core.KombinationSpec, providedDecisionContext *core.DecisionContext, options pipelineRunOptions) *PipelineResult {
	startTime := time.Now()
	result := &PipelineResult{
		Steps:    make([]PipelineStep, 0, 8),
		Warnings: []string{},
	}

	exec := &pipelineExecution{
		pipeline:                p,
		workingSpec:             p.copySpec(spec),
		result:                  result,
		providedDecisionContext: providedDecisionContext,
		options:                 options,
		startTime:               startTime,
	}

	if exec.runRequiredStep("pre-validation", exec.preValidate) {
		return result
	}
	if exec.runRequiredStep("intent-analysis", exec.analyzeIntent) {
		return result
	}
	if exec.runRequiredStep("stackkit-resolution", exec.resolveStackKit) {
		return result
	}
	if exec.runRequiredStep("addon-detection", exec.detectAddons) {
		return result
	}
	if exec.runRequiredStep("environment-eligibility", exec.validateEnvironment) {
		return result
	}
	if exec.runRequiredStep("capability-guidance", exec.applyCapabilityGuidance) {
		return result
	}
	if exec.runRequiredStep("stackkit-profile-resolution", exec.resolveProfile) {
		return result
	}

	if !options.runEngine {
		exec.skipEngine()
		return result
	}

	if exec.runRequiredStep("unifier-engine", exec.runUnifierEngine) {
		return result
	}

	result.Success = true
	result.TotalTime = time.Since(startTime)
	return result
}

type pipelineExecution struct {
	pipeline                *Pipeline
	workingSpec             *core.KombinationSpec
	result                  *PipelineResult
	providedDecisionContext *core.DecisionContext
	options                 pipelineRunOptions
	startTime               time.Time
}

func (e *pipelineExecution) runRequiredStep(name string, fn func() error) bool {
	step := e.pipeline.runStep(name, fn)
	e.result.Steps = append(e.result.Steps, step)
	if step.Status != failureStatus {
		return false
	}
	e.result.Success = false
	e.result.FailedStep = name
	e.result.ErrorMessage = step.Error
	e.result.TotalTime = time.Since(e.startTime)
	return true
}

func (e *pipelineExecution) preValidate() error {
	validationResult, err := e.pipeline.preValidator.ValidateSchema(e.workingSpec)
	if err != nil {
		return fmt.Errorf("pre-validation error: %w", err)
	}
	e.result.ValidationResult = validationResult
	if !validationResult.Valid {
		return fmt.Errorf("validation failed with %d errors", len(validationResult.Errors))
	}
	return nil
}

func (e *pipelineExecution) analyzeIntent() error {
	e.result.DecisionContext = BuildDecisionContext(e.workingSpec, e.providedDecisionContext)
	requirementsSpec, err := e.pipeline.engine.Analyze(e.workingSpec)
	if err != nil {
		return fmt.Errorf("intent analysis error: %w", err)
	}
	e.result.RequirementsSpec = requirementsSpec
	return nil
}

func (e *pipelineExecution) resolveStackKit() error {
	resolveResult := e.pipeline.resolver.Resolve(e.workingSpec)
	e.result.ResolveResult = resolveResult
	e.result.Warnings = append(e.result.Warnings, resolveResult.Warnings...)

	if e.options.strictResolution && !resolveResult.Valid {
		return fmt.Errorf("StackKit resolution failed: kit '%s' not available", resolveResult.StackKit)
	}
	if !resolveResult.Valid {
		e.result.Warnings = append(e.result.Warnings, fmt.Sprintf("StackKit '%s' may not be available", resolveResult.StackKit))
	}

	if resolveResult.Valid || e.workingSpec.Kit == "" {
		e.workingSpec.Kit = resolveResult.StackKit
	}
	return nil
}

func (e *pipelineExecution) detectAddons() error {
	e.result.DetectionResult = e.pipeline.addonDetector.Detect(e.workingSpec)
	return nil
}

func (e *pipelineExecution) validateEnvironment() error {
	addons := detectionAddonNames(e.result.DetectionResult)
	e.result.DecisionTrace = BuildDecisionTrace(e.workingSpec, e.result.DecisionContext, e.result.ResolveResult, addons)
	applyPipelineDecisionArtifacts(e.result, e.workingSpec)
	if len(e.result.DecisionTrace.BlockingGaps) > 0 {
		return fmt.Errorf("%s", blockingGapMessage(e.result.DecisionTrace.BlockingGaps))
	}
	return nil
}

func (e *pipelineExecution) applyCapabilityGuidance() error {
	applyPipelineDecisionArtifacts(e.result, e.workingSpec)
	return nil
}

func (e *pipelineExecution) resolveProfile() error {
	if e.result.DecisionTrace == nil {
		addons := detectionAddonNames(e.result.DetectionResult)
		e.result.DecisionTrace = BuildDecisionTrace(e.workingSpec, e.result.DecisionContext, e.result.ResolveResult, addons)
	}
	if e.result.DecisionTrace.StackKitProfile == "" {
		return fmt.Errorf("StackKit profile resolution failed")
	}
	applyPipelineDecisionArtifacts(e.result, e.workingSpec)
	return nil
}

func (e *pipelineExecution) skipEngine() {
	if e.options.includeSkippedEngine {
		e.result.Steps = append(e.result.Steps, PipelineStep{
			Name:   "unifier-engine",
			Status: "skipped",
		})
		e.result.Warnings = append(e.result.Warnings, "No nodes provided yet; skipping unifier-engine")
	}
	e.result.UnifiedSpec = nil
	e.result.Success = true
	e.result.TotalTime = time.Since(e.startTime)
}

func (e *pipelineExecution) runUnifierEngine() error {
	unifiedSpec, err := e.pipeline.engine.Unify(e.workingSpec, []core.Worker{})
	if err != nil {
		return fmt.Errorf("unification error: %w", err)
	}
	e.result.UnifiedSpec = unifiedSpec
	ApplyUnifiedDecisionArtifacts(unifiedSpec, e.result.DecisionContext, e.result.DecisionTrace)
	if e.result.DecisionTrace != nil && e.result.DecisionTrace.SelectedStackKit != "" {
		unifiedSpec.StackKit = e.result.DecisionTrace.SelectedStackKit
	}
	e.applyDetectedAddons(unifiedSpec)
	return nil
}

func (e *pipelineExecution) applyDetectedAddons(unifiedSpec *core.UnifiedSpec) {
	if e.result.DetectionResult == nil || len(e.result.DetectionResult.Addons) == 0 {
		return
	}
	if unifiedSpec.Metadata == nil {
		unifiedSpec.Metadata = make(map[string]string)
	}
	for i, addon := range e.result.DetectionResult.Addons {
		unifiedSpec.Metadata[fmt.Sprintf("addon_%d", i)] = addon.Name
	}
}

// ExecuteInput runs the pipeline starting from raw/partial user input.
//
// Important behavior:
//   - StackKit resolution happens here (after import), and only auto-selects when the input
//     does not specify a kit.
//   - If no nodes are present yet (agent registration later), the Unifier Engine step is skipped.
func (p *Pipeline) ExecuteInput(input *core.InputSpec) *PipelineResult {
	return p.ExecuteInputWithDecisionContext(input, nil)
}

// ExecuteInputWithDecisionContext runs the pipeline from raw input plus optional
// decision-plane context supplied outside the user-owned intent file.
func (p *Pipeline) ExecuteInputWithDecisionContext(input *core.InputSpec, decisionContext *core.DecisionContext) *PipelineResult {
	spec := NormalizeInputSpec(input)
	// The spec pipeline handles nil and will surface validation errors.
	if spec == nil {
		return p.executeSpecWithDecisionContext(nil, decisionContext)
	}

	// If nodes aren't available yet, run validate-only steps and mark engine skipped.
	if len(spec.Nodes) == 0 {
		return p.executeWithDecisionContext(spec, decisionContext, pipelineRunOptions{
			runEngine:            false,
			includeSkippedEngine: true,
			strictResolution:     true,
		})
	}

	return p.executeSpecWithDecisionContext(spec, decisionContext)
}

func applyPipelineDecisionArtifacts(result *PipelineResult, spec *core.KombinationSpec) {
	if result == nil {
		return
	}
	if result.RequirementsSpec != nil {
		if result.ResolveResult != nil && result.ResolveResult.StackKit != "" {
			result.RequirementsSpec.StackKit = result.ResolveResult.StackKit
		}
		if result.DetectionResult != nil {
			result.RequirementsSpec.DetectedAddons = detectionAddonNames(result.DetectionResult)
		}
		ApplyDecisionArtifacts(result.RequirementsSpec, spec, result.DecisionContext, result.DecisionTrace)
	}
	if result.UnifiedSpec != nil {
		ApplyUnifiedDecisionArtifacts(result.UnifiedSpec, result.DecisionContext, result.DecisionTrace)
	}
}

func detectionAddonNames(result *DetectionResult) []string {
	if result == nil || len(result.Addons) == 0 {
		return nil
	}
	names := make([]string, 0, len(result.Addons))
	for _, addon := range result.Addons {
		names = append(names, addon.Name)
	}
	return names
}

func blockingGapMessage(gaps []core.EnvironmentGap) string {
	if len(gaps) == 0 {
		return ""
	}
	messages := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		if gap.Message != "" {
			messages = append(messages, gap.Message)
			continue
		}
		messages = append(messages, gap.Code)
	}
	return "environment eligibility failed: " + strings.Join(messages, "; ")
}

// runStep executes a pipeline step and captures timing/errors.
func (p *Pipeline) runStep(name string, fn func() error) PipelineStep {
	step := PipelineStep{
		Name:      name,
		Status:    "running",
		StartedAt: time.Now(),
	}

	err := fn()

	step.Duration = time.Since(step.StartedAt)

	if err != nil {
		step.Status = failureStatus
		step.Error = err.Error()
	} else {
		step.Status = "success"
	}

	return step
}

// copySpec creates a deep copy of a KombinationSpec.
func (p *Pipeline) copySpec(spec *core.KombinationSpec) *core.KombinationSpec {
	if spec == nil {
		return nil
	}

	// Create a new spec with copied values
	copied := &core.KombinationSpec{
		Name:    spec.Name,
		Version: spec.Version,
		Kit:     spec.Kit,
	}

	// Copy nodes
	if spec.Nodes != nil {
		copied.Nodes = make([]core.NodeSpec, len(spec.Nodes))
		for i, node := range spec.Nodes {
			copied.Nodes[i] = p.copyNode(node)
		}
	}

	// Copy services
	if spec.Services != nil {
		copied.Services = make([]core.ServiceSpec, len(spec.Services))
		for i, svc := range spec.Services {
			copied.Services[i] = p.copyService(svc)
		}
	}

	// Copy network
	copied.Network = spec.Network // NetworkSpec is a value type

	// Copy metadata
	if spec.Metadata != nil {
		copied.Metadata = make(map[string]string, len(spec.Metadata))
		for k, v := range spec.Metadata {
			copied.Metadata[k] = v
		}
	}

	return copied
}

// copyNode creates a copy of a NodeSpec.
func (p *Pipeline) copyNode(node core.NodeSpec) core.NodeSpec {
	copied := core.NodeSpec{
		Name:     node.Name,
		Type:     node.Type,
		Provider: node.Provider,
	}

	if node.SSH != nil {
		copied.SSH = &core.SSHConfig{
			Host:    node.SSH.Host,
			Port:    node.SSH.Port,
			User:    node.SSH.User,
			KeyPath: node.SSH.KeyPath,
		}
	}

	if node.Tags != nil {
		copied.Tags = make(map[string]string, len(node.Tags))
		for k, v := range node.Tags {
			copied.Tags[k] = v
		}
	}

	return copied
}

// copyService creates a copy of a ServiceSpec.
func (p *Pipeline) copyService(svc core.ServiceSpec) core.ServiceSpec {
	copied := core.ServiceSpec{
		Name: svc.Name,
		Type: svc.Type,
		Node: svc.Node,
	}

	if svc.Needs != nil {
		copied.Needs = make([]string, len(svc.Needs))
		copy(copied.Needs, svc.Needs)
	}

	return copied
}

// PreValidate runs only Step 1.
func (p *Pipeline) PreValidate(spec *core.KombinationSpec) (*core.ValidationResult, error) {
	return p.preValidator.ValidateSchema(spec)
}

// DetectAddons runs only Step 2b.
func (p *Pipeline) DetectAddons(spec *core.KombinationSpec) *DetectionResult {
	return p.addonDetector.Detect(spec)
}
