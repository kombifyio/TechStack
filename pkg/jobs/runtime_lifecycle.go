package jobs

import (
	"strings"
	"time"
)

const (
	runtimeLifecycleResultKey         = "runtime_lifecycle"
	runtimeLifecycleVersion           = "techstack.runtime-lifecycle/v1"
	runtimeLifecyclePending           = "pending"
	runtimeLifecycleRunning           = "running"
	runtimeLifecycleCompleted         = "completed"
	runtimeLifecycleCompletedDegraded = "completed_degraded"
	runtimeLifecycleFailed            = "failed"

	runtimePhaseServerAllocate   = "server_allocate"
	runtimePhaseReachability     = "reachability"
	runtimePhaseEnrollment       = "enrollment"
	runtimePhaseInventoryDelta   = "inventory_delta"
	runtimePhaseGenerate         = "generate"
	runtimePhasePrepareApply     = "prepare_apply"
	runtimePhaseVerify           = "verify"
	runtimePhaseServiceInventory = "service_inventory"
	runtimePhaseMonitoringReady  = "monitoring_ready"
)

var runtimeLifecyclePhaseOrder = []string{
	runtimePhaseServerAllocate,
	runtimePhaseReachability,
	runtimePhaseEnrollment,
	runtimePhaseInventoryDelta,
	runtimePhaseGenerate,
	runtimePhasePrepareApply,
	runtimePhaseVerify,
	runtimePhaseServiceInventory,
	runtimePhaseMonitoringReady,
}

func runtimeLifecyclePhaseIndex(phase string) int {
	for i, candidate := range runtimeLifecyclePhaseOrder {
		if candidate == phase {
			return i
		}
	}
	return -1
}

func startRuntimeLifecyclePhase(job *Job, phase, message string) {
	updateRuntimeLifecycle(job, phase, runtimeLifecycleRunning, message, nil)
}

func completeRuntimeLifecyclePhase(job *Job, phase, message string, evidence map[string]interface{}) {
	updateRuntimeLifecycle(job, phase, runtimeLifecycleCompleted, message, evidence)
}

func completeDegradedRuntimeLifecyclePhase(job *Job, phase, message string, evidence map[string]interface{}) {
	updateRuntimeLifecycle(job, phase, runtimeLifecycleCompletedDegraded, message, evidence)
}

