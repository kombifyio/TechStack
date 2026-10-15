package oidcclient

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

	"github.com/kombifyio/techstack/internal/gocommon/internal/httpsecurity"
)

// ErrDiscoveryFailed is returned when `.well-known/openid-configuration` is
// unreachable or malformed.
var ErrDiscoveryFailed = errors.New("oidcclient: discovery failed")

// DiscoveryDocument is the subset of the OIDC discovery metadata kombify
// services consume. The full RFC-defined document has many more fields; we
// only surface what we use.
type DiscoveryDocument struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
	EndSessionEndpoint    string   `json:"end_session_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

// Discover fetches `<issuer>/.well-known/openid-configuration` and returns
// the parsed metadata. Use it on startup to populate
// [ProviderConfig.AuthorizationURL] / [ProviderConfig.TokenURL] /
// [ProviderConfig.JWKSURL] without hard-coding endpoint paths.
//
// The HTTP client defaults to a 5 s timeout when nil.
func Discover(ctx context.Context, issuer string, client *http.Client) (*DiscoveryDocument, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, fmt.Errorf("%w: issuer is required", ErrDiscoveryFailed)
	}
	if err := validateDiscoveryEndpoint("issuer", issuer); err != nil {
		return nil, err
	}
	issuerURL, _ := url.Parse(issuer)
	if issuerURL.RawQuery != "" || issuerURL.ForceQuery {
		return nil, fmt.Errorf("%w: issuer must not contain a query", ErrDiscoveryFailed)
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpsecurity.WithoutRedirects(client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrDiscoveryFailed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	doc := &DiscoveryDocument{}
	if err := json.Unmarshal(body, doc); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrDiscoveryFailed, err)
	}
	if err := validateDiscoveryDocument(doc, issuer); err != nil {
		return nil, err
	}
	return doc, nil
}

func validateDiscoveryDocument(doc *DiscoveryDocument, issuer string) error {
	if doc.Issuer != issuer {
		return fmt.Errorf("%w: response issuer differs from requested issuer", ErrDiscoveryFailed)
	}
	for _, endpoint := range []struct{ name, value string }{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
		{"jwks_uri", doc.JWKSURI},
	} {
		if err := validateDiscoveryEndpoint(endpoint.name, endpoint.value); err != nil {
			return err
		}
	}
	for _, endpoint := range []struct{ name, value string }{
		{"userinfo_endpoint", doc.UserinfoEndpoint},
		{"end_session_endpoint", doc.EndSessionEndpoint},
	} {
		if endpoint.value != "" {
			if err := validateDiscoveryEndpoint(endpoint.name, endpoint.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDiscoveryEndpoint(field, raw string) error {
	if err := validateHTTPSEndpoint(field, raw); err != nil {
		return fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	endpoint, _ := url.Parse(raw)
	if strings.Contains(raw, "#") {
		return fmt.Errorf("%w: %s must not contain a fragment", ErrDiscoveryFailed, field)
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: %s must use HTTPS except for loopback development", ErrDiscoveryFailed, field)
}

// ApplyDiscovery fills the URL fields of cfg from doc when they are unset.
// It is a convenience used after [Discover]:
//
//	doc, _ := oidcclient.Discover(ctx, cfg.Issuer, nil)
//	oidcclient.ApplyDiscovery(&cfg, doc)
//	provider, _ := oidcclient.NewProvider(cfg)
func ApplyDiscovery(cfg *ProviderConfig, doc *DiscoveryDocument) {
	if cfg == nil || doc == nil {
		return
	}
	if strings.TrimSpace(cfg.AuthorizationURL) == "" && doc.AuthorizationEndpoint != "" {
		cfg.AuthorizationURL = doc.AuthorizationEndpoint
	}
	if strings.TrimSpace(cfg.TokenURL) == "" && doc.TokenEndpoint != "" {
		cfg.TokenURL = doc.TokenEndpoint
	}
	if strings.TrimSpace(cfg.JWKSURL) == "" && doc.JWKSURI != "" {
		cfg.JWKSURL = doc.JWKSURI
	}
}
