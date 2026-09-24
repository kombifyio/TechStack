package monthlyruntime

import (
	"testing"
	"time"
)

func TestCanTransitionLegalAndIllegal(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
		want bool
	}{
		{"requested->provisioning", StateRequested, StateProvisioning, true},
		{"provisioning->enrolled_pending", StateProvisioning, StateEnrolledPendingAgent, true},
		{"provisioning->stalled", StateProvisioning, StateStalled, true},
		{"enrolled_pending->provisioned", StateEnrolledPendingAgent, StateProvisioned, true},
		{"provisioned->connected", StateProvisioned, StateConnected, true},
		{"connected->degraded", StateConnected, StateDegraded, true},
		{"degraded->connected", StateDegraded, StateConnected, true},
		{"stale->connected", StateStale, StateConnected, true},
		{"stalled->provisioning", StateStalled, StateProvisioning, true},
		{"decommissioning->decommissioned", StateDecommissioning, StateDecommissioned, true},
		{"failed->decommissioning", StateFailed, StateDecommissioning, true},
		{"failed->provisioning(retry)", StateFailed, StateProvisioning, true},
		{"any->decommissioning(requested)", StateRequested, StateDecommissioning, true},
		{"self-connected", StateConnected, StateConnected, true},

		// Illegal transitions.
		{"decommissioned is terminal", StateDecommissioned, StateProvisioning, false},
		{"requested cannot jump to connected", StateRequested, StateConnected, false},
		{"connected cannot go back to requested", StateConnected, StateRequested, false},
		{"provisioned cannot re-enter provisioning", StateProvisioned, StateProvisioning, false},
		{"unknown source rejected", State("bogus"), StateProvisioning, false},
		{"self on unknown rejected", State("bogus"), State("bogus"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransition(%q,%q) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestKnownStateRecognizesLifecycleContract(t *testing.T) {
	for _, state := range []State{
		StateRequested, StateProvisioning, StateEnrolledPendingAgent, StateProvisioned,
		StateConnected, StateDegraded, StateStale, StateStalled,
		StateDecommissioning, StateDecommissioned, StateFailed,
	} {
		if !KnownState(state) {
			t.Errorf("KnownState(%q) = false, want true", state)
		}
	}
	for _, state := range []State{"", "bogus"} {
		if KnownState(state) {
			t.Errorf("KnownState(%q) = true, want false", state)
		}
	}
}

func TestDeriveState(t *testing.T) {
	stalled := EnrollmentStalledThreshold + time.Minute
	fresh := 2 * time.Minute
	cases := []struct {
		name string
		in   DeriveInputs
		want State
	}{
		{"failed", DeriveInputs{EnrollmentStatus: "failed"}, StateFailed},
		{"enrolled+telemetry->connected", DeriveInputs{EnrollmentStatus: "enrolled", HasRuntimeTarget: true, HasTelemetry: true}, StateConnected},
		{"enrolled+target-no-telemetry->provisioned", DeriveInputs{EnrollmentStatus: "enrolled", HasRuntimeTarget: true}, StateProvisioned},
		{"enrolled-no-target->pending-agent", DeriveInputs{EnrollmentStatus: "enrolled"}, StateEnrolledPendingAgent},
		{"enrolled+stopped->provisioned", DeriveInputs{EnrollmentStatus: "enrolled", DesiredStopped: true, HasTelemetry: true}, StateProvisioned},
		{"retrying-fresh->degraded", DeriveInputs{EnrollmentStatus: "retrying", EnrollmentAge: fresh}, StateDegraded},
		{"retrying-aged->stalled", DeriveInputs{EnrollmentStatus: "retrying", EnrollmentAge: stalled}, StateStalled},
		{"pending-fresh->provisioning", DeriveInputs{EnrollmentStatus: "pending", EnrollmentAge: fresh}, StateProvisioning},
		{"pending-aged->stalled", DeriveInputs{EnrollmentStatus: "pending", EnrollmentAge: stalled}, StateStalled},
		{"pending-unknown-age->provisioning", DeriveInputs{EnrollmentStatus: "pending"}, StateProvisioning},
		{"empty->provisioning", DeriveInputs{}, StateProvisioning},
		{"unknown-status->provisioning", DeriveInputs{EnrollmentStatus: "weird"}, StateProvisioning},
		{"case-insensitive enrolled", DeriveInputs{EnrollmentStatus: " ENROLLED ", HasRuntimeTarget: true, HasTelemetry: true}, StateConnected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveState(tc.in); got != tc.want {
				t.Fatalf("DeriveState(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsEnrollmentStalledBoundary(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"unknown-age", 0, false},
		{"just-below", EnrollmentStalledThreshold - time.Second, false},
		{"at-threshold", EnrollmentStalledThreshold, true},
		{"above", EnrollmentStalledThreshold + time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEnrollmentStalled(tc.age); got != tc.want {
				t.Fatalf("IsEnrollmentStalled(%s) = %v, want %v", tc.age, got, tc.want)
			}
		})
	}
}
