// Package reachagent is the Guard side of kombify Guard reach. On a fixed
// cadence it exchanges state with the control plane over the Guard's
// key-bound channel: it reports its relay connection, hands over the host
// proof the control plane asks for (Connect registration), and learns where
// and as whom to connect its kombify.me relay. The relay then authenticates
// with a fresh host proof per connection, signed with the identity key that
// never leaves the Guard; no static relay key is involved.
//
// The Guard signs only what it can verify itself: the verifier audiences are
// compiled in, and worker, server and relay host must match its own
// enrollment and the audience's origin. A control plane that asks for
// anything else gets nothing signed.
package reachagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/guardreach"
)

const (
	pendingInterval  = 15 * time.Second
	readyInterval    = time.Minute
	idleInterval     = 10 * time.Minute
	errorIntervalMax = time.Minute
	proofThrottle    = 30 * time.Second
	exchangeBody     = 64 << 10
)

// Config configures an Agent.
type Config struct {
	// ExchangeURL is the control plane's reach endpoint for this worker.
	ExchangeURL string
	// HTTPClient must carry the Guard's key-bound transport.
	HTTPClient *http.Client
	Manager    *hostidentity.Manager
	// WorkerID, ServerID and TenantID are the Guard's own enrollment; the
	// control plane cannot make the Guard sign for another worker or server.
	WorkerID, ServerID, TenantID string
	Version                      string
	// ManageRelay is false when a static relay key is configured: the static
	// path then owns the relay and only the Connect exchange runs.
	ManageRelay bool
	// AllowRelayURL overrides the default policy (wss on the relay audience's
	// own host). Tests only.
	AllowRelayURL func(string) bool
	// PendingInterval and ReadyInterval override the exchange cadence while
	// reach is being set up and once it is ready. Tests only.
	PendingInterval, ReadyInterval time.Duration
	Logger                         *slog.Logger
	Now                            func() time.Time
}

// Agent runs the exchange loop and owns the relay connection.
type Agent struct {
	cfg   Config
	log   *slog.Logger
	relay *relayController

	mu        sync.Mutex
	proofSent map[string]time.Time
}

