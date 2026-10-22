// Package observability wires kombify Go services into Sentry for error
// reporting and performance tracing.
//
// This is the SINGLE SOURCE OF TRUTH for how internal kombify Go services
// initialize Sentry — defaults (sample rates, PII scrubbing, release tagging,
// user-context extraction from edgeauth headers) are enforced here so every
// service gets identical, DSGVO-safe behavior.
//
// Scope: SaaS services only (Ebene 2 per PRODUCT-SEGMENTATION.md).
// OSS tools (Ebene 1 — Sim, SpeechKit, StackKits, TechStack-OSS) MUST NOT
// import this package. See ../platform/standards/OBSERVABILITY-STANDARD.md.
//
// Usage:
//
//	func main() {
//	    shutdown := observability.MustInit(observability.Config{
//	        Service:     "kombify-cloud-backend",
//	        Environment: os.Getenv("SENTRY_ENVIRONMENT"),
//	        Release:     os.Getenv("SENTRY_RELEASE"),
//	    })
//	    defer shutdown()
//
//	    mux := http.NewServeMux()
//	    // ... register handlers ...
//	    http.ListenAndServe(":8080", observability.Middleware(mux))
//	}
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	sentry "github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
)

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

// MustInit initializes Sentry with kombify defaults. Safe to call even when
// SENTRY_DSN is unset — it becomes a no-op and returns a no-op shutdown.
//
// Returns a shutdown function that should be deferred in main().
func MustInit(cfg Config) func() {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = os.Getenv("SENTRY_DSN")
	}
	if dsn == "" {
		// No-op mode. Services in local dev / OSS-style deployment.
		return func() {}
	}

	env := cfg.Environment
	if env == "" {
		env = os.Getenv("SENTRY_ENVIRONMENT")
	}
	if env == "" {
		env = "prod"
	}

	release := cfg.Release
	if release == "" {
		release = os.Getenv("SENTRY_RELEASE")
	}

	traces := defaultTracesRate(env)
	if cfg.TracesSampleRate != nil {
		traces = *cfg.TracesSampleRate
	}

	options := sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          release,
		ServerName:       cfg.Service,
		AttachStacktrace: true,
		EnableTracing:    traces > 0,
		TracesSampleRate: traces,
		Debug:            cfg.Debug,
		BeforeSend:       scrubPII,
	}
	options.BeforeSendTransaction = scrubPII
	err := sentry.Init(options)
	if err != nil {
		// Do not fatal — observability failure must not take down the service.
		fmt.Fprintf(os.Stderr, "observability: sentry init failed: %v\n", err)
		return func() {}
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("service", cfg.Service)
	})

	return func() {
		sentry.Flush(5 * time.Second)
	}
}

// Middleware wraps an http.Handler with:
//   - Panic recovery (reported to Sentry, then re-raised as 500)
//   - Request-scoped Sentry hub
//   - User context from edgeauth identity headers (via identity.FromContext)
//
// Use AFTER edgeauth.Middleware so identity is already in context.
func Middleware(next http.Handler) http.Handler {
	// sentryhttp handles panic recovery + hub-per-request automatically.
	sentryHandler := sentryhttp.New(sentryhttp.Options{
		Repanic:         true,
		WaitForDelivery: false,
	})

	return sentryHandler.Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
			hub.Scope().SetRequest(r)
			if id := identity.FromContext(r.Context()); id.IsAuthenticated() {
				hub.Scope().SetUser(sentry.User{
					ID:       id.UserID,
					Username: id.UserID,
				})
				if id.OrgID != "" {
					hub.Scope().SetTag("org_id", id.OrgID)
				}
				if id.Tier != "" {
					hub.Scope().SetTag("tier", id.Tier)
				}
			}
		}
		next.ServeHTTP(w, r)
	}))
}

