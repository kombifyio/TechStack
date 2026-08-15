// Package httpguard implements the outbound HTTPS-only runtime channel used by
// kombify Guard when inbound gRPC/mTLS is not reachable from the control plane.
package httpguard

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/privatechannel"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	defaultInterval       = 30 * time.Second
	defaultRequestTimeout = 15 * time.Second
	defaultRetryAttempts  = 3
	maxResponseBody       = 64 << 10
	emptyCommandPollDelay = time.Second
)

// Collector returns a fresh, observed inventory snapshot. Implementations must
// not manufacture service state when an observation source is unavailable.
type Collector interface {
	Collect(context.Context) (Snapshot, error)
}

type CommandExecutor interface {
	Execute(context.Context, *agentpb.StackKitCommand) *agentpb.StackKitResult
}

type streamingCommandExecutor interface {
	ExecuteStreaming(context.Context, *agentpb.StackKitCommand, func(*agentpb.LogEntry)) *agentpb.StackKitResult
}

// stagedCollector lets host liveness be published before slower service
// discovery. SystemCollector implements this path; custom collectors can keep
// using the single Collect method.
type stagedCollector interface {
	CollectHostSnapshot(context.Context) (Snapshot, error)
	CollectInventory(context.Context, Snapshot) (Snapshot, error)
}

// Config describes one enrolled outbound runtime channel.
type Config struct {
	HeartbeatURL     string
	InventoryURL     string
	CommandURL       string
	CommandResultURL string
	RuntimeLogURL    string
	AgentToken       string
	RuntimeAgentID   string
	ServerID         string
	TenantID         string
	OwnerID          string
	StackID          string
	LeaseID          string
	AgentVersion     string
	SourceEpoch      string
	Interval         time.Duration
	RequestTimeout   time.Duration
	RetryAttempts    int
	HTTPClient       *http.Client
	Collector        Collector
	CommandExecutor  CommandExecutor
	// RuntimeConvergence supplies the provider-neutral package convergence
	// state. The callback is read atomically by the caller-owned tracker and is
	// intentionally separate from service inventory collection.
	RuntimeConvergence func() runtimeconvergence.Snapshot
	OnFirstHeartbeat   func()
	Logger             *slog.Logger
	// PrivateLANHTTPOrigin is the exact alpha-only clear-text capability. It
	// permits one literal RFC1918 :5264 origin, never DNS or public targets.
	PrivateLANHTTPOrigin string
}

// Client continuously publishes authenticated heartbeat and inventory
// observations. It never logs the bearer token or request payload.
type Client struct {
	cfg            Config
	httpClient     *http.Client
	log            *slog.Logger
	sourceEpoch    string
	sequence       atomic.Int64
	firstHeartbeat sync.Once
}

type controlRequest struct {
	RuntimeAgentID string                  `json:"runtime_agent_id"`
	TenantID       string                  `json:"tenant_id,omitempty"`
	OwnerID        string                  `json:"owner_id,omitempty"`
	StackID        string                  `json:"stack_id,omitempty"`
	LeaseID        string                  `json:"lease_id,omitempty"`
	ServerID       string                  `json:"server_id,omitempty"`
	Capabilities   []string                `json:"capabilities,omitempty"`
	Result         *agentpb.StackKitResult `json:"result,omitempty"`
	Log            *agentpb.LogEntry       `json:"log,omitempty"`
}

type controlResponse struct {
	Data struct {
		Command *agentpb.StackKitCommand `json:"command"`
	} `json:"data"`
}

