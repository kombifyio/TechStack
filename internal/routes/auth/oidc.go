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

// getCloudOIDCConfig reads cloud OIDC config from canonical environment custody.
func getCloudOIDCConfig() (issuer, clientID, clientSecret string, err error) {
	issuer = cloudIssuerFromEnv()
	clientID = firstEnv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "AUTH0_CLIENT_ID")
	clientSecret = firstEnv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET", "AUTH0_CLIENT_SECRET")

	if issuer == "" || clientID == "" {
		return "", "", "", fmt.Errorf("cloud_issuer and cloud_client_id are required")
	}

	return issuer, clientID, clientSecret, nil
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

// handleOIDCLogout handles the OIDC logout callback.
// In SaaS/Auth0 mode this clears the upstream Auth0 session and returns the
// browser to the Techstack login entrypoint. If cloud logout is not configured,
// it falls back to the local login page instead of returning a 404.
func handleOIDCLogout(app core.App) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if logoutURL := resolveCloudLogoutURL(e.Request); logoutURL != nil {
			return e.Redirect(http.StatusFound, *logoutURL)
		}

		return e.Redirect(http.StatusFound, "/login?logged_out=1")
	}
}
