package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/reachagent"
)

// startGuardReach runs the Guard side of reach for a key-bound Guard: the
// control-plane exchange and, unless a static relay key owns the relay, the
// kombify.me relay authenticated by host proofs. A Guard that never bound a
// host key, or a control plane without reach, leaves it idle; reach is never
// a precondition of the Guard's own work.
func startGuardReach(ctx context.Context, cfg *agentModeConfig, enrollment agentEnrollment, identity guardIdentityRuntime, manager *hostidentity.Manager, log *slog.Logger) {
	if manager == nil || identity.client == nil || identity.client.Mode() != hostidentity.ModeHostKey {
		return
	}
	exchangeURL := reachagent.ExchangeURLFromHeartbeat(enrollment.HeartbeatURL)
	if exchangeURL == "" {
		return
	}
	agent, err := reachagent.New(reachagent.Config{
		ExchangeURL: exchangeURL, HTTPClient: identity.httpClient(20 * time.Second), Manager: manager,
		WorkerID: enrollment.RuntimeAgentID, ServerID: enrollment.ServerID, TenantID: enrollment.TenantID,
		Version: version, ManageRelay: strings.TrimSpace(cfg.kombifyMeAPIKey) == "", Logger: log,
	})
	if err != nil {
		log.Warn("guard_reach_unavailable", "error", err.Error())
		return
	}
	go agent.Run(ctx)
	log.Info("guard_reach_started")
}
