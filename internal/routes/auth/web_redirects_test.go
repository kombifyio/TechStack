package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

func webRedirectRouter() *httpx.Router {
	router := httpx.NewRouter()
	router.GET("/auth/callback", handleWebAuthCallbackRedirect())
	router.GET("/auth/logout", handleWebAuthLogoutRedirect("", config.ModeSelfHosted))
	return router
}

func TestCloudLogoutRedirectUsesOnlyTrustedPortalOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		host string
		want string
	}{
		{name: "production", host: "techstack.kombify.io", want: "https://kombify.io/auth/signout?global=1"},
		{name: "development", host: "techstack.kombify.dev", want: "https://kombify.dev/auth/signout?global=1"},
		{name: "unknown hosted proxy", host: "internal-render.example", want: "https://kombify.io/auth/signout?global=1"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router := httpx.NewRouter()
			router.GET("/auth/cloud-logout", handleCloudLogoutRedirect(config.ModeSaaS))
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "https://"+test.host+"/auth/cloud-logout?next=https://evil.example", nil)
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", recorder.Code)
			}
			if got := recorder.Header().Get("Location"); got != test.want {
				t.Fatalf("location = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCloudLogoutRedirectStaysLocalForSelfHosted(t *testing.T) {
	t.Parallel()
	router := httpx.NewRouter()
	router.GET("/auth/cloud-logout", handleCloudLogoutRedirect(config.ModeSelfHosted))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/cloud-logout", nil))
	if got := recorder.Header().Get("Location"); got != webLoggedOutPath {
		t.Fatalf("location = %q, want %q", got, webLoggedOutPath)
	}
}

func TestWebAuthCallbackRedirectsToV2PreservingQuery(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc-123&state=xyz", nil)
	webRedirectRouter().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "/api/v2/auth/callback?code=abc-123&state=xyz" {
		t.Fatalf("location = %q", got)
	}
}

func TestWebAuthCallbackRedirectWithoutQuery(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	webRedirectRouter().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth/callback", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "/api/v2/auth/callback" {
		t.Fatalf("location = %q", got)
	}
}

func TestWebAuthLogoutPrefersV2WhenSessionCookiePresent(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: defaultSessionCookieName, Value: "session-token", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	webRedirectRouter().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	want := "/api/v2/auth/logout?next=" + "%2Flogin%3Fmanual%3D1%26logged_out%3D1"
	if got := recorder.Header().Get("Location"); got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
}

func TestWebAuthLogoutFallsBackToV1WithoutSessionCookie(t *testing.T) {
	t.Parallel()
	for name, cookie := range map[string]*http.Cookie{
		"no cookie":    nil,
		"empty cookie": {Name: defaultSessionCookieName, Value: "   ", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		webRedirectRouter().ServeHTTP(recorder, req)

		if recorder.Code != http.StatusFound {
			t.Fatalf("%s: status = %d, want 302", name, recorder.Code)
		}
		if got := recorder.Header().Get("Location"); got != "/api/v1/auth/logout" {
			t.Fatalf("%s: location = %q, want /api/v1/auth/logout", name, got)
		}
	}
}

func TestWebAuthLogoutHonorsCustomCookieName(t *testing.T) {
	t.Parallel()
	router := httpx.NewRouter()
	router.GET("/auth/logout", handleWebAuthLogoutRedirect("custom_session", config.ModeSelfHosted))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "custom_session", Value: "token", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	router.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("Location"); got == "/api/v1/auth/logout" {
		t.Fatalf("custom cookie should route to v2 logout, got %q", got)
	}
}

func TestWebAuthLogoutRoutesSaaSThroughGlobalCloudBridge(t *testing.T) {
	t.Parallel()
	for name, cookie := range map[string]*http.Cookie{
		"v2 cookie": {Name: defaultSessionCookieName, Value: "session-token"},
		"no cookie": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			router := httpx.NewRouter()
			router.GET("/auth/logout", handleWebAuthLogoutRedirect("", config.ModeSaaS))
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/auth/logout", nil)
			if cookie != nil {
				req.AddCookie(cookie)
			}
			router.ServeHTTP(recorder, req)
			want := "/auth/cloud-logout"
			if cookie != nil {
				want = "/api/v2/auth/logout?next=%2Fauth%2Fcloud-logout"
			}
			if got := recorder.Header().Get("Location"); got != want {
				t.Fatalf("location = %q, want %q", got, want)
			}
		})
	}
}
