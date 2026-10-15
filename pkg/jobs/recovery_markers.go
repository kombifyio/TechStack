package jobs

import "strings"

const (
	// ManagedProvisionWaitCheckpointResultKey marks a provision job that has
	// yielded for its native provider operation or for canonical Guard evidence
	// and is only replaying that wait. The control-plane projection strips
	// job_wait while the replay runs, so this durable marker is what lets
	// recovery tell a replaying wait (safe to resume) from a job that crossed
	// into rollout side effects (operator resolution). It binds the exact lease
	// and provider operation; ProvisionHandler clears it before any rollout.
	ManagedProvisionWaitCheckpointResultKey = "managed_provision_wait_checkpoint"

	// SupersededByJobIDResultKey records, on a source job, the deterministic
	// replacement job that took over its work.
	SupersededByJobIDResultKey = "superseded_by_job_id"

	operationIDField                      = "operation_id"
	enrollmentResumeSourceJobIDField      = "enrollment_resume_source_job_id"
	enrollmentResumeReplacementJobIDField = "enrollment_resume_replacement_job_id"
)

// ManagedProvisionWaitCheckpoint returns the wait reason of a valid durable
// replay marker. It is valid only when it names the exact lease and provider
// operation the result still carries; a Guard wait additionally requires the
// lease-ready checkpoint. Anything partial or mismatched is not a checkpoint.
func ManagedProvisionWaitCheckpoint(result map[string]interface{}) (string, bool) {
	marker := mapFromInterface(result[ManagedProvisionWaitCheckpointResultKey])
	reason := resultString(marker, "reason")
	leaseID := resultString(result, leaseIDField)
	operationID := resultString(result, operationIDField)
	if leaseID == "" || operationID == "" ||
		resultString(marker, leaseIDField) != leaseID || resultString(marker, operationIDField) != operationID {
		return "", false
	}
	switch reason {
	case WaitReasonManagedRuntimeProvider:
		return reason, true
	case WaitReasonCanonicalGuardEvidence:
		return reason, resultString(result, "runtime_phase") == string(RuntimePhaseLeaseReady)
	default:
		return "", false
	}
}

// recordManagedProvisionWaitCheckpoint binds the wait the handler is about to
// yield for to the exact lease and operation it holds. Without both
// correlations no marker is written, so recovery stays fail-closed.
func recordManagedProvisionWaitCheckpoint(job *Job, reason string) {
	if job == nil {
		return
	}
	job.mutateResult(func(result map[string]interface{}) {
		leaseID := resultString(result, leaseIDField)
		operationID := resultString(result, operationIDField)
		if leaseID == "" || operationID == "" {
			delete(result, ManagedProvisionWaitCheckpointResultKey)
			return
		}
		result[ManagedProvisionWaitCheckpointResultKey] = map[string]interface{}{
			"reason":         reason,
			leaseIDField:     leaseID,
			operationIDField: operationID,
		}
	})
}

// restoreManagedProvisionWaitCheckpoint carries a valid marker, and the exact
// lease and operation it binds, across the result rebuild of a replay. A crash
// while the replay runs then still finds the wait's recovery boundary.
func restoreManagedProvisionWaitCheckpoint(job *Job, previous map[string]interface{}) {
	if job == nil {
		return
	}
	if _, ok := ManagedProvisionWaitCheckpoint(previous); !ok {
		return
	}
	job.mutateResult(func(result map[string]interface{}) {
		for _, key := range []string{ManagedProvisionWaitCheckpointResultKey, leaseIDField, operationIDField} {
			if _, present := result[key]; !present {
				result[key] = previous[key]
			}
		}
	})
}

func clearManagedProvisionWaitCheckpoint(job *Job) {
	if job == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if _, ok := job.Result[ManagedProvisionWaitCheckpointResultKey]; !ok {
		return
	}
	job.Result = cloneJobMap(job.Result)
	delete(job.Result, ManagedProvisionWaitCheckpointResultKey)
}

// SupersededByJobID returns the replacement job that took over jobID, or "".
// Rollout retry records the explicit pointer; an enrollment-resume claim
// already persists its exact receipt on the source, whose replacement is
// derived only when that receipt names jobID as the source.
func SupersededByJobID(jobID string, result map[string]interface{}) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ""
	}
	if replacement := resultString(result, SupersededByJobIDResultKey); replacement != "" && replacement != jobID {
		return replacement
	}
	if resultString(result, enrollmentResumeSourceJobIDField) == jobID {
		if replacement := resultString(result, enrollmentResumeReplacementJobIDField); replacement != "" && replacement != jobID {
			return replacement
		}
	}
	return ""
}
