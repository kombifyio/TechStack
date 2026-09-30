package backupjobs

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// UsageReader reports the measured object-store total for one stack. It is the
// control-plane measurement, never the node-reported repository size: the node
// figure sums one generation's logical bytes and is neither kombify's cost nor
// the customer's protected total.
type UsageReader interface {
	Usage(ctx context.Context, tenantID, stackID string) (backupstore.StoredUsage, error)
}

// AdmissionConfig composes the entitlement gate and the usage meter into the
// single decision the scanner and the routes both resolve.
//
// Backups are retention-limited, never gigabyte-limited
// (BILLING-ENTITLEMENT-STANDARD §2.4, owner decision 2026-09-28): the meter
// only records the measured basis a restore-drill renewal receipt signs, and
// no measured size, missing measurement or unreadable meter denies a backup.
type AdmissionConfig struct {
	Features monthlyruntime.BackupFeatureChecker
	Usage    UsageReader
}

// NewAdmission returns the fail-closed backup entitlement gate.
func NewAdmission(cfg AdmissionConfig) (jobs.BackupAdmission, error) {
	if cfg.Features == nil {
		return nil, fmt.Errorf("backupjobs: admission requires a feature checker")
	}
	if cfg.Usage == nil {
		return nil, fmt.Errorf("backupjobs: admission requires a usage reader")
	}
	return func(ctx context.Context, req jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
		entitlement := monthlyruntime.EvaluateBackupEntitlement(ctx, cfg.Features, req.UserID, req.IncludeContent)
		if entitlement.Denied {
			return jobs.BackupAdmissionDecision{
				Denied:  true,
				Details: entitlement.BackupEntitlementDenialDetails(),
			}, nil
		}
		decision := jobs.BackupAdmissionDecision{
			QuotaBytes:     monthlyruntime.BackupSizingCeilingBytes,
			IncludeContent: req.IncludeContent,
		}
		// A missing or unreadable measurement leaves MeasuredAt zero. Scheduled
		// backups do not depend on it; the restore-drill path rejects a zero or
		// stale measurement itself because its signed receipt needs one.
		if usage, err := cfg.Usage.Usage(ctx, req.TenantID, req.StackID); err == nil {
			decision.UsedBytes = usage.StoredBytes
			decision.MeasuredAt = usage.MeasuredAt
		}
		return decision, nil
	}, nil
}
