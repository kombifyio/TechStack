package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
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
	IsFirstRun      bool    `json:"is_first_run"`      // true when the canonical local owner store is empty
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
func getAuthMode(app core.App, mode config.DeploymentMode, edition config.Edition, owners LocalOwnerLookup) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		var record *core.Record
		if !mode.IsSaaS() {
			var err error
			record, err = loadAuthConfig(app)
			if err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
					"Failed to read auth configuration", nil)
			}
		}

		ownerPresent := false
		if !mode.IsSaaS() {
			present, err := canonicalOwnerPresent(e.Request.Context(), owners)
			if err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
					"Failed to read local owner store", nil)
			}
			ownerPresent = present
		}

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

		if record == nil {
			// Canonical local owner store is first-run authority. PocketBase
			// auth_config is only a compatibility marker for mode fields.
			response.IsFirstRun = !mode.IsSaaS() && !ownerPresent

			// No config record found — self-hosted deployments can still opt into cloud auth via env.
			if !mode.IsSaaS() && envRequestsCloudAuth() {
				response.Mode = "cloud"
				response.AllowLocalLogin = false
				response.IsFirstRun = false
			}
			applySelfHostedCloudLoginGate(&response, mode)

			if response.Mode == "cloud" {
				if authURL := resolveCloudAuthorizationURL(e.Request); authURL != nil {
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

		// Build cloud auth URL if in cloud mode
		if response.Mode == "cloud" {
			if authURL := resolveCloudAuthorizationURL(e.Request); authURL != nil {
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

func resolveCloudAuthorizationURL(req *http.Request) *string {
	cloudIssuer := cloudIssuerFromEnv()
	cloudClientID := cloudClientIDFromEnv()
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

func resolveCloudLogoutURL(req *http.Request) *string {
	cloudIssuer := cloudIssuerFromEnv()
	cloudClientID := cloudClientIDFromEnv()
	if cloudIssuer == "" || cloudClientID == "" {
		return nil
	}

	logoutURL := buildOAuthLogoutURL(cloudIssuer, cloudClientID, req)
	return &logoutURL
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

func normalizeCloudIssuer(raw string) string {
	return config.NormalizeCloudAuthIssuer(raw)
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
		return publicOriginFromEnv()
	}

	scheme := requestScheme(req)
	host := requestHost(req)
	if host == "" {
		return publicOriginFromEnv()
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
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
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
