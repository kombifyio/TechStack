// Package guardidentity implements the host-bound proof-of-possession identity
// of kombify Guard: an Ed25519 key generated on the server, RFC 7638 key
// thumbprints, DPoP-shaped (RFC 9449) request proofs and the short-lived
// key-bound access tokens the control plane derives from them.
//
// The package is shared by the Guard agent (signing) and the control plane
// (verification) so both sides agree on one wire shape. It performs no I/O and
// never logs key material.
package guardidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

const (
	jwkKeyType = "OKP"
	jwkCurve   = "Ed25519"
)

// JWK is the public half of a Guard identity key (RFC 8037 OKP/Ed25519).
// Only kty, crv and x exist on the type: a private member ("d") or any other
// parameter is rejected when a JWK is parsed from the wire.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
}

// GenerateKey creates a new Guard identity key from the system CSPRNG.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Guard identity key: %w", err)
	}
	return key, nil
}

// JWKFromPublic renders an Ed25519 public key as a JWK.
func JWKFromPublic(key ed25519.PublicKey) JWK {
	return JWK{Kty: jwkKeyType, Crv: jwkCurve, X: base64.RawURLEncoding.EncodeToString(key)}
}

// ParseJWK decodes a public JWK strictly. Unknown members (in particular a
// private "d") make the key unacceptable instead of being silently dropped.
func ParseJWK(raw []byte) (JWK, error) {
	var jwk JWK
	if len(raw) == 0 || len(raw) > 512 {
		return jwk, errors.New("guard identity key has an invalid size")
	}
	decoder := json.NewDecoder(bytesReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&jwk); err != nil {
		return JWK{}, errors.New("guard identity key is not a public OKP/Ed25519 JWK")
	}
	if _, err := jwk.PublicKey(); err != nil {
		return JWK{}, err
	}
	return jwk, nil
}

// PublicKey validates the JWK and returns the Ed25519 public key.
func (j JWK) PublicKey() (ed25519.PublicKey, error) {
	if j.Kty != jwkKeyType || j.Crv != jwkCurve {
		return nil, errors.New("guard identity key must be OKP/Ed25519")
	}
	raw, err := base64.RawURLEncoding.DecodeString(j.X)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("guard identity key has an invalid public value")
	}
	return ed25519.PublicKey(raw), nil
}

// Thumbprint returns the RFC 7638 SHA-256 thumbprint of the key, base64url
// encoded. The required members are serialized in lexicographic order with no
// whitespace, exactly as the RFC prescribes.
func (j JWK) Thumbprint() (string, error) {
	if _, err := j.PublicKey(); err != nil {
		return "", err
	}
	canonical := `{"crv":"` + j.Crv + `","kty":"` + j.Kty + `","x":"` + j.X + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// Canonical returns the stable JSON persisted with a key binding.
func (j JWK) Canonical() (string, error) {
	if _, err := j.PublicKey(); err != nil {
		return "", err
	}
	return `{"crv":"` + j.Crv + `","kty":"` + j.Kty + `","x":"` + j.X + `"}`, nil
}

// EncodePrivateKeyPEM serializes a private key as PKCS#8 PEM for the Guard
// state directory. The caller owns file permissions and atomic replacement.
func EncodePrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode Guard identity key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// DecodePrivateKeyPEM parses a PKCS#8 PEM Ed25519 private key.
func DecodePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("guard identity key file is not a PKCS#8 PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("guard identity key file could not be parsed")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("guard identity key file is not an Ed25519 key")
	}
	return key, nil
}

// PublicJWK returns the JWK of a private key.
func PublicJWK(key ed25519.PrivateKey) JWK {
	return JWKFromPublic(key.Public().(ed25519.PublicKey))
}
