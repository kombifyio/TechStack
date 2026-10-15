package guardidentity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// AccessTokenTTL is the lifetime of a key-bound access token.
	AccessTokenTTL = 10 * time.Minute
	// TokenType is the Authorization scheme of a key-bound access token.
	TokenType = "DPoP"

	accessTokenPrefix = "tsga"
	accessTokenDomain = "kombify-techstack/guard-access-token/v1"
)

// AccessClaims binds one access token to a worker, a key and the credential
// it was exchanged for. Cred is a non-reversible fingerprint of the stored
// credential hash, so rotating the credential invalidates outstanding tokens
// immediately instead of after their lifetime.
type AccessClaims struct {
	TenantID  string `json:"tid"`
	WorkerID  string `json:"wid"`
	JKT       string `json:"jkt"`
	Cred      string `json:"cred"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// IssueAccessToken signs a short-lived access token bound to a key thumbprint
// (the RFC 9449 cnf.jkt confirmation).
func IssueAccessToken(secret []byte, claims AccessClaims, now time.Time) (string, time.Time, error) {
	if len(secret) == 0 {
		return "", time.Time{}, errors.New("guard access token secret is not configured")
	}
	if claims.TenantID == "" || claims.WorkerID == "" || claims.JKT == "" {
		return "", time.Time{}, errors.New("guard access token requires tenant, worker and key binding")
	}
	if now.IsZero() {
		now = time.Now()
	}
	claims.IssuedAt = now.Unix()
	expires := now.Add(AccessTokenTTL)
	claims.ExpiresAt = expires.Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	segment := base64.RawURLEncoding.EncodeToString(payload)
	return accessTokenPrefix + "." + segment + "." + signAccessToken(secret, segment), expires, nil
}

// VerifyAccessToken authenticates an access token and checks its lifetime. It
// returns CodeTokenExpired for an expired token and CodeProofInvalid
// otherwise; binding to a key and to current server state is the caller's
// job.
func VerifyAccessToken(secret []byte, token string, now time.Time) (AccessClaims, error) {
	var claims AccessClaims
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(secret) == 0 || len(parts) != 3 || parts[0] != accessTokenPrefix {
		return claims, Denied(CodeProofInvalid, ReasonTokenInvalid)
	}
	if !hmac.Equal([]byte(signAccessToken(secret, parts[1])), []byte(parts[2])) {
		return claims, Denied(CodeProofInvalid, ReasonTokenInvalid)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil ||
		claims.TenantID == "" || claims.WorkerID == "" || claims.JKT == "" || claims.ExpiresAt <= 0 {
		return AccessClaims{}, Denied(CodeProofInvalid, ReasonTokenInvalid)
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Unix() >= claims.ExpiresAt {
		return AccessClaims{}, Denied(CodeTokenExpired, "")
	}
	return claims, nil
}

// CredentialFingerprint derives the Cred claim from the stored credential hash.
func CredentialFingerprint(credentialSHA256 string) string {
	mac := hmac.New(sha256.New, []byte(accessTokenDomain))
	_, _ = mac.Write([]byte(credentialSHA256))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
}

func signAccessToken(secret []byte, segment string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(accessTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(segment))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
