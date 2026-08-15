// kombifyTechstack - The Hybrid Infrastructure Unifier
// ==================================================
// This is the new PocketBase-as-Framework entry point.
// PocketBase provides: Auth, Collections API, Realtime SSE, Admin UI
// kombifyTechstack provides: Custom routes, OpenTofu, Simulation, Business Logic
package main

import (
	"context"
	stdlog "log"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/cloudlogin"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

// defaultProductVersion is the canonical compiled-in product version. It MUST
// stay declared in this file: the release version-sync tooling
// (scripts/release-version.mjs check/set, mise deploy:versioned) reads and
// rewrites this constant here by exact file path + regex. Keep the declaration
// on its own line below. Version parsing/normalization helpers live in
// version.go.
const defaultProductVersion = "0.7.95"

func hasAnyAgentMTLSConfig(certFile, keyFile, caFile string) bool {
	return certFile != "" || keyFile != "" || caFile != ""
}

func v2EnabledFromEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_V2_ENABLED")))
	if v == "" {
		return true
	}
	return v == "true" || v == "1" || v == "yes"
}

func v2FirstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func envBoolDefault(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func normalizeV2Issuer(raw string) string {
	return config.NormalizeCloudAuthIssuer(raw)
}

func inferV2ProviderKind(issuer string) providers.Kind {
	lower := strings.ToLower(issuer)
	switch {
	case strings.Contains(lower, "auth0"), lower == config.DefaultCloudAuthIssuer:
		return providers.KindAuth0
	case strings.Contains(lower, "pocketid"), strings.Contains(lower, "pocket-id"):
		return providers.KindPocketID
	default:
		return providers.KindGeneric
	}
}

func v2SessionCookieNameFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("TECHSTACK_V2_SESSION_COOKIE_NAME")); value != "" {
		return value
	}
	return "techstack_session"
}

func v2SessionAudienceFromEnv() string {
	if value := v2FirstEnv("TECHSTACK_V2_SESSION_AUDIENCE", "TECHSTACK_V2_AUTH_CLIENT_ID", "TECHSTACK_AUTH_CLOUD_CLIENT_ID", "AUTH0_CLIENT_ID"); value != "" {
		return value
	}
	return "techstack-local"
}

func v2AuthProviderFromEnv() (*providers.Provider, error) {
	issuer := normalizeV2Issuer(v2FirstEnv(
		"TECHSTACK_V2_AUTH_ISSUER",
		"TECHSTACK_AUTH_CLOUD_ISSUER",
		"AUTH0_ISSUER",
		"AUTH0_DOMAIN",
	))
	clientID := v2FirstEnv(
		"TECHSTACK_V2_AUTH_CLIENT_ID",
		"TECHSTACK_AUTH_CLOUD_CLIENT_ID",
		"AUTH0_CLIENT_ID",
	)
	if issuer == "" && clientID != "" {
		issuer = config.DefaultCloudAuthIssuer
	}
	if issuer == "" || clientID == "" {
		return nil, nil
	}
	kind := providers.Kind(strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_V2_AUTH_KIND"))))
	if kind == "" {
		kind = inferV2ProviderKind(issuer)
	}
	providerID := strings.TrimSpace(os.Getenv("TECHSTACK_V2_AUTH_PROVIDER_ID"))
	if providerID == "" {
		providerID = "primary"
	}
	return providers.New(providers.Config{
		ID:           providerID,
		Kind:         kind,
		Issuer:       issuer,
		Audience:     strings.TrimSpace(os.Getenv("TECHSTACK_V2_AUTH_AUDIENCE")),
		ClientID:     clientID,
		ClientSecret: v2FirstEnv("TECHSTACK_V2_AUTH_CLIENT_SECRET", "TECHSTACK_AUTH_CLOUD_CLIENT_SECRET", "AUTH0_CLIENT_SECRET"),
	})
}

func v2DefaultProviderID(registry *providers.Registry) string {
	if registry == nil || registry.Len() == 0 {
		return ""
	}
	if providerID := strings.TrimSpace(os.Getenv("TECHSTACK_V2_AUTH_PROVIDER_ID")); providerID != "" {
		return providerID
	}
	if registry.Len() == 1 {
		ids := registry.IDs()
		if len(ids) == 1 {
			return ids[0]
		}
	}
	return ""
}

func v2AuthRegistry(ctx context.Context, database *db.DB) (*providers.Registry, string, error) {
	if database != nil {
		registry, err := providers.LoadRegistryFromDB(ctx, database)
		if err != nil {
			return nil, "", err
		}
		if registry.Len() > 0 {
			return registry, "postgres", nil
		}
	}
	provider, err := v2AuthProviderFromEnv()
	if err != nil {
		return nil, "", err
	}
	if provider == nil {
		return nil, "", nil
	}
	registry := providers.NewRegistry()
	registry.Add(provider)
	return registry, "env", nil
}

func applySelfHostedCloudLoginGate(mode config.DeploymentMode, registry *providers.Registry) (*providers.Registry, cloudlogin.Result) {
	if registry == nil || registry.Len() == 0 || !mode.IsSelfHosted() {
		return registry, cloudlogin.Result{Enabled: true, Reason: "not_applicable"}
	}
	result := cloudlogin.Evaluate(cloudlogin.OptionsFromEnv(mode))
	if !result.Enabled {
		return nil, result
	}
	return registry, result
}

// agentMTLSConfigOK reports whether an agent gRPC startup configuration is valid for the
// current deployment mode. Returns nil when OK; otherwise an error suitable for exit.
func agentMTLSConfigOK(cfg *config.Config, certFile, keyFile, caFile string) error {
	if cfg == nil || !cfg.IsProduction() {
		return nil // dev/local: anything goes
	}
	if hasAnyAgentMTLSConfig(certFile, keyFile, caFile) {
		return nil // production with certs: mTLS will be required by shouldRequireAgentMTLS
	}
	return nil // production without certs disables agent gRPC instead of starting it insecurely
}

func shouldStartAgentGRPC(cfg *config.Config, certFile, keyFile, caFile string) bool {
	// Production without certs: refuse to start agent gRPC
	// (the caller should already have failed via agentMTLSConfigOK before this).
	if cfg != nil && cfg.IsProduction() && !hasAnyAgentMTLSConfig(certFile, keyFile, caFile) {
		return false
	}
	return true
}

func shouldRequireAgentMTLS(cfg *config.Config, certFile, keyFile, caFile string) bool {
	return cfg != nil && cfg.IsProduction() && hasAnyAgentMTLSConfig(certFile, keyFile, caFile)
}

func main() {
	if err := runTechstack(context.Background()); err != nil {
		stdlog.Fatal(err)
	}
}
