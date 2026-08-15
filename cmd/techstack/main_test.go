package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commonauthlocal "github.com/kombifyio/go-common/authlocal"
	"github.com/kombifyio/go-common/authsession"
	commonedgeauth "github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/go-common/oidcclient"
	"github.com/kombifyio/techstack/internal/routes/sessionreauth"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	ksdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/middleware"
	v2 "github.com/kombifyio/techstack/pkg/v2"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

type memoryBreakglassStore struct {
	rec *commonauthlocal.Record
	err error
}

func (s *memoryBreakglassStore) Get(context.Context) (*commonauthlocal.Record, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.rec == nil {
		return nil, nil
	}
	rec := *s.rec
	return &rec, nil
}

func (s *memoryBreakglassStore) Save(_ context.Context, rec *commonauthlocal.Record) error {
	if s.err != nil {
		return s.err
	}
	copy := *rec
	s.rec = &copy
	return nil
}

func TestApplyDefaultServeHTTPAddsDefaultForServe(t *testing.T) {
	got := applyDefaultServeHTTP([]string{"techstack", "serve", "--dir", "pb_data"}, ":5260")
	want := []string{"techstack", "serve", "--http=:5260", "--dir", "pb_data"}

	if !slices.Equal(got, want) {
		t.Fatalf("applyDefaultServeHTTP() = %#v, want %#v", got, want)
	}
}

func TestProviderControlBootstrapRequestedOnlyForExactCommand(t *testing.T) {
	if !providerControlBootstrapRequested([]string{"techstack", "provider-control-bootstrap"}) {
		t.Fatal("exact provider-control bootstrap command was not detected")
	}
	for _, args := range [][]string{
		nil,
		{"techstack"},
		{"techstack", "serve"},
		{"techstack", "provider-control-bootstrap-extra"},
	} {
		if providerControlBootstrapRequested(args) {
			t.Fatalf("providerControlBootstrapRequested(%q) = true", args)
		}
	}
}

func TestNewTechstackAppUsesConfiguredPBDataDir(t *testing.T) {
	want := filepath.Join(t.TempDir(), "runtime", "pb")
	t.Setenv("TECHSTACK_PB_DATA_DIR", want)
	t.Setenv("TECHSTACK_DATA_DIR", filepath.Join(t.TempDir(), "ignored"))

	app := newTechstackApp()
	if got := app.DataDir(); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestNewTechstackAppDerivesPBDataDirFromDataDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("TECHSTACK_PB_DATA_DIR", "")
	t.Setenv("TECHSTACK_DATA_DIR", base)

	want := filepath.Join(base, "pb_data")
	app := newTechstackApp()
	if got := app.DataDir(); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestMergeCORSOriginsKeepsDefaultsAndConfiguredOrigins(t *testing.T) {
	got := mergeCORSOrigins(
		[]string{"http://localhost:5261", "http://127.0.0.1:5261"},
		[]string{"http://127.0.0.1:5281", " http://localhost:5261 ", "not an origin"},
	)

	for _, want := range []string{
		"http://localhost:5261",
		"http://127.0.0.1:5261",
		"http://127.0.0.1:5281",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("mergeCORSOrigins() = %#v, missing %q", got, want)
		}
	}
	if slices.Contains(got, "not an origin") {
		t.Fatalf("mergeCORSOrigins() kept invalid origin: %#v", got)
	}
}

func TestGlobalMiddlewareRateLimitsSignedEdgeUsersSeparately(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSaaS
	cfg.EdgeAuthSecret = "edge-secret"
	cfg.Server.RateLimitRPS = 1
	cfg.Server.RateLimitBurst = 1

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: cfg}})
	router.GET("/api/v1/probe", func(e *httpx.Event) error {
		return e.NoContent(http.StatusNoContent)
	})
	handler := router.BuildMux()

	for _, userID := range []string{"auth0|cloud-user-1", "auth0|cloud-user-2"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
		req.RemoteAddr = "10.0.0.1:443"
		req.Header.Set("X-Edge-Auth-Secret", "edge-secret")
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Org-ID", "tenant-1")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got, want := rec.Code, http.StatusNoContent; got != want {
			t.Fatalf("user %s status = %d body=%q, want %d", userID, got, rec.Body.String(), want)
		}
	}
}

func TestGlobalMiddlewareDoesNotRateLimitEmbeddedFrontendAssets(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.RateLimitRPS = 1
	cfg.Server.RateLimitBurst = 1

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: cfg}})
	router.GET("/_app/env.js", func(e *httpx.Event) error {
		_, _ = e.Response.Write([]byte("export const env = {};"))
		return nil
	})
	handler := router.BuildMux()

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/_app/env.js", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got, want := rec.Code, http.StatusOK; got != want {
			t.Fatalf("request %d status = %d body=%q, want %d", i+1, got, rec.Body.String(), want)
		}
	}
}

func TestGlobalMiddlewareDoesNotRateLimitAuthStateChecks(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.RateLimitRPS = 1
	cfg.Server.RateLimitBurst = 1

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: cfg}})
	for _, path := range []string{"/api/v1/auth/mode", "/api/v1/auth/stack-identity", "/api/v2/whoami", "/api/v2/auth/providers"} {
		routePath := path
		router.GET(routePath, func(e *httpx.Event) error {
			return e.NoContent(http.StatusNoContent)
		})
	}
	handler := router.BuildMux()

	for i := 0; i < 3; i++ {
		for _, path := range []string{"/api/v1/auth/mode", "/api/v1/auth/stack-identity", "/api/v2/whoami", "/api/v2/auth/providers"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if got, want := rec.Code, http.StatusNoContent; got != want {
				t.Fatalf("request %d %s status = %d body=%q, want %d", i+1, path, got, rec.Body.String(), want)
			}
		}
	}
}

