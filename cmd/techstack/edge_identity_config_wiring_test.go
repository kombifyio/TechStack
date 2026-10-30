package main

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/gocommon/edgeauth/edgeauthtest"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestSignedEdgeDemoFirstLoginBootstrapsEmptyAuthProjection(t *testing.T) {
	const (
		edgeSecret = "demo-first-login-edge-secret"
		demoTenant = "tenant-demo"
		demoUser   = "auth0|demo-first-login"
		demoEmail  = "demo@kombified.com"
	)
	t.Setenv("TECHSTACK_DEMO_USER_IDS", demoUser)
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", demoTenant)

	sessionManager, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-demo-first-login",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &stubAuthStore{}
	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSaaS
	cfg.EdgeAuthSecret = edgeSecret

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{
		startup: &startupContext{cfg: cfg},
		v2: &v2Boot{
			session:       sessionManager,
			authStore:     store,
			defaultTenant: "default",
		},
	})
	router.GET("/api/v1/stacks", func(e *httpx.Event) error {
		id := identity.FromContext(e.Request.Context())
		if id == nil || id.UserID != demoTenant || id.OrgID != demoTenant {
			http.Error(e.Response, "demo edge identity missing", http.StatusUnauthorized)
			return nil
		}
		return e.NoContent(http.StatusNoContent)
	})
	handler := router.BuildMux()

	request := func(nonce string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
		req.Header.Set(commonedgeauth.HeaderEdgeAuth, commonedgeauth.EdgeAuthValueJWT)
		req.Header.Set(commonedgeauth.HeaderUserID, demoUser)
		req.Header.Set(commonedgeauth.HeaderOrgID, demoTenant)
		req.Header.Set(commonedgeauth.HeaderUserEmail, demoEmail)
		req.Header.Set(commonedgeauth.HeaderUserRoles, "user")
		req.Header.Set(commonedgeauth.HeaderEntitlements, "techstack.inventory.read")
		req.Header.Set(commonedgeauth.HeaderKnowledgeTier, "standard")
		signV2EdgeRequestForConfigWiringTestWithNonce(t, req, edgeSecret, nonce)
		return req
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request("demo-first-login-1"))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first signed-edge request status = %d body=%q, want %d", first.Code, first.Body.String(), http.StatusNoContent)
	}
	if got, want := store.calls, []string{"tenant", "user", "membership"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap order = %#v, want %#v", got, want)
	}
	if len(store.tenants) != 1 {
		t.Fatalf("tenant upserts = %#v, want exactly one", store.tenants)
	}
	if tenant := store.tenants[0]; tenant.ID != demoTenant || tenant.ExternalOrgID != demoTenant || tenant.Status != "active" {
		t.Fatalf("tenant = %#v, want active configured demo tenant", tenant)
	}
	if len(store.users) != 1 {
		t.Fatalf("user upserts = %#v, want exactly one", store.users)
	}
	if user := store.users[0]; user.ID != demoUser || user.PrimaryEmail != demoEmail || user.Status != "active" {
		t.Fatalf("user = %#v, want active signed-edge subject", user)
	}
	if len(store.memberships) != 1 {
		t.Fatalf("membership upserts = %#v, want exactly one", store.memberships)
	}
	if membership := store.memberships[0]; membership.ID != demoTenant+":"+demoUser ||
		membership.TenantID != demoTenant || membership.UserID != demoUser ||
		membership.SubjectID != demoUser || membership.ProviderKey != "cloud" ||
		membership.RoleKey != "member" || membership.Status != "active" {
		t.Fatalf("membership = %#v, want owner-aligned active demo membership", membership)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request("demo-first-login-2"))
	if second.Code != http.StatusNoContent {
		t.Fatalf("second signed-edge request status = %d body=%q, want %d", second.Code, second.Body.String(), http.StatusNoContent)
	}
	if got, want := store.calls, []string{"tenant", "user", "membership"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("idempotent bootstrap calls = %#v, want no additional upserts after membership exists", got)
	}
}

