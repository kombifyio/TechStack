package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	tsauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Cloud-link flow: a self-hosted operator connects their kombify Cloud profile
// to the local account so stack creation can seed the Pocket ID owner from the
// verified cloud identity (owner_source "cloud-linked"). The flow is a plain
// OIDC authorization-code exchange with PKCE against the hosted cloud issuer;
// credentials are only ever collected on the hosted Universal Login page.
const (
	cloudLinkStateTTL         = 10 * time.Minute
	cloudLinkStatusIssued     = "issued"
	cloudLinkStatusConsumed   = "consumed"
	cloudLinkPurposeOwnerLink = "owner-link"
	cloudLinkCompletePath     = "/auth/cloud-link-complete"
	cloudLinkCallbackPath     = "/api/v1/auth/cloud-link/callback"
	cloudLinkBindingCookie    = "techstack_cloud_link"

	cloudLinkUnavailableErrorCode = "cloud_link_unavailable"
	reasonCloudOIDCNotConfigured  = "cloud_oidc_not_configured"
	reasonSaaSManagedIdentity     = "saas_managed_identity"
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
func handleRuntimeCloudLinkStart(runtime RuntimeStore, mode config.DeploymentMode) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if mode.IsSaaS() {
			return cloudLinkUnavailableOnSaaS(e)
		}
		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)

		issuer, clientID, _, err := getCloudOIDCConfig()
		if err != nil {
			return cloudLinkNotConfigured(e)
		}

		state, stateErr := randomURLToken(32)
		verifier, verifierErr := randomURLToken(48)
		if stateErr != nil || verifierErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to prepare the cloud link request", nil)
		}
		expiresAt := time.Now().UTC().Add(cloudLinkStateTTL)
		if storeErr := storeRuntimeCloudLinkState(runtime, e.Request.Context(), userID, state, verifier, expiresAt); storeErr != nil {
			slog.Default().Error("cloud-link: failed to store state", "error", storeErr)
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to prepare the cloud link request", nil)
		}
		setCloudLinkBindingCookie(e, state)

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
func handleRuntimeCloudLinkCallback(runtime RuntimeStore, mode config.DeploymentMode) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if mode.IsSaaS() {
			return cloudLinkComplete(e, "error", cloudLinkUnavailableErrorCode)
		}
		query := e.Request.URL.Query()
		if errParam := query.Get("error"); errParam != "" {
			slog.Default().Warn("cloud-link: provider error", "error", errParam, "description", query.Get("error_description"))
			return cloudLinkComplete(e, "error", "provider_error")
		}
		state := strings.TrimSpace(query.Get("state"))
		code := strings.TrimSpace(query.Get("code"))
		if state == "" || code == "" {
			return cloudLinkComplete(e, "error", "missing_code_or_state")
		}
		// The state (and with it the stored PKCE verifier) is usable only by
		// the browser that started the flow. Without this, a victim who opens
		// an attacker-started authorization URL would link their kombify
		// Cloud identity to the attacker's local account.
		if !cloudLinkBrowserMatches(e.Request, state) {
			slog.Default().Warn("cloud-link: callback without the initiating browser binding")
			return cloudLinkComplete(e, "error", "browser_mismatch")
		}
		clearCloudLinkBindingCookie(e)

		userID, verifier, consumeErr := consumeRuntimeCloudLinkState(runtime, e.Request.Context(), state)
		if consumeErr != nil {
			slog.Default().Warn("cloud-link: state consume failed", "error", consumeErr)
			return cloudLinkComplete(e, "error", "state_expired")
		}

		issuer, clientID, clientSecret, cfgErr := getCloudOIDCConfig()
		if cfgErr != nil {
			return cloudLinkComplete(e, "error", reasonCloudOIDCNotConfigured)
		}
		tokenResp, tokenErr := exchangeCodeForTokens(e.Request, issuer, clientID, clientSecret, code, cloudLinkRedirectURI(e.Request), verifier)
		if tokenErr != nil {
			slog.Default().Error("cloud-link: token exchange failed", "error", tokenErr)
			return cloudLinkComplete(e, "error", "token_exchange_failed")
		}
		userInfo, infoErr := fetchUserInfo(e.Request, issuer, tokenResp.AccessToken)
		if infoErr != nil {
			slog.Default().Error("cloud-link: userinfo fetch failed", "error", infoErr)
			return cloudLinkComplete(e, "error", "userinfo_failed")
		}
		if strings.TrimSpace(userInfo.Email) == "" {
			return cloudLinkComplete(e, "error", "email_missing")
		}
		if !userInfo.EmailVerified {
			return cloudLinkComplete(e, "error", "email_unverified")
		}

		if upsertErr := upsertRuntimeCloudLinkForUser(runtime, e.Request.Context(), userID, userInfo); upsertErr != nil {
			slog.Default().Error("cloud-link: link upsert failed", "error", upsertErr)
			return cloudLinkComplete(e, "error", "link_persist_failed")
		}
		return cloudLinkComplete(e, "ok", "")
	}
}

