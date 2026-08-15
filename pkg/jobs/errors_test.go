package jobs

import (
	"errors"
	"testing"
	"time"
)

func TestErrorCategory_String(t *testing.T) {
	tests := []struct {
		category ErrorCategory
		want     string
	}{
		{ErrorCategoryUnknown, "unknown"},
		{ErrorCategoryTransient, "transient"},
		{ErrorCategoryPermanent, "permanent"},
		{ErrorCategoryConfiguration, "configuration"},
		{ErrorCategoryResource, "resource"},
	}

	for _, tt := range tests {
		if got := tt.category.String(); got != tt.want {
			t.Errorf("ErrorCategory(%d).String() = %q, want %q", tt.category, got, tt.want)
		}
	}
}

func TestRetryPolicy_CalculateDelay(t *testing.T) {
	policy := &RetryPolicy{
		InitialDelay:      1 * time.Second,
		MaxDelay:          10 * time.Second,
		BackoffMultiplier: 2.0,
	}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // Capped at MaxDelay
		{6, 10 * time.Second}, // Still capped
	}

	for _, tt := range tests {
		got := policy.CalculateDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("CalculateDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestRetryPolicy_ShouldRetry(t *testing.T) {
	policy := &RetryPolicy{MaxAttempts: 3}

	tests := []struct {
		attempt int
		want    bool
	}{
		{0, true},
		{1, true},
		{2, true},
		{3, false},
		{4, false},
	}

	for _, tt := range tests {
		if got := policy.ShouldRetry(tt.attempt); got != tt.want {
			t.Errorf("ShouldRetry(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorCategory
	}{
		// Transient errors
		{"timeout", errors.New("operation timeout"), ErrorCategoryTransient},
		{"connection refused", errors.New("connection refused"), ErrorCategoryTransient},
		{"rate limit", errors.New("rate limit exceeded"), ErrorCategoryTransient},
		{"503", errors.New("503 Service Unavailable"), ErrorCategoryTransient},

		// Permanent errors
		{"not found", errors.New("resource not found"), ErrorCategoryPermanent},
		{"permission denied", errors.New("permission denied"), ErrorCategoryPermanent},
		{"invalid input", errors.New("invalid configuration"), ErrorCategoryPermanent},

		// Configuration errors
		{"missing field", errors.New("missing required field"), ErrorCategoryConfiguration},
		{"not configured", errors.New("service not configured"), ErrorCategoryConfiguration},

		// Resource errors
		{"quota exceeded", errors.New("quota exceeded"), ErrorCategoryResource},
		{"out of memory", errors.New("out of memory"), ErrorCategoryResource},

		// Unknown
		{"generic error", errors.New("something went wrong"), ErrorCategoryUnknown},
		{"nil error", nil, ErrorCategoryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transient error", NewTransientError(errors.New("timeout")), true},
		{"permanent error", NewPermanentError(errors.New("invalid")), false},
		{"resource error", NewResourceError(errors.New("quota")), true},
		{"config error", NewConfigError(errors.New("missing")), false},
		{"classified transient", errors.New("connection timeout"), true},
		{"classified permanent", errors.New("permission denied"), false},
		{"unknown error", errors.New("something happened"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestJobError(t *testing.T) {
	originalErr := errors.New("original error")
	jobErr := NewTransientError(originalErr)

	if jobErr.Error() != "original error" {
		t.Errorf("Error() = %q, want %q", jobErr.Error(), "original error")
	}

	if !errors.Is(jobErr, originalErr) {
		t.Error("errors.Is should return true for wrapped error")
	}

	if jobErr.Category != ErrorCategoryTransient {
		t.Errorf("Category = %v, want %v", jobErr.Category, ErrorCategoryTransient)
	}

	if !jobErr.Retryable {
		t.Error("Transient error should be retryable")
	}
}

func TestWrapError(t *testing.T) {
	err := errors.New("connection timeout")
	context := map[string]interface{}{
		"host": "example.com",
		"port": 443,
	}

	wrapped := WrapError(err, context)

	if wrapped.Category != ErrorCategoryTransient {
		t.Errorf("Category = %v, want %v", wrapped.Category, ErrorCategoryTransient)
	}

	if wrapped.Context["host"] != "example.com" {
		t.Error("Context should be preserved")
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()

	if policy.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", policy.MaxAttempts)
	}

	if policy.InitialDelay != 1*time.Second {
		t.Errorf("InitialDelay = %v, want 1s", policy.InitialDelay)
	}

	if policy.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", policy.MaxDelay)
	}

	if policy.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %v, want 2.0", policy.BackoffMultiplier)
	}
}
