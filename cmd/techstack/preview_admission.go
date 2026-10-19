package main

import (
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"

	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

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
// (brand marks, product logos, the creation-wizard preview catalogs). They
// are files, not work; like every public shell path they are admitted only
// through the router's frontend fallback, never a dynamic handler.
func previewStaticAssetPath(p string) bool {
	switch p {
	case "/kombify-navy.png", "/robots.txt":
		return true
	}
	return strings.HasPrefix(p, "/brand/") || strings.HasPrefix(p, "/brand-logos/") || strings.HasPrefix(p, "/preview-catalogs/")
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

func admitPreviewTransport(e *httpx.Event) error {
	if config.ReleaseChannel() != "preview" {
		return nil
	}
	p := e.Request.URL.Path
	// ServeMux splits escaped segments before unescaping them. Decoded Path
	// alone could mistake /api/v1%2Fhealth for the admitted /api/v1/health
	// while dispatching it to a different catch-all handler.
	if e.Request.URL.RawPath != "" || e.Request.URL.EscapedPath() != p || path.Clean(p) != p || strings.Contains(p, "\\") || e.Request.Header.Get("Upgrade") != "" || strings.Contains(e.Request.Header.Get("Accept"), "text/event-stream") {
		return httpx.NewAPIError(http.StatusForbidden, "preview_transport_not_admitted", "Preview transport is not admitted.", nil)
	}
	if e.Request.Method != http.MethodGet && e.Request.Method != http.MethodHead {
		return httpx.NewAPIError(http.StatusConflict, "preview_mutation_not_admitted", "Preview mutations require qualified work ownership.", nil)
	}
	if previewPublicPath(p) {
		// Static shell paths are admitted only through the router's frontend
		// fallback, never a newly registered dynamic handler at the same path.
		if !strings.HasPrefix(p, "/api/") && e.Request.Pattern != "" && e.Request.Pattern != "/" {
			return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Preview dynamic route is not admitted.", nil)
		}
		return nil
	}
	// Audit each admitted read: GET/HEAD alone does not prove absence of work.
	switch p {
	case "/api/v1/stacks", "/api/v2/whoami":
		return nil
	}
	return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Preview route execution requires qualified ownership.", nil)
}

func previewTransportAdmission(e *httpx.Event) error {
	if err := admitPreviewTransport(e); err != nil {
		return err
	}
	return e.Next()
}

func admitPreviewPrincipal(e *httpx.Event, subject string) error {
	if config.ReleaseChannel() != "preview" || previewPublicPath(e.Request.URL.Path) {
		return nil
	}
	if !config.PreviewSubjectAdmitted(subject) {
		return httpx.NewAPIError(http.StatusForbidden, "preview_subject_not_admitted", "Preview requires the admitted shared test identity.", nil)
	}
	return nil
}

func previewHTTPAdmission(e *httpx.Event) error {
	if config.ReleaseChannel() != "preview" {
		return e.Next()
	}
	if err := admitPreviewTransport(e); err != nil {
		return err
	}
	if previewPublicPath(e.Request.URL.Path) {
		return e.Next()
	}
	id := identity.FromContext(e.Request.Context())
	if id == nil || !id.IsAuthenticated() {
		return httpx.NewUnauthorizedError("Preview requires the admitted shared test identity.", nil)
	}
	if err := admitPreviewPrincipal(e, id.UserID); err != nil {
		return err
	}
	return e.Next()
}

// stableBootComponent gates constructors with effects outside the managed read path.
func stableBootComponent[T any](boot func() T) (result T) {
	if config.BackgroundExecutionEnabled() {
		return boot()
	}
	return result
}
