package orchestrator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/jobs"
)

// ============================================================
// F5: Drift Detection Methods
// ============================================================

// TriggerDriftCheck creates and enqueues a drift detection job for a stack.
func (o *Orchestrator) TriggerDriftCheck(stackID string, triggerType string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Find the stack record
	stack, err := o.app.FindRecordById("stacks", stackID)
	if err != nil {
		return "", fmt.Errorf("stack not found: %w", err)
	}

	// Create job record in PocketBase
	jobsCollection, err := o.app.FindCollectionByNameOrId("jobs")
	if err != nil {
		return "", fmt.Errorf("jobs collection not found: %w", err)
	}

	jobRecord := core.NewRecord(jobsCollection)
	jobRecord.Set("type", jobTypeForPersistence("drift_check"))
	jobRecord.Set("state", persistentStatePending)
	jobRecord.Set("progress", 0)
	jobRecord.Set("stack_id", stackID)
	jobRecord.Set("current_step", "Queued for drift detection")
	setRecordTenantIDFromStack(jobRecord, stack)

	if err := o.app.Save(jobRecord); err != nil {
		return "", fmt.Errorf("failed to create job record: %w", err)
	}

	// Create in-memory job
	job := &jobs.Job{
		ID:         jobRecord.Id,
		Type:       jobs.JobTypeDriftCheck,
		TargetType: targetTypeStack,
		TargetID:   stackID,
		TargetName: stack.GetString("name"),
		Payload: map[string]interface{}{
			"trigger_type": triggerType,
			"tenant_id":    stack.GetString("tenant_id"),
		},
		MaxAttempts: 1, // Drift checks should not auto-retry
	}

	// Enqueue with progress sync
	if err := o.enqueueWithSync(job, jobRecord, stack.GetString("tenant_id")); err != nil {
		return "", err
	}

	// Set up drift result sync
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.syncDriftResult(job.ID, stackID, triggerType)
	}()

	o.log.Info("drift_check_job_enqueued", "job_id", job.ID, "stack_id", stackID, "trigger_type", triggerType)

	return job.ID, nil
}

// TriggerDriftResolve creates and enqueues a drift resolution job for a stack.
func (o *Orchestrator) TriggerDriftResolve(stackID string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Find the stack record
	stack, err := o.app.FindRecordById("stacks", stackID)
	if err != nil {
		return "", fmt.Errorf("stack not found: %w", err)
	}

	// Create job record in PocketBase
	jobsCollection, err := o.app.FindCollectionByNameOrId("jobs")
	if err != nil {
		return "", fmt.Errorf("jobs collection not found: %w", err)
	}

	jobRecord := core.NewRecord(jobsCollection)
	jobRecord.Set("type", jobTypeForPersistence("drift_resolve"))
	jobRecord.Set("state", persistentStatePending)
	jobRecord.Set("progress", 0)
	jobRecord.Set("stack_id", stackID)
	jobRecord.Set("current_step", "Queued for drift resolution")
	setRecordTenantIDFromStack(jobRecord, stack)

	if err := o.app.Save(jobRecord); err != nil {
		return "", fmt.Errorf("failed to create job record: %w", err)
	}

	// Create in-memory job
	job := &jobs.Job{
		ID:          jobRecord.Id,
		Type:        jobs.JobTypeDriftResolve,
		TargetType:  targetTypeStack,
		TargetID:    stackID,
		TargetName:  stack.GetString("name"),
		MaxAttempts: 1,
	}

	// Enqueue with progress sync
	if err := o.enqueueWithSync(job, jobRecord, stack.GetString("tenant_id")); err != nil {
		return "", err
	}

	o.log.Info("drift_resolve_job_enqueued", "job_id", job.ID, "stack_id", stackID)

	return job.ID, nil
}

// syncDriftResult monitors a drift check job and saves the result to drift_results collection.
func (o *Orchestrator) syncDriftResult(jobID, stackID, triggerType string) {
	ticker := time.NewTicker(1 * time.Second)
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

			// Check if job is done
			if snapshot.State != jobs.JobStateCompleted && snapshot.State != jobs.JobStateFailed && snapshot.State != jobs.JobStateCancelled {
				continue
			}

			// Job is done - save drift result
			o.saveDriftResult(snapshot, stackID, triggerType)
			return
		}
	}
}

