package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// testWF is a configurable workflow definition for engine tests.
type testWF struct {
	typ    RunType
	steps  []StepDef
	policy RetryPolicy
}

func (w testWF) Type() RunType            { return w.typ }
func (w testWF) Steps() []StepDef         { return w.steps }
func (w testWF) RetryPolicy() RetryPolicy { return w.policy }

// newTestEngine builds an engine over an in-memory fake store with a no-op
// sleep so retry backoff does not slow tests.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng := NewEngine(newFakeStore(t), NewActivityRunner())
	eng.sleep = func(time.Duration) {}
	return eng
}

func ok(out map[string]any) (StepResult, error) { return StepResult{Output: out}, nil }

func TestEngine_HappyPath(t *testing.T) {
	eng := newTestEngine(t)
	var mu sync.Mutex
	counts := map[string]int{}
	mk := func(name string) StepDef {
		return StepDef{Name: name, Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
			return ok(map[string]any{"step": name})
		}}
	}
	eng.Register(testWF{
		typ:    TypeActionCardRemediation,
		policy: DefaultRetryPolicy(),
		steps:  []StepDef{mk("a"), mk("b"), mk("c")},
	})

	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	got, _ := eng.store.GetRun(runID)
	if got.Status != RunCompleted {
		t.Fatalf("status = %s, want completed", got.Status)
	}
	if got.CurrentStep != 3 {
		t.Errorf("current_step = %d, want 3", got.CurrentStep)
	}
	for _, n := range []string{"a", "b", "c"} {
		if counts[n] != 1 {
			t.Errorf("step %s ran %d times, want 1", n, counts[n])
		}
	}
	steps, _ := eng.store.ListSteps(runID)
	if len(steps) != 3 {
		t.Fatalf("steps persisted = %d, want 3", len(steps))
	}
	for _, s := range steps {
		if s.Status != StepCompleted {
			t.Errorf("step %s status = %s, want completed", s.Name, s.Status)
		}
	}
}

func TestEngine_SuspendAndResume(t *testing.T) {
	eng := newTestEngine(t)
	var mu sync.Mutex
	counts := map[string]int{}
	var gotSignal *Signal

	approval := StepDef{Name: "await_approval", Run: func(_ context.Context, rc *RunContext) (StepResult, error) {
		mu.Lock()
		counts["await_approval"]++
		mu.Unlock()
		if rc.Signal == nil {
			// first entry: suspend awaiting approval
			return StepResult{Suspend: &SuspendDirective{SignalKey: "approval:r1", Timeout: time.Hour}}, nil
		}
		// resumed: process the approval signal
		gotSignal = rc.Signal
		return ok(map[string]any{"approved": rc.Signal.Payload["approved"]})
	}}
	mk := func(name string) StepDef {
		return StepDef{Name: name, Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
			return ok(nil)
		}}
	}
	eng.Register(testWF{
		typ:    TypeDriftCorrection,
		policy: DefaultRetryPolicy(),
		steps:  []StepDef{mk("pre"), approval, mk("post")},
	})

	run := &Run{Type: TypeDriftCorrection, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Should be suspended at the approval step.
	got, _ := eng.store.GetRun(runID)
	if got.Status != RunSuspended {
		t.Fatalf("status = %s, want suspended", got.Status)
	}
	if got.AwaitingSignal != "approval:r1" {
		t.Errorf("awaiting_signal = %q, want approval:r1", got.AwaitingSignal)
	}
	// An escalation timer should have been armed.
	timers, _ := eng.store.ListDueTimers(time.Now().Add(2*time.Hour), 0)
	if len(timers) != 1 {
		t.Errorf("armed timers = %d, want 1", len(timers))
	}

	// Deliver the approval.
	if err := eng.Deliver(context.Background(), Signal{Key: "approval:r1", Payload: map[string]any{"approved": true}}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	got, _ = eng.store.GetRun(runID)
	if got.Status != RunCompleted {
		t.Fatalf("status after deliver = %s, want completed", got.Status)
	}
	if gotSignal == nil || gotSignal.Payload["approved"] != true {
		t.Errorf("resumed step did not see approval signal: %+v", gotSignal)
	}
	if counts["pre"] != 1 || counts["post"] != 1 {
		t.Errorf("pre/post ran %d/%d, want 1/1 (no re-run across suspend)", counts["pre"], counts["post"])
	}
	if counts["await_approval"] != 2 {
		t.Errorf("approval step ran %d times, want 2 (suspend + resume)", counts["await_approval"])
	}
}

func TestEngine_RetryThenSucceed(t *testing.T) {
	eng := newTestEngine(t)
	var attempts int
	flaky := StepDef{Name: "flaky", Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
		attempts++
		if attempts < 3 {
			return StepResult{}, errors.New("transient")
		}
		return ok(map[string]any{"ok": true})
	}}
	eng.Register(testWF{
		typ:    TypeCertRotation,
		policy: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, Multiplier: 2},
		steps:  []StepDef{flaky},
	})

	run := &Run{Type: TypeCertRotation, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if attempts != 3 {
		t.Errorf("executions = %d, want 3 (2 failures + success)", attempts)
	}
	got, _ := eng.store.GetRun(runID)
	if got.Status != RunCompleted {
		t.Fatalf("status = %s, want completed", got.Status)
	}
	step, _ := eng.store.GetStep(runID, 0)
	if step.Attempt != 2 {
		t.Errorf("step attempt (failures) = %d, want 2", step.Attempt)
	}
}

