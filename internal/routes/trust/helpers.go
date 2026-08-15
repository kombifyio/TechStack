// Package trust provides helper functions for pairing-token management.
package trust

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// GeneratePairingToken generates the retired opaque token format used only by
// the PocketBase compatibility path.
func GeneratePairingToken() (rawToken string, tokenHashHex string, err error) {
	// 24 bytes = 32 chars-ish in base64url, good enough for one-time pairing
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	rawToken = base64.RawURLEncoding.EncodeToString(b)
	tokenHashHex, err = pairingtoken.Hash(rawToken)
	return rawToken, tokenHashHex, err
}

// GenerateStorePairingToken generates the versioned tenant-routable format
// required for RLS-scoped binary download and worker registration.
func GenerateStorePairingToken(tenantID string) (rawToken string, tokenHashHex string, err error) {
	return pairingtoken.Generate(tenantID)
}
