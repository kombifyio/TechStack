package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/cloudlogin"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

const v2CloudLoginPath = "/api/v2/auth/login"

// AuthModeResponse represents the authentication mode configuration.
type AuthModeResponse struct {
	Mode            string  `json:"mode"`              // "local" or "cloud"
	Edition         string  `json:"edition"`           // "selfhost-oss", "preview", "saas-standalone", or "saas-embedded"
	DeploymentMode  string  `json:"deployment_mode"`   // "self-hosted" or "saas"
	IsFirstRun      bool    `json:"is_first_run"`      // true when no auth_config record exists
	CloudAuthURL    *string `json:"cloud_auth_url"`    // OAuth2 authorization URL for cloud mode
	PortalURL       *string `json:"portal_url"`        // Portal URL for cloud mode
	AllowLocalLogin bool    `json:"allow_local_login"` // Whether local login is allowed in cloud mode
}

// getAuthMode returns the current authentication mode and configuration.
// GET /api/v1/auth/mode
//
// Response:
//
//	{
//	  "data": {
//	    "mode": "local" | "cloud",
//	    "cloud_auth_url": "https://..." | null,
//	    "portal_url": "https://..." | null,
//	    "allow_local_login": true
//	  }
//	}
func getAuthMode(app core.App, mode config.DeploymentMode, edition config.Edition) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		// Try to find the auth_config record (singleton pattern)
		record, err := app.FindFirstRecordByFilter(
			"auth_config",
			"id != ''", // Match any record (singleton)
			nil,
		)

		// Default response for when no config exists
		response := AuthModeResponse{
			Mode:            "local",
			Edition:         string(authResponseEdition(edition, mode)),
			DeploymentMode:  string(mode),
			IsFirstRun:      false,
			CloudAuthURL:    nil,
			PortalURL:       nil,
			AllowLocalLogin: true,
		}
		applyPlatformAuthPolicy(&response, mode)

		if err != nil || record == nil {
			// No config record = first run (unless SaaS, which is pre-configured)
			response.IsFirstRun = !mode.IsSaaS()

			// No config record found — self-hosted deployments can still opt into cloud auth via env.
			if !mode.IsSaaS() && envRequestsCloudAuth() {
				response.Mode = "cloud"
				response.AllowLocalLogin = false
				response.IsFirstRun = false
			}
			applySelfHostedCloudLoginGate(&response, mode)

			if response.Mode == "cloud" {
				if authURL := resolveCloudAuthorizationURL(nil, e.Request); authURL != nil {
					response.CloudAuthURL = authURL
				}
			}
			return httpx.Success(e, http.StatusOK, response)
		}

		// Read values from the record
		recordMode := record.GetString("mode")
		if recordMode == "" {
			recordMode = "local"
		}
		response.Mode = recordMode
		response.AllowLocalLogin = record.GetBool("allow_local_login")
		applyPlatformAuthPolicy(&response, mode)
		applySelfHostedCloudLoginGate(&response, mode)

		// Set portal URL if available
		if portalURL := record.GetString("portal_url"); portalURL != "" {
			response.PortalURL = &portalURL
		}

		// Build cloud auth URL if in cloud mode
		if response.Mode == "cloud" {
			if authURL := resolveCloudAuthorizationURL(record, e.Request); authURL != nil {
				response.CloudAuthURL = authURL
			}
		}

		return httpx.Success(e, http.StatusOK, response)
	}
}

func authResponseEdition(edition config.Edition, mode config.DeploymentMode) config.Edition {
	if edition.IsValid() {
		return edition
	}
	if mode.IsSaaS() {
		return config.EditionSaaSStandalone
	}
	return config.EditionSelfHostOSS
}

func applySelfHostedCloudLoginGate(response *AuthModeResponse, deployMode config.DeploymentMode) {
	if response == nil || !deployMode.IsSelfHosted() || response.Mode != "cloud" {
		return
	}
	result := cloudlogin.Evaluate(cloudlogin.OptionsFromEnv(deployMode))
	if result.Enabled {
		return
	}
	response.Mode = "local"
	response.CloudAuthURL = nil
	response.PortalURL = nil
	response.AllowLocalLogin = true
}

func applyPlatformAuthPolicy(response *AuthModeResponse, deployMode config.DeploymentMode) {
	if response == nil {
		return
	}

	if deployMode.IsSaaS() {
		response.Mode = "cloud"
		response.AllowLocalLogin = false
		response.IsFirstRun = false
	}
}

func envRequestsCloudAuth() bool {
	switch strings.TrimSpace(os.Getenv("TECHSTACK_AUTH_MODE")) {
	case "saas", "cloud":
		return true
	default:
		return false
	}
}

func resolveCloudAuthorizationURL(record *core.Record, req *http.Request) *string {
	cloudIssuer := resolveCloudIssuer(record)
	cloudClientID := resolveCloudClientID(record)
	if cloudIssuer == "" || cloudClientID == "" {
		return nil
	}

	// The shared v2 authflow is the canonical browser entrypoint for kombify Cloud
	// sign-in. Keep issuer/client resolution as the enablement gate, but route the
	// browser through the local handler so TechStack owns provider selection,
	// state, callback handling, and session minting consistently.
	authURL := buildCloudLoginURL(req)
	return &authURL
}