func TestGlobalMiddlewareUsesIsolatedLimitForSignedPortalExchange(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.RateLimitRPS = 1
	cfg.Server.RateLimitBurst = 1

	router := httpx.NewRouter()
	bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: cfg}})
	router.POST("/api/v1/auth/portal-verify", func(e *httpx.Event) error {
		return e.NoContent(http.StatusNoContent)
	})
	handler := router.BuildMux()

	for i := 0; i < 8; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/portal-verify", strings.NewReader(`{"token":"signed-portal-token"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got, want := rec.Code, http.StatusNoContent; got != want {
			t.Fatalf("request %d status = %d body=%q, want %d", i+1, got, rec.Body.String(), want)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/portal-verify", strings.NewReader(`{"token":"signed-portal-token"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got, want := rec.Code, http.StatusTooManyRequests; got != want {
		t.Fatalf("request beyond isolated portal burst status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
}

func TestLocalDeviceSessionIssuesDeviceAdminCookieWithoutOwner(t *testing.T) {
	deviceToken := strings.Repeat("a", 64)
	t.Setenv(localDeviceTokenEnv, deviceToken)
	handler, manager := localDeviceSessionTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
	req.RemoteAddr = "127.0.0.1:52100"
	req.Header.Set(localDeviceSessionHeader, deviceToken)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "techstack_session" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %+v, want one HttpOnly techstack_session", cookies)
	}
	claims, err := manager.Verify(cookies[0].Value)
	if err != nil {
		t.Fatalf("verify session: %v", err)
	}
	if claims.Email != localDeviceEmail || claims.Provider != localDeviceProvider || claims.Role != "admin" || !strings.HasPrefix(claims.Subject, "device:") {
		t.Fatalf("claims = %+v", claims)
	}
	protected := httptest.NewRequest(http.MethodGet, "/test/device-admin", nil)
	protected.RemoteAddr = "127.0.0.1:52100"
	protected.AddCookie(cookies[0])
	protectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("device admin route status=%d body=%q", protectedResponse.Code, protectedResponse.Body.String())
	}
}

func TestLocalDeviceSessionRejectsInvalidToken(t *testing.T) {
	t.Setenv(localDeviceTokenEnv, strings.Repeat("a", 64))
	handler, _ := localDeviceSessionTestHandler(t, &commonauthlocal.Record{
		Email:   "demo@techstack.local",
		Claimed: true,
	})

	req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
	req.RemoteAddr = "127.0.0.1:52100"
	req.Header.Set(localDeviceSessionHeader, strings.Repeat("b", 64))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
}

func TestLocalDeviceSessionRequiresLoopback(t *testing.T) {
	deviceToken := strings.Repeat("a", 64)
	t.Setenv(localDeviceTokenEnv, deviceToken)
	handler, _ := localDeviceSessionTestHandler(t, &commonauthlocal.Record{
		Email:   "demo@techstack.local",
		Claimed: true,
	})

	req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
	req.RemoteAddr = "10.1.2.3:52100"
	req.Header.Set(localDeviceSessionHeader, deviceToken)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusForbidden; got != want {
		t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
}

func TestLocalDeviceSessionDoesNotDependOnClaimedOwner(t *testing.T) {
	deviceToken := strings.Repeat("a", 64)
	t.Setenv(localDeviceTokenEnv, deviceToken)
	handler, _ := localDeviceSessionTestHandler(t, &commonauthlocal.Record{
		Email:   "breakglass@techstack.local",
		Claimed: false,
	})

	req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
	req.RemoteAddr = "127.0.0.1:52100"
	req.Header.Set(localDeviceSessionHeader, deviceToken)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
}

func TestLocalDeviceSessionIsUnavailableWithoutDeviceToken(t *testing.T) {
	t.Setenv(localDeviceTokenEnv, "")
	handler, _ := localDeviceSessionTestHandler(t, &commonauthlocal.Record{
		Email:   "demo@techstack.local",
		Claimed: true,
	})

	req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
	req.RemoteAddr = "127.0.0.1:52100"
	req.Header.Set(localDeviceSessionHeader, strings.Repeat("a", 64))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusNotFound; got != want {
		t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
	}
}

func TestLocalDeviceSessionIsUnavailableOutsideLocalPosture(t *testing.T) {
	deviceToken := strings.Repeat("a", 64)
	for _, environment := range []string{"production", "development", "staging"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv(localDeviceTokenEnv, deviceToken)
			handler, _ := localDeviceSessionTestHandlerWithEnv(t, &commonauthlocal.Record{
				Email:   "demo@techstack.local",
				Claimed: true,
			}, environment)

			req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
			req.RemoteAddr = "127.0.0.1:52100"
			req.Header.Set(localDeviceSessionHeader, deviceToken)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if got, want := rec.Code, http.StatusNotFound; got != want {
				t.Fatalf("status = %d body=%q, want %d", got, rec.Body.String(), want)
			}
		})
	}
}

func TestLocalDeviceSessionDampsBruteForce(t *testing.T) {
	deviceToken := strings.Repeat("a", 64)
	t.Setenv(localDeviceTokenEnv, deviceToken)
	handler, _ := localDeviceSessionTestHandler(t, &commonauthlocal.Record{
		Email:   "demo@techstack.local",
		Claimed: true,
	})

	attempt := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, localDeviceSessionPath, nil)
		req.RemoteAddr = "127.0.0.1:52100"
		req.Header.Set(localDeviceSessionHeader, token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < localDeviceSessionMaxFailures; i++ {
		if got, want := attempt(strings.Repeat("b", 64)), http.StatusUnauthorized; got != want {
			t.Fatalf("attempt %d status = %d, want %d", i+1, got, want)
		}
	}
	if got, want := attempt(deviceToken), http.StatusTooManyRequests; got != want {
		t.Fatalf("damped status = %d, want %d (valid token must also be damped during cooldown)", got, want)
	}
}

