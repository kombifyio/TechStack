package config

import (
	"net"
	"net/url"
	"os"
	"strings"
)

const AllowedFrameOriginsEnv = "ALLOWED_FRAME_ORIGINS"

var productionPortalFrameOrigins = []string{
	"https://kombify.io",
	"https://app.kombify.io",
}

var devPortalFrameOrigins = []string{
	"https://kombify.dev",
	"https://app.kombify.dev",
}

var publicOriginEnvKeys = []string{
	"TECHSTACK_PUBLIC_ORIGIN",
	"PUBLIC_ORIGIN",
	"APP_PUBLIC_ORIGIN",
	"APP_URL",
}

// SplitOriginList accepts comma, whitespace, or mixed separators.
func SplitOriginList(raw string) []string {
	fields := strings.Fields(strings.ReplaceAll(raw, ",", " "))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// SanitizeOrigin validates and normalizes a browser origin.
func SanitizeOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func sanitizedOriginList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if origin := SanitizeOrigin(value); origin != "" && !containsString(out, origin) {
			out = append(out, origin)
		}
	}
	return out
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// AllowedFrameOriginsFromEnv is the global iframe and cross-site allowlist.
func AllowedFrameOriginsFromEnv() []string {
	return sanitizedOriginList(SplitOriginList(os.Getenv(AllowedFrameOriginsEnv)))
}

// PublicOriginFromEnv returns the canonical browser origin for hosted
// deployments. It is used by auth flows that must not leak internal Render or
// loopback hosts into OAuth redirect_uri values.
func PublicOriginFromEnv() string {
	for _, key := range publicOriginEnvKeys {
		if origin := SanitizeOrigin(os.Getenv(key)); origin != "" {
			return origin
		}
	}
	return ""
}

// DefaultSaaSFrameOrigins returns the canonical portal origins TechStack can be
// embedded from in SaaS mode. ALLOWED_FRAME_ORIGINS can add more without code
// changes.
func DefaultSaaSFrameOrigins() []string {
	return sanitizedOriginList(append(
		append([]string{}, productionPortalFrameOrigins...),
		devPortalFrameOrigins...,
	))
}

// InferredSaaSFrameOrigins narrows the canonical portal origin by request host.
func InferredSaaSFrameOrigins(host string, mode DeploymentMode) []string {
	if !mode.IsSaaS() {
		return nil
	}
	host = normalizeHost(host)
	switch {
	case strings.HasSuffix(host, ".kombify.dev"):
		return sanitizedOriginList(devPortalFrameOrigins)
	case strings.HasSuffix(host, ".kombify.io"):
		return sanitizedOriginList(productionPortalFrameOrigins)
	default:
		return nil
	}
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.Trim(host, "[]")
}

func appendUniqueSanitizedOrigins(base []string, additions ...[]string) []string {
	out := append([]string{}, base...)
	for _, values := range additions {
		for _, value := range values {
			origin := SanitizeOrigin(value)
			if origin == "" || containsString(out, origin) {
				continue
			}
			out = append(out, origin)
		}
	}
	return out
}
