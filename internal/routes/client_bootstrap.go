package routes

import (
	"net/http"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// clientBootstrapSentry is the browser Sentry section of the bootstrap payload.
type clientBootstrapSentry struct {
	DSN         string `json:"dsn"`
	Environment string `json:"environment"`
	Release     string `json:"release"`
}

// clientBootstrapPostHog is the browser PostHog section of the bootstrap payload.
type clientBootstrapPostHog struct {
	Key         string `json:"key"`
	Host        string `json:"host"`
	Environment string `json:"environment"`
}

// clientBootstrapTelemetry groups the telemetry toggles of the bootstrap payload.
type clientBootstrapTelemetry struct {
	Sentry  clientBootstrapSentry  `json:"sentry"`
	PostHog clientBootstrapPostHog `json:"posthog"`
}

// clientBootstrapResponse is the public runtime config served to the SPA.
type clientBootstrapResponse struct {
	Edition        string                   `json:"edition"`
	DeploymentMode string                   `json:"deployment_mode"`
	KombifyEdition string                   `json:"kombify_edition"`
	Version        string                   `json:"version"`
	PublicOrigin   string                   `json:"public_origin"`
	Telemetry      clientBootstrapTelemetry `json:"telemetry"`
}

// RegisterClientBootstrapRoutes adds GET /api/v1/client/bootstrap, the single
// runtime-config fetch of the static SPA (ADR-033 OQ2 web convergence).
//
// The SvelteKit frontend is built once as a static bundle and embedded into
// the Go binary, so runtime-varying public configuration can no longer be
// injected through `$env/dynamic/public` at Node startup. This endpoint is the
// replacement: it is unauthenticated, loopback- and public-safe, and returns
// only non-secret public config. Telemetry values are empty unless the
// operator or deployer sets them explicitly, which keeps selfhost-oss and
// local postures telemetry-free by default.
func RegisterClientBootstrapRoutes(r *httpx.Router, version string, edition config.Edition, mode config.DeploymentMode) {
	r.GET("/api/v1/client/bootstrap", func(e *httpx.Event) error {
		normalizedEdition, normalizedMode := normalizeRuntimeIdentity(edition, mode)
		e.Response.Header().Set("Cache-Control", "public, max-age=300")
		return httpx.Success(e, http.StatusOK, clientBootstrapResponse{
			Edition:        string(normalizedEdition),
			DeploymentMode: string(normalizedMode),
			KombifyEdition: firstNonEmptyEnv("PUBLIC_KOMBIFY_EDITION", "KOMBIFY_EDITION"),
			Version:        version,
			PublicOrigin:   config.PublicOriginFromEnv(),
			Telemetry: clientBootstrapTelemetry{
				Sentry: clientBootstrapSentry{
					DSN:         firstNonEmptyEnv("PUBLIC_SENTRY_DSN", "SENTRY_DSN_FRONTEND"),
					Environment: firstNonEmptyEnv("PUBLIC_SENTRY_ENVIRONMENT", "SENTRY_ENVIRONMENT"),
					Release:     firstNonEmptyEnv("PUBLIC_SENTRY_RELEASE", "SENTRY_RELEASE", "TECHSTACK_RELEASE", "RENDER_GIT_COMMIT", "GIT_COMMIT"),
				},
				PostHog: clientBootstrapPostHog{
					Key:         firstNonEmptyEnv("PUBLIC_POSTHOG_KEY"),
					Host:        firstNonEmptyEnv("PUBLIC_POSTHOG_HOST"),
					Environment: firstNonEmptyEnv("PUBLIC_POSTHOG_ENVIRONMENT", "PUBLIC_POSTHOG_ENV"),
				},
			},
		})
	})
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
