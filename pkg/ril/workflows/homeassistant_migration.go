package workflows

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

const (
	TypeHomeAssistantMigration workflow.RunType = "home_assistant_migration"
	TypeHomeAssistantRollback  workflow.RunType = "home_assistant_rollback"
	ActHAAssess                                 = "home_assistant_assess"
	ActHASourceArchive                          = "home_assistant_source_archive"
	ActHAProvisionIsolated                      = "home_assistant_provision_isolated"
	ActHARestoreIsolated                        = "home_assistant_restore_isolated"
	ActHAVerifyRestore                          = "home_assistant_verify_restore"
	ActHAStopSource                             = "home_assistant_stop_source"
	ActHATransferToTarget                       = "home_assistant_transfer_to_target"
	ActHAStartTarget                            = "home_assistant_start_target"
	ActHAVerifyTarget                           = "home_assistant_verify_target"
	ActHATargetArchive                          = "home_assistant_target_archive"
	ActHAStopTarget                             = "home_assistant_stop_target"
	ActHATransferToSource                       = "home_assistant_transfer_to_source"
	ActHAStartSource                            = "home_assistant_start_source"
	ActHAVerifySource                           = "home_assistant_verify_source"
)

// RegisterHomeAssistantWorkflows requires a complete native execution binding.
// Missing executors must remain a capability gap; definitions alone must not
// make a migration startable. Each mutating activity must durably claim its
// idempotency key before dispatch and reconcile uncertain native submissions.
func RegisterHomeAssistantWorkflows(engine *workflow.Engine, activities map[string]workflow.ActivityFunc) error {
	if engine == nil {
		return errors.New("Home Assistant workflow engine unavailable")
	}
	for _, name := range []string{ActHAAssess, ActHASourceArchive, ActHAProvisionIsolated, ActHARestoreIsolated, ActHAVerifyRestore, ActHAStopSource, ActHATransferToTarget, ActHAStartTarget, ActHAVerifyTarget, ActHATargetArchive, ActHAStopTarget, ActHATransferToSource, ActHAStartSource, ActHAVerifySource} {
		if activities[name] == nil {
			return fmt.Errorf("Home Assistant native activity unavailable: %s", name)
		}
	}
	for name, fn := range activities {
		engine.RegisterActivity(name, fn)
	}
	engine.Register(NewHomeAssistantMigrationWorkflow())
	engine.Register(NewHomeAssistantRollbackWorkflow())
	return nil
}

type HomeAssistantWorkflow struct{ rollback bool }

func NewHomeAssistantMigrationWorkflow() *HomeAssistantWorkflow { return &HomeAssistantWorkflow{} }
func NewHomeAssistantRollbackWorkflow() *HomeAssistantWorkflow {
	return &HomeAssistantWorkflow{rollback: true}
}
func (w *HomeAssistantWorkflow) Type() workflow.RunType {
	if w.rollback {
		return TypeHomeAssistantRollback
	}
	return TypeHomeAssistantMigration
}

// Native backup/restore endpoints do not promise idempotence. Retries belong to
// the durable dispatch/observe adapter, never blind engine re-submission.
func (w *HomeAssistantWorkflow) RetryPolicy() workflow.RetryPolicy {
	return workflow.RetryPolicy{MaxAttempts: 1}
}
func (w *HomeAssistantWorkflow) Steps() []workflow.StepDef {
	if w.rollback {
		return []workflow.StepDef{
			{Name: "confirm-return", Run: w.confirm},
			{Name: "archive-current-target", Run: haEvidenceStep(ActHATargetArchive, haArchiveEvidence)},
			{Name: "stop-target", Run: haEvidenceStep(ActHAStopTarget, haStoppedEvidence)},
			{Name: "return-exclusive-attachments", Run: haEvidenceStep(ActHATransferToSource, haExclusiveEvidence)},
			{Name: "start-original", Run: haEvidenceStep(ActHAStartSource, []string{"source_started"})},
			{Name: "verify-original", Run: haEvidenceStep(ActHAVerifySource, []string{"authenticated", "healthy", "original_archive_retained", "target_archive_retained"})},
		}
	}
	return []workflow.StepDef{
		{Name: "assess-source", Run: w.assess},
		{Name: "archive-source", Run: haEvidenceStep(ActHASourceArchive, haArchiveEvidence)},
		{Name: "provision-isolated-target", Run: haEvidenceStep(ActHAProvisionIsolated, []string{"fresh_haos", "devices_isolated", "egress_isolated", "target_identity_bound"})},
		{Name: "restore-isolated-target", Run: haEvidenceStep(ActHARestoreIsolated, []string{"restore_completed", "devices_isolated", "egress_isolated", "baseline_skipped"})},
		{Name: "verify-restored-configuration", Run: haEvidenceStep(ActHAVerifyRestore, []string{"authenticated", "configuration_verified", "identities_preserved", "devices_isolated", "egress_isolated"})},
		{Name: "confirm-cutover", Run: w.confirm},
		{Name: "stop-source", Run: haEvidenceStep(ActHAStopSource, haStoppedEvidence)},
		{Name: "transfer-exclusive-attachments", Run: haEvidenceStep(ActHATransferToTarget, haExclusiveEvidence)},
		{Name: "activate-target", Run: haEvidenceStep(ActHAStartTarget, []string{"target_started"})},
		{Name: "verify-active-target", Run: haEvidenceStep(ActHAVerifyTarget, []string{"authenticated", "healthy", "original_retained", "original_archive_retained"})},
	}
}

