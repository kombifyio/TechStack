package config

import (
	"net/url"
	"strings"
)

const (
	// DefaultCloudAuthIssuer is the canonical Auth0 custom domain used by the
	// kombify Cloud login surface. Product callback URLs still point back to the
	// TechStack app origin, not to this issuer domain.
	DefaultCloudAuthIssuer = "https://login.kombify.io"

	// CanonicalAuthCallbackPath is the only callback path the v2 browser auth
	// flow should generate for hosted kombify Cloud login.
	CanonicalAuthCallbackPath = "/api/v2/auth/callback"

)

// NormalizeCloudAuthIssuer returns a stable issuer URL. Kombify-hosted
// deployments pin the canonical value through their environment contract;
// standalone deployments may use their own OIDC issuer.
func NormalizeCloudAuthIssuer(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return strings.TrimSuffix(parsed.String(), "/")
	}
	return strings.TrimSuffix(value, "/")
}
