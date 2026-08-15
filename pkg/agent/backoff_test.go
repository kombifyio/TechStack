package agent

import (
	"testing"
	"time"
)

func TestBackoffCeilingGrowthAndCap(t *testing.T) {
	b := &Backoff{Base: time.Second, Factor: 2, Cap: 5 * time.Minute}

	// Draw many samples per attempt and assert every draw stays within the
	// attempt's ceiling (full jitter upper bound) and above the floor.
	ceilings := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	}
	for attempt, ceiling := range ceilings {
		b.mu.Lock()
		b.attempt = attempt
		b.mu.Unlock()
		for i := 0; i < 200; i++ {
			b.mu.Lock()
			b.attempt = attempt
			b.mu.Unlock()
			d := b.Next()
			if d > ceiling {
				t.Fatalf("attempt %d: delay %v exceeds ceiling %v", attempt, d, ceiling)
			}
			if d < 100*time.Millisecond {
				t.Fatalf("attempt %d: delay %v below floor", attempt, d)
			}
		}
	}
}

func TestBackoffCap(t *testing.T) {
	b := &Backoff{Base: time.Second, Factor: 2, Cap: 3 * time.Second}
	// Push the attempt counter far beyond the cap crossover.
	for i := 0; i < 50; i++ {
		if d := b.Next(); d > 3*time.Second {
			t.Fatalf("delay %v exceeds cap", d)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	b := NewBackoff()
	b.Next()
	b.Next()
	if got := b.Attempt(); got != 2 {
		t.Fatalf("Attempt() = %d, want 2", got)
	}
	b.Reset()
	if got := b.Attempt(); got != 0 {
		t.Fatalf("Attempt() after Reset = %d, want 0", got)
	}
	// First delay after reset must respect the base ceiling again.
	if d := b.Next(); d > time.Second {
		t.Fatalf("delay after reset %v exceeds base ceiling", d)
	}
}
