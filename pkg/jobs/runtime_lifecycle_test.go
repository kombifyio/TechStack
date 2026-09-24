package jobs

import (
	"testing"
	"time"
)

func TestRuntimeLifecycleKeepsSafeCheckpointAcrossRetry(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	completeRuntimeLifecyclePhase(job, runtimePhaseServerAllocate, "allocated", map[string]interface{}{leaseIDField: "lease-1"})
	startRuntimeLifecyclePhase(job, runtimePhaseServerAllocate, "retrying")
	startRuntimeLifecyclePhase(job, runtimePhaseReachability, "connecting")

	lifecycle := copyRuntimeLifecycle(job.Result)
	if got := resultString(lifecycle, "last_safe_checkpoint"); got != runtimePhaseServerAllocate {
		t.Fatalf("last_safe_checkpoint = %q, want %q", got, runtimePhaseServerAllocate)
	}
	if got := resultString(lifecycle, "current_phase"); got != runtimePhaseReachability {
		t.Fatalf("current_phase = %q, want %q", got, runtimePhaseReachability)
	}
	phases := lifecycle["phases"].([]interface{})
	first := phases[runtimeLifecyclePhaseIndex(runtimePhaseServerAllocate)].(map[string]interface{})
	if got := resultString(first, "status"); got != "completed" {
		t.Fatalf("completed checkpoint regressed to %q", got)
	}
}

func TestFreshRuntimeObservationRequiresSupportedVersionAndFreshTimestamp(t *testing.T) {
	now := time.Now().UTC()
	observation := map[string]interface{}{"observed_at": now.Add(-89 * time.Second).Format(time.RFC3339Nano)}
	outputs := map[string]interface{}{"observation": observation}
	for _, version := range []string{"stackkit.runtime-observation/v1", "stackkit.runtime-observation/v2"} {
		observation["version"] = version
		if got := freshRuntimeObservation(outputs, now); len(got) == 0 {
			t.Fatalf("fresh %s observation was rejected", version)
		}
	}
	observation["observed_at"] = now.Add(-91 * time.Second).Format(time.RFC3339Nano)
	if got := freshRuntimeObservation(outputs, now); len(got) != 0 {
		t.Fatalf("stale observation was accepted: %#v", got)
	}
	observation["observed_at"] = now.Format(time.RFC3339Nano)
	observation["version"] = "unknown/v1"
	if got := freshRuntimeObservation(outputs, now); len(got) != 0 {
		t.Fatalf("unknown observation version was accepted: %#v", got)
	}
}

func TestRuntimeLifecycleAdvancesSafeCheckpointOnlyOnCompletion(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	completeRuntimeLifecyclePhase(job, runtimePhaseGenerate, "generated", nil)
	completeDegradedRuntimeLifecyclePhase(job, runtimePhasePrepareApply, "core available", map[string]interface{}{"status": "completed_degraded"})
	startRuntimeLifecyclePhase(job, runtimePhaseVerify, "verifying")
	failCurrentRuntimeLifecyclePhase(job)

	lifecycle := copyRuntimeLifecycle(job.Result)
	if got := resultString(lifecycle, "status"); got != runtimeLifecycleCompletedDegraded {
		t.Fatalf("lifecycle status = %q, want completed_degraded", got)
	}
	if got := resultString(lifecycle, "last_safe_checkpoint"); got != runtimePhasePrepareApply {
		t.Fatalf("last_safe_checkpoint = %q, want %q", got, runtimePhasePrepareApply)
	}
	phases := lifecycle["phases"].([]interface{})
	phase := phases[runtimeLifecyclePhaseIndex(runtimePhaseVerify)].(map[string]interface{})
	if got := resultString(phase, "status"); got != "failed" {
		t.Fatalf("verify status = %q, want failed", got)
	}
}

func TestRuntimeLifecycleDoesNotRewindCurrentPhaseOnLateCompletion(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	startRuntimeLifecyclePhase(job, runtimePhasePrepareApply, "applying")
	completeRuntimeLifecyclePhase(job, runtimePhaseReachability, "bootstrap boundary passed", nil)

	lifecycle := copyRuntimeLifecycle(job.Result)
	if got := resultString(lifecycle, "current_phase"); got != runtimePhasePrepareApply {
		t.Fatalf("current_phase = %q, want %q (late completion of an earlier phase must not rewind)", got, runtimePhasePrepareApply)
	}
	if got := resultString(lifecycle, "last_safe_checkpoint"); got != runtimePhaseReachability {
		t.Fatalf("last_safe_checkpoint = %q, want %q", got, runtimePhaseReachability)
	}
	phases := lifecycle["phases"].([]interface{})
	reach := phases[runtimeLifecyclePhaseIndex(runtimePhaseReachability)].(map[string]interface{})
	if got := resultString(reach, "status"); got != "completed" {
		t.Fatalf("reachability status = %q, want completed", got)
	}
}

func TestRuntimeLifecycleFailureMarksRunningPhaseWhenPointerIsStale(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	completeRuntimeLifecyclePhase(job, runtimePhaseReachability, "reachable", nil)
	startRuntimeLifecyclePhase(job, runtimePhasePrepareApply, "applying")
	// Reproduce the historical stale pointer: a projection written before the
	// no-rewind rule can still carry current_phase on a completed entry.
	job.Result[runtimeLifecycleResultKey].(map[string]interface{})["current_phase"] = runtimePhaseReachability

	failCurrentRuntimeLifecyclePhase(job)

	lifecycle := copyRuntimeLifecycle(job.Result)
	if got := resultString(lifecycle, "current_phase"); got != runtimePhasePrepareApply {
		t.Fatalf("current_phase = %q, want %q (failure must land on the running phase)", got, runtimePhasePrepareApply)
	}
	phases := lifecycle["phases"].([]interface{})
	prepare := phases[runtimeLifecyclePhaseIndex(runtimePhasePrepareApply)].(map[string]interface{})
	if got := resultString(prepare, "status"); got != "failed" {
		t.Fatalf("prepare_apply status = %q, want failed", got)
	}
	reach := phases[runtimeLifecyclePhaseIndex(runtimePhaseReachability)].(map[string]interface{})
	if got := resultString(reach, "status"); got != "completed" {
		t.Fatalf("reachability status = %q, want completed (completed evidence must not be rewritten)", got)
	}
}

func TestRuntimeLifecycleClonesCallerEvidence(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	evidence := map[string]interface{}{"diagnostics": map[string]interface{}{"status": "ready"}}
	completeRuntimeLifecyclePhase(job, runtimePhaseReachability, "reachable", evidence)
	evidence["diagnostics"].(map[string]interface{})["status"] = "failed"

	lifecycle := copyRuntimeLifecycle(job.Snapshot().Result)
	phases := lifecycle["phases"].([]interface{})
	phaseEvidence := phases[runtimeLifecyclePhaseIndex(runtimePhaseReachability)].(map[string]interface{})["evidence"].(map[string]interface{})
	if got := phaseEvidence["diagnostics"].(map[string]interface{})["status"]; got != "ready" {
		t.Fatalf("caller evidence remained aliased: got %v", got)
	}
}
