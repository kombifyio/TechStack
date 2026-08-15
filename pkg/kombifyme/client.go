// Package kombifyme provides read/delete access to the kombify.me registry for
// TechStack diagnostics and legacy cleanup. StackKits own registration and
// service exposure.
package kombifyme

import (
	"bytes"
	"context"
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
	DefaultDirectAPIBase = "https://kombify.me/_kombify/api/v1"
	DefaultCloudAPIBase  = "https://kombify.io/api/v1/kombify-me"

	forwardMaxAttempts = 4
	forwardBaseDelay   = 2 * time.Second
	forwardMaxDelay    = 60 * time.Second
)

type Mode string

const (
	ModeDirect Mode = "direct"
	ModeCloud  Mode = "cloud"
)

type Config struct {
	DirectAPIBase string
	CloudAPIBase  string
	APIKey        string
	HTTPClient    *http.Client
}

type Client struct {
	cfg        Config
	httpClient *http.Client
}

type UpstreamResponse struct {
	Status int
	Body   any
}

type UpstreamError struct {
	Status int
	Body   any
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("kombify.me upstream returned %d", e.Status)
}

func DefaultConfigFromEnv() Config {
	return Config{
		DirectAPIBase: firstNonEmpty(os.Getenv("KOMBIFY_ME_API_BASE"), DefaultDirectAPIBase),
		CloudAPIBase:  firstNonEmpty(os.Getenv("KOMBIFY_CLOUD_KOMBIFY_ME_API_BASE"), os.Getenv("KOMBIFY_CLOUD_API_BASE"), DefaultCloudAPIBase),
		APIKey:        firstNonEmpty(os.Getenv("KOMBIFY_ME_API_KEY"), os.Getenv("KOMBIFY_API_KEY")),
	}
}

func NewClient(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.DirectAPIBase == "" {
		cfg.DirectAPIBase = DefaultDirectAPIBase
	}
	if cfg.CloudAPIBase == "" {
		cfg.CloudAPIBase = DefaultCloudAPIBase
	}
	return &Client{cfg: cfg, httpClient: httpClient}
}

func (c *Client) Forward(ctx context.Context, method, upstreamPath string, payload any, incomingAuthorization string) (*UpstreamResponse, error) {
	mode, base, authorization, err := c.resolveUpstream(incomingAuthorization)
	if err != nil {
		return nil, err
	}

	var encodedBody []byte
	if payload != nil {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode kombify.me payload: %w", marshalErr)
		}
		encodedBody = encoded
	}

	targetURL := joinURL(base, upstreamPath)
	for attempt := 1; attempt <= forwardMaxAttempts; attempt++ {
		var body io.Reader
		if encodedBody != nil {
			body = bytes.NewReader(encodedBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if mode == ModeDirect && c.cfg.APIKey != "" {
			req.Header.Set("X-Kombify-API-Key", c.cfg.APIKey)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		respBody, decodeErr := decodeJSONResponse(resp.Body)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if resp.StatusCode >= 400 {
			if shouldRetryForward(resp.StatusCode) && attempt < forwardMaxAttempts {
				if err := sleepForForwardRetry(ctx, attempt, resp.StatusCode, resp.Header.Get("Retry-After")); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &UpstreamError{Status: resp.StatusCode, Body: respBody}
		}

		return &UpstreamResponse{Status: resp.StatusCode, Body: respBody}, nil
	}

	return nil, fmt.Errorf("kombify.me forwarding exhausted retries")
}

func shouldRetryForward(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func sleepForForwardRetry(ctx context.Context, attempt int, status int, retryAfter string) error {
	delay, explicit := retryAfterDelay(retryAfter)
	if !explicit {
		if status == http.StatusTooManyRequests {
			delay = forwardMaxDelay
		} else {
			delay = forwardBaseDelay * time.Duration(1<<uint(attempt-1))
		}
	}
	if delay < 0 {
		delay = forwardBaseDelay * time.Duration(1<<uint(attempt-1))
	}
	if delay > forwardMaxDelay {
		delay = forwardMaxDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfterDelay(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		return time.Until(when), true
	}
	return 0, false
}

func (c *Client) resolveUpstream(incomingAuthorization string) (Mode, string, string, error) {
	if c.cfg.APIKey != "" {
		return ModeDirect, c.cfg.DirectAPIBase, "", nil
	}

	if strings.HasPrefix(incomingAuthorization, "Bearer ") {
		return ModeCloud, c.cfg.CloudAPIBase, incomingAuthorization, nil
	}

	return "", "", "", errors.New("kombify.me registry access requires KOMBIFY_ME_API_KEY or incoming Cloud authorization")
}

func decodeJSONResponse(body io.Reader) (any, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return string(data), nil
	}
	return value, nil
}

func joinURL(base, path string) string {
	parsed, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	}
	relative := &url.URL{Path: strings.TrimLeft(path, "/")}
	return parsed.ResolveReference(relative).String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