// New validates the enrollment and creates an outbound guard client. Plain
// HTTP is accepted only for loopback development targets so bearer credentials
// cannot be sent over a remote clear-text channel.
func New(cfg Config) (*Client, error) {
	cfg.HeartbeatURL = strings.TrimSpace(cfg.HeartbeatURL)
	cfg.InventoryURL = strings.TrimSpace(cfg.InventoryURL)
	cfg.CommandURL = strings.TrimSpace(cfg.CommandURL)
	cfg.CommandResultURL = strings.TrimSpace(cfg.CommandResultURL)
	cfg.RuntimeLogURL = strings.TrimSpace(cfg.RuntimeLogURL)
	cfg.AgentToken = strings.TrimSpace(cfg.AgentToken)
	cfg.RuntimeAgentID = strings.TrimSpace(cfg.RuntimeAgentID)
	cfg.SourceEpoch = strings.TrimSpace(cfg.SourceEpoch)
	if err := validateEndpointURL(cfg.HeartbeatURL, cfg.PrivateLANHTTPOrigin); err != nil {
		return nil, fmt.Errorf("heartbeat URL: %w", err)
	}
	if err := validateEndpointURL(cfg.InventoryURL, cfg.PrivateLANHTTPOrigin); err != nil {
		return nil, fmt.Errorf("inventory URL: %w", err)
	}
	if cfg.CommandURL != "" || cfg.CommandResultURL != "" {
		if err := validateEndpointURL(cfg.CommandURL, cfg.PrivateLANHTTPOrigin); err != nil {
			return nil, fmt.Errorf("command URL: %w", err)
		}
		if err := validateEndpointURL(cfg.CommandResultURL, cfg.PrivateLANHTTPOrigin); err != nil {
			return nil, fmt.Errorf("command result URL: %w", err)
		}
		if cfg.CommandExecutor == nil {
			return nil, errors.New("typed command executor is required when HTTPS control is configured")
		}
		if cfg.RuntimeLogURL == "" {
			cfg.RuntimeLogURL = strings.TrimSuffix(cfg.CommandResultURL, "/commands/result") + "/runtime/logs"
		}
		if err := validateEndpointURL(cfg.RuntimeLogURL, cfg.PrivateLANHTTPOrigin); err != nil {
			return nil, fmt.Errorf("runtime log URL: %w", err)
		}
	}
	if cfg.AgentToken == "" {
		return nil, errors.New("runtime agent token is required")
	}
	if cfg.RuntimeAgentID == "" {
		return nil, errors.New("runtime agent id is required")
	}
	if cfg.SourceEpoch == "" {
		var epochErr error
		cfg.SourceEpoch, epochErr = newGuardSourceEpoch()
		if epochErr != nil {
			return nil, fmt.Errorf("create Guard source epoch: %w", epochErr)
		}
	}
	if cfg.Collector == nil {
		return nil, errors.New("inventory collector is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.RetryAttempts <= 0 {
		cfg.RetryAttempts = defaultRetryAttempts
	}
	if cfg.RetryAttempts > 5 {
		cfg.RetryAttempts = 5
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: cfg.RequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		cfg: cfg, httpClient: client, log: logger.With("component", "http-guard"),
		sourceEpoch: cfg.SourceEpoch,
	}, nil
}

// Run publishes an initial observation immediately and then continues until
// cancellation. Transport failures use capped jittered backoff; each HTTP call
// also has a small bounded retry budget.
func (c *Client) Run(ctx context.Context) error {
	if c.cfg.CommandURL != "" {
		go c.runControlLoop(ctx)
	}
	backoff := newRetryBackoff(time.Second, 30*time.Second)
	for {
		err := c.sendCycle(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := c.cfg.Interval
		if err != nil {
			delay = backoff.Next()
			c.log.Warn("guard_publish_failed", "error", err.Error(), "retry_in", delay.String())
		} else {
			backoff.Reset()
			c.log.Debug("guard_publish_succeeded", "runtime_agent_id", c.cfg.RuntimeAgentID)
		}
		if err := waitContext(ctx, delay); err != nil {
			return nil
		}
	}
}

func (c *Client) runControlLoop(ctx context.Context) {
	backoff := newRetryBackoff(time.Second, 30*time.Second)
	for ctx.Err() == nil {
		command, err := c.pollTypedCommand(ctx)
		if err != nil {
			delay := backoff.Next()
			c.log.Warn("guard_control_poll_failed", "error", err.Error(), "retry_in", delay.String())
			_ = waitContext(ctx, delay)
			continue
		}
		backoff.Reset()
		if command == nil {
			_ = waitContext(ctx, emptyCommandPollDelay)
			continue
		}
		var result *agentpb.StackKitResult
		if streaming, ok := c.cfg.CommandExecutor.(streamingCommandExecutor); ok {
			logQueue := make(chan *agentpb.LogEntry, 512)
			logsDone := make(chan struct{})
			go func() {
				defer close(logsDone)
				for entry := range logQueue {
					if err := c.submitRuntimeLog(ctx, entry); err != nil {
						c.log.Warn("guard_runtime_log_failed", "command_id", command.GetCommandId(), "error", err.Error())
					}
				}
			}()
			result = streaming.ExecuteStreaming(ctx, command, func(entry *agentpb.LogEntry) {
				select {
				case logQueue <- entry:
				case <-ctx.Done():
				}
			})
			close(logQueue)
			<-logsDone
		} else {
			result = c.cfg.CommandExecutor.Execute(ctx, command)
		}
		if result == nil {
			result = &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: false, Stderr: "typed command executor returned no result"}
		}
		if result.CommandId == "" {
			result.CommandId = command.GetCommandId()
		}
		if err := c.submitTypedResult(ctx, result); err != nil {
			c.log.Warn("guard_control_result_failed", "command_id", command.GetCommandId(), "error", err.Error())
		}
	}
}

func (c *Client) controlIdentity() controlRequest {
	return controlRequest{
		RuntimeAgentID: c.cfg.RuntimeAgentID, TenantID: c.cfg.TenantID, OwnerID: c.cfg.OwnerID,
		StackID: c.cfg.StackID, LeaseID: c.cfg.LeaseID, ServerID: c.cfg.ServerID,
		Capabilities: []string{"stackkit", stackkitcommand.ExpectedPlanHashCapability},
	}
}

func (c *Client) pollTypedCommand(ctx context.Context) (*agentpb.StackKitCommand, error) {
	status, body, err := c.postJSONResponse(ctx, c.cfg.CommandURL, c.controlIdentity())
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, unexpectedStatusError(status, body)
	}
	var response controlResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode typed command: %w", err)
	}
	if response.Data.Command == nil || strings.TrimSpace(response.Data.Command.GetCommandId()) == "" {
		return nil, errors.New("typed command response is empty")
	}
	return response.Data.Command, nil
}

