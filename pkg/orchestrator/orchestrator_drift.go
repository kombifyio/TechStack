package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

type DriftLifecycleRequest struct {
	RequestContext context.Context
	TenantID       string
	OwnerID        string
	StackID        string
	TriggerType    string
}

func (o *Orchestrator) TriggerDriftCheck(req DriftLifecycleRequest) (string, error) {
	return o.triggerDriftJob(req, jobs.JobTypeDriftCheck, "Queued for drift detection")
}

func (o *Orchestrator) TriggerDriftResolve(req DriftLifecycleRequest) (string, error) {
	return o.triggerDriftJob(req, jobs.JobTypeDriftResolve, "Queued for drift resolution")
}

func (o *Orchestrator) triggerDriftJob(req DriftLifecycleRequest, jobType jobs.JobType, step string) (string, error) {
	if err := requireDriftLifecycleIdentity(req); err != nil {
		return "", err
	}
	if o.jobStore == nil || o.effectiveStackStore() == nil || o.driftStore == nil {
		return "", fmt.Errorf("canonical drift lifecycle stores are required")
	}
	ctx := req.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	stack, err := o.findStackForJob(ctx, req.StackID, ProvisionStackOptions{
		RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID,
	})
	if err != nil {
		return "", fmt.Errorf("stack not found: %w", err)
	}
	if jobType == jobs.JobTypeDriftResolve && stack.driftStatus != "drifted" {
		return "", fmt.Errorf("stack is not eligible for drift resolution")
	}

	if jobType == jobs.JobTypeDriftCheck {
		if err := o.updateCanonicalDriftStatus(ctx, stack, "checking", nil); err != nil {
			return "", err
		}
	}
	jobID, err := o.createJobRecordForStack(ctx, stack, string(jobType), step)
	if err != nil {
		if jobType == jobs.JobTypeDriftCheck {
			_ = o.updateCanonicalDriftStatus(ctx, stack, "unknown", nil)
		}
		return "", err
	}
	job := &jobs.Job{
		ID: jobID, Type: jobType, TargetType: targetTypeStack, TargetID: stack.id, TargetName: stack.name,
		Payload: map[string]interface{}{
			"trigger_type": firstNonEmptyString(req.TriggerType, "manual"),
			"tenant_id":    stack.tenantID,
			"owner_id":     stack.ownerID,
		},
		MaxAttempts: 1,
	}
	if err := o.enqueueWithSync(job, stack.tenantID); err != nil {
		if jobType == jobs.JobTypeDriftCheck {
			_ = o.updateCanonicalDriftStatus(ctx, stack, "unknown", nil)
		}
		return "", err
	}
	if jobType == jobs.JobTypeDriftCheck {
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			o.syncDriftResult(job.ID, stack.tenantID, stack.ownerID, stack.id, firstNonEmptyString(req.TriggerType, "manual"))
		}()
	}
	o.log.Info("drift_job_enqueued", "job_id", job.ID, "stack_id", stack.id, "job_type", jobType)
	return job.ID, nil
}

func requireDriftLifecycleIdentity(req DriftLifecycleRequest) error {
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.OwnerID) == "" || strings.TrimSpace(req.StackID) == "" {
		return fmt.Errorf("drift lifecycle requires exact tenant, owner, and stack identity")
	}
	return nil
}

func (o *Orchestrator) syncDriftResult(jobID, tenantID, ownerID, stackID, triggerType string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			job, ok := o.queue.Get(jobID)
			if !ok {
				return
			}
			snapshot := job.Snapshot()
			if snapshot.PersistenceSuppressed {
				return
			}
			if snapshot.State != jobs.JobStateCompleted && snapshot.State != jobs.JobStateFailed && snapshot.State != jobs.JobStateCancelled {
				continue
			}
			o.saveDriftResult(snapshot, tenantID, ownerID, stackID, triggerType)
			return
		}
	}
}