func failCurrentRuntimeLifecyclePhase(job *Job) {
	if job == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	lifecycle, ok := job.Result[runtimeLifecycleResultKey].(map[string]interface{})
	if !ok {
		return
	}
	current := stringFromInterface(lifecycle["current_phase"])
	phases, _ := lifecycle["phases"].([]interface{})
	failEntry := func(i int, entry map[string]interface{}) {
		entry[resultStatusField] = runtimeLifecycleFailed
		entry["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		entry["message"] = "Phase failed; retry resumes from the last safe checkpoint"
		phases[i] = entry
		lifecycle["current_phase"] = stringFromInterface(entry["id"])
		lifecycle["updated_at"] = entry["observed_at"]
		job.Result[runtimeLifecycleResultKey] = lifecycle
	}
	for i, raw := range phases {
		entry, entryOK := raw.(map[string]interface{})
		if !entryOK || stringFromInterface(entry["id"]) != current || stringFromInterface(entry[resultStatusField]) != runtimeLifecycleRunning {
			continue
		}
		failEntry(i, entry)
		return
	}
	// The pointer can sit on an already-completed phase when a later phase was
	// running while an earlier confirmation arrived. Fail the highest running
	// phase so the projection reports where the job actually stopped instead of
	// leaving it "running" forever.
	bestIdx, bestRank := -1, -1
	var bestEntry map[string]interface{}
	for i, raw := range phases {
		entry, entryOK := raw.(map[string]interface{})
		if !entryOK || stringFromInterface(entry[resultStatusField]) != runtimeLifecycleRunning {
			continue
		}
		rank := runtimeLifecyclePhaseIndex(stringFromInterface(entry["id"]))
		if rank > bestRank {
			bestIdx, bestRank, bestEntry = i, rank, entry
		}
	}
	if bestEntry != nil {
		failEntry(bestIdx, bestEntry)
	}
}

func updateRuntimeLifecycle(job *Job, phase, status, message string, evidence map[string]interface{}) {
	if job == nil || !knownRuntimeLifecyclePhase(phase) {
		return
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Result == nil {
		job.Result = map[string]interface{}{}
	}
	lifecycle := runtimeLifecycleFromResult(job.Result)
	phases := lifecycle["phases"].([]interface{})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, raw := range phases {
		entry, ok := raw.(map[string]interface{})
		if !ok || stringFromInterface(entry["id"]) != phase {
			continue
		}
		// A retry may revisit a safe checkpoint. Keep the checkpoint completed
		// until the subsequent phase supplies newer evidence.
		if stringFromInterface(entry[resultStatusField]) == runtimeLifecycleCompleted && status == runtimeLifecycleRunning {
			lifecycle["current_phase"] = phase
			lifecycle["updated_at"] = now
			job.Result[runtimeLifecycleResultKey] = lifecycle
			return
		}
		entry[resultStatusField] = status
		entry["observed_at"] = now
		if strings.TrimSpace(message) != "" {
			entry["message"] = strings.TrimSpace(message)
		}
		if len(evidence) > 0 {
			entry["evidence"] = cloneJobMap(evidence)
		}
		phases[i] = entry
		break
	}
	// A late completion of an earlier phase (e.g. reachability confirmed while
	// prepare_apply already runs) must not rewind the pointer.
	current := stringFromInterface(lifecycle["current_phase"])
	if status == runtimeLifecycleRunning || runtimeLifecyclePhaseIndex(phase) >= runtimeLifecyclePhaseIndex(current) {
		lifecycle["current_phase"] = phase
	}
	lifecycle["updated_at"] = now
	if status == runtimeLifecycleCompleted || status == runtimeLifecycleCompletedDegraded {
		lifecycle["last_safe_checkpoint"] = phase
	}
	if status == runtimeLifecycleCompletedDegraded {
		lifecycle["status"] = runtimeLifecycleCompletedDegraded
	}
	job.Result[runtimeLifecycleResultKey] = lifecycle
}

func runtimeLifecycleFromResult(result map[string]interface{}) map[string]interface{} {
	if existing, ok := result[runtimeLifecycleResultKey].(map[string]interface{}); ok &&
		stringFromInterface(existing["version"]) == runtimeLifecycleVersion {
		if _, ok := existing["phases"].([]interface{}); ok {
			return existing
		}
	}
	phases := make([]interface{}, 0, len(runtimeLifecyclePhaseOrder))
	for _, phase := range runtimeLifecyclePhaseOrder {
		phases = append(phases, map[string]interface{}{"id": phase, resultStatusField: runtimeLifecyclePending})
	}
	return map[string]interface{}{
		"version": runtimeLifecycleVersion,
		"phases":  phases,
	}
}

func knownRuntimeLifecyclePhase(phase string) bool {
	for _, candidate := range runtimeLifecyclePhaseOrder {
		if candidate == phase {
			return true
		}
	}
	return false
}

func copyRuntimeLifecycle(result map[string]interface{}) map[string]interface{} {
	if result == nil {
		return nil
	}
	lifecycle, _ := result[runtimeLifecycleResultKey].(map[string]interface{})
	return lifecycle
}

func freshRuntimeObservation(outputs map[string]interface{}, now time.Time) map[string]interface{} {
	observation := sanitizedRuntimeObservation(outputs)
	if !supportedRuntimeObservationVersion(resultString(observation, "version")) {
		return nil
	}
	observedAt, err := time.Parse(time.RFC3339Nano, resultString(observation, "observed_at"))
	if err != nil || observedAt.After(now.Add(30*time.Second)) || now.Sub(observedAt) > 90*time.Second {
		return nil
	}
	return observation
}

func supportedRuntimeObservationVersion(version string) bool {
	switch strings.TrimSpace(version) {
	case "stackkit.runtime-observation/v1", "stackkit.runtime-observation/v2":
		return true
	default:
		return false
	}
}
