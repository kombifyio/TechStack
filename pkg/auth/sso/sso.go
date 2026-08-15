// Package sso provides JWT verification for SSO tokens from kombify Cloud Portal.
// Tokens are HMAC-signed (HS256) with a shared secret.
package sso

import (
	"errors"
	"fmt"
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
	Sub           string         `json:"sub"`   // User ID from kombify Cloud
	Email         string         `json:"email"` // User email
	Name          string         `json:"name"`  // User display name
	Tool          string         `json:"tool"`  // Tool ID (e.g., "kombifystack")
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
	// Secret is the HMAC secret used for signing tokens (SSO_JWT_SECRET env var)
	Secret string

	// AllowedTools is the list of tool IDs that are accepted (e.g., ["kombifystack"])
	// If empty, tool validation is skipped
	AllowedTools []string

	// ClockSkew is the allowed clock skew for token validation (default: 30s)
	ClockSkew time.Duration
}

// Verifier handles SSO token verification.
type Verifier struct {
	secret       []byte
	allowedTools map[string]struct{}
	clockSkew    time.Duration
}

// ssoCustomClaims extends jwt.RegisteredClaims with our custom fields
type ssoCustomClaims struct {
	jwt.RegisteredClaims
	Email         string         `json:"email"`
	Name          string         `json:"name"`
	Tool          string         `json:"tool"`
	StackIdentity *StackIdentity `json:"stackIdentity,omitempty"`
	AccessToken   string         `json:"accessToken,omitempty"`
}

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

	return &Verifier{
		secret:       []byte(cfg.Secret),
		allowedTools: allowedTools,
		clockSkew:    clockSkew,
	}, nil
}

// Verify parses and validates an SSO JWT token string.
// It verifies the HMAC-SHA256 signature, expiration, and tool claim.
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

	// Build the payload
	payload := &SSOTokenPayload{
		Sub:           claims.Subject,
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

	// Return the secret for HMAC verification
	// golang-jwt/jwt uses crypto/subtle internally for HMAC comparison
	return v.secret, nil
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
	return nil
}

// mapError converts jwt library errors to our error types.
func (v *Verifier) mapError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrTokenExpired
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return ErrTokenNotYetValid
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

// VerifyFromEnv creates a Verifier using the SSO_JWT_SECRET environment variable.
// This is a convenience function for common use cases.
func VerifyFromEnv(secret string, allowedTools []string) (*Verifier, error) {
	return NewVerifier(Config{
		Secret:       secret,
		AllowedTools: allowedTools,
	})
}

// IsExpired checks if the error indicates an expired token.
func IsExpired(err error) bool {
	return errors.Is(err, ErrTokenExpired)
}

// IsInvalid checks if the error indicates an invalid token.
func IsInvalid(err error) bool {
	return errors.Is(err, ErrTokenInvalid)
}

// IsInvalidTool checks if the error indicates an invalid tool claim.
func IsInvalidTool(err error) bool {
	return errors.Is(err, ErrInvalidTool)
}
