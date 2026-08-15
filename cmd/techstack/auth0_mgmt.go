package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// organizationLister resolves the Auth0 organizations a user belongs to. It is
// the last non-default rung of the login-time tenant resolution chain; a nil
// lister simply skips that rung.
type organizationLister interface {
	UserOrganizations(ctx context.Context, userID string) ([]string, error)
}

// auth0MgmtClient is a minimal Auth0 Management API client scoped to the one
// read this service needs (GET /api/v2/users/{id}/organizations). The
// client-credentials token is cached until shortly before expiry.
type auth0MgmtClient struct {
	domain       string
	clientID     string
	clientSecret string
	audience     string
	http         *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// newAuth0MgmtClientFromEnv returns nil when the AUTH0_MGMT_* configuration is
// absent; callers treat nil as "rung unavailable", never as an error.
func newAuth0MgmtClientFromEnv() *auth0MgmtClient {
	domain := strings.TrimSpace(os.Getenv("AUTH0_MGMT_DOMAIN"))
	clientID := strings.TrimSpace(os.Getenv("AUTH0_MGMT_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("AUTH0_MGMT_CLIENT_SECRET"))
	if domain == "" || clientID == "" || clientSecret == "" {
		return nil
	}
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	domain = strings.TrimSuffix(domain, "/")
	audience := strings.TrimSpace(os.Getenv("AUTH0_MGMT_AUDIENCE"))
	if audience == "" {
		audience = "https://" + domain + "/api/v2/"
	}
	return &auth0MgmtClient{
		domain:       domain,
		clientID:     clientID,
		clientSecret: clientSecret,
		audience:     audience,
		http:         &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *auth0MgmtClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"audience":      {c.audience},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.domain+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth0 mgmt token: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.AccessToken) == "" {
		return "", fmt.Errorf("auth0 mgmt token: empty access token")
	}
	c.token = body.AccessToken
	expiresIn := time.Duration(body.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 5 * time.Minute
	}
	c.tokenExp = time.Now().Add(expiresIn - 30*time.Second)
	return c.token, nil
}

// UserOrganizations returns the Auth0 organization ids (org_…) the user is a
// member of, in Auth0's listing order.
func (c *auth0MgmtClient) UserOrganizations(ctx context.Context, userID string) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("auth0 mgmt client not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("auth0 mgmt: user id required")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := "https://" + c.domain + "/api/v2/users/" + url.PathEscape(userID) + "/organizations"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth0 mgmt organizations: unexpected status %d", resp.StatusCode)
	}
	var orgs []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(orgs))
	for _, org := range orgs {
		if id := strings.TrimSpace(org.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