func TestEngine_FailWithCompensation(t *testing.T) {
	eng := newTestEngine(t)
	var compensated int
	prep := StepDef{
		Name: "prep",
		Run:  func(_ context.Context, _ *RunContext) (StepResult, error) { return ok(nil) },
		Compensate: func(_ context.Context, _ *RunContext) (StepResult, error) {
			compensated++
			return ok(nil)
		},
	}
	boom := StepDef{Name: "boom", Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
		return StepResult{}, errors.New("unrecoverable")
	}}
	eng.Register(testWF{
		typ:    TypeRollingUpdate,
		policy: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, Multiplier: 2},
		steps:  []StepDef{prep, boom},
	})

	run := &Run{Type: TypeRollingUpdate, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	got, _ := eng.store.GetRun(runID)
	if got.Status != RunFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("expected run error to be recorded")
	}
	if compensated != 1 {
		t.Errorf("prep compensated %d times, want 1", compensated)
	}
	prepStep, _ := eng.store.GetStep(runID, 0)
	if prepStep.Status != StepCompensated {
		t.Errorf("prep step status = %s, want compensated", prepStep.Status)
	}
	boomStep, _ := eng.store.GetStep(runID, 1)
	if boomStep.Status != StepFailed {
		t.Errorf("boom step status = %s, want failed", boomStep.Status)
	}
}

// TestEngine_RestartResume proves a fresh engine (simulating a process restart)
// resumes a suspended run from its checkpoint without re-running completed steps.
func TestEngine_RestartResume(t *testing.T) {
	store := newFakeStore(t)
	var mu sync.Mutex
	counts := map[string]int{}

	build := func() *Engine {
		eng := NewEngine(store, NewActivityRunner())
		eng.sleep = func(time.Duration) {}
		mk := func(name string) StepDef {
			return StepDef{Name: name, Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
				mu.Lock()
				counts[name]++
				mu.Unlock()
				return ok(nil)
			}}
		}
		gate := StepDef{Name: "gate", Run: func(_ context.Context, rc *RunContext) (StepResult, error) {
			mu.Lock()
			counts["gate"]++
			mu.Unlock()
			if rc.Signal == nil {
				return StepResult{Suspend: &SuspendDirective{SignalKey: "sig:restart"}}, nil
			}
			return ok(nil)
		}}
		eng.Register(testWF{typ: TypeActionCardRemediation, policy: DefaultRetryPolicy(),
			steps: []StepDef{mk("first"), gate, mk("last")}})
		return eng
	}

	// Engine #1 starts the run; it suspends at the gate.
	eng1 := build()
	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	runID, err := eng1.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got, _ := eng1.store.GetRun(runID); got.Status != RunSuspended {
		t.Fatalf("status = %s, want suspended", got.Status)
	}

	// Engine #2 (fresh instance, same DB) delivers the signal and resumes.
	eng2 := build()
	if err := eng2.Deliver(context.Background(), Signal{Key: "sig:restart"}); err != nil {
		t.Fatalf("Deliver on fresh engine: %v", err)
	}
	if got, _ := eng2.store.GetRun(runID); got.Status != RunCompleted {
		t.Fatalf("status after resume = %s, want completed", got.Status)
	}
	// "first" must have run exactly once despite the restart.
	if counts["first"] != 1 {
		t.Errorf("first ran %d times across restart, want 1", counts["first"])
	}
	if counts["last"] != 1 {
		t.Errorf("last ran %d times, want 1", counts["last"])
	}
}

func TestEngine_ActivityIdempotencyKey(t *testing.T) {
	eng := newTestEngine(t)
	var gotIdem string
	eng.acts.Register("record", func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		gotIdem = IdempotencyKeyFromContext(ctx)
		return map[string]any{"done": true}, nil
	})
	// The runner must thread the step-provided idempotency key into the activity
	// context so side-effecting activities can dedup across retries.
	stepWithKey := StepDef{Name: "call_activity", Run: func(ctx context.Context, rc *RunContext) (StepResult, error) {
		out, err := rc.Activities.Run(ctx, "record", map[string]any{"x": 1}, "idem-xyz")
		return StepResult{Output: out}, err
	}}
	eng.Register(testWF{typ: TypeActionCardRemediation, policy: DefaultRetryPolicy(), steps: []StepDef{stepWithKey}})

	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	if _, err := eng.StartRun(context.Background(), run); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if gotIdem != "idem-xyz" {
		t.Errorf("activity idempotency key = %q, want idem-xyz", gotIdem)
	}
}