func (c *Client) submitTypedResult(ctx context.Context, result *agentpb.StackKitResult) error {
	request := c.controlIdentity()
	request.Result = result
	status, body, err := c.postJSONResponse(ctx, c.cfg.CommandResultURL, request)
	if err != nil {
		return err
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	return unexpectedStatusError(status, body)
}

func (c *Client) submitRuntimeLog(ctx context.Context, entry *agentpb.LogEntry) error {
	if entry == nil || c.cfg.RuntimeLogURL == "" {
		return nil
	}
	request := c.controlIdentity()
	request.Log = entry
	status, body, err := c.postJSONResponse(ctx, c.cfg.RuntimeLogURL, request)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return unexpectedStatusError(status, body)
	}
	return nil
}

func (c *Client) postJSONResponse(ctx context.Context, endpoint string, payload any) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kombify-techstack-guard/"+firstNonEmpty(c.cfg.AgentVersion, "unknown"))
	if tenantID := strings.TrimSpace(c.cfg.TenantID); tenantID != "" {
		req.Header.Set("X-Kombify-Tenant-ID", tenantID)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	return response.StatusCode, responseBody, readErr
}

func unexpectedStatusError(status int, payload []byte) error {
	if detail := compactResponseDetail(payload); detail != "" {
		return fmt.Errorf("unexpected HTTP status %d: %s", status, detail)
	}
	return fmt.Errorf("unexpected HTTP status %d", status)
}

func (c *Client) sendCycle(ctx context.Context) error {
	if collector, ok := c.cfg.Collector.(stagedCollector); ok {
		snapshot, err := collector.CollectHostSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("collect host heartbeat: %w", err)
		}
		c.prepareObservation(&snapshot)
		if publishErr := c.postJSON(ctx, c.cfg.HeartbeatURL, heartbeatFromSnapshot(snapshot)); publishErr != nil {
			return fmt.Errorf("publish heartbeat: %w", publishErr)
		}
		c.firstHeartbeat.Do(func() {
			if c.cfg.OnFirstHeartbeat != nil {
				c.cfg.OnFirstHeartbeat()
			}
		})
		snapshot, err = collector.CollectInventory(ctx, snapshot)
		if err != nil {
			return fmt.Errorf("collect service inventory after heartbeat: %w", err)
		}
		c.prepareObservation(&snapshot)
		if publishErr := c.postJSON(ctx, c.cfg.InventoryURL, snapshot); publishErr != nil {
			return fmt.Errorf("publish inventory: %w", publishErr)
		}
		return nil
	}

	snapshot, err := c.cfg.Collector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect inventory: %w", err)
	}
	c.prepareObservation(&snapshot)
	if publishErr := c.postJSON(ctx, c.cfg.HeartbeatURL, heartbeatFromSnapshot(snapshot)); publishErr != nil {
		return fmt.Errorf("publish heartbeat: %w", publishErr)
	}
	c.firstHeartbeat.Do(func() {
		if c.cfg.OnFirstHeartbeat != nil {
			c.cfg.OnFirstHeartbeat()
		}
	})
	c.prepareObservation(&snapshot)
	if publishErr := c.postJSON(ctx, c.cfg.InventoryURL, snapshot); publishErr != nil {
		return fmt.Errorf("publish inventory: %w", publishErr)
	}
	return nil
}

func (c *Client) prepareObservation(snapshot *Snapshot) {
	c.applyIdentity(snapshot)
	snapshot.RuntimeConvergence = c.runtimeConvergenceSnapshot()
	snapshot.SourceEpoch = c.sourceEpoch
	snapshot.SourceSequence = c.sequence.Add(1)
	snapshot.ObservedAt = time.Now().UTC()
}

