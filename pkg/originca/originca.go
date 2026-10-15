// Package originca issues and revokes the Cloudflare Origin CA certificate of a
// managed kombify.me install (ADR-0047).
//
// The private key never exists here: the node generates it and sends only a
// CSR. This package therefore only ever handles a CSR, the public certificate
// and the Cloudflare certificate id, and refuses any input that carries key
// material or names a host outside kombify.me. There is no ACME fallback: a
// missing token fails closed with a typed, secret-free error.
package originca

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// TokenEnv names the Cloudflare API token (Zone: SSL and Certificates:
	// Edit on kombify.me) that issues and revokes origin certificates.
	TokenEnv = "CLOUDFLARE_KOMBIFY_ME_ORIGIN_CA_TOKEN"

	defaultBaseURL = "https://api.cloudflare.com/client/v4"
	// ValidityDays is the requested certificate lifetime. Cloudflare sends no
	// expiry notice, so Techstack renews at two thirds of it.
	ValidityDays = 90
	requestType  = "origin-ecc"
)

// TokenMissingError is the fail-closed error of a missing token. It names the
// environment variable and never a value.
type TokenMissingError struct{}

func (*TokenMissingError) Error() string {
	return "managed kombify.me origin certificates require " + TokenEnv + "; no ACME fallback is attempted"
}

// ErrUnavailable reports a rollout without an origin certificate service.
var ErrUnavailable = errors.New("managed kombify.me origin certificate service is not configured (requires " + TokenEnv + " and the encrypted custody database)")

// Config carries the Cloudflare credential and test overrides.
type Config struct {
	APIToken   string
	BaseURL    string
	HTTPClient *http.Client
}

// FromEnv resolves the token. A missing token is a *TokenMissingError.
func FromEnv() (Config, error) {
	token := strings.TrimSpace(os.Getenv(TokenEnv))
	if token == "" {
		return Config{}, &TokenMissingError{}
	}
	return Config{APIToken: token}, nil
}

// Certificate is Cloudflare's answer to an issuance: the public leaf and the
// id that revokes it.
type Certificate struct {
	ID        string
	PEM       string
	Hosts     []string
	ExpiresOn time.Time
}

// Client talks to the Cloudflare Origin CA REST API.
type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, &TokenMissingError{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

var certificateIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Issue has Cloudflare sign csrPEM for hosts. The CSR must be a valid ECDSA
// request whose names, when present, equal hosts; every host must be below
// kombify.me. Anything carrying private key material is refused before any
// request leaves the process.
func (c *Client) Issue(ctx context.Context, hosts []string, csrPEM string) (Certificate, error) {
	normalized, err := NormalizeHosts(hosts)
	if err != nil {
		return Certificate{}, err
	}
	if err := checkCSR(normalized, csrPEM); err != nil {
		return Certificate{}, err
	}
	envelope, status, err := c.do(ctx, http.MethodPost, "/certificates", map[string]interface{}{
		"hostnames": normalized, "requested_validity": ValidityDays, "request_type": requestType, "csr": csrPEM,
	})
	if err != nil {
		return Certificate{}, fmt.Errorf("issue origin certificate: %w", err)
	}
	if !envelope.Success {
		return Certificate{}, fmt.Errorf("issue origin certificate: HTTP %d: %s", status, envelope.errorText())
	}
	var result struct {
		ID          string `json:"id"`
		Certificate string `json:"certificate"`
		ExpiresOn   string `json:"expires_on"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return Certificate{}, fmt.Errorf("parse origin certificate: %w", err)
	}
	if !certificateIDPattern.MatchString(result.ID) || strings.TrimSpace(result.Certificate) == "" {
		return Certificate{}, fmt.Errorf("origin certificate response is missing its id or certificate")
	}
	if ContainsPrivateKey([]byte(result.Certificate)) {
		return Certificate{}, fmt.Errorf("origin certificate response carries private key material")
	}
	block, _ := pem.Decode([]byte(result.Certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return Certificate{}, fmt.Errorf("origin certificate response is not a PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Certificate{}, fmt.Errorf("parse origin certificate: %w", err)
	}
	return Certificate{ID: result.ID, PEM: result.Certificate, Hosts: normalized, ExpiresOn: leaf.NotAfter.UTC()}, nil
}

func checkCSR(hosts []string, csrPEM string) error {
	if ContainsPrivateKey([]byte(csrPEM)) {
		return fmt.Errorf("origin certificate request carries private key material; only a CSR may leave the node")
	}
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("origin certificate request requires a PEM CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return fmt.Errorf("origin certificate CSR is invalid")
	}
	if csr.PublicKeyAlgorithm != x509.ECDSA {
		return fmt.Errorf("origin certificate CSR must be ECDSA")
	}
	names := append([]string(nil), csr.DNSNames...)
	if csr.Subject.CommonName != "" {
		names = append(names, csr.Subject.CommonName)
	}
	for _, name := range names {
		if !ValidHost(strings.ToLower(name)) {
			return fmt.Errorf("origin certificate CSR names %q outside kombify.me", name)
		}
	}
	if len(csr.DNSNames) > 0 && !SameHosts(hosts, csr.DNSNames) {
		return fmt.Errorf("origin certificate CSR differs from the requested hostnames")
	}
	return nil
}

// Revoke deletes the certificate at Cloudflare. A certificate that is already
// gone (HTTP 404 or already revoked) is success, so a retried decommission is
// idempotent; every other failure is returned.
func (c *Client) Revoke(ctx context.Context, certificateID string) error {
	certificateID = strings.TrimSpace(certificateID)
	if certificateID == "" {
		return nil
	}
	if !certificateIDPattern.MatchString(certificateID) {
		return fmt.Errorf("origin certificate id is invalid")
	}
	envelope, status, err := c.do(ctx, http.MethodDelete, "/certificates/"+certificateID, nil)
	if err != nil {
		return fmt.Errorf("revoke origin certificate %s: %w", certificateID, err)
	}
	if envelope.Success || status == http.StatusNotFound || strings.Contains(strings.ToLower(envelope.errorText()), "already revoked") {
		return nil
	}
	return fmt.Errorf("revoke origin certificate %s: HTTP %d: %s", certificateID, status, envelope.errorText())
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (e *cfEnvelope) errorText() string {
	parts := make([]string, 0, len(e.Errors))
	for _, cfErr := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", cfErr.Code, cfErr.Message))
	}
	return strings.Join(parts, "; ")
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*cfEnvelope, int, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport error names the URL, never the Authorization header.
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var envelope cfEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if resp.StatusCode == http.StatusNotFound {
			return &cfEnvelope{}, resp.StatusCode, nil
		}
		return nil, resp.StatusCode, fmt.Errorf("cloudflare %s %s: HTTP %d with non-JSON body", method, path, resp.StatusCode)
	}
	return &envelope, resp.StatusCode, nil
}
