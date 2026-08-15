// Package authmw is a thin compatibility shim that re-exports the session
// middleware from the shared [authsession] package in kombify-go-common.
//
// Migration history: this package was the original donor of
// authsession.Middleware (lifted 2026-05-03 as part of the kombify auth
// standardization). It now re-exports the upstream surface so existing
// TechStack import paths (`pkg/v2/authmw`) keep working without
// behavioral change.
//
// New code SHOULD import `github.com/kombifyio/go-common/authsession`
// directly. This shim will be removed once all call sites have been
// migrated; track the cleanup in Beads.
package authmw

import (
	"github.com/kombifyio/go-common/authsession"
)

// DefaultSessionCookieName is the browser session cookie used by TechStack's
// HttpOnly auth surface.
const DefaultSessionCookieName = "techstack_session"

// Re-exported error sentinels.
var ErrMissingClaims = authsession.ErrMissingClaims

// Type aliases keep struct field access identical to the donor API.
type (
	Middleware = authsession.Middleware
	Option     = authsession.Option
)

// WithCookieName overrides the default browser session cookie name.
var WithCookieName = authsession.WithCookieName

// New returns a [Middleware] backed by the given session manager. We default
// the cookie name to the legacy [DefaultSessionCookieName] before applying
// caller options so wire compatibility is preserved.
func New(mgr *authsession.Manager, opts ...Option) *Middleware {
	all := append([]Option{authsession.WithCookieName(DefaultSessionCookieName)}, opts...)
	return authsession.NewMiddleware(mgr, all...)
}

// Context helpers — re-exported as values so callers that took function
// references (e.g. `var fn = authmw.ClaimsFrom`) continue to compile.
var (
	WithClaims = authsession.WithClaims
	ClaimsFrom = authsession.ClaimsFrom
	TenantFrom = authsession.TenantFrom
)