func buildCloudLoginURL(req *http.Request) string {
	return fmt.Sprintf("%s%s", redirectOrigin(req), v2CloudLoginPath)
}

func resolveCloudLogoutURL(record *core.Record, req *http.Request) *string {
	cloudIssuer := resolveCloudIssuer(record)
	cloudClientID := resolveCloudClientID(record)
	if cloudIssuer == "" || cloudClientID == "" {
		return nil
	}

	logoutURL := buildOAuthLogoutURL(cloudIssuer, cloudClientID, req)
	return &logoutURL
}

func resolveCloudIssuer(record *core.Record) string {
	if issuer := normalizeCloudIssuer(firstRecordField(record, "cloud_issuer", "auth0_issuer")); issuer != "" {
		return issuer
	}
	return cloudIssuerFromEnv()
}

func resolveCloudClientID(record *core.Record) string {
	if clientID := strings.TrimSpace(firstRecordField(record, "cloud_client_id", "auth0_client_id")); clientID != "" {
		return clientID
	}
	return cloudClientIDFromEnv()
}

func cloudIssuerFromEnv() string {
	if issuer := normalizeCloudIssuer(
		firstEnv(
			"TECHSTACK_AUTH_CLOUD_ISSUER",
			"AUTH0_ISSUER",
			"AUTH0_DOMAIN",
		),
	); issuer != "" {
		return issuer
	}
	return config.DefaultCloudAuthIssuer
}

func cloudClientIDFromEnv() string {
	return firstEnv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "AUTH0_CLIENT_ID")
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func firstRecordField(record *core.Record, keys ...string) string {
	if record == nil {
		return ""
	}

	for _, key := range keys {
		if value := strings.TrimSpace(record.GetString(key)); value != "" {
			return value
		}
	}

	return ""
}

func normalizeCloudIssuer(raw string) string {
	return config.NormalizeCloudAuthIssuer(raw)
}

// buildOAuthAuthorizationURL constructs the OAuth2 authorization URL for cloud auth.
func buildOAuthAuthorizationURL(issuer, clientID string, req *http.Request) string {
	// Build the authorization endpoint URL
	authEndpoint := fmt.Sprintf("%s/authorize", strings.TrimSuffix(issuer, "/"))
	redirectURI := fmt.Sprintf("%s/api/v1/auth/callback", redirectOrigin(req))

	// Build the full authorization URL with query parameters
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", "openid profile email")

	return fmt.Sprintf("%s?%s", authEndpoint, params.Encode())
}

func buildOAuthLogoutURL(issuer, clientID string, req *http.Request) string {
	logoutEndpoint := fmt.Sprintf("%s/oidc/logout", strings.TrimSuffix(issuer, "/"))
	postLogoutRedirectURI := fmt.Sprintf("%s/login?logged_out=1", redirectOrigin(req))

	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("post_logout_redirect_uri", postLogoutRedirectURI)

	return fmt.Sprintf("%s?%s", logoutEndpoint, params.Encode())
}

func redirectOrigin(req *http.Request) string {
	if req == nil {
		if origin := publicOriginFromEnv(); origin != "" {
			return origin
		}
		return "http://localhost"
	}

	scheme := requestScheme(req)
	host := requestHost(req)
	if host == "" {
		if origin := publicOriginFromEnv(); origin != "" {
			return origin
		}
		return scheme + "://localhost"
	}

	if isLoopbackHost(host) {
		if origin := publicOriginFromEnv(); origin != "" {
			return origin
		}
	}

	return fmt.Sprintf("%s://%s", scheme, host)
}

func requestScheme(req *http.Request) string {
	if req == nil {
		return "https"
	}

	if proto := firstHeaderValue(req.Header.Get("X-Forwarded-Proto")); proto != "" {
		return proto
	}
	if req.TLS != nil {
		return "https"
	}
	return "http"
}

func requestHost(req *http.Request) string {
	if req == nil {
		return ""
	}

	if host := firstHeaderValue(req.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	if req.Host != "" {
		return req.Host
	}
	return req.URL.Host
}

func firstHeaderValue(value string) string {
	parts := strings.Split(value, ",")
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func publicOriginFromEnv() string {
	return config.PublicOriginFromEnv()
}

func isLoopbackHost(host string) bool {
	candidate := strings.TrimSpace(host)
	if candidate == "" {
		return false
	}

	if strings.HasPrefix(candidate, "[") {
		if parsedHost, _, err := net.SplitHostPort(candidate); err == nil {
			candidate = parsedHost
		}
	} else if parsedHost, _, err := net.SplitHostPort(candidate); err == nil {
		candidate = parsedHost
	} else if idx := strings.Index(candidate, ":"); idx >= 0 && !strings.Contains(candidate, ".") {
		candidate = candidate[:idx]
	}

	candidate = strings.Trim(candidate, "[]")
	if strings.EqualFold(candidate, "localhost") {
		return true
	}

	ip := net.ParseIP(candidate)
	return ip != nil && ip.IsLoopback()
}
