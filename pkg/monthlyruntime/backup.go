package monthlyruntime

// Backup product entitlements (photo-vault lifecycle plan, phase 3).
// Feature keys ride the same Stripe/FGA/Flagship entitlement chain as the
// managed-runtime keys and resolve at the edge as signed flags; unknown keys
// evaluate to false, so the gate is fail-closed by construction. Enforcement
// happens BEFORE any job enqueue, provider call, or storage provisioning
// (FEATURE-ENTITLEMENT-UX-STANDARD).

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// FeatureBackupConfig gates configuration-class backups (starter+).
	FeatureBackupConfig = "techstack.backup.config"
	// FeatureBackupContent gates content-class backups (pro+).
	FeatureBackupContent = "techstack.backup.content"
	// FeatureBackupStoragePro grants the pro storage budget.
	FeatureBackupStoragePro = "techstack.backup.storage.pro"
	// FeatureBackupStorageAyn grants the all-you-need storage budget.
	FeatureBackupStorageAyn = "techstack.backup.storage.ayn"

	// BackupCapability names the capability in denial envelopes.
	BackupCapability = "techstack.backup"

	// BackupEntitlementErrorCode marks a backup request denied by the
	// entitlement gate.
	BackupEntitlementErrorCode = "backup_entitlement_denied"
	// BackupQuotaExceededErrorCode marks a backup denied because the stored
	// repository size exceeds the account's storage budget.
	BackupQuotaExceededErrorCode = "backup_quota_exceeded"
	// ReasonQuotaExceeded is the reason_code for storage budget denials.
	ReasonQuotaExceeded = "storage_quota_exceeded"

	// Storage budgets (release decision 2026-07-08): 250 GB pro, 1 TB ayn.
	defaultBackupQuotaProBytes = int64(250) << 30
	defaultBackupQuotaAynBytes = int64(1) << 40
)

// BackupFeatureChecker matches the feature service surface the route layer
// already injects for managed-runtime gates.
type BackupFeatureChecker interface {
	IsEnabled(ctx context.Context, featureKey string, userID string) (bool, error)
}

// BackupEntitlementDecision reports the gate outcome. Deny reasons reuse the
// managed-runtime entitlement reason codes so the frontend parses one shape.
type BackupEntitlementDecision struct {
	Denied           bool
	ReasonCode       string
	RequiredFeatures []string
	MissingFeatures  []string
	// QuotaBytes is the granted storage budget (largest granted tier),
	// valid when the decision is not denied.
	QuotaBytes int64
}

// RequiredFeatureKeysForBackup lists the keys a backup request must hold.
// Content-class backups require the content feature on top of config.
func RequiredFeatureKeysForBackup(includeContent bool) []string {
	keys := []string{FeatureBackupConfig}
	if includeContent {
		keys = append(keys, FeatureBackupContent)
	}
	return keys
}

// EvaluateBackupEntitlement is the fail-closed gate: a missing checker, a
// check error, or a disabled feature all deny. On success the decision
// carries the granted storage budget (ayn > pro > config-only default of the
// pro budget, so config-only accounts are still bounded).
func EvaluateBackupEntitlement(ctx context.Context, checker BackupFeatureChecker, userID string, includeContent bool) BackupEntitlementDecision {
	required := RequiredFeatureKeysForBackup(includeContent)
	if checker == nil {
		return BackupEntitlementDecision{
			Denied:           true,
			ReasonCode:       EntitlementReasonCheckerUnavailable,
			RequiredFeatures: required,
		}
	}
	for _, featureKey := range required {
		enabled, err := checker.IsEnabled(ctx, featureKey, userID)
		if err != nil {
			return BackupEntitlementDecision{
				Denied:           true,
				ReasonCode:       EntitlementReasonFeatureCheckFailed,
				RequiredFeatures: required,
				MissingFeatures:  []string{featureKey},
			}
		}
		if !enabled {
			return BackupEntitlementDecision{
				Denied:           true,
				ReasonCode:       EntitlementReasonFeatureDisabled,
				RequiredFeatures: required,
				MissingFeatures:  []string{featureKey},
			}
		}
	}
	return BackupEntitlementDecision{
		QuotaBytes:       resolveBackupQuota(ctx, checker, userID),
		RequiredFeatures: required,
	}
}

