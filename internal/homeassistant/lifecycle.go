package homeassistant

import (
	"context"
	"errors"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"regexp"
)

var pinnedCoreVersion = regexp.MustCompile(`^[0-9]{4}\.[0-9]{1,2}\.[0-9]+$`)
var pinnedOSVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)

// Restore validates a retained backup on a separately authorized isolated target.
// It never replaces the production source or applies a fresh-install baseline.
func (b *InstanceBinding) Restore(ctx context.Context, operationID, backupOperationID string, recovery *MigrationBinding) (map[string]any, error) {
	if !b.lifecycleReady() || operationID == "" || backupOperationID == "" || recovery == nil || recovery.Runtime == nil || recovery.SourceCore == nil || recovery.TargetCore == nil || recovery.TargetCore.Endpoint() == b.Core.Endpoint() || recovery.SourceCore.Endpoint() != b.Core.Endpoint() || !b.Supervisor.grants.Restore {
		return nil, ErrUnauthorized
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, backup, err := b.Custody.Claim(ctx, b.backupKey(backupOperationID))
	if err != nil {
		return nil, err
	}
	if backup["complete"] != true {
		return nil, errors.New("completed retained backup required")
	}
	key := "instance-restore/" + operationName(b.BindingRef+"/"+operationID)
	fresh, intent, err := b.Custody.Claim(ctx, key)
	if err != nil {
		return nil, err
	}
	if !fresh && (str(intent, "backup_operation_id") != backupOperationID || str(intent, "target_binding_ref") != recovery.TargetBindingRef) {
		return nil, ErrUnauthorized
	}
	if fresh {
		if err = b.Custody.Save(ctx, key, map[string]any{"backup_operation_id": backupOperationID, "target_binding_ref": recovery.TargetBindingRef}); err != nil {
			return nil, err
		}
	}
	done, err := recovery.RuntimePrepare(ctx)
	if err != nil {
		return nil, err
	}
	if !done {
		return pending(), nil
	}
	input := map[string]any{"owner_id": recovery.OwnerID, "request": map[string]any{"source_service_id": recovery.SourceServiceID, "target_binding_ref": recovery.TargetBindingRef, "authorization_ref": recovery.AuthorizationRef}, "evidence": map[string]any{workflows.ActHASourceArchive: backup}}
	// Use the source archive custody, keeping its key outside native backup bytes.
	bound := *recovery
	bound.Custody, bound.Archives = b.Custody, b.Archives
	out, err := bound.restore(workflow.WithIdempotencyKey(ctx, key), input)
	if err != nil || out["pending"] == true {
		return out, err
	}
	out, err = bound.verifyRestore(ctx, input)
	if err == nil {
		out["complete"] = true
	}
	return out, err
}

func (b *InstanceBinding) lifecycleReady() bool {
	return b != nil && b.BindingRef != "" && b.ManagementScope == "managed" && b.Origin != "existing" && b.Core != nil && b.Supervisor != nil && b.Custody != nil && b.Archives != nil && b.DependencyAssessmentRef != ""
}

func (b *InstanceBinding) backupKey(operationID string) string {
	return "instance-backup/" + operationName(b.BindingRef+"/"+operationID)
}

// Backup is resumable native full backup plus encrypted off-instance readback.
// The caller repeats the same operation ID while pending; no native POST replay.
func (b *InstanceBinding) Backup(ctx context.Context, operationID string) (map[string]any, error) {
	if !b.lifecycleReady() || operationID == "" || !b.Supervisor.BackupAllowed() {
		return nil, ErrUnauthorized
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return createNativeArchive(ctx, b.backupKey(operationID), b.Supervisor, b.Core, b.Custody, b.Archives, b.DependencyAssessmentRef)
}

// Update requires this exact encrypted archive to have passed a native isolated
// restore. Recovery proof is produced by verifyRestore, never a request flag.
// The durable update intent is not resubmitted after an uncertain response.
func (b *InstanceBinding) Update(ctx context.Context, operationID, version, backupOperationID string) (map[string]any, error) {
	return b.UpdateTarget(ctx, operationID, "core", version, backupOperationID)
}

func (b *InstanceBinding) UpdateTarget(ctx context.Context, operationID, target, version, backupOperationID string) (map[string]any, error) {
	if target == "" {
		target = "core"
	}
	validVersion := target == "core" && pinnedCoreVersion.MatchString(version) || target == "os" && pinnedOSVersion.MatchString(version)
	if !b.lifecycleReady() || !b.Supervisor.grants.Update || operationID == "" || backupOperationID == "" || !validVersion {
		return nil, ErrUnauthorized
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, backup, err := b.Custody.Claim(ctx, b.backupKey(backupOperationID))
	if err != nil {
		return nil, err
	}
	if backup["complete"] != true {
		return nil, errors.New("completed off-instance backup required")
	}
	archive, err := archiveReceipt(backup)
	if err != nil {
		return nil, err
	}
	if err = b.Archives.Verify(ctx, archive); err != nil {
		return nil, err
	}
	if _, err = b.Custody.ReadRecoveryKey(ctx, b.backupKey(backupOperationID)); err != nil {
		return nil, err
	}
	_, recovery, err := b.Custody.Claim(ctx, "verified-recovery/"+archive.SHA256)
	if err != nil {
		return nil, err
	}
	if recovery["verified"] != true || str(recovery, "archive_sha256") != archive.SHA256 {
		return nil, errors.New("exact archive isolated recovery proof required")
	}
	observed, err := b.Core.Observe(ctx)
	if err != nil {
		return nil, err
	}
	observedVersion := observed.CoreVersion
	if target == "os" {
		platform, e := b.Supervisor.Observe(ctx)
		if e != nil {
			return nil, e
		}
		observedVersion = platform.OSVersion
	}
	key := "instance-update/" + operationName(b.BindingRef+"/"+operationID)
	fresh, intent, err := b.Custody.Claim(ctx, key)
	if err != nil {
		return nil, err
	}
	if !fresh {
		retainedTarget := str(intent, "target")
		if retainedTarget == "" {
			retainedTarget = "core"
		}
		if retainedTarget != target || str(intent, "version") != version || str(intent, "archive_sha256") != archive.SHA256 {
			return nil, ErrUnauthorized
		}
		if observedVersion == version {
			return map[string]any{"complete": true, "target": target, target + "_version": version, "archive_sha256": archive.SHA256}, nil
		}
		if target == "os" && intent["submitted"] == true {
			return map[string]any{"pending": true, "reboot_required": true, "target": "os", "version": version}, nil
		}
		return nil, errors.New("update dispatch outcome uncertain; observe native operation before retry")
	}
	if observed.CoreVersion != str(recovery, "core_version") {
		return nil, errors.New("recovery proof does not match current Core version")
	}
	intent = map[string]any{"target": target, "version": version, "archive_sha256": archive.SHA256}
	if err = b.Custody.Save(ctx, key, intent); err != nil {
		return nil, err
	}
	if target == "os" {
		err = b.Supervisor.SubmitOSUpdate(ctx, version)
	} else {
		err = b.Supervisor.SubmitCoreUpdate(ctx, version)
	}
	if err != nil {
		return nil, err
	}
	if target == "os" {
		intent["submitted"] = true
		if err = b.Custody.Save(ctx, key, intent); err != nil {
			return nil, err
		}
		return map[string]any{"pending": true, "reboot_required": true, "target": "os", "version": version}, nil
	}
	return pending(), nil
}
