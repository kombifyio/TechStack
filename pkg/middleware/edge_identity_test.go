package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/gocommon/edgeauth/edgeauthtest"
	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// testNonceSeq keeps every signed test envelope nonce unique: the edge replay
// guard rejects a repeated nonce.
var testNonceSeq atomic.Int64

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
	if e.Request.Header.Get(headerEdgeService) == "" {
		e.Request.Header.Set(headerEdgeService, "capabilities")
	}
	nonce := "test-nonce-" + strconv.FormatInt(testNonceSeq.Add(1), 10)
	edgeauthtest.Sign(t, e.Request, edgeauthtest.Config{Version: edgeSignatureVersion, Secret: secret, KeyID: defaultEdgeKeyID, At: time.Now(), Nonce: nonce})
}

// TestEdgeIdentityMiddleware_SelfHosted verifies that in self-hosted mode the
// middleware is a no-op and passes through without touching headers or context.
func TestEdgeIdentityMiddleware_SelfHosted(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode: config.ModeSelfHosted,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set("X-User-ID", "user-123")
	e.Request.Header.Set(tenantdb.ContextTokenHeader, "untrusted-local-header")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tenantdb.ContextTokenFromContext(e.Request.Context()) != "" {
		t.Fatal("self-hosted pass-through promoted an untrusted token into context")
	}
	if e.Request.Header.Get(tenantdb.ContextTokenHeader) != "untrusted-local-header" {
		t.Fatal("self-hosted pass-through changed the existing raw header behavior")
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
	if !IsEdgeAuthenticated(e.Request.Context()) {
		t.Fatal("expected edge-authenticated request context")
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

func TestEdgeIdentityMiddleware_BudgetRotationKeepsIdentityKeysSeparate(t *testing.T) {
	t.Setenv("EDGE_AUTH_SECRET_NEXT", "identity-next")
	t.Setenv("EDGE_AUTH_KEY_ID_NEXT", "next")
	t.Setenv("EDGE_FLAGS_SECRET", "")
	t.Setenv("EDGE_FLAGS_SECRET_NEXT", "")
	t.Setenv("EDGE_FLAGS_KEY_ID", "")
	t.Setenv("EDGE_FLAGS_KEY_ID_NEXT", "")
	for _, tc := range []struct {
		name, primary, next, signer, keyID string
		valid                              bool
	}{
		{"dedicated-primary", testEdgeFlagsSecret, "", testEdgeFlagsSecret, "primary", true},
		{"ambient-identity-next-denied", testEdgeFlagsSecret, "", "identity-next", "next", false},
		{"preload-accepts-old-primary", "", "budget-next", testEdgeAuthSecret, "primary", true},
		{"preload-accepts-new-budget-key", "", "budget-next", "budget-next", "next", true},
		{"preload-denies-identity-next-as-budget", "", "budget-next", "identity-next", "next", false},
		{"dedicated-rotation", testEdgeFlagsSecret, "budget-next", "budget-next", "next", true},
	} {
		for _, version := range []string{"v1", "v2"} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				cfg := EdgeIdentityConfig{Mode: config.ModeSaaS, EdgeAuthSecret: testEdgeAuthSecret,
					EdgeFlagsSecret: tc.primary, EdgeFlagsNextSecret: tc.next}
				e := newTestEvent(http.MethodPost, "/api/v1/stacks")
				e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
				e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|budget-owner")
				e.Request.Header.Set(commonedgeauth.HeaderOrgID, "budget-tenant")
				e.Request.Header.Set(commonedgeauth.HeaderRequestID, "budget-rotation-request")
				flags := map[string]bool{"techstack.managed.runtime": true}
				if version == "v2" {
					e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
					e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
					attachSignedTechStackDecision(t, e, testEdgeAuthSecret, tc.signer, tc.keyID, flags, nil)
				} else {
					signEdgeRequest(t, e, testEdgeAuthSecret)
					headers, err := commonedgeauth.SignFlagHeaders(tc.signer, tc.keyID, flags, nil, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					copyHeaders(e.Request.Header, headers)
				}
				err := EdgeIdentityMiddlewareWithConfig(cfg)(e)
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := http.StatusUnauthorized
				if tc.valid {
					wantStatus = http.StatusOK
				}
				if status := e.Response.(*httptest.ResponseRecorder).Code; status != wantStatus {
					t.Fatalf("status=%d want=%d", status, wantStatus)
				}
				if tc.valid {
					got, ok := commonedgeauth.FlagsFromContext(e.Request.Context())
					if !ok || !got.Bool("techstack.managed.runtime", false) {
						t.Fatal("verified flag missing")
					}
					_, bound := got.VerifiedDecisionBinding()
					if bound != (version == "v2") {
						t.Fatal("wrong decision provenance")
					}
				}
			})
		}
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_RejectsTamperedFlags(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})

	e := newTestEvent("GET", "/test")
	e.Request.Header.Set(tenantdb.ContextTokenHeader, "opaque-db-context-with-no-claims")
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

	if tenantdb.ContextTokenFromContext(e.Request.Context()) != "" || e.Request.Header.Get(tenantdb.ContextTokenHeader) != "" {
		t.Fatal("edge-denied request retained DB-context transport")
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

func TestEdgeIdentityMiddleware_NamesTheDivergedSigningKey(t *testing.T) {
	// The production failure: the edge was switched to a dedicated flags key
	// no origin had been given. A generic signature mismatch hid that for six
	// days. The denial must name the key the edge used and the keys this
	// origin holds, so one log line is enough to see what diverged.
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:           config.ModeSaaS,
		EdgeAuthSecret: testEdgeAuthSecret,
	})
	e := newTestEvent(http.MethodGet, "/api/v1/servers")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
	e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
	e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-diverged-key")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)
	signed, err := commonedgeauth.SignFlagHeaders("a-key-this-origin-never-received",
		"budget-20260912", map[string]bool{"techstack.managed.runtime": true}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	copyHeaders(e.Request.Header, signed)

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(previous)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code := e.Response.(*httptest.ResponseRecorder).Code; code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want the request refused", code)
	}
	out := logged.String()
	for _, want := range []string{
		edgeFlagsKeyDivergenceReason,
		"budget-20260912", // what the edge signed with
		"primary",         // what this origin holds
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log does not name %q, so the divergence is still invisible: %s", want, out)
		}
	}
}