// CaptureErr reports err to Sentry attached to the request hub when present,
// otherwise to the global hub. Returns the original error unchanged so it can
// be used in `return observability.CaptureErr(ctx, err)` chains.
func CaptureErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	hub.CaptureException(err)
	return err
}

// defaultTracesRate follows OBSERVABILITY-STANDARD.md §9.
//
// Profiles are configured per-transaction in sentry-go (not a ClientOptions
// rate) — see sentry.StartTransaction.WithProfileSampleRate. We leave that to
// call-sites that want it.
func defaultTracesRate(env string) float64 {
	if env == "prod" {
		return 0.1
	}
	return 1.0
}

// scrubPII is the error and transaction hook enforcing the PII rules from
// OBSERVABILITY-STANDARD.md §6. It strips request bodies, auth headers and
// cookies from outgoing events before they leave the process.
func scrubPII(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	secrets := []string{event.User.Email, event.User.IPAddress}
	collectRequestSecrets(event.Request, &secrets)
	// Collect from every container before redacting any copied value. Normalize
	// once so typed maps and slices share the same recursive policy.
	for index, breadcrumb := range event.Breadcrumbs {
		if breadcrumb != nil {
			// Scope breadcrumbs are shared between errors and transactions. Keep
			// their original evidence available to the next event's collection.
			owned := *breadcrumb
			owned.Data = prepareMetadata(breadcrumb.Data, &secrets)
			event.Breadcrumbs[index] = &owned
			collectURLSecrets(owned.Message, &secrets)
		}
	}
	for _, span := range event.Spans {
		if span != nil {
			span.Data = prepareMetadata(span.Data, &secrets)
			collectURLSecrets(span.Description, &secrets)
		}
	}
	for key, data := range event.Contexts {
		event.Contexts[key] = sentry.Context(prepareMetadata(data, &secrets))
	}
	if event.Request != nil {
		event.Request.Data = "" // body
		event.Request.Cookies = ""
		event.Request.QueryString = ""
		event.Request.Env = nil
		for k := range event.Request.Headers {
			if !safeRequestHeader(k) {
				delete(event.Request.Headers, k)
			}
		}
		event.Request.URL = scrubURL(event.Request.URL)
	}
	event.User.Email = ""
	event.User.IPAddress = ""
	for _, breadcrumb := range event.Breadcrumbs {
		if breadcrumb != nil {
			breadcrumb.Message = scrubText(breadcrumb.Message, secrets)
			scrubMetadataValue(breadcrumb.Data, secrets)
		}
	}
	for _, span := range event.Spans {
		if span != nil {
			span.Description = scrubText(span.Description, secrets)
			scrubMetadataValue(span.Data, secrets)
		}
	}
	// Root-span data is projected into the trace context, not event.Spans.
	for _, data := range event.Contexts {
		scrubMetadataValue(map[string]interface{}(data), secrets)
	}
	return event
}

func collectRequestSecrets(request *sentry.Request, secrets *[]string) {
	if request == nil {
		return
	}
	collectURLSecrets(request.URL, secrets)
	collectQuerySecrets(request.QueryString, secrets)
	*secrets = append(*secrets, request.Data, request.Cookies)
	var body interface{}
	if json.Unmarshal([]byte(request.Data), &body) == nil {
		collectMetadataStrings(body, secrets)
	}
	for _, value := range request.Env {
		*secrets = append(*secrets, value)
	}
	for header, value := range request.Headers {
		if !safeRequestHeader(header) {
			*secrets = append(*secrets, value)
		}
	}
}

func safeRequestHeader(key string) bool {
	switch strings.ToLower(key) {
	case "content-type", "accept", "x-request-id":
		return true
	default:
		return false
	}
}

func scrubURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// Normalize arbitrary JSON-shaped SDK metadata before walking it so nested
// typed maps/slices cannot bypass the same policy. Unserializable data is dropped.
func prepareMetadata(data map[string]interface{}, secrets *[]string) map[string]interface{} {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var normalized map[string]interface{}
	if json.Unmarshal(encoded, &normalized) != nil {
		return nil
	}
	// The SDK may already have removed request credentials. Also collect copies
	// under sensitive metadata keys before removing them, then redact aliases.
	collectSensitiveMetadata(normalized, secrets)
	return normalized
}

