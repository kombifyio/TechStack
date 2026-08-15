package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/internal/walletsync"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// enqueueWithSync enqueues a job and sets up progress synchronization.
func (o *Orchestrator) enqueueWithSync(job *jobs.Job, record *core.Record, tenantID string) error {
	tenantID = strings.TrimSpace(tenantID)
	if err := job.BindTenantID(tenantID); err != nil {
		return fmt.Errorf("bind durable job tenant: %w", err)
	}
	if err := o.ensureDurablePendingJob(job, tenantID); err != nil {
		return err
	}
	// Enqueue first to ensure job exists before sync starts
	if err := o.queue.Enqueue(job); err != nil {
		return err
	}

	recordID := ""
	if record != nil {
		recordID = record.Id
	}
	jobID := job.Snapshot().ID

	// Start progress sync goroutine with proper tracking
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.syncJobProgress(jobID, recordID, strings.TrimSpace(tenantID))
	}()

	return nil
}

func (o *Orchestrator) ensureDurablePendingJob(job *jobs.Job, tenantID string) error {
	if o.jobStore == nil || tenantID == "" || job == nil {
		return nil
	}
	snapshot := job.Snapshot()
	if snapshot.ID == "" || strings.TrimSpace(snapshot.TargetID) == "" || strings.TrimSpace(string(snapshot.Type)) == "" {
		return fmt.Errorf("durable job identity requires id, stack, and type")
	}
	if existing, err := o.jobStore.GetJob(o.ctx, tenantID, snapshot.ID); err == nil {
		return validateDurablePendingJob(existing, snapshot)
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return fmt.Errorf("load durable pending job: %w", err)
	}
	o.ensureControlPlaneStackForJob(o.ctx, snapshot, tenantID)
	request := controlplane.UpsertJobRequest{
		ID: snapshot.ID, TenantID: tenantID, StackID: snapshot.TargetID, Type: string(snapshot.Type),
		State: persistentStatePending, Priority: snapshot.Priority, Progress: snapshot.Progress,
		Step: snapshot.Step, Message: snapshot.Message, Error: snapshot.Error, ErrorDetails: snapshot.ErrorDetails,
		Logs: controlPlaneJobLogs(snapshot.Logs), Result: projectedLegacyJobResult(snapshot),
		ScheduledFor: time.Now().UTC(),
	}
	if _, err := o.jobStore.CreateJob(o.ctx, request); err == nil {
		return nil
	} else if !errors.Is(err, controlplane.ErrConflict) {
		return fmt.Errorf("create durable pending job: %w", err)
	}
	existing, err := o.jobStore.GetJob(o.ctx, tenantID, snapshot.ID)
	if err != nil {
		return fmt.Errorf("load existing durable job: %w", err)
	}
	return validateDurablePendingJob(existing, snapshot)
}

func validateDurablePendingJob(existing *controlplane.Job, snapshot jobs.JobSnapshot) error {
	if existing == nil {
		return fmt.Errorf("durable job is missing")
	}
	if existing.StackID != snapshot.TargetID || !strings.EqualFold(strings.TrimSpace(existing.Type), strings.TrimSpace(string(snapshot.Type))) ||
		canonicalEnrollmentJobState(existing.State) != persistentStatePending {
		return fmt.Errorf("%w: durable job %s belongs to another or active dispatch", controlplane.ErrConflict, snapshot.ID)
	}
	return nil
}

