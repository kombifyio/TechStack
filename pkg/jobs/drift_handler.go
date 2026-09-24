// Package jobs provides async job processing for kombifyTechstack.
// This file contains the drift detection job handler for Sprint 6.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
)

// Drift detection step IDs for frontend task tracking.
const (
	StepDriftValidate       = "drift_validate"
	StepDriftInitialize     = "drift_initialize"
	StepDriftCheckStacks    = "drift_check_stacks"
	StepDriftCollectResults = "drift_collect_results"
	StepDriftNotify         = "drift_notify"
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

// ResourceChange represents a single drifted StackKits subject.
type ResourceChange struct {
	Address      string                  `json:"address"`
	ResourceType string                  `json:"resource_type"`
	Name         string                  `json:"name"`
	Action       string                  `json:"action"`
	Before       map[string]interface{}  `json:"before,omitempty"`
	After        map[string]interface{}  `json:"after,omitempty"`
	Changes      map[string]ChangeDetail `json:"changes,omitempty"`
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
	TriggerType       string           `json:"trigger_type"`
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
	WorkDir          string
	DetectTimeout    time.Duration
	ReconcileTimeout time.Duration
}

// DefaultDriftCheckConfig returns a default configuration.
func DefaultDriftCheckConfig() *DriftCheckConfig {
	return &DriftCheckConfig{
		WorkDir:          filepath.Join("data", "provision"),
		DetectTimeout:    stackKitReadCommandTimeout,
		ReconcileTimeout: stackKitWriteCommandTimeout,
	}
}

func normalizeDriftCheckConfig(cfg *DriftCheckConfig) *DriftCheckConfig {
	if cfg == nil {
		return DefaultDriftCheckConfig()
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join("data", "provision")
	}
	if cfg.DetectTimeout <= 0 {
		cfg.DetectTimeout = stackKitReadCommandTimeout
	}
	if cfg.ReconcileTimeout <= 0 {
		cfg.ReconcileTimeout = stackKitWriteCommandTimeout
	}
	return cfg
}

// DriftCheckHandler creates a job handler for StackKits drift detection.
func DriftCheckHandler(cfg *DriftCheckConfig) JobHandler {
	cfg = normalizeDriftCheckConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) error {
		startTime := time.Now()

		job.setStep(StepDriftValidate)
		q.UpdateProgress(job.ID, 5, "Validating stack for drift check...")

		stackID := job.TargetID
		if stackID == "" {
			return wrapDriftError(StepDriftValidate, "missing stack_id",
				"The drift check request did not include a stack ID.")
		}

		triggerType := "manual"
		if tt, ok := job.Payload["trigger_type"].(string); ok && tt != "" {
			triggerType = tt
		}

		workDir := filepath.Join(cfg.WorkDir, stackID)
		q.UpdateProgress(job.ID, 10, "Stack validated")

		job.setStep(StepDriftInitialize)
		q.UpdateProgress(job.ID, 20, "Preparing StackKits drift detect...")

		job.setStep(StepDriftCheckStacks)
		q.UpdateProgress(job.ID, 30, "Running StackKits drift detect...")

		report, err := detectStackKitDrift(ctx, workDir, cfg.DetectTimeout)

		result := &DriftCheckResult{
			StackID:     stackID,
			CheckedAt:   time.Now(),
			TriggerType: triggerType,
			DurationMs:  time.Since(startTime).Milliseconds(),
		}

		if err != nil {
			result.Status = DriftStatusFailed
			result.ErrorMessage = "Drift check failed"
			result.ErrorDetails = err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				result.ErrorMessage = "Drift check timed out"
				result.ErrorDetails = fmt.Sprintf("Operation exceeded %v timeout", cfg.DetectTimeout)
			}
			q.UpdateProgress(job.ID, 50, fmt.Sprintf("Drift check failed: %s", result.ErrorMessage))
		} else if report.HasDrift {
			result.Status = DriftStatusDrifted
			result.AffectedResources = resourceChangesFromDriftReport(report)
			result.AffectedCount = len(result.AffectedResources)
			result.PlanSummary = summarizeDriftSubjects(report)
			q.UpdateProgress(job.ID, 60, "Analyzing drift results...")
		} else {
			result.Status = DriftStatusInSync
			result.PlanSummary = summarizeDriftSubjects(report)
			q.UpdateProgress(job.ID, 60, "Analyzing drift results...")
		}

		job.setStep(StepDriftCollectResults)
		q.UpdateProgress(job.ID, 70, "Collecting detailed results...")
		q.UpdateProgress(job.ID, 85, "Results collected")

		job.setStep(StepDriftNotify)
		q.UpdateProgress(job.ID, 90, "Finalizing drift check...")

		result.DurationMs = time.Since(startTime).Milliseconds()
		resultJSON, _ := json.Marshal(result)
		job.replaceResult(map[string]interface{}{
			"drift_result": json.RawMessage(resultJSON),
			"status":       string(result.Status),
			"stack_id":     stackID,
			"checked_at":   result.CheckedAt,
			"duration_ms":  result.DurationMs,
		})
		switch result.Status {
		case DriftStatusDrifted:
			q.recordJobOutcome(job, outcome.Decision{
				Status: outcome.StatusDegraded, ReasonCode: "runtime_drift_detected",
				Capability: "techstack.runtime.drift", Retryable: false,
				UserGuidance: &outcome.Guidance{
					Title: "Infrastructure drift was detected",
					Body:  "The observed StackKit state differs from the resolved plan. Review the affected subjects before starting reconciliation.",
					NextSteps: []outcome.Step{{
						ID: "review-drift", Label: "Review the affected subjects and reconciliation plan", Kind: "handoff",
					}},
				},
				SupportContext: map[string]any{"stack_id": stackID, "affected_count": result.AffectedCount},
			})
		case DriftStatusInSync:
			q.recordJobOutcome(job, jobAvailableOutcome(job))
		}

		var statusMsg string
		switch result.Status {
		case DriftStatusInSync:
			statusMsg = "Infrastructure is in sync - no drift detected"
		case DriftStatusDrifted:
			statusMsg = fmt.Sprintf("Drift detected: %d subject(s) changed", result.AffectedCount)
		case DriftStatusFailed:
			statusMsg = fmt.Sprintf("Drift check failed: %s", result.ErrorMessage)
		default:
			statusMsg = "Drift check completed"
		}

		q.UpdateProgress(job.ID, 100, statusMsg)

		if result.Status == DriftStatusFailed {
			return wrapDriftError(StepDriftCollectResults, result.ErrorMessage, result.ErrorDetails)
		}
		return nil
	}
}

