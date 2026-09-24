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

func TestGetSSOSecret_UsesLegacyKombifySecretAlias(t *testing.T) {
	t.Setenv("SSO_JWT_SECRET", "")
	t.Setenv("KOMBIFY_SSO_SECRET", "legacy-sso-secret")

	secret, err := getSSOSecret()
	if err != nil {
		t.Fatalf("getSSOSecret() unexpected error: %v", err)
	}

	if secret != "legacy-sso-secret" {
		t.Fatalf("getSSOSecret() = %q, want %q", secret, "legacy-sso-secret")
	}
}

func TestGetSSOSecret_PrefersPrimarySecretWhenBothAreSet(t *testing.T) {
	t.Setenv("SSO_JWT_SECRET", "primary-sso-secret")
	t.Setenv("KOMBIFY_SSO_SECRET", "legacy-sso-secret")

	secret, err := getSSOSecret()
	if err != nil {
		t.Fatalf("getSSOSecret() unexpected error: %v", err)
	}

	if secret != "primary-sso-secret" {
		t.Fatalf("getSSOSecret() = %q, want %q", secret, "primary-sso-secret")
	}
}

func TestHandlePortalVerifyFailsClosedWithoutCanonicalIdentityStore(t *testing.T) {
	const secret = "portal-verify-test-secret"
	t.Setenv("SSO_JWT_SECRET", secret)
	manager, err := session.NewManager(session.Config{
		Audience: "portal-verify-test",
		Secret:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	claims := jwt.MapClaims{
		"sub":       "auth0|portal-user",
		"tenant_id": "usr:auth0|portal-user",
		"email":     "portal-user@example.test",
		"name":      "Portal User",
		"tool":      "kombifystack",
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign portal token: %v", err)
	}
	body, err := json.Marshal(PortalVerifyRequest{Token: token})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request: httptest.NewRequest(
			http.MethodPost,
			"/api/v1/auth/portal-verify",
			bytes.NewReader(body),
		),
		Response: recorder,
	}
	event.Request.Header.Set("Content-Type", "application/json")

	if err := handlePortalVerify(app, PortalSession{
		Manager: manager, CookieName: "techstack_session",
	})(event); err != nil {
		t.Fatalf("handlePortalVerify() unexpected error: %v", err)
	}
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
	t.Setenv("SSO_JWT_SECRET", secret)
	manager, err := session.NewManager(session.Config{
		Audience: "portal-verify-missing-tenant",
		Secret:   []byte(strings.Repeat("m", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	claims := jwt.MapClaims{
		"sub":   "auth0|portal-user",
		"email": "portal-user@example.test",
		"name":  "Portal User",
		"tool":  "kombifystack",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign portal token: %v", err)
	}
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
	store := &portalVerifyAuthStore{}

	if err := handlePortalVerify(app, PortalSession{
		Manager: manager, CookieName: "techstack_session", AuthStore: store,
	})(event); err != nil {
		t.Fatalf("handlePortalVerify() unexpected error: %v", err)
	}
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
	t.Setenv("SSO_JWT_SECRET", secret)
	manager, err := session.NewManager(session.Config{
		Audience: "portal-verify-tenant-test",
		Secret:   []byte(strings.Repeat("t", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}

	app, err := tests.NewTestApp(pocketBaseTestDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	ensureSSOTestUserLinksCollection(t, app)

	claims := jwt.MapClaims{
		"sub":       "auth0|portal-tenant-user",
		"email":     "portal-tenant-user@example.test",
		"name":      "Portal Tenant User",
		"tool":      "kombifystack",
		"tenant_id": canonicalTenant,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign portal token: %v", err)
	}
	body, err := json.Marshal(PortalVerifyRequest{Token: token})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request: httptest.NewRequest(
			http.MethodPost,
			"/api/v1/auth/portal-verify",
			bytes.NewReader(body),
		),
		Response: recorder,
	}
	event.Request.Header.Set("Content-Type", "application/json")
	store := &portalVerifyAuthStore{}

	if err := handlePortalVerify(app, PortalSession{
		Manager: manager, CookieName: "techstack_session",
		AuthStore: store,
	})(event); err != nil {
		t.Fatalf("handlePortalVerify() unexpected error: %v", err)
	}
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d (body=%s)", got, want, recorder.Body.String())
	}

	var sessionCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "techstack_session" {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("portal verify did not issue the browser session cookie")
	}
	issuedClaims, err := manager.Verify(sessionCookie.Value)
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