// syncJobProgress periodically syncs job progress to PocketBase.
func (o *Orchestrator) syncJobProgress(jobID, recordID, tenantID string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			// Shutdown cancels this loop and the job's own context at the same
			// instant. The queue reacts by marking the job cancelled in memory
			// (queue.go cancelJobInternal), but that state only ever reaches the
			// database through this loop -- which would otherwise be gone. The
			// row would then stay "running" forever, holding the per-stack
			// execution claim and stranding the stack with no error to show for
			// it. Take one last detached write on the way out.
			o.flushTerminalJobStateAfterShutdown(jobID, tenantID)
			return
		case <-ticker.C:
			job, ok := o.queue.Get(jobID)
			if !ok {
				return
			}
			snapshot := job.Snapshot()
			if snapshot.PersistenceSuppressed {
				o.log.Info("job_sync_detached_from_local_runtime", "job_id", snapshot.ID)
				return
			}

			if recordID != "" {
				o.syncPocketBaseJobSnapshot(snapshot, recordID)
			}
			controlPlaneSynced := true
			if tenantID != "" && o.jobStore != nil {
				if err := o.syncControlPlaneJobSnapshot(snapshot, tenantID); errors.Is(err, controlplane.ErrConflict) {
					if current, ok := o.queue.Get(jobID); ok && jobSnapshotFenceAdvanced(snapshot, current.Snapshot()) {
						o.log.Debug("stale_job_snapshot_fenced_retrying_current_state", "job_id", snapshot.ID, "tenant_id", tenantID)
						continue
					}
					// The durable row belongs to another executor now. Returning
					// here used to stop only the reporting: the handler kept
					// running and kept driving real providers, so two processes
					// could act on one job - the thing the fence exists to
					// prevent - and updated_at froze on a job that was very much
					// alive, which is the one false positive the stale-job
					// abandon in abandon_stale_job.go could act on.
					detached := o.queue.DetachFencedExecutionIfUnchanged(
						snapshot.ID,
						snapshot,
						"Durable job execution was claimed by another process",
					)
					if !detached {
						if current, ok := o.queue.Get(jobID); ok && jobSnapshotFenceAdvanced(snapshot, current.Snapshot()) {
							o.log.Debug("stale_job_snapshot_fenced_retrying_current_state", "job_id", snapshot.ID, "tenant_id", tenantID)
							continue
						}
					}
					o.log.Warn(
						"job_sync_fenced_by_durable_state",
						"job_id", snapshot.ID,
						"tenant_id", tenantID,
						"local_execution_detached", detached,
					)
					return
				} else if err != nil {
					controlPlaneSynced = false
				}
			}

			// Check if job is done
			if controlPlaneSynced && (snapshot.State == jobs.JobStateCompleted || snapshot.State == jobs.JobStateFailed || snapshot.State == jobs.JobStateCancelled) {
				// Final update to stack status
				o.updateStackStatusSnapshot(snapshot)
				return
			}
		}
	}
}

func jobSnapshotFenceAdvanced(observed, current jobs.JobSnapshot) bool {
	if observed.Type != current.Type || observed.State != current.State {
		return true
	}
	if observed.StartedAt == nil || current.StartedAt == nil {
		return observed.StartedAt != nil || current.StartedAt != nil
	}
	return !observed.StartedAt.Equal(*current.StartedAt)
}

func (o *Orchestrator) syncPocketBaseJob(job *jobs.Job, recordID string) {
	if job == nil {
		return
	}
	o.syncPocketBaseJobSnapshot(job.Snapshot(), recordID)
}

func (o *Orchestrator) syncPocketBaseJobSnapshot(job jobs.JobSnapshot, recordID string) {
	record, err := o.app.FindRecordById("jobs", recordID)
	if err != nil {
		o.log.Error("job_record_not_found", "record_id", recordID, "error", err)
		return
	}

	record.Set("state", projectedLegacyJobState(job))
	record.Set("progress", job.Progress)
	if job.Step != "" {
		record.Set("step", job.Step)
	}
	if len(job.Logs) > 0 {
		lastLog := job.Logs[len(job.Logs)-1]
		record.Set("current_step", lastLog.Message)
		record.Set("message", lastLog.Message)
	}
	if job.Message != "" {
		record.Set("message", job.Message)
	}
	if job.State == jobs.JobStateWaiting {
		record.Set("error", "")
		record.Set("error_details", "")
	}
	if job.Error != "" {
		record.Set("error", job.Error)
	}
	if job.ErrorDetails != "" {
		record.Set("error_details", job.ErrorDetails)
	}
	if result := projectedLegacyJobResult(job); result != nil {
		record.Set("result", result)
	}
	if err := o.app.Save(record); err != nil {
		o.log.Error("failed_to_sync_job", "job_id", job.ID, "error", err)
	}
}

// terminalFlushTimeout bounds the shutdown write. Stop() already waits on the
// sync waitgroup for five seconds, so this stays well inside that budget: a
// write that cannot land in this window would be cut off by the process exiting
// anyway, and blocking longer would only delay the shutdown it is racing.
const terminalFlushTimeout = 3 * time.Second

