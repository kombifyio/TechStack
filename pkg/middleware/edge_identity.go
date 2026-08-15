package middleware

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	commonedgeauth "github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

// userPlanContextKey is a context key for the user's subscription plan/tier.
type userPlanContextKey struct{}

type edgeAuthenticatedContextKey struct{}

// UserPlanFromContext retrieves the user plan from the request context.
// Returns empty string if not present.
func UserPlanFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	plan, _ := ctx.Value(userPlanContextKey{}).(string)
	return plan
}

func markEdgeAuthenticatedContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, edgeAuthenticatedContextKey{}, true)
}

func isEdgeAuthenticatedContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	edgeAuthenticated, _ := ctx.Value(edgeAuthenticatedContextKey{}).(bool)
	return edgeAuthenticated
}

// IsEdgeAuthenticated reports whether the request passed a verified Gateway
// edge envelope (or the explicitly configured legacy shared-secret hop).
// Product handlers use this only to decide whether edge-minted projections may
// be consumed; it grants no entitlement by itself.
func IsEdgeAuthenticated(ctx context.Context) bool {
	return isEdgeAuthenticatedContext(ctx)
}

// EdgeIdentityConfig holds configuration for Edge identity extraction.
type EdgeIdentityConfig struct {
	// Mode is the deployment mode (self-hosted or saas).
	Mode config.DeploymentMode
	// Secret is the shared secret that Edge injects via X-Edge-Auth-Secret.
	// If non-empty, requests missing the correct secret are treated as
	// unauthenticated (identity headers are NOT trusted).
	Secret string
	// EdgeAuthSecret verifies HMAC-signed Cloudflare edge identity headers.
	// Defaults to EDGE_AUTH_SECRET when empty.
	EdgeAuthSecret string
	// EdgeAuthNextSecret is accepted during edge-auth key rotation.
	// Defaults to EDGE_AUTH_SECRET_NEXT when empty.
	EdgeAuthNextSecret string
	// EdgeAuthKeyID and EdgeAuthNextKeyID identify the identity-envelope keys.
	// This keyring remains separate from the request-decision keyring.
	EdgeAuthKeyID     string
	EdgeAuthNextKeyID string
	// EdgeFlagsSecret signs request-bound TechStack v2 decisions. When empty,
	// EDGE_FLAGS_SECRET is used, then the identity secret as a compatibility
	// fallback. Legacy v1 rollout flags are never commercial authority.
	EdgeFlagsSecret     string
	EdgeFlagsNextSecret string
	EdgeFlagsKeyID      string
	EdgeFlagsNextKeyID  string
	// EdgeSignatureWindow is the maximum accepted timestamp skew.
	// Defaults to 5 minutes.
	EdgeSignatureWindow time.Duration
}

// EdgeIdentityMiddleware extracts Edge-injected identity headers
// (X-User-ID, X-Org-ID, X-User-Email, X-User-Roles, X-User-Plan) and stores them
// in the request context. Only active when DeploymentMode is "saas".
//
// If EdgeIdentityConfig.Secret is set, the middleware verifies that
// X-Edge-Auth-Secret matches before trusting identity headers.
// This prevents spoofing when services are reachable directly (bypassing Edge).
//
// In self-hosted mode this is a no-op pass-through.
func EdgeIdentityMiddleware(mode config.DeploymentMode) func(*httpx.Event) error {
	return EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{Mode: mode})
}

