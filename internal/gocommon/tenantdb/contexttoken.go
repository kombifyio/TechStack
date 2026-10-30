package tenantdb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// ContextTokenSetting is the transaction-local Postgres setting that
	// carries the signed context token. kombify_ctx.verified_context() reads
	// and verifies it; nothing else in the database trusts it.
	ContextTokenSetting = "kombify.ctx_token" // #nosec G101 -- a setting name, not a credential.

	// ContextTokenHeader is the request header in which the trusted issuer
	// (the managed edge) hands the signed context token to a service.
	ContextTokenHeader = "X-Kombify-DB-Context" // #nosec G101 -- a header name, not a credential.

	// MaxContextTokenTTL is the longest lifetime the database accepts. Work
	// that outlives it is system work and runs under a system role.
	MaxContextTokenTTL = 15 * time.Minute

	contextTokenVersion = "v1"
	maxContextTokenLen  = 4096
	minContextKeyLen    = 32
)

var (
	ErrNoContextToken   = errors.New("tenantdb: no context token in context")
	ErrContextRejected  = errors.New("tenantdb: database rejected the context token")
	ErrContextMismatch  = errors.New("tenantdb: context token subject or org differs from the request identity")
	ErrInvalidClaims    = errors.New("tenantdb: invalid context claims")
	ErrInvalidSignerKey = errors.New("tenantdb: invalid context signing key")

	keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ContextClaims is the tenant context a trusted issuer binds to a verified
// principal. Audience lists the database login roles allowed to present the
// token; the database rejects it for any other session_user.
type ContextClaims struct {
	Subject   string
	OrgID     string
	Audience  []string
	IssuedAt  time.Time
	ExpiresAt time.Time
	// Scopes are exact capabilities admitted from the verified credential by
	// the trusted issuer. Never populate them from request input or RBAC-only
	// permissions. Empty preserves the legacy identity-only wire format.
	Scopes []string
	// Capabilities are separately admitted signed product entitlements. They are
	// never OAuth scope aliases or caller-requested permissions.
	Capabilities []string
}

type contextPayload struct {
	Sub string          `json:"sub"`
	Org string          `json:"org"`
	Aud []string        `json:"aud"`
	Iat int64           `json:"iat"`
	Exp int64           `json:"exp"`
	Scp json.RawMessage `json:"scp,omitempty"`
	Cap json.RawMessage `json:"cap,omitempty"`
}

// ContextSigner mints context tokens. Only the trusted issuer holds its key:
// the managed edge, which derives the claims from the authenticated
// principal. A runtime service never holds a signing key, otherwise faulty
// service code could mint any context.
type ContextSigner struct {
	keyID  string
	secret []byte
}

// NewContextSigner returns a signer for the key registered in the database
// under keyID (see RegisterContextKey). The secret must be at least 32 bytes.
func NewContextSigner(keyID string, secret []byte) (*ContextSigner, error) {
	if !keyIDPattern.MatchString(keyID) {
		return nil, fmt.Errorf("%w: key id must match %s", ErrInvalidSignerKey, keyIDPattern)
	}
	if len(secret) < minContextKeyLen {
		return nil, fmt.Errorf("%w: secret must be at least %d bytes", ErrInvalidSignerKey, minContextKeyLen)
	}
	return &ContextSigner{keyID: keyID, secret: append([]byte(nil), secret...)}, nil
}

// Sign returns a v1 context token for claims:
//
//	v1.<key id>.<base64url JSON payload>.<base64url HMAC-SHA256>
//
// The MAC covers "v1.<key id>.<payload>". Times are whole Unix seconds.
func (s *ContextSigner) Sign(claims ContextClaims) (string, error) {
	if err := validateClaims(claims); err != nil {
		return "", err
	}
	var scopes, capabilities json.RawMessage
	if len(claims.Scopes) > 0 {
		scopes, _ = json.Marshal(claims.Scopes)
	}
	if len(claims.Capabilities) > 0 {
		capabilities, _ = json.Marshal(claims.Capabilities)
	}
	payload, err := json.Marshal(contextPayload{
		Sub: claims.Subject,
		Org: claims.OrgID,
		Aud: claims.Audience,
		Iat: claims.IssuedAt.Unix(),
		Exp: claims.ExpiresAt.Unix(),
		Scp: scopes,
		Cap: capabilities,
	})
	if err != nil {
		return "", fmt.Errorf("tenantdb: encode context claims: %w", err)
	}
	signed := contextTokenVersion + "." + s.keyID + "." + base64.RawURLEncoding.EncodeToString(payload)
	token := signed + "." + base64.RawURLEncoding.EncodeToString(contextMAC(s.secret, signed))
	if len(token) > maxContextTokenLen {
		return "", fmt.Errorf("%w: token longer than %d bytes", ErrInvalidClaims, maxContextTokenLen)
	}
	return token, nil
}

func validateClaims(c ContextClaims) error {
	if !validContextCapabilities(c.Capabilities) {
		return fmt.Errorf("%w: invalid capability list", ErrInvalidClaims)
	}
	if !validContextScopes(c.Scopes) {
		return fmt.Errorf("%w: invalid scope list", ErrInvalidClaims)
	}
	if c.Subject == "" {
		return fmt.Errorf("%w: subject is required", ErrInvalidClaims)
	}
	if len(c.Audience) == 0 {
		return fmt.Errorf("%w: at least one audience is required", ErrInvalidClaims)
	}
	for _, aud := range c.Audience {
		if aud == "" {
			return fmt.Errorf("%w: empty audience", ErrInvalidClaims)
		}
	}
	if c.IssuedAt.IsZero() || c.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: issued-at and expiry are required", ErrInvalidClaims)
	}
	lifetime := c.ExpiresAt.Unix() - c.IssuedAt.Unix()
	if lifetime <= 0 || lifetime > int64(MaxContextTokenTTL/time.Second) {
		return fmt.Errorf("%w: lifetime must be within (0, %s]", ErrInvalidClaims, MaxContextTokenTTL)
	}
	return nil
}

