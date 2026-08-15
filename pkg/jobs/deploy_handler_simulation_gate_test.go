package jobs

import "testing"

// A managed cloud rollout used to hard-require the simulate gate, which made
// every one of them impossible: #265 retired the simulate bridge and
// scripts/render-environment-contract.mjs lists TECHSTACK_KOMBISIM_URL under
// retiredLiveEnvironmentKeys, so the live service has no such variable by
// design. Observed on techstack.kombify.io:
//
//	Phase: generate 60% reason: managed cloud simulation gate is not configured
func TestRunSimulationGateSkipsAnAbsentGateForAManagedRuntime(t *testing.T) {
	job := &Job{ID: "job-1", TargetID: "stack-1"}
	rollout := &deployRollout{
		job:            job,
		cfg:            &ProvisionConfig{},
		managedRuntime: true,
	}

	if err := rollout.runSimulationGate(t.Context()); err != nil {
		t.Fatalf("runSimulationGate: %v", err)
	}
	if got := job.Result[simulationGateStatusField]; got != simulationGateNotConfigured {
		t.Fatalf("%s = %v, want %q", simulationGateStatusField, got, simulationGateNotConfigured)
	}
}
