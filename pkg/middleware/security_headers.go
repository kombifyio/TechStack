package middleware

import (
	"net/http"
	"strings"

	"github.com/kombifyio/go-common/httputil"

	"github.com/kombifyio/techstack/pkg/config"
)

// ApplySecurityHeaders keeps the default shared behavior from kombify-go-common.
func ApplySecurityHeaders(h http.Header) {
	httputil.ApplySecurityHeaders(h)
}

// ApplyRequestSecurityHeaders adds request-aware frame-ancestor handling for SaaS hosts.
func ApplyRequestSecurityHeaders(h http.Header, r *http.Request, mode config.DeploymentMode) {
	httputil.ApplySecurityHeaders(h)

	if origins := inferPortalOrigins(r, mode); len(origins) > 0 {
		mergeFrameAncestors(h, origins)
	}
}

// SecurityHeadersMiddleware preserves the previous default behavior for call sites
// that do not have request/deployment-mode context.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return httputil.SecurityHeadersMiddleware(next)
}

func inferPortalOrigins(r *http.Request, mode config.DeploymentMode) []string {
	if r == nil || !mode.IsSaaS() {
		return nil
	}

	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	if host == "" {
		return nil
	}

	return config.InferredSaaSFrameOrigins(host, mode)
}

func mergeFrameAncestors(h http.Header, origins []string) {
	csp := h.Get("Content-Security-Policy")
	directives := strings.Split(csp, ";")
	keptDirectives := make([]string, 0, len(directives))
	ancestors := make([]string, 0, len(origins)+1)

	addAncestor := func(raw string) {
		origin := sanitizeFrameAncestor(raw)
		if origin == "" {
			return
		}
		for _, existing := range ancestors {
			if existing == origin {
				return
			}
		}
		ancestors = append(ancestors, origin)
	}

	addAncestor("'self'")
	for _, directive := range directives {
		directive = strings.TrimSpace(directive)
		if directive == "" {
			continue
		}
		fields := strings.Fields(directive)
		if len(fields) > 0 && strings.EqualFold(fields[0], "frame-ancestors") {
			for _, ancestor := range fields[1:] {
				addAncestor(ancestor)
			}
			continue
		}
		keptDirectives = append(keptDirectives, directive)
	}
	for _, origin := range origins {
		addAncestor(origin)
	}

	nextDirectives := append(
		[]string{"frame-ancestors " + strings.Join(ancestors, " ")},
		keptDirectives...,
	)
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", strings.Join(nextDirectives, "; "))
}

func sanitizeFrameAncestor(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "'self'" {
		return raw
	}

	return config.SanitizeOrigin(raw)
}
