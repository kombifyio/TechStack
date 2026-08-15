// Package jobs provides error handling and retry logic for job processing.
package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// WaitReasonManagedRuntimeEnrollment identifies a non-terminal rollout wait
	// while the already-created managed VM is still exposing its runtime target.
	WaitReasonManagedRuntimeEnrollment = "waiting_enrollment"
	// WaitReasonManagedRuntimeProvider identifies a non-terminal wait while the
	// native provider operation is still creating its durable resource graph.
	WaitReasonManagedRuntimeProvider = "waiting_provider_provision"
	// WaitReasonCanonicalGuardEvidence identifies an auto-rollout paused before
	// any deploy side effect until the exact canonical Guard runtime is fresh.
	WaitReasonCanonicalGuardEvidence = "waiting_canonical_guard"
	// ManagedRuntimeEnrollmentRecoveryGrace is the server-authoritative delay
	// after a missed scheduled check before manual exact-target recovery opens.
	ManagedRuntimeEnrollmentRecoveryGrace = 2 * time.Minute
	defaultJobWaitResumeDelay             = 5 * time.Second
)

var (
	// ErrAutoDeployAdmissionUnavailable keeps direct ProvisionHandler callers
	// fail-closed when the canonical control-plane gate was not wired.
	ErrAutoDeployAdmissionUnavailable = errors.New("auto-deploy canonical Guard admission is unavailable")
	// ErrAutoDeployAdmissionTimeout terminates the bounded wait without ever
	// falling back to provider metadata, Worker approval, or legacy LastSeen.
	ErrAutoDeployAdmissionTimeout = errors.New("auto-deploy canonical Guard admission timed out")
)

// JobWaitError asks the in-process queue to suspend a job without consuming an
// attempt and resume that same job after ResumeAfter. It is deliberately
// generic so later asynchronous dependencies can use the same honest state.
type JobWaitError struct {
	Reason      string
	Message     string
	ResumeAfter time.Duration
	Cause       error
}

