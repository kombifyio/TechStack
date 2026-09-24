package homeassistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

// MigrationRuntime is bound by the local substrate executor, never by request
// booleans. Done means a fresh native observation proved the postcondition.
// Isolation includes an API-reachable management channel, blocked production
// egress and detached radios. StopSource includes every source app/container.
type MigrationRuntime interface {
	PrepareIsolated(context.Context) (bool, error)
	VerifyIsolation(context.Context) (bool, error)
	StopSource(context.Context) (bool, error)
	StartSource(context.Context) (bool, error)
	StopTarget(context.Context) (bool, error)
	StartTarget(context.Context) (bool, error)
	TransferToTarget(context.Context) (bool, error)
	TransferToSource(context.Context) (bool, error)
	SourceStopped(context.Context) (bool, error)
	TargetStopped(context.Context) (bool, error)
}
type OperationCustody interface {
	Claim(context.Context, string) (bool, map[string]any, error)
	Save(context.Context, string, map[string]any) error
	RecoveryKey(context.Context, string) (string, error)
	ReadRecoveryKey(context.Context, string) (string, error)
}
type ArchiveStore interface {
	Put(context.Context, string, io.Reader) (backupstore.ArchiveReceipt, error)
	Open(context.Context, backupstore.ArchiveReceipt) (io.ReadCloser, error)
	Verify(context.Context, backupstore.ArchiveReceipt) error
}

// MigrationBinding is trusted owner configuration. Tokens remain inside the
// clients; request input selects only these exact, already-authorized IDs.
type MigrationBinding struct {
	ProvisionRequest                                                                      substrateprovision.Request
	SourceBackup                                                                          NativeBackup
	OwnerID, SourceServiceID, TargetBindingRef, AuthorizationRef, DependencyAssessmentRef string
	SourceCore, TargetCore                                                                *Client
	SourceSupervisor, TargetSupervisor                                                    *Supervisor
	Custody                                                                               OperationCustody
	Archives                                                                              ArchiveStore
	Runtime                                                                               MigrationRuntime
}

func (b *MigrationBinding) Activities() map[string]workflow.ActivityFunc {
	return map[string]workflow.ActivityFunc{
		workflows.ActHAAssess:            b.assess,
		workflows.ActHASourceArchive:     b.archive(false),
		workflows.ActHAProvisionIsolated: b.runtimeStep(b.RuntimePrepare, []string{"fresh_haos", "devices_isolated", "egress_isolated", "target_identity_bound"}),
		workflows.ActHARestoreIsolated:   b.restore,
		workflows.ActHAVerifyRestore:     b.verifyRestore,
		workflows.ActHAStopSource:        b.runtimeStep(b.RuntimeStopSource, []string{"stopped", "apps_stopped", "device_control_quiesced", "identity_verified"}),
		workflows.ActHATransferToTarget:  b.runtimeStep(b.RuntimeTransferTarget, []string{"exclusive_device_ownership", "network_assignment_verified", "previous_instance_stopped"}),
		workflows.ActHAStartTarget:       b.runtimeStep(b.RuntimeStartTarget, []string{"target_started"}),
		workflows.ActHAVerifyTarget:      b.verifyActive(false),
		workflows.ActHATargetArchive:     b.archive(true),
		workflows.ActHAStopTarget:        b.runtimeStep(b.RuntimeStopTarget, []string{"stopped", "apps_stopped", "device_control_quiesced", "identity_verified"}),
		workflows.ActHATransferToSource:  b.runtimeStep(b.RuntimeTransferSource, []string{"exclusive_device_ownership", "network_assignment_verified", "previous_instance_stopped"}),
		workflows.ActHAStartSource:       b.runtimeStep(b.RuntimeStartSource, []string{"source_started"}),
		workflows.ActHAVerifySource:      b.verifyActive(true),
	}
}
func (b *MigrationBinding) authorize(input map[string]any) error {
	if b == nil || b.SourceCore == nil || b.TargetCore == nil || b.sourceBackup() == nil || b.TargetSupervisor == nil || b.Custody == nil || b.Archives == nil || b.Runtime == nil || b.DependencyAssessmentRef == "" {
		return errors.New("Home Assistant native migration binding incomplete")
	}
	req, _ := input["request"].(map[string]any)
	if str(input, "owner_id") != b.OwnerID || b.OwnerID == "" || str(req, "source_service_id") != b.SourceServiceID || b.SourceServiceID == "" || str(req, "target_binding_ref") != b.TargetBindingRef || b.TargetBindingRef == "" || str(req, "authorization_ref") != b.AuthorizationRef || b.AuthorizationRef == "" {
		return errors.New("Home Assistant migration binding authorization denied")
	}
	return nil
}
func (b *MigrationBinding) assess(ctx context.Context, input map[string]any) (map[string]any, error) {
	if err := b.authorize(input); err != nil {
		return nil, err
	}
	platform, err := b.sourceBackup().Observe(ctx)
	if err != nil {
		return nil, err
	}
	observation, err := b.SourceCore.Observe(ctx)
	if err != nil {
		return nil, err
	}
	if !b.sourceBackup().BackupAllowed() || !b.TargetSupervisor.grants.Restore || !b.TargetSupervisor.grants.Backup {
		return nil, errors.New("native source backup, target restore and rollback-backup grants required")
	}
	out := proof("source_identity_verified", "native_backup_available", "external_dependencies_accounted", "lifecycle_authorized", "target_binding_verified")
	out["installation_method"] = platform.InstallationMethod
	out["platform_observation"] = platform
	out["core_observation"] = observation
	out["dependency_assessment_ref"] = b.DependencyAssessmentRef
	return out, nil
}

