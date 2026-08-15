// Package jobs provides async job processing for kombifyTechstack.
// This file contains the drift detection job handler for Sprint 6.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/pkg/tofu"
)

// Drift detection step IDs for frontend task tracking.
const (
	StepDriftValidate       = "drift_validate"        // Validating stack for drift check
	StepDriftInitialize     = "drift_initialize"      // Initializing Terramate
	StepDriftCheckStacks    = "drift_check_stacks"    // Running drift detection
	StepDriftCollectResults = "drift_collect_results" // Collecting drift results
	StepDriftNotify         = "drift_notify"          // Sending notifications
)

// DriftStatus represents the drift status of a stack.
type DriftStatus string

const (
	DriftStatusInSync   DriftStatus = "in_sync"
	DriftStatusDrifted  DriftStatus = "drifted"
	DriftStatusFailed   DriftStatus = "failed"
	DriftStatusChecking DriftStatus = "checking"
	DriftStatusUnknown  DriftStatus = "unknown"
)

// ResourceChange represents a single resource change detected during drift detection.
type ResourceChange struct {
	Address      string                  `json:"address"`       // e.g., "proxmox_vm.worker[0]"
	ResourceType string                  `json:"resource_type"` // e.g., "proxmox_vm"
	Name         string                  `json:"name"`          // Resource name
	Action       string                  `json:"action"`        // create, update, delete, no-op
	Before       map[string]interface{}  `json:"before,omitempty"`
	After        map[string]interface{}  `json:"after,omitempty"`
	Changes      map[string]ChangeDetail `json:"changes,omitempty"` // Field-level changes
}

// ChangeDetail represents a single field change.
type ChangeDetail struct {
	From interface{} `json:"from"`
	To   interface{} `json:"to"`
}

// DriftCheckResult contains the complete result of a drift detection run.
type DriftCheckResult struct {
	StackID           string           `json:"stack_id"`
	Status            DriftStatus      `json:"status"`
	AffectedCount     int              `json:"affected_count"`
	AffectedResources []ResourceChange `json:"affected_resources"`
	PlanSummary       PlanSummary      `json:"plan_summary"`
	CheckedAt         time.Time        `json:"checked_at"`
	DurationMs        int64            `json:"duration_ms"`
	TriggerType       string           `json:"trigger_type"` // manual, scheduled, webhook
	ErrorMessage      string           `json:"error_message,omitempty"`
	ErrorDetails      string           `json:"error_details,omitempty"`
}

// PlanSummary contains a summary of planned changes.
type PlanSummary struct {
	ToCreate  int `json:"to_create"`
	ToUpdate  int `json:"to_update"`
	ToDelete  int `json:"to_delete"`
	Unchanged int `json:"unchanged"`
}

// DriftCheckConfig holds configuration for drift detection operations.
type DriftCheckConfig struct {
	// WorkDir is the base directory for OpenTofu workspaces
	WorkDir string
	// TerramateTimeout is the timeout for Terramate operations
	TerramateTimeout time.Duration
	// PlanTimeout is the timeout for individual plan operations
	PlanTimeout time.Duration
}

// DefaultDriftCheckConfig returns a default configuration.
func DefaultDriftCheckConfig() *DriftCheckConfig {
	return &DriftCheckConfig{
		WorkDir:          filepath.Join("data", "provision"),
		TerramateTimeout: 30 * time.Minute,
		PlanTimeout:      10 * time.Minute,
	}
}

