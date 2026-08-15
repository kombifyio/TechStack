// Package notifications proxies a Techstack user's notification center to the
// kombify-Notifications engine. Techstack's SvelteKit layer has no server-side
// identity, so the Go backend resolves the authenticated user and signs the
// engine call itself (HS256 service JWT, iss kombify-techstack, aud
// kombify-notifications, scope notifications:*), forwarding the recipient as the
// auth0_user_id query param the engine expects (desk parity).
package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEngineURL = "https://kombify-notifications.onrender.com"
	issuer           = "kombify-techstack"
	audience         = "kombify-notifications"
	tokenTTL         = 5 * time.Minute
	requestTimeout   = 12 * time.Second
	maxResponseBytes = 1 << 20
)

// ErrNotConfigured is returned when SERVICE_AUTH_SECRET is unset, so the engine
// call cannot be signed. Handlers map it to 503.
var ErrNotConfigured = errors.New("notifications engine not configured")

// Engine is a thin, signed HTTP client for the notifications engine.
type Engine struct {
	baseURL string
	secret  string
	http    *http.Client
}

// NewEngineFromEnv builds an Engine from NOTIFICATIONS_ENGINE_URL (optional,
// defaults to the live origin) and SERVICE_AUTH_SECRET (required to sign).
func NewEngineFromEnv() *Engine {
	base := strings.TrimSpace(os.Getenv("NOTIFICATIONS_ENGINE_URL"))
	if base == "" {
		base = defaultEngineURL
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1")
	return &Engine{
		baseURL: base,
		secret:  strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET")),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Configured reports whether a signing secret is present.
func (e *Engine) Configured() bool { return e != nil && e.secret != "" }

// Result is a passthrough of the engine's HTTP response.
type Result struct {
	Status int
	Body   []byte
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signToken mints an HS256 service JWT the engine accepts on the Authorization
// header: iss/aud in its allowlists, exp required, scope enforced per route.
func (e *Engine) signToken(scope string) (string, error) {
	if e.secret == "" {
		return "", ErrNotConfigured
	}
	now := time.Now()
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iss":   issuer,
		"aud":   audience,
		"iat":   now.Unix(),
		"exp":   now.Add(tokenTTL).Unix(),
		"svc":   "techstack",
		"scope": scope,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + b64(claims)
	mac := hmac.New(sha256.New, []byte(e.secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + b64(mac.Sum(nil)), nil
}

func (e *Engine) do(ctx context.Context, method, path, scope string, body []byte) (*Result, error) {
	if !e.Configured() {
		return nil, ErrNotConfigured
	}
	token, err := e.signToken(scope)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	return &Result{Status: resp.StatusCode, Body: respBody}, nil
}

func feedQuery(auth0UserID string, extra url.Values) string {
	v := url.Values{}
	v.Set("auth0_user_id", auth0UserID)
	for k, vals := range extra {
		for _, val := range vals {
			v.Add(k, val)
		}
	}
	return v.Encode()
}

// Feed returns the recipient's undismissed feed (newest first).
func (e *Engine) Feed(ctx context.Context, auth0UserID string, limit int) (*Result, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	q := feedQuery(auth0UserID, url.Values{"limit": []string{strconv.Itoa(limit)}})
	return e.do(ctx, http.MethodGet, "/v1/notifications/feed?"+q, "notifications:read", nil)
}

// MarkRead marks a feed item read.
func (e *Engine) MarkRead(ctx context.Context, auth0UserID, itemID string) (*Result, error) {
	path := "/v1/notifications/feed/" + url.PathEscape(itemID) + "/read?" + feedQuery(auth0UserID, nil)
	return e.do(ctx, http.MethodPost, path, "notifications:write", nil)
}

// Dismiss dismisses a feed item.
func (e *Engine) Dismiss(ctx context.Context, auth0UserID, itemID string) (*Result, error) {
	path := "/v1/notifications/feed/" + url.PathEscape(itemID) + "/dismiss?" + feedQuery(auth0UserID, nil)
	return e.do(ctx, http.MethodPost, path, "notifications:write", nil)
}

// MarkAllRead marks all of the recipient's items read.
func (e *Engine) MarkAllRead(ctx context.Context, auth0UserID string) (*Result, error) {
	return e.do(ctx, http.MethodPost, "/v1/notifications/feed/read-all?"+feedQuery(auth0UserID, nil), "notifications:write", nil)
}

// GetPreferences returns the recipient's topic-by-channel preference matrix.
func (e *Engine) GetPreferences(ctx context.Context, auth0UserID string) (*Result, error) {
	return e.do(ctx, http.MethodGet, "/v1/notifications/preferences?"+feedQuery(auth0UserID, nil), "notifications:read", nil)
}

// PutPreferences persists a preference matrix (locked channels are ignored engine-side).
func (e *Engine) PutPreferences(ctx context.Context, auth0UserID string, matrix []byte) (*Result, error) {
	return e.do(ctx, http.MethodPut, "/v1/notifications/preferences?"+feedQuery(auth0UserID, nil), "notifications:write", matrix)
}

// DispatchProduct sends one idempotent in-app product notification. It is the
// dispatch boundary used by Techstack's durable outbox worker.
func (e *Engine) DispatchProduct(ctx context.Context, event ProductEvent) (*DispatchResult, error) {
	channel := strings.TrimSpace(event.Channel)
	if channel == "" {
		channel = "in_app"
	}
	body, err := json.Marshal(map[string]any{
		"organization_id": nullableString(event.OrganizationID),
		"topic_slug":      event.Topic,
		"channel":         channel,
		"recipient":       map[string]string{"auth0_user_id": event.Auth0UserID},
		"payload":         event.Payload,
		"idempotency_key": event.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	result, err := e.do(ctx, http.MethodPost, "/v1/notifications/dispatch", "notifications:dispatch", body)
	if err != nil {
		return nil, err
	}
	if result.Status < 200 || result.Status >= 300 {
		var envelope struct {
			Error     string `json:"error"`
			ErrorCode string `json:"error_code"`
			Retryable bool   `json:"retryable"`
		}
		_ = json.Unmarshal(result.Body, &envelope)
		code := envelope.ErrorCode
		if code == "" {
			code = envelope.Error
		}
		retryable := envelope.Retryable || result.Status == http.StatusTooManyRequests || result.Status >= 500
		return nil, &DispatchError{StatusCode: result.Status, Code: code, Retryable: retryable}
	}
	var out DispatchResult
	if err := json.Unmarshal(result.Body, &out); err != nil {
		return nil, fmt.Errorf("notifications: decode dispatch response: %w", err)
	}
	return &out, nil
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
