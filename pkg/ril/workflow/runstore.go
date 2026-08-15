package workflow

import (
	"errors"
	"time"
)

// ErrNotFound is returned when a run/step/timer does not exist.
var ErrNotFound = errors.New("workflow: not found")

// RunStore is the durable persistence surface the Engine and Worker depend on.
// The active implementation is *PgStore (pg_store.go). Method semantics are
// documented there.
type RunStore interface {
	// Runs
	CreateRun(run *Run) error
	GetRun(runID string) (*Run, error)
	UpdateRun(run *Run) error
	ListRunsByStatus(status RunStatus, limit int) ([]*Run, error)
	FindSuspendedBySignal(signalKey string) (*Run, error)

	// Steps
	CreateStep(step *Step) error
	UpdateStep(step *Step) error
	GetStep(runID string, idx int) (*Step, error)
	ListSteps(runID string) ([]*Step, error)

	// Timers
	CreateTimer(timer *Timer) error
	ListDueTimers(now time.Time, limit int) ([]*Timer, error)
	MarkTimerFired(timerID string) error
}