// EdgeIdentityMiddlewareFromConfig binds the Techstack runtime configuration
// to both the legacy shared-secret path and the signed edge envelope verifier.
// TECHSTACK_EDGE_AUTH_SECRET is loaded into Config.EdgeAuthSecret, so keeping
// this translation in one place prevents startup and integration servers from
// silently accepting only one of the two migration paths.
func EdgeIdentityMiddlewareFromConfig(cfg *config.Config) func(*httpx.Event) error {
	if cfg == nil {
		return EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{})
	}
	return EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:                cfg.DeploymentMode,
		Secret:              cfg.EdgeAuthSecret,
		EdgeAuthSecret:      cfg.EdgeAuthSecret,
		EdgeAuthNextSecret:  cfg.EdgeAuthNextSecret,
		EdgeAuthKeyID:       cfg.EdgeAuthKeyID,
		EdgeAuthNextKeyID:   cfg.EdgeAuthNextKeyID,
		EdgeFlagsSecret:     cfg.EdgeFlagsSecret,
		EdgeFlagsNextSecret: cfg.EdgeFlagsNextSecret,
		EdgeFlagsKeyID:      cfg.EdgeFlagsKeyID,
		EdgeFlagsNextKeyID:  cfg.EdgeFlagsNextKeyID,
	})
}

// Edge-auth constants — headers set by the Cloudflare Edge Router after Auth0
// JWT validation. The CF Worker strips client-injected identity headers and
// only sets X-Kombify-Edge-Auth after successful validation.
const (
	headerEdgeAuth       = "X-Kombify-Edge-Auth"
	edgeAuthValueJWT     = "auth0-jwt"
	edgeAuthValueAPIKey  = "api-key"
	headerEdgeSignature  = "X-Kombify-Edge-Signature"
	headerEdgeTimestamp  = "X-Kombify-Edge-Timestamp"
	headerEdgeNonce      = "X-Kombify-Edge-Nonce"
	headerEdgeSignedPath = "X-Kombify-Edge-Signed-Path"
	headerEdgeKeyID      = "X-Kombify-Edge-Key-ID"
	headerEdgeService    = "X-Kombify-Edge-Service"
	headerPublicPrefix   = "X-Kombify-Public-Prefix"
	headerUserScope      = "X-User-Scope"

	techStackDecisionAudience     = "techstack"
	techStackDecisionPublicPrefix = "/v1/techstack"

	// edgeSignatureVersion is the v1 signature prefix. Version negotiation and
	// crypto now live in go-common/edgeauth; this constant is retained for the
	// signature-construction test helpers.
	edgeSignatureVersion = "v1"
	defaultEdgeKeyID     = "primary"
	defaultEdgeNextKeyID = "next"
)

