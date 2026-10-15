package guardreach

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

const (
	// DefaultGatewayURL is the public Gateway origin; the registration API is
	// served there for the Techstack control plane only.
	DefaultGatewayURL = "https://api.kombify.io"

	gatewayNodesPath       = "/v1/internal/connect/techstack-nodes"
	gatewayRevokePath      = gatewayNodesPath + "/revoke"
	gatewayRevocationsPath = gatewayNodesPath + "/revocations"
	gatewayWriteScope      = "connect:techstack-nodes:write"
	gatewayReadScope       = "connect:techstack-nodes:read"
	gatewayReasonPrefix    = "connect_techstack_node_"
	gatewayMaxBody         = 64 << 10

	// gatewayTokenTTL stays under the 60 second ceiling Gateway enforces on
	// these routes.
	gatewayTokenTTL = 45 * time.Second
)

// Gateway is the Connect registration client of the Techstack control plane.
// Every call carries its own HS256 service token (caller techstack, audience
// kombify-gateway) signed with the Techstack-only Connect secret, never the
// shared service-auth secret: the exact scope of the operation, the owner as
// on_behalf_of, a lifetime under 60 seconds, a random one-use jti and a
// request_binding over the exact method, request target and body bytes sent.
type Gateway struct {
	base   string
	secret string
	http   *http.Client
}

// GatewayConfig configures a Gateway client. Secret is the dedicated Techstack
// secret for the Connect techstack-nodes routes; there is no fallback to the
// shared SERVICE_AUTH_SECRET.
type GatewayConfig struct {
	BaseURL    string
	Secret     string
	HTTPClient *http.Client
}

// NewGateway validates the configuration. The base URL must be HTTPS outside
// loopback, like every other Gateway publisher of the control plane.
func NewGateway(cfg GatewayConfig) (*Gateway, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultGatewayURL
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://127.0.0.1:") && !strings.HasPrefix(base, "http://localhost:") {
		return nil, errors.New("guardreach: Gateway URL must use HTTPS outside localhost")
	}
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return nil, errors.New("guardreach: the dedicated Gateway Connect service secret is required for Connect registration")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Gateway{base: base, secret: secret, http: httpClient}, nil
}

// gatewayClaims is the service-token payload Gateway verifies on the Connect
// routes. servicecall.Claims has no jti and its bound-token issuer refuses an
// owner identity, so the token is built here with the same wire shape.
type gatewayClaims struct {
	Iss            string                     `json:"iss"`
	Aud            string                     `json:"aud"`
	Iat            int64                      `json:"iat"`
	Exp            int64                      `json:"exp"`
	Svc            string                     `json:"svc"`
	Scope          string                     `json:"scope"`
	OnBehalfOf     *servicecall.OnBehalfOf    `json:"on_behalf_of,omitempty"`
	JTI            string                     `json:"jti"`
	RequestBinding servicecall.RequestBinding `json:"request_binding"`
}