func contextMAC(secret []byte, signed string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signed))
	return mac.Sum(nil)
}

// verifyContextToken is the Go reference of kombify_ctx.verified_context().
// Services do not call it (they hold no key); it pins the format the
// database verifies.
func verifyContextToken(token string, keys map[string][]byte, sessionUser string, now time.Time) (ContextClaims, bool) {
	p, ok := verifiedPayload(token, keys)
	if !ok || p.Sub == "" {
		return ContextClaims{}, false
	}
	audOK := false
	for _, aud := range p.Aud {
		audOK = audOK || aud == sessionUser
	}
	iat, exp := time.Unix(p.Iat, 0), time.Unix(p.Exp, 0)
	if !audOK || !exp.After(now) || iat.After(now.Add(time.Minute)) || !exp.After(iat) || exp.Sub(iat) > MaxContextTokenTTL {
		return ContextClaims{}, false
	}
	return ContextClaims{Subject: p.Sub, OrgID: p.Org, Audience: p.Aud, IssuedAt: iat, ExpiresAt: exp, Scopes: decodeContextScopes(p.Scp), Capabilities: decodeContextCapabilities(p.Cap)}, true
}

// verifiedPayload checks the token's shape and MAC and decodes its payload.
func verifiedPayload(token string, keys map[string][]byte) (contextPayload, bool) {
	var p contextPayload
	if token == "" || len(token) > maxContextTokenLen {
		return p, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != contextTokenVersion || !keyIDPattern.MatchString(parts[1]) {
		return p, false
	}
	secret, ok := keys[parts[1]]
	if !ok {
		return p, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !hmac.Equal(sig, contextMAC(secret, parts[0]+"."+parts[1]+"."+parts[2])) {
		return p, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || json.Unmarshal(raw, &p) != nil {
		return p, false
	}
	return p, true
}

type contextTokenKey struct{}

// NewContextWithToken stores the signed context token in ctx.
func NewContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, contextTokenKey{}, token)
}

// ContextTokenFromContext returns the signed context token stored in ctx.
func ContextTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(contextTokenKey{}).(string)
	return token
}

// ContextTokenMiddleware moves the issuer's token from ContextTokenHeader
// into the request context. The service does not need to trust the header:
// the database verifies the token, and a forged one matches zero rows.
func ContextTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get(ContextTokenHeader))
		if token != "" && len(token) <= maxContextTokenLen {
			r = r.WithContext(NewContextWithToken(r.Context(), token))
		}
		next.ServeHTTP(w, r)
	})
}