func (o *Orchestrator) saveDriftResult(job jobs.JobSnapshot, tenantID, ownerID, stackID, triggerType string) {
	result := driftCheckResultFromSnapshot(job)
	if result.StackID != "" && result.StackID != stackID {
		o.log.Error("drift_result_stack_mismatch", "job_id", job.ID, "stack_id", stackID, "result_stack_id", result.StackID)
		return
	}
	checkedAt := result.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = o.now()
	}
	status := string(result.Status)
	if status == "" {
		status = "unknown"
	}
	affected := make([]map[string]any, 0, len(result.AffectedResources))
	if payload, err := json.Marshal(result.AffectedResources); err == nil {
		_ = json.Unmarshal(payload, &affected)
	}
	plan := map[string]any{}
	if payload, err := json.Marshal(result.PlanSummary); err == nil {
		_ = json.Unmarshal(payload, &plan)
	}
	details := map[string]any{
		"trigger_type":  firstNonEmptyString(result.TriggerType, triggerType),
		"checked_at":    checkedAt.Format(time.RFC3339Nano),
		"duration_ms":   result.DurationMs,
		"error_message": result.ErrorMessage,
		"error_details": result.ErrorDetails,
	}
	resultID := job.ID + ":drift"
	saved, err := o.driftStore.CreateDriftResult(o.ctx, controlplane.DriftResult{
		ID: resultID, TenantID: tenantID, OwnerSubjectID: ownerID, StackID: stackID, JobID: job.ID,
		Status: status, AffectedResources: affected, PlanSummary: plan, Details: details,
	})
	if err != nil {
		o.log.Error("failed_to_save_drift_result", "stack_id", stackID, "error", err)
		return
	}
	stack, err := o.effectiveStackStore().GetStack(o.ctx, tenantID, stackID)
	if err != nil || stack.OwnerSubjectID != ownerID {
		o.log.Error("stack_not_found_for_drift_result", "stack_id", stackID, "error", err)
		return
	}
	if _, err := o.effectiveStackStore().UpdateStackRuntime(o.ctx, tenantID, stackID, controlplane.RuntimeUpdate{
		Status: stack.Status, RuntimeSummary: stack.RuntimeSummary, DriftStatus: status, DriftCheckedAt: &checkedAt,
	}); err != nil {
		o.log.Error("failed_to_update_stack_drift_status", "stack_id", stackID, "error", err)
	}
	o.appendDriftActivity(tenantID, ownerID, stack, saved)
}

func driftCheckResultFromSnapshot(job jobs.JobSnapshot) jobs.DriftCheckResult {
	var result jobs.DriftCheckResult
	if job.Result != nil {
		if payload, err := json.Marshal(job.Result["drift_result"]); err == nil {
			_ = json.Unmarshal(payload, &result)
		}
		if result.Status == "" {
			if status, ok := job.Result["status"].(string); ok {
				result.Status = jobs.DriftStatus(status)
			}
		}
	}
	if job.State == jobs.JobStateFailed {
		result.Status = jobs.DriftStatusFailed
		result.ErrorMessage = firstNonEmptyString(result.ErrorMessage, job.Error)
		result.ErrorDetails = firstNonEmptyString(result.ErrorDetails, job.ErrorDetails)
	}
	return result
}

func (o *Orchestrator) updateCanonicalDriftStatus(ctx context.Context, stack *orchestratorStack, status string, checkedAt *time.Time) error {
	if stack == nil {
		return controlplane.ErrNotFound
	}
	_, err := o.effectiveStackStore().UpdateStackRuntime(ctx, stack.tenantID, stack.id, controlplane.RuntimeUpdate{
		Status: stack.status, RuntimeSummary: stack.runtimeSummary, DriftStatus: status, DriftCheckedAt: checkedAt,
	})
	return err
}

func (o *Orchestrator) appendDriftActivity(tenantID, ownerID string, stack *controlplane.Stack, result *controlplane.DriftResult) {
	if o.activityStore == nil || stack == nil || result == nil {
		return
	}
	action, message := "drift_failed", fmt.Sprintf("Drift check failed for stack '%s'", stack.Name)
	if result.Status == "drifted" {
		action, message = "drift_detected", fmt.Sprintf("Drift detected in stack '%s'", stack.Name)
	} else if result.Status == "in_sync" {
		action, message = "drift_clean", fmt.Sprintf("Stack '%s' is in sync (no drift)", stack.Name)
	}
	_, err := o.activityStore.AppendActivity(o.ctx, controlplane.ActivityEvent{
		ID: result.ID + ":activity", TenantID: tenantID, InstanceID: stack.InstanceID, StackID: stack.ID,
		CorrelationID: result.JobID, ActorSubjectID: ownerID, Action: action, Category: "drift",
		Severity: "info", Message: message, Details: map[string]any{"drift_result_id": result.ID, "drift_status": result.Status},
	})
	if err != nil && !errors.Is(err, controlplane.ErrConflict) {
		o.log.Error("failed_to_log_drift_activity", "stack_id", stack.ID, "error", err)
	}
}
