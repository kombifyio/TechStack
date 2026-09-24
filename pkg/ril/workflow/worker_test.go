package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failingSignalStore struct{ RunStore }

func (failingSignalStore) FindSuspendedBySignal(string) (*Run, error) {
	return nil, errors.New("transient store failure")
}

func TestWorker_SweepTimers_ResumesDurableTimerAfterRestart(t *testing.T) {
	eng := newTestEngine(t)
	started := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	eng.now = func() time.Time { return started }

	var resumedTimedOut bool
	gate := StepDef{Name: "await", Run: func(_ context.Context, rc *RunContext) (StepResult, error) {
		if rc.Signal == nil {
			return StepResult{Suspend: &SuspendDirective{SignalKey: "esc:test", Timeout: time.Minute, TimerKind: TimerReminder}}, nil
		}
		resumedTimedOut = rc.Signal.TimedOut
		return ok(nil)
	}}
	definition := testWF{typ: TypeActionCardRemediation, policy: DefaultRetryPolicy(), steps: []StepDef{gate}}
	eng.Register(definition)

	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got, _ := eng.store.GetRun(runID); got.Status != RunSuspended {
		t.Fatalf("status = %s, want suspended", got.Status)
	}

	// Reconstruct the engine over the same durable store, as process startup
	// does, then sweep after the persisted timer is due.
	restarted := NewEngine(eng.store, NewActivityRunner())
	restarted.Register(definition)
	restarted.now = func() time.Time { return started.Add(2 * time.Minute) }
	w := NewWorker(restarted, WorkerConfig{Batch: 10})
	w.SweepTimers(context.Background())

	got, _ := restarted.store.GetRun(runID)
	if got.Status != RunCompleted {
		t.Fatalf("status after sweep = %s, want completed", got.Status)
	}
	if !resumedTimedOut {
		t.Error("resumed step should have seen Signal.TimedOut = true")
	}
	// Timer should be marked fired (no longer due).
	due, _ := restarted.store.ListDueTimers(started.Add(2*time.Minute), 0)
	if len(due) != 0 {
		t.Errorf("due timers after sweep = %d, want 0", len(due))
	}
}

func TestWorker_SweepTimers_RetriesTimerAfterDeliveryFailure(t *testing.T) {
	store := newFakeStore(t)
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if err := store.CreateTimer(&Timer{
		RunID: "run-1", Kind: TimerEscalation, FireAt: now.Add(-time.Minute), SignalKey: "esc:test",
	}); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(failingSignalStore{RunStore: store}, NewActivityRunner())
	eng.now = func() time.Time { return now }
	NewWorker(eng, WorkerConfig{Batch: 10}).SweepTimers(context.Background())

	due, err := store.ListDueTimers(now, 0)
	if err != nil || len(due) != 1 {
		t.Fatalf("due timers after delivery failure = %d, err=%v; want one retryable timer", len(due), err)
	}
}

func TestWorker_PollRuns_AdvancesPending(t *testing.T) {
	eng := newTestEngine(t)
	w := NewWorker(eng, WorkerConfig{Batch: 10})

	var ran bool
	eng.Register(testWF{
		typ:    TypeDriftCorrection,
		policy: DefaultRetryPolicy(),
		steps: []StepDef{{Name: "only", Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
			ran = true
			return ok(nil)
		}}},
	})

	// Create a run directly in pending WITHOUT advancing it (simulates a run
	// enqueued for the worker, or one left pending by a crash before advance).
	run := &Run{Type: TypeDriftCorrection, OwnerID: "u"}
	if err := eng.store.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if got, _ := eng.store.GetRun(run.RunID); got.Status != RunPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}

	// The poll loop should pick it up and drive it to completion.
	w.PollRuns(context.Background())

	if !ran {
		t.Error("poll did not execute the pending run's step")
	}
	got, _ := eng.store.GetRun(run.RunID)
	if got.Status != RunCompleted {
		t.Errorf("status after poll = %s, want completed", got.Status)
	}
}

func TestWorker_PollRuns_RunsBackgroundTaskWithoutBlockingPendingRuns(t *testing.T) {
	eng := newTestEngine(t)
	w := NewWorker(eng, WorkerConfig{Batch: 10})
	backgroundCalls := 0
	eng.SetBackgroundTask(func(context.Context) error {
		backgroundCalls++
		return context.DeadlineExceeded
	})

	var ran bool
	eng.Register(testWF{
		typ:    TypeDriftCorrection,
		policy: DefaultRetryPolicy(),
		steps: []StepDef{{Name: "only", Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
			ran = true
			return ok(nil)
		}}},
	})
	run := &Run{Type: TypeDriftCorrection, OwnerID: "u"}
	if err := eng.store.CreateRun(run); err != nil {
		t.Fatal(err)
	}

	w.PollRuns(context.Background())
	if backgroundCalls != 1 {
		t.Fatalf("background calls = %d, want 1", backgroundCalls)
	}
	if !ran {
		t.Fatal("pending workflow was blocked by a failed background task")
	}
}

func TestWorker_PollRuns_AdvancesEnqueuedProviderIncident(t *testing.T) {
	eng := newTestEngine(t)
	w := NewWorker(eng, WorkerConfig{Batch: 10})
	var advanced bool
	eng.Register(testWF{
		typ:    TypeProviderIncidentAdvisory,
		policy: DefaultRetryPolicy(),
		steps: []StepDef{{Name: "advisory", Run: func(_ context.Context, _ *RunContext) (StepResult, error) {
			advanced = true
			return ok(nil)
		}}},
	})
	run := &Run{RunID: "provider-incident-stable", Type: TypeProviderIncidentAdvisory, OwnerID: "tenant-a"}
	if err := eng.store.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	w.PollRuns(context.Background())
	if !advanced {
		t.Fatal("worker did not advance the enqueued provider incident run")
	}
	got, _ := eng.store.GetRun(run.RunID)
	if got.Status != RunCompleted {
		t.Fatalf("status after worker poll = %s, want completed", got.Status)
	}
}

func TestNewWorker_Defaults(t *testing.T) {
	eng := newTestEngine(t)
	w := NewWorker(eng, WorkerConfig{}) // all zero -> defaults
	if w.cfg.PollInterval <= 0 || w.cfg.TimerInterval <= 0 || w.cfg.Batch <= 0 {
		t.Errorf("zero config not defaulted: %+v", w.cfg)
	}
}