func TestEdgeIdentityMiddleware_DecisionDenialNamesAnOperatorFault(t *testing.T) {
	// An origin that cannot verify the edge's decision envelope must say which
	// kind of failure it is. Without a reason code the bare 401 was read as an
	// expired session, and clients looped through Universal Login against a
	// cause no sign-in can repair (live 2026-09-12 to 2026-09-18: the edge
	// signed with a dedicated EDGE_FLAGS_SECRET no origin was given).
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:            config.ModeSaaS,
		EdgeAuthSecret:  testEdgeAuthSecret,
		EdgeFlagsSecret: testEdgeFlagsSecret,
	})
	e := newTestEvent(http.MethodGet, "/api/v1/servers")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
	e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|owner-1")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "tenant-1")
	e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-diverged")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)
	// A decision signed with trust material this origin does not hold — the
	// exact shape of the production divergence.
	diverged, err := commonedgeauth.SignFlagHeaders("a-different-flags-secret", "primary",
		map[string]bool{"techstack.managed.runtime": true}, nil, time.Now())
	if err != nil {
		t.Fatalf("SignFlagHeaders: %v", err)
	}
	copyHeaders(e.Request.Header, diverged)

	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	// Fail-closed stays fail-closed.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want the request refused", rec.Code)
	}
	// The canonical envelope: clients read error.details (api/client.ts), which
	// is where the session-reprojection code is read from too.
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("denial body is not JSON (%v): %s", err, rec.Body.String())
	}
	details := body.Error.Details
	if details["reason_code"] != edgeDecisionUnverifiableReason {
		t.Fatalf("reason_code=%v, want %q so clients stop offering re-login",
			details["reason_code"], edgeDecisionUnverifiableReason)
	}
	if details["retryable"] != true {
		t.Fatalf("retryable=%v, want true: the caller did nothing wrong", details["retryable"])
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

// TestUserPlanFromContext verifies the helper returns the stored plan.
func TestUserPlanFromContext(t *testing.T) {
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
	if e.Request.Header.Get(headerEdgeService) == "" {
		e.Request.Header.Set(headerEdgeService, "capabilities")
	}
	nonce := "test-nonce-v2-" + strconv.FormatInt(testNonceSeq.Add(1), 10)
	edgeauthtest.Sign(t, e.Request, edgeauthtest.Config{Version: "v2", Secret: secret, KeyID: defaultEdgeKeyID, At: issuedAt, Nonce: nonce})
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
	e.Request.Header.Set(tenantdb.ContextTokenHeader, "opaque-db-context-with-no-claims")
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

	if tenantdb.ContextTokenFromContext(e.Request.Context()) != "" || e.Request.Header.Get(tenantdb.ContextTokenHeader) != "" {
		t.Fatal("edge-denied request retained DB-context transport")
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != 401 {
		t.Fatalf("expected 401 for tampered v2 entitlements, got %d", rec.Code)
	}
	if id := identity.FromContext(e.Request.Context()); id != nil {
		t.Fatalf("expected no identity for tampered v2 request, got %#v", id)
	}
}

func TestEdgeIdentityMiddleware_EdgeAuth_RejectsEnvelopeSignedForAnotherService(t *testing.T) {
	e := newTestEvent("GET", "/v1/techstack/probe")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, "kombify-ai")
	e.Request.Header.Set("X-User-ID", "user-1")
	signEdgeRequest(t, e, testEdgeAuthSecret)

	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{Mode: config.ModeSaaS, EdgeAuthSecret: testEdgeAuthSecret})
	_ = mw(e)
	if rec := e.Response.(*httptest.ResponseRecorder); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

// signEdgeRequestV9 signs the v9 envelope: the v7 payload (issuer, AI and
// resource context, principal/agent attestation, step-up facts) plus
// X-User-Email-Verified, before timestamp and nonce (go-common edgeauth).
func signEdgeRequestV9(t *testing.T, e *httpx.Event, secret string) {
	t.Helper()
	if e.Request.Header.Get(headerEdgeService) == "" {
		e.Request.Header.Set(headerEdgeService, "techstack")
	}
	nonce := "test-nonce-v9-" + strconv.FormatInt(testNonceSeq.Add(1), 10)
	edgeauthtest.Sign(t, e.Request, edgeauthtest.Config{Version: commonedgeauth.EdgeSignatureVersionV9, Secret: secret, KeyID: defaultEdgeKeyID, At: time.Now(), Nonce: nonce, SignedPath: e.Request.URL.RequestURI()})
}

// Regression (kombify-Techstack-7kf0): only a verified v9 envelope that signs
// email_verified=true carries Identity.EmailVerified; v9 keeps the signed
// entitlements of v2/v7; an unsigned (v2) email-verified header is ignored and
// stripped.
func TestEdgeIdentityMiddleware_V9SignedEmailVerified(t *testing.T) {
	mw := EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{Mode: config.ModeSaaS, EdgeAuthSecret: testEdgeAuthSecret})
	run := func(verifiedHeader string, sign func(*testing.T, *httpx.Event, string)) *httpx.Event {
		e := newTestEvent("POST", "/v1/techstack/wizard/runs")
		e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
		e.Request.Header.Set("X-User-ID", "auth0|direct")
		e.Request.Header.Set("X-User-Email", "direct@example.com")
		e.Request.Header.Set(commonedgeauth.HeaderEntitlements, "managed.ionos")
		e.Request.Header.Set(commonedgeauth.HeaderUserEmailVerified, verifiedHeader)
		sign(t, e, testEdgeAuthSecret)
		if err := mw(e); err != nil {
			t.Fatalf("middleware error: %v", err)
		}
		if code := e.Response.(*httptest.ResponseRecorder).Code; code != 200 {
			t.Fatalf("expected signed request to pass, got %d", code)
		}
		if got := e.Request.Header.Get(commonedgeauth.HeaderUserEmailVerified); got != "" {
			t.Fatalf("raw email-verified header was not stripped: %q", got)
		}
		return e
	}

	e := run("true", signEdgeRequestV9)
	if id := identity.FromContext(e.Request.Context()); id == nil || !id.EmailVerified || id.Email != "direct@example.com" {
		t.Fatalf("v9 verified email not attached: %#v", id)
	}
	if ents, ok := SignedEntitlementsFromContext(e.Request.Context()); !ok || !ents.Has("managed.ionos") {
		t.Fatalf("v9 signed entitlements dropped: %#v", ents)
	}

	if id := identity.FromContext(run("false", signEdgeRequestV9).Request.Context()); id == nil || id.EmailVerified {
		t.Fatalf("v9 email_verified=false produced verified identity: %#v", id)
	}
	if id := identity.FromContext(run("true", signEdgeRequestV2).Request.Context()); id == nil || id.EmailVerified {
		t.Fatalf("unsigned v2 email-verified header was trusted: %#v", id)
	}
}

