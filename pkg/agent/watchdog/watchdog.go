package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// DefaultInterval is the standard check-loop period.
const DefaultInterval = 30 * time.Second

// HealReporter is called after every recipe execution attempt so that
// the agent can forward the event to Core (e.g. via gRPC).
type HealReporter func(ctx context.Context, event HealEvent) error

// Watchdog is the self-heal engine.  It owns a set of recipes and
// evaluates them on a fixed interval.
type Watchdog struct {
	recipes  []Recipe
	reporter HealReporter
	logger   *slog.Logger
	interval time.Duration

	// stateProvider is injected for testing; production code replaces it
	// with the real collector.
	stateProvider func() (*SystemState, error)

	done chan struct{}
}

// New creates a Watchdog.  Pass nil for reporter if events should be
// silently discarded (useful in tests that only care about side-effects).
func New(recipes []Recipe, reporter HealReporter, logger *slog.Logger) *Watchdog {
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = func(context.Context, HealEvent) error { return nil }
	}
	return &Watchdog{
		recipes:  recipes,
		reporter: reporter,
		logger:   logger,
		interval: DefaultInterval,
		stateProvider: func() (*SystemState, error) {
			return &SystemState{CollectedAt: time.Now()}, nil
		},
		done: make(chan struct{}),
	}
}

// SetInterval overrides the default 30-second loop period.
// Must be called before Start.
func (w *Watchdog) SetInterval(d time.Duration) {
	w.interval = d
}

// SetStateProvider replaces the default (empty) state collector.
func (w *Watchdog) SetStateProvider(fn func() (*SystemState, error)) {
	w.stateProvider = fn
}

// Start launches the check loop in a new goroutine.  It blocks until
// ctx is cancelled or Stop is called.
func (w *Watchdog) Start(ctx context.Context) {
	go w.run(ctx)
}

// Stop signals the loop to exit and waits for it to finish.
func (w *Watchdog) Stop() {
	select {
	case <-w.done:
		// already stopped
	default:
		close(w.done)
	}
}

// run is the main loop.
func (w *Watchdog) run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Run one cycle immediately on start.
	w.cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-ticker.C:
			w.cycle(ctx)
		}
	}
}

// cycle evaluates every enabled recipe once.
func (w *Watchdog) cycle(ctx context.Context) {
	state, err := w.stateProvider()
	if err != nil {
		w.logger.Error("watchdog: state collection failed", "err", err)
		return
	}

	for _, r := range w.recipes {
		if !r.Enabled() {
			continue
		}
		w.evaluate(ctx, r, state)
	}
}

// evaluate runs the full Check → DryRun → Execute → (Rollback) pipeline
// for a single recipe.
func (w *Watchdog) evaluate(ctx context.Context, r Recipe, state *SystemState) {
	cond, err := r.Check(ctx, state)
	if err != nil {
		w.logger.Error("watchdog: check failed",
			"recipe", r.Name(), "err", err)
		return
	}
	if cond == nil || !cond.Triggered {
		return
	}

	w.logger.Info("watchdog: condition triggered",
		"recipe", r.Name(),
		"reason", cond.Reason,
		"severity", cond.Severity)

	if !r.AutoExec() {
		w.report(ctx, HealEvent{
			EventID:       uuid.New().String(),
			RecipeName:    r.Name(),
			TriggerReason: cond.Reason,
			AutoExecuted:  false,
			Success:       false,
			ActionTaken:   "none — manual approval required",
			Severity:      cond.Severity,
			Timestamp:     time.Now(),
		})
		return
	}

	// Dry-run first.
	plan, err := r.DryRun(ctx, cond)
	if err != nil {
		w.logger.Error("watchdog: dry-run failed",
			"recipe", r.Name(), "err", err)
		return
	}

	// Execute.
	start := time.Now()
	result, err := r.Execute(ctx, plan)
	dur := time.Since(start)

	if err != nil || (result != nil && !result.Success) {
		w.logger.Warn("watchdog: execute failed, attempting rollback",
			"recipe", r.Name(), "err", err)

		rbResult, rbErr := r.Rollback(ctx, result)
		rbAction := "rollback-attempted"
		if rbErr != nil {
			rbAction = fmt.Sprintf("rollback-failed: %v", rbErr)
		} else if rbResult != nil && rbResult.Success {
			rbAction = "rollback-succeeded"
		}

		w.report(ctx, HealEvent{
			EventID:        uuid.New().String(),
			RecipeName:     r.Name(),
			TriggerReason:  cond.Reason,
			AutoExecuted:   true,
			Success:        false,
			ActionTaken:    plan.DryRunOutput,
			RollbackAction: rbAction,
			Severity:       cond.Severity,
			Duration:       dur,
			Timestamp:      time.Now(),
		})
		return
	}

	w.logger.Info("watchdog: heal succeeded",
		"recipe", r.Name(), "duration", dur)

	w.report(ctx, HealEvent{
		EventID:       uuid.New().String(),
		RecipeName:    r.Name(),
		TriggerReason: cond.Reason,
		AutoExecuted:  true,
		Success:       true,
		ActionTaken:   plan.DryRunOutput,
		Severity:      cond.Severity,
		Duration:      dur,
		Timestamp:     time.Now(),
	})
}

// report calls the HealReporter callback.
func (w *Watchdog) report(ctx context.Context, ev HealEvent) {
	if err := w.reporter(ctx, ev); err != nil {
		w.logger.Error("watchdog: failed to report heal event",
			"event_id", ev.EventID, "err", err)
	}
}