var haArchiveEvidence = []string{"native_full_backup", "encrypted", "off_instance", "immutable", "integrity_verified", "key_separate_custody", "configuration_inventory_retained", "versions_retained", "external_dependencies_accounted"}
var haStoppedEvidence = []string{"stopped", "apps_stopped", "device_control_quiesced", "identity_verified"}
var haExclusiveEvidence = []string{"exclusive_device_ownership", "network_assignment_verified", "previous_instance_stopped"}

// Each step forwards prior server-produced evidence through durable context.
// Request input is never accepted as successful evidence. Archive/key secrets
// belong in custody; only opaque references may cross this workflow boundary.
func haEvidenceStep(activity string, required []string) workflow.StepFunc {
	return func(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
		if rc == nil || rc.Run == nil || rc.Activities == nil {
			return workflow.StepResult{}, errors.New("Home Assistant workflow context unavailable")
		}
		startedKey := activity + ":started_at"
		started := contextString(rc, startedKey)
		if started == "" {
			started = time.Now().UTC().Format(time.RFC3339Nano)
			setContext(rc, startedKey, started)
		}
		at, err := time.Parse(time.RFC3339Nano, started)
		if err != nil || time.Since(at) > 15*time.Minute {
			return workflow.StepResult{}, errors.New("Home Assistant native phase exceeded its observation window; retained state requires inspection")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute-time.Since(at))
		defer cancel()
		out, err := rc.Activities.Run(ctx, activity, map[string]any{"run_id": rc.Run.RunID, "owner_id": rc.Run.OwnerID, "request": rc.Run.Input, "evidence": rc.Run.Context}, idemKey(rc, activity))
		if err != nil {
			return workflow.StepResult{}, err
		}
		if mapBool(out, "pending") {
			return workflow.StepResult{Output: out, Suspend: &workflow.SuspendDirective{SignalKey: "ha-observe:" + rc.Run.RunID + ":" + activity, Timeout: 5 * time.Second, TimerKind: workflow.TimerRetry}}, nil
		}
		for _, key := range required {
			if !mapBool(out, key) {
				return workflow.StepResult{}, fmt.Errorf("Home Assistant %s did not prove %s", activity, key)
			}
		}
		// Enforce the durable archive receipt before progressing toward restore.
		if activity == ActHASourceArchive || activity == ActHATargetArchive {
			for _, key := range []string{"archive_ref", "archive_sha256", "recovery_key_ref"} {
				if mapString(out, key) == "" {
					return workflow.StepResult{}, fmt.Errorf("Home Assistant archive lacks %s", key)
				}
			}
			if mapString(out, "archive_ref") == mapString(out, "recovery_key_ref") {
				return workflow.StepResult{}, errors.New("Home Assistant archive and recovery key custody must be separate")
			}
		}
		setContext(rc, activity, out)
		return workflow.StepResult{Output: out}, nil
	}
}
func (w *HomeAssistantWorkflow) assess(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if inputString(rc, "source_service_id") == "" || inputString(rc, "target_binding_ref") == "" || inputString(rc, "authorization_ref") == "" {
		return workflow.StepResult{}, errors.New("Home Assistant migration requires source, exact target binding and explicit authorization")
	}
	out, err := haEvidenceStep(ActHAAssess, []string{"source_identity_verified", "native_backup_available", "external_dependencies_accounted", "lifecycle_authorized", "target_binding_verified"})(ctx, rc)
	if err != nil {
		return out, err
	}
	method := mapString(out.Output, "installation_method")
	if method != "haos" && method != "container" {
		return workflow.StepResult{}, errors.New("unsupported Home Assistant source installation")
	}
	return out, nil
}
func (w *HomeAssistantWorkflow) confirm(_ context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if rc == nil || rc.Run == nil {
		return workflow.StepResult{}, errors.New("Home Assistant workflow context unavailable")
	}
	if w.rollback && (inputString(rc, "migration_run_id") == "" || inputString(rc, "authorization_ref") == "") {
		return workflow.StepResult{}, errors.New("rollback requires an owned migration and explicit authorization")
	}
	if rc.Signal == nil {
		return workflow.StepResult{Suspend: &workflow.SuspendDirective{SignalKey: HomeAssistantConfirmationKey(rc.Run.RunID), Timeout: 24 * time.Hour, TimerKind: workflow.TimerEscalation}}, nil
	}
	if rc.Signal.TimedOut || rc.Signal.Key != HomeAssistantConfirmationKey(rc.Run.RunID) || !mapBool(rc.Signal.Payload, "confirmed") {
		return workflow.StepResult{}, errors.New("Home Assistant transition not explicitly confirmed")
	}
	return workflow.StepResult{Output: map[string]any{"confirmed": true}}, nil
}
func HomeAssistantConfirmationKey(runID string) string { return "home_assistant_confirm:" + runID }
