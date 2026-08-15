package workflow

import "testing"

func TestValidateRunTransition_Allowed(t *testing.T) {
	allowed := []struct{ from, to RunStatus }{
		{RunPending, RunRunning},
		{RunPending, RunCancelled},
		{RunRunning, RunSuspended},
		{RunRunning, RunCompleted},
		{RunRunning, RunFailed},
		{RunRunning, RunCompensating},
		{RunSuspended, RunRunning},
		{RunSuspended, RunFailed},
		{RunCompensating, RunFailed},
	}
	for _, tc := range allowed {
		if err := ValidateRunTransition(tc.from, tc.to); err != nil {
			t.Errorf("expected %s->%s allowed, got %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateRunTransition_Rejected(t *testing.T) {
	rejected := []struct{ from, to RunStatus }{
		{RunCompleted, RunRunning},   // terminal
		{RunFailed, RunRunning},      // terminal
		{RunCancelled, RunRunning},   // terminal
		{RunPending, RunCompleted},   // must run first
		{RunSuspended, RunCompleted}, // must resume to running first
		{RunRunning, RunRunning},     // no-op
	}
	for _, tc := range rejected {
		if err := ValidateRunTransition(tc.from, tc.to); err == nil {
			t.Errorf("expected %s->%s rejected, got nil", tc.from, tc.to)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []RunStatus{RunCompleted, RunFailed, RunCancelled}
	for _, s := range terminal {
		if !IsTerminal(s) {
			t.Errorf("expected %s terminal", s)
		}
	}
	nonTerminal := []RunStatus{RunPending, RunRunning, RunSuspended, RunCompensating}
	for _, s := range nonTerminal {
		if IsTerminal(s) {
			t.Errorf("expected %s non-terminal", s)
		}
	}
}

func TestValidateStepTransition(t *testing.T) {
	allowed := []struct{ from, to StepStatus }{
		{StepPending, StepRunning},
		{StepPending, StepSkipped},
		{StepRunning, StepCompleted},
		{StepRunning, StepFailed},
		{StepFailed, StepRunning}, // retry
		{StepFailed, StepCompensated},
		{StepCompleted, StepCompensated}, // saga rollback
	}
	for _, tc := range allowed {
		if err := ValidateStepTransition(tc.from, tc.to); err != nil {
			t.Errorf("expected step %s->%s allowed, got %v", tc.from, tc.to, err)
		}
	}
	rejected := []struct{ from, to StepStatus }{
		{StepCompleted, StepRunning},
		{StepSkipped, StepRunning},
		{StepCompleted, StepFailed},
		{StepRunning, StepRunning}, // no-op
	}
	for _, tc := range rejected {
		if err := ValidateStepTransition(tc.from, tc.to); err == nil {
			t.Errorf("expected step %s->%s rejected, got nil", tc.from, tc.to)
		}
	}
}
