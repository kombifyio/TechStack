package main

import (
	"net/http"
	"path"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
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
	case "/api/v1/health", "/api/v1/health/live", "/api/v1/health/ready", "/api/v1/health/startup", "/api/v1/info", "/api/v1/client/bootstrap", "/api/v1/auth/mode", "/api/v1/csrf", "/api/v2/auth/providers":
		return true
	}
	return p == "/" || p == "/login" || p == "/dashboard" || strings.HasPrefix(p, "/dashboard/") || strings.HasPrefix(p, "/_app/") || strings.HasPrefix(p, "/assets/")
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
		return httpx.NewAPIError(http.StatusForbidden, "preview_transport_not_admitted", "Nightly transport is not admitted.", nil)
	}
	if e.Request.Method != http.MethodGet && e.Request.Method != http.MethodHead {
		return httpx.NewAPIError(http.StatusConflict, "preview_mutation_not_admitted", "Nightly mutations require qualified work ownership.", nil)
	}
	if previewPublicPath(p) {
		// Static shell paths are admitted only through the router's frontend
		// fallback, never a newly registered dynamic handler at the same path.
		if !strings.HasPrefix(p, "/api/") && e.Request.Pattern != "" && e.Request.Pattern != "/" {
			return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Nightly dynamic route is not admitted.", nil)
		}
		return nil
	}
	// Audit each admitted read: GET/HEAD alone does not prove absence of work.
	switch p {
	case "/api/v1/stacks", "/api/v2/whoami":
		return nil
	}
	return httpx.NewAPIError(http.StatusForbidden, "preview_route_not_admitted", "Nightly route execution requires qualified ownership.", nil)
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
		return httpx.NewAPIError(http.StatusForbidden, "preview_subject_not_admitted", "Nightly requires the admitted shared test identity.", nil)
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
		return httpx.NewUnauthorizedError("Nightly requires the admitted shared test identity.", nil)
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