// EdgeIdentityMiddlewareWithConfig creates identity middleware with full config.
//
// Trust priority (checked in order):
//  1. Cloudflare Edge Router (X-Kombify-Edge-Auth: auth0-jwt) — primary path for
//     HTTP routes migrated from Edge. CF strips client-injected headers.
//  2. Edge shared secret (X-Edge-Auth-Secret) — legacy path for gRPC (Phase 5).
//
// In self-hosted mode this is a no-op pass-through.
func EdgeIdentityMiddlewareWithConfig(cfg EdgeIdentityConfig) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		if !cfg.Mode.IsSaaS() {
			return e.Next()
		}

		// ── Path 1: Cloudflare Edge Router auth ─────────────────────────────────
		edgeAuth := strings.TrimSpace(e.Request.Header.Get(headerEdgeAuth))
		if edgeAuth != "" {
			if !isSupportedEdgeAuthValue(edgeAuth) {
				stripIdentityHeaders(e.Request)
				http.Error(e.Response, "invalid edge authentication", http.StatusUnauthorized)
				return nil
			}
			if err := verifyEdgeSignature(e.Request, cfg); err != nil {
				slog.Warn("edge identity: signature verification failed", "error", err)
				stripIdentityHeaders(e.Request)
				http.Error(e.Response, "invalid edge authentication", http.StatusUnauthorized)
				return nil
			}
			if err := attachV2EdgeEntitlements(e); err != nil {
				slog.Warn("edge identity: entitlement envelope invalid", "error", err)
				stripIdentityHeaders(e.Request)
				http.Error(e.Response, "invalid edge entitlements", http.StatusUnauthorized)
				return nil
			}
			e.Request = e.Request.WithContext(markEdgeAuthenticatedContext(e.Request.Context()))
			id := identity.FromHeaders(e.Request.Header.Get)
			if id.IsAuthenticated() {
				ctx := identity.NewContext(e.Request.Context(), id)
				e.Request = e.Request.WithContext(ctx)
			}
			// X-User-Tier (CF) → plan context; fall back to X-User-Plan (Edge).
			tier := strings.TrimSpace(e.Request.Header.Get("X-User-Tier"))
			if tier == "" {
				tier = strings.TrimSpace(e.Request.Header.Get("X-User-Plan"))
			}
			if tier != "" {
				ctx := context.WithValue(e.Request.Context(), userPlanContextKey{}, tier)
				e.Request = e.Request.WithContext(ctx)
			}
			if err := attachEdgeFlags(e, cfg); err != nil {
				slog.Warn("edge flags: signature verification failed", "error", err)
				stripIdentityHeaders(e.Request)
				http.Error(e.Response, "invalid edge flags", http.StatusUnauthorized)
				return nil
			}
			stripIdentityHeaders(e.Request)
			return e.Next()
		}

		// ── Path 2: Edge shared secret (legacy) ─────────────────────────────────
		if cfg.Secret != "" {
			reqSecret := e.Request.Header.Get("X-Edge-Auth-Secret")
			if subtle.ConstantTimeCompare([]byte(reqSecret), []byte(cfg.Secret)) != 1 {
				slog.Debug("edge identity: secret mismatch, skipping header trust")
				stripIdentityHeaders(e.Request)
				return e.Next()
			}
			e.Request.Header.Del("X-Edge-Auth-Secret")
			e.Request = e.Request.WithContext(markEdgeAuthenticatedContext(e.Request.Context()))
		} else {
			slog.Debug("edge identity: no trusted edge or Edge secret configured, skipping header trust")
			stripIdentityHeaders(e.Request)
			return e.Next()
		}

		id := identity.FromHeaders(e.Request.Header.Get)
		if id.IsAuthenticated() {
			ctx := identity.NewContext(e.Request.Context(), id)
			e.Request = e.Request.WithContext(ctx)
		}

		// Extract X-User-Plan header and store in context
		if plan := strings.TrimSpace(e.Request.Header.Get("X-User-Plan")); plan != "" {
			ctx := context.WithValue(e.Request.Context(), userPlanContextKey{}, plan)
			e.Request = e.Request.WithContext(ctx)
		}

		// Strip identity headers to prevent downstream spoofing
		stripIdentityHeaders(e.Request)

		return e.Next()
	}
}

// stripIdentityHeaders removes Edge-injected headers from the request
// so that downstream handlers cannot be confused by them if they were
// spoofed by an external caller (defense in depth).
func stripIdentityHeaders(r *http.Request) {
	r.Header.Del("X-User-ID")
	r.Header.Del("X-Org-ID")
	r.Header.Del("X-User-Email")
	r.Header.Del("X-User-Roles")
	r.Header.Del("X-User-Plan")
	r.Header.Del("X-User-Tier")
	r.Header.Del(headerUserScope)
	r.Header.Del(commonedgeauth.HeaderEntitlements)
	r.Header.Del(commonedgeauth.HeaderKnowledgeTier)
	r.Header.Del(commonedgeauth.HeaderFlags)
	r.Header.Del(commonedgeauth.HeaderBudgets)
	r.Header.Del(commonedgeauth.HeaderFlagsSignature)
	r.Header.Del(commonedgeauth.HeaderFlagsTimestamp)
	r.Header.Del(commonedgeauth.HeaderFlagsKeyID)
}