// mintToken signs one token for exactly this request.
func (g *Gateway) mintToken(scope string, obo *servicecall.OnBehalfOf, binding servicecall.RequestBinding, now time.Time) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("mint Gateway token id: %w", err)
	}
	payload, err := json.Marshal(gatewayClaims{
		Iss: "kombify-techstack", Aud: "kombify-gateway", Iat: now.Unix(), Exp: now.Add(gatewayTokenTTL).Unix(),
		Svc: "techstack", Scope: scope, OnBehalfOf: obo, JTI: base64.RawURLEncoding.EncodeToString(id), RequestBinding: binding,
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// GatewayError is a structured Gateway denial. It carries the stable reason
// code only, never the response body.
type GatewayError struct {
	Status     int
	ReasonCode string
	Retryable  bool
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("Gateway answered HTTP %d (%s)", e.Status, firstNonEmpty(e.ReasonCode, "no reason code"))
}

// Node is the Connect techstack-node Gateway recorded.
type Node struct {
	NodeID            string
	Status            string
	BindingGeneration int64
	Created           bool
	RevokedNow        bool
}

// NodeUpsert registers or refreshes one server for its owner.
type NodeUpsert struct {
	OwnerSubject, ConnectTenantID string
	Roles                         []string
	HostProof                     string
	KeyThumbprint                 string
	DisplayName, StackID          string
	Placement                     string
}

type nodeBody struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
	Guard  struct {
		BindingGeneration int64 `json:"binding_generation"`
	} `json:"guard"`
	RevokedNow bool `json:"revoked_now"`
}

func (g *Gateway) obo(owner, connectTenant string, roles []string) *servicecall.OnBehalfOf {
	return &servicecall.OnBehalfOf{Sub: owner, OrgID: connectTenant, Roles: roles}
}

// PutNode upserts the techstack-node of one server. Gateway verifies the host
// proof itself; a server whose proof does not verify is never recorded.
func (g *Gateway) PutNode(ctx context.Context, in NodeUpsert) (*Node, error) {
	body, err := json.Marshal(map[string]string{
		"host_proof": in.HostProof, "guard_key_thumbprint": in.KeyThumbprint,
		"display_name": in.DisplayName, "stack_id": in.StackID, "placement": in.Placement,
	})
	if err != nil {
		return nil, err
	}
	var parsed nodeBody
	status, err := g.do(ctx, gatewayWriteScope, http.MethodPut, gatewayNodesPath, body, g.obo(in.OwnerSubject, in.ConnectTenantID, in.Roles), &parsed)
	if err != nil {
		return nil, err
	}
	return &Node{NodeID: parsed.NodeID, Status: parsed.Status, BindingGeneration: parsed.Guard.BindingGeneration, Created: status == http.StatusCreated}, nil
}

// RevokeNode detaches one server. Revoking a revoked node answers with
// RevokedNow false, and a server Gateway never registered is reported as
// (nil, nil): there is nothing to revoke.
func (g *Gateway) RevokeNode(ctx context.Context, owner, connectTenant string, roles []string, serverID, reason string) (*Node, error) {
	body, err := json.Marshal(map[string]string{"server_id": serverID, "reason": reason})
	if err != nil {
		return nil, err
	}
	var parsed nodeBody
	if _, err := g.do(ctx, gatewayWriteScope, http.MethodPost, gatewayRevokePath, body, g.obo(owner, connectTenant, roles), &parsed); err != nil {
		var denial *GatewayError
		if errors.As(err, &denial) && denial.Status == http.StatusNotFound && denial.ReasonCode == "node_not_found" {
			return nil, nil
		}
		return nil, err
	}
	return &Node{NodeID: parsed.NodeID, Status: parsed.Status, BindingGeneration: parsed.Guard.BindingGeneration, RevokedNow: parsed.RevokedNow}, nil
}

// Revocation is one event of the Gateway revocation log.
type Revocation struct {
	Seq               int64  `json:"seq"`
	NodeID            string `json:"node_id"`
	TenantID          string `json:"tenant_id"`
	ServerID          string `json:"server_id"`
	WorkerID          string `json:"worker_id"`
	KeyThumbprint     string `json:"guard_key_thumbprint"`
	BindingGeneration int64  `json:"binding_generation"`
	Reason            string `json:"reason"`
	RevokedBy         string `json:"revoked_by"`
}

// RevocationPage is one page of the cursor.
type RevocationPage struct {
	Events     []Revocation `json:"events"`
	NextCursor int64        `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}

// Revocations reads the log after a cursor with the control plane's machine
// token (no on_behalf_of), so it sees every tenant's events.
func (g *Gateway) Revocations(ctx context.Context, after int64, limit int) (*RevocationPage, error) {
	query := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
	var page RevocationPage
	if _, err := g.do(ctx, gatewayReadScope, http.MethodGet, gatewayRevocationsPath+"?"+query.Encode(), nil, nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func (g *Gateway) do(ctx context.Context, scope, method, path string, body []byte, obo *servicecall.OnBehalfOf, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The binding covers the exact bytes and request target of this call; a
	// nil body hashes as the empty body Gateway reads for a GET.
	binding, err := servicecall.NewRequestBinding(method, req.URL.RequestURI(), body)
	if err != nil {
		return 0, fmt.Errorf("bind Gateway request: %w", err)
	}
	token, err := g.mintToken(scope, obo, binding, time.Now())
	if err != nil {
		return 0, err
	}
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	response, err := g.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Gateway request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, gatewayMaxBody+1))
	if readErr != nil {
		return response.StatusCode, fmt.Errorf("read Gateway response: %w", readErr)
	}
	if len(data) > gatewayMaxBody {
		return response.StatusCode, errors.New("Gateway response exceeds bounded envelope")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, parseGatewayDenial(response.StatusCode, data)
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return response.StatusCode, errors.New("Gateway answered with an unreadable body")
	}
	return response.StatusCode, nil
}

func parseGatewayDenial(status int, body []byte) *GatewayError {
	denial := &GatewayError{Status: status, Retryable: status >= 500 || status == http.StatusTooManyRequests}
	var envelope struct {
		ReasonCode string `json:"reason_code"`
		Retryable  *bool  `json:"retryable"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		denial.ReasonCode = strings.TrimPrefix(envelope.ReasonCode, gatewayReasonPrefix)
		if envelope.Retryable != nil {
			denial.Retryable = *envelope.Retryable
		}
	}
	return denial
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