func (e *JobWaitError) Error() string {
	if e == nil {
		return "job is waiting"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "job is waiting"
}

// Unwrap preserves the last dependency error for diagnostics and errors.Is.
func (e *JobWaitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func asJobWaitError(err error) (*JobWaitError, bool) {
	var waitErr *JobWaitError
	if !errors.As(err, &waitErr) || waitErr == nil {
		return nil, false
	}
	return waitErr, true
}

func isJobWaitError(err error) bool {
	_, ok := asJobWaitError(err)
	return ok
}

// ManagedRuntimeEnrollmentPendingError is the typed domain signal returned
// while the VM may still complete within the bounded enrollment wait window.
// It unwraps to JobWaitError so the queue can apply generic wait semantics.
type ManagedRuntimeEnrollmentPendingError struct {
	LeaseID string
	wait    *JobWaitError
}

func newManagedRuntimeEnrollmentPendingError(leaseID string, resumeAfter time.Duration, cause error) *ManagedRuntimeEnrollmentPendingError {
	leaseID = strings.TrimSpace(leaseID)
	if resumeAfter <= 0 {
		resumeAfter = defaultJobWaitResumeDelay
	}
	message := "Managed VM enrollment is still pending; the next rollout check is scheduled."
	if leaseID != "" {
		message = fmt.Sprintf("Managed VM lease %s is still enrolling; the next rollout check is scheduled.", leaseID)
	}
	return &ManagedRuntimeEnrollmentPendingError{
		LeaseID: leaseID,
		wait: &JobWaitError{
			Reason:      WaitReasonManagedRuntimeEnrollment,
			Message:     message,
			ResumeAfter: resumeAfter,
			Cause:       cause,
		},
	}
}

func (e *ManagedRuntimeEnrollmentPendingError) Error() string {
	if e == nil || e.wait == nil {
		return "managed runtime enrollment is still pending"
	}
	return e.wait.Error()
}

func (e *ManagedRuntimeEnrollmentPendingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.wait
}

// ErrorCategory categorizes job errors for retry decisions.
type ErrorCategory int

const (
	// ErrorCategoryUnknown is an uncategorized error.
	ErrorCategoryUnknown ErrorCategory = iota
	// ErrorCategoryTransient is a temporary error that may succeed on retry.
	ErrorCategoryTransient
	// ErrorCategoryPermanent is a permanent error that should not be retried.
	ErrorCategoryPermanent
	// ErrorCategoryConfiguration is a misconfiguration error.
	ErrorCategoryConfiguration
	// ErrorCategoryResource is a resource unavailable error.
	ErrorCategoryResource
)

// String returns the string representation of the error category.
func (c ErrorCategory) String() string {
	switch c {
	case ErrorCategoryTransient:
		return "transient"
	case ErrorCategoryPermanent:
		return "permanent"
	case ErrorCategoryConfiguration:
		return "configuration"
	case ErrorCategoryResource:
		return "resource"
	default:
		return "unknown"
	}
}

// RetryPolicy defines how a job should be retried.
type RetryPolicy struct {
	// MaxAttempts is the maximum number of retry attempts.
	MaxAttempts int
	// InitialDelay is the initial delay before the first retry.
	InitialDelay time.Duration
	// MaxDelay is the maximum delay between retries.
	MaxDelay time.Duration
	// BackoffMultiplier is the multiplier for exponential backoff.
	BackoffMultiplier float64
}

// DefaultRetryPolicy returns a sensible default retry policy.
func DefaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{
		MaxAttempts:       3,
		InitialDelay:      1 * time.Second,
		MaxDelay:          30 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// CalculateDelay calculates the delay for the given attempt number.
func (p *RetryPolicy) CalculateDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return p.InitialDelay
	}

	delay := p.InitialDelay
	for i := 1; i < attempt; i++ {
		delay = time.Duration(float64(delay) * p.BackoffMultiplier)
		if delay > p.MaxDelay {
			delay = p.MaxDelay
			break
		}
	}

	return delay
}

// ShouldRetry returns whether the job should be retried.
func (p *RetryPolicy) ShouldRetry(attempt int) bool {
	return attempt < p.MaxAttempts
}

// JobError wraps an error with additional context for job processing.
type JobError struct {
	Original  error
	Category  ErrorCategory
	Retryable bool
	Context   map[string]interface{}
}

// Error implements the error interface.
func (e *JobError) Error() string {
	if e.Original != nil {
		return e.Original.Error()
	}
	return "unknown job error"
}

// Unwrap returns the underlying error.
func (e *JobError) Unwrap() error {
	return e.Original
}

// NewJobError creates a new JobError with the given error and category.
func NewJobError(err error, category ErrorCategory) *JobError {
	return &JobError{
		Original:  err,
		Category:  category,
		Retryable: category == ErrorCategoryTransient || category == ErrorCategoryResource,
	}
}

// NewTransientError creates a new transient error that should be retried.
func NewTransientError(err error) *JobError {
	return NewJobError(err, ErrorCategoryTransient)
}

// NewPermanentError creates a new permanent error that should not be retried.
func NewPermanentError(err error) *JobError {
	return NewJobError(err, ErrorCategoryPermanent)
}

// NewConfigError creates a new configuration error.
func NewConfigError(err error) *JobError {
	return NewJobError(err, ErrorCategoryConfiguration)
}

// NewResourceError creates a new resource unavailable error.
func NewResourceError(err error) *JobError {
	return NewJobError(err, ErrorCategoryResource)
}

// ClassifyError attempts to classify an error based on its message.
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return ErrorCategoryUnknown
	}

	msg := strings.ToLower(err.Error())

	// Check for transient errors
	transientPatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"temporary",
		"try again",
		"rate limit",
		"too many requests",
		"service unavailable",
		"503",
		"504",
		"network unreachable",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(msg, pattern) {
			return ErrorCategoryTransient
		}
	}

	// Check for permanent errors
	permanentPatterns := []string{
		"not found",
		"permission denied",
		"access denied",
		"unauthorized",
		"forbidden",
		"invalid",
		"malformed",
		"unsupported",
	}

	for _, pattern := range permanentPatterns {
		if strings.Contains(msg, pattern) {
			return ErrorCategoryPermanent
		}
	}

	// Check for configuration errors
	configPatterns := []string{
		"missing",
		"required",
		"configuration",
		"config",
		"not set",
		"not configured",
	}

	for _, pattern := range configPatterns {
		if strings.Contains(msg, pattern) {
			return ErrorCategoryConfiguration
		}
	}

	// Check for resource errors
	resourcePatterns := []string{
		"resource",
		"quota",
		"limit exceeded",
		"insufficient",
		"out of memory",
		"disk full",
	}

	for _, pattern := range resourcePatterns {
		if strings.Contains(msg, pattern) {
			return ErrorCategoryResource
		}
	}

	return ErrorCategoryUnknown
}

// IsRetryable checks if an error should be retried.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check if it's a JobError with explicit retryable flag
	var jobErr *JobError
	if errors.As(err, &jobErr) {
		return jobErr.Retryable
	}

	// Classify and check
	category := ClassifyError(err)
	return category == ErrorCategoryTransient || category == ErrorCategoryResource
}

// WrapError wraps an error with job context and classification.
func WrapError(err error, context map[string]interface{}) *JobError {
	category := ClassifyError(err)
	jobErr := NewJobError(err, category)
	jobErr.Context = context
	return jobErr
}

// ProvisionError wraps errors with step and detail information for frontend display.
type ProvisionError struct {
	Step    string
	Message string
	Details string
}

// Error implements the error interface.
func (e *ProvisionError) Error() string {
	return e.Message
}

// wrapProvisionError creates a ProvisionError with step context.
func wrapProvisionError(step, message, details string) error {
	return &ProvisionError{
		Step:    step,
		Message: message,
		Details: details,
	}
}
