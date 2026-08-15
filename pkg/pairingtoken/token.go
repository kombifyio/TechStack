// Package pairingtoken defines the one-time capability used to bootstrap a
// worker into a tenant-scoped control plane.
package pairingtoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Prefix             = "kpt1"
	secretBytes        = 32
	legacySecretBytes  = 24
	maximumTenantBytes = 255
	// MaxWireBytes bounds every untrusted token before splitting, decoding, or
	// hashing. It is exactly kpt1 + separators + base64url(255-byte tenant) +
	// base64url(32-byte secret).
	MaxWireBytes = 389
)

var (
	// ErrInvalidToken means the token is neither a canonical current token nor
	// a canonical token emitted by the retired generator.
	ErrInvalidToken = errors.New("invalid pairing token")
	// ErrLegacyToken identifies a retired opaque wire format. Callers may attempt
	// their pre-RLS lookup path, but must not treat it as tenant-scoped.
	ErrLegacyToken = errors.New("legacy pairing token")
)

// ParsedToken is the bounded routing and authentication material derived from
// a canonical pairing-token wire value. Legacy tokens have an empty TenantID.
type ParsedToken struct {
	TenantID  string
	TokenHash string
	Legacy    bool
}

// Generate returns a versioned token carrying a non-secret tenant locator and
// the SHA-256 hash that must be stored. The locator selects the RLS scope only;
// possession is still authenticated by an exact hash match over the full token.
func Generate(tenantID string) (rawToken string, tokenHashHex string, err error) {
	if !validTenantID(tenantID) {
		return "", "", ErrInvalidToken
	}
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	rawToken = strings.Join([]string{
		Prefix,
		base64.RawURLEncoding.EncodeToString([]byte(tenantID)),
		base64.RawURLEncoding.EncodeToString(secret),
	}, ".")
	return rawToken, sha256Hex(rawToken), nil
}

// Parse validates and classifies a token before deriving its SHA-256 hash.
func Parse(rawToken string) (ParsedToken, error) {
	var parsed ParsedToken
	if len(rawToken) == 0 || len(rawToken) > MaxWireBytes {
		return parsed, ErrInvalidToken
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return parsed, ErrInvalidToken
	}
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 || parts[0] != Prefix {
		if isCanonicalLegacyToken(rawToken) {
			parsed.Legacy = true
			parsed.TokenHash = sha256Hex(rawToken)
			return parsed, nil
		}
		return parsed, ErrInvalidToken
	}

	tenantBytes, err := decodeCanonical(parts[1])
	if err != nil || len(tenantBytes) == 0 || len(tenantBytes) > maximumTenantBytes {
		return parsed, ErrInvalidToken
	}
	parsed.TenantID = string(tenantBytes)
	if !validTenantID(parsed.TenantID) {
		return ParsedToken{}, ErrInvalidToken
	}
	secret, err := decodeCanonical(parts[2])
	if err != nil || len(secret) != secretBytes {
		return ParsedToken{}, ErrInvalidToken
	}
	parsed.TokenHash = sha256Hex(rawToken)
	return parsed, nil
}

// TenantID returns the tenant locator from a canonical current token. A token
// in one of the exact retired opaque formats returns ErrLegacyToken so callers
// can keep a deliberately bounded compatibility path.
func TenantID(rawToken string) (string, error) {
	parsed, err := Parse(rawToken)
	if err != nil {
		return "", err
	}
	if parsed.Legacy {
		return "", ErrLegacyToken
	}
	return parsed.TenantID, nil
}

// Hash returns the full-token SHA-256 only after the wire value passes the
// strict length and canonical-format checks.
func Hash(rawToken string) (string, error) {
	parsed, err := Parse(rawToken)
	if err != nil {
		return "", err
	}
	return parsed.TokenHash, nil
}

func sha256Hex(rawToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawToken)))
	return hex.EncodeToString(sum[:])
}

func decodeCanonical(segment string) ([]byte, error) {
	if segment == "" || strings.Contains(segment, "=") {
		return nil, ErrInvalidToken
	}
	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != segment {
		return nil, ErrInvalidToken
	}
	return decoded, nil
}

func isCanonicalLegacyToken(rawToken string) bool {
	if rawToken == "" || strings.Contains(rawToken, ".") {
		return false
	}
	if strings.HasPrefix(rawToken, "ks_") {
		decoded, err := hex.DecodeString(strings.TrimPrefix(rawToken, "ks_"))
		return err == nil && len(decoded) == secretBytes
	}
	decoded, err := decodeCanonical(rawToken)
	return err == nil && len(decoded) == legacySecretBytes
}

func validTenantID(tenantID string) bool {
	if tenantID == "" || len(tenantID) > maximumTenantBytes || !utf8.ValidString(tenantID) || strings.TrimSpace(tenantID) != tenantID {
		return false
	}
	for _, r := range tenantID {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
