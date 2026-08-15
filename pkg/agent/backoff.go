package agent

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Backoff implements capped exponential backoff with full jitter, used by the
// Supervisor to pace reconnect attempts against Core. Full jitter (delay drawn
// uniformly from [0, ceiling]) avoids reconnect stampedes when many agents lose
// the same Core at once.
type Backoff struct {
	// Base is the ceiling of the first attempt's delay.
	Base time.Duration
	// Factor multiplies the ceiling per attempt.
	Factor float64
	// Cap is the maximum ceiling.
	Cap time.Duration

	mu      sync.Mutex
	attempt int
	rng     *rand.Rand
}

// NewBackoff returns a Backoff with the agent defaults: base 1s, factor 2,
// cap 5min.
func NewBackoff() *Backoff {
	return &Backoff{
		Base:   time.Second,
		Factor: 2,
		Cap:    5 * time.Minute,
		// #nosec G404 -- jitter for reconnect pacing, not security-sensitive.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Next returns the delay to wait before the next attempt and advances the
// attempt counter.
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	ceiling := float64(b.Base) * math.Pow(b.Factor, float64(b.attempt))
	if ceiling > float64(b.Cap) || ceiling <= 0 {
		ceiling = float64(b.Cap)
	}
	b.attempt++

	if b.rng == nil {
		// #nosec G404 -- jitter for reconnect pacing, not security-sensitive.
		b.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	// Full jitter: uniform in [0, ceiling]. Guarantee a small floor so a
	// pathological draw of ~0 cannot hot-loop the dialer.
	d := time.Duration(b.rng.Int63n(int64(ceiling) + 1))
	const floor = 100 * time.Millisecond
	if d < floor {
		d = floor
	}
	return d
}

// Reset clears the attempt counter. Called after a connection has been stable
// long enough that the next failure should be treated as fresh.
func (b *Backoff) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attempt = 0
}

// Attempt returns the number of Next calls since the last Reset.
func (b *Backoff) Attempt() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempt
}
