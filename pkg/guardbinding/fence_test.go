package guardbinding

import (
	"testing"
	"time"
)

// The authority of a received command ends at the earlier of its absolute
// expiry and the lifetime it had left at receipt on the monotonic clock, so a
// wall clock set back cannot extend it.
func TestAuthorityEndsAtTheEarlierOfAbsoluteAndMonotonicExpiry(t *testing.T) {
	received := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	window := AuthorityWindow{ExpiresAt: received.Add(2 * time.Minute), ReceivedAt: received, ReceivedMono: time.Hour}

	if denial := window.Check(received.Add(time.Minute), time.Hour+time.Minute); denial != nil {
		t.Fatalf("authority within both bounds was denied: %v", denial)
	}
	// One minute of wall time but more than two of monotonic time: the clock
	// was set back within the tolerated margin, the lifetime is still spent.
	denial := window.Check(received.Add(time.Minute), time.Hour+2*time.Minute+time.Second)
	if denial == nil || denial.Code != CodeAuthorityExpired {
		t.Fatalf("spent monotonic lifetime admitted an effect: %v", denial)
	}
}

// Without a trustworthy remaining lifetime (no receipt in this process, a
// monotonic clock that does not continue, as after a reboot, or a stepped wall
// clock) a command needs online revalidation before any effect.
func TestUntrustedRemainingLifetimeRequiresOnlineRevalidation(t *testing.T) {
	received := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	valid := AuthorityWindow{ExpiresAt: received.Add(10 * time.Minute), ReceivedAt: received, ReceivedMono: time.Hour}
	cases := map[string]struct {
		window AuthorityWindow
		now    time.Time
		mono   time.Duration
	}{
		"recovered without receipt":  {AuthorityWindow{ExpiresAt: valid.ExpiresAt}, received.Add(time.Minute), time.Minute},
		"monotonic clock restarted":  {valid, received.Add(time.Minute), time.Minute},
		"wall clock stepped forward": {valid, received.Add(9 * time.Minute), time.Hour + time.Second},
		"wall clock stepped back":    {valid, received.Add(-6 * time.Minute), time.Hour + time.Minute},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			denial := tc.window.Check(tc.now, tc.mono)
			if denial == nil || denial.Code != CodeRevalidationRequired {
				t.Fatalf("untrusted lifetime admitted an effect: %v", denial)
			}
		})
	}
}
