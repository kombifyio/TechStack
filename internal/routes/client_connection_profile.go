package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	commonprofile "github.com/kombifyio/go-common/nativeclient/profile"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// Wire constants alias the shared consumer contract in
// kombify-go-common/nativeclient/profile so the producer cannot drift from
// what native clients parse.
const (
	clientConnectionProfilePath    = commonprofile.WellKnownPath
	clientConnectionProfileVersion = "1"
	clientDeploymentModeLocal      = commonprofile.ModeLocal
	clientDeploymentModeSelfHosted = commonprofile.ModeSelfHosted
	clientFlowLocalBootstrap       = commonprofile.FlowLocalBootstrap
	clientFlowPKCE                 = commonprofile.FlowAuthorizationCodePKCE
	clientFlowDeviceAuthorization  = commonprofile.FlowDeviceAuthorization
)

var (
	clientInstanceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)
	clientScopePattern      = regexp.MustCompile(`^[A-Za-z0-9:._/-]+$`)
)

// ClientConnectionProfileConfig contains only public, operator-provided
// metadata. Local bootstrap metadata and all advertised product capabilities
// are owned by this route and cannot be expanded through environment input.
type ClientConnectionProfileConfig struct {
	DeploymentMode string
	BaseURL        string
	InstanceID     string
	OIDCIssuer     string
	OIDCClientID   string
	OIDCAudience   string
	OIDCScopes     []string
	OIDCFlow       string
}

// The wire structs ARE the shared consumer types: producing and parsing the
// profile share one declaration, so a field change on either side is a
// compile-time event, not silent drift.
type (
	clientConnectionProfile = commonprofile.Profile
	clientProfileOIDC       = commonprofile.OIDC
	clientProfileSync       = commonprofile.Sync
)

// RegisterClientConnectionProfileRoute publishes the strict Core v1 profile.
// Invalid or incomplete runtime configuration is evaluated at request time and
// fails closed with a guided 503 response rather than a partial profile.
func RegisterClientConnectionProfileRoute(r *httpx.Router, cfg ClientConnectionProfileConfig) {
	if r == nil {
		return
	}
	r.GET(clientConnectionProfilePath, func(e *httpx.Event) error {
		profile, err := buildClientConnectionProfile(cfg, e.Request)
		if err != nil {
			return writeClientDiscoveryUnavailable(e.Response)
		}
		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.Header().Set("Cache-Control", "public, max-age=300")
		e.Response.WriteHeader(http.StatusOK)
		return json.NewEncoder(e.Response).Encode(profile)
	})
}

func buildClientConnectionProfile(cfg ClientConnectionProfileConfig, request *http.Request) (clientConnectionProfile, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.DeploymentMode))
	if mode != clientDeploymentModeLocal && mode != clientDeploymentModeSelfHosted {
		return clientConnectionProfile{}, fmt.Errorf("unsupported client deployment mode %q", mode)
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	if !clientInstanceIDPattern.MatchString(instanceID) {
		return clientConnectionProfile{}, errors.New("invalid client instance id")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" && mode == clientDeploymentModeLocal {
		baseURL = localClientRequestOrigin(request)
	}
	baseURL, err := validateClientEndpoint(baseURL, mode, true)
	if err != nil {
		return clientConnectionProfile{}, fmt.Errorf("invalid client base url: %w", err)
	}

	oidc := clientProfileOIDC{
		Issuer:   strings.TrimSpace(cfg.OIDCIssuer),
		ClientID: strings.TrimSpace(cfg.OIDCClientID),
		Audience: strings.TrimSpace(cfg.OIDCAudience),
		Scopes:   normalizeClientScopes(cfg.OIDCScopes),
		Flow:     strings.TrimSpace(cfg.OIDCFlow),
	}
	if mode == clientDeploymentModeLocal {
		oidc = clientProfileOIDC{
			Issuer:   baseURL,
			ClientID: "techstack-local",
			Scopes:   []string{"openid", "profile"},
			Flow:     clientFlowLocalBootstrap,
		}
	}
	if err := validateClientOIDC(mode, oidc); err != nil {
		return clientConnectionProfile{}, err
	}

	return clientConnectionProfile{
		Version:        clientConnectionProfileVersion,
		DeploymentMode: mode,
		BaseURL:        baseURL,
		InstanceID:     instanceID,
		OIDC:           oidc,
		Capabilities: []string{
			"techstack.runtime.read",
			"techstack.runtime.write",
		},
		APIVersions: map[string]string{"techstack": "v1"},
		Sync: clientProfileSync{
			OfflineRead:  false,
			OfflineWrite: "disabled",
			ETag:         false,
			Tombstones:   false,
		},
	}, nil
}

func validateClientOIDC(mode string, oidc clientProfileOIDC) error {
	if len(oidc.ClientID) == 0 || len(oidc.ClientID) > 256 {
		return errors.New("public native OIDC client id required")
	}
	if len(oidc.Audience) > 512 {
		return errors.New("public native OIDC audience invalid")
	}
	if len(oidc.Scopes) == 0 {
		return errors.New("public native OIDC scopes required")
	}
	seen := make(map[string]bool, len(oidc.Scopes))
	for _, scope := range oidc.Scopes {
		if !clientScopePattern.MatchString(scope) || seen[scope] {
			return errors.New("public native OIDC scopes invalid")
		}
		seen[scope] = true
	}
	switch mode {
	case clientDeploymentModeLocal:
		if oidc.Flow != clientFlowLocalBootstrap {
			return errors.New("local client discovery requires local bootstrap")
		}
	case clientDeploymentModeSelfHosted:
		if oidc.Flow != clientFlowPKCE && oidc.Flow != clientFlowDeviceAuthorization {
			return errors.New("self-hosted client discovery requires PKCE or device authorization")
		}
	}
	_, err := validateClientEndpoint(oidc.Issuer, mode, false)
	return err
}

func validateClientEndpoint(raw, mode string, originOnly bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		return "", errors.New("absolute endpoint required")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("userinfo, query, and fragment are forbidden")
	}
	if originOnly && parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("base url must be an origin")
	}
	if parsed.Scheme != "https" {
		if mode != clientDeploymentModeLocal || parsed.Scheme != "http" || !isClientLoopbackHost(parsed.Hostname()) {
			return "", errors.New("HTTPS required except for local loopback HTTP")
		}
	}
	if originOnly {
		return parsed.Scheme + "://" + parsed.Host, nil
	}
	return raw, nil
}

func localClientRequestOrigin(request *http.Request) string {
	if request == nil || strings.TrimSpace(request.Host) == "" {
		return ""
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimSpace(request.Host)
}

func isClientLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeClientScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if value := strings.TrimSpace(scope); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func writeClientDiscoveryUnavailable(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	return json.NewEncoder(w).Encode(map[string]any{
		"error_code":  "client_discovery_unavailable",
		"reason_code": "client_connection_profile_not_configured",
		"retryable":   false,
		"user_guidance": map[string]any{
			"title":      "Client connection is not configured",
			"body":       "This TechStack instance cannot publish a safe client binding yet.",
			"next_steps": []string{"Ask the instance operator to configure the public URL and native OIDC client."},
		},
	})
}
