package reachagent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const (
	// denialRetry is how long a relay stays stopped after a permanent denial
	// before one new attempt: the Guard never reconnects in a loop against a
	// binding kombify.me refuses, but it does not stay dark forever if the
	// control plane repaired the binding without changing its generation.
	denialRetry = 5 * time.Minute
	// stableAfter resets the reconnect backoff for a connection that lasted.
	stableAfter = 30 * time.Second
)

// Relay states the Guard reports to the control plane.
const (
	relayConnecting = "connecting"
	relayConnected  = "connected"
	relayDenied     = "denied"
	relayStopped    = "stopped"
)

type relayKey struct {
	url, tenant, server, worker string
	generation                  int64
}

// relayController runs one relay session loop per relay target and restarts it
// when the target's generation, binding or URL changes.
type relayController struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	key      *relayKey
	cancel   context.CancelFunc
	done     chan struct{}
	status   guardreach.RelayStatus
	deniedAt time.Time
}

func newRelayController(cfg Config, log *slog.Logger) *relayController {
	return &relayController{cfg: cfg, log: log, status: guardreach.RelayStatus{State: relayStopped}}
}

// Status reports the relay state with the generation it applies to; an empty
// status before any relay ran is not reported.
func (c *relayController) Status() *guardreach.RelayStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Generation < 1 {
		return nil
	}
	status := c.status
	return &status
}

func (c *relayController) setStatus(generation int64, state, reason string) {
	c.mu.Lock()
	c.status = guardreach.RelayStatus{State: state, ReasonCode: reason, Generation: generation}
	c.mu.Unlock()
}

// Apply starts the relay for target, or keeps the running one. A relay that
// stopped on a permanent denial restarts only for a changed target or after
// denialRetry.
func (c *relayController) Apply(ctx context.Context, target guardreach.RelayTarget) {
	key := relayKey{url: target.RelayURL, tenant: target.TenantID, server: target.ServerID, worker: target.WorkerID, generation: target.Generation}
	c.mu.Lock()
	if c.key != nil && *c.key == key {
		running := c.cancel != nil && !chanClosed(c.done)
		retry := !c.deniedAt.IsZero() && time.Since(c.deniedAt) >= denialRetry
		if running || !retry {
			c.mu.Unlock()
			return
		}
	}
	c.mu.Unlock()
	c.Stop()
	relayCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.mu.Lock()
	c.key, c.cancel, c.done, c.deniedAt = &key, cancel, done, time.Time{}
	c.mu.Unlock()
	go func() {
		defer close(done)
		c.run(relayCtx, key)
	}()
}

func chanClosed(done chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Stop ends the relay session loop and waits for it.
func (c *relayController) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done, c.key = nil, nil, nil
	generation := c.status.Generation
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		c.setStatus(generation, relayStopped, "")
	}
}

func (c *relayController) run(ctx context.Context, key relayKey) {
	relay, err := kombifyme.NewRelay(kombifyme.RelayConfig{
		URL: key.url, Version: c.cfg.Version, Logger: c.log,
		ProofProvider: func(context.Context) (string, error) { return mintRelayProof(c.cfg.Manager, c.cfg.Now, key) },
		OnConnected:   func() { c.setStatus(key.generation, relayConnected, "") },
	})
	if err != nil {
		c.setStatus(key.generation, relayDenied, "relay_not_configured")
		return
	}
	backoff := agent.NewBackoff()
	for ctx.Err() == nil {
		c.setStatus(key.generation, relayConnecting, "")
		sessionErr := relay.RunSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if revoked := revokedIdentity(sessionErr); revoked {
			c.denied(key, "guard_identity_not_usable")
			return
		}
		if denial := kombifyme.AsRelayDenial(sessionErr); denial.Permanent() {
			// binding_unknown, binding_revoked and every other permanent refusal:
			// stop and report. A new generation from the control plane restarts it.
			c.log.Warn("guard_reach_relay_denied", "reason", denial.ReasonCode, "status", denial.Status)
			c.denied(key, denial.ReasonCode)
			return
		}
		var connected *kombifyme.RelaySessionError
		if errors.As(sessionErr, &connected) && connected.ConnectedFor >= stableAfter {
			backoff.Reset()
		}
		delay := backoff.Next()
		c.log.Warn("guard_reach_relay_reconnecting", "delay", delay.String(), "attempt", backoff.Attempt())
		if !sleep(ctx, delay) {
			return
		}
	}
}

func (c *relayController) denied(key relayKey, reason string) {
	if reason == "" {
		reason = "relay_denied"
	}
	c.setStatus(key.generation, relayDenied, reason)
	c.mu.Lock()
	c.deniedAt = time.Now()
	c.mu.Unlock()
}

func revokedIdentity(err error) bool {
	return errors.Is(err, hostidentity.ErrRevoked) || errors.Is(err, hostidentity.ErrRecoveryNeeded)
}

// mintRelayProof signs one single-use proof for the relay. The audience is the
// compiled-in relay origin, never a value the control plane supplied.
func mintRelayProof(manager *hostidentity.Manager, now func() time.Time, key relayKey) (string, error) {
	return manager.MintHostProof(guardidentity.HostProofInput{
		Issuer: key.worker, Subject: key.server, TenantID: key.tenant,
		Audience: guardidentity.AudienceRelay, Generation: key.generation, Now: now(),
	})
}
