package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/walletsync"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// enqueueWithSync enqueues a job and sets up progress synchronization.
func (o *Orchestrator) enqueueWithSync(job *jobs.Job, tenantID string) error {
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

	jobID := job.Snapshot().ID

	// Start progress sync goroutine with proper tracking
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.syncJobProgress(jobID, strings.TrimSpace(tenantID))
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
	recoveryPayload := redactedJobPayloadForRecovery(snapshot.Payload)
	if existing, err := o.jobStore.GetJob(o.ctx, tenantID, snapshot.ID); err == nil {
		if validateErr := validateDurablePendingJob(existing, snapshot); validateErr != nil {
			return validateErr
		}
		return o.persistJobRecoveryPayload(o.ctx, tenantID, snapshot.ID, recoveryPayload)
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return fmt.Errorf("load durable pending job: %w", err)
	}
	// Establish the stack projection once before creating a missing durable job.
	// Existing durable jobs already passed that authority boundary, so progress
	// heartbeats never need a second stack-read transaction every 500ms.
	o.ensureControlPlaneStackForJob(o.ctx, snapshot, tenantID)
	request := controlplane.UpsertJobRequest{
		ID: snapshot.ID, TenantID: tenantID, StackID: snapshot.TargetID, Type: string(snapshot.Type),
		State: persistentStatePending, Priority: snapshot.Priority, Progress: snapshot.Progress,
		Step: snapshot.Step, Message: snapshot.Message, Error: snapshot.Error, ErrorDetails: snapshot.ErrorDetails,
		Logs: controlPlaneJobLogs(snapshot.Logs), Result: projectedLegacyJobResult(snapshot),
		Payload: recoveryPayload, ScheduledFor: time.Now().UTC(),
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
	if validateErr := validateDurablePendingJob(existing, snapshot); validateErr != nil {
		return validateErr
	}
	return o.persistJobRecoveryPayload(o.ctx, tenantID, snapshot.ID, recoveryPayload)
}

// persistJobRecoveryPayload writes the redacted handler input to an already
// pending durable row so a restart can re-enqueue it. A conflict (missing or
// non-pending row) is not an error: the execution claim owns the row then.
func (o *Orchestrator) persistJobRecoveryPayload(ctx context.Context, tenantID, jobID string, payload map[string]any) error {
	if len(payload) == 0 {
		return nil
	}
	if err := o.jobStore.SetJobPayload(ctx, tenantID, jobID, payload); err != nil && !errors.Is(err, controlplane.ErrConflict) {
		return fmt.Errorf("persist job recovery payload: %w", err)
	}
	return nil
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

const (
	jobProgressSyncInterval     = 500 * time.Millisecond
	unchangedJobHeartbeatPeriod = time.Second
)

// syncJobProgress periodically syncs job progress to the canonical job store.
func (o *Orchestrator) syncJobProgress(jobID, tenantID string) {
	ticker := time.NewTicker(jobProgressSyncInterval)
	defer ticker.Stop()
	var lastPersisted jobs.JobSnapshot
	var lastPersistedAt time.Time
	hasPersisted := false
	terminalProjectionApplied := false

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
			// Local state becomes running just before the durable compare-and-set.
			// Do not let the periodic projector race that in-flight claim and
			// mistake its expected pending row for ownership by another process.
			if snapshot.ExecutionClaimPending {
				continue
			}

			controlPlaneSynced := true
			if tenantID != "" && o.jobStore != nil {
				// Keep the 500ms observation cadence so state changes still land
				// promptly, but do not rewrite the same JSON payload and logs on
				// every observation. The one-second floor still renews a live job
				// three times within the managed-decommission stale-running grace.
				unchanged := hasPersisted && reflect.DeepEqual(lastPersisted, snapshot)
				if unchanged && time.Since(lastPersistedAt) < unchangedJobHeartbeatPeriod {
					continue
				}
				if err := o.persistControlPlaneJobSnapshotContext(o.ctx, snapshot, tenantID); errors.Is(err, controlplane.ErrConflict) {
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
				} else {
					lastPersisted = snapshot
					lastPersistedAt = time.Now()
					hasPersisted = true
				}
			}

			// Check if job is done. The canonical status projection is applied once;
			// an idempotent terminal observer can then retry independently until its
			// own durable handoff succeeds.
			if controlPlaneSynced && (snapshot.State == jobs.JobStateCompleted || snapshot.State == jobs.JobStateFailed || snapshot.State == jobs.JobStateCancelled) {
				if !terminalProjectionApplied {
					o.updateStackStatusSnapshot(snapshot)
					terminalProjectionApplied = true
				}
				if err := o.observeTerminalJob(o.ctx, snapshot); err != nil {
					o.log.Warn("terminal_job_observer_failed", "job_id", snapshot.ID, "tenant_id", tenantID, "error", err)
					continue
				}
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
	if err := o.observeTerminalJob(ctx, snapshot); err != nil {
		o.log.Error("terminal_job_observer_failed_on_shutdown",
			"job_id", snapshot.ID, "tenant_id", tenantID, "error", err)
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
	return o.persistControlPlaneJobSnapshotContext(ctx, job, tenantID)
}

func (o *Orchestrator) persistControlPlaneJobSnapshotContext(ctx context.Context, job jobs.JobSnapshot, tenantID string) error {
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

func (o *Orchestrator) updateStackStatusSnapshot(job jobs.JobSnapshot) {
	if noWorkspaceDestroyProjectionReconciled(job) {
		// The handler has already archived this exact control-plane projection
		// through the durable reconciliation callback. Do not revive its state
		// while handling the terminal snapshot.
		return
	}
	newStatus := stackStatusForJob(job)
	if newStatus == "" {
		return
	}

	tenantID := firstNonEmptyJobString(job, stackTenantIDField)
	if tenantID == "" || o.effectiveStackStore() == nil {
		o.log.Error("canonical_stack_status_projection_unavailable", "stack_id", job.TargetID, "job_id", job.ID)
		return
	}
	o.updateControlPlaneStackStatus(job, newStatus)
	if job.State != jobs.JobStateCompleted || job.Result == nil {
		return
	}
	if syncErr := o.syncStackKitRuntimeInventoryFromJobSnapshot(tenantID, job); syncErr != nil {
		o.log.Warn("stackkit_inventory_projection_failed", "stack_id", job.TargetID, "error", syncErr)
	}
	if o.walletStore != nil {
		count, syncErr := walletsync.SyncStackKitOutputsToStore(o.ctx, o.walletStore, walletsync.SyncRequest{
			TenantID: tenantID, OwnerID: firstNonEmptyJobString(job, stackOwnerIDField),
			StackID: job.TargetID, StackName: job.TargetName, Result: job.Result,
		})
		if syncErr != nil {
			o.log.Warn("stackkit_wallet_sync_failed", "stack_id", job.TargetID, "error", syncErr)
		} else if count > 0 {
			o.log.Info("stackkit_wallet_sync_completed", "stack_id", job.TargetID, "items", count)
		}
	}
}

func noWorkspaceDestroyProjectionReconciled(job jobs.JobSnapshot) bool {
	return job.Type == jobs.JobTypeDestroy &&
		job.State == jobs.JobStateCompleted &&
		strings.EqualFold(strings.TrimSpace(stringResult(job.Result[jobs.DestroyWorkspaceStateResultField])), jobs.DestroyWorkspaceStateAbsent) &&
		boolResult(job.Result[jobs.DestroyProjectionReconciledResultField])
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

func (o *Orchestrator) updateControlPlaneStackStatus(job jobs.JobSnapshot, newStatus string) {
	store := o.effectiveStackStore()
	if store == nil {
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
	if _, err := store.UpdateStackRuntime(o.ctx, tenantID, job.TargetID, controlplane.RuntimeUpdate{
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