// flushTerminalJobStateAfterShutdown makes one detached attempt to persist a
// job that reached a terminal state as the process was going down.
//
// It deliberately does not use o.ctx: that context is already cancelled, which
// is the whole reason this function exists. A job left mid-flight is not
// terminal and is left alone -- reclaiming genuinely unknown work is a separate
// decision with provider-side-effect safety attached to it.
func (o *Orchestrator) flushTerminalJobStateAfterShutdown(jobID, tenantID string) {
	if o.queue == nil || o.jobStore == nil || strings.TrimSpace(tenantID) == "" {
		return
	}
	job, ok := o.queue.Get(jobID)
	if !ok {
		return
	}
	snapshot := job.Snapshot()
	if snapshot.PersistenceSuppressed || !terminalJobSnapshot(snapshot) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(o.ctx), terminalFlushTimeout)
	defer cancel()
	if err := o.syncControlPlaneJobSnapshotContext(ctx, snapshot, tenantID); err != nil {
		// Loud on purpose. A terminal state that never lands is exactly the
		// silence that made stranded rollouts look like hangs for hours.
		o.log.Error("terminal_job_state_not_persisted_on_shutdown",
			"job_id", snapshot.ID, "tenant_id", tenantID,
			"state", string(snapshot.State), "step", snapshot.Step, "error", err)
		return
	}
	o.log.Warn("terminal_job_state_persisted_on_shutdown",
		"job_id", snapshot.ID, "tenant_id", tenantID, "state", string(snapshot.State), "step", snapshot.Step)
}

