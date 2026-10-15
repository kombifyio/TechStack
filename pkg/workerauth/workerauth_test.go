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

func TestVerifyRotatingAcceptsSuccessorSecretOnlyWhileSharedKeyRotates(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	claims := Claims{TenantID: "tenant-1", ServerID: "server-1", RuntimeAgentID: "agent-1"}
	mint := func(secret string) string {
		token, err := Issue([]byte(secret), claims, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_SECRET_STRICT", "")
	t.Setenv("SERVICE_AUTH_SECRET", "primary-secret")
	t.Setenv("SERVICE_AUTH_SECRET_NEXT", "next-secret")

	for _, secret := range []string{"primary-secret", "next-secret"} {
		if _, err := VerifyRotating(mint(secret), now); err != nil {
			t.Fatalf("token signed with %s: %v", secret, err)
		}
	}
	if _, err := VerifyRotating(mint("unknown-secret"), now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token signed with an unknown key = %v, want ErrInvalidToken", err)
	}

	// A dedicated worker secret has its own rotation: the service successor is not a key.
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "dedicated-secret")
	if _, err := VerifyRotating(mint("next-secret"), now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("successor accepted despite a dedicated worker secret: %v", err)
	}
}
