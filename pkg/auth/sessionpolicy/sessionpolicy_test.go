package sessionpolicy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
)

// platform-jx5m6: the 7-day fixed session forced a new login every week; the
// owner rule is one login per device with a 30-day idle window.
func TestBrowserSessionRenewsActiveAndNotIdleExpired(t *testing.T) {
	cfg := BrowserSessionConfig("techstack-test", []byte("0123456789abcdef0123456789abcdef"))
	mgr, err := authsession.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := NewBrowser(cfg, mgr, "techstack_session", true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := mgr.Issue(authsession.Claims{Subject: "auth0|user-1", TenantID: "tenant-1"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	authsession.SetSessionCookie(login, "techstack_session", token, true)
	browser.StampLogin(login.Header())
	loginCookies := login.Result().Cookies()

	renewAt := func(offset time.Duration) []*http.Cookie {
		browser.now = func() time.Time { return time.Now().Add(offset) }
		req := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
		for _, c := range loginCookies {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
		rec := httptest.NewRecorder()
		browser.Renew(rec, req)
		return rec.Result().Cookies()
	}

	if got := renewAt(time.Hour); len(got) != 0 {
		t.Fatalf("renewed within the renewal interval: %v", got)
	}
	renewed := renewAt(25 * time.Hour)
	if len(renewed) != 1 || renewed[0].Name != "techstack_session" {
		t.Fatalf("active session was not renewed: %v", renewed)
	}
	if got, want := renewed[0].MaxAge, int(BrowserSessionIdleLifetime/time.Second); got != want {
		t.Fatalf("renewed cookie Max-Age = %d, want %d", got, want)
	}
	if !renewed[0].HttpOnly || !renewed[0].Secure || renewed[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("renewed cookie lost its security flags: %+v", renewed[0])
	}
	claims, err := mgr.Verify(renewed[0].Value)
	if err != nil || claims.Subject != "auth0|user-1" || claims.TenantID != "tenant-1" {
		t.Fatalf("renewed token does not carry the session identity: %+v, %v", claims, err)
	}
	if got := renewAt(31 * 24 * time.Hour); len(got) != 0 {
		t.Fatalf("idle-expired session was renewed: %v", got)
	}
}