func localDeviceSessionTestHandler(t *testing.T, rec *commonauthlocal.Record) (http.Handler, *authsession.Manager) {
	return localDeviceSessionTestHandlerWithEnv(t, rec, "local")
}

func localDeviceSessionTestHandlerWithEnv(t *testing.T, rec *commonauthlocal.Record, environment string) (http.Handler, *authsession.Manager) {
	t.Helper()
	localDeviceSessionAttempts.reset()
	t.Cleanup(localDeviceSessionAttempts.reset)
	cfg := config.DefaultConfig()
	cfg.Server.Environment = environment
	cfg.Server.RateLimitRPS = 20
	cfg.Server.RateLimitBurst = 20
	manager, err := authsession.NewManager(authsession.Config{
		Audience: "techstack-local",
		Secret:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatalf("authsession.NewManager: %v", err)
	}
	router := httpx.NewRouter()
	deps := routeDeps{
		startup: &startupContext{cfg: cfg},
		v2: &v2Boot{
			session:       manager,
			cookieName:    "techstack_session",
			defaultTenant: "default",
		},
		log: logger.Default(),
	}
	bindGlobalMiddleware(router, deps)
	registerLocalDeviceSessionRouteWithStore(router, deps, &memoryBreakglassStore{rec: rec})
	router.GET("/test/device-admin", func(e *httpx.Event) error {
		id := identity.FromContext(e.Request.Context())
		if id == nil || !id.IsAuthenticated() || !slices.Contains(id.Roles, "admin") {
			return httpx.Forbidden(e, "device admin session required")
		}
		return httpx.Success(e, http.StatusOK, map[string]any{"ok": true})
	})
	return router.BuildMux(), manager
}

func TestStackControlPlaneStoresUsePostgresWhenV2DBIsReady(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	stores := stackControlPlaneStores(&v2Boot{db: &ksdb.DB{DB: sqlDB}})
	if stores.Stacks == nil {
		t.Fatal("stack store is nil")
	}
	if stores.Jobs == nil {
		t.Fatal("job store is nil")
	}
	if stores.Wallet == nil {
		t.Fatal("wallet store is nil")
	}
}

func TestEnsureDefaultControlPlaneTenantRunsWithoutBrowserSession(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 7, 18, 16, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT set_config").
		WithArgs("app.tenant_id", "tenant-local").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO techstack_tenants").
		WithArgs("tenant-local", "", "tenant-local", "self_hosted", "active", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id, external_org_id, display_name, kind, status").
		WithArgs("tenant-local").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "external_org_id", "display_name", "kind", "status", "metadata_json", "created_at", "updated_at",
		}).AddRow("tenant-local", nil, "tenant-local", "self_hosted", "active", `{}`, now, now))
	mock.ExpectCommit()

	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSelfHosted
	boot := &v2Boot{db: &ksdb.DB{DB: sqlDB}, defaultTenant: "tenant-local"}
	if boot.session != nil {
		t.Fatal("test precondition: browser session must be absent")
	}
	if err := ensureDefaultControlPlaneTenant(context.Background(), cfg, boot); err != nil {
		t.Fatalf("ensureDefaultControlPlaneTenant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestStackControlPlaneStoresEmptyWithoutV2DB(t *testing.T) {
	stores := stackControlPlaneStores(&v2Boot{})
	if stores.Stacks != nil || stores.Jobs != nil || stores.Wallet != nil {
		t.Fatalf("stores = %#v, want empty without db", stores)
	}
}

func TestV2AuthPublicOriginHandlerRewritesRenderLoopbackOrigin(t *testing.T) {
	var seenHost, seenForwardedHost, seenForwardedProto string
	handler := v2AuthPublicOriginHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		seenForwardedHost = r.Header.Get("X-Forwarded-Host")
		seenForwardedProto = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(http.StatusNoContent)
	}), "https://techstack.kombify.io")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5262/api/v2/auth/login", nil)
	req.Host = "127.0.0.1:5262"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := seenHost, "techstack.kombify.io"; got != want {
		t.Fatalf("Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedHost, "techstack.kombify.io"; got != want {
		t.Fatalf("X-Forwarded-Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedProto, "https"; got != want {
		t.Fatalf("X-Forwarded-Proto = %q, want %q", got, want)
	}
}

func TestV2AuthPublicOriginHandlerPreservesRenderPreviewOrigin(t *testing.T) {
	var seenHost, seenForwardedHost, seenForwardedProto string
	handler := v2AuthPublicOriginHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		seenForwardedHost = r.Header.Get("X-Forwarded-Host")
		seenForwardedProto = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(http.StatusNoContent)
	}), "https://techstack.kombify.io")

	req := httptest.NewRequest(http.MethodGet, "https://kombify-techstack-pr-137.onrender.com/api/v2/auth/login", nil)
	req.Host = "kombify-techstack-pr-137.onrender.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "kombify-techstack-pr-137.onrender.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := seenHost, "kombify-techstack-pr-137.onrender.com"; got != want {
		t.Fatalf("Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedHost, "kombify-techstack-pr-137.onrender.com"; got != want {
		t.Fatalf("X-Forwarded-Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedProto, "https"; got != want {
		t.Fatalf("X-Forwarded-Proto = %q, want %q", got, want)
	}
}

func TestV2AuthPublicOriginHandlerUsesRenderPreviewEnvOrigin(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "preview")
	t.Setenv("RENDER_EXTERNAL_URL", "https://kombify-techstack-pr-137.onrender.com")

	var seenHost, seenForwardedHost, seenForwardedProto string
	handler := v2AuthPublicOriginHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		seenForwardedHost = r.Header.Get("X-Forwarded-Host")
		seenForwardedProto = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(http.StatusNoContent)
	}), "https://techstack.kombify.io")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5262/api/v2/auth/login", nil)
	req.Host = "127.0.0.1:5262"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := seenHost, "kombify-techstack-pr-137.onrender.com"; got != want {
		t.Fatalf("Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedHost, "kombify-techstack-pr-137.onrender.com"; got != want {
		t.Fatalf("X-Forwarded-Host = %q, want %q", got, want)
	}
	if got, want := seenForwardedProto, "https"; got != want {
		t.Fatalf("X-Forwarded-Proto = %q, want %q", got, want)
	}
}