func (b *MigrationBinding) archive(target bool) workflow.ActivityFunc {
	return func(ctx context.Context, input map[string]any) (map[string]any, error) {
		if err := b.authorize(input); err != nil {
			return nil, err
		}
		s := b.sourceBackup()
		if target {
			s = b.restoredSupervisor()
		}
		core := b.SourceCore
		if target {
			core = b.TargetCore
		}
		return createNativeArchive(ctx, workflow.IdempotencyKeyFromContext(ctx), s, core, b.Custody, b.Archives, b.DependencyAssessmentRef)
	}
}

func (b *MigrationBinding) restore(ctx context.Context, input map[string]any) (map[string]any, error) {
	if err := b.authorize(input); err != nil {
		return nil, err
	}
	isolated, err := b.Runtime.VerifyIsolation(ctx)
	if err != nil {
		return nil, err
	}
	if !isolated {
		return nil, errors.New("target isolated management path not verified")
	}
	evidence, _ := input["evidence"].(map[string]any)
	source, _ := evidence[workflows.ActHASourceArchive].(map[string]any)
	archive, err := archiveReceipt(source)
	if err != nil {
		return nil, err
	}
	if err = b.Archives.Verify(ctx, archive); err != nil {
		return nil, err
	}
	password, err := b.Custody.ReadRecoveryKey(ctx, str(source, "recovery_key_ref"))
	if err != nil {
		return nil, err
	}
	key := workflow.IdempotencyKeyFromContext(ctx)
	fresh, receipt, err := b.Custody.Claim(ctx, key+":upload")
	if err != nil {
		return nil, err
	}
	slug := str(receipt, "slug")
	if fresh {
		reader, err := b.Archives.Open(ctx, archive)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		out, err := b.TargetSupervisor.UploadBackup(ctx, reader)
		if err != nil {
			return nil, err
		}
		slug = out.Slug
		if err = b.Custody.Save(ctx, key+":upload", map[string]any{"slug": slug}); err != nil {
			return nil, err
		}
	}
	if slug == "" {
		info, err := b.TargetSupervisor.BackupInfo(ctx, str(source, "slug"))
		if err != nil {
			return nil, errors.New("uncertain backup upload requires reconciliation")
		}
		slug = info.Slug
	}
	fresh, receipt, err = b.Custody.Claim(ctx, key+":restore")
	if err != nil {
		return nil, err
	}
	job := str(receipt, "job_id")
	if fresh {
		out, err := b.TargetSupervisor.SubmitRestore(ctx, slug, password)
		if err != nil {
			return nil, err
		}
		job = out.JobID
		if err = b.Custody.Save(ctx, key+":restore", map[string]any{"job_id": job}); err != nil {
			return nil, err
		}
	}
	if job == "" {
		return nil, errors.New("uncertain native restore must be reconciled; it will not be resubmitted")
	}
	done, err := b.TargetSupervisor.ObserveJob(ctx, job)
	if err != nil && b.TargetSupervisor.viaCore {
		done, err = b.restoredSupervisor().ObserveJob(ctx, job)
	}
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnauthorized) {
			return pending(), nil
		}
		return nil, err
	}
	if !done {
		return pending(), nil
	}
	return proof("restore_completed", "devices_isolated", "egress_isolated", "baseline_skipped"), nil
}

