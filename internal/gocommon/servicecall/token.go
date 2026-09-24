package servicecall

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Token-validation errors. Kept package-level so callers can type-switch.
var (
	ErrBadToken         = errors.New("servicecall: malformed token")
	ErrBadSignature     = errors.New("servicecall: invalid signature")
	ErrExpired          = errors.New("servicecall: token expired")
	ErrNotYetValid      = errors.New("servicecall: token not yet valid")
	ErrEmptySecret      = errors.New("servicecall: empty signing secret")
	ErrWrongAudience    = errors.New("servicecall: wrong audience")
	ErrCallerDenied     = errors.New("servicecall: caller not in allowlist")
	ErrMissingExpiry    = errors.New("servicecall: missing exp claim")
	ErrMissingIssuedAt  = errors.New("servicecall: missing iat claim")
	ErrLifetimeExceeded = errors.New("servicecall: token lifetime exceeds maximum")
	ErrIssuerMismatch   = errors.New("servicecall: iss does not match svc")
)

// Fixed header bytes for HS256 JWT. Matches the standard `{"alg":"HS256","typ":"JWT"}`.
// We emit the same bytes every time so the encoded prefix is deterministic and
// we don't need to parse the header on verify.
var jwtHeader = []byte(`{"alg":"HS256","typ":"JWT"}`)

// IssueToken builds a signed service-call token from cfg and the supplied
// target/obo/requestID. cfg.ServiceName and cfg.Secret must be set.
func IssueToken(cfg Config, target string, obo *OnBehalfOf, requestID string) (string, error) {
	if cfg.Secret == "" {
		return "", ErrEmptySecret
	}
	ttl := cfg.TokenTTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}
	now := time.Now()
	claims := Claims{
		Iss:        "kombify-" + cfg.ServiceName,
		Aud:        "kombify-" + target,
		Iat:        now.Unix(),
		Exp:        now.Add(ttl).Unix(),
		Svc:        cfg.ServiceName,
		OnBehalfOf: obo,
		RequestID:  requestID,
	}
	return signClaims(claims, cfg.Secret)
}

// VerifyToken parses and validates token against primary (and optionally
// secretNext during rotation). Returns the decoded claims on success.
// Enforces the SERVICE-TO-SERVICE-AUTH-STANDARD claim rules: exp and iat are
// required, the lifetime is capped at MaxTokenTTL, iat may not be in the
// future beyond the clock skew, and iss must be "kombify-" + svc.
// Does NOT enforce audience or caller policy — that is the middleware's job.
func VerifyToken(token, primary, next string) (*Claims, error) {
	c, err := ParseAndVerifySignature(token, primary, next)
	if err != nil {
		return nil, err
	}
	if err := validateClaims(c, time.Now()); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseAndVerifySignature validates the JWT signature and decodes the claims
// without applying the time or issuer policy. It exists for cross-language
// wire-format checks; authentication paths must use VerifyToken.
func ParseAndVerifySignature(token, primary, next string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrBadToken
	}
	if !verifyHMAC(signingInput, sig, primary) && !verifyHMAC(signingInput, sig, next) {
		return nil, ErrBadSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrBadToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrBadToken
	}
	return &c, nil
}

func validateClaims(c *Claims, now time.Time) error {
	if c.Exp <= 0 {
		return ErrMissingExpiry
	}
	if c.Iat <= 0 {
		return ErrMissingIssuedAt
	}
	if c.Exp <= c.Iat || time.Duration(c.Exp-c.Iat)*time.Second > MaxTokenTTL {
		return ErrLifetimeExceeded
	}
	if now.Unix() > c.Exp {
		return ErrExpired
	}
	if now.Add(clockSkewTolerance).Unix() < c.Iat {
		return ErrNotYetValid
	}
	if c.Svc == "" || c.Iss != "kombify-"+c.Svc {
		return ErrIssuerMismatch
	}
	return nil
}

func signClaims(claims Claims, secret string) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(jwtHeader) +
		"." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := mac.Sum(nil)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func verifyHMAC(signingInput string, sig []byte, secret string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return hmac.Equal(mac.Sum(nil), sig)
}
