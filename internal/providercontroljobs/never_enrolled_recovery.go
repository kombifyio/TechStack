package providercontroljobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	defaultNeverEnrolledRecoveryBatch    = 25
	maximumNeverEnrolledRecoveryPerPass  = 100
	defaultNeverEnrolledRecoveryInterval = 30 * time.Second
	// MinimumNeverEnrolledRecoveryWindow refuses windows shorter than the
	// managed enrollment wait, so an operator misconfiguration cannot
	// decommission a VM that is still enrolling.
	MinimumNeverEnrolledRecoveryWindow = 5 * time.Minute
	defaultNeverEnrolledRecoveryWindow = time.Hour
	maximumNeverEnrolledRecoveryWindow = 24 * time.Hour
)

// NeverEnrolledRecoveryCandidate is one unreleased managed lease whose
// enrollment window passed without a canonical enrolled runtime.
type NeverEnrolledRecoveryCandidate struct {
	TenantID   string
	OwnerID    string
	LeaseID    string
	StackID    string
	ReservedAt time.Time
}

// NeverEnrolledRecovery re-enters the existing provider-control decommission
// application for managed leases that were provisioned but never enrolled or
// rolled out. Discovery is read-only; provider-control still proves exact
// provider absence before it records a release fact.
type NeverEnrolledRecovery struct {
	database       *sql.DB
	decommissioner jobs.ManagedLeaseDecommissioner
	window         time.Duration
	batchSize      int
	interval       time.Duration
	onReap         func(NeverEnrolledRecoveryCandidate)
	onError        func(error)
	cursorMu       sync.Mutex
	afterTenant    string
	afterLease     string
}

type NeverEnrolledRecoveryConfig struct {
	Database       *sql.DB
	Decommissioner jobs.ManagedLeaseDecommissioner
	// EnrollmentWindow is how long a reserved managed lease may stay without
	// an enrolled runtime before it is reaped. Zero selects one hour.
	EnrollmentWindow time.Duration
	BatchSize        int
	Interval         time.Duration
	// OnReap fires before the decommission call so billing-risk cleanup is
	// observable (log/alert) even when the decommission later fails.
	OnReap  func(NeverEnrolledRecoveryCandidate)
	OnError func(error)
}

func NewNeverEnrolledRecovery(cfg NeverEnrolledRecoveryConfig) (*NeverEnrolledRecovery, error) {
	if cfg.Database == nil || nilManagerDependency(cfg.Decommissioner) {
		return nil, fmt.Errorf("providercontroljobs: never-enrolled recovery database and decommissioner are required")
	}
	window := cfg.EnrollmentWindow
	if window <= 0 {
		window = defaultNeverEnrolledRecoveryWindow
	}
	if window < MinimumNeverEnrolledRecoveryWindow || window > maximumNeverEnrolledRecoveryWindow {
		return nil, fmt.Errorf("providercontroljobs: never-enrolled enrollment window must be between %s and %s", MinimumNeverEnrolledRecoveryWindow, maximumNeverEnrolledRecoveryWindow)
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultNeverEnrolledRecoveryBatch
	}
	if cfg.BatchSize > 101 {
		return nil, fmt.Errorf("providercontroljobs: never-enrolled recovery batch must not exceed 101")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultNeverEnrolledRecoveryInterval
	}
	return &NeverEnrolledRecovery{
		database: cfg.Database, decommissioner: cfg.Decommissioner,
		window: window, batchSize: cfg.BatchSize, interval: cfg.Interval,
		onReap: cfg.OnReap, onError: cfg.OnError,
	}, nil
}

// Run performs one pass at boot and then repeats on the configured interval
// until the context is canceled.
func (r *NeverEnrolledRecovery) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.runPass(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runPass(ctx)
		}
	}
}

func (r *NeverEnrolledRecovery) runPass(ctx context.Context) {
	if err := r.RecoverOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && r.onError != nil {
		r.onError(err)
	}
}

// RecoverOnce reaps at most the bounded per-pass budget. Candidates are
// consumed in tenant/lease order with a resumable keyset cursor.
func (r *NeverEnrolledRecovery) RecoverOnce(ctx context.Context) error {
	if r == nil || r.database == nil || r.decommissioner == nil {
		return fmt.Errorf("providercontroljobs: never-enrolled recovery is not configured")
	}
	r.cursorMu.Lock()
	defer r.cursorMu.Unlock()
	afterTenant, afterLease := r.afterTenant, r.afterLease
	processed := 0
	for processed < maximumNeverEnrolledRecoveryPerPass {
		limit := r.batchSize
		if remaining := maximumNeverEnrolledRecoveryPerPass - processed; limit > remaining {
			limit = remaining
		}
		candidates, err := r.loadCandidates(ctx, afterTenant, afterLease, limit)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			r.afterTenant, r.afterLease = "", ""
			return nil
		}
		for _, candidate := range candidates {
			afterTenant, afterLease = candidate.TenantID, candidate.LeaseID
			r.afterTenant, r.afterLease = afterTenant, afterLease
			if r.onReap != nil {
				r.onReap(candidate)
			}
			_, decommissionErr := r.decommissioner.DecommissionManagedLeases(ctx, jobs.ManagedLeaseDecommissionRequest{
				TenantID: candidate.TenantID, OwnerID: candidate.OwnerID,
				LeaseID: candidate.LeaseID, StackID: candidate.StackID,
			})
			var waiting *jobs.JobWaitError
			if decommissionErr != nil && !errors.As(decommissionErr, &waiting) {
				if r.onError != nil {
					r.onError(fmt.Errorf("providercontroljobs: reap never-enrolled runtime lease %s: %w", candidate.LeaseID, decommissionErr))
				}
			}
			processed++
		}
		if len(candidates) < limit {
			r.afterTenant, r.afterLease = "", ""
			return nil
		}
	}
	return nil
}

func (r *NeverEnrolledRecovery) loadCandidates(
	ctx context.Context,
	afterTenant string,
	afterLease string,
	limit int,
) ([]NeverEnrolledRecoveryCandidate, error) {
	rows, err := r.database.QueryContext(ctx, `
		SELECT tenant_id, owner_subject_id, lease_id, stack_id, reserved_at
		FROM provider_control_list_never_enrolled_runtime_candidates($1, $2, $3, $4)
	`, strings.TrimSpace(afterTenant), strings.TrimSpace(afterLease), int(r.window.Seconds()), limit)
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: list never-enrolled runtime candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]NeverEnrolledRecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate NeverEnrolledRecoveryCandidate
		if err := rows.Scan(&candidate.TenantID, &candidate.OwnerID, &candidate.LeaseID, &candidate.StackID, &candidate.ReservedAt); err != nil {
			return nil, fmt.Errorf("providercontroljobs: scan never-enrolled runtime candidate: %w", err)
		}
		if candidate.TenantID == "" || candidate.OwnerID == "" || candidate.LeaseID == "" || candidate.StackID == "" {
			return nil, fmt.Errorf("providercontroljobs: never-enrolled runtime candidate is incomplete")
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: iterate never-enrolled runtime candidates: %w", err)
	}
	return candidates, nil
}
