package routes

import (
	"testing"
	"time"
)

// A peer that floods registration is refused, other peers and later windows
// are unaffected (Guard threat-model F3).
func TestWorkerRegisterLimiterThrottlesOnePeerOnly(t *testing.T) {
	l := newWorkerRegisterLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < workerRegisterMaxPerWindow; i++ {
		if !l.allow("203.0.113.1") {
			t.Fatalf("attempt %d refused within budget", i)
		}
	}
	if l.allow("203.0.113.1") {
		t.Fatal("flooding peer admitted beyond budget")
	}
	if !l.allow("203.0.113.2") {
		t.Fatal("unrelated peer refused")
	}
	now = now.Add(workerRegisterWindow + time.Second)
	if !l.allow("203.0.113.1") {
		t.Fatal("peer still refused after the window")
	}
}
