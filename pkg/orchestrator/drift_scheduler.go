package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

const (
	// MinDriftScheduleInterval and MaxDriftScheduleInterval bound the
	// scheduled drift detection: often enough that Advanced drift is found
	// within a day, never so often that detection floods the job queue.
	MinDriftScheduleInterval = 15 * time.Minute
	MaxDriftScheduleInterval = 24 * time.Hour

	driftScheduleTenantPage      = 100
	driftScheduleMaxTenants      = 1000
	driftScheduleMaxDispatches   = 200
	driftScheduleTriggerType     = "scheduled"
	driftScheduleRunningStatus   = persistentStateRunning
	driftScheduleCheckingStatus  = "checking"
	driftScheduleAdvancedModeKey = jobs.StackKitModeField
)

// BoundedDriftScheduleInterval clamps a configured interval into the
// supported range.
func BoundedDriftScheduleInterval(interval time.Duration) time.Duration {
	switch {
	case interval < MinDriftScheduleInterval:
		return MinDriftScheduleInterval
	case interval > MaxDriftScheduleInterval:
		return MaxDriftScheduleInterval
	default:
		return interval
	}
}

// DriftSchedulerConfig wires the scheduled Advanced drift detection.
type DriftSchedulerConfig struct {
	Directory controlplane.DriftScheduleTenantDirectory
	Interval  time.Duration
	Now       func() time.Time
	OnError   func(error)
}

// DriftScheduler enumerates the tenants of the drift schedule directory and
// starts a drift check for every rolled-out stack that recorded Advanced
// Mode and has not been checked within the interval. It dispatches only
// Advanced drift: the drift job runs the typed DRIFT_DETECT on the node and
// fails a report that is not from the Terramate generation target.
type DriftScheduler struct {
	orch *Orchestrator
	cfg  DriftSchedulerConfig
	mu   sync.Mutex
}

// NewDriftScheduler builds the scheduler. The interval is bounded.
func (o *Orchestrator) NewDriftScheduler(cfg DriftSchedulerConfig) (*DriftScheduler, error) {
	if o == nil || cfg.Directory == nil {
		return nil, fmt.Errorf("drift scheduler requires the orchestrator and the drift schedule tenant directory")
	}
	cfg.Interval = BoundedDriftScheduleInterval(cfg.Interval)
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &DriftScheduler{orch: o, cfg: cfg}, nil
}

// Interval is the bounded interval between passes.
func (s *DriftScheduler) Interval() time.Duration { return s.cfg.Interval }

// Run schedules on the interval until the context ends.
func (s *DriftScheduler) Run(ctx context.Context) {
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

func (s *DriftScheduler) pass(ctx context.Context) {
	if _, err := s.ScheduleOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.report(err)
	}
}

func (s *DriftScheduler) report(err error) {
	if s.cfg.OnError != nil {
		s.cfg.OnError(err)
	}
}

// ScheduleOnce runs one bounded pass and returns the started drift job IDs.
func (s *DriftScheduler) ScheduleOnce(ctx context.Context) ([]string, error) {
	if s == nil || s.orch == nil {
		return nil, fmt.Errorf("drift scheduler is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.orch.effectiveStackStore()
	if store == nil {
		return nil, fmt.Errorf("drift scheduler requires the canonical stack store")
	}
	now := s.cfg.Now().UTC()
	started := []string{}
	after := ""
	for visited := 0; visited < driftScheduleMaxTenants; {
		tenants, err := s.cfg.Directory.ListDriftScheduleTenants(ctx, after, driftScheduleTenantPage)
		if err != nil {
			return started, err
		}
		if len(tenants) == 0 {
			return started, nil
		}
		for _, tenantID := range tenants {
			after = tenantID
			// The other release channel runs this tenant's work (pkg/releasechannel).
			if !releasechannel.OwnsTenant(tenantID) {
				continue
			}
			visited++
			if err := ctx.Err(); err != nil {
				return started, err
			}
			stacks, err := store.ListStacksByTenant(ctx, tenantID)
			if err != nil {
				s.report(fmt.Errorf("drift schedule: list stacks of tenant %s: %w", tenantID, err))
				continue
			}
			if len(stacks) == 0 {
				if err := s.cfg.Directory.CompactDriftScheduleTenant(ctx, tenantID); err != nil {
					s.report(fmt.Errorf("drift schedule: compact tenant %s: %w", tenantID, err))
				}
				continue
			}
			for _, stack := range stacks {
				if len(started) >= driftScheduleMaxDispatches {
					return started, nil
				}
				if !s.due(stack, now) {
					continue
				}
				jobID, err := s.orch.TriggerDriftCheck(DriftLifecycleRequest{
					RequestContext: ctx, TenantID: stack.TenantID, OwnerID: stack.OwnerSubjectID,
					StackID: stack.ID, TriggerType: driftScheduleTriggerType,
				})
				if err != nil {
					s.report(fmt.Errorf("drift schedule: start drift check of stack %s: %w", stack.ID, err))
					continue
				}
				started = append(started, jobID)
			}
		}
	}
	return started, nil
}

// due reports whether a stack takes part in this pass: it is rolled out,
// recorded Advanced Mode, is not being checked and was not checked within
// the interval.
func (s *DriftScheduler) due(stack controlplane.Stack, now time.Time) bool {
	if stack.DeletedAt != nil || strings.TrimSpace(stack.OwnerSubjectID) == "" ||
		stack.Status != driftScheduleRunningStatus || stack.DriftStatus == driftScheduleCheckingStatus {
		return false
	}
	mode, _ := stack.RuntimeSummary[driftScheduleAdvancedModeKey].(string)
	if mode != jobs.StackKitModeAdvanced {
		return false
	}
	return stack.DriftCheckedAt == nil || now.Sub(*stack.DriftCheckedAt) >= s.cfg.Interval
}
