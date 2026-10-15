package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
)

const (
	guardTelemetryAgentEnv    = "TECHSTACK_GUARD_TELEMETRY_ENABLED"
	guardTelemetryIntervalEnv = "TECHSTACK_GUARD_TELEMETRY_INTERVAL"
	guardTelemetrySpoolEnv    = "TECHSTACK_GUARD_TELEMETRY_SPOOL_BYTES"
	guardOTLPBindEnv          = "TECHSTACK_GUARD_OTLP_BIND"
	guardOTLPGRPCPortEnv      = "TECHSTACK_GUARD_OTLP_GRPC_PORT"
	guardOTLPHTTPPortEnv      = "TECHSTACK_GUARD_OTLP_HTTP_PORT"
)

// startGuardTelemetry runs the Guard's telemetry on the managed transport: it
// samples the host and containers within the data contract, keeps the samples in
// a bounded spool until the control plane acknowledges them, and serves a
// loopback OTLP sink so the StackKits collector exports to the Guard instead of
// to the network. Telemetry is never a precondition of the Guard's own work.
func startGuardTelemetry(ctx context.Context, cfg *agentModeConfig, custodyRoot *os.Root, enrollment agentEnrollment, identity guardIdentityRuntime, execution guardExecution, log *slog.Logger) {
	if !envBoolDefault(guardTelemetryAgentEnv, true) {
		return
	}
	url := derivedAgentControlURL(enrollment.HeartbeatURL, "telemetry")
	if url == "" || strings.TrimSpace(cfg.stateDir) == "" {
		return
	}
	budget := int64(guardtelemetry.DefaultSpoolBytes)
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(guardTelemetrySpoolEnv)), 10, 64); err == nil && value > 0 {
		budget = value
	}
	spool, err := guardtelemetry.OpenSpoolRoot(custodyRoot, "telemetry-spool", guardtelemetry.SpoolConfig{MaxBytes: budget})
	if err != nil {
		log.Warn("guard_telemetry_unavailable", "error", err.Error())
		return
	}
	sender, err := guardtelemetry.NewSender(guardtelemetry.SenderConfig{
		URL: url, HTTPClient: identity.httpClient(30 * time.Second), AgentToken: enrollment.AgentToken, TenantID: enrollment.TenantID, Spool: spool,
	})
	if err != nil {
		log.Warn("guard_telemetry_unavailable", "error", err.Error())
		return
	}
	interval := durationEnv(guardTelemetryIntervalEnv, guardtelemetry.DefaultInterval)
	agent, err := guardtelemetry.NewAgent(guardtelemetry.AgentConfig{
		Host: &guardtelemetry.HostSampler{}, Containers: guardtelemetry.ContainerSampler{Probe: execution.telemetryDockerProbe()},
		Drives: &drivehealth.Collector{Probe: execution.telemetryDriveProbe()}, DriveHealthEnabled: sender.DriveHealthSupported,
		Spool: spool, Sender: sender, Interval: max(interval, 10*time.Second), Logger: log,
	})
	if err != nil {
		log.Warn("guard_telemetry_unavailable", "error", err.Error())
		return
	}
	if sink, sinkErr := guardtelemetry.StartOTLP(guardtelemetry.OTLPConfig{
		Bind: os.Getenv(guardOTLPBindEnv), GRPCPort: portEnv(guardOTLPGRPCPortEnv, guardtelemetry.DefaultOTLPGRPCPort),
		HTTPPort: portEnv(guardOTLPHTTPPortEnv, guardtelemetry.DefaultOTLPHTTPPort), Submit: agent.Submit, Logger: log,
	}); sinkErr != nil {
		// Without the sink the collector cannot export to the Guard; the Guard's
		// own samples still flow.
		log.Warn("guard_otlp_sink_unavailable", "error", sinkErr.Error())
	} else {
		go func() {
			<-ctx.Done()
			sink.Stop()
		}()
	}
	go agent.Run(ctx)
	log.Info("guard_telemetry_started", "interval", interval.String(), "spool_bytes", budget)
}

// portEnv reads a port; 0 disables, an invalid value keeps the default.
func portEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 0 || port > 65535 {
		return fallback
	}
	return port
}

// telemetryDockerProbe selects how container readings are taken: through the
// executor's fixed probes in the split layout (the daemon never holds the docker
// socket), directly on a Guard that still runs as root, and not at all otherwise.
func (e guardExecution) telemetryDockerProbe() guardtelemetry.DockerProbe {
	switch {
	case e.remote != nil:
		return e.remote.ProbeDockerKind
	case e.mode == guardModeLegacyRoot:
		return guardtelemetry.ExecDockerProbe
	}
	return nil
}

// telemetryDriveProbe keeps raw-device access on the privileged side of a split
// Guard. Missing privilege remains an explicit collection failure.
func (e guardExecution) telemetryDriveProbe() drivehealth.Probe {
	if e.remote != nil {
		return e.remote.ProbeDrive
	}
	return drivehealth.ExecProbe
}
