package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

// WorkerConfig tunes the worker's two loops.
type WorkerConfig struct {
	// PollInterval is how often runnable runs are re-advanced (crash-recovery
	// safety net for runs left pending/running after a restart).
	PollInterval time.Duration
	// TimerInterval is how often due timers are swept and fired.
	TimerInterval time.Duration
	// Batch caps how many runs/timers are processed per tick.
	Batch int
}

// DefaultWorkerConfig returns sensible defaults: 15s run poll, 10s timer sweep,
// 50 per batch.
func DefaultWorkerConfig() WorkerConfig {
	return WorkerConfig{PollInterval: 15 * time.Second, TimerInterval: 10 * time.Second, Batch: 50}
}

// Worker drives the engine autonomously: a poll loop advances runnable runs
// (so a process restart resumes in-flight work) and a sweep loop fires durable
// timers (escalation/reminder). Inline StartRun/Deliver still advance runs
// immediately; the worker is the background safety net + timer engine.
type Worker struct {
	engine *Engine
	store  RunStore
	cfg    WorkerConfig
	log    *logger.Logger
}

// NewWorker builds a worker. Zero-valued config fields fall back to defaults.
func NewWorker(engine *Engine, cfg WorkerConfig) *Worker {
	def := DefaultWorkerConfig()
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = def.PollInterval
	}
	if cfg.TimerInterval <= 0 {
		cfg.TimerInterval = def.TimerInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = def.Batch
	}
	return &Worker{
		engine: engine,
		store:  engine.store,
		cfg:    cfg,
		log:    logger.Get().WithComponent("ril.workflow.worker"),
	}
}

// Start launches the poll + sweep loops and returns immediately. Both stop when
// ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	go w.loop(ctx, w.cfg.PollInterval, w.PollRuns)
	go w.loop(ctx, w.cfg.TimerInterval, w.SweepTimers)
	w.log.Info("ril_workflow_worker_started",
		"poll", w.cfg.PollInterval.String(), "timer", w.cfg.TimerInterval.String())
}

func (w *Worker) loop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// PollRuns advances runnable runs. Exported so it can be invoked in one-shot
// mode (tests, manual reconcile) as well as from the loop.
func (w *Worker) PollRuns(ctx context.Context) {
	if w.engine.backgroundTask != nil {
		if err := w.engine.backgroundTask(ctx); err != nil {
			w.log.Warn("ril_workflow_background_task_failed", "error", err.Error())
		}
	}
	for _, st := range []RunStatus{RunPending, RunRunning} {
		runs, err := w.store.ListRunsByStatus(st, w.cfg.Batch)
		if err != nil {
			w.log.Error("ril_workflow_poll_list_failed", "status", string(st), "error", err.Error())
			continue
		}
		for _, r := range runs {
			if err := w.engine.tryAdvance(ctx, r.RunID); err != nil {
				w.log.Warn("ril_workflow_poll_advance_failed", "run_id", r.RunID, "error", err.Error())
			}
		}
	}
}

// SweepTimers fires due timers: it delivers a timed-out signal to the run
// awaiting the timer's signal key, then marks the timer fired. A run that was
// already resumed (ErrNotFound) is benign — the timer is still marked fired.
func (w *Worker) SweepTimers(ctx context.Context) {
	timers, err := w.store.ListDueTimers(w.engine.now(), w.cfg.Batch)
	if err != nil {
		w.log.Error("ril_workflow_timer_list_failed", "error", err.Error())
		return
	}
	for _, tm := range timers {
		sig := Signal{Key: tm.SignalKey, TimedOut: true, DeliverAt: w.engine.now()}
		if derr := w.engine.Deliver(ctx, sig); derr != nil && !errors.Is(derr, ErrNotFound) {
			w.log.Warn("ril_workflow_timer_deliver_failed", "run_id", tm.RunID, "signal", tm.SignalKey, "error", derr.Error())
		}
		if merr := w.store.MarkTimerFired(tm.ID); merr != nil {
			w.log.Warn("ril_workflow_timer_mark_failed", "timer_id", tm.ID, "error", merr.Error())
		}
	}
}