func resolveBackupQuota(ctx context.Context, checker BackupFeatureChecker, userID string) int64 {
	if enabled, err := checker.IsEnabled(ctx, FeatureBackupStorageAyn, userID); err == nil && enabled {
		return backupQuotaFromEnv("TECHSTACK_BACKUP_QUOTA_AYN_GB", defaultBackupQuotaAynBytes)
	}
	return backupQuotaFromEnv("TECHSTACK_BACKUP_QUOTA_PRO_GB", defaultBackupQuotaProBytes)
}

func backupQuotaFromEnv(envKey string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return fallback
	}
	gb, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || gb <= 0 {
		return fallback
	}
	return gb << 30
}

// backupFailureDetails builds the canonical structured envelope for backup
// denials — the backup-scoped sibling of managedRuntimeFailureDetails, so
// entitlement and quota denials parse as the exact same frontend shape.
func backupFailureDetails(phase, phaseLabel, errorCode, reasonCode string, required, missing []string, guidance, extra map[string]any) map[string]any {
	details := map[string]any{
		"phase":             phase,
		"phase_label":       phaseLabel,
		"error_code":        errorCode,
		"reason_code":       reasonCode,
		"capability":        BackupCapability,
		"required_features": required,
		"missing_features":  missing,
		"retryable":         false,
		"user_guidance":     guidance,
		"support_context": map[string]any{
			"feature_source": "Stripe/FGA/Flagship entitlement chain",
			"cost_bearing":   true,
		},
	}
	for k, v := range extra {
		details[k] = v
	}
	return details
}

// BackupEntitlementDenialDetails builds the structured 403 envelope for the
// backup entitlement gate, mirroring ManagedRuntimeEntitlementDenialDetails.
func (d BackupEntitlementDecision) BackupEntitlementDenialDetails() map[string]any {
	reasonCode := d.ReasonCode
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = EntitlementReasonFeatureDisabled
	}
	missing := compactStringSlice(d.MissingFeatures)
	required := compactStringSlice(d.RequiredFeatures)
	if len(missing) == 0 && reasonCode != EntitlementReasonCheckerUnavailable {
		missing = required
	}
	return backupFailureDetails(
		"backup_entitlement",
		"Backup availability",
		BackupEntitlementErrorCode,
		reasonCode,
		required,
		missing,
		map[string]any{
			"title": "Backups are not active for this account",
			"body":  "This account's plan does not include the requested backup scope. Configuration backups need a starter plan; content backups (your photos and app data) need pro or higher.",
			"next_steps": []string{
				"Upgrade your plan to enable backups for this stack.",
				"Ask a workspace admin or kombify support to enable the backup entitlement.",
			},
		},
		map[string]any{
			"remediation": "Enable the required backup entitlement for this account, or reduce the requested backup scope.",
		},
	)
}

// BackupQuotaExceededDetails builds the structured envelope returned when the
// stored repository size exceeds the granted storage budget. Not retryable
// until the account frees space or upgrades.
func BackupQuotaExceededDetails(quotaBytes, usedBytes int64) map[string]any {
	return backupFailureDetails(
		"backup_quota",
		"Backup storage budget",
		BackupQuotaExceededErrorCode,
		ReasonQuotaExceeded,
		[]string{FeatureBackupStorageAyn},
		[]string{FeatureBackupStorageAyn},
		map[string]any{
			"title": "Backup storage budget reached",
			"body":  fmt.Sprintf("This stack's backups use %s of the %s included in your plan. New backup runs are paused until space is freed or the plan is upgraded.", formatBytes(usedBytes), formatBytes(quotaBytes)),
			"next_steps": []string{
				"Delete old snapshots to free backup storage.",
				"Upgrade to a plan with a larger backup storage budget.",
			},
		},
		map[string]any{
			"quota_bytes": quotaBytes,
			"used_bytes":  usedBytes,
		},
	)
}

func formatBytes(value int64) string {
	switch {
	case value >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(value)/float64(1<<40))
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(value)/float64(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
