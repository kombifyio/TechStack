package providercontroljobs

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func decommissionRecord(status providerexecutor.Status, phase providerexecutor.Phase) providercontrol.OperationRecord {
	return providercontrol.OperationRecord{
		Command: providerexecutor.Command{Operation: providerexecutor.OperationDecommission},
		Head:    providerexecutor.Receipt{Status: status, Phase: phase},
	}
}

// Success is not the only terminal state. Treating it as the only one is what
// turned a failed decommission into an infinite job: the caller saw "not yet
// converged", returned a JobWaitError, and the retry re-entered Start with the
// same at-most-once idempotency key, which returned the same failed record.
//
// In production that job polled every five seconds indefinitely and blocked
// every other stack execution behind it with waiting_stack_execution, so one
// dead operation stalled the whole queue.
func TestTerminallyFailedDecommissionIsRecognised(t *testing.T) {
	failed := decommissionRecord(providerexecutor.StatusFailed, providerexecutor.PhaseFailed)
	if !nativeDecommissionTerminallyFailed(failed) {
		t.Fatal("a failed decommission was not recognised as terminally failed; the job will wait forever")
	}
	if nativeDecommissionTerminal(failed) {
		t.Fatal("a failed decommission must not be mistaken for proven absence")
	}
}

// Still-converging states must keep waiting: ending the job early would abandon
// a delete the provider is genuinely still processing.
func TestConvergingDecommissionIsNeitherTerminalNorFailed(t *testing.T) {
	for name, record := range map[string]providercontrol.OperationRecord{
		"delete accepted": decommissionRecord(
			providerexecutor.StatusPending, providerexecutor.PhaseDeleteAccepted),
		"absence pending": decommissionRecord(
			providerexecutor.StatusPending, providerexecutor.PhaseAbsencePending),
		"requested": decommissionRecord(
			providerexecutor.StatusPending, providerexecutor.PhaseRequested),
	} {
		t.Run(name, func(t *testing.T) {
			if nativeDecommissionTerminal(record) {
				t.Fatal("a converging decommission was treated as proven absence")
			}
			if nativeDecommissionTerminallyFailed(record) {
				t.Fatal("a converging decommission was abandoned as failed")
			}
		})
	}
}

// Proven absence stays the single success condition.
func TestProvenAbsenceRemainsTheOnlySuccess(t *testing.T) {
	success := decommissionRecord(providerexecutor.StatusSucceeded, providerexecutor.PhaseAbsent)
	if !nativeDecommissionTerminal(success) {
		t.Fatal("proven absence was not terminal")
	}
	if nativeDecommissionTerminallyFailed(success) {
		t.Fatal("proven absence was reported as a failure")
	}
}

// A failed operation belonging to a different operation kind must not be
// mistaken for this lease's decommission outcome.
func TestFailedProvisionIsNotADecommissionFailure(t *testing.T) {
	record := providercontrol.OperationRecord{
		Command: providerexecutor.Command{Operation: providerexecutor.OperationProvision},
		Head: providerexecutor.Receipt{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
		},
	}
	if nativeDecommissionTerminallyFailed(record) {
		t.Fatal("a failed provision was treated as a failed decommission")
	}
}

func TestRetryableDecommissionFailureSuspendsTheDestroyJob(t *testing.T) {
	candidate := retryCandidate(0)
	candidate.ServerID = "server-1"
	record := decommissionRecord(providerexecutor.StatusFailed, providerexecutor.PhaseFailed)
	record.Command.OperationID = "op-first-failed"

	err := nativeDecommissionFailure(candidate, record)
	var waitErr *jobs.JobWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error = %T %v, want JobWaitError", err, err)
	}
	if !errors.Is(err, ErrNativeDecommissionFailed) {
		t.Fatalf("error = %v, want ErrNativeDecommissionFailed cause", err)
	}
}

func TestLastDecommissionFailureExhaustsTheAttemptBudget(t *testing.T) {
	candidate := retryCandidate(maxNativeDecommissionAttempts - 1)
	candidate.ServerID = "server-1"
	record := decommissionRecord(providerexecutor.StatusFailed, providerexecutor.PhaseFailed)
	record.Command.OperationID = "op-last-failed"

	err := nativeDecommissionFailure(candidate, record)
	var waitErr *jobs.JobWaitError
	if errors.As(err, &waitErr) {
		t.Fatalf("last error = %v, must not schedule a fifth provider attempt", err)
	}
	if !errors.Is(err, ErrNativeDecommissionAttemptsExhausted) {
		t.Fatalf("error = %v, want ErrNativeDecommissionAttemptsExhausted", err)
	}
}