// New validates the configuration.
func New(cfg Config) (*Agent, error) {
	parsed, err := url.Parse(strings.TrimSpace(cfg.ExchangeURL))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("kombify Guard reach exchange URL is required")
	}
	if cfg.Manager == nil || cfg.HTTPClient == nil {
		return nil, errors.New("kombify Guard reach needs the identity manager and its key-bound client")
	}
	if strings.TrimSpace(cfg.WorkerID) == "" || strings.TrimSpace(cfg.ServerID) == "" || strings.TrimSpace(cfg.TenantID) == "" {
		return nil, errors.New("kombify Guard reach needs the worker, server and tenant of the enrollment")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.AllowRelayURL == nil {
		cfg.AllowRelayURL = relayURLOnAudienceHost
	}
	if cfg.PendingInterval <= 0 {
		cfg.PendingInterval = pendingInterval
	}
	if cfg.ReadyInterval <= 0 {
		cfg.ReadyInterval = readyInterval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	agent := &Agent{cfg: cfg, log: logger.With("component", "guard-reach"), proofSent: map[string]time.Time{}}
	agent.relay = newRelayController(cfg, agent.log)
	return agent, nil
}

// ExchangeURLFromHeartbeat derives the reach endpoint from the heartbeat URL
// of an enrollment, like the identity endpoints are derived.
func ExchangeURLFromHeartbeat(heartbeatURL string) string {
	base := strings.TrimSuffix(strings.TrimSpace(heartbeatURL), "/heartbeat")
	if base == strings.TrimSpace(heartbeatURL) || base == "" {
		return ""
	}
	return base + "/reach"
}

// relayURLOnAudienceHost accepts a relay URL only on the host of the relay
// audience, over wss, so a proof minted for the relay can only travel there.
func relayURLOnAudienceHost(raw string) bool {
	relay, err := url.Parse(raw)
	audience, audienceErr := url.Parse(guardidentity.AudienceRelay)
	return err == nil && audienceErr == nil && relay.Scheme == "wss" && relay.User == nil &&
		strings.EqualFold(relay.Hostname(), audience.Hostname()) && relay.Port() == ""
}

// Run exchanges until the context ends and stops the relay on the way out.
func (a *Agent) Run(ctx context.Context) {
	defer a.relay.Stop()
	var proofs []guardreach.ProofSubmission
	failures := 0
	for ctx.Err() == nil {
		document, err := a.exchange(ctx, guardreach.ExchangeRequest{Proofs: proofs, Relay: a.relay.Status()})
		proofs = nil
		wait := a.cfg.PendingInterval
		if err != nil {
			failures++
			wait = errorWait(failures, a.cfg.PendingInterval)
			a.log.Warn("guard_reach_exchange_failed", "error", err.Error(), "retry_in", wait.String())
			if a.cfg.Manager.Record().State == hostidentity.StateRevoked {
				// The control plane revoked this Guard: nothing may stay reachable
				// through it, whatever the relay still holds open.
				a.relay.Stop()
				wait = idleInterval
			}
		} else {
			failures = 0
			var deliverNow bool
			proofs, deliverNow = a.answer(document)
			wait = a.apply(ctx, document)
			if deliverNow {
				wait = 0
			}
		}
		if wait > 0 && !sleep(ctx, wait) {
			return
		}
	}
}

func errorWait(failures int, base time.Duration) time.Duration {
	wait := base
	for i := 1; i < failures && wait < errorIntervalMax; i++ {
		wait *= 2
	}
	if wait > errorIntervalMax {
		wait = errorIntervalMax
	}
	return wait
}

func sleep(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// answer mints the host proofs the control plane asked for, each one
// throttled so a control plane that keeps asking cannot turn the Guard into a
// signing loop. It returns whether to deliver them immediately.
func (a *Agent) answer(document *guardreach.Document) ([]guardreach.ProofSubmission, bool) {
	var out []guardreach.ProofSubmission
	for _, request := range document.ProofRequests {
		if request.Audience != guardidentity.AudienceConnect ||
			request.WorkerID != a.cfg.WorkerID || request.ServerID != a.cfg.ServerID || request.Generation < 1 ||
			strings.TrimSpace(request.TenantID) == "" {
			a.log.Warn("guard_reach_proof_request_refused", "audience", request.Audience)
			continue
		}
		key := fmt.Sprintf("%s|%d", request.Audience, request.Generation)
		a.mu.Lock()
		last := a.proofSent[key]
		throttled := !last.IsZero() && a.cfg.Now().Sub(last) < proofThrottle
		if !throttled {
			a.proofSent[key] = a.cfg.Now()
		}
		a.mu.Unlock()
		if throttled {
			continue
		}
		proof, err := a.cfg.Manager.MintHostProof(guardidentity.HostProofInput{
			Issuer: request.WorkerID, Subject: request.ServerID, TenantID: request.TenantID,
			Audience: guardidentity.AudienceConnect, Generation: request.Generation, Now: a.cfg.Now(),
		})
		if err != nil {
			a.log.Warn("guard_reach_proof_not_minted", "error", err.Error())
			continue
		}
		out = append(out, guardreach.ProofSubmission{Audience: request.Audience, Generation: request.Generation, Proof: proof})
	}
	return out, len(out) > 0
}

// apply turns the reach document into relay control and the next wait.
func (a *Agent) apply(ctx context.Context, document *guardreach.Document) time.Duration {
	target := document.KombifyMe
	if !a.cfg.ManageRelay {
		a.relay.Stop()
		target = nil
	}
	switch document.State {
	case guardreach.StateRevoked, "disabled", "needs_host_key", guardreach.StateNotEnrolled:
		a.relay.Stop()
		return idleInterval
	}
	if target != nil && a.acceptTarget(target) {
		a.relay.Apply(ctx, *target)
	} else {
		a.relay.Stop()
	}
	if document.State == guardreach.StateReady {
		return a.cfg.ReadyInterval
	}
	return a.cfg.PendingInterval
}

// acceptTarget refuses a relay target that is not this Guard's own binding on
// an allowed relay host.
func (a *Agent) acceptTarget(target *guardreach.RelayTarget) bool {
	if target.State != string(guardreach.TargetRegistered) || target.Generation < 1 ||
		target.WorkerID != a.cfg.WorkerID || target.ServerID != a.cfg.ServerID || target.TenantID != a.cfg.TenantID ||
		!a.cfg.AllowRelayURL(target.RelayURL) {
		a.log.Warn("guard_reach_relay_target_refused")
		return false
	}
	return true
}

func (a *Agent) exchange(ctx context.Context, request guardreach.ExchangeRequest) (*guardreach.Document, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ExchangeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, exchangeBody))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("control plane answered HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data struct {
			Reach guardreach.Document `json:"reach"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.Data.Reach.State == "" {
		return nil, errors.New("control plane returned an unusable reach document")
	}
	return &envelope.Data.Reach, nil
}
