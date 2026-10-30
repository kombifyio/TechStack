package observability

// Config is shared by the Sentry build (observability.go) and the Full Local
// build (observability_full_local.go, tag kombify_full_local) so the two never
// drift; it imports nothing.
// Config configures Sentry initialization for a kombify SaaS Go service.
type Config struct {
	// Service is the Sentry project slug (e.g. "kombify-cloud-backend").
	// Required when Enabled=true.
	Service string

	// DSN is the Sentry DSN. Defaults to os.Getenv("SENTRY_DSN").
	// When empty, observability is disabled (safe for local dev).
	DSN string

	// Environment is the Sentry environment tag ("prod", "dev", "preview").
	// Defaults to os.Getenv("SENTRY_ENVIRONMENT") or "prod".
	Environment string

	// Release is the release identifier, typically the Git SHA.
	// Defaults to os.Getenv("SENTRY_RELEASE").
	Release string

	// TracesSampleRate overrides the default (0.1 in prod, 1.0 in dev).
	TracesSampleRate *float64

	// Debug enables Sentry SDK debug logging.
	Debug bool
}
