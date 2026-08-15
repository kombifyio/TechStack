package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

func fetchClientBootstrap(t *testing.T) map[string]any {
	t.Helper()
	router := httpx.NewRouter()
	RegisterClientBootstrapRoutes(router, "9.9.9", config.EditionSelfHostOSS, config.ModeSelfHosted)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/client/bootstrap", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data envelope: %s", recorder.Body.String())
	}
	return envelope.Data
}

func telemetrySection(t *testing.T, data map[string]any, key string) map[string]any {
	t.Helper()
	telemetry, ok := data["telemetry"].(map[string]any)
	if !ok {
		t.Fatalf("telemetry section missing: %#v", data)
	}
	section, ok := telemetry[key].(map[string]any)
	if !ok {
		t.Fatalf("telemetry.%s section missing: %#v", key, telemetry)
	}
	return section
}

func TestClientBootstrapTelemetryFreeByDefault(t *testing.T) {
	for _, key := range []string{
		"PUBLIC_SENTRY_DSN", "SENTRY_DSN_FRONTEND", "PUBLIC_SENTRY_ENVIRONMENT",
		"SENTRY_ENVIRONMENT", "PUBLIC_SENTRY_RELEASE", "SENTRY_RELEASE",
		"TECHSTACK_RELEASE", "RENDER_GIT_COMMIT", "GIT_COMMIT",
		"PUBLIC_POSTHOG_KEY", "PUBLIC_POSTHOG_HOST", "PUBLIC_POSTHOG_ENVIRONMENT",
		"PUBLIC_POSTHOG_ENV", "PUBLIC_KOMBIFY_EDITION", "KOMBIFY_EDITION",
	} {
		t.Setenv(key, "")
	}

	data := fetchClientBootstrap(t)
	if got := data["edition"]; got != string(config.EditionSelfHostOSS) {
		t.Fatalf("edition = %v, want %s", got, config.EditionSelfHostOSS)
	}
	if got := data["deployment_mode"]; got != string(config.ModeSelfHosted) {
		t.Fatalf("deployment_mode = %v, want %s", got, config.ModeSelfHosted)
	}
	if got := data["version"]; got != "9.9.9" {
		t.Fatalf("version = %v, want 9.9.9", got)
	}
	if got := data["kombify_edition"]; got != "" {
		t.Fatalf("kombify_edition = %v, want empty", got)
	}

	sentry := telemetrySection(t, data, "sentry")
	for _, field := range []string{"dsn", "environment", "release"} {
		if got := sentry[field]; got != "" {
			t.Fatalf("sentry.%s = %v, want empty by default", field, got)
		}
	}
	posthog := telemetrySection(t, data, "posthog")
	for _, field := range []string{"key", "host", "environment"} {
		if got := posthog[field]; got != "" {
			t.Fatalf("posthog.%s = %v, want empty by default", field, got)
		}
	}
}

func TestClientBootstrapReflectsRuntimeEnv(t *testing.T) {
	t.Setenv("PUBLIC_SENTRY_DSN", "https://public@sentry.example/1")
	t.Setenv("PUBLIC_SENTRY_ENVIRONMENT", "prod")
	t.Setenv("PUBLIC_SENTRY_RELEASE", "abc123")
	t.Setenv("PUBLIC_POSTHOG_KEY", "phc_test")
	t.Setenv("PUBLIC_POSTHOG_HOST", "https://e.kombify.io")
	t.Setenv("PUBLIC_POSTHOG_ENVIRONMENT", "prod")
	t.Setenv("PUBLIC_KOMBIFY_EDITION", "saas-standalone")

	data := fetchClientBootstrap(t)
	sentry := telemetrySection(t, data, "sentry")
	if sentry["dsn"] != "https://public@sentry.example/1" || sentry["environment"] != "prod" || sentry["release"] != "abc123" {
		t.Fatalf("sentry section = %#v", sentry)
	}
	posthog := telemetrySection(t, data, "posthog")
	if posthog["key"] != "phc_test" || posthog["host"] != "https://e.kombify.io" || posthog["environment"] != "prod" {
		t.Fatalf("posthog section = %#v", posthog)
	}
	if data["kombify_edition"] != "saas-standalone" {
		t.Fatalf("kombify_edition = %v", data["kombify_edition"])
	}
}

func TestClientBootstrapFallsBackToBackendTelemetryEnv(t *testing.T) {
	t.Setenv("PUBLIC_SENTRY_DSN", "")
	t.Setenv("SENTRY_DSN_FRONTEND", "https://frontend@sentry.example/2")
	t.Setenv("PUBLIC_SENTRY_RELEASE", "")
	t.Setenv("SENTRY_RELEASE", "")
	t.Setenv("TECHSTACK_RELEASE", "")
	t.Setenv("RENDER_GIT_COMMIT", "deadbeefcafe")
	t.Setenv("GIT_COMMIT", "")

	sentry := telemetrySection(t, fetchClientBootstrap(t), "sentry")
	if sentry["dsn"] != "https://frontend@sentry.example/2" {
		t.Fatalf("sentry.dsn = %v, want SENTRY_DSN_FRONTEND fallback", sentry["dsn"])
	}
	if sentry["release"] != "deadbeefcafe" {
		t.Fatalf("sentry.release = %v, want RENDER_GIT_COMMIT fallback", sentry["release"])
	}
}
