package backupjobs

import (
	"context"
	"errors"
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
type AdmissionConfig struct {
	Features monthlyruntime.BackupFeatureChecker
	Usage    UsageReader
	// AllowUnmeasured admits a stack that has never been measured. It defaults
	// to false. An unmeasured stack is unknown rather than empty, and admitting
	// on unknown is how a quota stops being a quota; the flag exists only so an
	// operator can bootstrap a fleet before the first sweep has run, and it is
	// a deliberate, visible choice rather than a silent fallback.
	AllowUnmeasured bool
}

// NewAdmission returns the fail-closed backup gate.
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

		usage, err := cfg.Usage.Usage(ctx, req.TenantID, req.StackID)
		switch {
		case errors.Is(err, backupstore.ErrUsageNotFound):
			if !cfg.AllowUnmeasured {
				return jobs.BackupAdmissionDecision{
					Denied:     true,
					QuotaBytes: entitlement.QuotaBytes,
					Details: monthlyruntime.BackupQuotaExceededDetails(
						entitlement.QuotaBytes, 0, entitlement.GrantedStorageFeature),
				}, nil
			}
		case err != nil:
			// A meter that cannot answer denies. Treating an unreadable meter
			// as zero usage would admit every tenant precisely when the
			// control plane has lost sight of what they are storing.
			return jobs.BackupAdmissionDecision{Denied: true, QuotaBytes: entitlement.QuotaBytes},
				fmt.Errorf("backupjobs: read measured backup usage: %w", err)
		}

		if entitlement.QuotaBytes > 0 && usage.StoredBytes >= entitlement.QuotaBytes {
			return jobs.BackupAdmissionDecision{
				Denied:     true,
				QuotaBytes: entitlement.QuotaBytes,
				UsedBytes:  usage.StoredBytes,
				Details: monthlyruntime.BackupQuotaExceededDetails(
					entitlement.QuotaBytes, usage.StoredBytes, entitlement.GrantedStorageFeature),
			}, nil
		}

		return jobs.BackupAdmissionDecision{
			QuotaBytes: entitlement.QuotaBytes,
			UsedBytes:  usage.StoredBytes,
		}, nil
	}, nil
}
