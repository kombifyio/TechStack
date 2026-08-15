package main

import (
	"net/url"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/instance"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

const (
	clientDiscoveryModeEnv      = "TECHSTACK_CLIENT_DEPLOYMENT_MODE"
	clientDiscoveryBaseURLEnv   = "TECHSTACK_CLIENT_BASE_URL"
	clientDiscoveryIssuerEnv    = "TECHSTACK_CLIENT_OIDC_ISSUER"
	clientDiscoveryClientIDEnv  = "TECHSTACK_CLIENT_OIDC_CLIENT_ID"
	clientDiscoveryAudienceEnv  = "TECHSTACK_CLIENT_OIDC_AUDIENCE"
	clientDiscoveryScopesEnv    = "TECHSTACK_CLIENT_OIDC_SCOPES"
	clientDiscoveryFlowEnv      = "TECHSTACK_CLIENT_OIDC_FLOW"
	clientTLSFingerprintEnv     = "TECHSTACK_CLIENT_TLS_FINGERPRINT_SHA256"
	clientDiscoveryModeLocal    = "local"
	clientDiscoveryModeSelfHost = "self_hosted"
)

// techstackClientConnectionProfileConfig projects boot-time product identity
// into public client metadata. Hosted/SaaS editions intentionally return an
// unsupported mode here: their cloud profile belongs to the Cloud/Gateway
// origin and is implemented by a separate slice.
func techstackClientConnectionProfileConfig(cfg *config.Config, identity instance.Identity) routes.ClientConnectionProfileConfig {
	baseURL := strings.TrimSpace(os.Getenv(clientDiscoveryBaseURLEnv))
	if baseURL == "" {
		baseURL = clientDiscoveryPublicOrigin()
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(clientDiscoveryModeEnv)))
	if cfg == nil || !cfg.DeploymentMode.IsSelfHosted() {
		mode = ""
	} else if mode == "" {
		mode = clientDiscoveryModeLocal
		if baseURL != "" {
			mode = clientDiscoveryModeSelfHost
		}
	}

	scopes := clientDiscoveryScopes(os.Getenv(clientDiscoveryScopesEnv))
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile"}
	}
	return routes.ClientConnectionProfileConfig{
		DeploymentMode: mode,
		BaseURL:        baseURL,
		InstanceID:     strings.TrimSpace(identity.ID),
		OIDCIssuer:     strings.TrimSpace(os.Getenv(clientDiscoveryIssuerEnv)),
		OIDCClientID:   strings.TrimSpace(os.Getenv(clientDiscoveryClientIDEnv)),
		OIDCAudience:   strings.TrimSpace(os.Getenv(clientDiscoveryAudienceEnv)),
		OIDCScopes:     scopes,
		OIDCFlow:       strings.TrimSpace(os.Getenv(clientDiscoveryFlowEnv)),
	}
}

func techstackClientPairingRouteConfig(profile routes.ClientConnectionProfileConfig, boot *v2Boot) routes.ClientPairingRouteConfig {
	config := routes.ClientPairingRouteConfig{
		Profile:              profile,
		TLSFingerprintSHA256: strings.TrimSpace(os.Getenv(clientTLSFingerprintEnv)),
	}
	if boot == nil {
		return config
	}
	config.DefaultTenantID = strings.TrimSpace(boot.defaultTenant)
	if boot.db != nil && boot.db.DB != nil && boot.session != nil && boot.authStore != nil && clientPairingOIDCAuthorityMatches(profile, boot.registry) {
		config.Store = clientPairingStore(boot)
	}
	return config
}

func clientPairingOIDCAuthorityMatches(profile routes.ClientConnectionProfileConfig, registry *providers.Registry) bool {
	if registry == nil || registry.Len() == 0 {
		return false
	}
	want := normalizedClientIssuer(profile.OIDCIssuer)
	if want == "" {
		return false
	}
	for _, id := range registry.IDs() {
		provider, err := registry.Get(id)
		if err == nil && normalizedClientIssuer(provider.Issuer()) == want {
			return true
		}
	}
	return false
}

func normalizedClientIssuer(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Scheme != httpsScheme || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String()
}

func clientDiscoveryPublicOrigin() string {
	for _, key := range []string{"TECHSTACK_PUBLIC_ORIGIN", "PUBLIC_ORIGIN", "APP_PUBLIC_ORIGIN", "APP_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			if origin := config.SanitizeOrigin(value); origin != "" {
				return origin
			}
			// Preserve invalid configured values so the route fails closed instead
			// of silently treating a broken remote configuration as local.
			return value
		}
	}
	return ""
}

func clientDiscoveryScopes(raw string) []string {
	fields := strings.Fields(strings.ReplaceAll(raw, ",", " "))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			out = append(out, value)
		}
	}
	return out
}
