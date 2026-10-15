package edgeauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

// parseEdgePublicKeys parses "kid=base64url,kid2=base64url". A malformed
// entry fails the whole set so a typo never silently disables verification.
func parseEdgePublicKeys(raw string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, encoded, ok := strings.Cut(entry, "=")
		kid, encoded = strings.TrimSpace(kid), strings.TrimRight(strings.TrimSpace(encoded), "=")
		key, err := base64.RawURLEncoding.DecodeString(encoded)
		if !ok || kid == "" || err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("edge_signature_public_key_invalid")
		}
		keys[kid] = ed25519.PublicKey(key)
	}
	return keys, nil
}

// verifyEd25519Envelope checks a "<kid>.<base64url signature>" value.
func verifyEd25519Envelope(keys map[string]ed25519.PublicKey, presented, payload string) bool {
	kid, encoded, ok := strings.Cut(presented, ".")
	if !ok {
		return false
	}
	key, known := keys[strings.TrimSpace(kid)]
	if !known {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(key, []byte(payload), sig)
}
