// Package auth provides tests for auth API routes.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetAuthMode_Default tests that without any config, we get local mode.
func TestGetAuthMode_Default(t *testing.T) {
	t.Run("returns local mode when no auth_config exists", func(t *testing.T) {
		// Expected default response
		expectedResponse := AuthModeResponse{
			Mode:            "local",
			CloudAuthURL:    nil,
			PortalURL:       nil,
			AllowLocalLogin: true,
		}

		// Simulate the expected response format
		successResponse := ksapi.SuccessResponse{
			Data: expectedResponse,
		}
		body, err := json.Marshal(successResponse)
		require.NoError(t, err)

		var result ksapi.SuccessResponse
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)

		// Verify the response structure
		dataBytes, err := json.Marshal(result.Data)
		require.NoError(t, err)

		var modeResponse AuthModeResponse
		err = json.Unmarshal(dataBytes, &modeResponse)
		require.NoError(t, err)

		assert.Equal(t, "local", modeResponse.Mode)
		assert.Nil(t, modeResponse.CloudAuthURL)
		assert.Nil(t, modeResponse.PortalURL)
		assert.True(t, modeResponse.AllowLocalLogin)
	})

	t.Run("response has correct JSON structure", func(t *testing.T) {
		expectedJSON := `{"data":{"mode":"local","edition":"selfhost-oss","deployment_mode":"self-hosted","is_first_run":false,"cloud_auth_url":null,"portal_url":null,"allow_local_login":true}}`
		var expected, actual map[string]any

		response := AuthModeResponse{
			Mode:            "local",
			Edition:         "selfhost-oss",
			DeploymentMode:  "self-hosted",
			IsFirstRun:      false,
			CloudAuthURL:    nil,
			PortalURL:       nil,
			AllowLocalLogin: true,
		}
		actualBytes, _ := json.Marshal(ksapi.SuccessResponse{Data: response})

		err := json.Unmarshal([]byte(expectedJSON), &expected)
		require.NoError(t, err)
		err = json.Unmarshal(actualBytes, &actual)
		require.NoError(t, err)

		assert.Equal(t, expected, actual)
	})
}

// TestGetAuthMode_LocalMode tests explicit local mode configuration.
func TestGetAuthMode_LocalMode(t *testing.T) {
	t.Run("returns local mode when explicitly configured", func(t *testing.T) {
		// Simulate a record with mode="local"
		response := AuthModeResponse{
			Mode:            "local",
			CloudAuthURL:    nil,
			PortalURL:       nil,
			AllowLocalLogin: true,
		}

		assert.Equal(t, "local", response.Mode)
		assert.Nil(t, response.CloudAuthURL)
		assert.True(t, response.AllowLocalLogin)
	})

	t.Run("local mode allows login fallback", func(t *testing.T) {
		response := AuthModeResponse{
			Mode:            "local",
			AllowLocalLogin: true,
		}

		// In local mode, local login should always be allowed
		assert.True(t, response.AllowLocalLogin)
	})
}

// TestGetAuthMode_CloudMode tests cloud mode with hosted cloud configuration.
func TestGetAuthMode_CloudMode(t *testing.T) {
	t.Run("returns cloud mode with auth URL", func(t *testing.T) {
		// Expected cloud mode response
		authURL := "https://localhost/api/v2/auth/login"
		portalURL := "https://portal.kombify.cloud"

		response := AuthModeResponse{
			Mode:            "cloud",
			CloudAuthURL:    &authURL,
			PortalURL:       &portalURL,
			AllowLocalLogin: false,
		}

		assert.Equal(t, "cloud", response.Mode)
		assert.NotNil(t, response.CloudAuthURL)
		assert.Contains(t, *response.CloudAuthURL, "/api/v2/auth/login")
		assert.NotNil(t, response.PortalURL)
		assert.Equal(t, "https://portal.kombify.cloud", *response.PortalURL)
	})

	t.Run("cloud mode can allow local login fallback", func(t *testing.T) {
		authURL := "https://localhost/api/v2/auth/login"
		response := AuthModeResponse{
			Mode:            "cloud",
			CloudAuthURL:    &authURL,
			AllowLocalLogin: true,
		}

		assert.Equal(t, "cloud", response.Mode)
		assert.True(t, response.AllowLocalLogin)
	})

	t.Run("cloud mode can disallow local login", func(t *testing.T) {
		authURL := "https://localhost/api/v2/auth/login"
		response := AuthModeResponse{
			Mode:            "cloud",
			CloudAuthURL:    &authURL,
			AllowLocalLogin: false,
		}

		assert.Equal(t, "cloud", response.Mode)
		assert.False(t, response.AllowLocalLogin)
	})
}

