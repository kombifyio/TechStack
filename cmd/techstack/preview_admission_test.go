package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

// Preview is a full release channel: a signed-in person gets every action,
// while machine routes and anonymous work stay refused (CI-CD §4.6.2).
func TestPreviewServesSignedInPeopleAndKeepsMachineRoutesOnStable(t *testing.T) {
	t.Setenv(config.EnvReleaseChannel, "preview")
	for _, tc := range []struct {
		subject, method, path string
		stream                bool
		want                  int
	}{
		{"auth0|customer", http.MethodGet, "/api/v1/stacks", false, http.StatusOK},
		{"auth0|customer", http.MethodPost, "/api/v1/stacks", false, http.StatusOK},
		{"auth0|customer", http.MethodGet, "/api/v1/stacks", true, http.StatusOK},
		{"", http.MethodGet, "/api/v1/stacks", false, http.StatusUnauthorized},
		{"", http.MethodGet, "/api/v1/stacks", true, http.StatusUnauthorized},
		{"", http.MethodPost, "/api/v1/stacks", false, http.StatusConflict},
		{"auth0|customer", http.MethodPost, "/internal/v1/datasubject/requests", false, http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/_internal/jobs/reclaim", false, http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/api/v1/internal/runtime/summary", false, http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/api/v1/agent/commands", false, http.StatusForbidden},
		{"auth0|customer", http.MethodPost, "/webhook", false, http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/dashboard/reclaim", false, http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/api/v2/auth/login", false, http.StatusOK},
		{"auth0|customer", http.MethodGet, "/api/v2/auth/logout", false, http.StatusOK},
		{"", http.MethodGet, "/api/v1/auth/methods", false, http.StatusOK},
		{"", http.MethodPost, "/api/v1/auth/methods", false, http.StatusConflict},
	} {
		router := httpx.NewRouter()
		execution := config.NewPreviewExecution()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, previewExecution: execution, log: logger.Default()})
		reached := false
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			router.Route(method, tc.path, func(e *httpx.Event) error { reached = true; return e.NoContent(http.StatusOK) })
		}
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		if tc.subject != "" {
			req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: tc.subject}))
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
			t.Fatalf("%s %s %s (stream=%v): status=%d reached=%v", tc.subject, tc.method, tc.path, tc.stream, rec.Code, reached)
		}
	}
	for _, rawPath := range []string{"/api/v1%2Fhealth", "/api/v1%2fstacks"} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, log: logger.Default()})
		router.GET("/api/{rest...}", func(e *httpx.Event) error {
			t.Error("encoded slash reached a different catch-all handler")
			return e.NoContent(200)
		})
		req := httptest.NewRequest(http.MethodGet, rawPath, nil)
		req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "auth0|customer"}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("encoded path %s admitted: %d", rawPath, rec.Code)
		}
	}
	// ServeMux may canonicalize malformed paths before dispatch; none may
	// execute a mutation or an internal handler through an alternate spelling.
	for _, rawPath := range []string{"//api/v1/stacks", "/api/v1/../v1/stacks", "/%5finternal/jobs/reclaim"} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, log: logger.Default()})
		router.POST("/api/v1/stacks", func(e *httpx.Event) error { t.Error("canonicalized request reached mutation"); return e.NoContent(200) })
		router.POST("/_internal/jobs/reclaim", func(e *httpx.Event) error {
			t.Error("canonicalized request reached internal handler")
			return e.NoContent(200)
		})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, rawPath, nil))
		if rec.Code < 300 {
			t.Fatalf("alternate path %s admitted: %d", rawPath, rec.Code)
		}
	}
	booted := false
	stableBootComponent(func() bool { booted = true; return true })
	if booted {
		t.Fatal("preview ran a stable-only shared-work constructor")
	}
}

// The Nightly login page reads its sign-in methods and the Windows handoff
// returns to /stacks; both must reach Nightly without admitting new work.
// The app's static brand marks and logos load on Nightly for every visitor,
// but only as files from the frontend fallback: a dynamic handler registered
// at the same path, or a look-alike prefix, is still refused.
func TestPreviewServesStaticAssetsOnlyThroughFallback(t *testing.T) {
	t.Setenv(config.EnvReleaseChannel, "preview")
	for _, tc := range []struct {
		path    string
		dynamic bool
		want    bool
	}{
		{"/brand/techstack-rosette-128.webp", false, true},
		{"/brand-logos/pocket-id.svg", false, true},
		{"/preview-catalogs/use-cases.json", false, true},
		{"/kombify-navy.png", false, true},
		{"/brand/techstack-rosette-128.webp", true, false},
		{"/brandx/secret", false, false},
	} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, previewExecution: config.NewPreviewExecution(), log: logger.Default()})
		reached := false
		serve := func(e *httpx.Event) error { reached = true; return e.NoContent(http.StatusOK) }
		if tc.dynamic {
			router.GET(tc.path, serve)
		} else {
			router.SetNotFound(serve)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if reached != tc.want {
			t.Fatalf("%s (dynamic handler=%v): status=%d reached=%v", tc.path, tc.dynamic, rec.Code, reached)
		}
	}
}

func TestPreviewLoginReachesCloudSignIn(t *testing.T) {
	t.Setenv(config.EnvReleaseChannel, "preview")
	for _, dynamic := range []bool{false, true} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, previewExecution: config.NewPreviewExecution(), log: logger.Default()})
		reached := false
		serve := func(e *httpx.Event) error { reached = true; return e.NoContent(http.StatusOK) }
		if dynamic {
			router.GET("/stacks", serve)
		} else {
			router.SetNotFound(serve)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stacks", nil))
		if want := !dynamic; reached != want {
			t.Fatalf("/stacks shell (dynamic handler=%v): status=%d reached=%v", dynamic, rec.Code, reached)
		}
	}

	registry := providers.NewRegistry()
	primary, err := providers.New(providers.Config{ID: "primary", Kind: providers.KindAuth0, Issuer: "https://login.kombify.io/", ClientID: "client"})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(primary)
	rec := httptest.NewRecorder()
	nightlyAuthMethodsHandler(registry).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/methods", nil))
	var body struct {
		Providers []struct {
			ID, Kind, Label string
			AuthURL         string `json:"auth_url"`
		} `json:"providers"`
		BreakGlass struct {
			Initialized bool `json:"initialized"`
		} `json:"breakglass"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("methods status=%d err=%v", rec.Code, err)
	}
	if len(body.Providers) != 1 || body.Providers[0].ID != "primary" || body.Providers[0].Kind != "auth0" ||
		body.Providers[0].AuthURL != "/api/v2/auth/login?provider=primary" || body.BreakGlass.Initialized {
		t.Fatalf("Nightly methods must list only the cloud provider: %+v", body)
	}
}
