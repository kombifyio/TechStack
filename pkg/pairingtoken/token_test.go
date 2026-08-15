package pairingtoken

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestGenerateCreatesTenantRoutableHashedToken(t *testing.T) {
	rawToken, tokenHash, err := Generate("tenant-1")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tenantID, parseErr := TenantID(rawToken)
	if parseErr != nil || tenantID != "tenant-1" {
		t.Fatalf("TenantID = %q, %v; want tenant-1", tenantID, parseErr)
	}
	parsed, parseErr := Parse(rawToken)
	if parseErr != nil {
		t.Fatalf("Parse: %v", parseErr)
	}
	if tokenHash != parsed.TokenHash {
		t.Fatalf("hash = %q, want SHA256 of full token", tokenHash)
	}
	parts := strings.Split(rawToken, ".")
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != secretBytes {
		t.Fatalf("secret has %d bytes, %v; want %d", len(secret), err, secretBytes)
	}
}

func TestParseRejectsOversizedWireBeforeHashing(t *testing.T) {
	oversized := strings.Repeat(".", MaxWireBytes+1)
	if _, err := Parse(oversized); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse oversized error = %v, want ErrInvalidToken", err)
	}
	if _, err := Hash(oversized); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Hash oversized error = %v, want ErrInvalidToken", err)
	}
}

func TestTenantIDRejectsMalformedCurrentTokens(t *testing.T) {
	tenant := base64.RawURLEncoding.EncodeToString([]byte("tenant-1"))
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, secretBytes))
	for _, rawToken := range []string{
		"",
		"not-a-token",
		Prefix + "." + tenant,
		"kpt2." + tenant + "." + secret,
		Prefix + ".*." + secret,
		Prefix + "." + tenant + "=." + secret,
		Prefix + "." + tenant + ".short",
		Prefix + ".." + secret,
		Prefix + "." + tenant + "." + secret + ".extra",
	} {
		t.Run(rawToken, func(t *testing.T) {
			if _, err := TenantID(rawToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("TenantID(%q) error = %v, want ErrInvalidToken", rawToken, err)
			}
		})
	}
}

func TestTenantIDClassifiesOnlyCanonicalRetiredTokensAsLegacy(t *testing.T) {
	legacyBase64 := base64.RawURLEncoding.EncodeToString(make([]byte, legacySecretBytes))
	legacyKS := "ks_" + strings.Repeat("01", secretBytes)
	for _, legacy := range []string{legacyBase64, legacyKS} {
		if _, err := TenantID(legacy); !errors.Is(err, ErrLegacyToken) {
			t.Fatalf("TenantID(%q) error = %v, want ErrLegacyToken", legacy, err)
		}
	}
	for _, malformed := range []string{legacyBase64 + "=", legacyBase64[:len(legacyBase64)-1], "ks_" + strings.Repeat("01", secretBytes-1), "pairing-token-binary-test"} {
		if _, err := TenantID(malformed); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("TenantID(%q) error = %v, want ErrInvalidToken", malformed, err)
		}
	}
}

func TestGenerateRejectsUnsafeTenantLocators(t *testing.T) {
	for _, tenantID := range []string{"", " tenant-1", "tenant-1 ", "tenant\n1", strings.Repeat("x", maximumTenantBytes+1)} {
		if _, _, err := Generate(tenantID); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("Generate(%q) error = %v, want ErrInvalidToken", tenantID, err)
		}
	}
}
