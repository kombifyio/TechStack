package routes

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// docsRedirectTarget is the canonical Tier-1 documentation host. The redirect
// used to live in the SvelteKit server layout (docs/+layout.server.ts); with
// the static-SPA convergence (ADR-033 OQ2) Go owns it so the /docs URLs keep
// working without a Node SSR process.
const docsRedirectTarget = "https://docs.kombify.io/techstack"

// RegisterDocsRedirectRoutes permanently redirects /docs and /docs/* to the
// canonical Mintlify documentation.
func RegisterDocsRedirectRoutes(r *httpx.Router) {
	redirect := func(e *httpx.Event) error {
		return e.Redirect(http.StatusPermanentRedirect, docsRedirectTarget)
	}
	r.GET("/docs", redirect)
	r.GET("/docs/{rest...}", redirect)
}
