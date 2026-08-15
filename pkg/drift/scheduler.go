// Package drift provides periodic drift detection scheduling.
package drift

import (
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
)

// DriftTriggerer is the interface needed to enqueue drift checks.
// Satisfied by *orchestrator.Orchestrator.
type DriftTriggerer interface {
	TriggerDriftCheck(stackID string, triggerType string) (string, error)
}

// SchedulerConfig holds configuration for the drift scheduler.
type SchedulerConfig struct {
	Enabled   bool
	Interval  time.Duration
	Retention int // Max drift results to keep per stack (0 = no cleanup)
}

// NewSchedulerConfigFromConfig creates scheduler config from the main config.
func NewSchedulerConfigFromConfig(cfg *config.Config) SchedulerConfig {
	return SchedulerConfig{
		Enabled:   cfg.Drift.Enabled,
		Interval:  cfg.Drift.DriftIntervalDuration(),
		Retention: cfg.Drift.Retention,
	}
}

// Scheduler handles periodic drift detection for all running stacks.
type Scheduler struct {
	config    SchedulerConfig
	app       core.App
	triggerer DriftTriggerer
	log       *logger.Logger
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.RWMutex
	running   bool
	lastRun   time.Time
	lastError error
}

// NewScheduler creates a new drift scheduler.
func NewScheduler(app core.App, triggerer DriftTriggerer, cfg SchedulerConfig) *Scheduler {
	log := logger.Get().WithComponent("drift-scheduler")

	if cfg.Interval < time.Minute {
		cfg.Interval = 6 * time.Hour
		log.Warn("Drift interval too short, using default", "interval", cfg.Interval)
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 50
	}

	return &Scheduler{
		config:    cfg,
		app:       app,
		triggerer: triggerer,
		log:       log,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the drift scheduler.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	s.log.Info("Starting drift scheduler", "interval", s.config.Interval)

	s.wg.Add(1)
	go s.run()
}

// Stop gracefully stops the drift scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	s.log.Info("Stopping drift scheduler")
	close(s.stopCh)
	s.wg.Wait()
	s.log.Info("Drift scheduler stopped")
}

// IsRunning returns whether the scheduler is running.
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// LastRun returns the time of the last drift check run.
func (s *Scheduler) LastRun() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRun
}

// LastError returns the error from the last drift check run (if any).
func (s *Scheduler) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// run is the main scheduler loop.
func (s *Scheduler) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	// Run initial check after a short delay (let the system stabilize)
	initialDelay := time.NewTimer(60 * time.Second)
	defer initialDelay.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-initialDelay.C:
			s.checkAllStacks()
		case <-ticker.C:
			s.checkAllStacks()
		}
	}
}

// checkAllStacks triggers drift checks for all stacks with status "running".
func (s *Scheduler) checkAllStacks() {
	s.log.Info("Starting scheduled drift check for all running stacks")
	startTime := time.Now()

	// Query all stacks with status "running"
	stacks, err := s.app.FindRecordsByFilter("stacks", "status = 'running'", "", 0, 0)
	if err != nil {
		s.mu.Lock()
		s.lastRun = startTime
		s.lastError = err
		s.mu.Unlock()
		s.log.Error("Failed to query running stacks", "error", err)
		return
	}

	if len(stacks) == 0 {
		s.mu.Lock()
		s.lastRun = startTime
		s.lastError = nil
		s.mu.Unlock()
		s.log.Info("No running stacks found, skipping drift check")
		return
	}

	triggered := 0
	var lastErr error
	for _, stack := range stacks {
		jobID, err := s.triggerer.TriggerDriftCheck(stack.Id, "scheduled")
		if err != nil {
			s.log.Error("Failed to trigger drift check",
				"stack_id", stack.Id,
				"stack_name", stack.GetString("name"),
				"error", err,
			)
			lastErr = err
			continue
		}
		triggered++
		s.log.Info("Drift check triggered",
			"stack_id", stack.Id,
			"stack_name", stack.GetString("name"),
			"job_id", jobID,
		)
	}

	s.mu.Lock()
	s.lastRun = startTime
	s.lastError = lastErr
	s.mu.Unlock()

	s.log.Info("Scheduled drift check completed",
		"stacks_found", len(stacks),
		"checks_triggered", triggered,
		"duration", time.Since(startTime),
	)

	// Apply retention policy: remove old results beyond the retention limit per stack
	s.applyRetention(stacks)
}

// applyRetention removes drift results beyond the retention limit for each stack.
func (s *Scheduler) applyRetention(stacks []*core.Record) {
	if s.config.Retention <= 0 {
		return
	}

	for _, stack := range stacks {
		// Find all results for this stack, ordered by created DESC, skip the first N (retention)
		results, err := s.app.FindRecordsByFilter(
			"drift_results",
			"stack_id = {:stackId}",
			"-created",
			0, 0,
			map[string]any{"stackId": stack.Id},
		)
		if err != nil {
			s.log.Error("Failed to query drift results for retention", "stack_id", stack.Id, "error", err)
			continue
		}

		if len(results) <= s.config.Retention {
			continue
		}

		// Delete excess results (keep only the newest N)
		excess := results[s.config.Retention:]
		deleted := 0
		for _, r := range excess {
			if err := s.app.Delete(r); err != nil {
				s.log.Error("Failed to delete old drift result", "id", r.Id, "error", err)
			} else {
				deleted++
			}
		}
		if deleted > 0 {
			s.log.Info("Drift retention applied", "stack_id", stack.Id, "deleted", deleted, "kept", s.config.Retention)
		}
	}
}