// DriftCheckHandler creates a job handler for drift detection.
// It uses the Terramate runner to check for infrastructure drift and collects detailed results.
func DriftCheckHandler(cfg *DriftCheckConfig) JobHandler {
	if cfg == nil {
		cfg = DefaultDriftCheckConfig()
	}

	return func(ctx context.Context, job *Job, q *Queue) error {
		startTime := time.Now()

		// ============================================================
		// STEP 1: VALIDATE - Extract stack info and validate
		// ============================================================
		job.setStep(StepDriftValidate)
		q.UpdateProgress(job.ID, 5, "Validating stack for drift check...")

		stackID := job.TargetID
		if stackID == "" {
			return wrapDriftError(StepDriftValidate, "missing stack_id",
				"The drift check request did not include a stack ID.")
		}

		// Get trigger type from payload (defaults to "manual")
		triggerType := "manual"
		if tt, ok := job.Payload["trigger_type"].(string); ok {
			triggerType = tt
		}

		// Determine work directory
		workDir := filepath.Join(cfg.WorkDir, stackID)

		q.UpdateProgress(job.ID, 10, "Stack validated")

		// ============================================================
		// STEP 2: INITIALIZE - Create Terramate runner
		// ============================================================
		job.setStep(StepDriftInitialize)
		q.UpdateProgress(job.ID, 15, "Initializing Terramate...")

		runner, err := tofu.NewTerramateRunner(workDir)
		if err != nil {
			return wrapDriftError(StepDriftInitialize, fmt.Sprintf("failed to create Terramate runner: %v", err),
				"Could not initialize the drift detection engine. Is Terramate installed?")
		}
		runner.WithTimeout(cfg.TerramateTimeout)

		// Check if Terramate is installed
		version, err := runner.CheckInstalled()
		if err != nil {
			return wrapDriftError(StepDriftInitialize, fmt.Sprintf("Terramate not available: %v", err),
				"Terramate CLI is not installed or not accessible. Please install Terramate to enable drift detection.")
		}

		q.UpdateProgress(job.ID, 20, fmt.Sprintf("Terramate %s ready", version))

		// ============================================================
		// STEP 3: CHECK STACKS - Run drift detection
		// ============================================================
		job.setStep(StepDriftCheckStacks)
		q.UpdateProgress(job.ID, 30, "Running drift detection...")

		// Create context with plan timeout
		planCtx, cancel := context.WithTimeout(ctx, cfg.PlanTimeout)
		defer cancel()

		// Use the existing SyncDriftStatus method from TerramateRunner
		driftResult, err := runner.SyncDriftStatus(planCtx)

		result := &DriftCheckResult{
			StackID:     stackID,
			CheckedAt:   time.Now(),
			TriggerType: triggerType,
			DurationMs:  time.Since(startTime).Milliseconds(),
		}

		if err != nil {
			// Check if this is a context timeout
			if planCtx.Err() == context.DeadlineExceeded {
				result.Status = DriftStatusFailed
				result.ErrorMessage = "Drift check timed out"
				result.ErrorDetails = fmt.Sprintf("Operation exceeded %v timeout", cfg.PlanTimeout)
			} else {
				result.Status = DriftStatusFailed
				result.ErrorMessage = "Drift check failed"
				result.ErrorDetails = err.Error()
			}

			q.UpdateProgress(job.ID, 50, fmt.Sprintf("Drift check failed: %s", result.ErrorMessage))
		} else {
			q.UpdateProgress(job.ID, 60, "Analyzing drift results...")

			// Determine status from driftResult
			if len(driftResult.Drifted) > 0 {
				result.Status = DriftStatusDrifted
				result.AffectedCount = len(driftResult.Drifted)
			} else if len(driftResult.Failed) > 0 {
				result.Status = DriftStatusFailed
				result.ErrorMessage = fmt.Sprintf("%d stack(s) failed drift check", len(driftResult.Failed))
			} else {
				result.Status = DriftStatusInSync
			}
		}

		// ============================================================
		// STEP 4: COLLECT RESULTS - Get detailed drift information
		// ============================================================
		job.setStep(StepDriftCollectResults)
		q.UpdateProgress(job.ID, 70, "Collecting detailed results...")

		// If drift was detected, get detailed information for each drifted stack
		if result.Status == DriftStatusDrifted && driftResult != nil {
			affectedResources := make([]ResourceChange, 0)

			for _, stackPath := range driftResult.Drifted {
				// Get detailed drift information for this stack
				details, detailErr := runner.GetDriftDetails(planCtx, stackPath)
				if detailErr != nil {
					// Log but continue - we still have the high-level drift info
					q.log.Warn("failed_to_get_drift_details",
						"stack_path", stackPath,
						"error", detailErr.Error(),
					)
					continue
				}

				// Parse the planned changes if available
				if details.PlannedChanges != nil {
					changes := parsePlanOutput(details.PlannedChanges)
					affectedResources = append(affectedResources, changes...)
				}
			}

			result.AffectedResources = affectedResources
			result.AffectedCount = len(affectedResources)

			// Calculate plan summary
			result.PlanSummary = calculatePlanSummary(affectedResources)
		}

		q.UpdateProgress(job.ID, 85, "Results collected")

		// ============================================================
		// STEP 5: NOTIFY - Prepare notification (future: send alerts)
		// ============================================================
		job.setStep(StepDriftNotify)
		q.UpdateProgress(job.ID, 90, "Finalizing drift check...")

		// Store result in job
		result.DurationMs = time.Since(startTime).Milliseconds()
		resultJSON, _ := json.Marshal(result)
		job.replaceResult(map[string]interface{}{
			"drift_result": json.RawMessage(resultJSON),
			"status":       string(result.Status),
			"stack_id":     stackID,
			"checked_at":   result.CheckedAt,
			"duration_ms":  result.DurationMs,
		})

		// Build status message based on result
		var statusMsg string
		switch result.Status {
		case DriftStatusInSync:
			statusMsg = "Infrastructure is in sync - no drift detected"
		case DriftStatusDrifted:
			statusMsg = fmt.Sprintf("Drift detected: %d resource(s) changed", result.AffectedCount)
		case DriftStatusFailed:
			statusMsg = fmt.Sprintf("Drift check failed: %s", result.ErrorMessage)
		default:
			statusMsg = "Drift check completed"
		}

		q.UpdateProgress(job.ID, 100, statusMsg)

		// Return error only if the check itself failed (not if drift was detected)
		if result.Status == DriftStatusFailed {
			return wrapDriftError(StepDriftCollectResults, result.ErrorMessage, result.ErrorDetails)
		}

		return nil
	}
}