// DriftResolveHandler creates a job handler that asks StackKits to reconcile
// drift. The pinned CLI currently denies reconcile before side effects; the
// handler still dispatches that official command instead of applying tofu.
func DriftResolveHandler(cfg *DriftCheckConfig) JobHandler {
	cfg = normalizeDriftCheckConfig(cfg)

	return func(ctx context.Context, job *Job, q *Queue) error {
		startTime := time.Now()

		job.setStep(StepDriftValidate)
		q.UpdateProgress(job.ID, 10, "Validating stack for drift resolution...")

		stackID := job.TargetID
		if stackID == "" {
			return wrapDriftError(StepDriftValidate, "missing stack_id",
				"The drift resolution request did not include a stack ID.")
		}

		workDir := filepath.Join(cfg.WorkDir, stackID)

		job.setStep(StepDriftInitialize)
		q.UpdateProgress(job.ID, 20, "Preparing StackKits drift reconcile...")

		job.setStep(StepDriftCheckStacks)
		q.UpdateProgress(job.ID, 50, "Reconciling drift with StackKits...")

		if err := reconcileStackKitDrift(ctx, workDir, cfg.ReconcileTimeout); err != nil {
			return wrapDriftError(StepDriftCheckStacks, fmt.Sprintf("stackkit drift reconcile failed: %v", err),
				"Could not reconcile drift with the pinned StackKits CLI.")
		}

		q.UpdateProgress(job.ID, 90, "Drift resolved")
		job.replaceResult(map[string]interface{}{
			"stack_id":    stackID,
			"resolved":    true,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"executor":    "stackkit-drift-reconcile",
		})
		q.recordJobOutcome(job, jobAvailableOutcome(job))
		q.UpdateProgress(job.ID, 100, "Infrastructure synchronized successfully")
		return nil
	}
}

// RegisterDriftHandlers registers the drift detection job handlers on a queue.
func RegisterDriftHandlers(q *Queue, cfg *DriftCheckConfig) {
	cfg = normalizeDriftCheckConfig(cfg)
	q.RegisterHandler(JobTypeDriftCheck, DriftCheckHandler(cfg))
	q.RegisterHandler(JobTypeDriftResolve, DriftResolveHandler(cfg))
}

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

func resourceChangesFromDriftReport(report stackKitDriftReport) []ResourceChange {
	changes := make([]ResourceChange, 0, len(report.Subjects))
	for _, subject := range report.Subjects {
		if subject.Status != "drifted" {
			continue
		}
		changes = append(changes, ResourceChange{
			Address:      subject.Subject,
			ResourceType: "stackkit.subject",
			Name:         subject.Subject,
			Action:       "update",
			Changes: map[string]ChangeDetail{
				"status": {From: "in-sync", To: firstNonEmpty(subject.Code, subject.Status)},
			},
		})
	}
	return changes
}

func summarizeDriftSubjects(report stackKitDriftReport) PlanSummary {
	summary := PlanSummary{}
	for _, subject := range report.Subjects {
		if subject.Status == "drifted" {
			summary.ToUpdate++
			continue
		}
		summary.Unchanged++
	}
	return summary
}