// getCloudLinkStatus reports the operator's current cloud link; the wizard
// polls it while the popup flow runs.
func getRuntimeCloudLinkStatus(runtime RuntimeStore) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)
		if err := runtime.Validate(); err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical cloud-link authority is unavailable", nil)
		}
		profile, err := runtime.Persistence.GetCloudProfile(e.Request.Context(), runtime.TenantID, runtime.InstanceID, userID)
		if errors.Is(err, controlplane.ErrNotFound) || err == nil && (profile == nil || profile.Link == nil) {
			return httpx.Success(e, http.StatusOK, cloudLinkStatusResponse{Linked: false})
		}
		if err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Cloud-link readback is unavailable", nil)
		}
		link := profile.Link
		return httpx.Success(e, http.StatusOK, cloudLinkStatusResponse{Linked: true, ExternalEmail: link.Email, ExternalName: link.DisplayName, EmailVerified: link.EmailVerified, LinkedAt: link.LinkedAt})
	}
}

func deleteRuntimeCloudLink(runtime RuntimeStore) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}
		if err := runtime.Validate(); err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical cloud-link authority is unavailable", nil)
		}
		userID, _ := AuthUserID(e)
		profile, err := runtime.Persistence.GetCloudProfile(e.Request.Context(), runtime.TenantID, runtime.InstanceID, userID)
		if errors.Is(err, controlplane.ErrNotFound) || err == nil && (profile == nil || profile.Link == nil) {
			return httpx.Success(e, http.StatusOK, map[string]any{"removed": false})
		}
		if err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Cloud-link readback is unavailable", nil)
		}
		if err := runtime.Persistence.SetCloudLink(e.Request.Context(), runtime.TenantID, runtime.InstanceID, userID, nil); err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to remove cloud link", nil)
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

// cloudLinkUnavailableOnSaaS answers the SaaS edition, where the kombify Cloud
// account already is the operator identity: cloud-link only projects a Cloud
// profile into a self-hosted install and has no state store here.
func cloudLinkUnavailableOnSaaS(e *httpx.Event) error {
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Cloud link is only available on self-hosted installs", map[string]any{
		"error_code":  cloudLinkUnavailableErrorCode,
		"reason_code": reasonSaaSManagedIdentity,
		"retryable":   false,
		"user_guidance": map[string]any{
			"title": "Your kombify Cloud account is already connected",
			"body":  "On techstack.kombify.io you sign in with your kombify Cloud account, so there is nothing to link. Cloud link connects a kombify Cloud profile to a self-hosted Techstack install.",
			"next_steps": []string{
				"Continue with your current account; stacks you create use it as the owner.",
				"To link a Cloud profile to a self-hosted install, start Cloud link from that install.",
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

// setCloudLinkBindingCookie binds the issued state to the initiating browser.
// SameSite=Lax still accompanies the top-level redirect back from the hosted
// login; the path scope keeps the cookie off every other request.
func setCloudLinkBindingCookie(e *httpx.Event, state string) {
	e.SetCookie(&http.Cookie{
		Name:     cloudLinkBindingCookie,
		Value:    state,
		Path:     cloudLinkCallbackPath,
		MaxAge:   int(cloudLinkStateTTL / time.Second),
		HttpOnly: true,
		Secure:   requestScheme(e.Request) == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCloudLinkBindingCookie(e *httpx.Event) {
	e.SetCookie(&http.Cookie{
		Name:     cloudLinkBindingCookie,
		Value:    "",
		Path:     cloudLinkCallbackPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestScheme(e.Request) == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func cloudLinkBrowserMatches(req *http.Request, state string) bool {
	cookie, err := req.Cookie(cloudLinkBindingCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) == 1
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

func storeRuntimeCloudLinkState(runtime RuntimeStore, ctx context.Context, userID, state, verifier string, expiresAt time.Time) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	encrypted, err := tsauth.EncryptIfNeeded(tsauth.GetEncryptor(), verifier)
	if err != nil {
		return err
	}
	return runtime.Persistence.SaveCloudLinkState(ctx, controlplane.CloudLinkState{TenantID: runtime.TenantID, InstanceID: runtime.InstanceID, SubjectID: userID, StateHash: cloudLinkStateHash(state), VerifierCiphertext: encrypted, ExpiresAt: expiresAt})
}

func consumeRuntimeCloudLinkState(runtime RuntimeStore, ctx context.Context, state string) (string, string, error) {
	if err := runtime.Validate(); err != nil {
		return "", "", err
	}
	stored, err := runtime.Persistence.ConsumeCloudLinkState(ctx, runtime.TenantID, runtime.InstanceID, cloudLinkStateHash(state), time.Now().UTC())
	if err != nil {
		return "", "", err
	}
	if stored == nil || !tsauth.IsEncrypted(stored.VerifierCiphertext) {
		return "", "", errCloudLinkStateInvalid
	}
	verifier, err := tsauth.DecryptIfNeeded(tsauth.GetEncryptor(), stored.VerifierCiphertext)
	if err != nil {
		return "", "", err
	}
	return stored.SubjectID, verifier, nil
}

func upsertRuntimeCloudLinkForUser(runtime RuntimeStore, ctx context.Context, userID string, info *oidcUserInfo) error {
	if err := runtime.Validate(); err != nil {
		return err
	}
	if info == nil {
		return fmt.Errorf("cloud identity required")
	}
	return runtime.Persistence.SetCloudLink(ctx, runtime.TenantID, runtime.InstanceID, userID, &controlplane.CloudIdentity{SubjectID: info.Sub, Email: info.Email, DisplayName: info.Name, EmailVerified: info.EmailVerified, LinkedAt: time.Now().UTC().Format(time.RFC3339Nano)})
}
