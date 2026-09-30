// Package edgeauth provides middleware that trusts headers set by the
// Cloudflare Edge Router after Auth0 JWT validation.
//
// The Cloudflare Edge Router (kombify-Gateway/cloudflare-edge):
//  1. Validates the incoming Bearer JWT against Auth0 JWKS (RS256).
//  2. Strips any client-injected x-user-*, x-org-*, and x-kombify-edge-* headers.
//  3. Injects x-user-*, x-org-*, x-kombify-edge-auth, route context, and HMAC
//     signature headers from the validated JWT claims or service identity.
//
// In production, configure EDGE_AUTH_SECRET so the origin verifies that the
// downstream identity headers were set by the edge after real JWT validation.
//
// New code must use identity.FromContext() for the canonical identity. The
// legacy kong package has been removed (Kong decommissioned 2026-04-17).
//
// Self-hosted deployments (StackKits, Simulate CLI, SpeechKit) do not go through
// the CF Edge Router. They should set Enabled=false and handle auth locally.
package edgeauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
)

// HeaderEdgeAuth is the edge identity marker set by the CF Edge Router after
// successful edge validation. Marker presence alone is not authorization proof;
// middleware must also verify the signed edge envelope.
const (
	HeaderEdgeAuth      = "X-Kombify-Edge-Auth"
	EdgeAuthValueJWT    = "auth0-jwt"
	EdgeAuthValueAPIKey = "api-key"
)

// Headers forwarded from the edge (same names as Kong used).
const (
	HeaderUserID         = "X-User-ID"
	HeaderUserIssuer     = "X-User-Issuer"
	HeaderOrgID          = "X-Org-ID"
	HeaderUserEmail      = "X-User-Email"
	HeaderUserRoles      = "X-User-Roles"
	HeaderUserTier       = "X-User-Tier"
	HeaderUserScope      = "X-User-Scope"
	HeaderRequestID      = "X-Request-ID"
	HeaderEdgeService    = "X-Kombify-Edge-Service"
	HeaderPublicPrefix   = "X-Kombify-Public-Prefix"
	HeaderEdgeSignature  = "X-Kombify-Edge-Signature"
	HeaderEdgeTimestamp  = "X-Kombify-Edge-Timestamp"
	HeaderEdgeNonce      = "X-Kombify-Edge-Nonce"
	HeaderEdgeSignedPath = "X-Kombify-Edge-Signed-Path"
	HeaderEdgeKeyID      = "X-Kombify-Edge-Key-ID"
	// HeaderEntitlements and HeaderKnowledgeTier are forwarded from the edge and
	// consumed by AI-Platform access policy / knowledge tiering. They are bound by
	// the v2 edge signature so they cannot be forged downstream.
	HeaderEntitlements     = "X-Kombify-Entitlements"
	HeaderKnowledgeTier    = "X-Kombify-Knowledge-Tier"
	HeaderClientID         = "X-Kombify-Client-ID"
	HeaderProductID        = "X-Kombify-Product-ID"
	HeaderAIWorkload       = "X-Kombify-AI-Workload"
	HeaderResourceScope    = "X-Kombify-Resource-Scope"
	edgeSignatureVersion   = "v1"
	edgeSignatureVersionV2 = "v2"
	// EdgeSignatureVersionV4 binds the verified issuer and OAuth client plus
	// the client-registration-derived AI product/workload context. Origins may
	// use AIContextBoundBySignature only after Middleware accepted the request.
	EdgeSignatureVersionV4 = "v4"
	// EdgeSignatureVersionV5 additionally binds the Gateway-selected resource
	// scope used by AI data isolation, budgets, and provider-key custody.
	EdgeSignatureVersionV5 = "v5"
	// EdgeSignatureVersionV7 binds every v6 field (issuer, AI context,
	// resource scope, principal and agent attestation) plus the step-up facts
	// amr, acr and auth_time. Only a v7 envelope yields StepUpClaims.
	EdgeSignatureVersionV7 = "v7"
	defaultEdgeKeyID       = "primary"
	defaultEdgeNextKeyID   = "next"
	defaultSignatureWindow = 5 * time.Minute
)