func scrubMetadataValue(value interface{}, secrets []string) interface{} {
	switch data := value.(type) {
	case string:
		return scrubText(data, secrets)
	case map[string]interface{}:
		scrubMetadataMap(data, secrets, false)
	case []interface{}:
		for i, value := range data {
			data[i] = scrubMetadataValue(value, secrets)
		}
	}
	return value
}

func metadataHeaders(key string) bool {
	return strings.HasSuffix(strings.ToLower(key), "headers")
}

func scrubMetadataMap(data map[string]interface{}, secrets []string, headers bool) {
	for key, value := range data {
		if sensitiveMetadataKey(key) || (headers && !safeRequestHeader(key)) {
			delete(data, key)
			continue
		}
		if metadataHeaders(key) {
			if nested, ok := value.(map[string]interface{}); ok {
				scrubMetadataMap(nested, secrets, true)
			} else {
				delete(data, key)
			}
			continue
		}
		data[key] = scrubMetadataValue(value, secrets)
	}
}

func sensitiveMetadataKey(key string) bool {
	name := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
	for _, sensitive := range []string{"authorization", "cookie", "password", "secret", "token", "signature", "query", "body", "email", "ipaddress", "requestdata", "env", "xkombify", "xuser", "xorg"} {
		if strings.Contains(name, sensitive) {
			return true
		}
	}
	return false
}

func collectSensitiveMetadata(value interface{}, secrets *[]string) {
	switch data := value.(type) {
	case map[string]interface{}:
		for key, value := range data {
			if sensitiveMetadataKey(key) {
				collectMetadataStrings(value, secrets)
			}
			if metadataHeaders(key) {
				collectHeaderSecrets(value, secrets)
			}
			collectSensitiveMetadata(value, secrets)
		}
	case []interface{}:
		for _, value := range data {
			collectSensitiveMetadata(value, secrets)
		}
	case string:
		collectURLSecrets(data, secrets)
	}
}

func collectHeaderSecrets(value interface{}, secrets *[]string) {
	if headers, ok := value.(map[string]interface{}); ok {
		for key, value := range headers {
			if !safeRequestHeader(key) {
				collectMetadataStrings(value, secrets)
			}
		}
	} else {
		collectMetadataStrings(value, secrets)
	}
}

func collectQuerySecrets(query string, secrets *[]string) {
	*secrets = append(*secrets, query)
	values, _ := url.ParseQuery(query)
	for _, items := range values {
		*secrets = append(*secrets, items...)
	}
}

func collectURLSecrets(value string, secrets *[]string) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		*secrets = append(*secrets, parsed.User.Username(), password)
	}
	collectQuerySecrets(parsed.RawQuery, secrets)
	*secrets = append(*secrets, parsed.Fragment)
}

func scrubText(value string, secrets []string) string {
	value = scrubURL(value)
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[scrubbed]")
		}
	}
	return value
}

func collectMetadataStrings(value interface{}, values *[]string) {
	switch data := value.(type) {
	case string:
		*values = append(*values, data)
	case map[string]interface{}:
		for _, value := range data {
			collectMetadataStrings(value, values)
		}
	case []interface{}:
		for _, value := range data {
			collectMetadataStrings(value, values)
		}
	}
}

// ErrObservabilityDisabled is returned by utilities that require Sentry but
// the SDK is in no-op mode. Rarely needed — most code should check with
// IsEnabled() before branching.
var ErrObservabilityDisabled = errors.New("observability: disabled (no SENTRY_DSN)")

// IsEnabled reports whether Sentry was initialized successfully.
func IsEnabled() bool {
	return sentry.CurrentHub().Client().IsEnabled()
}