func terminalJobSnapshot(snapshot jobs.JobSnapshot) bool {
	switch snapshot.State {
	case jobs.JobStateCompleted, jobs.JobStateFailed, jobs.JobStateCancelled:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) syncControlPlaneJob(job *jobs.Job, tenantID string) {
	if job == nil {
		return
	}
	_ = o.syncControlPlaneJobSnapshot(job.Snapshot(), tenantID)
}

func (o *Orchestrator) syncControlPlaneJobSnapshot(job jobs.JobSnapshot, tenantID string) error {
	return o.syncControlPlaneJobSnapshotContext(o.ctx, job, tenantID)
}

func (o *Orchestrator) syncDurableJobExecutionSnapshot(ctx context.Context, job jobs.JobSnapshot) error {
	tenantID := firstNonEmptyJobString(job, "tenant_id")
	if tenantID == "" {
		return nil
	}
	err := o.syncControlPlaneJobSnapshotContext(ctx, job, tenantID)
	if errors.Is(err, controlplane.ErrConflict) {
		return jobs.ErrExecutionSnapshotFenced
	}
	return err
}

func (o *Orchestrator) syncControlPlaneJobSnapshotContext(ctx context.Context, job jobs.JobSnapshot, tenantID string) error {
	if ctx == nil {
		ctx = o.ctx
	}
	o.ensureControlPlaneStackForJob(ctx, job, tenantID)
	message := job.Message
	if message == "" && len(job.Logs) > 0 {
		message = job.Logs[len(job.Logs)-1].Message
	}
	_, err := o.jobStore.SyncJobSnapshot(ctx, controlplane.SyncJobSnapshotRequest{
		Job: controlplane.UpsertJobRequest{
			ID:           job.ID,
			TenantID:     tenantID,
			StackID:      job.TargetID,
			Type:         string(job.Type),
			State:        projectedControlPlaneJobState(job),
			Progress:     job.Progress,
			Step:         job.Step,
			Message:      message,
			Error:        job.Error,
			ErrorDetails: job.ErrorDetails,
			Logs:         controlPlaneJobLogs(job.Logs),
			Result:       projectedLegacyJobResult(job),
			ScheduledFor: projectedLegacyJobSchedule(job),
		},
		ObservedState:    string(job.State),
		AttemptStartedAt: job.StartedAt,
		CompletedAt:      job.CompletedAt,
	})
	if err != nil {
		if !errors.Is(err, controlplane.ErrConflict) {
			o.log.Error("failed_to_sync_controlplane_job", "job_id", job.ID, "tenant_id", tenantID, "error", err)
		}
		return err
	}
	return nil
}

func projectedLegacyJobState(job jobs.JobSnapshot) string {
	if job.State == jobs.JobStateWaiting {
		// Both legacy schemas currently constrain state to pending/running/
		// terminal values. The API reconstructs waiting from result.job_wait.
		return string(jobs.JobStatePending)
	}
	return string(job.State)
}

func projectedControlPlaneJobState(job jobs.JobSnapshot) string {
	state := projectedLegacyJobState(job)
	if state == string(jobs.JobStateCancelled) {
		// The PostgreSQL jobs constraint predates the public API and uses the
		// British spelling. Keep the storage adapter responsible for that legacy
		// detail so queue/API callers consistently use `canceled`.
		return "cancelled"
	}
	return state
}

func projectedLegacyJobSchedule(job jobs.JobSnapshot) time.Time {
	if job.State == jobs.JobStateWaiting && job.NextResumeAt != nil {
		return job.NextResumeAt.UTC()
	}
	return job.CreatedAt
}

func projectedLegacyJobResult(job jobs.JobSnapshot) map[string]interface{} {
	if job.Result == nil && job.State != jobs.JobStateWaiting {
		return nil
	}
	result := make(map[string]interface{}, len(job.Result)+1)
	for key, value := range job.Result {
		result[key] = value
	}
	if job.State != jobs.JobStateWaiting {
		delete(result, "job_wait")
		return result
	}
	wait := map[string]interface{}{
		"state":   string(jobs.JobStateWaiting),
		"reason":  job.WaitReason,
		"message": job.Message,
	}
	if job.NextResumeAt != nil {
		wait["next_resume_at"] = job.NextResumeAt.UTC().Format(time.RFC3339Nano)
	}
	result["job_wait"] = wait
	return result
}

func (o *Orchestrator) ensureControlPlaneStackForJob(ctx context.Context, job jobs.JobSnapshot, tenantID string) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(job.TargetID) == "" {
		return
	}
	if noWorkspaceDestroyProjectionReconciled(job) {
		// A completed reconciliation intentionally made the projection invisible.
		// Never recreate it just to persist this terminal destroy receipt.
		return
	}
	store := o.effectiveStackStore()
	if store == nil {
		return
	}
	if _, err := store.GetStack(ctx, tenantID, job.TargetID); err == nil {
		return
	}
	ownerID := firstNonEmptyString(
		stringFromAny(job.Payload["owner_id"]),
		stringFromAny(job.Result["owner_id"]),
	)
	stackName := firstNonEmptyString(
		job.TargetName,
		stringFromAny(job.Payload["stack_name"]),
		stringFromAny(job.Result["stack_name"]),
		job.TargetID,
	)
	config := map[string]any{}
	if spec, ok := job.Payload["spec"].(map[string]interface{}); ok {
		config["user_config"] = spec
	}
	for _, key := range []string{
		runtimeFieldLane,
		runtimeFieldProvisionMode,
		runtimeFieldConnectionMode,
		runtimeFieldStackKitRef,
		runtimeFieldLeaseProvider,
		runtimeFieldProviderRegion,
		runtimeFieldIONOSDatacenter,
		runtimeFieldSimProviderID,
	} {
		if value := firstNonNil(job.Result[key], job.Payload[key]); value != nil {
			config[key] = value
		}
	}
	_, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             job.TargetID,
		TenantID:       tenantID,
		OwnerSubjectID: ownerID,
		Name:           stackName,
		Mode:           stackModeEasy,
		Status:         persistentStateProvisioning,
		Config:         config,
	})
	if err != nil && !errors.Is(err, controlplane.ErrConflict) {
		o.log.Error("failed_to_project_stack_for_controlplane_job", "stack_id", job.TargetID, "tenant_id", tenantID, "error", err)
	}
}

func controlPlaneJobLogs(logs []jobs.LogEntry) []map[string]any {
	out := make([]map[string]any, 0, len(logs))
	for _, entry := range logs {
		out = append(out, map[string]any{
			"timestamp": entry.Timestamp.UTC().Format(time.RFC3339),
			"level":     entry.Level,
			"message":   entry.Message,
		})
	}
	return out
}

// updateStackStatus updates the stack status based on job completion.
func (o *Orchestrator) updateStackStatus(job *jobs.Job) {
	if job == nil {
		return
	}
	o.updateStackStatusSnapshot(job.Snapshot())
}

func (o *Orchestrator) updateStackStatusSnapshot(job jobs.JobSnapshot) {
	if noWorkspaceDestroyProjectionReconciled(job) {
		// The handler has already archived this exact control-plane projection
		// through the durable reconciliation callback. Do not revive its state
		// or touch a legacy PocketBase row while handling the terminal snapshot.
		return
	}
	newStatus := stackStatusForJob(job)
	if newStatus == "" {
		return
	}

	stack, stackErr := o.updateLegacyStackStatus(job, newStatus)
	o.updateControlPlaneStackStatus(job, newStatus)
	if job.State != jobs.JobStateCompleted || job.Result == nil {
		return
	}
	if syncErr := o.syncStackKitRuntimeInventoryFromJobSnapshot(firstNonEmptyJobString(job, "tenant_id"), job); syncErr != nil {
		o.log.Warn("stackkit_inventory_projection_failed", "stack_id", job.TargetID, "error", syncErr)
	}
	if stackErr == nil {
		o.syncCompletedLegacyStack(stack, job)
	}
}

