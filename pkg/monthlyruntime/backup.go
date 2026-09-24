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
	// FeatureBackupStorageStarter grants the starter storage budget. It is
	// also the budget an account with no storage key resolves to: an
	// unrecognised account must never inherit a paid tier's budget.
	FeatureBackupStorageStarter = "techstack.backup.storage.starter"
	// FeatureBackupStoragePro grants the pro storage budget. Grandfathered
	// pro_plus accounts carry this key, matching their PLAN_LIMITS row.
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

	// Storage budgets as stored bytes at the object store (decision record
	// 2026-09-18 D3). These defaults are provisional: PLAN_LIMITS in
	// kombify-Cloud is the one register of published quotas and must carry
	// these numbers over the entitlement chain, retiring the constants and
	// the env overrides below (platform-qhb2o.2). Until that lands they are
	// deliberately at or above the published figures, never below, so no
	// account is cut off by a number it was never shown.
	defaultBackupQuotaStarterBytes = int64(50) << 30
	defaultBackupQuotaProBytes     = int64(250) << 30
	defaultBackupQuotaAynBytes     = int64(500) << 30
)

// backupStorageTiers is the descending storage-budget ladder. The resolver
// grants the largest tier the account actually holds; the last entry is also
// the floor for an account holding no storage key at all.
var backupStorageTiers = []struct {
	featureKey string
	envKey     string
	fallback   int64
}{
	{FeatureBackupStorageAyn, "TECHSTACK_BACKUP_QUOTA_AYN_GB", defaultBackupQuotaAynBytes},
	{FeatureBackupStoragePro, "TECHSTACK_BACKUP_QUOTA_PRO_GB", defaultBackupQuotaProBytes},
	{FeatureBackupStorageStarter, "TECHSTACK_BACKUP_QUOTA_STARTER_GB", defaultBackupQuotaStarterBytes},
}

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
	// GrantedStorageFeature is the storage key the budget came from. A quota
	// denial reports this key, so the envelope names the tier the caller is
	// actually on instead of always naming all-you-need.
	GrantedStorageFeature string
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
// carries the storage budget of the largest tier the account actually holds
// (ayn > pro > starter), and an account holding no storage key falls to the
// starter budget rather than inheriting a paid tier's.
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
	quota, grantedKey := resolveBackupQuota(ctx, checker, userID)
	return BackupEntitlementDecision{
		QuotaBytes:            quota,
		GrantedStorageFeature: grantedKey,
		RequiredFeatures:      required,
	}
}

// resolveBackupQuota grants the largest storage tier the account holds and
// reports which key it came from. Walking the ladder matters: checking only
// the top key and falling through to a paid default hands every account below
// it - starter, and any account with no storage key at all - a budget it was
// never granted.
func resolveBackupQuota(ctx context.Context, checker BackupFeatureChecker, userID string) (int64, string) {
	for _, tier := range backupStorageTiers {
		enabled, err := checker.IsEnabled(ctx, tier.featureKey, userID)
		if err == nil && enabled {
			return backupQuotaFromEnv(tier.envKey, tier.fallback), tier.featureKey
		}
	}
	floor := backupStorageTiers[len(backupStorageTiers)-1]
	return backupQuotaFromEnv(floor.envKey, floor.fallback), floor.featureKey
}

// nextBackupStorageTier names the key above the granted one, or "" when the
// account already holds the largest tier. Denial guidance may only offer an
// upgrade that exists.
func nextBackupStorageTier(grantedKey string) string {
	for index := len(backupStorageTiers) - 1; index > 0; index-- {
		if backupStorageTiers[index].featureKey == grantedKey {
			return backupStorageTiers[index-1].featureKey
		}
	}
	return ""
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
	return structuredFailureDetails(failureEnvelope{
		phase: "backup_entitlement", phaseLabel: "Backup availability",
		errorCode: BackupEntitlementErrorCode, reasonCode: reasonCode,
		capability: BackupCapability, requiredFeatures: required, missingFeatures: missing,
		guidance: map[string]any{
			"title": "Backups are not active for this account",
			"body":  "This account's plan does not include the requested backup scope. Configuration backups need a starter plan; content backups (your photos and app data) need pro or higher.",
			"next_steps": []string{
				"Upgrade your plan to enable backups for this stack.",
				"Ask a workspace admin or kombify support to enable the backup entitlement.",
			},
		},
	}, map[string]any{
		"remediation": "Enable the required backup entitlement for this account, or reduce the requested backup scope.",
	})
}

// BackupQuotaExceededDetails builds the structured envelope returned when the
// stored repository size exceeds the granted storage budget. Not retryable
// until the account frees space or upgrades.
// BackupQuotaExceededDetails builds the storage-budget denial. grantedFeature
// is the storage key the account actually holds, from
// BackupEntitlementDecision.GrantedStorageFeature: reporting a fixed key would
// tell a starter account it is missing the all-you-need entitlement, which is
// both wrong and unactionable.
func BackupQuotaExceededDetails(quotaBytes, usedBytes int64, grantedFeature string) map[string]any {
	if strings.TrimSpace(grantedFeature) == "" {
		grantedFeature = FeatureBackupStorageStarter
	}
	upgrade := nextBackupStorageTier(grantedFeature)
	// Only offer steps the product exposes. There is no customer-facing verb
	// that deletes individual snapshots - retention is policy-driven - so
	// suggesting one sends the customer looking for a control that does not
	// exist.
	nextSteps := []string{}
	missing := []string{}
	if upgrade != "" {
		nextSteps = append(nextSteps, "Upgrade to a plan with a larger backup storage budget.")
		missing = append(missing, upgrade)
	} else {
		nextSteps = append(nextSteps, "Contact support to review this stack's backup scope and retention.")
	}
	return structuredFailureDetails(failureEnvelope{
		phase: "backup_quota", phaseLabel: "Backup storage budget",
		errorCode: BackupQuotaExceededErrorCode, reasonCode: ReasonQuotaExceeded,
		capability:       BackupCapability,
		requiredFeatures: []string{grantedFeature}, missingFeatures: missing,
		guidance: map[string]any{
			"title":      "Backup storage budget reached",
			"body":       fmt.Sprintf("This stack's backups use %s of the %s included in your plan. New backup runs are paused until space is freed or the plan is upgraded.", formatBytes(usedBytes), formatBytes(quotaBytes)),
			"next_steps": nextSteps,
		},
	}, map[string]any{
		"quota_bytes": quotaBytes,
		"used_bytes":  usedBytes,
	})
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
