package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

func TestGeneratePairingTokenReturnsSHA256Hash(t *testing.T) {
	rawToken, tokenHashHex, err := GeneratePairingToken()
	if err != nil {
		t.Fatalf("GeneratePairingToken returned error: %v", err)
	}
	if rawToken == "" {
		t.Fatal("expected raw token")
	}

	hash := sha256.Sum256([]byte(rawToken))
	if want := hex.EncodeToString(hash[:]); tokenHashHex != want {
		t.Fatalf("expected token hash %q, got %q", want, tokenHashHex)
	}
}

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

func TestGenerateStorePairingTokenCarriesTenantLocator(t *testing.T) {
	rawToken, tokenHashHex, err := GenerateStorePairingToken("tenant-1")
	if err != nil {
		t.Fatalf("GenerateStorePairingToken returned error: %v", err)
	}
	tenantID, parseErr := pairingtoken.TenantID(rawToken)
	if parseErr != nil || tenantID != "tenant-1" {
		t.Fatalf("TenantID = %q, %v; want tenant-1", tenantID, parseErr)
	}
	want, hashErr := pairingtoken.Hash(rawToken)
	if hashErr != nil {
		t.Fatalf("Hash returned error: %v", hashErr)
	}
	if tokenHashHex != want {
		t.Fatalf("expected token hash %q, got %q", want, tokenHashHex)
	}
}
