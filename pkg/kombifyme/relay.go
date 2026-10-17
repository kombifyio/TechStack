package kombifyme

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultRelayURL  = "wss://kombify.me/_kombify/api/v1/tunnel/connect"
	relayMaxBodySize = 10 * 1024 * 1024
	relayHTTPTimeout = 30 * time.Second
)

type RelayConfig struct {
	URL     string
	APIKey  string
	AgentID string
	Version string
	Logger  *slog.Logger
	Dialer  *websocket.Dialer
	// ProofProvider switches the relay to a kombify Guard host proof: it is
	// called once per connection and must return a fresh, single-use proof
	// (Authorization: KombifyGuard <proof>). No static key is involved; the
	// proof names the server, so neither APIKey nor AgentID is needed.
	ProofProvider func(context.Context) (string, error)
	// OnConnected, when set, runs once per session after the relay accepted the
	// connection.
	OnConnected func()
	// LoopbackOnly pins every relayed request to this host's loopback
	// interface. Guard reach publishes services of the Guard host only, so a
	// relay-supplied target_addr naming anything else (RFC1918, another
	// machine, a public host) is refused instead of dialed.
	LoopbackOnly bool
}

type Relay struct {
	cfg        RelayConfig
	dialer     *websocket.Dialer
	httpClient *http.Client
	log        *slog.Logger
	writeMu    sync.Mutex
}

type RelaySessionError struct {
	Err          error
	ConnectedFor time.Duration
	// Denial is set when the relay refused the connection itself (HTTP status
	// before the WebSocket upgrade); nil for network and session failures.
	Denial *RelayDenial
}

// RelayDenial is the relay's structured refusal of a connection.
type RelayDenial struct {
	Status     int
	ReasonCode string
	Retryable  bool
}

// Permanent reports whether retrying the same credential cannot succeed until
// the control plane changes the binding: the relay does not know this server
// or key, the binding was revoked, or the proof was refused as malformed or
// mis-signed. A caller must stop and report instead of reconnecting in a loop.
func (d *RelayDenial) Permanent() bool { return d != nil && !d.Retryable }

// AsRelayDenial extracts the relay's refusal from a session error.
func AsRelayDenial(err error) *RelayDenial {
	var sessionErr *RelaySessionError
	if errors.As(err, &sessionErr) {
		return sessionErr.Denial
	}
	return nil
}

func (e *RelaySessionError) Error() string { return e.Err.Error() }
func (e *RelaySessionError) Unwrap() error { return e.Err }

type relayMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type relayHTTPRequest struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body,omitempty"`
	Host       string            `json:"host"`
	TargetAddr string            `json:"target_addr"`
}

type relayHTTPResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body,omitempty"`
}

func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.URL == "" {
		cfg.URL = DefaultRelayURL
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "wss" && parsed.Scheme != "ws") || parsed.Host == "" {
		return nil, errors.New("kombify.me relay URL must be a ws:// or wss:// URL")
	}
	if cfg.ProofProvider == nil {
		if !strings.HasPrefix(cfg.APIKey, "kbi_") {
			return nil, errors.New("kombify.me relay requires a kbi_ API key")
		}
		if cfg.AgentID == "" {
			return nil, errors.New("kombify.me relay requires an agent ID")
		}
	}
	if cfg.Version == "" {
		cfg.Version = "unknown"
	}

	dialer := cfg.Dialer
	if dialer == nil {
		copy := *websocket.DefaultDialer
		dialer = &copy
	}
	transport := relayBaseTransport()
	transport.Proxy = nil
	transport.DialContext = dialRelayTarget
	if cfg.LoopbackOnly {
		transport.DialContext = dialRelayLoopbackTarget
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   relayHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Relay{cfg: cfg, dialer: dialer, httpClient: httpClient, log: logger}, nil
}

func (r *Relay) RunSession(ctx context.Context) error {
	conn, relayURL, err := r.connect(ctx)
	if err != nil {
		return err
	}
	connectedAt := time.Now()
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			r.log.Debug("kombify_me_relay_close_failed", "error", closeErr.Error())
		}
	}()
	conn.SetReadLimit(relayMaxBodySize*2 + 64*1024)
	r.log.Info("kombify_me_relay_connected", "url", relayURL.Redacted(), "agent_id", r.cfg.AgentID)
	if r.cfg.OnConnected != nil {
		r.cfg.OnConnected()
	}

	stopWorkers := r.startSessionWorkers(ctx, conn)
	defer stopWorkers()
	return r.readSessionMessages(ctx, conn, connectedAt)
}

