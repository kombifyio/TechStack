// Package edgeauthtest signs caller-populated edge identity headers for tests.
// Applications must obtain identity and authority from the real Gateway.
package edgeauthtest

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
)

// Config selects a common wire layout and signing material. Secret alone
// produces HMAC, SigningKey alone Ed25519, and both produce a dual envelope.
// KeyID binds the payload (default primary); SigningKeyID labels only the
// Ed25519 signature and may differ. At and Nonce default per invocation;
// set both for deterministic expiry/replay cases. SignedPath overrides the
// request's escaped path plus raw query, permitting wrong-target fixtures.
type Config struct {
	Version      string
	Secret       string
	KeyID        string
	SigningKey   ed25519.PrivateKey
	SigningKeyID string
	At           time.Time
	Nonce        string
	SignedPath   string
}

// Sign overwrites only transport fields: timestamp, nonce, key id, signed path
// and signatures. It clears unused signature headers when changing modes.
// Identity, service, entitlement and step-up headers must come from the caller.
// Supported versions are v1/v2/v4/v5/v7/v9; AI-owned v6/v8 are separate wires.
//
// Current edgeauth middleware derives version-dependent context from the HMAC
// signature field. Ed25519-only cryptographic acceptance therefore does not
// imply v7/v9 step-up/email/AI context parity. Use dual mode for Gateway parity;
// never synthesize a HMAC marker to manufacture those capabilities.
func Sign(t testing.TB, req *http.Request, cfg Config) {
	t.Helper()
	validate(t, req, cfg)
	secret := strings.TrimSpace(cfg.Secret)
	kid, signedPath, timestamp, nonce := transport(t, req, cfg)
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set(edgeauth.HeaderEdgeTimestamp, timestamp)
	req.Header.Set(edgeauth.HeaderEdgeNonce, nonce)
	req.Header.Set(edgeauth.HeaderEdgeKeyID, kid)
	req.Header.Set(edgeauth.HeaderEdgeSignedPath, signedPath)
	req.Header.Del(edgeauth.HeaderEdgeSignature)
	req.Header.Del(edgeauth.HeaderEdgeSignatureEd25519)
	wire := payload(cfg.Version, req, kid, signedPath, timestamp, nonce)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(wire))
		req.Header.Set(edgeauth.HeaderEdgeSignature, cfg.Version+"="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	}
	if cfg.SigningKey != nil {
		sig := ed25519.Sign(cfg.SigningKey, []byte(wire))
		req.Header.Set(edgeauth.HeaderEdgeSignatureEd25519, cfg.Version+"="+cfg.SigningKeyID+"."+base64.RawURLEncoding.EncodeToString(sig))
	}
}

func validate(t testing.TB, req *http.Request, cfg Config) {
	t.Helper()
	switch cfg.Version {
	case "v1", "v2", "v4", "v5", "v7", "v9":
	default:
		t.Fatalf("edgeauthtest: unsupported explicit version %q", cfg.Version)
	}
	if req == nil || req.URL == nil {
		t.Fatal("edgeauthtest: request and URL required")
	}
	if strings.TrimSpace(cfg.Secret) == "" && cfg.SigningKey == nil {
		t.Fatal("edgeauthtest: signing material required")
	}
	if cfg.SigningKey != nil && (len(cfg.SigningKey) != ed25519.PrivateKeySize || strings.TrimSpace(cfg.SigningKeyID) == "" || strings.ContainsAny(cfg.SigningKeyID, ".=\r\n")) {
		t.Fatal("edgeauthtest: invalid Ed25519 signing key or id")
	}
}

func transport(t testing.TB, req *http.Request, cfg Config) (kid, signedPath, timestamp, nonce string) {
	t.Helper()
	at := cfg.At
	if at.IsZero() {
		at = time.Now()
	}
	nonce = cfg.Nonce
	if nonce == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatalf("edgeauthtest: nonce: %v", err)
		}
		nonce = base64.RawURLEncoding.EncodeToString(raw[:])
	}
	kid = strings.TrimSpace(cfg.KeyID)
	if kid == "" {
		kid = "primary"
	}
	signedPath = cfg.SignedPath
	if signedPath == "" {
		signedPath = req.URL.EscapedPath()
		if signedPath == "" {
			signedPath = "/"
		}
		if req.URL.RawQuery != "" {
			signedPath += "?" + req.URL.RawQuery
		}
	}
	timestamp = strconv.FormatInt(at.Unix(), 10)
	return kid, signedPath, timestamp, nonce
}

// Kept test-side: the runtime verifier must remain an independent implementation.
// Fixed production-TypeScript vectors in edgeauth_test pin every supported layout.
func payload(version string, req *http.Request, kid, path, timestamp, nonce string) string {
	h := func(name string) string { return strings.TrimSpace(req.Header.Get(name)) }
	stepUp := version == "v7" || version == "v9"
	issuer := version == "v4" || version == "v5" || stepUp
	fields := []string{version, kid, strings.ToUpper(req.Method), path,
		h(edgeauth.HeaderEdgeAuth), h(edgeauth.HeaderEdgeService), h(edgeauth.HeaderPublicPrefix)}
	if issuer {
		fields = append(fields, h(edgeauth.HeaderUserIssuer))
	}
	fields = append(fields, h(edgeauth.HeaderUserID), h(edgeauth.HeaderOrgID), h(edgeauth.HeaderUserEmail),
		h(edgeauth.HeaderUserTier), h(edgeauth.HeaderUserRoles), h(edgeauth.HeaderUserScope))
	if version != "v1" {
		fields = append(fields, h(edgeauth.HeaderEntitlements), h(edgeauth.HeaderKnowledgeTier))
	}
	if issuer {
		fields = append(fields, h(edgeauth.HeaderClientID), h(edgeauth.HeaderProductID), h(edgeauth.HeaderAIWorkload))
	}
	if version == "v5" || stepUp {
		fields = append(fields, h(edgeauth.HeaderResourceScope))
	}
	if stepUp {
		fields = append(fields, h(edgeauth.HeaderPrincipalType), h(edgeauth.HeaderAgentID), h(edgeauth.HeaderAgentClass),
			h(edgeauth.HeaderAgentPerimeter), h(edgeauth.HeaderAgentPolicy), h(edgeauth.HeaderUserAMR), h(edgeauth.HeaderUserACR), h(edgeauth.HeaderUserAuthTime))
	}
	if version == "v9" {
		fields = append(fields, h(edgeauth.HeaderUserEmailVerified))
	}
	return strings.Join(append(fields, timestamp, nonce), "\n")
}
