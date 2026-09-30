package monthlyruntime

// Backup product entitlements (photo-vault lifecycle plan, phase 3).
// Feature keys ride the same Stripe/FGA/Flagship entitlement chain as the
// managed-runtime keys and resolve at the edge as signed flags; unknown keys
// evaluate to false, so the gate is fail-closed by construction. Enforcement
// happens BEFORE any job enqueue, provider call, or storage provisioning
// (FEATURE-ENTITLEMENT-UX-STANDARD).

import (
	"context"
	"strings"
)

const (
	// FeatureBackupConfig gates configuration-class backups (starter+).
	FeatureBackupConfig = "techstack.backup.config"
	// FeatureBackupContent gates content-class backups (pro+).
	FeatureBackupContent = "techstack.backup.content"

	// BackupCapability names the capability in denial envelopes.
	BackupCapability = "techstack.backup"

	// BackupEntitlementErrorCode marks a backup request denied by the
	// entitlement gate.
	BackupEntitlementErrorCode = "backup_entitlement_denied"

	// BackupSizingCeilingBytes is an internal capacity-planning figure, never a
	// customer limit. Backups are limited by retention (how many daily, weekly
	// and monthly snapshots a plan keeps), not by gigabytes
	// (BILLING-ENTITLEMENT-STANDARD §2.4, owner decision 2026-09-28); no
	// admission path may deny a backup against it. It survives only as the
	// quotaBytes basis of the signed restore-drill renewal receipt
	// (internal/advancedissuer.BackupRenewal), whose contract still requires a
	// positive ceiling above the measured usage.
	BackupSizingCeilingBytes = int64(500) << 30
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
// check error, or a disabled feature all deny. There is no storage budget:
// backups are retention-limited, not gigabyte-limited.
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
	return BackupEntitlementDecision{RequiredFeatures: required}
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
