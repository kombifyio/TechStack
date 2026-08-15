package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/collectors"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const httpsScheme = "https"

// isAgentMode reports whether the binary was invoked as `techstack agent`.
// The agent/worker mode turns the techstack binary into the node-side daemon
// (kombify Guard, the RIL server connection + monitoring agent): it dials Core
// over mTLS gRPC, heartbeats, ships metrics, executes diagnostic commands, and
// reconnects with backoff on any failure.
func isAgentMode(args []string) bool {
	return len(args) > 1 && args[1] == "agent"
}

type agentModeConfig struct {
	transport         string
	coreAddr          string
	agentID           string
	certFile          string
	keyFile           string
	caFile            string
	heartbeatInterval time.Duration
	metricsInterval   time.Duration
	samplerInterval   time.Duration
	disableMetrics    bool
	enrollmentFile    string
	heartbeatURL      string
	inventoryURL      string
	agentTokenFile    string
	agentToken        string
	accessManifest    string
	publicIP          string
	kombifyMeRelayURL string
	kombifyMeAPIKey   string
}

func parseAgentModeConfig(args []string) (*agentModeConfig, error) {
	fs := flag.NewFlagSet("techstack agent", flag.ContinueOnError)
	cfg := &agentModeConfig{}

	fs.StringVar(&cfg.transport, "transport", envDefault("TECHSTACK_AGENT_TRANSPORT", "grpc-mtls"),
		"runtime channel: grpc-mtls or https, env TECHSTACK_AGENT_TRANSPORT")
	fs.StringVar(&cfg.coreAddr, "core", envDefault("TECHSTACK_AGENT_CORE_ADDR", ""),
		"Core gRPC address (host:port), env TECHSTACK_AGENT_CORE_ADDR")
	fs.StringVar(&cfg.agentID, "agent-id", envDefault("TECHSTACK_AGENT_ID", envDefault("TECHSTACK_RUNTIME_AGENT_ID", "")),
		"stable agent identity (must match the enrolled cert CN), env TECHSTACK_AGENT_ID")
	fs.StringVar(&cfg.certFile, "cert", envDefault("TECHSTACK_AGENT_CERT_FILE", ""),
		"agent certificate (mTLS), env TECHSTACK_AGENT_CERT_FILE")
	fs.StringVar(&cfg.keyFile, "key", envDefault("TECHSTACK_AGENT_KEY_FILE", ""),
		"agent private key (mTLS), env TECHSTACK_AGENT_KEY_FILE")
	fs.StringVar(&cfg.caFile, "ca", envDefault("TECHSTACK_AGENT_CA_FILE", ""),
		"Core CA certificate, env TECHSTACK_AGENT_CA_FILE")
	fs.DurationVar(&cfg.heartbeatInterval, "heartbeat-interval", 0,
		"heartbeat interval override (0 = Core-provided config, default 30s)")
	fs.DurationVar(&cfg.metricsInterval, "metrics-interval", 30*time.Second,
		"metric collector interval")
	fs.DurationVar(&cfg.samplerInterval, "sampler-interval", 15*time.Second,
		"host resource sampling interval for heartbeats")
	fs.BoolVar(&cfg.disableMetrics, "no-metrics", false,
		"disable the metrics pusher (heartbeats still carry resource usage)")
	fs.StringVar(&cfg.enrollmentFile, "enrollment-file", envDefault("TECHSTACK_AGENT_ENROLLMENT_FILE", ""),
		"root-readable enrollment response JSON for the HTTPS channel")
	fs.StringVar(&cfg.heartbeatURL, "heartbeat-url", envDefault("TECHSTACK_HEARTBEAT_URL", ""),
		"HTTPS heartbeat endpoint (prefer enrollment-file)")
	fs.StringVar(&cfg.inventoryURL, "inventory-url", envDefault("TECHSTACK_INVENTORY_URL", ""),
		"HTTPS inventory endpoint (prefer enrollment-file)")
	fs.StringVar(&cfg.agentTokenFile, "agent-token-file", envDefault("TECHSTACK_AGENT_TOKEN_FILE", ""),
		"root-readable bearer token file for the HTTPS channel")
	fs.StringVar(&cfg.accessManifest, "access-manifest", envDefault("TECHSTACK_ACCESS_MANIFEST", ""),
		"StackKit .stackkit/access.json path (comma-separated fallbacks accepted)")
	fs.StringVar(&cfg.publicIP, "public-ip", envDefault("TECHSTACK_AGENT_PUBLIC_IP", ""),
		"observed/configured public IP when it is not assigned to a local interface")
	cfg.agentToken = strings.TrimSpace(os.Getenv("TECHSTACK_AGENT_TOKEN"))
	fs.StringVar(&cfg.kombifyMeRelayURL, "kombify-me-relay-url", envDefault("KOMBIFY_ME_RELAY_URL", kombifyme.DefaultRelayURL),
		"kombify.me relay WebSocket URL; enabled only when KOMBIFY_ME_API_KEY is set")
	cfg.kombifyMeAPIKey = strings.TrimSpace(os.Getenv("KOMBIFY_ME_API_KEY"))

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg.transport = strings.ToLower(strings.TrimSpace(cfg.transport))
	if cfg.transport == "http" {
		cfg.transport = httpsScheme
	}
	if cfg.transport != "grpc-mtls" && cfg.transport != httpsScheme {
		return nil, fmt.Errorf("unsupported agent transport %q (want grpc-mtls or https)", cfg.transport)
	}
	if cfg.transport == httpsScheme {
		if cfg.enrollmentFile == "" && cfg.agentToken == "" && cfg.agentTokenFile == "" {
			return nil, fmt.Errorf("HTTPS agent mode requires --enrollment-file or --agent-token-file")
		}
		return cfg, nil
	}

	var missing []string
	if cfg.coreAddr == "" {
		missing = append(missing, "--core / TECHSTACK_AGENT_CORE_ADDR")
	}
	if cfg.agentID == "" {
		missing = append(missing, "--agent-id / TECHSTACK_AGENT_ID")
	}
	if cfg.certFile == "" {
		missing = append(missing, "--cert / TECHSTACK_AGENT_CERT_FILE")
	}
	if cfg.keyFile == "" {
		missing = append(missing, "--key / TECHSTACK_AGENT_KEY_FILE")
	}
	if cfg.caFile == "" {
		missing = append(missing, "--ca / TECHSTACK_AGENT_CA_FILE")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("agent mode is mTLS-only (fail-closed); missing: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// baselinePreChecks reports basic node capabilities after every registration
// so Core sees Docker/GPU availability without a rollout spec. Non-blocking:
// they inform placement, they don't gate the agent.
func baselinePreChecks() []core.PreCheckDefinition {
	return []core.PreCheckDefinition{
		{Type: "docker_version", Description: "Docker runtime available", Blocking: false},
		{Type: "gpu_available", Description: "GPU present", Blocking: false},
	}
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// runAgentMode runs the node-side agent daemon until SIGINT/SIGTERM.
func runAgentMode(ctx context.Context, args []string) error {
	cfg, err := parseAgentModeConfig(args)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log = log.With("service", "techstack-agent", "version", version)
	if cfg.transport == httpsScheme {
		return runHTTPAgentMode(ctx, cfg, log)
	}

	client, err := agent.NewClient(agent.Config{
		CoreAddress: cfg.coreAddr,
		AgentID:     cfg.agentID,
		CertFile:    cfg.certFile,
		KeyFile:     cfg.keyFile,
		CAFile:      cfg.caFile,
		Logger:      log,
	})
	if err != nil {
		return fmt.Errorf("create agent client: %w", err)
	}
	defer func() { _ = client.Close() }()

	var pusher *agent.MetricsPusher
	if !cfg.disableMetrics {
		cols := []collectors.Collector{collectors.NewSystemCollector(cfg.metricsInterval)}
		if containerCol, colErr := collectors.NewContainerCollector(cfg.metricsInterval); colErr != nil {
			log.Warn("container_collector_unavailable", "error", colErr.Error())
		} else {
			cols = append(cols, containerCol)
		}
		pusher = agent.NewMetricsPusher(client, cols, log)
	}

	supervisor, err := agent.NewSupervisor(agent.SupervisorConfig{
		Client:            client,
		Logger:            log,
		HeartbeatInterval: cfg.heartbeatInterval,
		Sampler:           agent.NewResourceSampler(cfg.samplerInterval, log),
		Pusher:            pusher,
		PreChecks:         agent.NewPreCheckRunner(baselinePreChecks()),
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}

	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	relayCtx, stopRelay := context.WithCancel(runCtx)
	var relayWG sync.WaitGroup
	if cfg.kombifyMeAPIKey != "" {
		relay, relayErr := kombifyme.NewRelay(kombifyme.RelayConfig{
			URL:     cfg.kombifyMeRelayURL,
			APIKey:  cfg.kombifyMeAPIKey,
			AgentID: cfg.agentID,
			Version: version,
			Logger:  log,
		})
		if relayErr != nil {
			stopRelay()
			return fmt.Errorf("configure kombify.me relay: %w", relayErr)
		}
		relayWG.Add(1)
		go func() {
			defer relayWG.Done()
			runKombifyMeRelay(relayCtx, relay, log)
		}()
		log.Info("kombify_me_relay_enabled", "url", cfg.kombifyMeRelayURL)
	}

	log.Info("agent_mode_starting", "core", cfg.coreAddr, "agent_id", cfg.agentID)
	err = supervisor.Run(runCtx)
	stopRelay()
	relayWG.Wait()
	if err != nil && runCtx.Err() != nil {
		log.Info("agent_mode_stopped", "reason", runCtx.Err().Error())
		return nil
	}
	return err
}

type kombifyMeRelaySession interface {
	RunSession(context.Context) error
}

func runKombifyMeRelay(ctx context.Context, relay kombifyMeRelaySession, log *slog.Logger) {
	backoff := agent.NewBackoff()
	for {
		err := relay.RunSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("kombify.me relay session ended")
		}
		var sessionErr *kombifyme.RelaySessionError
		if errors.As(err, &sessionErr) && sessionErr.ConnectedFor >= 30*time.Second {
			backoff.Reset()
		}
		delay := backoff.Next()
		log.Warn("kombify_me_relay_reconnecting", "error", err.Error(), "delay", delay.String(), "attempt", backoff.Attempt())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
