package workerauth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	tokenPrefix = "tsra"
	defaultTTL  = 24 * time.Hour
)

var (
	ErrMissingSecret = errors.New("worker auth secret is not configured")
	ErrInvalidToken  = errors.New("invalid runtime agent token")
	ErrExpiredToken  = errors.New("runtime agent token expired")
)

type Claims struct {
	TenantID       string `json:"tenant_id"`
	OwnerID        string `json:"owner_id,omitempty"`
	StackID        string `json:"stack_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	ServerID       string `json:"server_id"`
	RuntimeAgentID string `json:"runtime_agent_id"`
	ExpiresAt      int64  `json:"exp"`
}

// OpaqueTokenContext binds one deterministic stateful runtime credential to
// the complete enrollment identity and the exact idempotent request that
// created its generation. The IdempotencyKey is intentionally never persisted;
// callers store only a keyed digest alongside the token hash.
type OpaqueTokenContext struct {
	TenantID       string
	OwnerID        string
	StackID        string
	ServerID       string
	RuntimeAgentID string
	RequestDigest  string
	IdempotencyKey string
	Generation     int64
}

// StrictSecretEnv, when true, makes the dedicated worker secret mandatory: the
// platform-wide SERVICE_AUTH_SECRET is then never used to sign or derive worker
// credentials. Turn it on once TECHSTACK_WORKER_AGENT_TOKEN_SECRET is
// provisioned; it fails a deployment that lacks the dedicated secret at boot
// (ErrDedicatedSecretRequired) instead of silently sharing one key between the
// service identity and every worker credential.
const StrictSecretEnv = "TECHSTACK_WORKER_SECRET_STRICT"

// ErrDedicatedSecretRequired reports strict mode without a dedicated secret.
var ErrDedicatedSecretRequired = errors.New("workerauth: " + StrictSecretEnv + " requires TECHSTACK_WORKER_AGENT_TOKEN_SECRET")

func dedicatedSecret() string {
	return firstNonEmpty(
		os.Getenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET"),
		os.Getenv("TECHSTACK_WORKER_TOKEN_SECRET"),
	)
}

// Strict reports whether the dedicated secret is mandatory.
func Strict() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(StrictSecretEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// SharesServiceSecret reports that worker credentials are keyed with the
// platform-wide SERVICE_AUTH_SECRET because no dedicated secret is configured.
func SharesServiceSecret() bool {
	return dedicatedSecret() == "" && !Strict() && strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET")) != ""
}

// CheckStrict fails when strict mode is on and the dedicated secret is missing.
func CheckStrict() error {
	if Strict() && dedicatedSecret() == "" {
		return ErrDedicatedSecretRequired
	}
	return nil
}

// SecretFromEnv returns the base secret of worker credentials: the dedicated
// secret, or, unless strict mode forbids it, the service secret.
func SecretFromEnv() []byte {
	secret := dedicatedSecret()
	if secret == "" && !Strict() {
		secret = strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))
	}
	if secret == "" {
		return nil
	}
	return []byte(secret)
}

// NextSecretFromEnv returns the rotation successor of the shared service secret
// (SERVICE_AUTH_SECRET_NEXT) while worker credentials are keyed with it. A
// dedicated worker secret has its own rotation, so then there is no successor.
// Credentials are verified against it after the current secret and never
// signed with it.
func NextSecretFromEnv() []byte {
	if dedicatedSecret() != "" || Strict() {
		return nil
	}
	next := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET_NEXT"))
	if next == "" || next == strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET")) {
		return nil
	}
	return []byte(next)
}

// VerifyRotating verifies a signed worker token against the current secret and,
// while a SERVICE_AUTH_SECRET rotation is open, against its successor.
func VerifyRotating(token string, now time.Time) (Claims, error) {
	claims, err := Verify(SecretFromEnv(), token, now)
	if err == nil || errors.Is(err, ErrExpiredToken) {
		return claims, err
	}
	if next := NextSecretFromEnv(); len(next) > 0 {
		if nextClaims, nextErr := Verify(next, token, now); nextErr == nil || errors.Is(nextErr, ErrExpiredToken) {
			return nextClaims, nextErr
		}
	}
	return claims, err
}

// PurposeKey derives one key per purpose from the base secret (HKDF-SHA256,
// the purpose is the info label). A token minted for one purpose never verifies
// under another, and a leak of a derived key does not reveal the base secret.
func PurposeKey(base []byte, purpose string) []byte {
	if len(base) == 0 || strings.TrimSpace(purpose) == "" {
		return nil
	}
	key, err := hkdf.Key(sha256.New, base, nil, "kombify-techstack/worker-auth/"+purpose, 32)
	if err != nil {
		return nil
	}
	return key
}

func Issue(secret []byte, claims Claims, now time.Time, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", ErrMissingSecret
	}
	claims.TenantID = strings.TrimSpace(claims.TenantID)
	claims.OwnerID = strings.TrimSpace(claims.OwnerID)
	claims.StackID = strings.TrimSpace(claims.StackID)
	claims.LeaseID = strings.TrimSpace(claims.LeaseID)
	claims.ServerID = strings.TrimSpace(claims.ServerID)
	claims.RuntimeAgentID = strings.TrimSpace(claims.RuntimeAgentID)
	if claims.TenantID == "" || claims.ServerID == "" || claims.RuntimeAgentID == "" {
		return "", fmt.Errorf("%w: tenant_id, server_id, and runtime_agent_id are required", ErrInvalidToken)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	claims.ExpiresAt = now.Add(ttl).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadSegment := base64.RawURLEncoding.EncodeToString(payload)
	signature := sign(secret, payloadSegment)
	return strings.Join([]string{tokenPrefix, payloadSegment, signature}, "."), nil
}

func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	var claims Claims
	if len(secret) == 0 {
		return claims, ErrMissingSecret
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		return claims, ErrInvalidToken
	}
	if !hmac.Equal([]byte(sign(secret, parts[1])), []byte(parts[2])) {
		return claims, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, ErrInvalidToken
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, ErrInvalidToken
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if claims.ExpiresAt <= 0 {
		return claims, ErrInvalidToken
	}
	if now.Unix() >= claims.ExpiresAt {
		return claims, ErrExpiredToken
	}
	claims.TenantID = strings.TrimSpace(claims.TenantID)
	claims.OwnerID = strings.TrimSpace(claims.OwnerID)
	claims.StackID = strings.TrimSpace(claims.StackID)
	claims.LeaseID = strings.TrimSpace(claims.LeaseID)
	claims.ServerID = strings.TrimSpace(claims.ServerID)
	claims.RuntimeAgentID = strings.TrimSpace(claims.RuntimeAgentID)
	if claims.TenantID == "" || claims.ServerID == "" || claims.RuntimeAgentID == "" {
		return claims, ErrInvalidToken
	}
	return claims, nil
}

func OpaqueToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return tokenPrefix + ".opaque." + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// DeriveOpaqueToken deterministically derives a 256-bit opaque credential for
// one credential generation. Domain-separated HMAC means a retry can recover
// the exact token without storing reversible credential material, while any
// identity, request, idempotency key, or generation change yields a different
// credential.
func DeriveOpaqueToken(secret []byte, ctx OpaqueTokenContext) (string, error) {
	if len(secret) == 0 {
		return "", ErrMissingSecret
	}
	ctx.TenantID = strings.TrimSpace(ctx.TenantID)
	ctx.OwnerID = strings.TrimSpace(ctx.OwnerID)
	ctx.StackID = strings.TrimSpace(ctx.StackID)
	ctx.ServerID = strings.TrimSpace(ctx.ServerID)
	ctx.RuntimeAgentID = strings.TrimSpace(ctx.RuntimeAgentID)
	ctx.RequestDigest = strings.TrimSpace(ctx.RequestDigest)
	ctx.IdempotencyKey = strings.TrimSpace(ctx.IdempotencyKey)
	if ctx.TenantID == "" || ctx.OwnerID == "" || ctx.ServerID == "" ||
		ctx.RuntimeAgentID == "" || ctx.RequestDigest == "" ||
		ctx.IdempotencyKey == "" || ctx.Generation <= 0 {
		return "", fmt.Errorf("%w: complete identity, request digest, idempotency key, and positive generation are required", ErrInvalidToken)
	}
	payload, err := json.Marshal(struct {
		Domain         string `json:"domain"`
		TenantID       string `json:"tenant_id"`
		OwnerID        string `json:"owner_id"`
		StackID        string `json:"stack_id"`
		ServerID       string `json:"server_id"`
		RuntimeAgentID string `json:"runtime_agent_id"`
		RequestDigest  string `json:"request_digest"`
		IdempotencyKey string `json:"idempotency_key"`
		Generation     int64  `json:"generation"`
	}{
		Domain:   "kombify-techstack/runtime-agent-opaque/v1",
		TenantID: ctx.TenantID, OwnerID: ctx.OwnerID, StackID: ctx.StackID,
		ServerID: ctx.ServerID, RuntimeAgentID: ctx.RuntimeAgentID,
		RequestDigest: ctx.RequestDigest, IdempotencyKey: ctx.IdempotencyKey,
		Generation: ctx.Generation,
	})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return tokenPrefix + ".opaque." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// KeyedDigest returns a domain-separated SHA-256 HMAC suitable for persisting
// a comparison value for sensitive idempotency material.
func KeyedDigest(secret []byte, domain, value string) (string, error) {
	if len(secret) == 0 {
		return "", ErrMissingSecret
	}
	domain = strings.TrimSpace(domain)
	value = strings.TrimSpace(value)
	if domain == "" || value == "" {
		return "", errors.New("worker auth keyed digest requires domain and value")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// IsSignedToken reports whether token follows the signed runtime-agent token
// wire format. It does not validate the signature; callers that need trust
// must still call Verify with the configured secret.
func IsSignedToken(token string) bool {
	parts := strings.Split(strings.TrimSpace(token), ".")
	return len(parts) == 3 && parts[0] == tokenPrefix && parts[1] != "opaque" && parts[1] != "" && parts[2] != ""
}

// IsOpaqueToken reports whether token is a well-formed stateful runtime-agent
// credential. Opaque credentials are authorized only through their exact
// server-side hash and worker binding; this function does not authenticate one.
func IsOpaqueToken(token string) bool {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] != "opaque" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil && len(raw) == 32
}

func SHA256Hex(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
