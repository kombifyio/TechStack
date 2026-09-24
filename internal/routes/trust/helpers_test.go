package trust

import (
	"testing"
	"time"
)

func TestPairingTokenExpiresAtCapsRequestedLifetime(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		requestedMinutes *int
		wantMinutes      int
	}{
		{name: "default", wantMinutes: pairingTokenDefaultExpiryMinutes},
		{name: "shorter request", requestedMinutes: intPointer(5), wantMinutes: 5},
		{name: "maximum request", requestedMinutes: intPointer(pairingTokenMaxExpiryMinutes), wantMinutes: pairingTokenMaxExpiryMinutes},
		{name: "oversized request", requestedMinutes: intPointer(pairingTokenMaxExpiryMinutes + 1), wantMinutes: pairingTokenMaxExpiryMinutes},
		{name: "overflow-sized request", requestedMinutes: intPointer(int(^uint(0) >> 1)), wantMinutes: pairingTokenMaxExpiryMinutes},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := now.Add(time.Duration(test.wantMinutes) * time.Minute)
			if got := pairingTokenExpiresAt(now, test.requestedMinutes); !got.Equal(want) {
				t.Fatalf("pairingTokenExpiresAt = %s, want %s", got, want)
			}
		})
	}
}

func intPointer(value int) *int { return &value }