// DriftResolveHandler creates a job handler for resolving drift (re-applying infrastructure).
// This triggers a plan and apply to bring infrastructure back to desired state.
func DriftResolveHandler(cfg *DriftCheckConfig) JobHandler {
	if cfg == nil {
		cfg = DefaultDriftCheckConfig()
	}

	return func(ctx context.Context, job *Job, q *Queue) error {
		startTime := time.Now()

		// Step 1: Validate
		job.setStep(StepDriftValidate)
		q.UpdateProgress(job.ID, 10, "Validating stack for drift resolution...")

		stackID := job.TargetID
		if stackID == "" {
			return wrapDriftError(StepDriftValidate, "missing stack_id",
				"The drift resolution request did not include a stack ID.")
		}

		workDir := filepath.Join(cfg.WorkDir, stackID)

		// Step 2: Initialize OpenTofu runner
		job.setStep(StepDriftInitialize)
		q.UpdateProgress(job.ID, 20, "Preparing infrastructure tools...")

		tofuRunner := &tofu.DefaultRunner{
			Timeout: cfg.TerramateTimeout,
		}

		// Step 3: Initialize (in case providers changed)
		q.UpdateProgress(job.ID, 30, "Initializing OpenTofu...")
		if err := tofuRunner.Init(workDir); err != nil {
			return wrapDriftError(StepDriftInitialize, fmt.Sprintf("tofu init failed: %v", err),
				"Could not initialize infrastructure tools. Check your provider configuration.")
		}

		// Step 4: Plan and Apply
		job.setStep(StepDriftCheckStacks)
		q.UpdateProgress(job.ID, 50, "Applying infrastructure changes...")

		applyResult, err := tofuRunner.ApplyWithContext(ctx, workDir)
		if err != nil {
			return wrapDriftError(StepDriftCheckStacks, fmt.Sprintf("tofu apply failed: %v", err),
				"Could not apply infrastructure changes. Manual intervention may be required.")
		}

		q.UpdateProgress(job.ID, 90, "Drift resolved")

		// Store result
		job.replaceResult(map[string]interface{}{
			"stack_id":    stackID,
			"resolved":    true,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"changes": map[string]interface{}{
				"added":     applyResult.Resources.Added,
				"changed":   applyResult.Resources.Changed,
				"destroyed": applyResult.Resources.Destroyed,
			},
		})

		q.UpdateProgress(job.ID, 100, "Infrastructure synchronized successfully")

		return nil
	}
}

// RegisterDriftHandlers registers the drift detection job handlers on a queue.
func RegisterDriftHandlers(q *Queue, cfg *DriftCheckConfig) {
	if cfg == nil {
		cfg = DefaultDriftCheckConfig()
	}

	q.RegisterHandler(JobTypeDriftCheck, DriftCheckHandler(cfg))
	q.RegisterHandler(JobTypeDriftResolve, DriftResolveHandler(cfg))
}

