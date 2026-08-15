package workflow

import (
	"testing"
	"time"
)

func TestRetryPolicy_ShouldRetry(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 3}
	if p.ShouldRetry(1) != true {
		t.Error("attempt 1 of 3 should retry")
	}
	if p.ShouldRetry(2) != true {
		t.Error("attempt 2 of 3 should retry")
	}
	if p.ShouldRetry(3) != false {
		t.Error("attempt 3 of 3 should NOT retry (exhausted)")
	}

	noRetry := RetryPolicy{MaxAttempts: 1}
	if noRetry.ShouldRetry(1) {
		t.Error("MaxAttempts=1 means no retry")
	}
	zero := RetryPolicy{MaxAttempts: 0}
	if zero.ShouldRetry(1) {
		t.Error("MaxAttempts=0 means no retry")
	}
}

func TestRetryPolicy_BackoffProgression(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: time.Second,
		MaxBackoff:     10 * time.Second,
		Multiplier:     2.0,
	}
	// attempt 1 runs immediately
	if got := p.Backoff(1); got != 0 {
		t.Errorf("backoff(1) = %v, want 0", got)
	}
	// attempt 2 = initial
	if got := p.Backoff(2); got != time.Second {
		t.Errorf("backoff(2) = %v, want 1s", got)
	}
	// attempt 3 = initial * 2
	if got := p.Backoff(3); got != 2*time.Second {
		t.Errorf("backoff(3) = %v, want 2s", got)
	}
	// attempt 4 = initial * 4
	if got := p.Backoff(4); got != 4*time.Second {
		t.Errorf("backoff(4) = %v, want 4s", got)
	}
}

func TestRetryPolicy_BackoffClamp(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts:    20,
		InitialBackoff: time.Second,
		MaxBackoff:     5 * time.Second,
		Multiplier:     2.0,
	}
	// 1,2,4,8... clamps at 5s
	if got := p.Backoff(10); got != 5*time.Second {
		t.Errorf("backoff(10) = %v, want clamp 5s", got)
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxAttempts != 3 {
		t.Errorf("default MaxAttempts = %d, want 3", p.MaxAttempts)
	}
	if !p.ShouldRetry(1) || p.ShouldRetry(3) {
		t.Error("default policy retry window wrong")
	}
}
