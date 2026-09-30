package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/authprojection"
	cloudsso "github.com/kombifyio/techstack/pkg/auth/sso"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
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

func newPortalVerifyTestApp(t *testing.T) core.App {
	t.Helper()
	app, err := tests.NewTestApp(pocketBaseTestDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	ensureSSOTestUserLinksCollection(t, app)
	return app
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
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	ps := newPortalTestSession(t)
	ps.AuthStore = nil

	recorder := runPortalVerify(t, handlePortalVerify(app, ps), portalTestToken(t, secret, nil))
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
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	ps := newPortalTestSession(t)
	store := ps.AuthStore.(*portalVerifyAuthStore)

	recorder := runPortalVerify(t, handlePortalVerify(app, ps), portalTestToken(t, secret, jwt.MapClaims{"tenant_id": nil}))
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
	app := newPortalVerifyTestApp(t)
	ps := newPortalTestSession(t)
	store := ps.AuthStore.(*portalVerifyAuthStore)

	recorder := runPortalVerify(t, handlePortalVerify(app, ps), portalTestToken(t, secret, jwt.MapClaims{
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
	app := newPortalVerifyTestApp(t)
	handler := handlePortalVerify(app, newPortalTestSession(t))

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
	app := newPortalVerifyTestApp(t)
	handler := handlePortalVerify(app, newPortalTestSession(t))
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

func TestFindOrCreateUserFromSSO_RelinksExistingCloudUserByEmail(t *testing.T) {
	app, err := tests.NewTestApp(pocketBaseTestDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	userLinksCollection := ensureSSOTestUserLinksCollection(t, app)
	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("find users collection: %v", err)
	}

	user := core.NewRecord(usersCollection)
	user.SetEmail("test-user@kombify.io")
	user.Set("name", "Test User")
	user.SetVerified(true)
	user.SetPassword("test-password-123456789")
	if err := app.Save(user); err != nil {
		t.Fatalf("save user: %v", err)
	}

	staleLink := core.NewRecord(userLinksCollection)
	staleLink.Set("user", user.Id)
	staleLink.Set("provider", "cloud")
	staleLink.Set("external_id", "old-cloud-sub")
	staleLink.Set("external_email", "test-user@kombify.io")
	staleLink.Set("external_name", "Old Name")
	if err := app.Save(staleLink); err != nil {
		t.Fatalf("save stale link: %v", err)
	}

	payload := &cloudsso.SSOTokenPayload{
		Sub:   "new-cloud-sub",
		Email: "test-user@kombify.io",
		Name:  "Test User",
		Tool:  "kombifystack",
	}

	gotUser, err := findOrCreateUserFromSSO(app, payload)
	if err != nil {
		t.Fatalf("findOrCreateUserFromSSO() returned error: %v", err)
	}
	gotLink, err := app.FindRecordById("user_links", staleLink.Id)
	if err != nil {
		t.Fatalf("reload cloud link: %v", err)
	}

	if gotUser.Id != user.Id {
		t.Fatalf("got user %q, want existing user %q", gotUser.Id, user.Id)
	}
	if gotLink.Id != staleLink.Id {
		t.Fatalf("got link %q, want relinked existing link %q", gotLink.Id, staleLink.Id)
	}
	if gotLink.GetString("external_id") != "new-cloud-sub" {
		t.Fatalf("external_id was not refreshed: %q", gotLink.GetString("external_id"))
	}
	if gotLink.GetString("external_name") != "Test User" {
		t.Fatalf("external_name was not refreshed: %q", gotLink.GetString("external_name"))
	}
	resolved, err := authprojection.FindPocketBaseUser(app, payload.Sub)
	if err != nil || resolved == nil || resolved.Id != user.Id {
		t.Fatalf("canonical cloud subject did not resolve compatibility profile: user=%v err=%v", resolved, err)
	}

	links, err := app.FindRecordsByFilter(
		"user_links",
		"user = {:user} && provider = 'cloud'",
		"",
		10,
		0,
		map[string]any{"user": user.Id},
	)
	if err != nil {
		t.Fatalf("find cloud links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected one cloud link after relink, got %d", len(links))
	}
}

func ensureSSOTestUserLinksCollection(t *testing.T, app core.App) *core.Collection {
	t.Helper()

	if collection, err := app.FindCollectionByNameOrId("user_links"); err == nil {
		return collection
	}

	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("find users collection: %v", err)
	}

	collection := core.NewBaseCollection("user_links")
	collection.Fields.Add(
		&core.RelationField{
			Name:          "user",
			Required:      true,
			CollectionId:  usersCollection.Id,
			CascadeDelete: true,
		},
		&core.SelectField{
			Name:     "provider",
			Required: true,
			Values:   []string{"cloud", "google", "github", "microsoft", "local"},
		},
		&core.TextField{Name: "external_id", Required: true, Max: 500},
		&core.TextField{Name: "external_email", Required: true, Max: 500},
		&core.TextField{Name: "external_name", Max: 200},
	)
	collection.Indexes = append(collection.Indexes,
		"CREATE UNIQUE INDEX idx_user_links_user_provider ON user_links (user, provider)",
		"CREATE UNIQUE INDEX idx_user_links_provider_external_id ON user_links (provider, external_id)",
		"CREATE INDEX idx_user_links_external_email ON user_links (external_email)",
	)

	if err := app.Save(collection); err != nil {
		t.Fatalf("save user_links collection: %v", err)
	}

	return collection
}

func pocketBaseTestDataDir(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve go module cache: %v", err)
	}

	modCache := strings.TrimSpace(string(output))
	if modCache == "" {
		t.Fatal("resolve go module cache: empty result")
	}

	matches, err := filepath.Glob(filepath.Join(modCache, "github.com", "pocketbase", "pocketbase@*", "tests", "data"))
	if err != nil {
		t.Fatalf("resolve pocketbase test data: %v", err)
	}
	if len(matches) == 0 {
		matches, err = filepath.Glob(filepath.Join(modCache, "github.com", "*", "pocketbase@*", "tests", "data"))
		if err != nil {
			t.Fatalf("resolve pocketbase test data fallback: %v", err)
		}
	}
	for _, match := range matches {
		if strings.Contains(filepath.ToSlash(match), path.Join("github.com", "pocketbase", "pocketbase@")) {
			return match
		}
	}

	t.Fatal("resolve pocketbase test data: no matching data directory found")
	return ""
}
