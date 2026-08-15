package auth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// oidcHTTPClient is a pooled client for OIDC provider calls.
var oidcHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// errCloudLinkStateInvalid marks a cloud-link state that is unknown, expired,
// already consumed, or otherwise unusable.
var errCloudLinkStateInvalid = errors.New("cloud link state invalid")

const (
	envCloudClientSecretNext = "TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT" //nolint:gosec // env var NAME, not a credential value
	maxOIDCErrorBodyBytes    = 16 << 10
)

// oidcUserInfo represents the userinfo response from the hosted cloud provider.
type oidcUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// handleOIDCCallbackFull implements the full OAuth2 authorization code exchange flow
// for self-hosted instances using the hosted cloud identity provider.
//
// Flow:
//  1. The hosted identity provider redirects browser to /api/v1/auth/callback?code=xxx&state=yyy
//  2. Backend reads cloud OIDC config from auth_config record (or env vars)
//  3. Backend exchanges code for tokens at the provider's token endpoint
//  4. Backend fetches userinfo from the provider
//  5. Backend creates/updates local PB user + user_link
//  6. Backend generates PB auth token
//  7. Backend redirects browser to /auth/oidc-complete#pb_token=xxx
func handleOIDCCallbackFull(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		code := e.Request.URL.Query().Get("code")
		errorParam := e.Request.URL.Query().Get("error")

		if errorParam != "" {
			errorDesc := e.Request.URL.Query().Get("error_description")
			return redirectToLogin(e, fmt.Sprintf("OAuth error: %s - %s", errorParam, errorDesc))
		}

		if code == "" {
			return redirectToLogin(e, "Missing authorization code")
		}

		// Read cloud OIDC config
		issuer, clientID, clientSecret, err := getCloudOIDCConfig(app)
		if err != nil {
			app.Logger().Error("OIDC callback: failed to get cloud auth config", "error", err)
			return redirectToLogin(e, "Cloud authentication is not configured")
		}

		// Build redirect URI (must match the one used in the authorization request)
		redirectURI := buildRedirectURI(e.Request)

		// Exchange code for tokens
		tokenResp, err := exchangeCodeForTokens(e.Request, issuer, clientID, clientSecret, code, redirectURI, "")
		if err != nil {
			app.Logger().Error("OIDC callback: token exchange failed", "error", err)
			return redirectToLogin(e, "Authentication failed: could not exchange code")
		}

		// Get userinfo
		userInfo, err := fetchUserInfo(e.Request, issuer, tokenResp.AccessToken)
		if err != nil {
			app.Logger().Error("OIDC callback: userinfo fetch failed", "error", err)
			return redirectToLogin(e, "Authentication failed: could not fetch user info")
		}

		// Create or update local PB user
		pbUser, _, err := findOrCreateUserFromOIDC(app, userInfo)
		if err != nil {
			app.Logger().Error("OIDC callback: user creation failed", "error", err)
			return redirectToLogin(e, "Authentication failed: could not create user")
		}

		// Generate PB auth token
		pbToken, err := pbUser.NewAuthToken()
		if err != nil {
			app.Logger().Error("OIDC callback: token generation failed", "error", err)
			return redirectToLogin(e, "Authentication failed: could not generate session")
		}

		// Redirect to frontend with token in URL fragment
		userJSON, _ := json.Marshal(map[string]string{
			"id":    pbUser.Id,
			"email": pbUser.Email(),
			"name":  pbUser.GetString("name"),
		})

		fragment := url.Values{}
		fragment.Set("pb_token", pbToken)
		fragment.Set("user", string(userJSON))

		redirectURL := fmt.Sprintf("/auth/oidc-complete#%s", fragment.Encode())
		return e.Redirect(http.StatusFound, redirectURL)
	}
}

func getStoredCloudIssuer(record *core.Record) string {
	if record == nil {
		return ""
	}
	return record.GetString("cloud_issuer")
}

func getStoredCloudClientID(record *core.Record) string {
	if record == nil {
		return ""
	}
	return record.GetString("cloud_client_id")
}

