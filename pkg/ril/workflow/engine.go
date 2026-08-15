package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

const signalContextKey = "__signal"

// Engine executes registered WorkflowDefinitions over the durable Store. It is
// the checkpoint-per-step driver: it advances a run one step at a time,
// persisting state after each step so a restart resumes from current_step
// without replaying completed steps. See CONTRACT.md.
//
// Advance is synchronous (drives a run until it suspends, completes, or fails).
// The Worker (separate file) calls Advance from a poll loop + timer sweeper.
type Engine struct {
	store RunStore
	acts  *ActivityRunner
	defs  map[RunType]WorkflowDefinition
	log   *logger.Logger

	// now and sleep are injectable for deterministic tests.
	now            func() time.Time
	sleep          func(time.Duration)
	backgroundTask func(context.Context) error

	// runLocks serializes Advance per run so the worker poll loop and inline
	// StartRun/Deliver can never drive the same run concurrently.
	runLocks sync.Map // runID -> *sync.Mutex
}

// lockRun acquires the per-run lock and returns its release func.
func (e *Engine) lockRun(runID string) func() {
	v, _ := e.runLocks.LoadOrStore(runID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// tryAdvance advances a run only if no other goroutine currently holds its
// lock; otherwise it skips (returns nil). Used by the worker poll loop so one
// long-running inline advance cannot stall the whole sweep.
func (e *Engine) tryAdvance(ctx context.Context, runID string) error {
	v, _ := e.runLocks.LoadOrStore(runID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil
	}
	defer mu.Unlock()
	return e.advance(ctx, runID)
}

// NewEngine builds an engine over the given store + activity runner.
func NewEngine(store RunStore, acts *ActivityRunner) *Engine {
	return &Engine{
		store: store,
		acts:  acts,
		defs:  make(map[RunType]WorkflowDefinition),
		log:   logger.Get().WithComponent("ril.workflow.engine"),
		now:   func() time.Time { return time.Now().UTC() },
		sleep: time.Sleep,
	}
}

// Register adds a workflow definition. Call at wiring time before Advance.
func (e *Engine) Register(def WorkflowDefinition) {
	e.defs[def.Type()] = def
}

// SetBackgroundTask installs one bounded, best-effort task that runs before
// each worker poll. It is used for durable work that must be retried after a
// process restart but does not itself advance a workflow.
func (e *Engine) SetBackgroundTask(task func(context.Context) error) {
	e.backgroundTask = task
}

// StartRun persists a new run and drives it through its first phase (until it
// suspends, completes, or fails). Returns the run id.
func (e *Engine) StartRun(ctx context.Context, run *Run) (string, error) {
	if _, ok := e.defs[run.Type]; !ok {
		return "", fmt.Errorf("workflow: no definition registered for type %q", run.Type)
	}
	if err := e.store.CreateRun(run); err != nil {
		return "", err
	}
	return run.RunID, e.Advance(ctx, run.RunID)
}

// Deliver routes an external signal (approval, agent-result, timer fire) to the
// suspended run awaiting it, then resumes the run. Returns ErrNotFound (wrapped)
// if no run is awaiting the signal — a benign race (already resumed).
func (e *Engine) Deliver(ctx context.Context, sig Signal) error {
	run, err := e.store.FindSuspendedBySignal(sig.Key)
	if err != nil {
		return err
	}
	if sig.DeliverAt.IsZero() {
		sig.DeliverAt = e.now()
	}
	release := e.lockRun(run.RunID)
	defer release()

	if run.Context == nil {
		run.Context = map[string]any{}
	}
	run.Context[signalContextKey] = sig
	run.AwaitingSignal = ""
	run.Status = RunRunning
	if err := e.store.UpdateRun(run); err != nil {
		return err
	}
	e.log.Info("ril_workflow_signal_delivered", "run_id", run.RunID, "signal", sig.Key, "timed_out", sig.TimedOut)
	return e.advance(ctx, run.RunID)
}

// Advance drives a run forward under the per-run lock. See advance for the
// step-driving logic.
func (e *Engine) Advance(ctx context.Context, runID string) error {
	release := e.lockRun(runID)
	defer release()
	return e.advance(ctx, runID)
}

// advance runs steps from current_step until the run suspends (awaiting a
// signal), completes (all steps done), or fails (a step exhausts retries,
// triggering saga compensation). Caller MUST hold the per-run lock. Safe to
// call repeatedly and after a restart — completed steps are not re-run.
func (e *Engine) advance(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(runID)
	if err != nil {
		return err
	}
	if IsTerminal(run.Status) {
		return nil
	}
	def, ok := e.defs[run.Type]
	if !ok {
		return fmt.Errorf("workflow: no definition registered for type %q", run.Type)
	}
	steps := def.Steps()
	policy := def.RetryPolicy()

	// A signal delivered by Deliver() is stashed in the run context; consume it
	// so it reaches the (re-entrant) suspending step on resume.
	pending := e.consumeSignal(run)

	if run.Status == RunPending {
		now := e.now()
		run.Status = RunRunning
		run.StartedAt = &now
		if err := e.store.UpdateRun(run); err != nil {
			return err
		}
	}

	for run.CurrentStep < len(steps) {
		sd := steps[run.CurrentStep]
		step, err := e.getOrCreateStep(run, sd.Name)
		if err != nil {
			return err
		}

		// Already-completed step (restart/idempotent resume): skip forward.
		if step.Status == StepCompleted {
			run.CurrentStep++
			if err := e.store.UpdateRun(run); err != nil {
				return err
			}
			continue
		}

		rc := &RunContext{Run: run, Activities: e.acts, Signal: pending}
		pending = nil // a signal is consumed by the first step that runs

		res, runErr := e.runStepWithRetry(ctx, step, sd, policy, rc)
		if runErr != nil {
			return e.failWithCompensation(ctx, run, steps, runErr)
		}
		if res.Suspend != nil {
			return e.suspend(run, res.Suspend)
		}

		// Success: checkpoint the step, advance the run.
		now := e.now()
		step.Status = StepCompleted
		step.Output = res.Output
		step.Error = ""
		step.FinishedAt = &now
		if err := e.store.UpdateStep(step); err != nil {
			return err
		}
		run.CurrentStep++
		if err := e.store.UpdateRun(run); err != nil {
			return err
		}
	}

	now := e.now()
	run.Status = RunCompleted
	run.FinishedAt = &now
	if err := e.store.UpdateRun(run); err != nil {
		return err
	}
	e.log.Info("ril_workflow_run_completed", "run_id", run.RunID, "type", string(run.Type))
	return nil
}

// runStepWithRetry executes a step, retrying transient failures per the policy
// with exponential backoff. A returned StepResult with a non-nil Suspend (or a
// successful result) ends the loop immediately — suspend/resume does NOT consume
// the retry budget; only failures increment Attempt.
func (e *Engine) runStepWithRetry(ctx context.Context, step *Step, sd StepDef, policy RetryPolicy, rc *RunContext) (StepResult, error) {
	for {
		now := e.now()
		step.StartedAt = &now
		// Pending->Running (first run) or Failed->Running (retry). On resume the
		// step is already Running; UpdateStep treats same-status as a no-op.
		step.Status = StepRunning
		if err := e.store.UpdateStep(step); err != nil {
			return StepResult{}, err
		}

		res, err := safeRunStep(ctx, sd.Run, rc)
		if err == nil {
			return res, nil
		}

		step.Attempt++
		step.Error = err.Error()
		step.Status = StepFailed
		if uerr := e.store.UpdateStep(step); uerr != nil {
			return StepResult{}, uerr
		}
		e.log.Warn("ril_workflow_step_failed", "run_id", step.RunID, "step", step.Name, "attempt", step.Attempt, "error", err.Error())

		if !policy.ShouldRetry(step.Attempt) {
			return StepResult{}, err
		}
		e.sleep(policy.Backoff(step.Attempt + 1))
		rc.Signal = nil // a signal is only valid for the first execution
	}
}

// suspend transitions the run to suspended awaiting a signal, optionally arming
// a durable timer that will deliver the same signal (timed-out) on expiry.
func (e *Engine) suspend(run *Run, d *SuspendDirective) error {
	run.Status = RunSuspended
	run.AwaitingSignal = d.SignalKey
	if err := e.store.UpdateRun(run); err != nil {
		return err
	}
	if d.Timeout > 0 {
		kind := d.TimerKind
		if kind == "" {
			kind = TimerEscalation
		}
		if err := e.store.CreateTimer(&Timer{
			RunID:     run.RunID,
			Kind:      kind,
			FireAt:    e.now().Add(d.Timeout),
			SignalKey: d.SignalKey,
		}); err != nil {
			return err
		}
	}
	e.log.Info("ril_workflow_run_suspended", "run_id", run.RunID, "signal", d.SignalKey, "timeout", d.Timeout.String())
	return nil
}

// failWithCompensation runs saga compensation (each completed step's Compensate,
// in reverse order) then marks the run failed. Compensation is best-effort:
// a compensation error is logged but does not abort the remaining rollbacks.
func (e *Engine) failWithCompensation(ctx context.Context, run *Run, stepDefs []StepDef, cause error) error {
	run.Status = RunCompensating
	run.Error = cause.Error()
	if err := e.store.UpdateRun(run); err != nil {
		return err
	}
	e.log.Warn("ril_workflow_compensating", "run_id", run.RunID, "cause", cause.Error())

	steps, err := e.store.ListSteps(run.RunID)
	if err != nil {
		return err
	}
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if st.Status != StepCompleted {
			continue
		}
		if st.StepIndex < 0 || st.StepIndex >= len(stepDefs) {
			continue
		}
		comp := stepDefs[st.StepIndex].Compensate
		if comp == nil {
			continue
		}
		rc := &RunContext{Run: run, Activities: e.acts}
		if _, cerr := safeRunStep(ctx, comp, rc); cerr != nil {
			e.log.Error("ril_workflow_compensation_failed", "run_id", run.RunID, "step", st.Name, "error", cerr.Error())
			continue
		}
		st.Status = StepCompensated
		if uerr := e.store.UpdateStep(st); uerr != nil {
			e.log.Error("ril_workflow_compensation_persist_failed", "run_id", run.RunID, "step", st.Name, "error", uerr.Error())
		}
	}

	now := e.now()
	run.Status = RunFailed
	run.FinishedAt = &now
	if err := e.store.UpdateRun(run); err != nil {
		return err
	}
	e.log.Warn("ril_workflow_run_failed", "run_id", run.RunID, "error", cause.Error())
	return nil
}

// getOrCreateStep returns the step row for run.CurrentStep, creating a pending
// row (with a stable idempotency key) on first encounter.
func (e *Engine) getOrCreateStep(run *Run, name string) (*Step, error) {
	step, err := e.store.GetStep(run.RunID, run.CurrentStep)
	if err == nil {
		return step, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	step = &Step{
		RunID:          run.RunID,
		StepIndex:      run.CurrentStep,
		Name:           name,
		Status:         StepPending,
		IdempotencyKey: fmt.Sprintf("%s:%d", run.RunID, run.CurrentStep),
	}
	if err := e.store.CreateStep(step); err != nil {
		return nil, err
	}
	return step, nil
}

// consumeSignal extracts and removes a delivered signal from the run context.
func (e *Engine) consumeSignal(run *Run) *Signal {
	if run.Context == nil {
		return nil
	}
	raw, ok := run.Context[signalContextKey]
	if !ok {
		return nil
	}
	delete(run.Context, signalContextKey)
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var sig Signal
	if json.Unmarshal(b, &sig) != nil {
		return nil
	}
	return &sig
}

// safeRunStep invokes a step function, converting a panic into an error so one
// bad step cannot crash the engine/worker.
func safeRunStep(ctx context.Context, fn StepFunc, rc *RunContext) (res StepResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("workflow: step panicked: %v", r)
		}
	}()
	return fn(ctx, rc)
}
