// Package auth provides tests for auth API routes.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	pbmigration "github.com/kombifyio/techstack/internal/pocketbase_migration"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAuthModeReportsEmptyStoreAsFirstRun(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}
	require.NoError(t, getAuthMode(app, config.ModeSelfHosted, config.EditionSelfHostOSS, nil)(event))
	var response struct {
		Data AuthModeResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "local", response.Data.Mode)
	require.True(t, response.Data.IsFirstRun)
}

func TestGetAuthModeDoesNotRequireFirstRunStoreInSaaS(t *testing.T) {
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}
	require.NoError(t, getAuthMode(nil, config.ModeSaaS, config.EditionSaaSStandalone, nil)(event))
	var response struct {
		Data AuthModeResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "cloud", response.Data.Mode)
	require.False(t, response.Data.IsFirstRun)
	require.False(t, response.Data.AllowLocalLogin)
}

type memoryLocalOwnerStore struct {
	record *authlocal.Record
}

func (s memoryLocalOwnerStore) Get(context.Context) (*authlocal.Record, error) {
	return s.record, nil
}

func TestGetAuthModeReportsEmptyCanonicalStoreAsFirstRun(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}

	require.NoError(t, getAuthMode(app, config.ModeSelfHosted, config.EditionSelfHostOSS, memoryLocalOwnerStore{})(event))

	var response struct {
		Data AuthModeResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, response.Data.IsFirstRun)
}

func TestGetAuthModeTreatsCanonicalOwnerAsNotFirstRun(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}
	owners := memoryLocalOwnerStore{record: &authlocal.Record{Email: "admin@techstack.local"}}

	require.NoError(t, getAuthMode(app, config.ModeSelfHosted, config.EditionSelfHostOSS, owners)(event))

	var response struct {
		Data AuthModeResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "local", response.Data.Mode)
	require.False(t, response.Data.IsFirstRun)
}

func TestHandleSetupRejectsWhenCanonicalOwnerExists(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", nil),
		Response: recorder,
	}
	owners := memoryLocalOwnerStore{record: &authlocal.Record{Email: "admin@techstack.local"}}
	provisioner := func(context.Context, LocalOwnerSetup) error {
		t.Fatal("setup must not provision a second local owner")
		return nil
	}

	require.NoError(t, handleSetup(app, config.ModeSelfHosted, provisioner, owners)(event))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	_, findErr := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.Error(t, findErr)
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

func TestCreateAuthConfigUpsertsDeterministicSingleton(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()

	_, err = app.FindCollectionByNameOrId("auth_config")
	require.Error(t, err)

	require.NoError(t, createAuthConfig(app, "local", true))

	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.NoError(t, err)
	require.Equal(t, pbmigration.AuthConfigRecordID, record.Id)
	require.NoError(t, createAuthConfig(app, "cloud", false))
	updated, err := app.FindRecordById("auth_config", pbmigration.AuthConfigRecordID)
	require.NoError(t, err)
	require.Equal(t, "cloud", updated.GetString("mode"))
	require.False(t, updated.GetBool("allow_local_login"))
}

func TestSetupLocalProvisionsConfiguredLocalAuthAuthority(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()
	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	existing := core.NewRecord(users)
	existing.Id = authlocal.BreakGlassRecordID
	existing.SetEmail("stale-owner@example.test")
	existing.SetPassword("stale-password")
	require.NoError(t, app.Save(existing))

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
	projection, err := app.FindRecordById("users", authlocal.BreakGlassRecordID)
	require.NoError(t, err)
	require.Equal(t, req.Email, projection.Email())
	require.False(t, projection.ValidatePassword(req.Password))
	record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	require.NoError(t, err)
	require.Equal(t, "local", record.GetString("mode"))
	require.True(t, record.GetBool("allow_local_login"))
}

func TestSetupLocalDoesNotMarkCompleteWithoutCanonicalAuthority(t *testing.T) {
	app, err := tests.NewTestApp()
	require.NoError(t, err)
	defer app.Cleanup()

	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", nil),
		Response: recorder,
	}
	err = setupLocal(app, event, setupRequest{
		Mode:     "local",
		Name:     "Windows Owner",
		Email:    "windows-owner@example.test",
		Password: "supersecret123",
	}, nil)

	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	_, userErr := app.FindRecordById("users", authlocal.BreakGlassRecordID)
	require.Error(t, userErr)
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

// Compile-time interface checks
var _ core.App = (*tests.TestApp)(nil)
