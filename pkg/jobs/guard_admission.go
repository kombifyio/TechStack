package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// awaitAutoDeployGuardAdmission keeps RPCs and backoff on one durable deadline.
// Returning queue waits here would allow claim contention and replay preparation
// to add a fresh scheduling window after the provider is already ready.
func awaitAutoDeployGuardAdmission(ctx context.Context, cfg *ProvisionConfig, job *Job, q *Queue, request AutoDeployAdmissionRequest) error {
	timeout, interval := managedRuntimeTargetWaitConfig(cfg)
	snapshot := job.Snapshot()
	raw := firstNonEmpty(stringFromMap(snapshot.Result, autoDeployGuardWaitStartedAtField), stringFromMap(snapshot.Payload, autoDeployGuardWaitStartedAtField))
	// Customer-operated substrate guests have no managed-provider present
	// receipt. Preserve their original durable wait checkpoint; this exception
	// never opens a replacement window for a managed-provider lease.
	if raw == "" {
		guest, guestErr := customerGuestCheckpoint(job)
		if guestErr != nil {
			return wrapProvisionCause(StepPrepareRollout, guestErr, "The customer guest admission receipt is invalid.")
		}
		if guest != nil {
			startedAt := time.Now().UTC()
			raw = startedAt.Format(time.RFC3339Nano)
			setAutoDeployGuardWaitStart(job, startedAt)
		}
	}
	startedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || startedAt.IsZero() || startedAt.After(time.Now().UTC()) {
		return wrapProvisionCause(StepPrepareRollout, fmt.Errorf("%w: provider_ready_at_invalid", ErrAutoDeployAdmissionPrerequisite), "The provider-ready checkpoint is invalid; enrollment cannot start a replacement window.")
	}
	deadline := startedAt.Add(timeout)
	admissionCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	job.setStep(StepPrepareRollout)
	startRuntimeLifecyclePhase(job, runtimePhaseEnrollment, "Confirming canonical Guard admission within the provider-ready deadline")
	job.mutateResult(func(result map[string]interface{}) {
		result["guard_admission_deadline_at"] = deadline.Format(time.RFC3339Nano)
		result["guard_admission_phase"] = "canonical_guard_admission"
	})
	q.UpdateProgress(job.ID, 90, "Managed server ready; confirming its Guard connection...")
	var lastErr error
	for {
		if admissionCtx.Err() == nil {
			lastErr = cfg.AutoDeployAdmission(admissionCtx, request)
			if lastErr == nil && admissionCtx.Err() == nil {
				job.mutateResult(func(result map[string]interface{}) {
					result["guard_admission_elapsed_ms"] = time.Since(startedAt).Milliseconds()
					result["guard_admission_reason"] = "admitted"
				})
				completeRuntimeLifecyclePhase(job, runtimePhaseEnrollment, "Canonical Guard admission confirmed", nil)
				return nil
			}
		}
		if ctx.Err() != nil {
			return wrapProvisionCause(StepPrepareRollout, ctx.Err(), "Guard admission was interrupted; its original deadline is retained.")
		}
		if errors.Is(lastErr, ErrAutoDeployAdmissionPrerequisite) || errors.Is(lastErr, ErrAutoDeployAdmissionUnavailable) {
			job.mutateResult(func(result map[string]interface{}) {
				result["guard_admission_reason"] = "prerequisite_failed"
				result["guard_admission_failure"] = lastErr.Error()
				result["guard_admission_elapsed_ms"] = time.Since(startedAt).Milliseconds()
			})
			return wrapProvisionCause(StepPrepareRollout, lastErr, "Guard admission cannot succeed until the named prerequisite is repaired.")
		}
		if admissionCtx.Err() != nil || !time.Now().Before(deadline) {
			job.mutateResult(func(result map[string]interface{}) {
				result[GuardHeartbeatTimeoutReason] = true
				result["guard_admission_reason"] = "deadline_exceeded"
				result["guard_admission_elapsed_ms"] = time.Since(startedAt).Milliseconds()
			})
			return wrapProvisionCause(StepPrepareRollout, fmt.Errorf("%w: phase=canonical_guard_admission budget=%s: %v", ErrAutoDeployAdmissionTimeout, timeout, lastErr), "The provider-ready enrollment budget expired, including admission requests and backoff. Inspect bootstrap evidence before retrying.")
		}
		pause := time.NewTimer(managedRuntimeEnrollmentBoundedDelay(interval, time.Until(deadline)))
		select {
		case <-admissionCtx.Done():
			pause.Stop()
		case <-pause.C:
		}
	}
}
