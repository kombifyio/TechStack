// Package sso provides JWT verification for SSO tokens from kombify Cloud Portal.
// Tokens are HMAC-signed (HS256) with the Techstack-only portal SSO secret.
package sso

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Error types for SSO token verification
var (
	// ErrMissingSecret indicates the SSO secret is not configured
	ErrMissingSecret = errors.New("sso: missing JWT secret")

	// ErrTokenExpired indicates the token has expired
	ErrTokenExpired = errors.New("sso: token has expired")

	// ErrTokenInvalid indicates the token is malformed or signature is invalid
	ErrTokenInvalid = errors.New("sso: token is invalid")

	// ErrInvalidTool indicates the tool claim is not in the allowed list
	ErrInvalidTool = errors.New("sso: tool not allowed")

	// ErrTokenNotYetValid indicates the token's iat is in the future
	ErrTokenNotYetValid = errors.New("sso: token not yet valid")

	// ErrMissingClaims indicates required claims are missing from the token
	ErrMissingClaims = errors.New("sso: required claims missing")
)

// SSOTokenPayload represents the parsed claims from an SSO JWT token.
// This matches the payload structure created by kombify Cloud Portal.
type SSOTokenPayload struct {
	ID            string         `json:"jti"`                 // Single-use token ID
	Sub           string         `json:"sub"`                 // User ID from kombify Cloud
	TenantID      string         `json:"tenant_id,omitempty"` // Canonical isolation tenant from kombify Cloud
	Email         string         `json:"email"`               // User email
	Name          string         `json:"name"`                // User display name
	Tool          string         `json:"tool"`                // Tool ID (e.g., "kombifystack")
	StackIdentity *StackIdentity `json:"stackIdentity,omitempty"`
	IssuedAt      int64          `json:"iat"`                   // Issued at timestamp (Unix seconds)
	ExpiresAt     int64          `json:"exp"`                   // Expiration timestamp (Unix seconds)
	AccessToken   string         `json:"accessToken,omitempty"` // Optional kombify Cloud access token
}

type StackIdentity struct {
	Name           string `json:"name"`
	CharacterID    string `json:"characterId"`
	AnimationStyle string `json:"animationStyle"`
}

// Config holds the configuration for SSO token verification.
type Config struct {
	// Secret is the HMAC secret Cloud signs Techstack launch tokens with
	// (TECHSTACK_SSO_JWT_SECRET).
	Secret string

	// NextSecret is the optional rotation slot (TECHSTACK_SSO_JWT_SECRET_NEXT);
	// a token signed with either secret verifies.
	NextSecret string

	// AllowedTools is the list of tool IDs that are accepted (e.g., ["kombifystack"])
	// If empty, tool validation is skipped
	AllowedTools []string

	// ClockSkew is the allowed clock skew for token validation (default: 30s)
	ClockSkew time.Duration

	// MaxLifetime caps exp - iat (default: DefaultMaxLifetime).
	MaxLifetime time.Duration
}

// DefaultMaxLifetime bounds a launch token's exp - iat. Cloud mints five
// minutes; a longer-lived token widens the window for a captured token.
const DefaultMaxLifetime = 10 * time.Minute

// maxTokenIDLength bounds the jti kept in the replay guard.
const maxTokenIDLength = 128

// Verifier handles SSO token verification.
type Verifier struct {
	keys         []jwt.VerificationKey
	allowedTools map[string]struct{}
	clockSkew    time.Duration
	maxLifetime  time.Duration
}

// ssoCustomClaims extends jwt.RegisteredClaims with our custom fields
type ssoCustomClaims struct {
	jwt.RegisteredClaims
	TenantID      string         `json:"tenant_id,omitempty"`
	Email         string         `json:"email"`
	Name          string         `json:"name"`
	Tool          string         `json:"tool"`
	StackIdentity *StackIdentity `json:"stackIdentity,omitempty"`
	AccessToken   string         `json:"accessToken,omitempty"`
}

// CloudIssuer is the issuer kombify Cloud stamps on portal SSO tokens. Cloud
// also sets aud to "kombify-tool:<tool>", iat and a short exp
// (kombify-Cloud src/lib/server/sso-jwt.ts). SSO_JWT_SECRET is shared with other
// Cloud token types, so the verifier binds tokens to that exact contract.
const CloudIssuer = "kombify-cloud"

// NewVerifier creates a new SSO token verifier with the given configuration.
// Returns ErrMissingSecret if the secret is empty.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Secret == "" {
		return nil, ErrMissingSecret
	}

	allowedTools := make(map[string]struct{}, len(cfg.AllowedTools))
	for _, tool := range cfg.AllowedTools {
		allowedTools[tool] = struct{}{}
	}

	clockSkew := cfg.ClockSkew
	if clockSkew == 0 {
		clockSkew = 30 * time.Second
	}

	maxLifetime := cfg.MaxLifetime
	if maxLifetime == 0 {
		maxLifetime = DefaultMaxLifetime
	}

	keys := []jwt.VerificationKey{[]byte(cfg.Secret)}
	if cfg.NextSecret != "" {
		keys = append(keys, []byte(cfg.NextSecret))
	}

	return &Verifier{
		keys:         keys,
		allowedTools: allowedTools,
		clockSkew:    clockSkew,
		maxLifetime:  maxLifetime,
	}, nil
}

