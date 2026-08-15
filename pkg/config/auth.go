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

	legacyKombifyAuth0Host = "kombify.eu.auth0.com"
)

// NormalizeCloudAuthIssuer returns a stable issuer URL for cloud login config.
// The old Auth0 tenant host is intentionally folded into the custom domain so
// stale env vars do not send browsers back through the non-canonical tenant URL.
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
		if strings.EqualFold(parsed.Host, legacyKombifyAuth0Host) {
			return DefaultCloudAuthIssuer
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return strings.TrimSuffix(parsed.String(), "/")
	}

	return strings.TrimSuffix(value, "/")
}
