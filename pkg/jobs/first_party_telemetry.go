package jobs

import (
	"os"
	"strings"
)

const firstPartyTelemetryCapability = "telemetry-collection"

func firstPartyHostedKitTelemetry() bool {
	edition := strings.ToLower(strings.TrimSpace(os.Getenv("KOMBIFY_EDITION")))
	return strings.HasPrefix(edition, "saas") || strings.TrimSpace(os.Getenv("TECHSTACK_SAAS_BUILD_PROOF")) != ""
}

func firstPartyTelemetryKit(document map[string]interface{}) bool {
	kit := strings.TrimSpace(stringFromInterface(mapFromInterface(document["kit"])["slug"]))
	if kit == "" {
		kit = strings.TrimSpace(stringFromInterface(document["stackkit"]))
	}
	return kit == "cloud-kit" || kit == "basement-kit"
}

// applyFirstPartyTelemetryToStackSpec is first-party, best-effort telemetry.
// OTLP, Sentry, and PostHog are never Apply requirements: a missing exporter
// or DSN must not block kit rollout. Customer and OSS kits stay unchanged.
//
// When an OTLP endpoint is already in custody, enable telemetry-collection
// first and attach the collector so OpenTelemetry can run (Sentry can dock
// to that pipeline). The capability is omitted without an endpoint because
// its plan contract requires a collector — enabling it empty would fail
// Apply. Sentry/PostHog still travel through process environment only.
func applyFirstPartyTelemetryToStackSpec(document map[string]interface{}) {
	if document == nil || !firstPartyHostedKitTelemetry() || !firstPartyTelemetryKit(document) {
		return
	}
	endpoint := firstPartyOTLPEndpoint()
	if endpoint == "" {
		return
	}
	caps := mapFromInterface(document["capabilities"])
	caps["enable"] = prependCapability(caps["enable"], firstPartyTelemetryCapability)
	document["capabilities"] = caps
	insecure := strings.HasPrefix(strings.ToLower(endpoint), "http://")
	document["observability"] = map[string]interface{}{
		"profile": "single-node",
		"signals": map[string]interface{}{
			"metrics": true,
			"logs":    false,
			"traces":  false,
		},
		"collector": map[string]interface{}{
			"enabled":  true,
			"endpoint": endpoint,
			"protocol": firstPartyOTLPProtocol(endpoint),
			"tls":      map[string]interface{}{"insecure": insecure},
		},
	}
}

func prependCapability(raw interface{}, capability string) []interface{} {
	next := []interface{}{capability}
	for _, item := range interfaceSlice(raw) {
		value := strings.TrimSpace(stringFromInterface(item))
		if value == "" || value == capability {
			continue
		}
		next = append(next, value)
	}
	return next
}

func firstPartyOTLPEndpoint() string {
	return firstNonEmpty(
		strings.TrimSpace(os.Getenv("TECHSTACK_STACKKIT_OTLP_ENDPOINT")),
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
	)
}

func firstPartyOTLPProtocol(endpoint string) string {
	lower := strings.ToLower(endpoint)
	if strings.Contains(lower, "/v1/traces") || strings.Contains(lower, "http/protobuf") {
		return "http/protobuf"
	}
	return "grpc"
}

func firstPartyStackKitProcessEnv() []string {
	env := append([]string{}, os.Environ()...)
	for _, item := range firstPartyTelemetryEnvOverrides() {
		env = upsertEnv(env, item)
	}
	return env
}

func firstPartyTelemetryEnvOverrides() []string {
	if !firstPartyHostedKitTelemetry() {
		return nil
	}
	overrides := make([]string, 0, 8)
	copyEnv := func(name string) {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			overrides = append(overrides, name+"="+value)
		}
	}
	copyEnv("SENTRY_DSN")
	copyEnv("SENTRY_ENVIRONMENT")
	copyEnv("SENTRY_RELEASE")
	copyEnv("OTEL_EXPORTER_OTLP_ENDPOINT")
	copyEnv("OTEL_EXPORTER_OTLP_HEADERS")
	copyEnv("OTEL_EXPORTER_OTLP_PROTOCOL")
	copyEnv("PUBLIC_POSTHOG_KEY")
	copyEnv("PUBLIC_POSTHOG_HOST")
	if strings.TrimSpace(os.Getenv("POSTHOG_KEY")) == "" {
		if value := strings.TrimSpace(os.Getenv("PUBLIC_POSTHOG_KEY")); value != "" {
			overrides = append(overrides, "POSTHOG_KEY="+value)
		}
	} else {
		copyEnv("POSTHOG_KEY")
	}
	if strings.TrimSpace(os.Getenv("POSTHOG_HOST")) == "" {
		if value := strings.TrimSpace(os.Getenv("PUBLIC_POSTHOG_HOST")); value != "" {
			overrides = append(overrides, "POSTHOG_HOST="+value)
		}
	} else {
		copyEnv("POSTHOG_HOST")
	}
	return overrides
}

func upsertEnv(env []string, assignment string) []string {
	key, _, ok := strings.Cut(assignment, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return env
	}
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = assignment
			return env
		}
	}
	return append(env, assignment)
}
