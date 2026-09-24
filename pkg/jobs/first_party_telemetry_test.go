package jobs

import (
	"slices"
	"testing"
)

func TestApplyFirstPartyTelemetryPrependsCollectionOnHostedKits(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")
	t.Setenv("TECHSTACK_SAAS_BUILD_PROOF", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otel.example.invalid:4317")

	document := map[string]interface{}{
		"kit": map[string]interface{}{"slug": "cloud-kit"},
		"capabilities": map[string]interface{}{
			"enable": []interface{}{"public-edge"},
		},
	}
	applyFirstPartyTelemetryToStackSpec(document)

	enable := interfaceSlice(mapFromInterface(document["capabilities"])["enable"])
	if len(enable) < 2 || stringFromInterface(enable[0]) != firstPartyTelemetryCapability {
		t.Fatalf("enable = %#v, want telemetry-collection first", enable)
	}
	if stringFromInterface(enable[1]) != "public-edge" {
		t.Fatalf("enable = %#v, want existing capabilities after telemetry", enable)
	}
	observability := mapFromInterface(document["observability"])
	collector := mapFromInterface(observability["collector"])
	if stringFromInterface(collector["endpoint"]) != "https://otel.example.invalid:4317" {
		t.Fatalf("observability collector = %#v", collector)
	}
}

func TestApplyFirstPartyTelemetryDoesNotBlockApplyWithoutOTLP(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")
	t.Setenv("TECHSTACK_SAAS_BUILD_PROOF", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("TECHSTACK_STACKKIT_OTLP_ENDPOINT", "")
	t.Setenv("SENTRY_DSN", "https://public@example.invalid/1")

	document := map[string]interface{}{
		"kit":          map[string]interface{}{"slug": "cloud-kit"},
		"capabilities": map[string]interface{}{"enable": []interface{}{"public-edge"}},
	}
	applyFirstPartyTelemetryToStackSpec(document)
	enable := interfaceSlice(mapFromInterface(document["capabilities"])["enable"])
	if len(enable) != 1 || stringFromInterface(enable[0]) != "public-edge" {
		t.Fatalf("enable = %#v, want Apply to proceed without an OTLP collector", enable)
	}
	if _, exists := document["observability"]; exists {
		t.Fatalf("observability = %#v, want omitted so missing OTLP cannot fail Apply", document["observability"])
	}
	if !slices.Contains(firstPartyTelemetryEnvOverrides(), "SENTRY_DSN=https://public@example.invalid/1") {
		t.Fatal("Sentry DSN must still be injected when OTLP is absent")
	}
}

func TestApplyFirstPartyTelemetryLeavesOSSSpecsAlone(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "selfhost-oss")
	t.Setenv("TECHSTACK_SAAS_BUILD_PROOF", "")

	document := map[string]interface{}{
		"kit":          map[string]interface{}{"slug": "cloud-kit"},
		"capabilities": map[string]interface{}{"enable": []interface{}{}},
	}
	applyFirstPartyTelemetryToStackSpec(document)
	enable := interfaceSlice(mapFromInterface(document["capabilities"])["enable"])
	if len(enable) != 0 {
		t.Fatalf("OSS enable = %#v, want unchanged", enable)
	}
}

func TestFirstPartyTelemetryEnvCopiesPostHogAndSentry(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "saas-standalone")
	t.Setenv("SENTRY_DSN", "https://public@example.invalid/1")
	t.Setenv("PUBLIC_POSTHOG_KEY", "phc_test")
	t.Setenv("PUBLIC_POSTHOG_HOST", "https://eu.i.posthog.com")
	t.Setenv("POSTHOG_KEY", "")
	t.Setenv("POSTHOG_HOST", "")

	got := firstPartyTelemetryEnvOverrides()
	for _, want := range []string{
		"SENTRY_DSN=https://public@example.invalid/1",
		"POSTHOG_KEY=phc_test",
		"POSTHOG_HOST=https://eu.i.posthog.com",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("overrides missing %s from %v", want, got)
		}
	}
}