func TestGlobalMiddlewareUsesTechstackEdgeAuthConfigForLegacyAndV2(t *testing.T) {
	const (
		secret          = "documented-techstack-edge-secret"
		flagsSecret     = "documented-techstack-flags-secret"
		flagsNextSecret = "documented-techstack-flags-next-secret"
	)
	t.Setenv("KOMBIFY_EDITION", string(config.EditionSelfHostOSS))
	t.Setenv("DEPLOYMENT_MODE", "")
	t.Setenv("TECHSTACK_EDGE_AUTH_SECRET", secret)
	t.Setenv("TECHSTACK_EDGE_FLAGS_SECRET", flagsSecret)
	t.Setenv("TECHSTACK_EDGE_FLAGS_SECRET_NEXT", flagsNextSecret)
	t.Setenv("TECHSTACK_EDGE_FLAGS_KEY_ID", "flags-primary")
	t.Setenv("TECHSTACK_EDGE_FLAGS_KEY_ID_NEXT", "flags-next")
	// The product contract is TECHSTACK_EDGE_AUTH_SECRET. These shared-library
	// fallbacks must stay empty so the test catches broken startup wiring.
	t.Setenv("EDGE_AUTH_SECRET", "")
	t.Setenv("EDGE_AUTH_SECRET_NEXT", "")
	t.Setenv("EDGE_FLAGS_SECRET", "")
	t.Setenv("EDGE_FLAGS_SECRET_NEXT", "")
	t.Setenv("EDGE_FLAGS_KEY_ID", "")
	t.Setenv("EDGE_FLAGS_KEY_ID_NEXT", "")

	configPath := filepath.Join(t.TempDir(), "techstack.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write isolated Techstack config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load documented Techstack config: %v", err)
	}
	if cfg.EdgeAuthSecret != secret {
		t.Fatalf("EdgeAuthSecret = %q, want value loaded from TECHSTACK_EDGE_AUTH_SECRET", cfg.EdgeAuthSecret)
	}
	if cfg.EdgeFlagsSecret != flagsSecret || cfg.EdgeFlagsNextSecret != flagsNextSecret ||
		cfg.EdgeFlagsKeyID != "flags-primary" || cfg.EdgeFlagsNextKeyID != "flags-next" {
		t.Fatalf("decision keyring not loaded from TechStack config: %#v", cfg)
	}

	// Hosted editions intentionally require a private build-time gate, which is
	// unavailable in this public unit-test binary. Only the mode is switched;
	// the edge secret still travels through the production config loader.
	cfg.DeploymentMode = config.ModeSaaS

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: cfg}})
	router.GET("/api/v1/config-wiring-probe", func(e *httpx.Event) error {
		id := identity.FromContext(e.Request.Context())
		if id == nil || id.UserID != "auth0|config-wiring" || id.OrgID != "tenant-config-wiring" {
			http.Error(e.Response, "edge identity missing", http.StatusUnauthorized)
			return nil
		}
		if decisions, ok := commonedgeauth.FlagsFromContext(e.Request.Context()); ok {
			credits, binding, err := decisions.VerifiedCloudRuntimeCredits()
			if err != nil || credits.ManagedServers.Mode != commonedgeauth.ManagedServerCreditModeLimited ||
				credits.ManagedServers.Limit != 3 || binding.KeyID != "flags-next" {
				http.Error(e.Response, "request-bound decision missing", http.StatusUnauthorized)
				return nil
			}
			e.Response.Header().Set("X-Test-Decision", "verified")
		}
		return e.NoContent(http.StatusNoContent)
	})

	for _, tc := range []struct {
		name         string
		prepare      func(*http.Request)
		wantDecision bool
	}{
		{
			name: "legacy shared secret",
			prepare: func(req *http.Request) {
				req.Header.Set("X-Edge-Auth-Secret", secret)
			},
		},
		{
			name: "v2 signed envelope",
			prepare: func(req *http.Request) {
				req.Header.Set(commonedgeauth.HeaderEdgeAuth, commonedgeauth.EdgeAuthValueJWT)
				req.Header.Set(commonedgeauth.HeaderEntitlements, "techstack.inventory.read")
				req.Header.Set(commonedgeauth.HeaderKnowledgeTier, "standard")
				signV2EdgeRequestForConfigWiringTest(t, req, secret)
			},
		},
		{
			name: "request-bound decision next rotation key",
			prepare: func(req *http.Request) {
				req.Header.Set(commonedgeauth.HeaderEdgeAuth, commonedgeauth.EdgeAuthValueJWT)
				req.Header.Set(commonedgeauth.HeaderEdgeService, "techstack")
				req.Header.Set(commonedgeauth.HeaderPublicPrefix, "/v1/techstack")
				req.Header.Set(commonedgeauth.HeaderRequestID, "request-config-wiring")
				req.Header.Set(commonedgeauth.HeaderEntitlements, "techstack.managed.runtime,techstack.managed.runtime.cloudkit,techstack.managed.runtime.ionos")
				signV2EdgeRequestForConfigWiringTest(t, req, secret)
				decision, err := commonedgeauth.SignDecisionHeaders(commonedgeauth.DecisionSignInput{
					Secret: flagsNextSecret, KeyID: "flags-next",
					Method: req.Method, SignedPath: req.URL.RequestURI(),
					Audience: "techstack", PublicPrefix: "/v1/techstack",
					SubjectID: req.Header.Get(commonedgeauth.HeaderUserID), TenantID: req.Header.Get(commonedgeauth.HeaderOrgID),
					RequestID: req.Header.Get(commonedgeauth.HeaderRequestID), EdgeKeyID: req.Header.Get(commonedgeauth.HeaderEdgeKeyID),
					EdgeTimestamp: req.Header.Get(commonedgeauth.HeaderEdgeTimestamp), EdgeNonce: req.Header.Get(commonedgeauth.HeaderEdgeNonce),
					EdgeSignature: req.Header.Get(commonedgeauth.HeaderEdgeSignature),
					Flags:         map[string]bool{"techstack.managed.runtime": true},
					Budgets: map[string]any{commonedgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
						"managed_servers": map[string]any{"mode": "limited", "limit": 3},
					}},
				})
				if err != nil {
					t.Fatalf("SignDecisionHeaders: %v", err)
				}
				for header, values := range decision {
					for _, value := range values {
						req.Header.Set(header, value)
					}
				}
			},
			wantDecision: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/config-wiring-probe", nil)
			req.Header.Set(commonedgeauth.HeaderUserID, "auth0|config-wiring")
			req.Header.Set(commonedgeauth.HeaderOrgID, "tenant-config-wiring")
			tc.prepare(req)

			rec := httptest.NewRecorder()
			router.BuildMux().ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("request status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
			}
			if got := rec.Header().Get("X-Test-Decision"); (got == "verified") != tc.wantDecision {
				t.Fatalf("decision marker=%q, wantDecision=%t", got, tc.wantDecision)
			}
		})
	}
}

