package serviceregistry

import "testing"

func TestCanonicalStatesMatchTheStackKitsVocabulary(t *testing.T) {
	for _, test := range []struct {
		value        string
		wantDesired  DesiredState
		wantObserved ObservedState
		wantHealth   HealthState
	}{
		{value: "running", wantDesired: DesiredRunning, wantObserved: ObservedRunning, wantHealth: HealthUnknown},
		{value: "stopped", wantDesired: DesiredStopped, wantObserved: ObservedStopped, wantHealth: HealthUnknown},
		{value: "healthy", wantDesired: DesiredRunning, wantObserved: ObservedRunning, wantHealth: HealthHealthy},
		{value: "unhealthy", wantDesired: DesiredRunning, wantObserved: ObservedError, wantHealth: HealthUnhealthy},
		// `reachable` proves the service answers, not that a probe passed.
		{value: "reachable", wantDesired: DesiredRunning, wantObserved: ObservedRunning, wantHealth: HealthStarting},
		{value: "restarting", wantDesired: DesiredRunning, wantObserved: ObservedStarting, wantHealth: HealthStarting},
		{value: "not-required", wantDesired: DesiredRunning, wantObserved: ObservedUnknown, wantHealth: HealthNotRequired},
		{value: "  ", wantDesired: DesiredRunning, wantObserved: ObservedUnknown, wantHealth: HealthUnknown},
		{value: "banana", wantDesired: DesiredRunning, wantObserved: ObservedUnknown, wantHealth: HealthUnknown},
	} {
		t.Run(test.value, func(t *testing.T) {
			if got := CanonicalDesiredState(test.value); got != test.wantDesired {
				t.Fatalf("desired = %q, want %q", got, test.wantDesired)
			}
			if got := CanonicalObservedState(test.value); got != test.wantObserved {
				t.Fatalf("observed = %q, want %q", got, test.wantObserved)
			}
			if got := CanonicalHealthState(test.value); got != test.wantHealth {
				t.Fatalf("health = %q, want %q", got, test.wantHealth)
			}
		})
	}
}

func TestCanonicalStatesAlwaysValidate(t *testing.T) {
	for _, value := range []string{"running", "reachable", "banana", "", "ARCHIVED", "not_required"} {
		if err := ValidateDesiredState(string(CanonicalDesiredState(value))); err != nil {
			t.Fatalf("desired %q: %v", value, err)
		}
		if err := ValidateObservedState(string(CanonicalObservedState(value))); err != nil {
			t.Fatalf("observed %q: %v", value, err)
		}
		if err := ValidateHealthState(string(CanonicalHealthState(value))); err != nil {
			t.Fatalf("health %q: %v", value, err)
		}
	}
}

func TestValidateRejectsValuesOutsideTheVocabulary(t *testing.T) {
	if ValidateDesiredState("absent") == nil {
		t.Fatal("desired state absent must be rejected")
	}
	if ValidateObservedState("reachable") == nil {
		t.Fatal("observed state reachable must be rejected")
	}
	if ValidateHealthState("degraded") == nil {
		t.Fatal("health state degraded must be rejected")
	}
}

func TestDeriveStatusIsTheObservedProjection(t *testing.T) {
	if got := DeriveStatus(ObservedRunning); got != "running" {
		t.Fatalf("status = %q, want running", got)
	}
	// A running service with a failing probe still reports a running status.
	if DeriveStatus(ObservedRunning) == string(HealthUnhealthy) {
		t.Fatal("status must never be the health value")
	}
	if got := DeriveStatus(ObservedState("banana")); got != "unknown" {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestRetainedWorkflowStatePrefersTheTerminalValue(t *testing.T) {
	if got := RetainedWorkflowState("", "running"); got != "" {
		t.Fatalf("retained = %q, want none", got)
	}
	if got := RetainedWorkflowState("migrating", " Archived "); got != "archived" {
		t.Fatalf("retained = %q, want archived", got)
	}
	if got := RetainedWorkflowState("", "pending_verification"); got != "pending_verification" {
		t.Fatalf("retained = %q, want pending_verification", got)
	}
	if !IsControlPlaneWorkflowState("deploying") || IsControlPlaneWorkflowState("running") {
		t.Fatal("control-plane workflow detection is wrong")
	}
}