// wrapDriftError creates a structured error for drift detection failures.
func wrapDriftError(step, message, details string) error {
	return &JobError{
		Original:  fmt.Errorf("%s: %s", step, message),
		Category:  ErrorCategoryTransient,
		Retryable: true,
		Context: map[string]interface{}{
			"step":    step,
			"message": message,
			"details": details,
		},
	}
}

// parsePlanOutput converts raw Terraform/OpenTofu plan output to ResourceChange structs.
func parsePlanOutput(planOutput map[string]interface{}) []ResourceChange {
	changes := make([]ResourceChange, 0)

	// Try to extract resource_changes from plan output
	resourceChanges, ok := planOutput["resource_changes"].([]interface{})
	if !ok {
		return changes
	}

	for _, rc := range resourceChanges {
		rcMap, ok := rc.(map[string]interface{})
		if !ok {
			continue
		}

		change := ResourceChange{
			Address: getStringValue(rcMap, "address"),
			Name:    getStringValue(rcMap, "name"),
		}

		// Extract resource type from address
		if addr := change.Address; addr != "" {
			// Format: provider.resource_type.name
			parts := splitResourceAddress(addr)
			if len(parts) >= 2 {
				change.ResourceType = parts[len(parts)-2]
			}
		}

		// Extract action
		if changeBlock, ok := rcMap["change"].(map[string]interface{}); ok {
			if actions, ok := changeBlock["actions"].([]interface{}); ok && len(actions) > 0 {
				change.Action = getStringFromInterface(actions[0])
			}

			// Extract before/after values
			if before, ok := changeBlock["before"].(map[string]interface{}); ok {
				change.Before = before
			}
			if after, ok := changeBlock["after"].(map[string]interface{}); ok {
				change.After = after
			}

			// Calculate field-level changes
			change.Changes = calculateFieldChanges(change.Before, change.After)
		}

		// Only include changes that have actual actions
		if change.Action != "" && change.Action != "no-op" {
			changes = append(changes, change)
		}
	}

	return changes
}

// calculatePlanSummary generates a summary from resource changes.
func calculatePlanSummary(changes []ResourceChange) PlanSummary {
	summary := PlanSummary{}

	for _, c := range changes {
		switch c.Action {
		case "create":
			summary.ToCreate++
		case "update":
			summary.ToUpdate++
		case "delete":
			summary.ToDelete++
		case "no-op":
			summary.Unchanged++
		}
	}

	return summary
}

// calculateFieldChanges computes the differences between before and after states.
func calculateFieldChanges(before, after map[string]interface{}) map[string]ChangeDetail {
	if before == nil || after == nil {
		return nil
	}

	changes := make(map[string]ChangeDetail)

	// Find changed/added fields
	for key, afterVal := range after {
		beforeVal, existed := before[key]
		if !existed {
			// New field added
			changes[key] = ChangeDetail{From: nil, To: afterVal}
		} else if !jsonEqual(beforeVal, afterVal) {
			// Field changed
			changes[key] = ChangeDetail{From: beforeVal, To: afterVal}
		}
	}

	// Find deleted fields
	for key, beforeVal := range before {
		if _, exists := after[key]; !exists {
			changes[key] = ChangeDetail{From: beforeVal, To: nil}
		}
	}

	if len(changes) == 0 {
		return nil
	}

	return changes
}

// Helper functions

func getStringValue(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringFromInterface(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func splitResourceAddress(addr string) []string {
	// Simple split by dots, handling brackets
	parts := make([]string, 0)
	current := ""
	inBracket := false

	for _, ch := range addr {
		switch ch {
		case '[':
			inBracket = true
			current += string(ch)
		case ']':
			inBracket = false
			current += string(ch)
		case '.':
			if !inBracket && current != "" {
				parts = append(parts, current)
				current = ""
			} else {
				current += string(ch)
			}
		default:
			current += string(ch)
		}
	}

	if current != "" {
		parts = append(parts, current)
	}

	return parts
}

func jsonEqual(a, b interface{}) bool {
	// Simple JSON comparison
	aJSON, err1 := json.Marshal(a)
	bJSON, err2 := json.Marshal(b)

	if err1 != nil || err2 != nil {
		return false
	}

	return string(aJSON) == string(bJSON)
}
