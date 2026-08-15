package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Root-path auth redirects (ADR-033 OQ2 web convergence).
//
// Before the static-SPA convergence the Node SSR layer owned /auth/callback
// and /auth/logout and 302-redirected them into the Go-owned /api/v2/auth/*
// handlers (app/src/routes/auth/{callback,logout}/+page.server.ts). With the
// Node process removed, Go serves the same semantics on the same paths. The
// legacy v1 OIDC flow keeps its canonical /api/v1/auth/{callback,logout}
// endpoints unchanged.

const (
	webLoggedOutPath         = "/login?manual=1&logged_out=1"
	defaultSessionCookieName = "techstack_session"
	productionCloudOrigin    = "https://kombify.io"
	cloudLogoutPath          = "/auth/signout?global=1"
)

// handleWebAuthCallbackRedirect redirects the browser-facing /auth/callback
// path into the canonical v2 callback handler, preserving the provider's
// query string (code, state, error, ...).
func handleWebAuthCallbackRedirect() func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		target := config.CanonicalAuthCallbackPath
		if rawQuery := e.Request.URL.RawQuery; rawQuery != "" {
			target += "?" + rawQuery
		}
		return e.Redirect(http.StatusFound, target)
	}
}

// handleWebAuthLogoutRedirect routes /auth/logout to the v2 logout when a v2
// browser session cookie is present, otherwise to the v1 logout. Mirrors the
// retired SvelteKit auth/logout server load.
func handleWebAuthLogoutRedirect(sessionCookieName string, mode config.DeploymentMode) func(e *httpx.Event) error {
	cookieName := strings.TrimSpace(sessionCookieName)
	if cookieName == "" {
		cookieName = defaultSessionCookieName
	}
	return func(e *httpx.Event) error {
		if mode.IsSaaS() {
			if hasNonEmptyCookie(e.Request, cookieName) {
				target := "/api/v2/auth/logout?next=" + url.QueryEscape("/auth/cloud-logout")
				return e.Redirect(http.StatusFound, target)
			}
			return e.Redirect(http.StatusFound, "/auth/cloud-logout")
		}
		if hasNonEmptyCookie(e.Request, cookieName) {
			target := "/api/v2/auth/logout?next=" + url.QueryEscape(webLoggedOutPath)
			return e.Redirect(http.StatusFound, target)
		}
		return e.Redirect(http.StatusFound, "/api/v1/auth/logout")
	}
}

// handleCloudLogoutRedirect hands SaaS logout to the authoritative kombify
// Cloud sign-out entry. That entry clears client state and the independent
// Cloud broker/session before completing the upstream IdP logout. The destination is derived only
// from the trusted deployment host; request query parameters are never used as
// redirect authority.
func handleCloudLogoutRedirect(mode config.DeploymentMode) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if !mode.IsSaaS() {
			return e.Redirect(http.StatusFound, webLoggedOutPath)
		}

		origin := productionCloudOrigin
		if origins := config.InferredSaaSFrameOrigins(e.Request.Host, mode); len(origins) > 0 {
			origin = origins[0]
		}
		return e.Redirect(http.StatusFound, origin+cloudLogoutPath)
	}
}

func hasNonEmptyCookie(r *http.Request, name string) bool {
	if r == nil {
		return false
	}
	cookie, err := r.Cookie(name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(cookie.Value) != ""
}
