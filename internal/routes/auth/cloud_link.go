package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	tsauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Cloud-link flow: a self-hosted operator connects their kombify Cloud profile
// to the local account so stack creation can seed the Pocket ID owner from the
// verified cloud identity (owner_source "cloud-linked"). The flow is a plain
// OIDC authorization-code exchange with PKCE against the hosted cloud issuer;
// credentials are only ever collected on the hosted Universal Login page.
const (
	cloudLinkStatesCollection = "cloud_link_states"
	cloudLinkStateTTL         = 10 * time.Minute
	cloudLinkStatusIssued     = "issued"
	cloudLinkStatusConsumed   = "consumed"
	cloudLinkPurposeOwnerLink = "owner-link"
	cloudLinkCompletePath     = "/auth/cloud-link-complete"
	cloudLinkCallbackPath     = "/api/v1/auth/cloud-link/callback"

	cloudLinkUnavailableErrorCode = "cloud_link_unavailable"
	reasonCloudOIDCNotConfigured  = "cloud_oidc_not_configured"
)

type cloudLinkStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        string `json:"expires_at"`
}

type cloudLinkStatusResponse struct {
	Linked        bool   `json:"linked"`
	ExternalEmail string `json:"external_email,omitempty"`
	ExternalName  string `json:"external_name,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	LinkedAt      string `json:"linked_at,omitempty"`
}

// handleCloudLinkStart mints a single-use PKCE state and returns the hosted
// authorization URL the frontend opens in a popup (or full-page redirect).
func handleCloudLinkStart(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)

		issuer, clientID, _, err := getCloudOIDCConfig(app)
		if err != nil {
			return cloudLinkNotConfigured(e)
		}

		state, stateErr := randomURLToken(32)
		verifier, verifierErr := randomURLToken(48)
		if stateErr != nil || verifierErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to prepare the cloud link request", nil)
		}
		expiresAt := time.Now().UTC().Add(cloudLinkStateTTL)
		if storeErr := storeCloudLinkState(app, userID, state, verifier, expiresAt); storeErr != nil {
			app.Logger().Error("cloud-link: failed to store state", "error", storeErr)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to prepare the cloud link request", nil)
		}

		challenge := base64.RawURLEncoding.EncodeToString(func() []byte {
			sum := sha256.Sum256([]byte(verifier))
			return sum[:]
		}())
		query := url.Values{
			"response_type":         {"code"},
			"client_id":             {clientID},
			"redirect_uri":          {cloudLinkRedirectURI(e.Request)},
			"scope":                 {"openid profile email"},
			"state":                 {state},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
		}
		authorizationURL := strings.TrimSuffix(issuer, "/") + "/authorize?" + query.Encode()

		return httpx.Success(e, http.StatusOK, cloudLinkStartResponse{
			AuthorizationURL: authorizationURL,
			ExpiresAt:        expiresAt.Format(time.RFC3339),
		})
	}
}

// handleCloudLinkCallback finishes the PKCE exchange and upserts the cloud
// link for the user who initiated the flow (never a new user). All failures
// land on the completion page as fragment reasons so the popup can report
// them without leaking details into server logs users cannot see.
func handleCloudLinkCallback(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		query := e.Request.URL.Query()
		if errParam := query.Get("error"); errParam != "" {
			app.Logger().Warn("cloud-link: provider error", "error", errParam, "description", query.Get("error_description"))
			return cloudLinkComplete(e, "error", "provider_error")
		}
		state := strings.TrimSpace(query.Get("state"))
		code := strings.TrimSpace(query.Get("code"))
		if state == "" || code == "" {
			return cloudLinkComplete(e, "error", "missing_code_or_state")
		}

		userID, verifier, consumeErr := consumeCloudLinkState(app, state)
		if consumeErr != nil {
			app.Logger().Warn("cloud-link: state consume failed", "error", consumeErr)
			return cloudLinkComplete(e, "error", "state_expired")
		}

		issuer, clientID, clientSecret, cfgErr := getCloudOIDCConfig(app)
		if cfgErr != nil {
			return cloudLinkComplete(e, "error", reasonCloudOIDCNotConfigured)
		}
		tokenResp, tokenErr := exchangeCodeForTokens(e.Request, issuer, clientID, clientSecret, code, cloudLinkRedirectURI(e.Request), verifier)
		if tokenErr != nil {
			app.Logger().Error("cloud-link: token exchange failed", "error", tokenErr)
			return cloudLinkComplete(e, "error", "token_exchange_failed")
		}
		userInfo, infoErr := fetchUserInfo(e.Request, issuer, tokenResp.AccessToken)
		if infoErr != nil {
			app.Logger().Error("cloud-link: userinfo fetch failed", "error", infoErr)
			return cloudLinkComplete(e, "error", "userinfo_failed")
		}
		if strings.TrimSpace(userInfo.Email) == "" {
			return cloudLinkComplete(e, "error", "email_missing")
		}
		if !userInfo.EmailVerified {
			return cloudLinkComplete(e, "error", "email_unverified")
		}

		if upsertErr := upsertCloudLinkForUser(app, userID, userInfo); upsertErr != nil {
			app.Logger().Error("cloud-link: link upsert failed", "error", upsertErr)
			return cloudLinkComplete(e, "error", "link_persist_failed")
		}
		return cloudLinkComplete(e, "ok", "")
	}
}

// getCloudLinkStatus reports the operator's current cloud link; the wizard
// polls it while the popup flow runs.
func getCloudLinkStatus(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)
		record := findCloudLinkRecord(app, userID)
		if record == nil {
			return httpx.Success(e, http.StatusOK, cloudLinkStatusResponse{Linked: false})
		}
		return httpx.Success(e, http.StatusOK, cloudLinkStatusResponse{
			Linked:        true,
			ExternalEmail: record.GetString("external_email"),
			ExternalName:  record.GetString("external_name"),
			EmailVerified: record.GetBool("email_verified"),
			LinkedAt:      record.GetString("updated"),
		})
	}
}

// deleteCloudLink removes the operator's cloud link ("use a different account").
func deleteCloudLink(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)
		record := findCloudLinkRecord(app, userID)
		if record == nil {
			return httpx.Success(e, http.StatusOK, map[string]any{"removed": false})
		}
		if err := app.Delete(record); err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to remove the cloud link", nil)
		}
		return httpx.Success(e, http.StatusOK, map[string]any{"removed": true})
	}
}

func cloudLinkNotConfigured(e *httpx.Event) error {
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Cloud authentication is not configured on this instance", map[string]any{
		"error_code":  cloudLinkUnavailableErrorCode,
		"reason_code": reasonCloudOIDCNotConfigured,
		"retryable":   false,
		"user_guidance": map[string]any{
			"title": "kombify Cloud login is not configured",
			"body":  "This instance has no cloud OIDC issuer/client configured, so a kombify Cloud profile cannot be linked.",
			"next_steps": []string{
				"Configure TECHSTACK_AUTH_CLOUD_ISSUER, TECHSTACK_AUTH_CLOUD_CLIENT_ID, and TECHSTACK_AUTH_CLOUD_CLIENT_SECRET.",
				"Alternatively, use a local owner instead of the cloud-linked owner.",
			},
		},
	})
}

func cloudLinkComplete(e *httpx.Event, status, reason string) error {
	fragment := url.Values{}
	fragment.Set("status", status)
	if reason != "" {
		fragment.Set("reason", reason)
	}
	return e.Redirect(http.StatusFound, cloudLinkCompletePath+"#"+fragment.Encode())
}

// cloudLinkRedirectURI builds the OAuth2 redirect URI for the cloud-link
// callback from the current request origin (the start and callback requests
// must resolve to the same registered redirect URI).
func cloudLinkRedirectURI(req *http.Request) string {
	if req == nil {
		return "https://localhost" + cloudLinkCallbackPath
	}
	return requestOrigin(req) + cloudLinkCallbackPath
}

func randomURLToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func cloudLinkStateHash(state string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(state)))
	return hex.EncodeToString(sum[:])
}

func storeCloudLinkState(app core.App, userID, state, verifier string, expiresAt time.Time) error {
	collection, err := app.FindCollectionByNameOrId(cloudLinkStatesCollection)
	if err != nil {
		return err
	}
	encryptedVerifier, encErr := tsauth.EncryptIfNeeded(tsauth.GetEncryptor(), verifier)
	if encErr != nil {
		encryptedVerifier = verifier
	}
	record := core.NewRecord(collection)
	record.Set("state_hash", cloudLinkStateHash(state))
	record.Set("user", userID)
	record.Set("code_verifier", encryptedVerifier)
	record.Set("purpose", cloudLinkPurposeOwnerLink)
	record.Set("status", cloudLinkStatusIssued)
	record.Set("expires_at", expiresAt.Format(time.RFC3339))
	return app.Save(record)
}

// consumeCloudLinkState atomically consumes a single-use state and returns the
// initiating user plus the PKCE verifier. The guarded UPDATE makes replayed
// callbacks lose the race instead of silently double-linking.
func consumeCloudLinkState(app core.App, state string) (userID, verifier string, err error) {
	record, findErr := app.FindFirstRecordByFilter(
		cloudLinkStatesCollection,
		"state_hash = {:stateHash}",
		map[string]any{"stateHash": cloudLinkStateHash(state)},
	)
	if findErr != nil || record == nil {
		return "", "", errCloudLinkStateInvalid
	}
	if record.GetString("purpose") != cloudLinkPurposeOwnerLink {
		return "", "", errCloudLinkStateInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, updateErr := app.DB().NewQuery(`
		UPDATE {{` + cloudLinkStatesCollection + `}}
		SET [[status]] = {:consumed}, [[consumed_at]] = {:now}
		WHERE [[state_hash]] = {:stateHash}
			AND [[status]] = {:issued}
			AND [[expires_at]] > {:now}
	`).Bind(dbx.Params{
		"consumed":  cloudLinkStatusConsumed,
		"now":       now,
		"stateHash": cloudLinkStateHash(state),
		"issued":    cloudLinkStatusIssued,
	}).Execute()
	if updateErr != nil {
		return "", "", updateErr
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return "", "", errCloudLinkStateInvalid
	}
	decrypted, decErr := tsauth.DecryptIfNeeded(tsauth.GetEncryptor(), record.GetString("code_verifier"))
	if decErr != nil {
		decrypted = record.GetString("code_verifier")
	}
	return record.GetString("user"), decrypted, nil
}

func findCloudLinkRecord(app core.App, userID string) *core.Record {
	if app == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	record, err := app.FindFirstRecordByFilter(
		"user_links",
		"user = {:userID} && provider = 'cloud'",
		map[string]any{"userID": userID},
	)
	if err != nil {
		return nil
	}
	return record
}

// upsertCloudLinkForUser links (or relinks) the verified cloud identity to the
// given local user. Unlike findOrCreateUserFromOIDC it never creates a user:
// the flow strictly attaches an external identity to the already-authenticated
// operator.
func upsertCloudLinkForUser(app core.App, userID string, userInfo *oidcUserInfo) error {
	record := findCloudLinkRecord(app, userID)
	if record == nil {
		collection, err := app.FindCollectionByNameOrId("user_links")
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("user", userID)
		record.Set("provider", "cloud")
		record.Set("is_admin", false)
	}
	record.Set("external_id", userInfo.Sub)
	record.Set("external_email", userInfo.Email)
	record.Set("external_name", userInfo.Name)
	record.Set("email_verified", userInfo.EmailVerified)
	return app.Save(record)
}
