package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/guardcompat"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Bounds of one probe pass: at most this many probes are sent per pass, and
// at most this many wait for their result at once. Each probe is the typed
// host update plan with its own 5 minute command budget.
const (
	guardRoundTripProbesPerPass   = 16
	guardRoundTripProbeConcurrent = 4
	guardRoundTripProbeTenantPage = 100
)

// guardRoundTripProbeTarget is one connected Guard that may need a probe.
type guardRoundTripProbeTarget struct {
	tenantID, serverID, agentID string
	// generation is the HTTPS credential generation the Guard holds now, zero
	// for an mTLS agent.
	generation int64
}

// guardRoundTripProbe keeps the Connected round-trip evidence of a healthy
// idle Guard fresh. A Guard that answered no typed command for
// guardcompat.CommandRoundTripProbeAfter (or whose identity changed since it
// last did) receives one read-only typed command: `stackkit host updates plan`
// through the existing typed channel, never a shell. The hub (HTTPS) or the
// mTLS listener records the accepted result as the round trip; the probe
// itself discards the plan. A Guard that cannot run host commands is never
// probed and keeps its evidence from real commands only.
type guardRoundTripProbe struct {
	candidates func(ctx context.Context) ([]guardRoundTripProbeTarget, error)
	trips      interface {
		CommandRoundTripsForTenant(ctx context.Context, tenantID string) (map[string]agentcontrol.CommandRoundTrip, error)
	}
	dispatcher servermaintenance.Dispatcher
	interval   time.Duration
	now        func() time.Time
	log        *slog.Logger
}

// bootGuardRoundTripProbe composes the probe over the deployment's typed
// command channel. It returns nil without a database or a host command
// dispatcher.
func bootGuardRoundTripProbe(cfg *config.Config, boot *v2Boot, orch *orchestrator.Orchestrator, hub *agentcontrol.Hub, grpcState *grpcBoot, log *logger.Logger) *guardRoundTripProbe {
	if cfg == nil || boot == nil || boot.db == nil || boot.db.DB == nil || orch == nil || hub == nil {
		return nil
	}
	dispatcher := jobs.HostMaintenanceDispatcherFor(orch.StackKitCommander())
	if dispatcher == nil {
		return nil
	}
	probe := &guardRoundTripProbe{
		trips: hub, dispatcher: dispatcher,
		interval: guardcompat.CommandRoundTripProbeInterval, now: time.Now, log: log.Logger,
	}
	var grpcSrv *grpcserver.Server
	if grpcState != nil {
		grpcSrv = grpcState.server
	}
	if stackKitCommandsUseMTLS(cfg.DeploymentMode, cfg.Server.Environment, grpcSrv) {
		probe.candidates = mtlsRoundTripProbeCandidates(grpcSrv)
	} else {
		probe.candidates = httpsRoundTripProbeCandidates(controlPlanePostgresStore(boot), grpcState)
	}
	return probe
}

// stackKitCommandsUseMTLS mirrors stackKitCommanderForDeployment: typed
// commands travel over the mTLS listener only on a non-local self-hosted
// deployment that runs one.
func stackKitCommandsUseMTLS(mode config.DeploymentMode, environment string, grpcSrv *grpcserver.Server) bool {
	return !mode.IsSaaS() && !strings.EqualFold(strings.TrimSpace(environment), "local") && grpcSrv != nil
}

// httpsRoundTripProbeCandidates lists the connected, host-command capable
// servers of every tenant with a tracked connection.
func httpsRoundTripProbeCandidates(store *controlplane.PostgresStore, grpcState *grpcBoot) func(context.Context) ([]guardRoundTripProbeTarget, error) {
	capabilities := serverAgentCapabilities(grpcState)
	return func(ctx context.Context) ([]guardRoundTripProbeTarget, error) {
		if store == nil {
			return nil, nil
		}
		var out []guardRoundTripProbeTarget
		heartbeatCutoff := time.Now().UTC().Add(time.Second)
		freshAfter := time.Now().UTC().Add(-guardcompat.StaleAfter)
		after := ""
		for {
			tenants, err := store.ListObservationSweepTenants(ctx, after, guardRoundTripProbeTenantPage, heartbeatCutoff)
			if err != nil {
				return out, err
			}
			for _, tenantID := range tenants {
				servers, err := store.ListServerRuntimesByTenant(ctx, tenantID, "")
				if err != nil {
					return out, err
				}
				generations := map[string]int64{}
				if workers, err := store.ListWorkersByTenant(ctx, tenantID); err == nil {
					for _, worker := range workers {
						if state, stateErr := controlplane.WorkerCredentialStateFromWorker(worker); stateErr == nil {
							generations[strings.TrimSpace(worker.ID)] = state.Generation
						}
					}
				}
				for _, server := range servers {
					workerID := strings.TrimSpace(server.WorkerID)
					if workerID == "" || server.ConnectionState != string(serverregistry.ConnectionConnected) ||
						server.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
						server.LifecycleState == string(serverregistry.LifecycleDecommissioned) ||
						server.LastHeartbeatAt == nil || server.LastHeartbeatAt.Before(freshAfter) ||
						!containsString(capabilities(server), stackkitcommand.HostMaintenanceCapability) {
						continue
					}
					out = append(out, guardRoundTripProbeTarget{
						tenantID: tenantID, serverID: server.ID, agentID: workerID, generation: generations[workerID],
					})
				}
			}
			if len(tenants) < guardRoundTripProbeTenantPage {
				return out, nil
			}
			after = tenants[len(tenants)-1]
		}
	}
}