func (c *Client) applyIdentity(snapshot *Snapshot) {
	snapshot.RuntimeAgentID = c.cfg.RuntimeAgentID
	snapshot.ServerID = firstNonEmpty(snapshot.ServerID, c.cfg.ServerID)
	snapshot.TenantID = firstNonEmpty(snapshot.TenantID, c.cfg.TenantID)
	snapshot.OwnerID = firstNonEmpty(snapshot.OwnerID, c.cfg.OwnerID)
	snapshot.StackID = firstNonEmpty(snapshot.StackID, c.cfg.StackID)
	snapshot.LeaseID = firstNonEmpty(snapshot.LeaseID, c.cfg.LeaseID)
	snapshot.AgentVersion = firstNonEmpty(snapshot.AgentVersion, c.cfg.AgentVersion)
}

func (c *Client) runtimeConvergenceSnapshot() *runtimeconvergence.Snapshot {
	if c == nil || c.cfg.RuntimeConvergence == nil {
		return nil
	}
	snapshot, err := runtimeconvergence.Normalize(c.cfg.RuntimeConvergence())
	if err != nil {
		// The tracker is an internal boundary. If a future implementation sends
		// an invalid value, preserve the liveness heartbeat with a safe pending
		// status instead of serializing an unbounded or secret-bearing payload.
		safe := runtimeconvergence.Pending(time.Now().UTC())
		return &safe
	}
	return &snapshot
}

func heartbeatFromSnapshot(snapshot Snapshot) Heartbeat {
	return Heartbeat{
		SourceEpoch: snapshot.SourceEpoch, SourceSequence: snapshot.SourceSequence,
		ObservedAt:         snapshot.ObservedAt,
		CPUPercent:         snapshot.Host.CPUPercent,
		MemoryUsedBytes:    snapshot.Host.MemoryUsedBytes,
		MemoryTotalBytes:   snapshot.Host.MemoryTotalBytes,
		DiskUsedBytes:      snapshot.Host.DiskUsedBytes,
		DiskTotalBytes:     snapshot.Host.DiskTotalBytes,
		UptimeSeconds:      snapshot.Host.UptimeSeconds,
		RuntimeConvergence: snapshot.RuntimeConvergence,
	}
}

func newGuardSourceEpoch() (string, error) {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < c.cfg.RetryAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * 250 * time.Millisecond
			if err := waitContext(ctx, delay); err != nil {
				return err
			}
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if reqErr != nil {
			return reqErr
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.AgentToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "kombify-techstack-guard/"+firstNonEmpty(c.cfg.AgentVersion, "unknown"))
		if tenantID := strings.TrimSpace(c.cfg.TenantID); tenantID != "" {
			req.Header.Set("X-Kombify-Tenant-ID", tenantID)
		}
		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		// Keep the rejection envelope instead of discarding it. A status code
		// alone cannot distinguish a benign race from a permanently rejected
		// agent, which is exactly how a fail-closed conflict can retry for hours
		// without anyone being able to name the cause from the agent journal.
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		_ = resp.Body.Close()
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return nil
		}
		if detail := compactResponseDetail(payload); detail != "" {
			lastErr = fmt.Errorf("unexpected HTTP status %d: %s", resp.StatusCode, detail)
		} else {
			lastErr = fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
		}
		if resp.StatusCode < http.StatusInternalServerError && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}
	return lastErr
}

// compactResponseDetail renders a rejection envelope as one bounded log-safe
// line. Only the control plane's own error code, message, and reason are kept;
// the agent never echoes an arbitrary body back into its journal.
func compactResponseDetail(payload []byte) string {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return ""
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{
		strings.TrimSpace(envelope.Error.Code),
		strings.TrimSpace(envelope.Error.Message),
		strings.TrimSpace(envelope.Error.Details.Reason),
	} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	detail := strings.Join(parts, ": ")
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return detail
}

func validateEndpointURL(raw, privateLANHTTPOrigin string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("absolute URL is required")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("credentials, query parameters, and fragments are not allowed")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	if privatechannel.MatchesLANOrigin(raw, privateLANHTTPOrigin) {
		return nil
	}
	return errors.New("HTTPS is required for non-loopback runtime channels")
}

type retryBackoff struct {
	base    time.Duration
	cap     time.Duration
	attempt uint
	rng     *rand.Rand
}

func newRetryBackoff(base, cap time.Duration) *retryBackoff {
	// #nosec G404 -- jitter prevents synchronized reconnects; it is not used for credentials.
	return &retryBackoff{base: base, cap: cap, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func (b *retryBackoff) Next() time.Duration {
	ceiling := b.base * time.Duration(1<<min(b.attempt, 10))
	if ceiling > b.cap || ceiling <= 0 {
		ceiling = b.cap
	}
	b.attempt++
	if ceiling <= 100*time.Millisecond {
		return ceiling
	}
	return 100*time.Millisecond + time.Duration(b.rng.Int63n(int64(ceiling-100*time.Millisecond)+1))
}

func (b *retryBackoff) Reset() { b.attempt = 0 }

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
