package workflow

import "time"

// RetryPolicy controls step retry behaviour with exponential backoff. Adopted
// from the Temporal retry-policy pattern (explicit, bounded) — not the engine.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first. A value
	// <= 1 means no retry (single attempt).
	MaxAttempts int
	// InitialBackoff is the delay before the second attempt.
	InitialBackoff time.Duration
	// MaxBackoff caps the per-attempt delay.
	MaxBackoff time.Duration
	// Multiplier grows the backoff each attempt (e.g. 2.0 doubles it).
	Multiplier float64
}

// DefaultRetryPolicy is a sensible default for RIL workflow steps: 3 attempts,
// 2s → 4s → ... capped at 60s.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    3,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     60 * time.Second,
		Multiplier:     2.0,
	}
}

// ShouldRetry reports whether another attempt is allowed. attempt is 1-based
// (the attempt that just failed).
func (p RetryPolicy) ShouldRetry(attempt int) bool {
	if p.MaxAttempts <= 1 {
		return false
	}
	return attempt < p.MaxAttempts
}

// Backoff returns the delay before the given (1-based) attempt number. Backoff
// before attempt 1 is zero (the first attempt runs immediately). The result is
// clamped to [0, MaxBackoff].
func (p RetryPolicy) Backoff(attempt int) time.Duration {
	if attempt <= 1 || p.InitialBackoff <= 0 {
		return 0
	}
	mult := p.Multiplier
	if mult < 1 {
		mult = 1
	}
	d := float64(p.InitialBackoff)
	for i := 2; i < attempt; i++ {
		d *= mult
		if p.MaxBackoff > 0 && d >= float64(p.MaxBackoff) {
			return p.MaxBackoff
		}
	}
	if p.MaxBackoff > 0 && d > float64(p.MaxBackoff) {
		return p.MaxBackoff
	}
	return time.Duration(d)
}