const testEdgeProfileSecret = "test-edge-profile-secret"

// newSignedTechStackProfileEvent builds a Techstack request with a verified v2
// identity envelope and, when profile is non-nil, the Gateway profile headers
// bound to it (kombify-Gateway data-profile.ts signProfileHeaders).
func newSignedTechStackProfileEvent(t *testing.T, profile *dataprofile.Profile) *httpx.Event {
	t.Helper()
	e := newTestEvent(http.MethodGet, "/api/v1/stacks?limit=5")
	e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
	e.Request.Header.Set(headerEdgeService, techStackDecisionAudience)
	e.Request.Header.Set(headerPublicPrefix, techStackDecisionPublicPrefix)
	e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|alice")
	e.Request.Header.Set(commonedgeauth.HeaderOrgID, "org-1")
	e.Request.Header.Set(commonedgeauth.HeaderRequestID, "request-profile-1")
	signEdgeRequestV2(t, e, testEdgeAuthSecret)
	if profile == nil {
		return e
	}
	h := e.Request.Header
	signed, err := commonedgeauth.SignProfileHeaders(commonedgeauth.ProfileSignInput{
		Secret:        testEdgeProfileSecret,
		Method:        e.Request.Method,
		SignedPath:    h.Get(headerEdgeSignedPath),
		Audience:      h.Get(headerEdgeService),
		PublicPrefix:  h.Get(headerPublicPrefix),
		SubjectID:     h.Get(commonedgeauth.HeaderUserID),
		TenantID:      h.Get(commonedgeauth.HeaderOrgID),
		RequestID:     h.Get(commonedgeauth.HeaderRequestID),
		EdgeKeyID:     h.Get(headerEdgeKeyID),
		EdgeTimestamp: h.Get(headerEdgeTimestamp),
		EdgeNonce:     h.Get(headerEdgeNonce),
		EdgeSignature: h.Get(headerEdgeSignature),
		Profile:       *profile,
	})
	if err != nil {
		t.Fatalf("SignProfileHeaders: %v", err)
	}
	for name, values := range signed {
		h[name] = values
	}
	return e
}

