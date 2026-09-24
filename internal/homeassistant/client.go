// Package homeassistant implements the Home Assistant API boundary. Reading an
// instance never grants mutation authority, nor infers its installation method.
package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Observation struct {
	CoreVersion  string          `json:"core_version"`
	TimeZone     string          `json:"time_zone"`
	Components   []string        `json:"components"`
	ObservedAt   time.Time       `json:"observed_at"`
	Capabilities map[string]bool `json:"capabilities"`
}

type Client struct {
	endpoint *url.URL
	token    string
	http     *http.Client
}

var ErrUnavailable = errors.New("Home Assistant connection unavailable")
var ErrUnauthorized = errors.New("Home Assistant authorization denied")

// NewLocalClient is exclusively for the owner-authorized local executor. HTTP
// is accepted only on private addresses. DNS answers are pinned for the lifetime
// of the client and redirects are refused, including same-origin redirects.
func NewLocalClient(ctx context.Context, endpoint, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Home Assistant requires an HTTP(S) origin without credentials, path, query or fragment")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Home Assistant token required")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return nil, errors.New("Home Assistant address resolution failed")
	}
	for _, ip := range ips {
		if ip.IP.IsUnspecified() || ip.IP.IsMulticast() || ip.IP.IsLinkLocalUnicast() || (u.Scheme == "http" && !ip.IP.IsPrivate() && !ip.IP.IsLoopback()) {
			return nil, errors.New("Home Assistant address is not permitted for local access")
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	u.Path = ""
	return &Client{endpoint: u, token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close()           { c.http.CloseIdleConnections() }
func (c *Client) Endpoint() string { return c.endpoint.String() }

// Observe uses the native authenticated read boundary. Components only report
// loaded integrations; they do not prove a connector session or any write grant.
func (c *Client) Observe(ctx context.Context) (Observation, error) {
	var config struct {
		Version    string   `json:"version"`
		TimeZone   string   `json:"time_zone"`
		Components []string `json:"components"`
	}
	if err := c.json(ctx, http.MethodGet, "/api/config", nil, &config); err != nil {
		return Observation{}, err
	}
	if strings.TrimSpace(config.Version) == "" {
		return Observation{}, errors.New("Home Assistant did not return a Core version")
	}
	caps := map[string]bool{"core_rest_read": true, "mcp_integration_loaded": false, "supervisor_authorized": false, "backup_restore_authorized": false}
	for _, component := range config.Components {
		if component == "mcp_server" {
			caps["mcp_integration_loaded"] = true
		}
	}
	return Observation{CoreVersion: config.Version, TimeZone: config.TimeZone, Components: config.Components, ObservedAt: time.Now().UTC(), Capabilities: caps}, nil
}

// EntityIDs retains the identity inventory without persisting entity states,
// attributes, location data or configuration secrets in a workflow receipt.
func (c *Client) EntityIDs(ctx context.Context) ([]string, error) {
	var states []struct {
		ID string `json:"entity_id"`
	}
	if err := c.json(ctx, http.MethodGet, "/api/states", nil, &states); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(states))
	for _, state := range states {
		if state.ID == "" {
			return nil, errors.New("Home Assistant returned an empty entity identity")
		}
		ids = append(ids, state.ID)
	}
	return ids, nil
}

func (c *Client) json(ctx context.Context, method, path string, body io.Reader, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint.String()+path, body)
	if err != nil {
		return errors.New("invalid Home Assistant request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return ErrUnauthorized
		}
		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return ErrUnavailable
		}
		return fmt.Errorf("Home Assistant returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 {
		return errors.New("invalid Home Assistant response size")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("invalid Home Assistant JSON response")
	}
	return nil
}
