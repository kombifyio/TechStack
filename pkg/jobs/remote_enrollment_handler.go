package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRemoteEnrollmentUnavailable keeps the durable connect-remote enrollment
// job fail-closed when no executor is wired on this control plane.
var ErrRemoteEnrollmentUnavailable = errors.New("remote enrollment executor is not configured")

// remoteEnrollmentWaitAttemptsField counts queue-level retry waits so a
// permanently unreachable host fails closed instead of waiting forever.
const remoteEnrollmentWaitAttemptsField = "remote_enrollment_wait_attempts"

// remoteEnrollmentMaxWaitAttempts bounds retryable enrollment re-waits. Each
// attempt may already consume the enroller's own bounded window, so the queue
// only adds a small number of honest retries before the job terminates.
const remoteEnrollmentMaxWaitAttempts = 2

// RemoteEnrollmentRequest is the non-secret identity a durable enrollment
// handler needs. Credentials are resolved from wallet custody by the executor
// and never travel through the job payload.
type RemoteEnrollmentRequest struct {
	TenantID        string
	StackID         string
	OwnerID         string
	JobID           string
	PlannedServerID string
}

// RemoteEnrollmentOutcome reports the enrolled node back to the job result.
type RemoteEnrollmentOutcome struct {
	ServerID string
	Message  string
}

// RemoteEnrollmentExecutor performs the SSH enrollment lane. Implementations
// own credential custody, pairing-token minting and the control-plane origin;
// the jobs package owns durability, retries and progress projection.
type RemoteEnrollmentExecutor interface {
	ExecuteRemoteEnrollment(
		ctx context.Context,
		req RemoteEnrollmentRequest,
		progress func(step, message string, percent int),
	) (RemoteEnrollmentOutcome, error)
}

// RemoteEnrollmentRetryableError marks an enrollment failure a bounded number
// of queue-level retries may pick up again. Anything else fails the job.
type RemoteEnrollmentRetryableError struct {
	Reason  string
	Message string
	Cause   error
}

func (e *RemoteEnrollmentRetryableError) Error() string {
	if e == nil {
		return "remote enrollment retryable failure"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "remote enrollment retryable failure"
}

func (e *RemoteEnrollmentRetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RemoteEnrollmentFailure is a terminal enrollment failure that still carries
// the classified reason the UI shows. The handler records reason_code and
// retryable=false on the job before returning the error.
type RemoteEnrollmentFailure struct {
	Reason  string
	Message string
	Cause   error
}

func (e *RemoteEnrollmentFailure) Error() string {
	if e == nil {
		return "remote enrollment failed"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "remote enrollment failed"
}

func (e *RemoteEnrollmentFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RemoteEnrollmentHandler drives one durable remote_enrollment job. The
// durable row is created by the pairing mint; this handler resolves the
// non-secret identity from the job, delegates the SSH lane to the configured
// executor, and projects progress and outcomes onto the job.
func RemoteEnrollmentHandler(cfg *ProvisionConfig) JobHandler {
	var executor RemoteEnrollmentExecutor
	if cfg != nil {
		executor = cfg.RemoteEnrollment
	}
	return func(ctx context.Context, job *Job, _ *Queue) error {
		if job == nil {
			return fmt.Errorf("%w: job is required", ErrRemoteEnrollmentUnavailable)
		}
		if executor == nil {
			return ErrRemoteEnrollmentUnavailable
		}
		tenantID := strings.TrimSpace(stringFromMap(job.Payload, "tenant_id"))
		stackID := strings.TrimSpace(job.TargetID)
		if tenantID == "" || stackID == "" {
			return fmt.Errorf("remote enrollment job requires tenant and stack identity")
		}
		req := RemoteEnrollmentRequest{
			TenantID:        tenantID,
			StackID:         stackID,
			OwnerID:         strings.TrimSpace(stringFromMap(job.Payload, "owner_id")),
			JobID:           job.ID,
			PlannedServerID: firstNonEmpty(stringFromMap(job.Result, "planned_server_id"), stringFromMap(job.Payload, "planned_server_id")),
		}
		outcome, err := executor.ExecuteRemoteEnrollment(ctx, req, func(step, message string, percent int) {
			setRemoteEnrollmentProgress(job, step, message, percent)
		})
		if err != nil {
			var retryable *RemoteEnrollmentRetryableError
			if errors.As(err, &retryable) {
				if attempts := remoteEnrollmentWaitAttempts(job); attempts < remoteEnrollmentMaxWaitAttempts {
					job.mutateResult(func(result map[string]interface{}) {
						result[remoteEnrollmentWaitAttemptsField] = attempts + 1
						if reason := strings.TrimSpace(retryable.Reason); reason != "" {
							result["reason_code"] = reason
						}
						result["retryable"] = true
					})
					setRemoteEnrollmentProgress(job, "remote_ssh_connect", retryable.Error(), -1)
					return &JobWaitError{
						Reason:      firstNonEmpty(retryable.Reason, "remote_enrollment_retry"),
						Message:     retryable.Error(),
						ResumeAfter: defaultJobWaitResumeDelay * 3,
						Cause:       retryable.Cause,
					}
				}
				job.mutateResult(func(result map[string]interface{}) {
					result["retryable"] = true
					if reason := strings.TrimSpace(retryable.Reason); reason != "" {
						result["reason_code"] = reason
					}
				})
				return fmt.Errorf("remote enrollment retries exhausted: %w", retryable.Cause)
			}
			var failure *RemoteEnrollmentFailure
			if errors.As(err, &failure) {
				job.mutateResult(func(result map[string]interface{}) {
					result["retryable"] = false
					if reason := strings.TrimSpace(failure.Reason); reason != "" {
						result["reason_code"] = reason
					}
				})
				setRemoteEnrollmentProgress(job, "remote_ssh_connect", failure.Error(), -1)
				return failure
			}
			return err
		}
		job.mutateResult(func(result map[string]interface{}) {
			result["retryable"] = false
			delete(result, remoteEnrollmentWaitAttemptsField)
			if serverID := strings.TrimSpace(outcome.ServerID); serverID != "" {
				result["server_id"] = serverID
			}
			if req.PlannedServerID != "" {
				result["planned_server_id"] = req.PlannedServerID
			}
		})
		setRemoteEnrollmentProgress(job, "remote_ssh_enrolled", firstNonEmpty(outcome.Message, "Guard enrolled over SSH"), 100)
		return nil
	}
}

func remoteEnrollmentWaitAttempts(job *Job) int {
	if job == nil {
		return 0
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	switch typed := job.Result[remoteEnrollmentWaitAttemptsField].(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case int64:
		return int(typed)
	default:
		return 0
	}
}

func setRemoteEnrollmentProgress(job *Job, step, message string, percent int) {
	if job == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if trimmed := strings.TrimSpace(step); trimmed != "" {
		job.Step = trimmed
	}
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		job.Message = trimmed
	}
	if percent >= 0 {
		job.Progress = percent
	}
}