// mtlsRoundTripProbeCandidates lists the agents connected to the mTLS listener
// under an enrolled tenant and certificate that can run host commands.
func mtlsRoundTripProbeCandidates(grpcSrv *grpcserver.Server) func(context.Context) ([]guardRoundTripProbeTarget, error) {
	return func(context.Context) ([]guardRoundTripProbeTarget, error) {
		var out []guardRoundTripProbeTarget
		for _, agent := range grpcSrv.GetAgents() {
			if agent == nil || strings.TrimSpace(agent.Tenant) == "" || strings.TrimSpace(agent.CertSerial) == "" ||
				strings.EqualFold(agent.Status, "disconnected") ||
				!containsString(agent.Capabilities, stackkitcommand.HostMaintenanceCapability) {
				continue
			}
			out = append(out, guardRoundTripProbeTarget{tenantID: agent.Tenant, agentID: agent.ID})
		}
		return out, nil
	}
}

// Run probes every interval until ctx ends.
func (p *guardRoundTripProbe) Run(ctx context.Context) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// due reports whether the Guard's persisted round trip needs a probe.
func guardRoundTripProbeDue(target guardRoundTripProbeTarget, trip *agentcontrol.CommandRoundTrip, now time.Time) bool {
	if trip == nil || now.Sub(trip.AcknowledgedAt) >= guardcompat.CommandRoundTripProbeAfter {
		return true
	}
	if target.generation > 0 {
		return trip.Transport != agentcontrol.RoundTripTransportHTTPS || trip.Generation != target.generation
	}
	return trip.Transport != agentcontrol.RoundTripTransportMTLS
}

// guardRoundTripProbeCommandID is deterministic per Guard and probe window, so
// replicas that race for the same Guard send one command between them (the
// durable hub refuses a second command_id) and a Guard gets at most one probe
// per window.
func guardRoundTripProbeCommandID(target guardRoundTripProbeTarget, now time.Time) string {
	sum := sha256.Sum256([]byte("kombify.guard/round-trip-probe/v1\x00" + target.tenantID + "\x00" + target.agentID))
	window := now.UTC().Truncate(guardcompat.CommandRoundTripProbeAfter).Unix()
	return "guard-probe:" + hex.EncodeToString(sum[:])[:24] + ":" + strconv.FormatInt(window, 36)
}

func (p *guardRoundTripProbe) pass(ctx context.Context) {
	targets, err := p.candidates(ctx)
	if err != nil {
		p.log.Warn("guard_round_trip_probe_candidates_failed", "error", err.Error())
	}
	now := p.now().UTC()
	trips := map[string]map[string]agentcontrol.CommandRoundTrip{}
	var due []guardRoundTripProbeTarget
	for _, target := range targets {
		if len(due) == guardRoundTripProbesPerPass {
			break
		}
		tenantTrips, read := trips[target.tenantID]
		if !read {
			tenantTrips, err = p.trips.CommandRoundTripsForTenant(ctx, target.tenantID)
			if err != nil {
				p.log.Warn("guard_round_trip_probe_read_failed", "error", err.Error())
				continue
			}
			trips[target.tenantID] = tenantTrips
		}
		var trip *agentcontrol.CommandRoundTrip
		if found, ok := tenantTrips[target.agentID]; ok {
			trip = &found
		}
		if guardRoundTripProbeDue(target, trip, now) {
			due = append(due, target)
		}
	}
	slots := make(chan struct{}, guardRoundTripProbeConcurrent)
	var wg sync.WaitGroup
	for _, target := range due {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func(target guardRoundTripProbeTarget) {
			defer wg.Done()
			defer func() { <-slots }()
			p.probe(ctx, target, now)
		}(target)
	}
	wg.Wait()
}

func (p *guardRoundTripProbe) probe(ctx context.Context, target guardRoundTripProbeTarget, now time.Time) {
	_, err := p.dispatcher.Plan(ctx, servermaintenance.Target{
		TenantID: target.tenantID, ServerID: target.serverID, AgentID: target.agentID,
		CommandID: guardRoundTripProbeCommandID(target, now),
	})
	var refusal *servermaintenance.Refusal
	var failure *servermaintenance.Failure
	switch {
	case errors.As(err, &refusal) && refusal.Code == servermaintenance.RefusalStackKitsUpgradeRequired:
		// Refused before sending: the configured StackKits pin has no host
		// commands, so this deployment cannot probe.
		p.log.Info("guard_round_trip_probe_unavailable", "agent_id", target.agentID, "reason", refusal.Code)
	case err == nil, errors.As(err, &refusal), errors.As(err, &failure):
		// The Guard answered: its accepted result is the round trip, whatever
		// the plan said.
	case ctx.Err() != nil:
	default:
		p.log.Info("guard_round_trip_probe_unanswered", "agent_id", target.agentID, "error", err.Error())
	}
}