func TestV2AuthProviderFromEnvUsesFallbacks(t *testing.T) {
	t.Setenv("TECHSTACK_V2_AUTH_ISSUER", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "")
	t.Setenv("AUTH0_ISSUER", "")
	t.Setenv("AUTH0_DOMAIN", "login.kombify.io/")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "client-123")
	t.Setenv("TECHSTACK_V2_AUTH_KIND", "")
	t.Setenv("TECHSTACK_V2_AUTH_PROVIDER_ID", "")

	provider, err := v2AuthProviderFromEnv()
	if err != nil {
		t.Fatalf("v2AuthProviderFromEnv() error = %v", err)
	}
	if provider == nil {
		t.Fatal("v2AuthProviderFromEnv() returned nil provider")
	}
	if got, want := provider.ID(), "primary"; got != want {
		t.Fatalf("provider.ID() = %q, want %q", got, want)
	}
	if got, want := provider.Kind(), providers.KindAuth0; got != want {
		t.Fatalf("provider.Kind() = %q, want %q", got, want)
	}
	if got, want := provider.Issuer(), config.DefaultCloudAuthIssuer+"/"; got != want {
		t.Fatalf("provider.Issuer() = %q, want %q", got, want)
	}
	if got, want := provider.ClientID(), "client-123"; got != want {
		t.Fatalf("provider.ClientID() = %q, want %q", got, want)
	}
}

func TestV2AuthProviderFromEnvDefaultsToLoginKombifyIssuer(t *testing.T) {
	t.Setenv("TECHSTACK_V2_AUTH_ISSUER", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "")
	t.Setenv("AUTH0_DOMAIN", "")
	t.Setenv("AUTH0_ISSUER", "")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "client-123")
	t.Setenv("AUTH0_CLIENT_ID", "")
	t.Setenv("TECHSTACK_V2_AUTH_KIND", "")

	provider, err := v2AuthProviderFromEnv()
	if err != nil {
		t.Fatalf("v2AuthProviderFromEnv() error = %v", err)
	}
	if provider == nil {
		t.Fatal("v2AuthProviderFromEnv() returned nil provider")
	}
	if got, want := provider.Issuer(), config.DefaultCloudAuthIssuer+"/"; got != want {
		t.Fatalf("provider.Issuer() = %q, want %q", got, want)
	}
	if got, want := provider.Kind(), providers.KindAuth0; got != want {
		t.Fatalf("provider.Kind() = %q, want %q", got, want)
	}
}

func TestV2SessionAudienceFromEnvDefaultsToLocal(t *testing.T) {
	t.Setenv("TECHSTACK_V2_SESSION_AUDIENCE", "")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "")

	if got, want := v2SessionAudienceFromEnv(), "techstack-local"; got != want {
		t.Fatalf("v2SessionAudienceFromEnv() = %q, want %q", got, want)
	}
}

func TestV2SessionAudienceFromEnvUsesExplicitValue(t *testing.T) {
	t.Setenv("TECHSTACK_V2_SESSION_AUDIENCE", "custom-audience")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "client-id")

	if got, want := v2SessionAudienceFromEnv(), "custom-audience"; got != want {
		t.Fatalf("v2SessionAudienceFromEnv() = %q, want %q", got, want)
	}
}

func TestCompiledProductVersionIgnoresRuntimeProductVersion(t *testing.T) {
	t.Setenv("TECHSTACK_VERSION", "9.9.9")

	if got := compiledProductVersion(defaultProductVersion); got != defaultProductVersion {
		t.Fatalf("compiledProductVersion() = %q, want immutable build version %s", got, defaultProductVersion)
	}
}

func TestNormalizeProductVersionTrimsReleasePrefix(t *testing.T) {
	if got := normalizeProductVersion("v0.4.0-alpha.1"); got != "0.4.0-alpha.1" {
		t.Fatalf("normalizeProductVersion() = %q, want 0.4.0-alpha.1", got)
	}
}

func TestNormalizeProductVersionRejectsNonSemver(t *testing.T) {
	for _, value := range []string{"main", "release", "0d2e7db", "2026-05-17"} {
		t.Run(value, func(t *testing.T) {
			if got := normalizeProductVersion(value); got != "" {
				t.Fatalf("normalizeProductVersion() = %q, want empty", got)
			}
		})
	}
}