// getCloudOIDCConfig reads cloud OIDC config from auth_config record or env vars.
func getCloudOIDCConfig(app core.App) (issuer, clientID, clientSecret string, err error) {
	// Env vars take precedence
	issuer = cloudIssuerFromEnv()
	clientID = firstEnv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "AUTH0_CLIENT_ID")
	clientSecret = firstEnv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET", "AUTH0_CLIENT_SECRET")

	// Fall back to auth_config record
	if issuer == "" || clientID == "" {
		record, recErr := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
		if recErr != nil || record == nil {
			return "", "", "", fmt.Errorf("no auth_config record found")
		}

		if issuer == "" {
			issuer = getStoredCloudIssuer(record)
		}
		if clientID == "" {
			clientID = getStoredCloudClientID(record)
		}
		if clientSecret == "" {
			clientSecret = record.GetString("cloud_client_secret")
		}
	}

	if issuer == "" || clientID == "" {
		return "", "", "", fmt.Errorf("cloud_issuer and cloud_client_id are required")
	}

	return issuer, clientID, clientSecret, nil
}

// buildRedirectURI constructs the OAuth2 redirect URI based on the current request.
// It preserves the actual callback path so legacy /auth/callback and the new
// proxied /api/v1/auth/callback alias both complete successfully.
func buildRedirectURI(req *http.Request) string {
	if req == nil {
		return "https://localhost/api/v1/auth/callback"
	}

	callbackPath := "/api/v1/auth/callback"
	if req.URL != nil && req.URL.Path != "" {
		callbackPath = req.URL.Path
	}

	return requestOrigin(req) + callbackPath
}

// requestOrigin resolves the external scheme://host origin of the request,
// honoring reverse-proxy forwarding headers.
func requestOrigin(req *http.Request) string {
	scheme := "https"
	if req.TLS == nil {
		if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else {
			scheme = "http"
		}
	}

	host := req.Host
	if fwdHost := req.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	return fmt.Sprintf("%s://%s", scheme, host)
}

// oidcTokenResponse represents the token endpoint response.
type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type oidcTokenEndpointError struct {
	StatusCode int
	Code       string
}

func (e *oidcTokenEndpointError) Error() string {
	return fmt.Sprintf("token endpoint returned %d", e.StatusCode)
}

// exchangeCodeForTokens exchanges an authorization code for tokens at the
// provider token endpoint. codeVerifier is the PKCE verifier when the
// authorization request carried a code challenge; pass "" for non-PKCE flows.
func exchangeCodeForTokens(reqCtx *http.Request, issuer, clientID, clientSecret, code, redirectURI, codeVerifier string) (*oidcTokenResponse, error) {
	tokenResp, err := exchangeCodeForTokensWithSecret(reqCtx, issuer, clientID, clientSecret, code, redirectURI, codeVerifier)
	if err == nil {
		return tokenResp, nil
	}

	nextSecret := firstEnv(envCloudClientSecretNext)
	var providerErr *oidcTokenEndpointError
	if nextSecret == "" ||
		subtle.ConstantTimeCompare([]byte(clientSecret), []byte(nextSecret)) == 1 ||
		!errors.As(err, &providerErr) ||
		providerErr.Code != "invalid_client" {
		return nil, err
	}

	return exchangeCodeForTokensWithSecret(reqCtx, issuer, clientID, nextSecret, code, redirectURI, codeVerifier)
}

