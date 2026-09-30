package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A v2 login callback signs in only the browser that started that login.
func TestV2LoginCallbackCompletesOnlyInTheInitiatingBrowser(t *testing.T) {
	const callbackPath = "/api/v2/auth/callback"
	issued := 0
	login := v2AuthBindLoginState(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issued++
		state := "sealed-state-" + string(rune('a'+issued))
		http.Redirect(w, r, "https://login.kombify.io/authorize?client_id=ts&state="+url.QueryEscape(state), http.StatusFound)
	}), callbackPath, true)
	callback := v2AuthRequireLoginStateBinding(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "techstack_session", Value: "session-for-" + r.URL.Query().Get("code"), Path: "/"})
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}), callbackPath, "/login", true)

	startLogin := func() (string, []*http.Cookie) {
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/auth/login", nil))
		location, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return location.Query().Get("state"), rec.Result().Cookies()
	}
	finish := func(state string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, callbackPath+"?code=attacker-code&state="+url.QueryEscape(state), nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		callback.ServeHTTP(rec, req)
		return rec
	}
	sessionMinted := func(rec *httptest.ResponseRecorder) bool {
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == "techstack_session" && cookie.Value != "" {
				return true
			}
		}
		return false
	}

	attackerState, _ := startLogin()
	victimState, victimCookies := startLogin()

	// The attacker's authorization response replayed into the victim's browser.
	crossBrowser := finish(attackerState, victimCookies)
	if sessionMinted(crossBrowser) || crossBrowser.Header().Get("Location") != "/login?error=browser_mismatch" {
		t.Fatalf("callback with a state from another browser must fail without a session, got %d %q",
			crossBrowser.Code, crossBrowser.Header().Get("Location"))
	}

	// The initiating browser still completes its own login (parallel logins stay valid).
	own := finish(victimState, victimCookies)
	if !sessionMinted(own) || own.Header().Get("Location") != "/dashboard" {
		t.Fatalf("initiating browser must complete, got %d %q", own.Code, own.Header().Get("Location"))
	}
}