func profileTestMiddleware(t *testing.T, mode string) func(*httpx.Event) error {
	t.Helper()
	t.Setenv(dataProfileModeEnv, mode)
	t.Setenv("EDGE_PROFILE_SECRET", testEdgeProfileSecret)
	return EdgeIdentityMiddlewareWithConfig(EdgeIdentityConfig{
		Mode:                config.ModeSaaS,
		EdgeAuthSecret:      testEdgeAuthSecret,
		EdgeSignatureWindow: 5 * time.Minute,
	})
}

func euZeroProfile() *dataprofile.Profile {
	p := dataprofile.Profile{V: dataprofile.Version, Res: dataprofile.ResidencyEU, Ret: dataprofile.RetentionZero,
		Loc: dataprofile.LocalityHosted, Scope: dataprofile.ScopeAccount, Rev: 3, TS: 1792700000}
	return &p
}

func assertNoProfileHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if hasAnyProfileHeader(r) {
		t.Fatal("profile headers reached the handler")
	}
}

func TestEdgeIdentityMiddleware_DataProfile_ObserveAttachesVerifiedProfile(t *testing.T) {
	mw := profileTestMiddleware(t, dataProfileModeObserve)
	e := newSignedTechStackProfileEvent(t, euZeroProfile())
	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Response.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	profile, ok := DataProfileFromContext(e.Request.Context(), false)
	if !ok || !profile.EU() || !profile.Zero() || profile.Rev != 3 {
		t.Fatalf("profile = %+v ok=%v, want verified EU zero-retention rev 3", profile, ok)
	}
	assertNoProfileHeaders(t, e.Request)
}

func TestEdgeIdentityMiddleware_DataProfile_ObserveContinuesWithoutProfile(t *testing.T) {
	mw := profileTestMiddleware(t, dataProfileModeObserve)
	e := newSignedTechStackProfileEvent(t, nil)
	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Response.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if _, ok := DataProfileFromContext(e.Request.Context(), false); ok {
		t.Fatal("a missing profile must not resolve, never to Standard")
	}
}

func TestEdgeIdentityMiddleware_DataProfile_EnforceDeniesMissingAndTampered(t *testing.T) {
	for name, tamper := range map[string]func(*httpx.Event){
		"missing": func(e *httpx.Event) {},
		"tampered profile": func(e *httpx.Event) {
			// A Standard profile under the signature of the EU one.
			standard, _ := dataprofile.Standard().Encode()
			e.Request.Header.Set(commonedgeauth.HeaderProfile, standard)
		},
		"transplanted subject": func(e *httpx.Event) {
			// Re-signing the identity for another subject must break the
			// profile binding even though the identity itself verifies.
			e.Request.Header.Set(commonedgeauth.HeaderUserID, "auth0|mallory")
			signEdgeRequestV2(t, e, testEdgeAuthSecret)
		},
	} {
		t.Run(name, func(t *testing.T) {
			mw := profileTestMiddleware(t, dataProfileModeEnforce)
			var e *httpx.Event
			if name == "missing" {
				e = newSignedTechStackProfileEvent(t, nil)
			} else {
				e = newSignedTechStackProfileEvent(t, euZeroProfile())
			}
			e.Request.Header.Set(tenantdb.ContextTokenHeader, "opaque-db-context-with-no-claims")
			tamper(e)
			if err := mw(e); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tenantdb.ContextTokenFromContext(e.Request.Context()) != "" || e.Request.Header.Get(tenantdb.ContextTokenHeader) != "" {
				t.Fatal("profile-denied request retained DB-context transport")
			}
			rec := e.Response.(*httptest.ResponseRecorder)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "data_profile_unresolvable") {
				t.Fatalf("body = %s, want data_profile_unresolvable denial", rec.Body.String())
			}
			if _, ok := DataProfileFromContext(e.Request.Context(), false); ok {
				t.Fatal("denied request carries a profile")
			}
		})
	}
}

