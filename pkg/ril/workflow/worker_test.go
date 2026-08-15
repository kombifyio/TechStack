package workflow

import (
	"context"
	"testing"
	"time"
)

func TestWorker_SweepTimers_FiresEscalation(t *testing.T) {
	eng := newTestEngine(t)
	w := NewWorker(eng, WorkerConfig{Batch: 10})

	var resumedTimedOut bool
	gate := StepDef{Name: "await", Run: func(_ context.Context, rc *RunContext) (StepResult, error) {
		if rc.Signal == nil {
			// suspend WITHOUT an inline timeout; the test arms the timer manually
			return StepResult{Suspend: &SuspendDirective{SignalKey: "esc:test"}}, nil
		}
		resumedTimedOut = rc.Signal.TimedOut
		return ok(nil)
	}}
	eng.Register(testWF{typ: TypeActionCardRemediation, policy: DefaultRetryPolicy(), steps: []StepDef{gate}})

	run := &Run{Type: TypeActionCardRemediation, OwnerID: "u"}
	runID, err := eng.StartRun(context.Background(), run)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got, _ := eng.store.GetRun(runID); got.Status != RunSuspended {
		t.Fatalf("status = %s, want suspended", got.Status)
	}

	// Arm a timer that is already due, targeting the awaited signal.
	if err := eng.store.CreateTimer(&Timer{
		RunID:     runID,
		Kind:      TimerEscalation,
		FireAt:    time.Now().Add(-time.Minute).UTC(),
		SignalKey: "esc:test",
	}); err != nil {
		t.Fatalf("CreateTimer: %v", err)
	}

	// Sweep: the due timer should resume the run with a timed-out signal.
	w.SweepTimers(context.Background())

	got, _ := eng.store.GetRun(runID)
	if got.Status != RunCompleted {
		t.Fatalf("status after sweep = %s, want completed", got.Status)
	}
	if !resumedTimedOut {
		t.Error("resumed step should have seen Signal.TimedOut = true")
	}
	// Timer should be marked fired (no longer due).
	due, _ := eng.store.ListDueTimers(time.Now().UTC(), 0)
	if len(due) != 0 {
		t.Errorf("due timers after sweep = %d, want 0", len(due))
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
