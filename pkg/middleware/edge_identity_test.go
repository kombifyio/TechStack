package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	commonedgeauth "github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/go-common/identity"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const testEdgeAuthSecret = "test-edge-auth-secret"
const testEdgeFlagsSecret = "test-edge-flags-secret"

// newTestEvent creates a minimal *httpx.Event for unit testing.
func newTestEvent(method, path string) *httpx.Event {
	return &httpx.Event{
		Request:  httptest.NewRequest(method, path, nil),
		Response: httptest.NewRecorder(),
	}
}

func signEdgeRequest(t *testing.T, e *httpx.Event, secret string) {
	t.Helper()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "test-nonce"
	signedPath := e.Request.URL.RequestURI()
	service := e.Request.Header.Get(headerEdgeService)
	publicPrefix := e.Request.Header.Get(headerPublicPrefix)

	payload := strings.Join([]string{
		edgeSignatureVersion,
		defaultEdgeKeyID,
		e.Request.Method,
		signedPath,
		e.Request.Header.Get(headerEdgeAuth),
		service,
		publicPrefix,
		e.Request.Header.Get("X-User-ID"),
		e.Request.Header.Get("X-Org-ID"),
		e.Request.Header.Get("X-User-Email"),
		e.Request.Header.Get("X-User-Tier"),
		e.Request.Header.Get("X-User-Roles"),
		e.Request.Header.Get("X-User-Scope"),
		ts,
		nonce,
	}, "\n")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))

	e.Request.Header.Set(headerEdgeTimestamp, ts)
	e.Request.Header.Set(headerEdgeNonce, nonce)
	e.Request.Header.Set(headerEdgeSignedPath, signedPath)
	e.Request.Header.Set(headerEdgeKeyID, defaultEdgeKeyID)
	e.Request.Header.Set(headerEdgeSignature, edgeSignatureVersion+"="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

// TestEdgeIdentityMiddleware_SelfHosted verifies that in self-hosted mode the
// middleware is a no-op and passes through without touching headers or context.
func TestEdgeIdentityMiddleware_SelfHosted(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode: config.ModeSelfHosted,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set("X-User-ID", "user-123")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In self-hosted mode headers should NOT be stripped (middleware is a no-op).
	if e.Request.Header.Get("X-User-ID") == "" {
		t.Error("expected X-User-ID to remain in self-hosted mode (no-op middleware)")
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_TrustsHeaders verifies that when the
// CF Edge Auth header is present, identity headers are trusted and the
// identity is stored in context.
func TestEdgeIdentityMiddleware_EdgeAuth_TrustsHeaders(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-abc")
	e.Request.Header.Set("X-User-Email", "user@example.com")
	e.Request.Header.Set("X-User-Roles", "admin")
	e.Request.Header.Set("X-User-Plan", "pro")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Identity should be stored in context.
	id := identity.FromContext(e.Request.Context())
	if id == nil {
		t.Fatal("expected identity in context, got nil")
	}
	if id.UserID != "user-abc" {
		t.Errorf("expected UserID=user-abc, got %q", id.UserID)
	}
	if id.Email != "user@example.com" {
		t.Errorf("expected Email=user@example.com, got %q", id.Email)
	}
	if !isEdgeAuthenticatedContext(e.Request.Context()) {
		t.Fatal("expected edge-authenticated request context")
	}
	if !IsEdgeAuthenticated(e.Request.Context()) {
		t.Fatal("expected exported edge-authenticated request marker")
	}

	// Plan should be in context.
	plan := UserPlanFromContext(e.Request.Context())
	if plan != "pro" {
		t.Errorf("expected plan=pro, got %q", plan)
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_AttachesSignedFlags(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-flags")
	signEdgeRequest(t, e, testEdgeAuthSecret)
	flagHeaders, err := commonedgeauth.SignFlagHeaders(testEdgeAuthSecret, "primary", map[string]bool{
		"sim.monthly.runtime.standard": true,
	}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	for key, values := range flagHeaders {
		for _, value := range values {
			e.Request.Header.Set(key, value)
		}
	}

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flags, ok := commonedgeauth.FlagsFromContext(e.Request.Context())
	if !ok {
		t.Fatal("expected signed edge flags in request context")
	}
	if !flags.Bool("sim.monthly.runtime.standard", false) {
		t.Fatal("expected monthly runtime flag to be true")
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_AttachesDedicatedSecretSignedFlags(t *testing.T) {
	t.Setenv("EDGE_FLAGS_SECRET", "test-edge-flags-secret")
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-flags")
	signEdgeRequest(t, e, testEdgeAuthSecret)
	flagHeaders, err := commonedgeauth.SignFlagHeaders("test-edge-flags-secret", "primary", map[string]bool{
		"sim.monthly.runtime.standard": true,
	}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	for key, values := range flagHeaders {
		for _, value := range values {
			e.Request.Header.Set(key, value)
		}
	}

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flags, ok := commonedgeauth.FlagsFromContext(e.Request.Context())
	if !ok {
		t.Fatal("expected signed edge flags in request context")
	}
	if !flags.Bool("sim.monthly.runtime.standard", false) {
		t.Fatal("expected monthly runtime flag to be true")
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_RejectsTamperedFlags(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-flags")
	signEdgeRequest(t, e, testEdgeAuthSecret)
	flagHeaders, err := commonedgeauth.SignFlagHeaders(testEdgeAuthSecret, "primary", map[string]bool{
		"sim.monthly.runtime.standard": false,
	}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	for key, values := range flagHeaders {
		for _, value := range values {
			e.Request.Header.Set(key, value)
		}
	}
	e.Request.Header.Set(commonedgeauth.HeaderFlags, `{"sim.monthly.runtime.standard":true}`)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != 401 {
		t.Fatalf("expected 401 for tampered flags, got %d", rec.Code)
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_AttachesRequestBoundTechStackDecision(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:                config.ModeSaaS,
		EdgeAuthSecret:      testEdgeAuthSecret,
		EdgeFlagsSecret:     testEdgeFlagsSecret,
		EdgeFlagsKeyID:      "flags-primary",
		EdgeSignatureWindow: 5 * time.Minute,
	})

	e := newTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/managed-runtimes?provider=ionos")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
	e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
	e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-1")
	e.Request.Header.Set(commonedgeauth.HeaderEntitlements, "techstack.managed.runtime,techstack.managed.runtime.cloudkit,techstack.managed.runtime.ionos")
	attachSignedTechStackDecision(t, e, testEdgeAuthSecret, testEdgeFlagsSecret, "flags-primary", map[string]bool{
		"techstack.managed.runtime":          true,
		"techstack.managed.runtime.cloudkit": true,
		"techstack.managed.runtime.ionos":    true,
	}, map[string]any{
		commonedgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
			"managed_servers": map[string]any{"mode": "limited", "limit": 3},
		},
	})

	// An application-constructed context value must never be merged into the
	// request-bound commercial decision.
	e.Request = e.Request.WithContext(commonedgeauth.FlagsToContext(e.Request.Context(), commonedgeauth.FlagSet{
		Flags: map[string]bool{"forged.local.flag": true},
	}))

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decisions, ok := commonedgeauth.FlagsFromContext(e.Request.Context())
	if !ok {
		t.Fatal("expected request-bound edge decision in context")
	}
	if decisions.Bool("forged.local.flag", false) {
		t.Fatal("application-constructed flags were merged into the verified decision")
	}
	credits, binding, err := decisions.VerifiedCloudRuntimeCredits()
	if err != nil {
		t.Fatalf("VerifiedCloudRuntimeCredits: %v", err)
	}
	if credits.ManagedServers.Mode != commonedgeauth.ManagedServerCreditModeLimited || credits.ManagedServers.Limit != 3 {
		t.Fatalf("credits=%#v, want limited 3", credits)
	}
	if binding.SubjectID != "auth0|owner-1" || binding.TenantID != "tenant-1" ||
		binding.Audience != techStackDecisionAudience || binding.PublicPrefix != techStackDecisionPublicPrefix ||
		binding.Method != http.MethodPost || binding.SignedPath != e.Request.URL.RequestURI() || binding.RequestID != "request-1" {
		t.Fatalf("binding=%#v, want exact TechStack request binding", binding)
	}
	for _, header := range []string{
		commonedgeauth.HeaderFlags,
		commonedgeauth.HeaderBudgets,
		commonedgeauth.HeaderFlagsSignature,
		commonedgeauth.HeaderFlagsTimestamp,
		commonedgeauth.HeaderFlagsKeyID,
	} {
		if got := e.Request.Header.Get(header); got != "" {
			t.Fatalf("raw decision header %s was not stripped: %q", header, got)
		}
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_RejectsDetachedV1TechStackBudget(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:            config.ModeSaaS,
		EdgeAuthSecret:  testEdgeAuthSecret,
		EdgeFlagsSecret: testEdgeFlagsSecret,
	})
	e := newTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/managed-runtimes")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
	e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
	e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-v1")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)
	legacy, err := commonedgeauth.SignFlagHeaders(testEdgeFlagsSecret, "primary", map[string]bool{
		"techstack.managed.runtime": true,
	}, map[string]any{
		commonedgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
			"managed_servers": map[string]any{"mode": "unlimited"},
		},
	}, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	copyHeaders(e.Request.Header, legacy)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Response.(*httptest.ResponseRecorder).Code; got != http.StatusUnauthorized {
		t.Fatalf("status=%d, want detached v1 TechStack budget rejected", got)
	}
	if _, ok := commonedgeauth.FlagsFromContext(e.Request.Context()); ok {
		t.Fatal("detached v1 TechStack budget reached request context")
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_RejectsDecisionTransplantAndFreshness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		issuedAt time.Time
		mutate   func(*http.Request)
	}{
		{
			name: "request id transplant",
			mutate: func(r *http.Request) {
				r.Header.Set(commonedgeauth.HeaderRequestID, "request-other")
			},
		},
		{
			name:     "future decision outside freshness window",
			issuedAt: time.Now().Add(30 * time.Second),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuedAt := tc.issuedAt
			if issuedAt.IsZero() {
				issuedAt = time.Now()
			}
			mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
				Mode: config.ModeSaaS, EdgeAuthSecret: testEdgeAuthSecret,
				EdgeFlagsSecret: testEdgeFlagsSecret, EdgeFlagsKeyID: "flags-primary",
				EdgeSignatureWindow: 5 * time.Second,
			})
			e := newTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/managed-runtimes")
			e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
			e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
			e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
			e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
			e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
			e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-1")
			attachSignedTechStackDecisionAt(
				t, e, testEdgeAuthSecret, testEdgeFlagsSecret, "flags-primary",
				map[string]bool{"techstack.managed.runtime": true},
				map[string]any{commonedgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
					"managed_servers": map[string]any{"mode": "limited", "limit": 3},
				}},
				issuedAt,
			)
			if tc.mutate != nil {
				tc.mutate(e.Request)
			}

			if err := mw(e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := e.Response.(*httptest.ResponseRecorder).Code; got != http.StatusUnauthorized {
				t.Fatalf("status=%d, want transplanted/stale decision rejected", got)
			}
			if _, ok := commonedgeauth.FlagsFromContext(e.Request.Context()); ok {
				t.Fatal("transplanted/stale decision reached request context")
			}
		})
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_KeepsLegacyFlagsNonAuthoritativeForOtherServices(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:            config.ModeSaaS,
		EdgeAuthSecret:  testEdgeAuthSecret,
		EdgeFlagsSecret: testEdgeFlagsSecret,
	})
	e := newTestEvent(http.MethodGet, "/v1/connectors")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, "connector-vault")
	e.Request.Header.Set(headerPublicPrefix, "/v1/connectors")
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)
	legacy, err := commonedgeauth.SignFlagHeaders(testEdgeFlagsSecret, "primary", map[string]bool{
		"connector.release.preview": true,
	}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	copyHeaders(e.Request.Header, legacy)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decisions, ok := commonedgeauth.FlagsFromContext(e.Request.Context())
	if !ok || !decisions.Bool("connector.release.preview", false) {
		t.Fatalf("legacy non-authoritative rollout flag missing: %#v", decisions)
	}
	if _, ok := decisions.VerifiedDecisionBinding(); ok {
		t.Fatal("legacy rollout flags unexpectedly gained commercial provenance")
	}
}

func TestEdgeIdentityMiddleware_EdgeAPIKey_TrustsSignedHeaders(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueAPIKey)
	e.Request.Header.Set("X-User-ID", "api-key:pro")
	e.Request.Header.Set("X-User-Roles", "api-consumer")
	e.Request.Header.Set("X-User-Tier", "PRO")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id := identity.FromContext(e.Request.Context())
	if id == nil || id.UserID != "api-key:pro" {
		t.Fatalf("expected signed API-key identity, got %#v", id)
	}
	if got := UserPlanFromContext(e.Request.Context()); got != "PRO" {
		t.Fatalf("expected plan=PRO from signed API-key identity, got %q", got)
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_CaseInsensitive verifies that the edge
// auth header value check is case-insensitive.
func TestEdgeIdentityMiddleware_EdgeAuth_CaseInsensitive(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, "Auth0-JWT") // mixed case
	e.Request.Header.Set("X-User-ID", "user-xyz")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id := identity.FromContext(e.Request.Context())
	if id == nil || id.UserID != "user-xyz" {
		t.Error("expected identity from case-insensitive edge auth match")
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_TierHeader verifies that X-User-Tier
// (CF Edge canonical) takes precedence over X-User-Plan (Edge compat).
func TestEdgeIdentityMiddleware_EdgeAuth_TierHeader(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-tier")
	e.Request.Header.Set("X-User-Tier", "enterprise")
	e.Request.Header.Set("X-User-Plan", "starter") // should be ignored when Tier present
	signEdgeRequest(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	plan := UserPlanFromContext(e.Request.Context())
	if plan != "enterprise" {
		t.Errorf("expected plan=enterprise (from X-User-Tier), got %q", plan)
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_StripsHeaders verifies that identity
// headers are stripped after extraction (defense in depth).
func TestEdgeIdentityMiddleware_EdgeAuth_StripsHeaders(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-strip")
	e.Request.Header.Set("X-Org-ID", "org-strip")
	e.Request.Header.Set("X-User-Email", "strip@example.com")
	e.Request.Header.Set("X-User-Roles", "user")
	e.Request.Header.Set("X-User-Plan", "free")
	e.Request.Header.Set("X-User-Tier", "free")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, h := range []string{"X-User-ID", "X-Org-ID", "X-User-Email", "X-User-Roles", "X-User-Plan", "X-User-Tier"} {
		if v := e.Request.Header.Get(h); v != "" {
			t.Errorf("expected header %s to be stripped, got %q", h, v)
		}
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_MissingSignatureRejected(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-unsigned")

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if id := identity.FromContext(e.Request.Context()); id != nil {
		t.Fatalf("expected no identity, got %#v", id)
	}
	if got := e.Request.Header.Get("X-User-ID"); got != "" {
		t.Fatalf("expected spoofable header to be stripped, got %q", got)
	}
}

// TestEdgeIdentityMiddleware_SpoofRejection_WithSecret verifies that when a
// shared secret is configured, requests without the correct secret are
// rejected even if they carry identity headers.
func TestEdgeIdentityMiddleware_SpoofRejection_WithSecret(t *testing.T) {
	const realSecret = "real-secret-xyz"
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:   config.ModeSaaS,
		Secret: realSecret,
	})

	e := newTestEvent("GET", "/test")
	// No edge auth header, wrong Edge secret — attempt to spoof identity.
	e.Request.Header.Set("X-Edge-Auth-Secret", "wrong-secret")
	e.Request.Header.Set("X-User-ID", "attacker-id")

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With a wrong secret, identity headers must NOT be stored.
	id := identity.FromContext(e.Request.Context())
	if id != nil && id.UserID == "attacker-id" {
		t.Error("spoofed identity was trusted despite secret mismatch")
	}
	if got := e.Request.Header.Get("X-User-ID"); got != "" {
		t.Errorf("expected spoofed headers to be stripped after secret mismatch, got %q", got)
	}
}

// TestEdgeIdentityMiddleware_SpoofRejection_WithoutSecret verifies that SaaS
// requests do not trust identity headers when neither edge auth nor Edge
// secret verification is active.
func TestEdgeIdentityMiddleware_SpoofRejection_WithoutSecret(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode: config.ModeSaaS,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set("X-User-ID", "attacker-id")
	e.Request.Header.Set("X-User-Plan", "enterprise")

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id := identity.FromContext(e.Request.Context())
	if id != nil && id.UserID == "attacker-id" {
		t.Error("spoofed identity was trusted without trusted edge or Edge auth")
	}
	if got := UserPlanFromContext(e.Request.Context()); got != "" {
		t.Errorf("expected empty plan when spoofed headers are ignored, got %q", got)
	}
	if got := e.Request.Header.Get("X-User-ID"); got != "" {
		t.Errorf("expected spoofed headers to be stripped, got %q", got)
	}
}

// TestEdgeIdentityMiddleware_EdgeSecretFallback verifies the legacy Edge
// shared secret path still works when Edge Auth header is absent.
func TestEdgeIdentityMiddleware_EdgeSecretFallback(t *testing.T) {
	const testSecret = "super-secret-123"
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:   config.ModeSaaS,
		Secret: testSecret,
	})

	e := newTestEvent("GET", "/test")
	// No edge auth — use Edge secret path.
	e.Request.Header.Set("X-Edge-Auth-Secret", testSecret)
	e.Request.Header.Set("X-User-ID", "edge-user")
	e.Request.Header.Set("X-User-Plan", "team")

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id := identity.FromContext(e.Request.Context())
	if id == nil || id.UserID != "edge-user" {
		t.Error("expected identity from Edge secret path")
	}

	plan := UserPlanFromContext(e.Request.Context())
	if plan != "team" {
		t.Errorf("expected plan=team from Edge path, got %q", plan)
	}
}

// TestUserPlanFromContext verifies the helper returns empty string on nil
// context and the stored value otherwise.
func TestUserPlanFromContext(t *testing.T) {
	if got := UserPlanFromContext(nil); got != "" {
		t.Errorf("expected empty string for nil ctx, got %q", got)
	}

	e := newTestEvent("GET", "/")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "u1")
	e.Request.Header.Set("X-User-Plan", "starter")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})
	_ = mw(e)

	if got := UserPlanFromContext(e.Request.Context()); got != "starter" {
		t.Errorf("expected starter, got %q", got)
	}
}

// signEdgeRequestV2 signs the request with a v2 edge signature, which binds the
// entitlements + knowledge-tier headers into the HMAC payload (matching
// go-common/edgeauth buildSignaturePayload for version "v2").
func signEdgeRequestV2(t *testing.T, e *httpx.Event, secret string) {
	signEdgeRequestV2At(t, e, secret, time.Now())
}

func signEdgeRequestV2At(t *testing.T, e *httpx.Event, secret string, issuedAt time.Time) {
	t.Helper()

	ts := strconv.FormatInt(issuedAt.Unix(), 10)
	nonce := "test-nonce-v2"
	signedPath := e.Request.URL.RequestURI()

	payload := strings.Join([]string{
		"v2",
		defaultEdgeKeyID,
		e.Request.Method,
		signedPath,
		e.Request.Header.Get(headerEdgeAuth),
		e.Request.Header.Get(headerEdgeService),
		e.Request.Header.Get(headerPublicPrefix),
		e.Request.Header.Get("X-User-ID"),
		e.Request.Header.Get("X-Org-ID"),
		e.Request.Header.Get("X-User-Email"),
		e.Request.Header.Get("X-User-Tier"),
		e.Request.Header.Get("X-User-Roles"),
		e.Request.Header.Get("X-User-Scope"),
		e.Request.Header.Get("X-Kombify-Entitlements"),
		e.Request.Header.Get("X-Kombify-Knowledge-Tier"),
		ts,
		nonce,
	}, "\n")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))

	e.Request.Header.Set(headerEdgeTimestamp, ts)
	e.Request.Header.Set(headerEdgeNonce, nonce)
	e.Request.Header.Set(headerEdgeSignedPath, signedPath)
	e.Request.Header.Set(headerEdgeKeyID, defaultEdgeKeyID)
	e.Request.Header.Set(headerEdgeSignature, "v2="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func attachSignedTechStackDecision(
	t *testing.T,
	e *httpx.Event,
	edgeSecret string,
	flagsSecret string,
	flagsKeyID string,
	flags map[string]bool,
	budgets map[string]any,
) {
	attachSignedTechStackDecisionAt(t, e, edgeSecret, flagsSecret, flagsKeyID, flags, budgets, time.Now())
}

func attachSignedTechStackDecisionAt(
	t *testing.T,
	e *httpx.Event,
	edgeSecret string,
	flagsSecret string,
	flagsKeyID string,
	flags map[string]bool,
	budgets map[string]any,
	issuedAt time.Time,
) {
	t.Helper()
	signEdgeRequestV2At(t, e, edgeSecret, issuedAt)
	headers, err := commonedgeauth.SignDecisionHeaders(commonedgeauth.DecisionSignInput{
		Secret:        flagsSecret,
		KeyID:         flagsKeyID,
		Method:        e.Request.Method,
		SignedPath:    e.Request.URL.RequestURI(),
		Audience:      e.Request.Header.Get(headerEdgeService),
		PublicPrefix:  e.Request.Header.Get(headerPublicPrefix),
		SubjectID:     e.Request.Header.Get(commonedgeauth.HeaderUserID),
		TenantID:      e.Request.Header.Get(commonedgeauth.HeaderOrgID),
		RequestID:     e.Request.Header.Get(commonedgeauth.HeaderRequestID),
		EdgeKeyID:     e.Request.Header.Get(headerEdgeKeyID),
		EdgeTimestamp: e.Request.Header.Get(headerEdgeTimestamp),
		EdgeNonce:     e.Request.Header.Get(headerEdgeNonce),
		EdgeSignature: e.Request.Header.Get(headerEdgeSignature),
		Flags:         flags,
		Budgets:       budgets,
	})
	if err != nil {
		t.Fatalf("SignDecisionHeaders: %v", err)
	}
	copyHeaders(e.Request.Header, headers)
}

func copyHeaders(target, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			target.Set(key, value)
		}
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_V2Signature_Accepted verifies the v2 edge
// signature (entitlements + knowledge-tier bound) is accepted after the Gateway
// flip, proving Techstack's delegation to go-common dual-accepts v1 and v2.
func TestEdgeIdentityMiddleware_EdgeAuth_V2Signature_Accepted(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-v2")
	e.Request.Header.Set("X-User-Tier", "enterprise")
	e.Request.Header.Set("X-Kombify-Entitlements", "ai.responses,ai.embed")
	e.Request.Header.Set("X-Kombify-Knowledge-Tier", "flagship")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != 200 {
		t.Fatalf("expected v2-signed request to pass (200), got %d", rec.Code)
	}
	id := identity.FromContext(e.Request.Context())
	if id == nil || id.UserID != "user-v2" {
		t.Fatalf("expected identity from v2-signed request, got %#v", id)
	}
	if got := UserPlanFromContext(e.Request.Context()); got != "enterprise" {
		t.Fatalf("expected plan=enterprise from v2, got %q", got)
	}
	entitlements, ok := SignedEntitlementsFromContext(e.Request.Context())
	if !ok || !entitlements.Has("ai.responses") || !entitlements.Has("ai.embed") {
		t.Fatalf("expected signed v2 entitlements in request context, got %#v", entitlements)
	}
	if flags, ok := commonedgeauth.FlagsFromContext(e.Request.Context()); ok && (flags.Bool("ai.responses", false) || flags.Bool("ai.embed", false)) {
		t.Fatalf("authorization grants leaked into feature flags: %#v", flags)
	}
	if got := e.Request.Header.Get(commonedgeauth.HeaderEntitlements); got != "" {
		t.Fatalf("raw entitlement header was not stripped: %q", got)
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_V2Signature_AcceptsCanonicalWildcardEntitlement(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "admin-v2")
	e.Request.Header.Set(commonedgeauth.HeaderEntitlements, "*,techstack.inventory.read")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Response.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("canonical Gateway wildcard status = %d, want 200", got)
	}
	entitlements, ok := SignedEntitlementsFromContext(e.Request.Context())
	if !ok || !entitlements.Has("*") || !entitlements.Has("techstack.inventory.read") {
		t.Fatalf("signed wildcard entitlements = %#v", entitlements)
	}
}

func TestValidEdgeEntitlementAllowsOnlyExactWildcard(t *testing.T) {
	for value, want := range map[string]bool{
		"*":                        true,
		"techstack.inventory.read": true,
		"admin*":                   false,
		"*.inventory":              false,
	} {
		if got := validEdgeEntitlement(value); got != want {
			t.Fatalf("validEdgeEntitlement(%q) = %t, want %t", value, got, want)
		}
	}
}

// TestEdgeIdentityMiddleware_EdgeAuth_V2Signature_TamperedEntitlements_Returns401
// proves the v2 signature actually binds entitlements: escalating the
// entitlements header after signing must break the HMAC and yield 401.
func TestEdgeIdentityMiddleware_EdgeAuth_V2Signature_TamperedEntitlements_Returns401(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set("X-User-ID", "user-v2")
	e.Request.Header.Set("X-Kombify-Entitlements", "ai.responses")
	e.Request.Header.Set("X-Kombify-Knowledge-Tier", "standard")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)

	// Attacker escalates entitlements after the edge signed them.
	e.Request.Header.Set("X-Kombify-Entitlements", "ai.responses,admin.super")

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != 401 {
		t.Fatalf("expected 401 for tampered v2 entitlements, got %d", rec.Code)
	}
	if id := identity.FromContext(e.Request.Context()); id != nil {
		t.Fatalf("expected no identity for tampered v2 request, got %#v", id)
	}
}