// Verify parses and validates an SSO JWT token string.
// It verifies the HMAC-SHA256 signature, the Cloud issuer, required iat and
// exp, the tool claim, and an audience bound to that tool.
// Returns the parsed payload on success, or an appropriate error.
func (v *Verifier) Verify(tokenString string) (*SSOTokenPayload, error) {
	if tokenString == "" {
		return nil, fmt.Errorf("%w: empty token", ErrTokenInvalid)
	}

	// Parse and validate the token
	token, err := jwt.ParseWithClaims(
		tokenString,
		&ssoCustomClaims{},
		v.keyFunc,
		jwt.WithLeeway(v.clockSkew),
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(CloudIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return nil, v.mapError(err)
	}

	if !token.Valid {
		return nil, ErrTokenInvalid
	}

	// Extract claims
	claims, ok := token.Claims.(*ssoCustomClaims)
	if !ok {
		return nil, fmt.Errorf("%w: could not parse claims", ErrTokenInvalid)
	}

	// Validate required fields
	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	// Validate tool claim if allowedTools is configured
	if len(v.allowedTools) > 0 {
		if _, allowed := v.allowedTools[claims.Tool]; !allowed {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTool, claims.Tool)
		}
	}

	// A Cloud tool-API token or a token minted for another tool carries a
	// different audience; only a launch token for this tool is accepted.
	if !slices.Contains(claims.Audience, "kombify-tool:"+claims.Tool) {
		return nil, fmt.Errorf("%w: audience", ErrTokenInvalid)
	}

	// Build the payload
	payload := &SSOTokenPayload{
		ID:            claims.ID,
		Sub:           claims.Subject,
		TenantID:      claims.TenantID,
		Email:         claims.Email,
		Name:          claims.Name,
		Tool:          claims.Tool,
		StackIdentity: claims.StackIdentity,
		AccessToken:   claims.AccessToken,
	}

	// Extract timestamps
	if claims.IssuedAt != nil {
		payload.IssuedAt = claims.IssuedAt.Unix()
	}
	if claims.ExpiresAt != nil {
		payload.ExpiresAt = claims.ExpiresAt.Unix()
	}

	return payload, nil
}

// keyFunc provides the HMAC key for token verification.
// It also validates the signing method is HMAC.
func (v *Verifier) keyFunc(token *jwt.Token) (interface{}, error) {
	// Validate signing algorithm - must be HMAC
	if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
		return nil, fmt.Errorf("%w: unexpected signing method %v", ErrTokenInvalid, token.Header["alg"])
	}

	// golang-jwt tries each key of the set (current secret and rotation
	// slot) and compares HMACs in constant time.
	return jwt.VerificationKeySet{Keys: v.keys}, nil
}

// validateClaims checks that required claims are present.
func (v *Verifier) validateClaims(claims *ssoCustomClaims) error {
	if claims.Subject == "" {
		return fmt.Errorf("%w: sub", ErrMissingClaims)
	}
	if claims.Email == "" {
		return fmt.Errorf("%w: email", ErrMissingClaims)
	}
	if claims.Tool == "" {
		return fmt.Errorf("%w: tool", ErrMissingClaims)
	}
	// jwt.WithIssuedAt only checks iat when present; Cloud always sets it.
	if claims.IssuedAt == nil {
		return fmt.Errorf("%w: iat", ErrMissingClaims)
	}
	// A single-use token needs an ID; see ReplayGuard.
	if claims.ID == "" || len(claims.ID) > maxTokenIDLength {
		return fmt.Errorf("%w: jti", ErrMissingClaims)
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt.Time) > v.maxLifetime {
		return fmt.Errorf("%w: lifetime exceeds %s", ErrTokenInvalid, v.maxLifetime)
	}
	return nil
}

// mapError converts jwt library errors to our error types.
func (v *Verifier) mapError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrTokenExpired
	case errors.Is(err, jwt.ErrTokenNotValidYet), errors.Is(err, jwt.ErrTokenUsedBeforeIssued):
		return ErrTokenNotYetValid
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		return fmt.Errorf("%w: %v", ErrMissingClaims, err)
	case errors.Is(err, jwt.ErrTokenMalformed):
		return fmt.Errorf("%w: malformed", ErrTokenInvalid)
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return fmt.Errorf("%w: invalid signature", ErrTokenInvalid)
	case errors.Is(err, jwt.ErrSignatureInvalid):
		return fmt.Errorf("%w: signature mismatch", ErrTokenInvalid)
	default:
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
}