func TestCompiledBuildRevisionIgnoresRuntimeRevision(t *testing.T) {
	t.Setenv("TECHSTACK_BUILD_REVISION", "ffffffffffffffffffffffffffffffffffffffff")

	const linked = "02fa3578e0e6f74362d4208023a241cf4d2434ac"
	if got := compiledBuildRevision(linked); got != linked {
		t.Fatalf("compiledBuildRevision() = %q, want linked revision %q", got, linked)
	}
	for _, invalid := range []string{"deadbeef", "dev", "", "g" + linked[1:]} {
		if got := compiledBuildRevision(invalid); got != "dev" {
			t.Fatalf("compiledBuildRevision(%q) = %q, want dev", invalid, got)
		}
	}
}

func TestV2AuthProviderFromEnvRequiresIssuerAndClientID(t *testing.T) {
	t.Setenv("TECHSTACK_V2_AUTH_ISSUER", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "")
	t.Setenv("AUTH0_DOMAIN", "")
	t.Setenv("AUTH0_ISSUER", "")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "")

	provider, err := v2AuthProviderFromEnv()
	if err != nil {
		t.Fatalf("v2AuthProviderFromEnv() error = %v", err)
	}
	if provider != nil {
		t.Fatalf("v2AuthProviderFromEnv() = %+v, want nil", provider)
	}
}

func TestConfigureV2AuthUsesBootDefaultTenantWithoutCloudProvider(t *testing.T) {
	t.Setenv("TECHSTACK_V2_SESSION_SECRET", strings.Repeat("test-", 8))
	t.Setenv("TECHSTACK_V2_SESSION_AUDIENCE", "techstack-local")
	t.Setenv("TECHSTACK_V2_DEFAULT_TENANT_ID", "tenant-local")
	t.Setenv("TECHSTACK_V2_AUTH_ISSUER", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "")
	t.Setenv("AUTH0_DOMAIN", "")
	t.Setenv("AUTH0_ISSUER", "")
	t.Setenv("TECHSTACK_V2_AUTH_CLIENT_ID", "")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "")

	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSelfHosted
	boot := &v2Boot{defaultTenant: v2DefaultTenantIDFromEnv()}
	options := []v2.Option{}

	configureV2Auth(context.Background(), cfg, logger.Default(), boot, &options)

	if boot.session == nil {
		t.Fatal("configureV2Auth() did not initialize the session manager")
	}
	if boot.registry != nil {
		t.Fatalf("configureV2Auth() registry = %#v, want nil without cloud provider", boot.registry)
	}
	if got, want := boot.defaultTenant, "tenant-local"; got != want {
		t.Fatalf("configureV2Auth() defaultTenant = %q, want %q", got, want)
	}
	token, err := boot.session.Issue(authsession.Claims{Subject: "pocketbase:user-1", TenantID: boot.defaultTenant})
	if err != nil {
		t.Fatalf("session issue with local default tenant failed: %v", err)
	}
	claims, err := boot.session.Verify(token)
	if err != nil {
		t.Fatalf("verify issued session failed: %v", err)
	}
	if got, want := time.Duration(claims.Expires-claims.IssuedAt)*time.Second, sessionpolicy.BrowserSessionLifetime; got != want {
		t.Fatalf("session lifetime = %s, want %s", got, want)
	}
}