func (r *Relay) connect(ctx context.Context) (*websocket.Conn, *url.URL, error) {
	relayURL, err := url.Parse(r.cfg.URL)
	if err != nil {
		return nil, nil, &RelaySessionError{Err: err}
	}
	query := relayURL.Query()
	headers := http.Header{}
	if r.cfg.ProofProvider != nil {
		proof, proofErr := r.cfg.ProofProvider(ctx)
		if proofErr != nil {
			return nil, nil, &RelaySessionError{Err: fmt.Errorf("mint relay host proof: %w", proofErr)}
		}
		headers.Set("Authorization", "KombifyGuard "+proof)
	} else {
		query.Set("agent_id", r.cfg.AgentID)
		headers.Set("X-Kombify-API-Key", r.cfg.APIKey)
	}
	query.Set("version", r.cfg.Version)
	relayURL.RawQuery = query.Encode()

	conn, response, err := r.dialer.DialContext(ctx, relayURL.String(), headers)
	if err != nil {
		if response != nil {
			defer func() { _ = response.Body.Close() }()
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return nil, nil, &RelaySessionError{
				Err:    fmt.Errorf("relay connect returned %d: %s", response.StatusCode, strings.TrimSpace(string(body))),
				Denial: parseRelayDenial(response.StatusCode, body),
			}
		}
		return nil, nil, &RelaySessionError{Err: fmt.Errorf("relay connect: %w", err)}
	}
	return conn, relayURL, nil
}