func signV2EdgeRequestForConfigWiringTest(t testing.TB, req *http.Request, secret string) {
	signV2EdgeRequestForConfigWiringTestWithNonce(t, req, secret, "config-wiring-v2")
}

func signV2EdgeRequestForConfigWiringTestWithNonce(t testing.TB, req *http.Request, secret, nonce string) {
	t.Helper()
	nonce += "-" + rand.Text()
	if req.Header.Get(commonedgeauth.HeaderEdgeService) == "" {
		req.Header.Set(commonedgeauth.HeaderEdgeService, "capabilities")
	}
	edgeauthtest.Sign(t, req, edgeauthtest.Config{Version: "v2", Secret: secret, At: time.Now(), Nonce: nonce})
}

// D-2026-10-22-02: through the whole global middleware chain, a signed edge
// request reaches a handler only when its subject and tenant are on the same
// side of the test boundary.
func TestSignedEdgeRequestsKeepTestIdentitiesInTestTenants(t *testing.T) {
	const (
		edgeSecret = "test-identity-boundary-edge-secret"
		tester     = "auth0|tester"
		realUser   = "auth0|real-person"
		demoTenant = "tenant-demo"
	)
	t.Setenv("TECHSTACK_TEST_SUBJECTS", tester)
	t.Setenv("TEST_RIL_PRO_USER_ID", "")
	t.Setenv("TECHSTACK_DEMO_USER_IDS", "auth0|demo-visitor")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", demoTenant)

	sessionManager, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-test-identity-boundary",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSaaS
	cfg.EdgeAuthSecret = edgeSecret

	store := &stubAuthStore{}
	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{
		startup: &startupContext{cfg: cfg},
		v2:      &v2Boot{session: sessionManager, authStore: store, defaultTenant: "default"},
	})
	reached := 0
	router.GET("/api/v1/stacks", func(e *httpx.Event) error {
		reached++
		return e.NoContent(http.StatusNoContent)
	})
	handler := router.BuildMux()

	for i, tc := range []struct {
		name, subject, tenant string
		want                  int
	}{
		{"test identity in its own test tenant", tester, "usr:" + tester, http.StatusNoContent},
		{"test identity in a real organization", tester, "org_real", http.StatusForbidden},
		{"real person in the test identity's tenant", realUser, "usr:" + tester, http.StatusForbidden},
		{"real person in the demo tenant", realUser, demoTenant, http.StatusForbidden},
		{"real person in their own owner tenant", realUser, "usr:" + realUser, http.StatusNoContent},
	} {
		before, writes := reached, len(store.calls)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
		req.Header.Set(commonedgeauth.HeaderEdgeAuth, commonedgeauth.EdgeAuthValueJWT)
		req.Header.Set(commonedgeauth.HeaderUserID, tc.subject)
		req.Header.Set(commonedgeauth.HeaderOrgID, tc.tenant)
		req.Header.Set(commonedgeauth.HeaderUserEmail, "person@example.test")
		req.Header.Set(commonedgeauth.HeaderUserRoles, "user")
		req.Header.Set(commonedgeauth.HeaderEntitlements, "techstack.inventory.read")
		req.Header.Set(commonedgeauth.HeaderKnowledgeTier, "standard")
		signV2EdgeRequestForConfigWiringTestWithNonce(t, req, edgeSecret, "test-identity-boundary-"+strconv.Itoa(i))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != tc.want {
			t.Fatalf("%s: status = %d body=%q, want %d", tc.name, recorder.Code, recorder.Body.String(), tc.want)
		}
		if refused := tc.want == http.StatusForbidden; refused && (reached != before || len(store.calls) != writes) {
			t.Fatalf("%s: a refused request reached the handler or wrote %v", tc.name, store.calls[writes:])
		}
		if tc.want == http.StatusForbidden && !strings.Contains(recorder.Body.String(), "test_identity_tenant_refused") {
			t.Fatalf("%s: refusal body = %q, want test_identity_tenant_refused", tc.name, recorder.Body.String())
		}
	}
}
