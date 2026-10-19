package servicecall

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// timeNow is the verifier clock; tests may replace it.
var timeNow = time.Now

// Asymmetric service identity (AUTHENTICATION.md §8, platform
// review 2026-10-07 H2). Each caller holds its own Ed25519 private key and
// signs EdDSA tokens; receivers hold only public keys, each bound to exactly
// one caller service. A receiver can therefore no longer mint tokens, and a
// caller can no longer speak for another service, which the shared HS256
// SERVICE_AUTH_SECRET allowed.
//
// The cutover needs no flag day:
//  1. receivers add SERVICE_AUTH_ED25519_PUBLIC_KEYS and keep accepting HS256;
//  2. callers add SERVICE_AUTH_ED25519_PRIVATE_KEY and sign EdDSA from then on;
//  3. receivers set SERVICE_AUTH_REQUIRE_ED25519=true; only then is the
//     shared secret removed from that receiver.
const (
	// #nosec G101 -- public environment-variable identifier, not a secret value.
	EnvEd25519PrivateKey = "SERVICE_AUTH_ED25519_PRIVATE_KEY"
	EnvEd25519PublicKeys = "SERVICE_AUTH_ED25519_PUBLIC_KEYS"
	EnvRequireEd25519    = "SERVICE_AUTH_REQUIRE_ED25519"
)

// Ed25519 verification errors.
var (
	ErrUnknownKey         = errors.New("servicecall: unknown signing key")
	ErrKeyServiceMismatch = errors.New("servicecall: signing key belongs to another service")
	ErrAlgorithmRejected  = errors.New("servicecall: token algorithm not accepted")
	ErrInvalidKeyMaterial = errors.New("servicecall: invalid Ed25519 key material")
)

// CallerPublicKey is a verification key bound to one caller service id.
type CallerPublicKey struct {
	Service string
	Key     ed25519.PublicKey
}

type jwtHeaderFields struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	Typ string `json:"typ,omitempty"`
}

// ParseSigningKey parses "<kid>=<base64url 32-byte seed>".
func ParseSigningKey(raw string) (kid string, key ed25519.PrivateKey, err error) {
	kid, encoded, ok := strings.Cut(strings.TrimSpace(raw), "=")
	kid = strings.TrimSpace(kid)
	seed, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(encoded), "="))
	if !ok || !validKeyID(kid) || decodeErr != nil || len(seed) != ed25519.SeedSize {
		return "", nil, ErrInvalidKeyMaterial
	}
	return kid, ed25519.NewKeyFromSeed(seed), nil
}

// ParseCallerPublicKeys parses "<svc>:<kid>=<base64url key>,...". A malformed
// entry or a duplicate kid fails the whole set, so a typo never silently
// disables verification.
func ParseCallerPublicKeys(raw string) (map[string]CallerPublicKey, error) {
	keys := map[string]CallerPublicKey{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		binding, encoded, ok := strings.Cut(entry, "=")
		service, kid, bound := strings.Cut(binding, ":")
		service, kid = strings.ToLower(strings.TrimSpace(service)), strings.TrimSpace(kid)
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(encoded), "="))
		if !ok || !bound || service == "" || !validKeyID(kid) || err != nil || len(key) != ed25519.PublicKeySize {
			return nil, ErrInvalidKeyMaterial
		}
		if _, dup := keys[kid]; dup {
			return nil, fmt.Errorf("%w: duplicate kid %q", ErrInvalidKeyMaterial, kid)
		}
		keys[kid] = CallerPublicKey{Service: service, Key: ed25519.PublicKey(key)}
	}
	return keys, nil
}

// LoadEd25519FromEnv fills the Ed25519 fields of cfg that are still empty from
// SERVICE_AUTH_ED25519_PRIVATE_KEY, SERVICE_AUTH_ED25519_PUBLIC_KEYS and
// SERVICE_AUTH_REQUIRE_ED25519. Absent variables leave cfg unchanged.
func LoadEd25519FromEnv(cfg *Config) error {
	if cfg.SigningKey == nil {
		if raw := strings.TrimSpace(os.Getenv(EnvEd25519PrivateKey)); raw != "" {
			kid, key, err := ParseSigningKey(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", EnvEd25519PrivateKey, err)
			}
			cfg.SigningKeyID, cfg.SigningKey = kid, key
		}
	}
	if cfg.CallerPublicKeys == nil {
		if raw := strings.TrimSpace(os.Getenv(EnvEd25519PublicKeys)); raw != "" {
			keys, err := ParseCallerPublicKeys(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", EnvEd25519PublicKeys, err)
			}
			cfg.CallerPublicKeys = keys
		}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvRequireEd25519)), "true") {
		cfg.RequireEd25519 = true
	}
	return nil
}

// GenerateSigningKey creates a new caller key pair. It returns the private
// value for SERVICE_AUTH_ED25519_PRIVATE_KEY and the public entry the caller's
// receivers append to SERVICE_AUTH_ED25519_PUBLIC_KEYS.
func GenerateSigningKey(service, kid string) (privateValue, publicEntry string, err error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "" || !validKeyID(kid) {
		return "", "", ErrInvalidKeyMaterial
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return kid + "=" + base64.RawURLEncoding.EncodeToString(private.Seed()),
		service + ":" + kid + "=" + base64.RawURLEncoding.EncodeToString(public), nil
}

func validKeyID(kid string) bool {
	if kid == "" || len(kid) > 64 {
		return false
	}
	for i := 0; i < len(kid); i++ {
		c := kid[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func signEd25519(claims Claims, kid string, key ed25519.PrivateKey) (string, error) {
	header, err := json.Marshal(jwtHeaderFields{Alg: "EdDSA", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signingInput))), nil
}

// verifyWithConfig authenticates token under cfg: EdDSA tokens against the
// caller-bound public keys, HS256 tokens against the shared secrets unless
// cfg.RequireEd25519 is set. Claim policy is applied afterwards.
func verifyWithConfig(token string, cfg Config) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrBadToken
	}
	var header jwtHeaderFields
	if json.Unmarshal(rawHeader, &header) != nil {
		return nil, ErrBadToken
	}
	if header.Alg == "EdDSA" {
		return verifyEd25519Token(parts, header.Kid, cfg.CallerPublicKeys)
	}
	if header.Alg != "HS256" || cfg.RequireEd25519 {
		return nil, ErrAlgorithmRejected
	}
	return VerifyToken(token, cfg.Secret, cfg.SecretNext)
}

func verifyEd25519Token(parts []string, kid string, keys map[string]CallerPublicKey) (*Claims, error) {
	caller, known := keys[kid]
	if !known || len(caller.Key) != ed25519.PublicKeySize {
		return nil, ErrUnknownKey
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrBadToken
	}
	if !ed25519.Verify(caller.Key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, ErrBadSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrBadToken
	}
	var c Claims
	if json.Unmarshal(payload, &c) != nil {
		return nil, ErrBadToken
	}
	if err = validateClaims(&c, timeNow()); err != nil {
		return nil, err
	}
	// The key, not the claim, names the caller.
	if !strings.EqualFold(c.Svc, caller.Service) {
		return nil, ErrKeyServiceMismatch
	}
	return &c, nil
}