func TestV2SessionIdentityMiddlewareAcceptsSessionCookie(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(authsession.Claims{
		Subject:  "auth0|runtime-user",
		TenantID: "tenant-1",
		OrgID:    "org-1",
		Email:    "runtime@example.com",
		Role:     "admin,user",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	req.AddCookie(&http.Cookie{
		Name:     "techstack_session",
		Value:    token,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || !id.IsAuthenticated() {
		t.Fatal("v2 session did not populate authenticated identity")
	}
	if got, want := id.UserID, "auth0|runtime-user"; got != want {
		t.Fatalf("identity.UserID = %q, want %q", got, want)
	}
	if got, want := id.OrgID, "org-1"; got != want {
		t.Fatalf("identity.OrgID = %q, want %q", got, want)
	}
	if !id.HasRole("admin") || !id.HasRole("user") {
		t.Fatalf("identity roles = %#v, want admin and user", id.Roles)
	}
}

// stubAuthStore is an in-test controlplane.AuthStore double that records the
// upserts performed during cloud login.
type stubAuthStore struct {
	tenants     []controlplane.Tenant
	users       []controlplane.User
	memberships []controlplane.Membership
	membership  *controlplane.Membership
	calls       []string
}

func (s *stubAuthStore) UpsertTenant(_ context.Context, t controlplane.Tenant) (*controlplane.Tenant, error) {
	s.calls = append(s.calls, "tenant")
	s.tenants = append(s.tenants, t)
	return &t, nil
}
func (s *stubAuthStore) UpsertUser(_ context.Context, u controlplane.User) (*controlplane.User, error) {
	s.calls = append(s.calls, "user")
	s.users = append(s.users, u)
	return &u, nil
}
func (s *stubAuthStore) UpsertMembership(_ context.Context, m controlplane.Membership) (*controlplane.Membership, error) {
	s.calls = append(s.calls, "membership")
	s.memberships = append(s.memberships, m)
	stored := m
	s.membership = &stored
	return &m, nil
}
func (s *stubAuthStore) GetMembership(_ context.Context, tenantID, userID string) (*controlplane.Membership, error) {
	if s.membership == nil || s.membership.TenantID != tenantID || s.membership.UserID != userID {
		return nil, controlplane.ErrNotFound
	}
	stored := *s.membership
	return &stored, nil
}
func (s *stubAuthStore) ListMembershipsByUser(_ context.Context, userID string) ([]controlplane.Membership, error) {
	var out []controlplane.Membership
	for _, m := range s.memberships {
		if m.UserID == userID && (m.Status == "" || strings.EqualFold(m.Status, "active")) {
			out = append(out, m)
		}
	}
	return out, nil
}
func (s *stubAuthStore) UpsertAuthConfig(_ context.Context, c controlplane.AuthConfig) (*controlplane.AuthConfig, error) {
	return &c, nil
}
func (s *stubAuthStore) UpsertBreakglassAdmin(_ context.Context, a controlplane.BreakglassAdmin) (*controlplane.BreakglassAdmin, error) {
	return &a, nil
}
func (s *stubAuthStore) GetBreakglassAdmin(_ context.Context, _ string) (*controlplane.BreakglassAdmin, error) {
	return nil, controlplane.ErrNotFound
}

func TestV2CloudUserUpsertPersistsToControlPlaneAuthStore(t *testing.T) {
	store := &stubAuthStore{}
	claims := &oidcclient.Claims{
		Subject: "auth0|runtime-user",
		Email:   "runtime-user@kombify.io",
		Name:    "Runtime User",
		Raw: map[string]interface{}{
			"https://kombify.io/roles":        []interface{}{"admin", "global_admin", "developer"},
			"https://kombify.io/entitlements": []interface{}{"techstack.managed.runtime.cloudkit", "techstack.managed.runtime.ionos"},
		},
	}
	if err := v2CloudUserUpsert(store, "tenant-1")(context.Background(), claims, "tenant-1", "primary"); err != nil {
		t.Fatalf("v2CloudUserUpsert() error = %v", err)
	}

	if len(store.users) != 1 || store.users[0].ID != claims.Subject || store.users[0].PrimaryEmail != claims.Email {
		t.Fatalf("UpsertUser = %#v, want id=%q email=%q", store.users, claims.Subject, claims.Email)
	}
	if len(store.memberships) != 1 ||
		store.memberships[0].ProviderKey != "cloud" ||
		store.memberships[0].SubjectID != claims.Subject ||
		store.memberships[0].UserID != claims.Subject ||
		store.memberships[0].TenantID != "tenant-1" ||
		store.memberships[0].RoleKey != "global_admin" {
		t.Fatalf("UpsertMembership = %#v, want cloud link of %q under tenant-1", store.memberships, claims.Subject)
	}
	roles, ok := store.memberships[0].Metadata["platform_roles"].([]string)
	if !ok || !slices.Equal(roles, []string{"global_admin", "admin", "developer"}) {
		t.Fatalf("membership platform_roles = %#v, want global_admin/admin/developer", store.memberships[0].Metadata["platform_roles"])
	}
	entitlements, ok := store.memberships[0].Metadata["entitlements"].([]string)
	if !ok || !slices.Equal(entitlements, []string{"techstack.managed.runtime.cloudkit", "techstack.managed.runtime.ionos"}) {
		t.Fatalf("membership entitlements = %#v, want managed runtime CloudKit/IONOS", store.memberships[0].Metadata["entitlements"])
	}

	// The OIDC subject is the canonical control-plane user id.
	id := identityFromV2SessionClaims(&authsession.Claims{Subject: claims.Subject, Email: claims.Email})
	if got, want := id.UserID, claims.Subject; got != want {
		t.Fatalf("identity.UserID = %q, want %q (subject is canonical user id)", got, want)
	}
}

func TestV2CloudUserUpsertDefaultsMembershipRoleToMember(t *testing.T) {
	store := &stubAuthStore{}
	claims := &oidcclient.Claims{
		Subject: "auth0|runtime-user",
		Email:   "runtime-user@kombify.io",
		Name:    "Runtime User",
	}
	if err := v2CloudUserUpsert(store, "tenant-1")(context.Background(), claims, "tenant-1", "primary"); err != nil {
		t.Fatalf("v2CloudUserUpsert() error = %v", err)
	}
	if len(store.memberships) != 1 || store.memberships[0].RoleKey != "member" {
		t.Fatalf("UpsertMembership = %#v, want member role", store.memberships)
	}
}

func TestV2SessionIdentityUsesDemoTenantForDemoSubject(t *testing.T) {
	t.Setenv("TECHSTACK_DEMO_USER_IDS", "auth0|demo-user")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	id := identityFromV2SessionClaims(&authsession.Claims{
		Subject:  "auth0|demo-user",
		TenantID: "default",
		Email:    "demo@kombified.com",
	})
	if id.UserID != "auth0|demo-user" || id.OrgID != "tenant-demo" {
		t.Fatalf("identity = %+v, want demo user mapped to tenant-demo", id)
	}
}

func TestV2SessionIdentityMiddlewareHydratesDemoTenantMembership(t *testing.T) {
	t.Setenv("TECHSTACK_DEMO_USER_IDS", "auth0|demo-user")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(authsession.Claims{
		Subject:  "auth0|demo-user",
		TenantID: "default",
		Email:    "demo@kombified.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "tenant-demo",
		UserID:   "auth0|demo-user",
		RoleKey:  "member",
		Status:   "active",
	}}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, authStore: store, defaultTenant: "default", cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || id.OrgID != "tenant-demo" {
		t.Fatalf("identity = %+v, want demo tenant", id)
	}
}

func TestV2SessionIdentityMiddlewareHydratesMembershipRole(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(authsession.Claims{
		Subject:  "auth0|runtime-user",
		TenantID: "tenant-1",
		Email:    "runtime@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req.AddCookie(&http.Cookie{
		Name:     "techstack_session",
		Value:    token,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "tenant-1",
		UserID:   "auth0|runtime-user",
		RoleKey:  "global_admin",
		Status:   "active",
		Metadata: map[string]any{
			"entitlements": []string{"techstack.managed.runtime.cloudkit", "techstack.managed.runtime.ionos"},
		},
	}}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, authStore: store, cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || !id.HasRole("global_admin") {
		t.Fatalf("identity roles = %#v, want global_admin from membership", id)
	}
	flags, ok := commonedgeauth.FlagsFromContext(event.Request.Context())
	if !ok || !flags.Flags["techstack.managed.runtime.cloudkit"] || !flags.Flags["techstack.managed.runtime.ionos"] {
		t.Fatalf("edge flags = %#v, want CloudKit and IONOS entitlements from membership", flags)
	}
	entitlements, ok := middleware.SignedEntitlementsFromContext(event.Request.Context())
	if !ok || !entitlements.Has("techstack.managed.runtime.cloudkit") || !entitlements.Has("techstack.managed.runtime.ionos") {
		t.Fatalf("authorization entitlements = %#v, want CloudKit and IONOS grants from the server-owned membership", entitlements.Values())
	}
}

func TestV2SessionIdentityMiddlewareHydratesExistingEdgeIdentity(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	ctx := middleware.WithSignedEntitlements(req.Context(), "edge.only")
	req = req.WithContext(identity.NewContext(ctx, &identity.Identity{
		UserID: "google-oauth2|109651582266724371152",
		OrgID:  "marcel-personal",
		Email:  "mako.181092@googlemail.com",
	}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "default",
		UserID:   "google-oauth2|109651582266724371152",
		RoleKey:  "global_admin",
		Status:   "active",
		Metadata: map[string]any{"entitlements": []string{"techstack.managed.runtime.cloudkit"}},
	}}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, authStore: store, defaultTenant: "default", cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if got, want := id.UserID, "google-oauth2|109651582266724371152"; got != want {
		t.Fatalf("identity.UserID = %q, want existing edge identity %q", got, want)
	}
	if !id.HasRole("global_admin") {
		t.Fatalf("identity roles = %#v, want global_admin from default membership fallback", id.Roles)
	}
	if len(store.tenants) != 1 || store.tenants[0].ID != "marcel-personal" || store.tenants[0].Kind != "saas" {
		t.Fatalf("projected tenants = %#v, want signed edge tenant marcel-personal", store.tenants)
	}
	if len(store.memberships) != 1 || store.memberships[0].TenantID != "marcel-personal" || store.memberships[0].UserID != id.UserID {
		t.Fatalf("projected memberships = %#v, want org-scoped copy of fallback membership", store.memberships)
	}
	if got := store.memberships[0].Metadata["entitlements"]; !reflect.DeepEqual(got, []string{"techstack.managed.runtime.cloudkit"}) {
		t.Fatalf("projected entitlements = %#v, want fallback membership entitlements preserved", got)
	}
	entitlements, ok := middleware.SignedEntitlementsFromContext(event.Request.Context())
	if !ok || !slices.Equal(entitlements.Values(), []string{"edge.only"}) {
		t.Fatalf("authorization entitlements = %#v, want existing Edge-signed grant preserved", entitlements.Values())
	}
}

func TestV2SessionIdentityMiddlewareRejectsUnbackedEdgeTenant(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|unbacked-user",
		OrgID:  "unbacked-org",
	}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	store := &stubAuthStore{}

	err = v2SessionIdentityMiddleware(&v2Boot{session: mgr, authStore: store, defaultTenant: "default"})(event)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("middleware error = %v, want retryable 401 session_reprojection_required", err)
	}
	details, ok := apiErr.Details.(map[string]any)
	if !ok || details["reason_code"] != sessionreauth.ReasonCode || details["retryable"] != true {
		t.Fatalf("denial details = %#v, want reason_code=%q retryable=true", apiErr.Details, sessionreauth.ReasonCode)
	}
	if len(store.tenants) != 0 || len(store.memberships) != 0 {
		t.Fatalf("unexpected projection for unbacked identity: tenants=%#v memberships=%#v", store.tenants, store.memberships)
	}
}

func TestEnsureIdentityTenantProjectionRejectsIncompleteDemoIdentityPair(t *testing.T) {
	t.Setenv("TECHSTACK_DEMO_USER_IDS", "auth0|demo-user")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	for _, tc := range []struct {
		name     string
		userID   string
		tenantID string
	}{
		{
			name:     "configured demo subject in another tenant",
			userID:   "auth0|demo-user",
			tenantID: "tenant-attacker",
		},
		{
			name:     "unconfigured subject in demo tenant",
			userID:   "auth0|other-user",
			tenantID: "tenant-demo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubAuthStore{}
			membership, err := ensureIdentityTenantProjection(
				t.Context(),
				store,
				&identity.Identity{UserID: tc.userID, OrgID: tc.tenantID},
				nil,
			)
			if err == nil || membership != nil {
				t.Fatalf("projection = %#v, error = %v, want fail-closed rejection", membership, err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("unexpected bootstrap calls = %#v", store.calls)
			}
		})
	}
}

func TestV2SessionIdentityMiddlewareHydratesMissingEdgeTenantFromMembership(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|runtime-user",
		Email:  "runtime@example.com",
	}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "default",
		UserID:   "auth0|runtime-user",
		RoleKey:  "member",
		Status:   "active",
	}}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, authStore: store, defaultTenant: "default", cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || id.OrgID != "default" {
		t.Fatalf("identity = %+v, want default tenant from membership", id)
	}
}