// attachV2EdgeEntitlements copies only entitlements cryptographically bound to
// the verified v2 Edge envelope into an immutable request-context set. V1 and
// shared-secret requests deliberately receive no authorization grants.
func attachV2EdgeEntitlements(e *httpx.Event) error {
	if e == nil || e.Request == nil || !strings.HasPrefix(strings.TrimSpace(e.Request.Header.Get(headerEdgeSignature)), "v2=") {
		return nil
	}
	raw := strings.TrimSpace(e.Request.Header.Get(commonedgeauth.HeaderEntitlements))
	if raw == "" {
		return nil
	}
	entitlements := make([]string, 0)
	for _, entitlement := range strings.Split(raw, ",") {
		entitlement = strings.TrimSpace(entitlement)
		if !validEdgeEntitlement(entitlement) {
			return fmt.Errorf("edge_entitlement_invalid")
		}
		entitlements = append(entitlements, entitlement)
	}
	e.Request = e.Request.WithContext(WithSignedEntitlements(e.Request.Context(), entitlements...))
	return nil
}

func validEdgeEntitlement(value string) bool {
	// The Gateway's canonical entitlement contract normalizes all_features to
	// the exact wildcard "*". Accept only that whole value; embedded wildcard
	// characters remain invalid.
	if value == "*" {
		return true
	}
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("._:-", char) {
			return false
		}
	}
	return true
}

func attachEdgeFlags(e *httpx.Event, cfg EdgeIdentityConfig) error {
	if e == nil || e.Request == nil || !hasAnyEdgeFlagHeader(e.Request) {
		return nil
	}
	if strings.TrimSpace(e.Request.Header.Get(headerEdgeService)) == techStackDecisionAudience {
		flags, err := commonedgeauth.VerifyDecisionHeaders(e.Request, commonedgeauth.DecisionVerifyConfig{
			PrimarySecret:        firstNonEmpty(cfg.EdgeFlagsSecret, os.Getenv("EDGE_FLAGS_SECRET"), cfg.EdgeAuthSecret, os.Getenv("EDGE_AUTH_SECRET")),
			NextSecret:           firstNonEmpty(cfg.EdgeFlagsNextSecret, os.Getenv("EDGE_FLAGS_SECRET_NEXT"), cfg.EdgeAuthNextSecret, os.Getenv("EDGE_AUTH_SECRET_NEXT")),
			PrimaryKeyID:         firstNonEmpty(cfg.EdgeFlagsKeyID, os.Getenv("EDGE_FLAGS_KEY_ID"), defaultEdgeKeyID),
			NextKeyID:            firstNonEmpty(cfg.EdgeFlagsNextKeyID, os.Getenv("EDGE_FLAGS_KEY_ID_NEXT"), defaultEdgeNextKeyID),
			ExpectedAudience:     techStackDecisionAudience,
			ExpectedPublicPrefix: techStackDecisionPublicPrefix,
			SignatureWindow:      cfg.EdgeSignatureWindow,
			IdentityConfig: commonedgeauth.Config{
				EdgeAuthSecret:     firstNonEmpty(cfg.EdgeAuthSecret, os.Getenv("EDGE_AUTH_SECRET")),
				EdgeAuthNextSecret: firstNonEmpty(cfg.EdgeAuthNextSecret, os.Getenv("EDGE_AUTH_SECRET_NEXT")),
				EdgeAuthKeyID:      firstNonEmpty(cfg.EdgeAuthKeyID, os.Getenv("EDGE_AUTH_KEY_ID"), defaultEdgeKeyID),
				EdgeAuthNextKeyID:  firstNonEmpty(cfg.EdgeAuthNextKeyID, os.Getenv("EDGE_AUTH_KEY_ID_NEXT"), defaultEdgeNextKeyID),
				SignatureWindow:    cfg.EdgeSignatureWindow,
			},
		})
		if err != nil {
			return err
		}
		e.Request = e.Request.WithContext(commonedgeauth.FlagsToContext(e.Request.Context(), flags))
		return nil
	}

	// Other services sharing the TechStack origin retain v1 rollout flags.
	// Those flags have no VerifiedDecisionBinding and therefore cannot become
	// cost-bearing commercial authority.
	flags, err := commonedgeauth.VerifyFlagHeaders(e.Request, commonedgeauth.Config{
		EdgeAuthSecret:     firstNonEmpty(cfg.EdgeFlagsSecret, os.Getenv("EDGE_FLAGS_SECRET"), cfg.EdgeAuthSecret),
		EdgeAuthNextSecret: firstNonEmpty(cfg.EdgeFlagsNextSecret, os.Getenv("EDGE_FLAGS_SECRET_NEXT"), cfg.EdgeAuthNextSecret),
		EdgeAuthKeyID:      firstNonEmpty(cfg.EdgeFlagsKeyID, os.Getenv("EDGE_FLAGS_KEY_ID"), defaultEdgeKeyID),
		EdgeAuthNextKeyID:  firstNonEmpty(cfg.EdgeFlagsNextKeyID, os.Getenv("EDGE_FLAGS_KEY_ID_NEXT"), defaultEdgeNextKeyID),
		SignatureWindow:    cfg.EdgeSignatureWindow,
	})
	if err != nil {
		return err
	}
	e.Request = e.Request.WithContext(commonedgeauth.FlagsToContext(e.Request.Context(), flags))
	return nil
}