// saveDriftResult stores the drift detection result in the drift_results collection.
func (o *Orchestrator) saveDriftResult(job jobs.JobSnapshot, stackID, triggerType string) {
	// Get drift_results collection
	collection, err := o.app.FindCollectionByNameOrId("drift_results")
	if err != nil {
		o.log.Error("drift_results_collection_not_found", "error", err)
		return
	}

	// Get stack for owner_id
	stack, err := o.app.FindRecordById("stacks", stackID)
	if err != nil {
		o.log.Error("stack_not_found_for_drift_result", "stack_id", stackID, "error", err)
		return
	}

	// Create drift result record
	record := core.NewRecord(collection)
	record.Set("stack_id", stackID)
	record.Set("job_id", job.ID)
	record.Set("owner_id", stack.GetString("owner_id"))
	record.Set("trigger_type", triggerType)
	record.Set("checked_at", time.Now())
	populateDriftResultRecord(record, job)

	// Save drift result
	if err := o.app.Save(record); err != nil {
		o.log.Error("failed_to_save_drift_result", "stack_id", stackID, "error", err)
		return
	}

	// Update stack with drift status and link to result
	status := record.GetString("status")
	stack.Set("drift_status", status)
	stack.Set("drift_checked_at", time.Now())
	stack.Set("last_drift_result_id", record.Id)

	if err := o.app.Save(stack); err != nil {
		o.log.Error("failed_to_update_stack_drift_status", "stack_id", stackID, "error", err)
	}

	// Log drift result as activity
	o.logDriftActivity(stackID, stack.GetString("name"), status, record.Id)

	o.log.Info("drift_result_saved",
		"stack_id", stackID,
		"result_id", record.Id,
		"status", status,
	)
}

func populateDriftResultRecord(record *core.Record, job jobs.JobSnapshot) { // pocketbase-migration-compat: legacy drift projection only
	if job.Result != nil {
		status, ok := job.Result["status"].(string)
		if !ok {
			status = "unknown"
		}
		record.Set("status", status)
		if durationMs, ok := job.Result["duration_ms"].(int64); ok {
			record.Set("duration_ms", durationMs)
		}
		populateDriftResultDetails(record, decodeDriftResult(job.Result["drift_result"]))
	}
	if job.State == jobs.JobStateFailed {
		record.Set("status", "failed")
		record.Set("error_message", job.Error)
		record.Set("error_details", job.ErrorDetails)
	}
}

func decodeDriftResult(value any) map[string]interface{} {
	switch typed := value.(type) {
	case []byte:
		var result map[string]interface{}
		_ = json.Unmarshal(typed, &result)
		return result
	case string:
		var result map[string]interface{}
		_ = json.Unmarshal([]byte(typed), &result)
		return result
	case map[string]interface{}:
		return typed
	default:
		return nil
	}
}

func populateDriftResultDetails(record *core.Record, result map[string]interface{}) { // pocketbase-migration-compat: legacy drift projection only
	if result == nil {
		return
	}
	if affected, ok := result["affected_resources"]; ok {
		record.Set("affected_resources", affected)
	}
	if summary, ok := result["plan_summary"]; ok {
		record.Set("plan_summary", summary)
	}
	if count, ok := result["affected_count"].(float64); ok {
		record.Set("affected_count", int(count))
	}
}

// logDriftActivity creates an activity_log entry for a drift check result.
func (o *Orchestrator) logDriftActivity(stackID, stackName, status, resultID string) {
	collection, err := o.app.FindCollectionByNameOrId("activity_log")
	if err != nil {
		o.log.Error("activity_log_collection_not_found", "error", err)
		return
	}

	var action, details string
	switch status {
	case "drifted":
		action = "drift_detected"
		details = fmt.Sprintf("Drift detected in stack '%s'", stackName)
	case "in_sync":
		action = "drift_clean"
		details = fmt.Sprintf("Stack '%s' is in sync (no drift)", stackName)
	default:
		action = "drift_failed"
		details = fmt.Sprintf("Drift check failed for stack '%s'", stackName)
	}

	record := core.NewRecord(collection)
	record.Set("action", action)
	record.Set("details", details)
	record.Set("stack_id", stackID)
	if stack, err := o.app.FindRecordById("stacks", stackID); err == nil {
		setRecordTenantIDFromStack(record, stack)
	}
	record.Set("metadata", map[string]interface{}{
		"drift_result_id": resultID,
		"drift_status":    status,
	})

	if err := o.app.Save(record); err != nil {
		o.log.Error("failed_to_log_drift_activity", "stack_id", stackID, "error", err)
	}
}