func noWorkspaceDestroyProjectionReconciled(job jobs.JobSnapshot) bool {
	return job.Type == jobs.JobTypeDestroy &&
		job.State == jobs.JobStateCompleted &&
		strings.EqualFold(strings.TrimSpace(stringResult(job.Result[jobs.DestroyWorkspaceStateResultField])), jobs.DestroyWorkspaceStateAbsent) &&
		boolResult(job.Result[jobs.DestroyProjectionReconciledResultField])
}

func (o *Orchestrator) updateLegacyStackStatus(job jobs.JobSnapshot, newStatus string) (*core.Record, error) { // pocketbase-migration-compat: legacy stack projection only
	stack, err := o.app.FindRecordById("stacks", job.TargetID)
	if err != nil {
		o.log.Error("stack_not_found_for_update", "stack_id", job.TargetID, "error", err)
		return nil, err
	}
	if job.Result != nil {
		copyRuntimeResultFieldsToStack(stack, job.Result)
	}
	stack.Set("status", newStatus)
	if saveErr := o.app.Save(stack); saveErr != nil { // pocketbase-migration-compat: legacy stack projection only
		o.log.Error("failed_to_update_stack_status", "stack_id", job.TargetID, "error", saveErr)
	}
	return stack, nil
}

func (o *Orchestrator) syncCompletedLegacyStack(stack *core.Record, job jobs.JobSnapshot) { // pocketbase-migration-compat: legacy stack projection only
	if err := o.syncStackKitRuntimeInventorySnapshot(stack, job); err != nil {
		o.log.Warn("stackkit_inventory_projection_failed", "stack_id", job.TargetID, "error", err)
	}
	if err := o.ensureWorkerPairingToken(stack, job.Result); err != nil {
		o.log.Warn("worker_pairing_token_persist_failed", "stack_id", job.TargetID, "error", err)
	}
	req := walletsync.SyncRequest{
		TenantID:  stack.GetString("tenant_id"),
		OwnerID:   stack.GetString("owner_id"),
		StackID:   job.TargetID,
		StackName: stack.GetString("name"),
		Result:    job.Result,
	}
	count, err := o.syncStackKitWalletOutputs(req)
	if err != nil {
		o.log.Warn("stackkit_wallet_sync_failed", "stack_id", job.TargetID, "error", err)
	} else if count > 0 {
		o.log.Info("stackkit_wallet_sync_completed", "stack_id", job.TargetID, "items", count)
	}
}

func stackStatusForJob(job jobs.JobSnapshot) string {
	switch job.State {
	case jobs.JobStateCompleted:
		return completedStackStatusForJobType(job.Type)
	case jobs.JobStateFailed:
		return "error"
	case jobs.JobStateCancelled:
		// Cancellation stops a job, not the stack. In particular an enrollment
		// source superseded by an exact replacement and a deploy canceled for a
		// queued destroy must not overwrite the newer lifecycle status.
		return ""
	default:
		return ""
	}
}

func completedStackStatusForJobType(jobType jobs.JobType) string {
	switch jobType {
	case jobs.JobTypeProvision:
		// Provision is preparation only (intent + requirements). Rollout happens in deploy.
		return persistentStatePending
	case jobs.JobTypeDeploy:
		return persistentStateRunning
	case jobs.JobTypeDestroy:
		return "stopped"
	default:
		return ""
	}
}

type stackResultFieldSetter interface {
	Set(string, any)
}

func copyRuntimeResultFieldsToStack(stack stackResultFieldSetter, result map[string]interface{}) {
	for resultKey, fieldName := range stackRuntimeResultFieldMap() {
		if value, ok := result[resultKey]; ok {
			stack.Set(fieldName, value)
		}
	}
}