func hasAnyEdgeFlagHeader(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, header := range []string{
		commonedgeauth.HeaderFlags,
		commonedgeauth.HeaderBudgets,
		commonedgeauth.HeaderFlagsSignature,
		commonedgeauth.HeaderFlagsTimestamp,
		commonedgeauth.HeaderFlagsKeyID,
	} {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return true
		}
	}
	return false
}

// verifyEdgeSignature delegates HMAC envelope verification to the shared
// go-common edgeauth verifier, which dual-accepts v1 and v2 signatures. The v2
// payload additionally binds X-Kombify-Entitlements + X-Kombify-Knowledge-Tier
// so those headers cannot be forged downstream. Techstack no longer hand-rolls
// the edge-signature crypto — go-common is the single source of truth for the
// contract (kombify-Core/standards/EDGE-AUTH-RESPONSIBILITY-STANDARD.md), so the
// Gateway flip from v1 to v2 is accepted here without a further code change.
//
// The caller only invokes this after confirming X-Kombify-Edge-Auth is present
// and supported, so RequireEdgeAuth is set; go-common performs the version
// negotiation, timestamp-window, signed-path and dual-key-rotation checks.
func verifyEdgeSignature(r *http.Request, cfg EdgeIdentityConfig) error {
	commonCfg := commonedgeauth.Config{
		Enabled:            true,
		RequireEdgeAuth:    true,
		EdgeAuthSecret:     firstNonEmpty(cfg.EdgeAuthSecret, os.Getenv("EDGE_AUTH_SECRET")),
		EdgeAuthNextSecret: firstNonEmpty(cfg.EdgeAuthNextSecret, os.Getenv("EDGE_AUTH_SECRET_NEXT")),
		EdgeAuthKeyID:      firstNonEmpty(cfg.EdgeAuthKeyID, os.Getenv("EDGE_AUTH_KEY_ID"), defaultEdgeKeyID),
		EdgeAuthNextKeyID:  firstNonEmpty(cfg.EdgeAuthNextKeyID, os.Getenv("EDGE_AUTH_KEY_ID_NEXT"), defaultEdgeNextKeyID),
		SignatureWindow:    cfg.EdgeSignatureWindow,
	}

	verified := false
	recorder := httptest.NewRecorder()
	commonedgeauth.Middleware(commonCfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		verified = true
	})).ServeHTTP(recorder, r)
	if !verified {
		return fmt.Errorf("edge signature rejected (status %d)", recorder.Code)
	}
	return nil
}

func isSupportedEdgeAuthValue(value string) bool {
	normalized := strings.TrimSpace(value)
	return strings.EqualFold(normalized, edgeAuthValueJWT) ||
		strings.EqualFold(normalized, edgeAuthValueAPIKey)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