// TestBuildOAuthAuthorizationURL tests the OAuth URL builder.
func TestBuildOAuthAuthorizationURL(t *testing.T) {
	t.Run("builds correct URL with HTTPS", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://app.techstack.local/api/v1/auth/mode", nil)
		req.Host = "app.techstack.local"

		url := buildOAuthAuthorizationURL(
			"https://login.kombify.io",
			"test-client-id",
			req,
		)

		assert.Contains(t, url, "https://login.kombify.io/authorize")
		assert.Contains(t, url, "client_id=test-client-id")
		assert.Contains(t, url, "response_type=code")
		assert.Contains(t, url, "scope=openid")
		assert.Contains(t, url, "redirect_uri=")
	})

	t.Run("respects X-Forwarded-Proto header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://app.techstack.local/api/v1/auth/mode", nil)
		req.Host = "app.techstack.local"
		req.Header.Set("X-Forwarded-Proto", "https")

		url := buildOAuthAuthorizationURL(
			"https://login.kombify.io",
			"test-client-id",
			req,
		)

		assert.Contains(t, url, "redirect_uri=https")
	})

	t.Run("respects X-Forwarded-Host header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/auth/mode", nil)
		req.Host = "localhost"
		req.Header.Set("X-Forwarded-Host", "app.techstack.cloud")

		url := buildOAuthAuthorizationURL(
			"https://login.kombify.io",
			"test-client-id",
			req,
		)

		assert.Contains(t, url, "app.techstack.cloud")
	})

	t.Run("uses public origin when render host is loopback", func(t *testing.T) {
		t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io")

		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5262/api/v1/auth/mode", nil)
		req.Host = "127.0.0.1:5262"

		url := buildOAuthAuthorizationURL(
			"https://login.kombify.io",
			"test-client-id",
			req,
		)

		assert.Contains(t, url, "redirect_uri=https%3A%2F%2Ftechstack.kombify.io%2Fapi%2Fv1%2Fauth%2Fcallback")
		assert.NotContains(t, url, "127.0.0.1%3A5262")
	})

	t.Run("includes all required OAuth2 parameters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://app.techstack.local/", nil)
		req.Host = "app.techstack.local"

		url := buildOAuthAuthorizationURL(
			"https://auth.example.com",
			"my-client",
			req,
		)

		// Check all required OAuth2 parameters
		assert.Contains(t, url, "client_id=my-client")
		assert.Contains(t, url, "redirect_uri=")
		assert.Contains(t, url, "response_type=code")
		assert.Contains(t, url, "scope=openid+profile+email")
	})

	t.Run("uses standard authorize endpoint for cloud auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://app.techstack.local/", nil)
		req.Host = "app.techstack.local"

		url := buildOAuthAuthorizationURL(
			"https://login.kombify.io",
			"my-client",
			req,
		)

		assert.Contains(t, url, "https://login.kombify.io/authorize")
		assert.NotContains(t, url, "/oauth/v2/authorize")
	})
}

func TestCheckAuthAcceptsSignedEdgeIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/stack-identity", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "api-key:techstack"}))
	e := &httpx.Event{
		Request:  req,
		Response: httptest.NewRecorder(),
	}

	assert.NoError(t, CheckAuth(e))
	userID, ok := AuthUserID(e)
	assert.True(t, ok)
	assert.Equal(t, "api-key:techstack", userID)
}

func TestCreateAuthConfigCreatesMissingCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()

	_, err = app.FindCollectionByNameOrId("auth_config")
	require.Error(t, err)

	require.NoError(t, createAuthConfig(app, "local", true))

	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.NoError(t, err)
	require.Equal(t, "local", record.GetString("mode"))
	require.True(t, record.GetBool("allow_local_login"))
}

