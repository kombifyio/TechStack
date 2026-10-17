package auth

import (
	"context"
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAuthModeDoesNotRequireFirstRunStoreInSaaS(t *testing.T) {
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}
	require.NoError(t, getRuntimeAuthMode(RuntimeStore{}, config.ModeSaaS, config.EditionSaaSStandalone, nil)(event))
	var response struct {
		Data AuthModeResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "cloud", response.Data.Mode)
	require.False(t, response.Data.IsFirstRun)
	require.False(t, response.Data.AllowLocalLogin)
}

// The UI branches on wallet_source to pick the legacy label flow or the
// kombify Wallet picker, so an unset source must read as legacy.

func TestGetAuthModeReportsWhereCredentialsAreKept(t *testing.T) {
	for source, want := range map[kombifywallet.Source]string{
		kombifywallet.SourceKombify: "kombify",
		kombifywallet.SourceLegacy:  "legacy",
		"":                          "legacy",
	} {
		recorder := httptest.NewRecorder()
		event := &httpx.Event{Request: httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil), Response: recorder}
		require.NoError(t, withWalletSource(source, getRuntimeAuthMode(RuntimeStore{}, config.ModeSaaS, config.EditionSaaSStandalone, nil))(event))
		var response struct {
			Data AuthModeResponse `json:"data"`
		}
		require.NoError(t, json.NewDecoder(recorder.Body).Decode(&response))
		require.Equal(t, want, response.Data.WalletSource, "source %q", source)
	}
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
