package main

import (
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"

	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/releasechannel"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

// Preview admission (CI-CD-PLATFORM-STANDARD §4.6.2). The Preview deployment
// is a full release channel: every signed-in person it reaches gets every
// action, and the operator UI sends people who chose Stable back there. What
// stays on stable for every tenant are the machine routes (agents, webhooks,
// internal service calls) and the shared work that is nobody's homelab;
// homelab work follows its owner's channel (pkg/releasechannel).

func previewAuthPath(p string) bool {
	switch p {
	case "/api/v2/auth/login", "/api/v2/auth/logout", config.CanonicalAuthCallbackPath:
		return true
	}
	return false
}

func previewPublicPath(p string) bool {
	if previewAuthPath(p) {
		return true
	}
	switch p {
	case "/api/v1/health", "/api/v1/health/live", "/api/v1/health/ready", "/api/v1/health/startup", "/api/v1/info", "/api/v1/client/bootstrap", "/api/v1/auth/mode", "/api/v1/auth/methods", "/api/v1/csrf", "/api/v2/auth/providers":
		return true
	}
	// /stacks is the legacy sign-in return target (the Windows browser handoff
	// returns there); its shell only redirects to /dashboard.
	return p == "/" || p == "/login" || p == "/stacks" || p == "/dashboard" || strings.HasPrefix(p, "/dashboard/") || strings.HasPrefix(p, "/_app/") || strings.HasPrefix(p, "/assets/") || previewStaticAssetPath(p)
}

// previewStaticAssetPath names the app's own static files outside /_app/
// (brand marks, product logos, the creation-wizard preview catalogs, the
// RFC 9116 security contact). They are files, not work; like every public
// shell path they are admitted only through the router's frontend fallback,
// never a dynamic handler.
func previewStaticAssetPath(p string) bool {
	switch p {
	case "/kombify-navy.png", "/robots.txt", "/.well-known/security.txt":
		return true
	}
	return strings.HasPrefix(p, "/brand/") || strings.HasPrefix(p, "/preview-catalogs/")
}

// previewMachinePath names the service, agent and webhook routes. Guards and
// services talk to stable for every tenant; the work they carry is durable,
// so a job running on Preview still completes through them.
func previewMachinePath(p string) bool {
	for _, prefix := range []string{"/_internal/", "/internal/", "/api/internal/", "/api/v1/internal/", "/api/v1/agent/"} {
		if p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return strings.HasPrefix(p, "/webhook")
}

// nightlyAuthMethodsHandler answers the login page's sign-in methods read on
// Preview. Preview never boots local auth, whose bootstrap writes break-glass
// state, so this lists only the registered cloud providers: no break-glass and
// no database read. Without it the login page reports the backend unavailable
// and never starts the cloud sign-in.
func nightlyAuthMethodsHandler(registry *providers.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := commonauthlocal.MethodsResponse{Providers: []commonauthlocal.ProviderInfo{}}
		if registry != nil {
			ids := registry.IDs()
			sort.Strings(ids)
			for _, id := range ids {
				provider, err := registry.Get(id)
				if err != nil {
					continue
				}
				label := provider.ID()
				switch provider.Kind() {
				case providers.KindAuth0:
					label = "kombify Cloud"
				case providers.KindPocketID:
					label = "Pocket ID"
				}
				response.Providers = append(response.Providers, commonauthlocal.ProviderInfo{
					ID:      provider.ID(),
					Kind:    string(provider.Kind()),
					Label:   label,
					AuthURL: "/api/v2/auth/login?provider=" + provider.ID(),
				})
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(response)
	})
}

// hidePreviewFromCrawlers keeps the Preview deployment out of search engines
// (CI-CD-PLATFORM-STANDARD §4.6.2 "Preview is hidden from users"): every
// response carries a noindex header and /robots.txt disallows everything.
// Stable and Early Access, which serve the public links, are unchanged.
func hidePreviewFromCrawlers(e *httpx.Event) error {
	if config.ReleaseChannel() != "preview" {
		return e.Next()
	}
	e.Response.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if e.Request.URL.Path == "/robots.txt" && previewReadOnly(e.Request) {
		e.Response.Header().Set("Cache-Control", "no-store")
		return e.String(http.StatusOK, "User-agent: *\nDisallow: /\n")
	}
	return e.Next()
}

func previewReadOnly(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

func previewStream(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func admitPreviewTransport(e *httpx.Event) error {
	if !config.SecondaryChannel() {
		return nil
	}
	p := e.Request.URL.Path
	// ServeMux splits escaped segments before unescaping them. Decoded Path
	// alone could mistake /api/v1%2Fhealth for the admitted /api/v1/health
	// while dispatching it to a different catch-all handler.
	if e.Request.URL.RawPath != "" || e.Request.URL.EscapedPath() != p || path.Clean(p) != p || strings.Contains(p, "\\") {
		return httpx.NewAPIError(http.StatusForbidden, "preview_transport_not_admitted", "Preview transport is not admitted.", nil)
	}
	if previewMachinePath(p) && !stageWorkerTransport(e.Request) {
		return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Machine routes run on stable.", nil)
	}
	if !previewPublicPath(p) {
		// Signed-in work is admitted after identity (previewHTTPAdmission).
		return nil
	}
	if !previewReadOnly(e.Request) {
		return httpx.NewAPIError(http.StatusConflict, "preview_mutation_not_admitted", "Preview mutations require a signed-in person.", nil)
	}
	if previewStream(e.Request) {
		return httpx.NewAPIError(http.StatusForbidden, "preview_transport_not_admitted", "Preview transport is not admitted.", nil)
	}
	// Static shell paths are admitted only through the router's frontend
	// fallback, never a newly registered dynamic handler at the same path.
	if !strings.HasPrefix(p, "/api/") && e.Request.Pattern != "" && e.Request.Pattern != "/" {
		return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Preview dynamic route is not admitted.", nil)
	}
	return nil
}

func previewTransportAdmission(e *httpx.Event) error {
	if err := admitPreviewTransport(e); err != nil {
		return err
	}
	return e.Next()
}

func previewHTTPAdmission(e *httpx.Event) error {
	if !config.SecondaryChannel() {
		return e.Next()
	}
	if err := admitPreviewTransport(e); err != nil {
		return err
	}
	if stageWorkerTransport(e.Request) || previewPublicPath(e.Request.URL.Path) {
		return e.Next()
	}
	if id := identity.FromContext(e.Request.Context()); id == nil || !id.IsAuthenticated() {
		if !previewReadOnly(e.Request) {
			return httpx.NewAPIError(http.StatusConflict, "preview_mutation_not_admitted", "Preview mutations require a signed-in person.", nil)
		}
		// A person opening a page link (/servers, /settings, ...) signs in
		// first; API and asset reads keep the plain 401.
		if previewPageRequest(e.Request) {
			return e.Redirect(http.StatusFound, "/login")
		}
		return httpx.NewUnauthorizedError("Preview requires a signed-in person.", nil)
	}
	if !previewReadOnly(e.Request) {
		if err := releasechannel.RequireStageRequestPrincipal(e.Request.Context()); err != nil {
			return tenantguard.ExecutionRefusal(err)
		}
	}
	return e.Next()
}

// stageWorkerTransport defers only existing finite machine routes to their
// capability/worker authentication after an actual Stage owner was bound.
func stageWorkerTransport(request *http.Request) bool {
	return releasechannel.StageOwnerActive() && routes.WorkerMachineRoute(request.Method, request.URL.Path)
}

// previewPageRequest is a browser navigation to an app page: a read outside
// /api/ that asks for HTML.
func previewPageRequest(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || previewStream(r) {
		return false
	}
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// stableBootComponent gates constructors of shared work that is nobody's
// homelab (agent connections, monitoring, posture): stable runs it for every
// tenant.
func stableBootComponent[T any](boot func() T) (result T) {
	if config.RunsSharedWork() {
		return boot()
	}
	return result
}