// parseRelayDenial reads the platform reason envelope of a refused connect. A
// refusal without an envelope is retryable only for server errors, so a proxy
// or an outage never reads as a permanent revocation.
func parseRelayDenial(status int, body []byte) *RelayDenial {
	denial := &RelayDenial{Status: status, Retryable: status >= 500 || status == http.StatusTooManyRequests}
	var envelope struct {
		ReasonCode string `json:"reason_code"`
		Retryable  *bool  `json:"retryable"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		denial.ReasonCode = envelope.ReasonCode
		if envelope.Retryable != nil {
			denial.Retryable = *envelope.Retryable
		}
	}
	return denial
}

func (r *Relay) startSessionWorkers(ctx context.Context, conn *websocket.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.writeMu.Lock()
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "agent shutdown"), time.Now().Add(time.Second))
			r.writeMu.Unlock()
			_ = conn.Close()
		case <-done:
		}
	}()

	ping := time.NewTicker(20 * time.Second)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ping.C:
				_ = r.writeJSON(conn, map[string]string{"type": "ping"})
			}
		}
	}()
	return func() {
		ping.Stop()
		close(done)
	}
}

func (r *Relay) readSessionMessages(ctx context.Context, conn *websocket.Conn, connectedAt time.Time) error {
	for {
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &RelaySessionError{Err: fmt.Errorf("relay read: %w", readErr), ConnectedFor: time.Since(connectedAt)}
		}
		var message relayMessage
		if err := json.Unmarshal(data, &message); err != nil {
			r.log.Warn("kombify_me_relay_invalid_message", "error", err.Error())
			continue
		}
		switch message.Type {
		case "handshake_ack":
			r.log.Info("kombify_me_relay_ready")
		case "pong":
			continue
		case "http_request":
			go r.handleHTTPRequest(ctx, conn, message)
		}
	}
}

func (r *Relay) handleHTTPRequest(parent context.Context, conn *websocket.Conn, message relayMessage) {
	response := relayHTTPResponse{StatusCode: http.StatusBadGateway, Headers: map[string]string{"content-type": "application/json"}}
	var payload relayHTTPRequest
	if message.RequestID == "" || json.Unmarshal(message.Payload, &payload) != nil {
		response.Body = encodeRelayBody([]byte(`{"error":"invalid relay request"}`))
		r.writeHTTPResponse(conn, message.RequestID, response)
		return
	}

	status, headers, body, err := r.forwardHTTPRequest(parent, payload)
	if err != nil {
		// Guard threat-model F10: never echo internal error text to the peer.
		r.log.Warn("kombify_me_relay_forward_failed", "request_id", message.RequestID, "error", err.Error())
		response.Body = encodeRelayBody([]byte(`{"error":"relay forward failed"}`))
	} else {
		response.StatusCode = status
		response.Headers = headers
		response.Body = encodeRelayBody(body)
	}
	r.writeHTTPResponse(conn, message.RequestID, response)
}

func (r *Relay) forwardHTTPRequest(parent context.Context, payload relayHTTPRequest) (int, map[string]string, []byte, error) {
	if r.cfg.LoopbackOnly {
		if err := requireLoopbackTarget(payload.TargetAddr); err != nil {
			return 0, nil, nil, err
		}
	}
	req, cancel, err := buildRelayTargetRequest(parent, payload)
	if err != nil {
		return 0, nil, nil, err
	}
	defer cancel()

	// This network request is the relay's intended product boundary. The URL
	// has passed resolveRelayTarget, and the non-overridable transport resolves
	// and rechecks every dial in dialRelayTarget to deny metadata/link-local
	// destinations even after DNS rebinding. Redirects and proxy inheritance
	// are disabled in NewRelay.
	res, err := r.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("target request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	return readRelayTargetResponse(res)
}

func buildRelayTargetRequest(parent context.Context, payload relayHTTPRequest) (*http.Request, context.CancelFunc, error) {
	if !isAllowedRelayMethod(payload.Method) {
		return nil, nil, errors.New("relay request method is not supported")
	}
	target, err := resolveRelayTarget(payload.TargetAddr, payload.Path)
	if err != nil {
		return nil, nil, err
	}
	body, err := decodeRelayBody(payload.Body)
	if err != nil || len(body) > relayMaxBodySize {
		return nil, nil, errors.New("invalid relay request body")
	}

	ctx, cancel := context.WithTimeout(parent, relayHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, payload.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("build target request: %w", err)
	}
	copyRelayRequestHeaders(req, payload)
	return req, cancel, nil
}

func copyRelayRequestHeaders(req *http.Request, payload relayHTTPRequest) {
	for name, value := range payload.Headers {
		if isHopByHopHeader(name) || strings.EqualFold(name, "host") || strings.HasPrefix(strings.ToLower(name), "x-kombify-") {
			continue
		}
		req.Header.Set(name, value)
	}
	if payload.Host != "" {
		req.Header.Set("X-Forwarded-Host", payload.Host)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
}

func readRelayTargetResponse(res *http.Response) (int, map[string]string, []byte, error) {
	limited := io.LimitReader(res.Body, relayMaxBodySize+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read target response: %w", err)
	}
	if len(responseBody) > relayMaxBodySize {
		return 0, nil, nil, errors.New("target response body exceeds relay limit")
	}

	responseHeaders := make(map[string]string)
	for name, values := range res.Header {
		if isHopByHopHeader(name) {
			continue
		}
		responseHeaders[name] = strings.Join(values, ", ")
	}
	return res.StatusCode, responseHeaders, responseBody, nil
}

func isAllowedRelayMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func (r *Relay) writeHTTPResponse(conn *websocket.Conn, requestID string, response relayHTTPResponse) {
	if err := r.writeJSON(conn, map[string]any{
		"type":       "http_response",
		"request_id": requestID,
		"payload":    response,
	}); err != nil {
		r.log.Warn("kombify_me_relay_response_failed", "request_id", requestID, "error", err.Error())
	}
}

func (r *Relay) writeJSON(conn *websocket.Conn, value any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func resolveRelayTarget(baseAddress, requestPath string) (*url.URL, error) {
	base, err := url.Parse(baseAddress)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return nil, errors.New("invalid authorized tunnel target")
	}
	if isMetadataHost(base.Hostname()) {
		return nil, errors.New("cloud metadata targets are denied")
	}
	relative, err := url.ParseRequestURI(requestPath)
	if err != nil || !strings.HasPrefix(relative.Path, "/") || relative.IsAbs() || relative.Host != "" {
		return nil, errors.New("invalid relay request path")
	}
	base.Path = strings.TrimRight(base.Path, "/") + relative.Path
	base.RawPath = ""
	base.RawQuery = relative.RawQuery
	return base, nil
}

var errRelayTargetNotLoopback = errors.New("relay target is not on this host's loopback interface")

// requireLoopbackTarget refuses a relay target whose host is not a loopback
// literal or localhost. The dial-time check repeats it on resolved addresses.
func requireLoopbackTarget(baseAddress string) error {
	base, err := url.Parse(baseAddress)
	if err != nil || base.Hostname() == "" {
		return errors.New("invalid authorized tunnel target")
	}
	host := strings.ToLower(strings.TrimSuffix(base.Hostname(), "."))
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return errRelayTargetNotLoopback
}

func dialRelayLoopbackTarget(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: relayHTTPTimeout, KeepAlive: 30 * time.Second}
	var lastErr error = errors.New("tunnel target resolved to no addresses")
	for _, resolved := range addresses {
		if !resolved.IP.IsLoopback() {
			return nil, errRelayTargetNotLoopback
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func isMetadataHost(host string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(host, "."))
	if normalized == "metadata" || normalized == "instance-data" || normalized == "metadata.google.internal" {
		return true
	}
	ip := net.ParseIP(normalized)
	return isMetadataIP(ip)
}

func isMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.Equal(net.ParseIP("169.254.169.254")) ||
		ip.IsLinkLocalUnicast() ||
		ip.Equal(net.ParseIP("100.100.100.200")) ||
		ip.Equal(net.ParseIP("168.63.129.16")) ||
		ip.Equal(net.ParseIP("fd00:ec2::254"))
}

func dialRelayTarget(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	dialer := &net.Dialer{Timeout: relayHTTPTimeout, KeepAlive: 30 * time.Second}
	for _, resolved := range addresses {
		if isMetadataIP(resolved.IP) {
			return nil, errors.New("cloud metadata targets are denied")
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("tunnel target resolved to no addresses")
	}
	return nil, lastErr
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func encodeRelayBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(body)
}

func decodeRelayBody(body string) ([]byte, error) {
	if body == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// relayBaseTransport clones the default transport's settings. A LAN-paired
// Guard wraps http.DefaultTransport in its pinning router, which is not an
// *http.Transport; the relay dials its own target and needs none of it.
func relayBaseTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