// Headers bound only by the v7 edge signature.
const (
	// Principal and agent attestation, bound from v6 (Gateway) and by v7 here.
	HeaderPrincipalType  = "X-Kombify-Principal-Type"
	HeaderAgentID        = "X-Kombify-Agent-ID"
	HeaderAgentClass     = "X-Kombify-Agent-Class"
	HeaderAgentPerimeter = "X-Kombify-Agent-Perimeter"
	HeaderAgentPolicy    = "X-Kombify-Agent-Policy"
	// Step-up facts of the verified Auth0 token, bound only by v7:
	// comma-separated `amr`, the `acr` value and `auth_time` in Unix seconds.
	HeaderUserAMR      = "X-User-AMR"
	HeaderUserACR      = "X-User-ACR"
	HeaderUserAuthTime = "X-User-Auth-Time"
)

// Config configures the edge auth middleware.
type Config struct {
	// Enabled controls whether the middleware is active.
	// Defaults to false so self-hosted (StackKits, Simulate, SpeechKit) are safe.
	Enabled bool

	// RequireEdgeAuth forces a 401 when the X-Kombify-Edge-Auth header is
	// absent or has an unexpected value. Set to true for SaaS-only services.
	// When false, requests without the header proceed without identity context.
	RequireEdgeAuth bool

	// EdgeAuthSecret verifies HMAC-signed identity headers from the Cloudflare
	// Edge Router. When empty, EDGE_AUTH_SECRET is read from the environment.
	EdgeAuthSecret string

	// EdgeAuthNextSecret is accepted during dual-key rotation. When empty,
	// EDGE_AUTH_SECRET_NEXT is read from the environment.
	EdgeAuthNextSecret string

	// EdgeAuthKeyID identifies EdgeAuthSecret in the signed envelope. When
	// empty, EDGE_AUTH_KEY_ID is read from the environment, then "primary".
	EdgeAuthKeyID string

	// EdgeAuthNextKeyID identifies EdgeAuthNextSecret in the signed envelope.
	// When empty, EDGE_AUTH_KEY_ID_NEXT is read from the environment, then "next".
	EdgeAuthNextKeyID string

	// RequireSignature is retained for source compatibility. Edge-authenticated
	// requests and unsigned identity headers are always verified fail-closed.
	RequireSignature bool

	// SignatureWindow is the maximum allowed skew for X-Kombify-Edge-Timestamp.
	// Defaults to 5 minutes.
	SignatureWindow time.Duration
}