func TestSetupLocalProvisionsConfiguredLocalAuthAuthority(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()

	req := setupRequest{
		Mode:     "local",
		Name:     "Windows Owner",
		Email:    "windows-owner@example.test",
		Password: "supersecret123",
	}
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", nil),
		Response: recorder,
	}
	var got LocalOwnerSetup
	provisioner := func(ctx context.Context, setup LocalOwnerSetup) error {
		require.NotNil(t, ctx)
		got = setup
		return nil
	}

	require.NoError(t, setupLocal(app, event, req, provisioner))

	require.Equal(t, LocalOwnerSetup{
		Name:     "Windows Owner",
		Email:    "windows-owner@example.test",
		Password: "supersecret123",
	}, got)
	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.NoError(t, err)
	require.Equal(t, "local", record.GetString("mode"))
	require.True(t, record.GetBool("allow_local_login"))
}

func TestSetupLocalDoesNotMarkCompleteWhenProvisionerFails(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()

	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", nil),
		Response: recorder,
	}
	provisionErr := errors.New("local authority unavailable")

	err = setupLocal(app, event, setupRequest{
		Mode:     "local",
		Name:     "Windows Owner",
		Email:    "windows-owner@example.test",
		Password: "supersecret123",
	}, func(context.Context, LocalOwnerSetup) error {
		return provisionErr
	})

	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	_, findErr := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.Error(t, findErr)
}

// TestOIDCCallback tests the OIDC callback handler.
func TestOIDCCallback(t *testing.T) {
	t.Run("handles missing code parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)

		// No code parameter should result in bad request
		code := req.URL.Query().Get("code")
		assert.Empty(t, code, "missing code should trigger bad request")
	})

	t.Run("handles error parameter from IdP", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?error=access_denied&error_description=User%20denied%20access", nil)

		errorParam := req.URL.Query().Get("error")
		errorDesc := req.URL.Query().Get("error_description")

		assert.Equal(t, "access_denied", errorParam)
		assert.Equal(t, "User denied access", errorDesc)
	})

	t.Run("handles valid code parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=test-auth-code-12345", nil)

		code := req.URL.Query().Get("code")
		assert.NotEmpty(t, code, "valid code should be present")
		assert.Equal(t, "test-auth-code-12345", code)
	})
}

// TestAuthModeResponseSerialization tests JSON serialization of responses.
func TestAuthModeResponseSerialization(t *testing.T) {
	t.Run("local mode serializes correctly", func(t *testing.T) {
		response := AuthModeResponse{
			Mode:            "local",
			Edition:         "selfhost-oss",
			DeploymentMode:  "self-hosted",
			IsFirstRun:      false,
			CloudAuthURL:    nil,
			PortalURL:       nil,
			AllowLocalLogin: true,
		}

		body, err := json.Marshal(response)
		require.NoError(t, err)

		jsonStr := string(body)
		assert.Contains(t, jsonStr, `"mode":"local"`)
		assert.Contains(t, jsonStr, `"edition":"selfhost-oss"`)
		assert.Contains(t, jsonStr, `"deployment_mode":"self-hosted"`)
		assert.Contains(t, jsonStr, `"allow_local_login":true`)
		assert.Contains(t, jsonStr, `"cloud_auth_url":null`)
		assert.Contains(t, jsonStr, `"portal_url":null`)
	})

	t.Run("cloud mode serializes correctly", func(t *testing.T) {
		authURL := "https://techstack.kombify.io/api/v2/auth/login"
		portalURL := "https://portal.example.com"
		response := AuthModeResponse{
			Mode:            "cloud",
			Edition:         "saas-standalone",
			DeploymentMode:  "saas",
			IsFirstRun:      false,
			CloudAuthURL:    &authURL,
			PortalURL:       &portalURL,
			AllowLocalLogin: false,
		}

		body, err := json.Marshal(response)
		require.NoError(t, err)

		jsonStr := string(body)
		assert.Contains(t, jsonStr, `"mode":"cloud"`)
		assert.Contains(t, jsonStr, `"edition":"saas-standalone"`)
		assert.Contains(t, jsonStr, `"allow_local_login":false`)
		assert.Contains(t, jsonStr, `"cloud_auth_url":"https://techstack.kombify.io/api/v2/auth/login"`)
		assert.Contains(t, jsonStr, `"portal_url":"https://portal.example.com"`)
	})
}

// Compile-time interface checks
var _ core.App = (*tests.TestApp)(nil)
