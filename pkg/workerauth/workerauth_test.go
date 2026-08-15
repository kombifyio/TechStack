package workerauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestVerifyRejectsSignedTokenAtExpiration(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	token, err := Issue([]byte("secret"), Claims{
		TenantID: "tenant-1", ServerID: "server-1", RuntimeAgentID: "agent-1",
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify([]byte("secret"), token, now.Add(time.Second)); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("Verify at exp = %v, want ErrExpiredToken", err)
	}
	if _, err := Verify([]byte("rotated-secret"), token, now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify with rotated key = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsSignedTokenWithoutExpiration(t *testing.T) {
	claims := Claims{TenantID: "tenant-1", ServerID: "server-1", RuntimeAgentID: "agent-1"}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	segment := base64.RawURLEncoding.EncodeToString(payload)
	token := tokenPrefix + "." + segment + "." + sign([]byte("secret"), segment)
	if _, err := Verify([]byte("secret"), token, time.Now().UTC()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify without exp = %v, want ErrInvalidToken", err)
	}
}

func TestTokenKindClassification(t *testing.T) {
	opaque, err := OpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Issue([]byte("secret"), Claims{
		TenantID: "tenant-1", ServerID: "server-1", RuntimeAgentID: "agent-1",
	}, time.Now().UTC(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !IsOpaqueToken(opaque) || IsSignedToken(opaque) {
		t.Fatalf("opaque token misclassified: %q", opaque)
	}
	if !IsSignedToken(signed) || IsOpaqueToken(signed) {
		t.Fatalf("signed token misclassified: %q", signed)
	}
	for _, malformed := range []string{"tsra.opaque.short", "tsra.opaque.", "tsra.payload."} {
		if IsOpaqueToken(malformed) {
			t.Fatalf("malformed token classified as opaque: %q", malformed)
		}
	}
}
