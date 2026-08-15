package v2

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	commonauthflow "github.com/kombifyio/go-common/authflow"
	commonauthlocal "github.com/kombifyio/go-common/authlocal"
	commonoidc "github.com/kombifyio/go-common/oidcclient"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/kombifyio/techstack/pkg/v2/authmw"
)

type serverFakeExchanger struct {
	result *commonoidc.CodeExchangeResult
	err    error
}

type serverLocalStore struct {
	rec *commonauthlocal.Record
}

func (s *serverLocalStore) Get(_ context.Context) (*commonauthlocal.Record, error) {
	if s.rec == nil {
		return nil, nil
	}
	clone := *s.rec
	return &clone, nil
}

func (s *serverLocalStore) Save(_ context.Context, rec *commonauthlocal.Record) error {
	if rec == nil {
		s.rec = nil
		return nil
	}
	clone := *rec
	s.rec = &clone
	return nil
}

func (f *serverFakeExchanger) ExchangeCode(_ context.Context, _ *commonoidc.Provider, _ commonoidc.CodeExchangeRequest) (*commonoidc.CodeExchangeResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func newServerProviderFixture(t *testing.T) (*providers.Provider, *rsa.PrivateKey, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": "kid-1",
				"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	}))
	provider, err := providers.New(providers.Config{
		ID:       "primary",
		Kind:     providers.KindGeneric,
		Issuer:   server.URL,
		ClientID: "techstack-client",
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return provider, key, server
}

func signServerTestIDToken(t *testing.T, key *rsa.PrivateKey, issuer string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   issuer,
		"aud":   "techstack-client",
		"sub":   "user-123",
		"email": "user@example.com",
		"role":  "admin",
		"exp":   time.Now().Add(time.Minute).Unix(),
	})
	token.Header["kid"] = "kid-1"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustStateFromLocation(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if strings.TrimSpace(state) == "" {
		t.Fatal("state missing")
	}
	return state
}

func assertRedirectURIFromLocation(t *testing.T, location, want string) {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != want {
		t.Fatalf("redirect_uri = %q, want %q in %q", got, want, location)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	req := httptest.NewRequest(http.MethodGet, "/api/v2/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("content-type: got %q, want %q", got, want)
	}

	var body HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status field: got %q, want %q", body.Status, "ok")
	}
	if body.Version != Version {
		t.Fatalf("version field: got %q, want %q", body.Version, Version)
	}
	if body.StartedAtRFC33 == "" {
		t.Fatalf("startedAt empty")
	}
	if body.DB.Status != "disabled" {
		t.Fatalf("db.status: got %q, want %q (no DB attached)", body.DB.Status, "disabled")
	}
}

func TestHealthMethodNotAllowed(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	req := httptest.NewRequest(http.MethodPost, "/api/v2/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("POST should not be 200, got %d", rec.Code)
	}
}

func TestWhoAmIEndpointWithSessionCookie(t *testing.T) {
	mgr, err := session.NewManager(session.Config{
		Audience: "frontend",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(session.Claims{Subject: "user-1", TenantID: "tenant-1", Email: "u@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(WithSession(mgr))
	handler := srv.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
	req.AddCookie(&http.Cookie{Name: authmw.DefaultSessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	var body WhoAmIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Subject != "user-1" || body.TenantID != "tenant-1" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestAuthRoutesRegisteredWhenHandlersAttached(t *testing.T) {
	srv := NewServer(WithAuthHandlers(AuthHandlers{
		Providers: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		Login:     http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		Callback:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		Logout:    http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	}))
	handler := srv.Routes()
	for _, path := range []string{"/api/v2/auth/providers", "/api/v2/auth/login", "/api/v2/auth/callback", "/api/v2/auth/logout"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if got, want := rec.Code, http.StatusNoContent; got != want {
				t.Fatalf("status: got %d, want %d", got, want)
			}
		})
	}
}

func TestEndToEndAuthFlowThroughServerRoutes(t *testing.T) {
	provider, key, jwksServer := newServerProviderFixture(t)
	defer jwksServer.Close()

	sharedProvider, err := provider.ToOIDCClient()
	if err != nil {
		t.Fatal(err)
	}
	registry := commonoidc.NewRegistry()
	registry.Add(sharedProvider)

	mgr, err := session.NewManager(session.Config{
		Audience: "frontend",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}

	authFlow, err := commonauthflow.NewService(commonauthflow.Config{
		Providers:         registry,
		Sessions:          mgr,
		StateSecret:       []byte("fedcba9876543210fedcba9876543210"),
		DefaultProviderID: "primary",
		DefaultTenantID:   "tenant-default",
		DefaultReturnTo:   "/stacks",
		CallbackPath:      config.CanonicalAuthCallbackPath,
		SessionCookieName: authmw.DefaultSessionCookieName,
		Exchanger: &serverFakeExchanger{result: &commonoidc.CodeExchangeResult{
			IDToken: signServerTestIDToken(t, key, jwksServer.URL),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := NewServer(
		WithSession(mgr),
		WithAuthHandlers(AuthHandlers{
			Providers: authFlow.ProvidersHandler(),
			Login:     authFlow.LoginHandler(),
			Callback:  authFlow.CallbackHandler(),
			Logout:    authFlow.LogoutHandler(),
		}),
	).Routes()

	providersReq := httptest.NewRequest(http.MethodGet, "/api/v2/auth/providers", nil)
	providersRec := httptest.NewRecorder()
	handler.ServeHTTP(providersRec, providersReq)
	if got, want := providersRec.Code, http.StatusOK; got != want {
		t.Fatalf("providers status: got %d, want %d", got, want)
	}

	loginReq := httptest.NewRequest(http.MethodGet, "/api/v2/auth/login?tenant_id=tenant-42&return_to=%2Fapi%2Fv2%2Fwhoami", nil)
	loginReq.Host = "127.0.0.1:5260"
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if got, want := loginRec.Code, http.StatusFound; got != want {
		t.Fatalf("login status: got %d, want %d", got, want)
	}
	loginLocation := loginRec.Header().Get("Location")
	assertRedirectURIFromLocation(t, loginLocation, "http://127.0.0.1:5260"+config.CanonicalAuthCallbackPath)
	state := mustStateFromLocation(t, loginLocation)

	forwardedLoginReq := httptest.NewRequest(http.MethodGet, "/api/v2/auth/login", nil)
	forwardedLoginReq.Host = "127.0.0.1:5260"
	forwardedLoginReq.Header.Set("X-Forwarded-Proto", "https")
	forwardedLoginReq.Header.Set("X-Forwarded-Host", "techstack.kombify.io")
	forwardedLoginRec := httptest.NewRecorder()
	handler.ServeHTTP(forwardedLoginRec, forwardedLoginReq)
	if got, want := forwardedLoginRec.Code, http.StatusFound; got != want {
		t.Fatalf("forwarded login status: got %d, want %d", got, want)
	}
	assertRedirectURIFromLocation(
		t,
		forwardedLoginRec.Header().Get("Location"),
		"https://techstack.kombify.io"+config.CanonicalAuthCallbackPath,
	)

	callbackReq := httptest.NewRequest(http.MethodGet, "/api/v2/auth/callback?code=code-123&state="+url.QueryEscape(state), nil)
	callbackReq.Host = "127.0.0.1:5260"
	callbackRec := httptest.NewRecorder()
	handler.ServeHTTP(callbackRec, callbackReq)
	if got, want := callbackRec.Code, http.StatusFound; got != want {
		t.Fatalf("callback status: got %d, want %d", got, want)
	}
	if got, want := callbackRec.Header().Get("Location"), "/api/v2/whoami"; got != want {
		t.Fatalf("callback redirect: got %q, want %q", got, want)
	}

	callbackCookies := callbackRec.Result().Cookies()
	if len(callbackCookies) != 1 {
		t.Fatalf("callback cookies: %+v", callbackCookies)
	}
	sessionCookie := callbackCookies[0]
	if sessionCookie.Name != authmw.DefaultSessionCookieName {
		t.Fatalf("session cookie name: got %q", sessionCookie.Name)
	}

	whoamiReq := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
	whoamiReq.AddCookie(sessionCookie)
	whoamiRec := httptest.NewRecorder()
	handler.ServeHTTP(whoamiRec, whoamiReq)
	if got, want := whoamiRec.Code, http.StatusOK; got != want {
		t.Fatalf("whoami status: got %d, want %d body=%q", got, want, whoamiRec.Body.String())
	}
	var whoami WhoAmIResponse
	if err := json.Unmarshal(whoamiRec.Body.Bytes(), &whoami); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if whoami.Subject != "user-123" || whoami.TenantID != "tenant-42" || whoami.Provider != "primary" || whoami.Role != "admin" {
		t.Fatalf("unexpected whoami: %+v", whoami)
	}

	logoutReq := httptest.NewRequest(http.MethodGet, "/api/v2/auth/logout?next=%2Fapi%2Fv2%2Fwhoami", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)
	if got, want := logoutRec.Code, http.StatusFound; got != want {
		t.Fatalf("logout status: got %d, want %d", got, want)
	}
	if got, want := logoutRec.Header().Get("Location"), "/api/v2/whoami"; got != want {
		t.Fatalf("logout redirect: got %q, want %q", got, want)
	}
	logoutCookies := logoutRec.Result().Cookies()
	if len(logoutCookies) != 1 || logoutCookies[0].Name != authmw.DefaultSessionCookieName || logoutCookies[0].MaxAge != -1 {
		t.Fatalf("logout cookies: %+v", logoutCookies)
	}

	whoamiAfterLogoutReq := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
	whoamiAfterLogoutRec := httptest.NewRecorder()
	handler.ServeHTTP(whoamiAfterLogoutRec, whoamiAfterLogoutReq)
	if got, want := whoamiAfterLogoutRec.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("whoami after logout status: got %d, want %d body=%q", got, want, whoamiAfterLogoutRec.Body.String())
	}
}

//nolint:gocyclo // End-to-end route test intentionally keeps the flow linear.
func TestEndToEndLocalAuthFlowThroughServerRoutes(t *testing.T) {
	provider, _, jwksServer := newServerProviderFixture(t)
	defer jwksServer.Close()

	sharedProvider, err := provider.ToOIDCClient()
	if err != nil {
		t.Fatal(err)
	}
	registry := commonoidc.NewRegistry()
	registry.Add(sharedProvider)

	mgr, err := session.NewManager(session.Config{
		Audience: "frontend",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}

	localSvc, err := commonauthlocal.New(commonauthlocal.Config{
		Store:             &serverLocalStore{},
		Sessions:          mgr,
		DefaultTenantID:   "tenant-default",
		BootstrapEmail:    "breakglass@techstack.local",
		SessionCookieName: authmw.DefaultSessionCookieName,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPassword, err := localSvc.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	handlers := commonauthlocal.NewHandlers(localSvc, registry, commonauthlocal.WithLoginRedirectPath("/api/v2/auth/login?provider="))
	handler := NewServer(
		WithSession(mgr),
		WithAuthHandlers(AuthHandlers{
			Methods:          handlers.MethodsHandler(),
			LocalLogin:       handlers.LoginHandler(),
			LocalLogout:      handlers.LogoutHandler(),
			BreakGlassClaim:  handlers.ClaimHandler(),
			BreakGlassReveal: handlers.RevealHandler(),
		}),
	).Routes()

	methodsReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/methods", nil)
	methodsRec := httptest.NewRecorder()
	handler.ServeHTTP(methodsRec, methodsReq)
	if got, want := methodsRec.Code, http.StatusOK; got != want {
		t.Fatalf("methods status: got %d, want %d body=%q", got, want, methodsRec.Body.String())
	}
	var methods commonauthlocal.MethodsResponse
	if unmarshalErr := json.Unmarshal(methodsRec.Body.Bytes(), &methods); unmarshalErr != nil {
		t.Fatalf("decode methods: %v", unmarshalErr)
	}
	if !methods.BreakGlass.Initialized || !methods.BreakGlass.HasPendingReveal || methods.BreakGlass.Claimed {
		t.Fatalf("unexpected breakglass status: %+v", methods.BreakGlass)
	}
	var sawOIDC bool
	var sawBreakGlass bool
	for _, providerInfo := range methods.Providers {
		switch providerInfo.ID {
		case "primary":
			sawOIDC = true
			if got, want := providerInfo.AuthURL, "/api/v2/auth/login?provider=primary"; got != want {
				t.Fatalf("primary auth_url: got %q, want %q", got, want)
			}
		case commonauthlocal.DefaultProviderID:
			sawBreakGlass = true
			if got, want := providerInfo.Kind, "breakglass"; got != want {
				t.Fatalf("breakglass provider kind: got %q, want %q", got, want)
			}
		}
	}
	if !sawOIDC || !sawBreakGlass {
		t.Fatalf("methods providers missing expected entries: %+v", methods.Providers)
	}

	revealReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/breakglass/reveal", nil)
	revealRec := httptest.NewRecorder()
	handler.ServeHTTP(revealRec, revealReq)
	if got, want := revealRec.Code, http.StatusOK; got != want {
		t.Fatalf("reveal status: got %d, want %d body=%q", got, want, revealRec.Body.String())
	}
	var revealed commonauthlocal.RevealResult
	if unmarshalErr := json.Unmarshal(revealRec.Body.Bytes(), &revealed); unmarshalErr != nil {
		t.Fatalf("decode reveal: %v", unmarshalErr)
	}
	if revealed.Email != "breakglass@techstack.local" || revealed.Password != bootstrapPassword || revealed.Claimed {
		t.Fatalf("unexpected reveal payload: %+v", revealed)
	}

	claimBody, err := json.Marshal(map[string]string{
		"current_password": revealed.Password,
		"new_email":        "operator@example.com",
		"new_password":     "supersecret123",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/breakglass/claim", strings.NewReader(string(claimBody)))
	claimReq.Header.Set("Content-Type", "application/json")
	claimRec := httptest.NewRecorder()
	handler.ServeHTTP(claimRec, claimReq)
	if got, want := claimRec.Code, http.StatusCreated; got != want {
		t.Fatalf("claim status: got %d, want %d body=%q", got, want, claimRec.Body.String())
	}
	var claimResp struct {
		OK       bool `json:"ok"`
		LoggedIn bool `json:"logged_in"`
	}
	if unmarshalErr := json.Unmarshal(claimRec.Body.Bytes(), &claimResp); unmarshalErr != nil {
		t.Fatalf("decode claim: %v", unmarshalErr)
	}
	if !claimResp.OK || !claimResp.LoggedIn {
		t.Fatalf("unexpected claim response: %+v", claimResp)
	}
	claimCookies := claimRec.Result().Cookies()
	if len(claimCookies) != 1 || claimCookies[0].Name != authmw.DefaultSessionCookieName {
		t.Fatalf("claim cookies: %+v", claimCookies)
	}
	claimedSession := claimCookies[0]

	whoamiReq := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
	whoamiReq.AddCookie(claimedSession)
	whoamiRec := httptest.NewRecorder()
	handler.ServeHTTP(whoamiRec, whoamiReq)
	if got, want := whoamiRec.Code, http.StatusOK; got != want {
		t.Fatalf("whoami status: got %d, want %d body=%q", got, want, whoamiRec.Body.String())
	}
	var whoami WhoAmIResponse
	if unmarshalErr := json.Unmarshal(whoamiRec.Body.Bytes(), &whoami); unmarshalErr != nil {
		t.Fatalf("decode whoami: %v", unmarshalErr)
	}
	if whoami.Email != "operator@example.com" || whoami.Provider != commonauthlocal.DefaultProviderID || whoami.TenantID != "tenant-default" {
		t.Fatalf("unexpected whoami: %+v", whoami)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(claimedSession)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)
	if got, want := logoutRec.Code, http.StatusOK; got != want {
		t.Fatalf("logout status: got %d, want %d body=%q", got, want, logoutRec.Body.String())
	}
	logoutCookies := logoutRec.Result().Cookies()
	if len(logoutCookies) != 1 || logoutCookies[0].Name != authmw.DefaultSessionCookieName || logoutCookies[0].MaxAge != -1 {
		t.Fatalf("logout cookies: %+v", logoutCookies)
	}

	revealAgainReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/breakglass/reveal", nil)
	revealAgainRec := httptest.NewRecorder()
	handler.ServeHTTP(revealAgainRec, revealAgainReq)
	if got, want := revealAgainRec.Code, http.StatusGone; got != want {
		t.Fatalf("reveal after claim status: got %d, want %d body=%q", got, want, revealAgainRec.Body.String())
	}

	loginBody, err := json.Marshal(map[string]string{
		"email":    "operator@example.com",
		"password": "supersecret123",
	})
	if err != nil {
		t.Fatal(err)
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(loginBody)))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if got, want := loginRec.Code, http.StatusOK; got != want {
		t.Fatalf("login status: got %d, want %d body=%q", got, want, loginRec.Body.String())
	}
	var loginResp struct {
		OK       bool   `json:"ok"`
		Email    string `json:"email"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if !loginResp.OK || loginResp.Email != "operator@example.com" || loginResp.Provider != commonauthlocal.DefaultProviderID {
		t.Fatalf("unexpected login response: %+v", loginResp)
	}
	loginCookies := loginRec.Result().Cookies()
	if len(loginCookies) != 1 || loginCookies[0].Name != authmw.DefaultSessionCookieName {
		t.Fatalf("login cookies: %+v", loginCookies)
	}
}
