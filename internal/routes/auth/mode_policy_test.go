package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/pocketbase/pocketbase/core"
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

	authURL := resolveCloudAuthorizationURL(nil, req)
	if assert.NotNil(t, authURL) {
		assert.Equal(t, "https://techstack.kombify.io/api/v2/auth/login", *authURL)
	}
}

func TestResolveCloudAuthorizationURL_UsesAuth0IssuerFallback(t *testing.T) {
	t.Setenv("AUTH0_ISSUER", "login.kombify.io")
	t.Setenv("AUTH0_CLIENT_ID", "legacy-client-id")

	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/api/v1/auth/mode", nil)
	req.Host = "techstack.kombify.io"

	authURL := resolveCloudAuthorizationURL(nil, req)
	if assert.NotNil(t, authURL) {
		assert.Equal(t, "https://techstack.kombify.io/api/v2/auth/login", *authURL)
	}
}

func TestResolveCloudAuthorizationURL_UsesLegacyAuth0RecordFields(t *testing.T) {
	collection := core.NewBaseCollection("auth_config")
	collection.Fields.Add(
		&core.TextField{Name: "auth0_issuer"},
		&core.TextField{Name: "auth0_client_id"},
	)
	record := core.NewRecord(collection)
	record.Set("auth0_issuer", "https://legacy.auth0.example")
	record.Set("auth0_client_id", "legacy-client-id")

	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/api/v1/auth/mode", nil)
	req.Host = "techstack.kombify.io"

	authURL := resolveCloudAuthorizationURL(record, req)
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

func TestCloudIssuerFromEnvMapsLegacyKombifyTenantToCustomDomain(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://kombify.eu.auth0.com/")
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

func TestBuildRedirectURI_UsesProxiedCallbackPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/api/v1/auth/callback?code=test", nil)
	req.Host = "techstack.kombify.io"

	redirectURI := buildRedirectURI(req)

	assert.Equal(t, "https://techstack.kombify.io/api/v1/auth/callback", redirectURI)
}

func TestBuildRedirectURI_PreservesLegacyCallbackPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/auth/callback?code=test", nil)
	req.Host = "techstack.kombify.io"

	redirectURI := buildRedirectURI(req)

	assert.Equal(t, "https://techstack.kombify.io/auth/callback", redirectURI)
}
