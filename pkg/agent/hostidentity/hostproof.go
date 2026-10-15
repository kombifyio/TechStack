package hostidentity

import (
	"errors"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// ErrNoIdentityKey means no identity key is loaded, so nothing can be signed.
var ErrNoIdentityKey = errors.New("kombify Guard identity key is not available")

// MintHostProof signs a host proof for one verifier audience with the Guard
// identity key. The key stays inside the Manager: callers receive only the
// compact JWS. A Guard whose identity is revoked or needs recovery signs
// nothing, so a stale or cloned state directory cannot keep reach alive.
func (m *Manager) MintHostProof(in guardidentity.HostProofInput) (string, error) {
	m.mu.Lock()
	key, state := m.key, m.rec.State
	m.mu.Unlock()
	if key == nil {
		return "", ErrNoIdentityKey
	}
	switch state {
	case StateRevoked:
		return "", ErrRevoked
	case StateRecoveryNeeded:
		return "", ErrRecoveryNeeded
	}
	return guardidentity.SignHostProof(key, in)
}
