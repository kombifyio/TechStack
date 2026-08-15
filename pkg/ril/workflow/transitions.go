package workflow

import "fmt"

// runTransition is a status change for a run.
type runTransition struct {
	from RunStatus
	to   RunStatus
}

// validRunTransitions defines the allowed run state machine:
//
//	pending      -> running | cancelled
//	running      -> suspended | completed | failed | compensating | cancelled
//	suspended    -> running | cancelled | failed
//	compensating -> failed
var validRunTransitions = map[runTransition]bool{
	{RunPending, RunRunning}:      true,
	{RunPending, RunCancelled}:    true,
	{RunRunning, RunSuspended}:    true,
	{RunRunning, RunCompleted}:    true,
	{RunRunning, RunFailed}:       true,
	{RunRunning, RunCompensating}: true,
	{RunRunning, RunCancelled}:    true,
	{RunSuspended, RunRunning}:    true,
	{RunSuspended, RunCancelled}:  true,
	{RunSuspended, RunFailed}:     true,
	{RunCompensating, RunFailed}:  true,
}

// ValidateRunTransition returns an error if the run status change is not allowed.
func ValidateRunTransition(from, to RunStatus) error {
	if from == to {
		return fmt.Errorf("workflow: no-op run transition: already %s", from)
	}
	if !validRunTransitions[runTransition{from, to}] {
		return fmt.Errorf("workflow: invalid run transition: %s -> %s", from, to)
	}
	return nil
}

// IsTerminal reports whether a run status is final (no further transitions).
func IsTerminal(s RunStatus) bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// stepTransition is a status change for a step.
type stepTransition struct {
	from StepStatus
	to   StepStatus
}

// validStepTransitions defines the allowed step state machine:
//
//	pending   -> running | skipped
//	running   -> completed | failed
//	failed    -> running (retry) | compensated
//	completed -> compensated (saga rollback)
var validStepTransitions = map[stepTransition]bool{
	{StepPending, StepRunning}:       true,
	{StepPending, StepSkipped}:       true,
	{StepRunning, StepCompleted}:     true,
	{StepRunning, StepFailed}:        true,
	{StepFailed, StepRunning}:        true, // retry
	{StepFailed, StepCompensated}:    true,
	{StepCompleted, StepCompensated}: true, // saga rollback
}

// ValidateStepTransition returns an error if the step status change is not allowed.
func ValidateStepTransition(from, to StepStatus) error {
	if from == to {
		return fmt.Errorf("workflow: no-op step transition: already %s", from)
	}
	if !validStepTransitions[stepTransition{from, to}] {
		return fmt.Errorf("workflow: invalid step transition: %s -> %s", from, to)
	}
	return nil
}