// Middleware returns an http.Handler middleware that:
//  1. Skips if Enabled=false.
//  2. Requires a supported X-Kombify-Edge-Auth marker value.
//  3. Verifies the signed edge envelope fail-closed.
//  4. On success: extracts identity, stores in context via identity.NewContext.
//  5. On missing header: rejects (401) if RequireEdgeAuth=true, else passes through.
//
// Use identity.FromContext(ctx) to retrieve the identity in handlers.
func Middleware(cfg Config) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			edgeAuth := strings.TrimSpace(r.Header.Get(HeaderEdgeAuth))
			if edgeAuth == "" {
				if hasUnsignedIdentityHeaders(r) {
					w.Header().Set("WWW-Authenticate", `Bearer realm="api.kombify.io"`)
					http.Error(w, `{"error":"edge_auth_required","reason":"edge_signature_missing"}`, http.StatusUnauthorized)
					return
				}
				if cfg.RequireEdgeAuth {
					w.Header().Set("WWW-Authenticate", `Bearer realm="api.kombify.io"`)
					http.Error(w, `{"error":"edge_auth_required","reason":"missing_edge_auth_header"}`, http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if !isSupportedEdgeAuthValue(edgeAuth) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="api.kombify.io"`)
				http.Error(w, `{"error":"edge_auth_required","reason":"invalid_edge_auth_header"}`, http.StatusUnauthorized)
				return
			}

			if err := verifyEdgeSignature(r, cfg); err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="api.kombify.io"`)
				http.Error(w, fmt.Sprintf(`{"error":"edge_auth_required","reason":"%s"}`, err.Error()), http.StatusUnauthorized)
				return
			}

			id := extractIdentity(r)
			ctx := identity.NewContext(r.Context(), id)
			if claims, ok := stepUpClaimsFromVerifiedRequest(r); ok {
				ctx = context.WithValue(ctx, stepUpContextKey{}, claims)
			} else {
				// Below v7 the step-up headers are unsigned: drop them so no
				// handler can read them as if they were verified.
				r.Header.Del(HeaderUserAMR)
				r.Header.Del(HeaderUserACR)
				r.Header.Del(HeaderUserAuthTime)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func verifyEdgeSignature(r *http.Request, cfg Config) error {
	primary := strings.TrimSpace(firstNonEmpty(cfg.EdgeAuthSecret, os.Getenv("EDGE_AUTH_SECRET")))
	next := strings.TrimSpace(firstNonEmpty(cfg.EdgeAuthNextSecret, os.Getenv("EDGE_AUTH_SECRET_NEXT")))
	primaryKeyID := strings.TrimSpace(firstNonEmpty(cfg.EdgeAuthKeyID, os.Getenv("EDGE_AUTH_KEY_ID"), defaultEdgeKeyID))
	nextKeyID := strings.TrimSpace(firstNonEmpty(cfg.EdgeAuthNextKeyID, os.Getenv("EDGE_AUTH_KEY_ID_NEXT"), defaultEdgeNextKeyID))
	hasSecret := primary != "" || next != ""
	if !hasSecret {
		return fmt.Errorf("edge_signature_secret_missing")
	}

	signature := strings.TrimSpace(r.Header.Get(HeaderEdgeSignature))
	timestamp := strings.TrimSpace(r.Header.Get(HeaderEdgeTimestamp))
	nonce := strings.TrimSpace(r.Header.Get(HeaderEdgeNonce))
	signedPath := strings.TrimSpace(r.Header.Get(HeaderEdgeSignedPath))
	keyID := strings.TrimSpace(r.Header.Get(HeaderEdgeKeyID))
	if signature == "" || timestamp == "" || nonce == "" || signedPath == "" || keyID == "" {
		return fmt.Errorf("edge_signature_missing")
	}
	version, err := edgeSignatureVersionFromHeader(signature)
	if err != nil {
		return err
	}
	if signedPath != requestSignedPath(r) {
		return fmt.Errorf("edge_signature_path_mismatch")
	}
	if err := validateTimestamp(timestamp, cfg.SignatureWindow); err != nil {
		return err
	}

	payload := buildSignaturePayload(version, r.Method, signedPath, keyID, r, timestamp, nonce)
	presented := strings.TrimPrefix(signature, version+"=")
	for _, candidate := range []struct {
		keyID  string
		secret string
	}{
		{keyID: primaryKeyID, secret: primary},
		{keyID: nextKeyID, secret: next},
	} {
		if candidate.secret == "" {
			continue
		}
		if keyID != candidate.keyID {
			continue
		}
		expected := signPayload(candidate.secret, payload)
		if hmac.Equal([]byte(presented), []byte(expected)) {
			return nil
		}
	}
	return fmt.Errorf("edge_signature_invalid")
}

// edgeSignatureVersionFromHeader returns the supported signature version encoded
// in the "<version>=<sig>" header. Older versions stay accepted during the
// staged rollout; origins separately decide which protected fields they need.
func edgeSignatureVersionFromHeader(signature string) (string, error) {
	switch {
	case strings.HasPrefix(signature, EdgeSignatureVersionV7+"="):
		return EdgeSignatureVersionV7, nil
	case strings.HasPrefix(signature, EdgeSignatureVersionV5+"="):
		return EdgeSignatureVersionV5, nil
	case strings.HasPrefix(signature, EdgeSignatureVersionV4+"="):
		return EdgeSignatureVersionV4, nil
	case strings.HasPrefix(signature, edgeSignatureVersionV2+"="):
		return edgeSignatureVersionV2, nil
	case strings.HasPrefix(signature, edgeSignatureVersion+"="):
		return edgeSignatureVersion, nil
	default:
		return "", fmt.Errorf("edge_signature_version_invalid")
	}
}

// buildSignaturePayload reconstructs the edge-signature payload for the given
// signature version. v1 binds identity + route headers; v2 additionally binds
// entitlements and knowledge tier; v4 additionally binds the verified issuer,
// OAuth client, and client-registration-derived product/workload context. v5
// additionally binds the selected resource scope. v7 additionally binds the
// principal/agent attestation and the step-up facts; its issuer may be empty,
// as in the Gateway's v6. The field order must match the Cloudflare edge signer
// (kombify-Gateway/cloudflare-edge/src/edge-signature.ts).
func buildSignaturePayload(version, method, signedPath, keyID string, r *http.Request, timestamp, nonce string) string {
	header := func(name string) string { return strings.TrimSpace(r.Header.Get(name)) }
	bindsIssuer := version == EdgeSignatureVersionV4 || version == EdgeSignatureVersionV5 || version == EdgeSignatureVersionV7
	bindsEntitlements := version == edgeSignatureVersionV2 || bindsIssuer
	fields := []string{
		version,
		keyID,
		strings.ToUpper(method),
		signedPath,
		header(HeaderEdgeAuth),
		header(HeaderEdgeService),
		header(HeaderPublicPrefix),
	}
	if bindsIssuer {
		fields = append(fields, header(HeaderUserIssuer))
	}
	fields = append(fields,
		header(HeaderUserID),
		header(HeaderOrgID),
		header(HeaderUserEmail),
		header(HeaderUserTier),
		header(HeaderUserRoles),
		header(HeaderUserScope),
	)
	if bindsEntitlements {
		fields = append(fields, header(HeaderEntitlements), header(HeaderKnowledgeTier))
	}
	if bindsIssuer {
		fields = append(fields, header(HeaderClientID), header(HeaderProductID), header(HeaderAIWorkload))
	}
	if version == EdgeSignatureVersionV5 || version == EdgeSignatureVersionV7 {
		fields = append(fields, header(HeaderResourceScope))
	}
	if version == EdgeSignatureVersionV7 {
		fields = append(fields,
			header(HeaderPrincipalType),
			header(HeaderAgentID),
			header(HeaderAgentClass),
			header(HeaderAgentPerimeter),
			header(HeaderAgentPolicy),
			header(HeaderUserAMR),
			header(HeaderUserACR),
			header(HeaderUserAuthTime),
		)
	}
	fields = append(fields, timestamp, nonce)
	return strings.Join(fields, "\n")
}

// AIContextBoundBySignature reports whether the request's already-verified
// edge envelope cryptographically covers client, product, and workload. It is
// a version capability check, not independent authentication proof, and must
// only be used after Middleware has accepted the request.
func AIContextBoundBySignature(r *http.Request) bool {
	signature := strings.TrimSpace(r.Header.Get(HeaderEdgeSignature))
	return strings.HasPrefix(signature, EdgeSignatureVersionV4+"=") ||
		strings.HasPrefix(signature, EdgeSignatureVersionV5+"=") ||
		strings.HasPrefix(signature, EdgeSignatureVersionV7+"=")
}

// AIResourceContextBoundBySignature reports whether the already-verified edge
// envelope also covers the resource scope. Call it only after Middleware has
// accepted the request.
func AIResourceContextBoundBySignature(r *http.Request) bool {
	signature := strings.TrimSpace(r.Header.Get(HeaderEdgeSignature))
	return strings.HasPrefix(signature, EdgeSignatureVersionV5+"=") ||
		strings.HasPrefix(signature, EdgeSignatureVersionV7+"=")
}

func signPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func requestSignedPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return path
}

func validateTimestamp(raw string, window time.Duration) error {
	return validateTimestampAt(raw, window, time.Now())
}

func validateTimestampAt(raw string, window time.Duration, now time.Time) error {
	if window <= 0 {
		window = defaultSignatureWindow
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("edge_signature_timestamp_invalid")
	}
	ts := time.Unix(seconds, 0)
	if ts.Before(now.Add(-window)) || ts.After(now.Add(window)) {
		return fmt.Errorf("edge_signature_timestamp_out_of_window")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isSupportedEdgeAuthValue(value string) bool {
	return strings.EqualFold(value, EdgeAuthValueJWT) || strings.EqualFold(value, EdgeAuthValueAPIKey)
}

func hasUnsignedIdentityHeaders(r *http.Request) bool {
	for _, header := range []string{
		HeaderUserID,
		HeaderUserIssuer,
		HeaderOrgID,
		HeaderUserEmail,
		HeaderUserRoles,
		HeaderUserTier,
		HeaderUserScope,
		HeaderEntitlements,
		HeaderKnowledgeTier,
		HeaderClientID,
		HeaderProductID,
		HeaderAIWorkload,
		HeaderResourceScope,
		HeaderUserAMR,
		HeaderUserACR,
		HeaderUserAuthTime,
		HeaderEdgeSignature,
		HeaderEdgeTimestamp,
		HeaderEdgeNonce,
		HeaderEdgeSignedPath,
		HeaderEdgeKeyID,
	} {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return true
		}
	}
	return false
}

// extractIdentity builds an Identity from the edge-injected request headers.
func extractIdentity(r *http.Request) *identity.Identity {
	id := &identity.Identity{
		UserID: strings.TrimSpace(r.Header.Get(HeaderUserID)),
		OrgID:  strings.TrimSpace(r.Header.Get(HeaderOrgID)),
		Email:  strings.TrimSpace(r.Header.Get(HeaderUserEmail)),
		Tier:   strings.TrimSpace(r.Header.Get(HeaderUserTier)),
	}
	if rolesHeader := strings.TrimSpace(r.Header.Get(HeaderUserRoles)); rolesHeader != "" {
		for _, r := range strings.Split(rolesHeader, ",") {
			if t := strings.TrimSpace(r); t != "" {
				id.Roles = append(id.Roles, t)
			}
		}
	}
	return id
}

// IsEdgeAuthenticated reports whether the request carries a supported
// Cloudflare Edge Router identity marker. It does not verify the signature and
// must not be used as an authorization decision.
func IsEdgeAuthenticated(r *http.Request) bool {
	return isSupportedEdgeAuthValue(strings.TrimSpace(r.Header.Get(HeaderEdgeAuth)))
}

// StepUpClaims are the authentication facts a verified v7 envelope binds:
// the Auth0 `amr` values, the `acr` value and `auth_time`. A zero AuthTime
// means the token named none.
type StepUpClaims struct {
	AMR      []string
	ACR      string
	AuthTime time.Time
}

type stepUpContextKey struct{}

// authTimePattern is the strict auth_time form shared with the TypeScript
// verifier: positive decimal Unix seconds, no sign, spaces or leading zero.
var authTimePattern = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

// StepUpFromContext returns the step-up claims Middleware attached after
// verifying a v7 envelope. Any other version, an unverified request or a
// disabled middleware yields ok=false: origins treat that as "no fresh
// multi-factor sign-in" and fail closed.
func StepUpFromContext(ctx context.Context) (StepUpClaims, bool) {
	if ctx == nil {
		return StepUpClaims{}, false
	}
	claims, ok := ctx.Value(stepUpContextKey{}).(StepUpClaims)
	if !ok {
		return StepUpClaims{}, false
	}
	claims.AMR = append([]string(nil), claims.AMR...)
	return claims, true
}

// stepUpClaimsFromVerifiedRequest reads the step-up headers of a request
// whose envelope already verified. Only v7 covers them; below v7 they are
// unsigned and ignored.
func stepUpClaimsFromVerifiedRequest(r *http.Request) (StepUpClaims, bool) {
	if !strings.HasPrefix(strings.TrimSpace(r.Header.Get(HeaderEdgeSignature)), EdgeSignatureVersionV7+"=") {
		return StepUpClaims{}, false
	}
	claims := StepUpClaims{ACR: strings.TrimSpace(r.Header.Get(HeaderUserACR))}
	for _, method := range strings.Split(r.Header.Get(HeaderUserAMR), ",") {
		if method = strings.TrimSpace(method); method != "" {
			claims.AMR = append(claims.AMR, method)
		}
	}
	if raw := r.Header.Get(HeaderUserAuthTime); authTimePattern.MatchString(raw) {
		seconds, _ := strconv.ParseInt(raw, 10, 64)
		claims.AuthTime = time.Unix(seconds, 0).UTC()
	}
	return claims, true
}
