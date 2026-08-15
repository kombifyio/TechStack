package runtimeconvergence

import (
	"errors"
	"testing"
	"time"
)

func TestConvergenceSnapshotAggregatesAndRejectsRawErrors(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	degraded := Aggregate(now,
		Component{Name: TechstackRuntimeComponent, State: ComponentReady, ObservedAt: now},
		Component{Name: StackKitsRuntimeComponent, State: ComponentFailed, ObservedAt: now, ErrorCode: StackKitsRuntimeUnavailableError},
	)
	normalized, err := Normalize(degraded)
	if err != nil {
		t.Fatalf("Normalize degraded convergence: %v", err)
	}
	if normalized.State != StateDegraded || normalized.ErrorCode != ConvergenceIncompleteError {
		t.Fatalf("degraded state = %#v", normalized)
	}
	if got := Map(normalized); got == nil || got["state"] != StateDegraded {
		t.Fatalf("canonical map = %#v", got)
	}

	degraded.Components[1].ErrorCode = "dial tcp 10.0.0.4:443: connection refused"
	if _, err := Normalize(degraded); !errors.Is(err, ErrInvalid) {
		t.Fatalf("raw executor error was accepted: %v", err)
	}
}

func TestTrackerPublishesWholeSnapshotsAtomically(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 1, 0, 0, time.UTC)
	tracker := NewTracker(now)
	initial := tracker.Snapshot()
	if initial.State != StatePending || componentState(initial, TechstackRuntimeComponent) != ComponentPending || componentState(initial, StackKitsRuntimeComponent) != ComponentPending {
		t.Fatalf("initial tracker state = %#v", initial)
	}
	ready := Aggregate(now.Add(time.Second),
		Component{Name: TechstackRuntimeComponent, State: ComponentReady, ObservedAt: now.Add(time.Second)},
		Component{Name: StackKitsRuntimeComponent, State: ComponentReady, ObservedAt: now.Add(time.Second)},
	)
	tracker.Set(ready)
	got := tracker.Snapshot()
	if got.State != StateReady || !got.ObservedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("published state = %#v", got)
	}
	ready.Components[0].State = ComponentFailed
	if tracker.Snapshot().Components[0].State != ComponentReady {
		t.Fatal("tracker exposed caller mutation")
	}
}

func componentState(snapshot Snapshot, name string) string {
	for _, component := range snapshot.Components {
		if component.Name == name {
			return component.State
		}
	}
	return ""
}