func stackRuntimeResultFieldMap() map[string]string {
	return map[string]string{
		"runtime_phase":                "runtime_phase",
		"server_mode":                  "server_mode",
		runtimeFieldLane:               runtimeFieldLane,
		"runtime_offering_id":          "runtime_offering_id",
		runtimeFieldLeaseProvider:      runtimeFieldLeaseProvider,
		runtimeFieldProviderRegion:     runtimeFieldProviderRegion,
		runtimeFieldIONOSDatacenter:    runtimeFieldIONOSDatacenter,
		"lease_id":                     "lease_id",
		runtimeFieldSimProviderID:      runtimeFieldSimProviderID,
		"simulate_node_lifecycle":      "simulate_node_lifecycle",
		"desired_state":                "desired_state",
		"billing_mode":                 "billing_mode",
		"billing_cadence":              "billing_cadence",
		runtimeFieldStackKitRef:        runtimeFieldStackKitRef,
		"verification_status":          "verification_status",
		runtimeFieldProvisionMode:      runtimeFieldProvisionMode,
		runtimeFieldConnectionMode:     runtimeFieldConnectionMode,
		"server_remote_host_present":   "server_remote_host_present",
		"server_remote_user_present":   "server_remote_user_present",
		"server_remote_auth_method":    "server_remote_auth_method",
		"server_remote_credential_ref": "server_remote_credential_ref",
		"server_remote_use_sudo":       "server_remote_use_sudo",
		runtimeFieldInstallCommand:     runtimeFieldInstallCommand,
	}
}

func (o *Orchestrator) updateControlPlaneStackStatus(job jobs.JobSnapshot, newStatus string) {
	if o.stackStore == nil {
		return
	}
	tenantID := firstNonEmptyJobString(job, "tenant_id")
	if tenantID == "" {
		return
	}
	runtimeSummary := map[string]any{}
	for key, value := range job.Result {
		runtimeSummary[key] = value
	}
	if _, err := o.stackStore.UpdateStackRuntime(o.ctx, tenantID, job.TargetID, controlplane.RuntimeUpdate{
		Status:         newStatus,
		RuntimeSummary: runtimeSummary,
	}); err != nil {
		o.log.Error("failed_to_update_controlplane_stack_status", "stack_id", job.TargetID, "tenant_id", tenantID, "error", err)
	}
}

func firstNonEmptyJobString(job jobs.JobSnapshot, key string) string {
	for _, values := range []map[string]interface{}{job.Result, job.Payload} {
		if values == nil {
			continue
		}
		if value, ok := values[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func (o *Orchestrator) syncStackKitWalletOutputs(req walletsync.SyncRequest) (int, error) {
	if o.walletStore != nil {
		return walletsync.SyncStackKitOutputsToStore(o.ctx, o.walletStore, req)
	}
	return walletsync.SyncStackKitOutputs(o.app, req)
}

func (o *Orchestrator) ensureWorkerPairingToken(stack *core.Record, result map[string]interface{}) error {
	if stack == nil || !requiresWorkerPairingToken(result) {
		return nil
	}
	token := strings.TrimSpace(stringResult(result["registration_token"]))
	ownerID := strings.TrimSpace(stack.GetString("owner_id"))
	if token == "" || ownerID == "" {
		return nil
	}

	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	existing, err := o.app.FindRecordsByFilter(
		"pairing_tokens",
		"token_hash = {:h}",
		"",
		1,
		0,
		map[string]any{"h": tokenHash},
	)
	if err == nil && len(existing) > 0 {
		if existing[0].GetString("stack_id") == "" {
			existing[0].Set("stack_id", stack.Id)
			_ = o.app.Save(existing[0])
		}
		return nil
	}

	collection, err := o.app.FindCollectionByNameOrId("pairing_tokens")
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	stackName := strings.TrimSpace(stack.GetString("name"))
	if stackName == "" {
		stackName = stack.Id
	}
	record.Set("user", ownerID)
	record.Set("name", stackName+" worker enrollment")
	record.Set("stack_id", stack.Id)
	record.Set("token_hash", tokenHash)
	record.Set("used", false)
	record.Set("expires_at", time.Now().Add(2*time.Hour))
	return o.app.Save(record)
}

func requiresWorkerPairingToken(result map[string]interface{}) bool {
	if result == nil {
		return false
	}
	if boolResult(result[runtimeFieldInstallCommand]) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(stringResult(result[runtimeFieldConnectionMode]))) {
	case "agent-oneliner":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(stringResult(result[runtimeFieldProvisionMode]))) {
	case "install-command":
		return true
	default:
		return false
	}
}

func stringResult(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func boolResult(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
