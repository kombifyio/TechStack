package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/middleware"
)

func cookieByName(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestPortalSessionCookiesAreCrossSiteOnlyForCloudPreviewPortal(t *testing.T) {
	ps := PortalSession{CookieName: "techstack_session", Secure: true}

	t.Run("cloud preview portal embeds cross-site and receives SameSite=None session and CSRF cookies", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := setPortalSessionCookies(rec, ps, "session-token", "https://kombify-cloud-native-pr-434.onrender.com"); err != nil {
			t.Fatalf("setPortalSessionCookies: %v", err)
		}
		cookies := rec.Result().Cookies()
		session := cookieByName(t, cookies, "techstack_session")
		if session == nil || session.Value != "session-token" {
			t.Fatalf("session cookie missing: %+v", cookies)
		}
		if session.SameSite != http.SameSiteNoneMode || !session.Secure || !session.HttpOnly {
			t.Fatalf("session cookie must be SameSite=None; Secure; HttpOnly for the preview portal, got %s", session.String())
		}
		csrf := cookieByName(t, cookies, middleware.CSRFCookieName)
		if csrf == nil || csrf.Value == "" {
			t.Fatalf("csrf cookie missing: %+v", cookies)
		}
		if csrf.SameSite != http.SameSiteNoneMode || !csrf.Secure || csrf.HttpOnly {
			t.Fatalf("csrf cookie must be SameSite=None; Secure and readable by the app, got %s", csrf.String())
		}
		if rec.Header().Get(middleware.CSRFHeaderName) != csrf.Value {
			t.Fatalf("csrf header must carry the issued token")
		}
	})

	for _, origin := range []string{"", "https://kombify.io", "https://evil.onrender.com", "https://kombify-cloud-native-pr-434.onrender.com.attacker.test"} {
		t.Run("origin "+origin+" keeps the same-site session cookie", func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := setPortalSessionCookies(rec, ps, "session-token", origin); err != nil {
				t.Fatalf("setPortalSessionCookies: %v", err)
			}
			cookies := rec.Result().Cookies()
			session := cookieByName(t, cookies, "techstack_session")
			if session == nil || session.SameSite != http.SameSiteLaxMode || !session.Secure {
				t.Fatalf("expected SameSite=Lax secure session cookie, got %+v", cookies)
			}
			if cookieByName(t, cookies, middleware.CSRFCookieName) != nil {
				t.Fatalf("no cross-site csrf cookie outside the preview portal: %+v", cookies)
			}
		})
	}

	t.Run("insecure deployments never issue SameSite=None", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := setPortalSessionCookies(rec, PortalSession{CookieName: "techstack_session"}, "session-token", "https://kombify-cloud-native-pr-434.onrender.com"); err != nil {
			t.Fatalf("setPortalSessionCookies: %v", err)
		}
		session := cookieByName(t, rec.Result().Cookies(), "techstack_session")
		if session == nil || session.SameSite != http.SameSiteLaxMode || session.Secure {
			t.Fatalf("expected the plain Lax cookie, got %+v", session)
		}
	})
}
