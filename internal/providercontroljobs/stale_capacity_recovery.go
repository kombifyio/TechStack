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
	defaultStaleCapacityRecoveryBatch    = 25
	maximumStaleCapacityRecoveryPerPass  = 100
	defaultStaleCapacityRecoveryInterval = 15 * time.Second
)

type StaleCapacityRecoveryConfig struct {
	Database       *sql.DB
	Decommissioner jobs.ManagedLeaseDecommissioner
	BatchSize      int
	Interval       time.Duration
	OnError        func(error)
}

// StaleCapacityRecovery closes the operational gap left by destroy jobs that
// predated terminal provider-absence custody. It discovers only native leases
// whose lease or exact canonical server already requests absence and which
// still hold an unreleased reservation, then re-enters the same provider-control
// decommission application used by a user destroy. It neither deletes ledger
// rows nor invents release facts.
type StaleCapacityRecovery struct {
	database       *sql.DB
	decommissioner jobs.ManagedLeaseDecommissioner
	batchSize      int
	interval       time.Duration
	onError        func(error)
	cursorMu       sync.Mutex
	afterTenant    string
	afterLease     string
}

type staleCapacityCandidate struct {
	tenantID string
	ownerID  string
	leaseID  string
	stackID  string
}

func NewStaleCapacityRecovery(cfg StaleCapacityRecoveryConfig) (*StaleCapacityRecovery, error) {
	if cfg.Database == nil || nilManagerDependency(cfg.Decommissioner) {
		return nil, fmt.Errorf("providercontroljobs: stale capacity database and decommissioner are required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultStaleCapacityRecoveryBatch
	}
	if cfg.BatchSize > 101 {
		return nil, fmt.Errorf("providercontroljobs: stale capacity batch must not exceed 101")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultStaleCapacityRecoveryInterval
	}
	return &StaleCapacityRecovery{
		database: cfg.Database, decommissioner: cfg.Decommissioner,
		batchSize: cfg.BatchSize, interval: cfg.Interval, onError: cfg.OnError,
	}, nil
}

func (r *StaleCapacityRecovery) Run(ctx context.Context) {
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

func (r *StaleCapacityRecovery) runPass(ctx context.Context) {
	if err := r.RecoverOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && r.onError != nil {
		r.onError(err)
	}
}

func (r *StaleCapacityRecovery) RecoverOnce(ctx context.Context) error {
	if r == nil || r.database == nil || r.decommissioner == nil {
		return fmt.Errorf("providercontroljobs: stale capacity recovery is not configured")
	}
	r.cursorMu.Lock()
	defer r.cursorMu.Unlock()
	afterTenant, afterLease := r.afterTenant, r.afterLease
	processed := 0
	for processed < maximumStaleCapacityRecoveryPerPass {
		limit := r.batchSize
		if remaining := maximumStaleCapacityRecoveryPerPass - processed; limit > remaining {
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
			afterTenant, afterLease = candidate.tenantID, candidate.leaseID
			r.afterTenant, r.afterLease = afterTenant, afterLease
			_, decommissionErr := r.decommissioner.DecommissionManagedLeases(ctx, jobs.ManagedLeaseDecommissionRequest{
				TenantID: candidate.tenantID, OwnerID: candidate.ownerID,
				LeaseID: candidate.leaseID, StackID: candidate.stackID,
			})
			var waiting *jobs.JobWaitError
			if decommissionErr != nil && !errors.As(decommissionErr, &waiting) {
				if r.onError != nil {
					r.onError(fmt.Errorf("providercontroljobs: recover stale capacity lease %s: %w", candidate.leaseID, decommissionErr))
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

func (r *StaleCapacityRecovery) loadCandidates(
	ctx context.Context,
	afterTenant string,
	afterLease string,
	limit int,
) ([]staleCapacityCandidate, error) {
	rows, err := r.database.QueryContext(ctx, `
		SELECT tenant_id, owner_subject_id, lease_id, stack_id
		FROM provider_control_list_stale_capacity_recovery_candidates($1, $2, $3)
	`, strings.TrimSpace(afterTenant), strings.TrimSpace(afterLease), limit)
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: list stale capacity recovery candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]staleCapacityCandidate, 0, limit)
	for rows.Next() {
		var candidate staleCapacityCandidate
		if err := rows.Scan(&candidate.tenantID, &candidate.ownerID, &candidate.leaseID, &candidate.stackID); err != nil {
			return nil, fmt.Errorf("providercontroljobs: scan stale capacity recovery candidate: %w", err)
		}
		if candidate.tenantID == "" || candidate.ownerID == "" || candidate.leaseID == "" || candidate.stackID == "" {
			return nil, fmt.Errorf("providercontroljobs: stale capacity recovery candidate is incomplete")
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: iterate stale capacity recovery candidates: %w", err)
	}
	return candidates, nil
}
