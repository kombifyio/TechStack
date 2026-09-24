package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/stretchr/testify/assert"
)

func TestApplyPlatformAuthPolicy_EnforcesCloudForSaaS(t *testing.T) {
	response := AuthModeResponse{
		Mode:            "local",
		DeploymentMode:  string(config.ModeSaaS),
		IsFirstRun:      true,
		AllowLocalLogin: true,
	}

	applyPlatformAuthPolicy(&response, config.ModeSaaS)

	assert.Equal(t, "cloud", response.Mode)
	assert.False(t, response.AllowLocalLogin)
	assert.False(t, response.IsFirstRun)
}

func TestApplyPlatformAuthPolicy_LeavesSelfHostedUntouched(t *testing.T) {
	response := AuthModeResponse{
		Mode:            "local",
		DeploymentMode:  string(config.ModeSelfHosted),
		IsFirstRun:      true,
		AllowLocalLogin: true,
	}

	applyPlatformAuthPolicy(&response, config.ModeSelfHosted)

	assert.Equal(t, "local", response.Mode)
	assert.True(t, response.AllowLocalLogin)
	assert.True(t, response.IsFirstRun)
}

func TestApplySelfHostedCloudLoginGate_FallsBackToLocalWithoutEnrollment(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://stack.example.com")
	t.Setenv("TECHSTACK_SELFHOSTED_CLOUD_LOGIN_TOKEN", "")
	t.Setenv("TECHSTACK_SELFHOSTED_CLOUD_LOGIN_PUBLIC_KEY", "")

	response := AuthModeResponse{
		Mode:            "cloud",
		DeploymentMode:  string(config.ModeSelfHosted),
		AllowLocalLogin: false,
	}

	applySelfHostedCloudLoginGate(&response, config.ModeSelfHosted)

	assert.Equal(t, "local", response.Mode)
	assert.True(t, response.AllowLocalLogin)
	assert.Nil(t, response.CloudAuthURL)
	assert.Nil(t, response.PortalURL)
}

func TestAuthResponseEdition_DefaultsFromDeploymentMode(t *testing.T) {
	assert.Equal(t, config.EditionPreview, authResponseEdition(config.EditionPreview, config.ModeSaaS))
	assert.Equal(t, config.EditionSaaSStandalone, authResponseEdition("", config.ModeSaaS))
	assert.Equal(t, config.EditionSelfHostOSS, authResponseEdition("", config.ModeSelfHosted))
}

func TestResolveCloudAuthorizationURL_UsesEnvFallback(t *testing.T) {
	t.Setenv("AUTH0_ISSUER", "login.kombify.io")
	t.Setenv("AUTH0_CLIENT_ID", "test-client-id")

	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/api/v1/auth/mode", nil)
	req.Host = "techstack.kombify.io"

	authURL := resolveCloudAuthorizationURL(req)
	if assert.NotNil(t, authURL) {
		assert.Equal(t, "https://techstack.kombify.io/api/v2/auth/login", *authURL)
	}
}

func TestResolveCloudAuthorizationURL_UsesAuth0IssuerFallback(t *testing.T) {
	t.Setenv("AUTH0_ISSUER", "login.kombify.io")
	t.Setenv("AUTH0_CLIENT_ID", "legacy-client-id")

	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/api/v1/auth/mode", nil)
	req.Host = "techstack.kombify.io"

	authURL := resolveCloudAuthorizationURL(req)
	if assert.NotNil(t, authURL) {
		assert.Equal(t, "https://techstack.kombify.io/api/v2/auth/login", *authURL)
	}
}

func TestCloudIssuerFromEnvDefaultsToLoginKombify(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "")
	t.Setenv("AUTH0_DOMAIN", "")
	t.Setenv("AUTH0_ISSUER", "")

	assert.Equal(t, config.DefaultCloudAuthIssuer, cloudIssuerFromEnv())
}

func TestCloudIssuerFromEnvUsesCanonicalConfiguredIssuer(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://login.kombify.io/")
	t.Setenv("AUTH0_DOMAIN", "")
	t.Setenv("AUTH0_ISSUER", "")

	assert.Equal(t, config.DefaultCloudAuthIssuer, cloudIssuerFromEnv())
}

func TestBuildOAuthLogoutURL_UsesOIDCEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/auth/logout", nil)
	req.Host = "techstack.kombify.io"

	logoutURL := buildOAuthLogoutURL(
		config.DefaultCloudAuthIssuer,
		"test-client-id",
		req,
	)

	assert.Contains(t, logoutURL, "https://login.kombify.io/oidc/logout")
	assert.Contains(t, logoutURL, "client_id=test-client-id")
	assert.Contains(t, logoutURL, "post_logout_redirect_uri=")
	assert.Contains(t, logoutURL, "https%3A%2F%2Ftechstack.kombify.io%2Flogin%3Flogged_out%3D1")
}

func TestBuildOAuthLogoutURL_UsesPublicOriginForLoopbackHosts(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5262/auth/logout", nil)
	req.Host = "127.0.0.1:5262"

	logoutURL := buildOAuthLogoutURL(
		config.DefaultCloudAuthIssuer,
		"test-client-id",
		req,
	)

	assert.Contains(t, logoutURL, "post_logout_redirect_uri=https%3A%2F%2Ftechstack.kombify.io%2Flogin%3Flogged_out%3D1")
	assert.NotContains(t, logoutURL, "127.0.0.1%3A5262")
}

func TestRedirectOriginDoesNotInventLocalhost(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "")
	t.Setenv("PUBLIC_ORIGIN", "")
	t.Setenv("APP_PUBLIC_ORIGIN", "")
	t.Setenv("APP_URL", "")

	if got := redirectOrigin(nil); strings.Contains(strings.ToLower(got), "localhost") {
		t.Fatalf("redirectOrigin(nil) = %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = ""
	req.URL.Host = ""
	if got := redirectOrigin(req); strings.Contains(strings.ToLower(got), "localhost") {
		t.Fatalf("redirectOrigin(empty request) = %q", got)
	}

	if got := buildCloudLoginURL(nil); strings.Contains(strings.ToLower(got), "localhost") {
		t.Fatalf("buildCloudLoginURL(nil) = %q", got)
	}

	logoutURL := buildOAuthLogoutURL(config.DefaultCloudAuthIssuer, "test-client-id", nil)
	if strings.Contains(strings.ToLower(logoutURL), "localhost") {
		t.Fatalf("buildOAuthLogoutURL(nil) = %q", logoutURL)
	}
}