// Restoring HA also restores its native accounts/tokens. The retained source
// read credential is therefore the exact post-restore Core credential; the
// pre-restore target account is never recreated or written into the archive.
func (b *MigrationBinding) restoredSupervisor() *Supervisor {
	if !b.TargetSupervisor.viaCore {
		return b.TargetSupervisor
	}
	s := *b.TargetSupervisor
	c := *s.client
	c.token = b.TargetCore.token
	s.client = &c
	return &s
}

func (b *MigrationBinding) verifyRestore(ctx context.Context, input map[string]any) (map[string]any, error) {
	if err := b.authorize(input); err != nil {
		return nil, err
	}
	isolated, err := b.Runtime.VerifyIsolation(ctx)
	if err != nil || !isolated {
		return nil, errors.New("target restore isolation unverified")
	}
	source, err := b.SourceCore.Observe(ctx)
	if err != nil {
		return nil, err
	}
	target, err := b.TargetCore.Observe(ctx)
	if err != nil {
		return nil, err
	}
	if source.CoreVersion != target.CoreVersion {
		return nil, errors.New("restored Core version differs from source")
	}
	if !containsComponents(target.Components, source.Components) {
		return nil, errors.New("restored integration inventory differs from source")
	}
	evidence, _ := input["evidence"].(map[string]any)
	archive, _ := evidence[workflows.ActHASourceArchive].(map[string]any)
	raw, _ := json.Marshal(archive["entity_ids"])
	var originalIDs []string
	if json.Unmarshal(raw, &originalIDs) != nil || originalIDs == nil {
		return nil, errors.New("source entity identity inventory missing")
	}
	targetIDs, err := b.TargetCore.EntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	if !containsComponents(targetIDs, originalIDs) {
		return nil, errors.New("restored entity identities differ from source backup")
	}
	retained, err := archiveReceipt(archive)
	if err != nil {
		return nil, err
	}
	if err = b.Archives.Verify(ctx, retained); err != nil {
		return nil, err
	}
	if _, err = b.Custody.ReadRecoveryKey(ctx, str(archive, "recovery_key_ref")); err != nil {
		return nil, err
	}
	key := "verified-recovery/" + retained.SHA256
	if _, _, err = b.Custody.Claim(ctx, key); err != nil {
		return nil, err
	}
	if err = b.Custody.Save(ctx, key, map[string]any{"verified": true, "archive_sha256": retained.SHA256, "core_version": target.CoreVersion}); err != nil {
		return nil, err
	}
	return proof("authenticated", "configuration_verified", "identities_preserved", "devices_isolated", "egress_isolated"), nil
}
func (b *MigrationBinding) verifyActive(source bool) workflow.ActivityFunc {
	return func(ctx context.Context, input map[string]any) (map[string]any, error) {
		if err := b.authorize(input); err != nil {
			return nil, err
		}
		c := b.TargetCore
		stopped, err := b.Runtime.SourceStopped(ctx)
		if source {
			c = b.SourceCore
			stopped, err = b.Runtime.TargetStopped(ctx)
		}
		if err != nil || !stopped {
			return nil, errors.New("other Home Assistant instance is not proven stopped")
		}
		if _, err := c.Observe(ctx); err != nil {
			return nil, err
		}
		evidence, _ := input["evidence"].(map[string]any)
		keys := []string{workflows.ActHASourceArchive}
		if source {
			keys = append(keys, workflows.ActHATargetArchive)
		}
		for _, key := range keys {
			receipt, _ := evidence[key].(map[string]any)
			archive, err := archiveReceipt(receipt)
			if err != nil {
				return nil, err
			}
			if err = b.Archives.Verify(ctx, archive); err != nil {
				return nil, err
			}
		}
		return proof("authenticated", "healthy", "original_retained", "original_archive_retained", "target_archive_retained"), nil
	}
}
func (b *MigrationBinding) runtimeStep(fn func(context.Context) (bool, error), keys []string) workflow.ActivityFunc {
	return func(ctx context.Context, input map[string]any) (map[string]any, error) {
		if err := b.authorize(input); err != nil {
			return nil, err
		}
		done, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		if !done {
			return pending(), nil
		}
		return proof(keys...), nil
	}
}
func (b *MigrationBinding) RuntimePrepare(ctx context.Context) (bool, error) {
	return b.Runtime.PrepareIsolated(ctx)
}
func (b *MigrationBinding) RuntimeStopSource(ctx context.Context) (bool, error) {
	return b.Runtime.StopSource(ctx)
}
func (b *MigrationBinding) RuntimeStopTarget(ctx context.Context) (bool, error) {
	return b.Runtime.StopTarget(ctx)
}
func (b *MigrationBinding) RuntimeStartSource(ctx context.Context) (bool, error) {
	ok, err := b.Runtime.TargetStopped(ctx)
	if err != nil || !ok {
		return false, errors.New("target must be stopped before original activation")
	}
	return b.Runtime.StartSource(ctx)
}
func (b *MigrationBinding) RuntimeStartTarget(ctx context.Context) (bool, error) {
	ok, err := b.Runtime.SourceStopped(ctx)
	if err != nil || !ok {
		return false, errors.New("source must be stopped before target activation")
	}
	return b.Runtime.StartTarget(ctx)
}
func (b *MigrationBinding) RuntimeTransferTarget(ctx context.Context) (bool, error) {
	ok, err := b.Runtime.SourceStopped(ctx)
	if err != nil || !ok {
		return false, errors.New("source must be stopped before attachment transfer")
	}
	return b.Runtime.TransferToTarget(ctx)
}
func (b *MigrationBinding) RuntimeTransferSource(ctx context.Context) (bool, error) {
	ok, err := b.Runtime.TargetStopped(ctx)
	if err != nil || !ok {
		return false, errors.New("target must be stopped before attachment return")
	}
	return b.Runtime.TransferToSource(ctx)
}
func proof(keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		out[k] = true
	}
	return out
}
func pending() map[string]any                 { return map[string]any{"pending": true} }
func str(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func operationName(key string) string {
	hash := sha256.Sum256([]byte(key))
	return "kombify-migration-" + hex.EncodeToString(hash[:])
}
func archiveReceipt(m map[string]any) (backupstore.ArchiveReceipt, error) {
	var size int64
	switch v := m["archive_bytes"].(type) {
	case int64:
		size = v
	case float64:
		size = int64(v)
	}
	r := backupstore.ArchiveReceipt{ObjectKey: str(m, "archive_ref"), SHA256: str(m, "archive_sha256"), Bytes: size}
	if r.ObjectKey == "" || len(r.SHA256) != 64 || size <= 0 {
		return r, errors.New("retained source archive receipt missing")
	}
	return r, nil
}
func containsComponents(actual, required []string) bool {
	available := map[string]bool{}
	for _, value := range actual {
		available[value] = true
	}
	for _, value := range required {
		if !available[value] {
			return false
		}
	}
	return true
}
