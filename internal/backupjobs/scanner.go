package backupjobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	defaultScanInterval = 60 * time.Second
	defaultBatchSize    = 50
	maximumPerPass      = 500
)

// ScheduleStore is the slice of the projection the scanner needs. It is an
// interface so the decision path can be exercised without Postgres, and so the
// scanner cannot reach any part of the store it has no business touching.
type ScheduleStore interface {
	ListDue(ctx context.Context, now time.Time, afterTenant, afterStack string, limit int) ([]Due, error)
	MarkRan(ctx context.Context, tenantID, stackID string, now time.Time) error
}

// Enqueuer accepts one admitted backup. It is the narrow seam onto the job
// queue so the scanner never owns queue mechanics.
type Enqueuer interface {
	EnqueueBackup(ctx context.Context, payload jobs.BackupPayload) error
}

// ScannerConfig wires the scanner. Every field is required except the
// observers, which exist so a denial is visible to operators without the
// scanner deciding how to report it.
type ScannerConfig struct {
	Store     ScheduleStore
	Admission jobs.BackupAdmission
	Enqueuer  Enqueuer
	Interval  time.Duration
	BatchSize int
	Now       func() time.Time
	OnError   func(error)
	OnDenied  func(Due, jobs.BackupAdmissionDecision)
}

// Scanner selects due stacks, resolves the entitlement and quota gate for each,
// and enqueues the admitted ones.
//
// The gate runs here, before the enqueue, and not in the handler: a job that
// exists has already committed the platform to the cost-bearing action.
type Scanner struct {
	cfg         ScannerConfig
	cursorMu    sync.Mutex
	afterTenant string
	afterStack  string
}

func NewScanner(cfg ScannerConfig) (*Scanner, error) {
	if cfg.Store == nil || cfg.Enqueuer == nil {
		return nil, fmt.Errorf("backupjobs: scanner requires a schedule store and an enqueuer")
	}
	if cfg.Admission == nil {
		// Refusing to start is the point. A scanner without a gate would run
		// cost-bearing backups for every due stack with nothing to stop it.
		return nil, fmt.Errorf("backupjobs: scanner requires a backup admission authority")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultScanInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Scanner{cfg: cfg}, nil
}

// Run scans on an interval until the context ends.
func (s *Scanner) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.pass(ctx)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pass(ctx)
		}
	}
}

func (s *Scanner) pass(ctx context.Context) {
	if err := s.ScanOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && s.cfg.OnError != nil {
		s.cfg.OnError(err)
	}
}

// ScanOnce runs one bounded, keyset-paginated pass.
func (s *Scanner) ScanOnce(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("backupjobs: scanner is not configured")
	}
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()

	now := s.cfg.Now().UTC()
	processed := 0
	for processed < maximumPerPass {
		limit := s.cfg.BatchSize
		if remaining := maximumPerPass - processed; limit > remaining {
			limit = remaining
		}
		due, err := s.cfg.Store.ListDue(ctx, now, s.afterTenant, s.afterStack, limit)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			// The cursor resets only when a pass runs dry, so a long fleet is
			// walked across passes instead of starving its tail.
			s.afterTenant, s.afterStack = "", ""
			return nil
		}
		for _, item := range due {
			s.afterTenant, s.afterStack = item.TenantID, item.StackID
			processed++
			if err := s.handle(ctx, item, now); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				if s.cfg.OnError != nil {
					s.cfg.OnError(err)
				}
			}
		}
	}
	return nil
}

func (s *Scanner) handle(ctx context.Context, item Due, now time.Time) error {
	subject := item.OwnerID
	if subject == "" {
		// Without a subject the entitlement chain has nobody to resolve, and
		// AdmitBackup denies. Advancing the slot keeps the stack from being
		// re-selected forever over a projection defect.
		_ = s.cfg.Store.MarkRan(ctx, item.TenantID, item.StackID, now)
		return fmt.Errorf("backupjobs: stack %s has no owner subject to gate against", item.StackID)
	}

	decision, err := jobs.AdmitBackup(ctx, s.cfg.Admission, jobs.BackupAdmissionRequest{
		TenantID:       item.TenantID,
		StackID:        item.StackID,
		UserID:         subject,
		IncludeContent: item.IncludeContent,
	})
	// The slot advances whether the gate admitted or denied. A denied stack
	// whose slot never moves is re-selected on every pass, which turns one
	// over-quota tenant into an unbounded retry loop against the entitlement
	// chain.
	if markErr := s.cfg.Store.MarkRan(ctx, item.TenantID, item.StackID, now); markErr != nil {
		return fmt.Errorf("backupjobs: advance schedule for %s: %w", item.StackID, markErr)
	}
	if err != nil {
		return fmt.Errorf("backupjobs: admission for %s: %w", item.StackID, err)
	}
	if decision.Denied {
		if s.cfg.OnDenied != nil {
			s.cfg.OnDenied(item, decision)
		}
		return nil
	}

	return s.cfg.Enqueuer.EnqueueBackup(ctx, jobs.BackupPayload{
		TenantID:          item.TenantID,
		StackID:           item.StackID,
		StackName:         item.StackName,
		OwnerID:           item.OwnerID,
		IncludeContent:    item.IncludeContent,
		QuotaBytes:        decision.QuotaBytes,
		AdmittedUsedBytes: decision.UsedBytes,
	})
}
