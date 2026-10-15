package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/golang-jwt/jwt/v5"
)

// portalTestToken signs a Cloud-shaped Techstack launch token; overrides
// replace or (with a nil value) remove claims.
func portalTestToken(t *testing.T, secret string, overrides jwt.MapClaims) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       "kombify-cloud",
		"aud":       "kombify-tool:kombifystack",
		"jti":       "jti-" + t.Name() + "-" + now.Format(time.RFC3339Nano),
		"sub":       "auth0|portal-user",
		"tenant_id": "usr:auth0|portal-user",
		"email":     "portal-user@example.test",
		"name":      "Portal User",
		"tool":      "kombifystack",
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	}
	for key, value := range overrides {
		if value == nil {
			delete(claims, key)
			continue
		}
		claims[key] = value
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign portal token: %v", err)
	}
	return token
}

func runPortalVerify(t *testing.T, handler func(*httpx.Event) error, token string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(PortalVerifyRequest{Token: token})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/auth/portal-verify", bytes.NewReader(body)),
		Response: recorder,
	}
	event.Request.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		event.Request.AddCookie(cookie)
	}
	if err := handler(event); err != nil {
		t.Fatalf("handlePortalVerify() unexpected error: %v", err)
	}
	return recorder
}

func portalSessionCookie(recorder *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "techstack_session" && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func newPortalTestSession(t *testing.T) PortalSession {
	t.Helper()
	manager, err := session.NewManager(session.Config{
		Audience: "portal-verify-test",
		Secret:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return PortalSession{Manager: manager, CookieName: "techstack_session", AuthStore: &portalVerifyAuthStore{}}
}

func TestHandlePortalVerifyFailsClosedWithoutCanonicalIdentityStore(t *testing.T) {
	const secret = "portal-verify-test-secret"
	t.Setenv("TECHSTACK_SSO_JWT_SECRET", secret)
	ps := newPortalTestSession(t)
	ps.AuthStore = nil

	recorder := runPortalVerify(t, handleRuntimePortalVerify(ps), portalTestToken(t, secret, nil))
	if got, want := recorder.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("status = %d, want %d (body=%s)", got, want, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"pb_token"`) {
		t.Fatalf("portal verify must not return a successful compatibility session: %s", recorder.Body.String())
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("portal verify must not set a partial browser cookie: %+v", cookies)
	}
}

func TestHandlePortalVerifyRejectsMissingTenantBeforeSessionProjection(t *testing.T) {
	const secret = "portal-verify-missing-tenant-secret"
	t.Setenv("TECHSTACK_SSO_JWT_SECRET", secret)
	ps := newPortalTestSession(t)
	store := ps.AuthStore.(*portalVerifyAuthStore)

	recorder := runPortalVerify(t, handleRuntimePortalVerify(ps), portalTestToken(t, secret, jwt.MapClaims{"tenant_id": nil}))
	if got, want := recorder.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d (body=%s)", got, want, recorder.Body.String())
	}
	if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("missing tenant must not set a browser cookie: %+v", cookies)
	}
	if store.membership.TenantID != "" {
		t.Fatalf("missing tenant must not project a membership: %+v", store.membership)
	}
}

func TestHandlePortalVerifyUsesPayloadTenantForSessionAndMembership(t *testing.T) {
	const (
		secret          = "portal-verify-tenant-test-secret"
		canonicalTenant = "org_portal_customer"
	)
	t.Setenv("TECHSTACK_SSO_JWT_SECRET", secret)
	ps := newPortalTestSession(t)
	store := ps.AuthStore.(*portalVerifyAuthStore)

	recorder := runPortalVerify(t, handleRuntimePortalVerify(ps), portalTestToken(t, secret, jwt.MapClaims{
		"sub":       "auth0|portal-tenant-user",
		"email":     "portal-tenant-user@example.test",
		"tenant_id": canonicalTenant,
	}))
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d (body=%s)", got, want, recorder.Body.String())
	}
	sessionCookie := portalSessionCookie(recorder)
	if sessionCookie == nil {
		t.Fatal("portal verify did not issue the browser session cookie")
	}
	issuedClaims, err := ps.Manager.Verify(sessionCookie.Value)
	if err != nil {
		t.Fatalf("verify issued browser session: %v", err)
	}
	if issuedClaims.TenantID != canonicalTenant {
		t.Fatalf("browser session tenant = %q, want payload tenant %q", issuedClaims.TenantID, canonicalTenant)
	}
	if store.membership.TenantID != canonicalTenant {
		t.Fatalf("control-plane membership tenant = %q, want payload tenant %q", store.membership.TenantID, canonicalTenant)
	}
}

// Only Cloud's Techstack-only secret (or its rotation slot) signs a portal
// session; the SSO_JWT_SECRET that Simulate and kombify.me also hold does not.
func TestHandlePortalVerifyAcceptsOnlyTheTechstackSecret(t *testing.T) {
	const (
		techstackSecret = "techstack-portal-secret-0123456789abcdef"
		rotationSecret  = "techstack-portal-secret-next-0123456789ab"
		sharedSecret    = "shared-sso-secret-held-by-other-services"
	)
	t.Setenv("TECHSTACK_SSO_JWT_SECRET", techstackSecret)
	t.Setenv("TECHSTACK_SSO_JWT_SECRET_NEXT", rotationSecret)
	t.Setenv("SSO_JWT_SECRET", sharedSecret)
	t.Setenv("KOMBIFY_SSO_SECRET", sharedSecret)
	handler := handleRuntimePortalVerify(newPortalTestSession(t))

	for _, tc := range []struct {
		secret string
		want   int
	}{
		{sharedSecret, http.StatusUnauthorized},
		{rotationSecret, http.StatusOK},
		{techstackSecret, http.StatusOK},
	} {
		recorder := runPortalVerify(t, handler, portalTestToken(t, tc.secret, jwt.MapClaims{"jti": "jti-" + tc.secret}))
		if recorder.Code != tc.want || (tc.want != http.StatusOK) != (portalSessionCookie(recorder) == nil) {
			t.Fatalf("token signed with %q: status %d, want %d (body=%s)", tc.secret, recorder.Code, tc.want, recorder.Body.String())
		}
	}
}

// A launch token is exchanged once. A replay from another client is refused;
// the browser that already holds this subject's session may present it again
// (Cloud's embed re-sends its cached token after an in-frame reload).
func TestHandlePortalVerifyTokenIsSingleUse(t *testing.T) {
	const secret = "portal-verify-single-use-secret"
	t.Setenv("TECHSTACK_SSO_JWT_SECRET", secret)
	handler := handleRuntimePortalVerify(newPortalTestSession(t))
	token := portalTestToken(t, secret, nil)

	first := runPortalVerify(t, handler, token)
	session := portalSessionCookie(first)
	if first.Code != http.StatusOK || session == nil {
		t.Fatalf("first exchange: status %d, session %v (body=%s)", first.Code, session != nil, first.Body.String())
	}
	replay := runPortalVerify(t, handler, token)
	if replay.Code != http.StatusUnauthorized || portalSessionCookie(replay) != nil {
		t.Fatalf("replay from another client: status %d, want 401 without a session", replay.Code)
	}
	again := runPortalVerify(t, handler, token, session)
	if again.Code != http.StatusOK {
		t.Fatalf("re-presentation by the session holder: status %d, want 200 (body=%s)", again.Code, again.Body.String())
	}
}

type portalVerifyAuthStore struct {
	tenant     controlplane.Tenant
	user       controlplane.User
	membership controlplane.Membership
}

func (s *portalVerifyAuthStore) UpsertTenant(_ context.Context, tenant controlplane.Tenant) (*controlplane.Tenant, error) {
	s.tenant = tenant
	return &s.tenant, nil
}

func (s *portalVerifyAuthStore) UpsertUser(_ context.Context, user controlplane.User) (*controlplane.User, error) {
	s.user = user
	return &s.user, nil
}

func (s *portalVerifyAuthStore) UpsertMembership(_ context.Context, membership controlplane.Membership) (*controlplane.Membership, error) {
	s.membership = membership
	return &s.membership, nil
}

func (s *portalVerifyAuthStore) GetMembership(context.Context, string, string) (*controlplane.Membership, error) {
	return nil, nil
}

func (s *portalVerifyAuthStore) ListMembershipsByUser(context.Context, string) ([]controlplane.Membership, error) {
	return nil, nil
}

func (s *portalVerifyAuthStore) UpsertAuthConfig(_ context.Context, config controlplane.AuthConfig) (*controlplane.AuthConfig, error) {
	return &config, nil
}

func (s *portalVerifyAuthStore) UpsertBreakglassAdmin(_ context.Context, admin controlplane.BreakglassAdmin) (*controlplane.BreakglassAdmin, error) {
	return &admin, nil
}

func (s *portalVerifyAuthStore) GetBreakglassAdmin(context.Context, string) (*controlplane.BreakglassAdmin, error) {
	return nil, nil
}