func TestV2SessionIdentityMiddlewareKeepsExistingEdgeIdentity(t *testing.T) {
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(authsession.Claims{Subject: "auth0|runtime-user", TenantID: "tenant-1"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "edge:user"}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}

	if err := v2SessionIdentityMiddleware(&v2Boot{session: mgr, cookieName: "techstack_session"})(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if got, want := id.UserID, "edge:user"; got != want {
		t.Fatalf("identity.UserID = %q, want existing edge identity %q", got, want)
	}
}

func TestV2DefaultProviderIDUsesSingleProviderFallback(t *testing.T) {
	t.Setenv("TECHSTACK_V2_AUTH_PROVIDER_ID", "")
	registry := providers.NewRegistry()
	provider, err := providers.New(providers.Config{
		ID:       "primary",
		Kind:     providers.KindGeneric,
		Issuer:   "https://id.example.com",
		ClientID: "frontend",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(provider)

	if got, want := v2DefaultProviderID(registry), "primary"; got != want {
		t.Fatalf("v2DefaultProviderID() = %q, want %q", got, want)
	}
}

func TestApplySelfHostedCloudLoginGate_DisablesRegistryWithoutEnrollment(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://stack.example.com")
	t.Setenv("TECHSTACK_SELFHOSTED_CLOUD_LOGIN_TOKEN", "")
	t.Setenv("TECHSTACK_SELFHOSTED_CLOUD_LOGIN_PUBLIC_KEY", "")

	registry := providers.NewRegistry()
	provider, err := providers.New(providers.Config{
		ID:       "primary",
		Kind:     providers.KindAuth0,
		Issuer:   "https://login.kombify.io",
		ClientID: "client-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(provider)

	gated, result := applySelfHostedCloudLoginGate(config.ModeSelfHosted, registry)
	if gated != nil {
		t.Fatalf("applySelfHostedCloudLoginGate() registry = %#v, want nil", gated)
	}
	if result.Enabled {
		t.Fatalf("applySelfHostedCloudLoginGate() enabled = true, want false")
	}
	if result.Reason != "token_missing" {
		t.Fatalf("applySelfHostedCloudLoginGate() reason = %q, want token_missing", result.Reason)
	}
}

func TestApplySelfHostedCloudLoginGate_SkipsSaaS(t *testing.T) {
	registry := providers.NewRegistry()
	provider, err := providers.New(providers.Config{
		ID:       "primary",
		Kind:     providers.KindAuth0,
		Issuer:   "https://login.kombify.io",
		ClientID: "client-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(provider)

	gated, result := applySelfHostedCloudLoginGate(config.ModeSaaS, registry)
	if gated == nil || gated.Len() != 1 {
		t.Fatalf("applySelfHostedCloudLoginGate() registry = %#v, want unchanged registry", gated)
	}
	if !result.Enabled {
		t.Fatalf("applySelfHostedCloudLoginGate() enabled = false, want true")
	}
	if result.Reason != "not_applicable" {
		t.Fatalf("applySelfHostedCloudLoginGate() reason = %q, want not_applicable", result.Reason)
	}
}

func TestShouldRequireAgentMTLS(t *testing.T) {
	t.Setenv("TECHSTACK_AGENT_MTLS_OPTIONAL", "")
	tests := []struct {
		name     string
		cfg      *config.Config
		certFile string
		keyFile  string
		caFile   string
		want     bool
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
		{
			name: "development config",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "development"},
			},
			want: false,
		},
		{
			name: "production config without certs",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			want: false,
		},
		{
			name: "production config with full certs",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			certFile: "/certs/server.crt",
			keyFile:  "/certs/server.key",
			caFile:   "/certs/ca.crt",
			want:     true,
		},
		{
			name: "production config with partial certs",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			certFile: "/certs/server.crt",
			want:     true,
		},
		{
			name: "prod shorthand with full certs",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "prod"},
			},
			certFile: "/certs/server.crt",
			keyFile:  "/certs/server.key",
			caFile:   "/certs/ca.crt",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRequireAgentMTLS(tt.cfg, tt.certFile, tt.keyFile, tt.caFile); got != tt.want {
				t.Fatalf("shouldRequireAgentMTLS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldStartAgentGRPC(t *testing.T) {
	t.Setenv("TECHSTACK_AGENT_MTLS_OPTIONAL", "true")
	tests := []struct {
		name     string
		cfg      *config.Config
		certFile string
		keyFile  string
		caFile   string
		want     bool
	}{
		{
			name: "nil config starts",
			cfg:  nil,
			want: true,
		},
		{
			name: "development without certs starts",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "development"},
			},
			want: true,
		},
		{
			name: "production without certs disables grpc even with legacy opt-out env",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			want: false,
		},
		{
			name: "production with partial certs starts so constructor can fail closed",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			certFile: "/certs/server.crt",
			want:     true,
		},
		{
			name: "production with full certs starts",
			cfg: &config.Config{
				Server: config.ServerConfig{Environment: "production"},
			},
			certFile: "/certs/server.crt",
			keyFile:  "/certs/server.key",
			caFile:   "/certs/ca.crt",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStartAgentGRPC(tt.cfg, tt.certFile, tt.keyFile, tt.caFile); got != tt.want {
				t.Fatalf("shouldStartAgentGRPC() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentMTLSConfigOK_ProductionWithoutCertsFallsBackToDisabledGRPC(t *testing.T) {
	t.Setenv("TECHSTACK_AGENT_MTLS_OPTIONAL", "true")
	cfg := &config.Config{Server: config.ServerConfig{Environment: "production"}}

	err := agentMTLSConfigOK(cfg, "", "", "")
	if err != nil {
		t.Fatalf("agentMTLSConfigOK() error = %v, want nil so shouldStartAgentGRPC can disable the listener", err)
	}
}
