package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// The v2 login binds its sealed OIDC state to the browser that started it.
// go-common authflow (archived, not changed here) seals the state without a
// browser binding, so its callback would accept an authorization response
// that another browser started and sign the victim into the attacker's
// account (login CSRF). Each login sets a cookie whose name and value derive
// from its own state, so parallel logins in several tabs stay independent.
const (
	v2LoginStateCookiePrefix = "techstack_login_"
	v2LoginStateCookieMaxAge = 10 * time.Minute
	v2LoginBrowserMismatch   = "browser_mismatch"
)

func v2LoginStateDigest(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func v2LoginStateCookieName(state string) string {
	return v2LoginStateCookiePrefix + v2LoginStateDigest(state)[:16]
}

func v2LoginStateCookie(state, callbackPath string, secure bool, maxAge int) *http.Cookie {
	value := ""
	if maxAge > 0 {
		value = v2LoginStateDigest(state)
	}
	return &http.Cookie{
		Name:     v2LoginStateCookieName(state),
		Value:    value,
		Path:     callbackPath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		// Lax still accompanies the identity provider's top-level GET
		// redirect back to the callback.
		SameSite: http.SameSiteLaxMode,
	}
}

// v2AuthBindLoginState sets the binding cookie for the state the wrapped login
// handler put into its authorization redirect.
func v2AuthBindLoginState(next http.Handler, callbackPath string, secure bool) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		if state := authorizationRedirectState(recorder); state != "" {
			http.SetCookie(w, v2LoginStateCookie(state, callbackPath, secure, int(v2LoginStateCookieMaxAge/time.Second)))
		}
		copyRecordedHTTPResponse(w, recorder)
	})
}

// v2AuthRequireLoginStateBinding rejects an authorization response whose
// state was not issued to this browser before any code exchange happens.
// Identity-provider error responses mint no session and pass through.
func v2AuthRequireLoginStateBinding(next http.Handler, callbackPath, loginErrorPath string, secure bool) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		state := strings.TrimSpace(query.Get("state"))
		if state == "" {
			next.ServeHTTP(w, r)
			return
		}
		bound := v2LoginStateBound(r, state)
		http.SetCookie(w, v2LoginStateCookie(state, callbackPath, secure, -1))
		if !bound && strings.TrimSpace(query.Get("error")) == "" {
			http.Redirect(w, r, loginErrorPath+"?error="+v2LoginBrowserMismatch, http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func v2LoginStateBound(r *http.Request, state string) bool {
	cookie, err := r.Cookie(v2LoginStateCookieName(state))
	if err != nil || cookie.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(v2LoginStateDigest(state))) == 1
}

func authorizationRedirectState(recorder *httptest.ResponseRecorder) string {
	if recorder.Code < http.StatusMultipleChoices || recorder.Code >= http.StatusBadRequest {
		return ""
	}
	target, err := url.Parse(strings.TrimSpace(recorder.Header().Get("Location")))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(target.Query().Get("state"))
}