func TestEdgeIdentityMiddleware_DataProfile_OffIgnoresAndStripsHeaders(t *testing.T) {
	mw := profileTestMiddleware(t, "")
	e := newSignedTechStackProfileEvent(t, euZeroProfile())
	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := DataProfileFromContext(e.Request.Context(), false); ok {
		t.Fatal("off mode attached a profile")
	}
	assertNoProfileHeaders(t, e.Request)
}

func TestEdgeIdentityMiddleware_DataProfile_UnsignedRequestNeverCarriesProfile(t *testing.T) {
	mw := profileTestMiddleware(t, dataProfileModeObserve)
	e := newTestEvent(http.MethodGet, "/api/v1/stacks")
	encoded, _ := euZeroProfile().Encode()
	e.Request.Header.Set(commonedgeauth.HeaderProfile, encoded)
	if err := mw(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := DataProfileFromContext(e.Request.Context(), false); ok {
		t.Fatal("an unsigned caller supplied a profile")
	}
	assertNoProfileHeaders(t, e.Request)
}

// The token stays opaque: only the existing database verifier may accept claims.
func TestEdgeIdentityMiddleware_OpaqueDBContextTransport(t *testing.T) {
	const opaque = "opaque-db-context-with-no-claims"
	for _, tc := range []struct {
		name                                 string
		signed, tampered, legacy, noIdentity bool
		token                                string
		wantToken, wantIdentity              bool
	}{
		{name: "signed", signed: true, token: opaque, wantToken: true, wantIdentity: true},
		{name: "unsigned", token: opaque},
		{name: "tampered", signed: true, tampered: true, token: opaque},
		{name: "legacy shared secret", legacy: true, token: opaque, wantIdentity: true},
		{name: "no identity", signed: true, noIdentity: true, token: opaque},
		{name: "too long", signed: true, token: strings.Repeat("x", 4097), wantIdentity: true},
		{name: "missing", signed: true, wantIdentity: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := EdgeIdentityConfig{Mode: config.ModeSaaS, EdgeAuthSecret: testEdgeAuthSecret}
			if tc.legacy {
				cfg.Secret = "transport-legacy-test-secret"
			}
			e := newTestEvent("GET", "/opaque-db-context")
			if !tc.noIdentity {
				e.Request.Header.Set("X-User-ID", "transport-user")
				e.Request.Header.Set("X-Org-ID", "transport-org")
			}
			e.Request.Header.Set(tenantdb.ContextTokenHeader, tc.token)
			if tc.signed {
				e.Request.Header.Set(headerEdgeAuth, edgeAuthValueJWT)
				signEdgeRequest(t, e, testEdgeAuthSecret)
			}
			if tc.legacy {
				e.Request.Header.Set("X-Edge-Auth-Secret", cfg.Secret)
			}
			if tc.tampered {
				e.Request.Header.Set("X-User-ID", "tampered-user")
			}
			if err := EdgeIdentityMiddlewareWithConfig(cfg)(e); err != nil {
				t.Fatal(err)
			}
			got := tenantdb.ContextTokenFromContext(e.Request.Context())
			if tc.wantToken && got != opaque {
				t.Fatal("opaque token did not reach Common transport context")
			}
			if !tc.wantToken && got != "" {
				t.Fatal("unadmitted token reached Common transport context")
			}
			if e.Request.Header.Get(tenantdb.ContextTokenHeader) != "" {
				t.Fatal("raw DB-context header survived stripping")
			}
			id := identity.FromContext(e.Request.Context())
			if tc.wantIdentity {
				if id == nil || id.UserID != "transport-user" || id.OrgID != "transport-org" {
					t.Fatal("transport changed the existing identity or tenant")
				}
			} else if id != nil && id.IsAuthenticated() {
				t.Fatal("DB-context token authenticated a request")
			}
		})
	}
}