func exchangeCodeForTokensWithSecret(reqCtx *http.Request, issuer, clientID, clientSecret, code, redirectURI, codeVerifier string) (*oidcTokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/oauth/token", strings.TrimSuffix(issuer, "/"))

	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

	req, err := http.NewRequestWithContext(reqCtx.Context(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Use Basic Auth for client credentials (RFC 6749 §2.3.1)
	if clientSecret != "" {
		req.SetBasicAuth(clientID, clientSecret)
	} else {
		// Public client — include client_id in body
		data.Set("client_id", clientID)
		req.Body = http.NoBody
		req, _ = http.NewRequestWithContext(reqCtx.Context(), "POST", tokenURL, strings.NewReader(data.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var providerError struct {
			Code string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, maxOIDCErrorBodyBytes)).Decode(&providerError)
		return nil, &oidcTokenEndpointError{
			StatusCode: resp.StatusCode,
			Code:       strings.TrimSpace(providerError.Code),
		}
	}

	var tokenResp oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	return &tokenResp, nil
}

// fetchUserInfo fetches user information from the provider userinfo endpoint.
func fetchUserInfo(reqCtx *http.Request, issuer, accessToken string) (*oidcUserInfo, error) {
	userInfoURL := fmt.Sprintf("%s/userinfo", strings.TrimSuffix(issuer, "/"))

	req, err := http.NewRequestWithContext(reqCtx.Context(), "GET", userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned %d", resp.StatusCode)
	}

	var userInfo oidcUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}

	return &userInfo, nil
}

// findOrCreateUserFromOIDC finds or creates a PocketBase user based on OIDC userinfo.
// Uses the same user_links pattern as findOrCreateUserFromSSO.
func findOrCreateUserFromOIDC(app core.App, userInfo *oidcUserInfo) (*core.Record, *core.Record, error) {
	// Try to find existing user_link by external_id (cloud subject)
	userLink, err := app.FindFirstRecordByFilter(
		"user_links",
		"external_id = {:external_id} && provider = 'cloud'",
		map[string]any{
			"external_id": userInfo.Sub,
		},
	)

	if err == nil && userLink != nil {
		// Found existing link — get PB user and update profile
		userID := userLink.GetString("user")
		pbUser, err := app.FindRecordById("users", userID)
		if err != nil {
			return nil, nil, fmt.Errorf("linked user not found: %w", err)
		}

		// Update mutable fields from cloud claims
		pbUser.Set("name", userInfo.Name)
		pbUser.SetVerified(userInfo.EmailVerified)
		if err := app.Save(pbUser); err != nil {
			app.Logger().Warn("OIDC: failed to update user profile", "error", err)
		}

		// Update link fields
		userLink.Set("external_email", userInfo.Email)
		userLink.Set("external_name", userInfo.Name)
		userLink.Set("email_verified", userInfo.EmailVerified)
		if err := app.Save(userLink); err != nil {
			app.Logger().Warn("OIDC: failed to update user_link", "error", err)
		}

		return pbUser, userLink, nil
	}

	// No existing link — check by email
	pbUser, err := app.FindFirstRecordByFilter(
		"users",
		"email = {:email}",
		map[string]any{"email": userInfo.Email},
	)

	if err != nil {
		// Create new PB user
		usersCollection, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return nil, nil, fmt.Errorf("find users collection: %w", err)
		}

		pbUser = core.NewRecord(usersCollection)
		pbUser.SetEmail(userInfo.Email)
		pbUser.Set("name", userInfo.Name)
		pbUser.SetVerified(userInfo.EmailVerified)
		pbUser.SetPassword(security.RandomString(32))

		if err := app.Save(pbUser); err != nil {
			return nil, nil, fmt.Errorf("create user: %w", err)
		}
	}

	// Create user_link
	userLinksCollection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		return nil, nil, fmt.Errorf("find user_links collection: %w", err)
	}

	userLink = core.NewRecord(userLinksCollection)
	userLink.Set("user", pbUser.Id)
	userLink.Set("provider", "cloud")
	userLink.Set("external_id", userInfo.Sub)
	userLink.Set("external_email", userInfo.Email)
	userLink.Set("external_name", userInfo.Name)
	userLink.Set("email_verified", userInfo.EmailVerified)
	userLink.Set("is_admin", false)

	if err := app.Save(userLink); err != nil {
		return nil, nil, fmt.Errorf("create user_link: %w", err)
	}

	return pbUser, userLink, nil
}

// redirectToLogin redirects the user to the login page with an error message.
func redirectToLogin(e *httpx.Event, msg string) error {
	return e.Redirect(http.StatusFound, "/login?error="+url.QueryEscape(msg))
}

// handleOIDCLogout handles the OIDC logout callback.
// In SaaS/Auth0 mode this clears the upstream Auth0 session and returns the
// browser to the Techstack login entrypoint. If cloud logout is not configured,
// it falls back to the local login page instead of returning a 404.
func handleOIDCLogout(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		record, err := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
		if err == nil {
			if logoutURL := resolveCloudLogoutURL(record, e.Request); logoutURL != nil {
				return e.Redirect(http.StatusFound, *logoutURL)
			}
		}

		if logoutURL := resolveCloudLogoutURL(nil, e.Request); logoutURL != nil {
			return e.Redirect(http.StatusFound, *logoutURL)
		}

		return e.Redirect(http.StatusFound, "/login?logged_out=1")
	}
}
