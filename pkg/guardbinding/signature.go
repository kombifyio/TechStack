package guardbinding

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// signatureDomain separates binding signatures from every other Ed25519
// signature the same key could make.
const signatureDomain = "techstack.guard.command-binding/v1\x00"

// PinnedKey is one control-plane public key a Guard accepts binding
// signatures from.
type PinnedKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

// KeyIDOf names a public key by the first 16 hex characters of its SHA-256.
func KeyIDOf(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:])[:16]
}

// Signer signs the bindings the control plane offers one worker. Every worker
// has its own key, so a Guard accepts only bindings issued for it.
type Signer struct {
	key ed25519.PrivateKey
	pin PinnedKey
}

// NewSignerFromSeed builds a signer from an Ed25519 seed.
func NewSignerFromSeed(seed []byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("binding signing seed must be %d bytes", ed25519.SeedSize)
	}
	key := ed25519.NewKeyFromSeed(seed)
	public := key.Public().(ed25519.PublicKey)
	return &Signer{key: key, pin: PinnedKey{KeyID: KeyIDOf(public), PublicKey: base64.StdEncoding.EncodeToString(public)}}, nil
}

// workerKeyLabel separates per-worker binding keys from every other key
// derived from the same base secret.
const workerKeyLabel = "techstack.guard.command-binding-key/v1"

// DeriveWorkerSigner derives one worker's signer from the control plane's base
// secret (HKDF-SHA256 over a length-prefixed tenant and worker id). Each worker
// gets its own key, and a leaked worker key reveals neither the base secret nor
// another worker's key. The control plane stores no key: the same inputs always
// derive the same signer.
func DeriveWorkerSigner(base []byte, tenantID, workerID string) (*Signer, error) {
	if len(base) == 0 || tenantID == "" || workerID == "" {
		return nil, fmt.Errorf("binding key derivation needs a base secret, a tenant and a worker")
	}
	info := workerKeyLabel
	for _, field := range []string{tenantID, workerID} {
		info += fmt.Sprintf("|%d:%s", len(field), field)
	}
	seed, err := hkdf.Key(sha256.New, base, nil, info, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return NewSignerFromSeed(seed)
}

// PublicKeys is the pin set the worker's Guard receives at enrollment.
func (s *Signer) PublicKeys() []PinnedKey {
	if s == nil {
		return nil
	}
	return []PinnedKey{s.pin}
}

// Sign sets the binding's key id and signature. A nil signer leaves the
// binding unsigned.
func (s *Signer) Sign(b *Binding) error {
	if s == nil || b == nil {
		return nil
	}
	b.KeyID, b.Signature = s.pin.KeyID, ""
	message, err := b.signedMessage()
	if err != nil {
		return err
	}
	b.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.key, message))
	return nil
}

// VerifySignature checks the binding against the Guard's pin set. With no pins
// any binding passes, as before signing existed. Once a key is pinned, a
// binding without a valid signature from a pinned key is refused.
func (b Binding) VerifySignature(pins []PinnedKey) *Denial {
	if len(pins) == 0 {
		return nil
	}
	if b.Signature == "" {
		return Denyf(CodeBindingMismatch, "binding is unsigned but this Guard pins the control plane's signing key")
	}
	var public ed25519.PublicKey
	for _, pin := range pins {
		if pin.KeyID != b.KeyID {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(pin.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize || KeyIDOf(raw) != pin.KeyID {
			return Denyf(CodeBindingMismatch, "pinned key %q is malformed", pin.KeyID)
		}
		public = raw
	}
	if public == nil {
		return Denyf(CodeBindingMismatch, "binding is signed by key %q, which this Guard does not pin", b.KeyID)
	}
	signature, err := base64.RawURLEncoding.DecodeString(b.Signature)
	if err != nil {
		return Denyf(CodeBindingMismatch, "binding signature is malformed")
	}
	message, err := b.signedMessage()
	if err != nil || !ed25519.Verify(public, message, signature) {
		return Denyf(CodeBindingMismatch, "binding signature does not verify")
	}
	return nil
}

func (b Binding) signedMessage() ([]byte, error) {
	b.Signature = ""
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	return append([]byte(signatureDomain), canonical...), nil
}
