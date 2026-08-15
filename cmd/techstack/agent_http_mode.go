package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

const maxAgentSecretFileBytes = 1 << 20
const stackKitRuntimeConvergenceInterval = 5 * time.Minute
const runtimePackageConvergenceRetryInterval = 15 * time.Second

type currentStackKitCommandRuntime interface {
	EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error
	Execute(context.Context, *agentpb.StackKitCommand) *agentpb.StackKitResult
	FailureResult(*agentpb.StackKitCommand, error) *agentpb.StackKitResult
}

type runtimePackageConverger interface {
	EnsureTechstackRuntime(context.Context, agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error)
	EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error
}

type currentStackKitCommandExecutor struct {
	runtime currentStackKitCommandRuntime
	cfg     agentpkg.StackKitRuntimeBootstrapConfig
	ready   func() bool
}

func (executor currentStackKitCommandExecutor) Execute(ctx context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return executor.ExecuteStreaming(ctx, command, nil)
}

func (executor currentStackKitCommandExecutor) ExecuteStreaming(ctx context.Context, command *agentpb.StackKitCommand, sink func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	if executor.ready != nil && !executor.ready() {
		return executor.runtime.FailureResult(command, errors.New("runtime convergence is pending; retry after the Guard reports runtime readiness"))
	}
	if err := executor.runtime.EnsureRuntime(ctx, executor.cfg); err != nil {
		return executor.runtime.FailureResult(command, fmt.Errorf("converge current StackKits release before command: %w", err))
	}
	if streaming, ok := executor.runtime.(interface {
		ExecuteStreaming(context.Context, *agentpb.StackKitCommand, func(*agentpb.LogEntry)) *agentpb.StackKitResult
	}); ok {
		return streaming.ExecuteStreaming(ctx, command, sink)
	}
	return executor.runtime.Execute(ctx, command)
}

type agentEnrollmentFile struct {
	Data agentEnrollment `json:"data"`
	agentEnrollment
}

type agentEnrollment struct {
	WorkerID             string `json:"worker_id"`
	ServerID             string `json:"server_id"`
	RuntimeAgentID       string `json:"runtime_agent_id"`
	TenantID             string `json:"tenant_id"`
	OwnerID              string `json:"owner_id"`
	StackID              string `json:"stack_id"`
	LeaseID              string `json:"lease_id"`
	AgentToken           string `json:"agent_token"`
	HeartbeatURL         string `json:"heartbeat_url"`
	InventoryURL         string `json:"inventory_url"`
	CommandURL           string `json:"command_url"`
	CommandResultURL     string `json:"command_result_url"`
	StackKitReleaseURL   string `json:"stackkit_release_url"`
	AllowPrivateLANHTTP  bool   `json:"allow_private_lan_http"`
	PrivateLANHTTPOrigin string `json:"private_lan_http_origin"`
	ChannelBootstrap     struct {
		TenantID             string `json:"tenant_id"`
		OwnerID              string `json:"owner_id"`
		StackID              string `json:"stack_id"`
		LeaseID              string `json:"lease_id"`
		ServerID             string `json:"server_id"`
		RuntimeAgentID       string `json:"runtime_agent_id"`
		HeartbeatURL         string `json:"heartbeat_url"`
		InventoryURL         string `json:"inventory_url"`
		CommandURL           string `json:"command_url"`
		CommandResultURL     string `json:"command_result_url"`
		StackKitReleaseURL   string `json:"stackkit_release_url"`
		AllowPrivateLANHTTP  bool   `json:"allow_private_lan_http"`
		PrivateLANHTTPOrigin string `json:"private_lan_http_origin"`
	} `json:"channel_bootstrap"`
}

func runHTTPAgentMode(ctx context.Context, cfg *agentModeConfig, log *slog.Logger) error {
	enrollment, err := resolveHTTPAgentEnrollment(cfg)
	if err != nil {
		return err
	}
	manifestPaths := splitNonEmpty(cfg.accessManifest)
	collector := httpguard.NewSystemCollector(httpguard.SystemCollectorConfig{
		AccessManifestFiles: manifestPaths,
		PublicIP:            cfg.publicIP,
		// Unmanaged service discovery is on by default: a host without a
		// StackKit must still report what it is actually running. The kill
		// switch exists so a fleet operator can silence host probes without
		// waiting for an agent release.
		Discovery: httpguard.DiscoveryConfig{
			Disabled: !envBoolDefault("TECHSTACK_AGENT_SERVICE_DISCOVERY", true),
		},
	})
	interval := cfg.heartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	stackKitReleaseURL := firstCLIValue(enrollment.StackKitReleaseURL, enrollment.ChannelBootstrap.StackKitReleaseURL, derivedAgentReleaseURL(enrollment.HeartbeatURL))
	privateLANHTTPOrigin := firstCLIValue(enrollment.PrivateLANHTTPOrigin, enrollment.ChannelBootstrap.PrivateLANHTTPOrigin)
	stackKitExecutor := agentpkg.NewStackKitExecutorFromEnv()
	techstackRuntimeConfig := agentpkg.TechstackRuntimeConvergenceConfig{
		URL:                  derivedAgentBinaryURL(enrollment.HeartbeatURL),
		AgentToken:           enrollment.AgentToken,
		RuntimeAgentID:       enrollment.RuntimeAgentID,
		TenantID:             enrollment.TenantID,
		AgentPath:            envDefault("TECHSTACK_AGENT_EXECUTABLE_PATH", "/usr/local/bin/techstack"),
		OperationsPath:       envDefault("TECHSTACK_OPERATIONS_EXECUTABLE_PATH", "/usr/local/libexec/techstack-stackkit-operations"),
		PrivateLANHTTPOrigin: privateLANHTTPOrigin,
	}
	stackKitRuntimeConfig := agentpkg.StackKitRuntimeBootstrapConfig{
		URL: stackKitReleaseURL, AgentToken: enrollment.AgentToken,
		RuntimeAgentID: enrollment.RuntimeAgentID, TenantID: enrollment.TenantID,
		PrivateLANHTTPOrigin: privateLANHTTPOrigin,
	}
	var runtimeReady atomic.Bool
	runtimeConvergence := runtimeconvergence.NewTracker(time.Now().UTC())
	runCtx, stop := signalContext(ctx)
	defer stop()
	client, err := httpguard.New(httpguard.Config{
		HeartbeatURL:     enrollment.HeartbeatURL,
		InventoryURL:     enrollment.InventoryURL,
		CommandURL:       enrollment.CommandURL,
		CommandResultURL: enrollment.CommandResultURL,
		AgentToken:       enrollment.AgentToken,
		RuntimeAgentID:   enrollment.RuntimeAgentID,
		ServerID:         enrollment.ServerID,
		TenantID:         enrollment.TenantID,
		OwnerID:          enrollment.OwnerID,
		StackID:          enrollment.StackID,
		LeaseID:          enrollment.LeaseID,
		AgentVersion:     version,
		Interval:         interval,
		Collector:        collector,
		CommandExecutor: currentStackKitCommandExecutor{
			runtime: stackKitExecutor,
			cfg:     stackKitRuntimeConfig,
			ready:   runtimeReady.Load,
		},
		RuntimeConvergence: runtimeConvergence.Snapshot,
		OnFirstHeartbeat: func() {
			go convergeRuntimePackages(runCtx, stop, stackKitExecutor, techstackRuntimeConfig, stackKitRuntimeConfig, stackKitRuntimeConvergenceInterval, runtimeReady.Store, log, runtimeConvergence.Set)
		},
		Logger:               log,
		PrivateLANHTTPOrigin: privateLANHTTPOrigin,
	})
	if err != nil {
		return fmt.Errorf("configure HTTPS guard: %w", err)
	}
	log.Info("agent_mode_starting", "transport", httpsScheme, "agent_id", enrollment.RuntimeAgentID)
	if err := client.Run(runCtx); err != nil {
		return err
	}
	log.Info("agent_mode_stopped", "transport", httpsScheme)
	return nil
}

func resolveHTTPAgentEnrollment(cfg *agentModeConfig) (agentEnrollment, error) {
	var enrollment agentEnrollment
	if cfg == nil {
		return enrollment, errors.New("agent config is required")
	}
	if path := strings.TrimSpace(cfg.enrollmentFile); path != "" {
		loaded, err := loadAgentEnrollment(path)
		if err != nil {
			return enrollment, err
		}
		enrollment = loaded
	}
	if path := strings.TrimSpace(cfg.agentTokenFile); path != "" {
		data, err := readSecretFile(path)
		if err != nil {
			return enrollment, fmt.Errorf("read agent token file: %w", err)
		}
		enrollment.AgentToken = strings.TrimSpace(string(data))
	} else if cfg.agentToken != "" {
		enrollment.AgentToken = cfg.agentToken
	}
	enrollment.RuntimeAgentID = firstCLIValue(cfg.agentID, enrollment.RuntimeAgentID, enrollment.WorkerID, enrollment.ChannelBootstrap.RuntimeAgentID, os.Getenv("TECHSTACK_RUNTIME_AGENT_ID"))
	enrollment.ServerID = firstCLIValue(enrollment.ServerID, enrollment.ChannelBootstrap.ServerID, os.Getenv("TECHSTACK_SERVER_ID"))
	enrollment.TenantID = firstCLIValue(enrollment.TenantID, enrollment.ChannelBootstrap.TenantID, os.Getenv("TECHSTACK_TENANT_ID"))
	enrollment.OwnerID = firstCLIValue(enrollment.OwnerID, enrollment.ChannelBootstrap.OwnerID, os.Getenv("TECHSTACK_OWNER_ID"))
	enrollment.StackID = firstCLIValue(enrollment.StackID, enrollment.ChannelBootstrap.StackID, os.Getenv("TECHSTACK_STACK_ID"))
	enrollment.LeaseID = firstCLIValue(enrollment.LeaseID, enrollment.ChannelBootstrap.LeaseID, os.Getenv("TECHSTACK_LEASE_ID"))
	enrollment.HeartbeatURL = firstCLIValue(cfg.heartbeatURL, enrollment.HeartbeatURL, enrollment.ChannelBootstrap.HeartbeatURL)
	enrollment.InventoryURL = firstCLIValue(cfg.inventoryURL, enrollment.InventoryURL, enrollment.ChannelBootstrap.InventoryURL)
	enrollment.CommandURL = firstCLIValue(enrollment.CommandURL, enrollment.ChannelBootstrap.CommandURL, derivedAgentControlURL(enrollment.HeartbeatURL, "commands/next"))
	enrollment.CommandResultURL = firstCLIValue(enrollment.CommandResultURL, enrollment.ChannelBootstrap.CommandResultURL, derivedAgentControlURL(enrollment.HeartbeatURL, "commands/result"))
	enrollment.StackKitReleaseURL = firstCLIValue(enrollment.StackKitReleaseURL, enrollment.ChannelBootstrap.StackKitReleaseURL, derivedAgentReleaseURL(enrollment.HeartbeatURL))
	missing := []string{}
	for label, value := range map[string]string{
		"runtime_agent_id": enrollment.RuntimeAgentID,
		"agent_token":      enrollment.AgentToken,
		"heartbeat_url":    enrollment.HeartbeatURL,
		"inventory_url":    enrollment.InventoryURL,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, label)
		}
	}
	if len(missing) > 0 {
		return enrollment, fmt.Errorf("HTTPS agent enrollment is incomplete: missing %s", strings.Join(missing, ", "))
	}
	return enrollment, nil
}

func derivedAgentReleaseURL(heartbeatURL string) string {
	heartbeatURL = strings.TrimSpace(heartbeatURL)
	marker := "/api/v1/workers/"
	index := strings.Index(heartbeatURL, marker)
	if index < 0 {
		return ""
	}
	return strings.TrimRight(heartbeatURL[:index], "/") + "/api/v1/agent/stackkit-release/" + runtime.GOOS + "/" + runtime.GOARCH
}

func derivedAgentBinaryURL(heartbeatURL string) string {
	heartbeatURL = strings.TrimSpace(heartbeatURL)
	marker := "/api/v1/workers/"
	index := strings.Index(heartbeatURL, marker)
	if index < 0 {
		return ""
	}
	return strings.TrimRight(heartbeatURL[:index], "/") + "/api/v1/agent/binary/" + runtime.GOOS + "/" + runtime.GOARCH
}

func convergeRuntimePackages(
	ctx context.Context,
	stop context.CancelFunc,
	executor runtimePackageConverger,
	techstackCfg agentpkg.TechstackRuntimeConvergenceConfig,
	stackKitCfg agentpkg.StackKitRuntimeBootstrapConfig,
	interval time.Duration,
	markReady func(bool),
	log *slog.Logger,
	statusPublishers ...func(runtimeconvergence.Snapshot),
) {
	if interval < time.Minute {
		interval = time.Minute
	}
	ready := false
	for {
		status := convergeRuntimePackagesOnceStatus(ctx, stop, executor, techstackCfg, stackKitCfg, log)
		for _, publish := range statusPublishers {
			if publish != nil {
				publish(status.snapshot)
			}
		}
		keepRunning, converged := status.keepRunning, status.converged
		if !keepRunning {
			return
		}
		if converged && !ready {
			ready = true
			if markReady != nil {
				markReady(true)
			}
		}
		delay := interval
		if !ready {
			delay = runtimePackageConvergenceRetryInterval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

type runtimePackageConvergenceStatus struct {
	keepRunning bool
	converged   bool
	snapshot    runtimeconvergence.Snapshot
}

func convergeRuntimePackagesOnce(
	ctx context.Context,
	stop context.CancelFunc,
	executor runtimePackageConverger,
	techstackCfg agentpkg.TechstackRuntimeConvergenceConfig,
	stackKitCfg agentpkg.StackKitRuntimeBootstrapConfig,
	log *slog.Logger,
) (bool, bool) {
	status := convergeRuntimePackagesOnceStatus(ctx, stop, executor, techstackCfg, stackKitCfg, log)
	return status.keepRunning, status.converged
}

func convergeRuntimePackagesOnceStatus(
	ctx context.Context,
	stop context.CancelFunc,
	executor runtimePackageConverger,
	techstackCfg agentpkg.TechstackRuntimeConvergenceConfig,
	stackKitCfg agentpkg.StackKitRuntimeBootstrapConfig,
	log *slog.Logger,
) runtimePackageConvergenceStatus {
	observedAt := time.Now().UTC()
	result, err := executor.EnsureTechstackRuntime(ctx, techstackCfg)
	techstackReady := err == nil
	if err != nil {
		log.Warn("techstack_runtime_convergence_failed", "error", err.Error())
	} else if result.AgentUpdated {
		log.Info("techstack_runtime_updated", "sha256", result.SHA256, "restart", true)
		pending := runtimeconvergence.Aggregate(observedAt,
			runtimeconvergence.Component{Name: runtimeconvergence.TechstackRuntimeComponent, State: runtimeconvergence.ComponentPending, ObservedAt: observedAt, ErrorCode: runtimeconvergence.AgentRestartRequiredError},
			runtimeconvergence.Component{Name: runtimeconvergence.StackKitsRuntimeComponent, State: runtimeconvergence.ComponentPending, ObservedAt: observedAt},
		)
		stop()
		return runtimePackageConvergenceStatus{snapshot: pending}
	}
	stackKitErr := executor.EnsureRuntime(ctx, stackKitCfg)
	if stackKitErr != nil {
		log.Warn("stackkit_runtime_convergence_failed", "error", stackKitErr.Error())
	}
	state := runtimeconvergence.Aggregate(observedAt,
		runtimeconvergence.Component{
			Name: runtimeconvergence.TechstackRuntimeComponent,
			State: func() string {
				if techstackReady {
					return runtimeconvergence.ComponentReady
				}
				return runtimeconvergence.ComponentFailed
			}(),
			ObservedAt: observedAt,
			ErrorCode: func() string {
				if !techstackReady {
					return runtimeconvergence.TechstackRuntimeUnavailableError
				}
				return ""
			}(),
		},
		runtimeconvergence.Component{
			Name: runtimeconvergence.StackKitsRuntimeComponent,
			State: func() string {
				if stackKitErr == nil {
					return runtimeconvergence.ComponentReady
				}
				return runtimeconvergence.ComponentFailed
			}(),
			ObservedAt: observedAt,
			ErrorCode: func() string {
				if stackKitErr != nil {
					return runtimeconvergence.StackKitsRuntimeUnavailableError
				}
				return ""
			}(),
		},
	)
	return runtimePackageConvergenceStatus{keepRunning: true, converged: techstackReady && stackKitErr == nil, snapshot: state}
}

func derivedAgentControlURL(heartbeatURL, suffix string) string {
	heartbeatURL = strings.TrimSpace(heartbeatURL)
	base := strings.TrimSuffix(heartbeatURL, "/heartbeat")
	if heartbeatURL == "" || base == heartbeatURL {
		return ""
	}
	return base + "/" + strings.TrimPrefix(suffix, "/")
}

func loadAgentEnrollment(path string) (agentEnrollment, error) {
	data, err := readSecretFile(path)
	if err != nil {
		return agentEnrollment{}, fmt.Errorf("read enrollment file: %w", err)
	}
	var wrapped agentEnrollmentFile
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return agentEnrollment{}, fmt.Errorf("parse enrollment file: %w", err)
	}
	if wrapped.Data.RuntimeAgentID != "" || wrapped.Data.AgentToken != "" {
		return wrapped.Data, nil
	}
	return wrapped.agentEnrollment, nil
}

func readSecretFile(path string) ([]byte, error) {
	directory, name, err := rootedSecretFileLocation(path)
	if err != nil {
		return nil, err
	}
	// The directory is an intentional operator-supplied configuration value,
	// normalized to an absolute path. OpenRoot confines the subsequent basename
	// lookup so symlinks and traversal cannot escape that directory.
	root, err := os.OpenRoot(directory) // #nosec G703 -- see confinement above.
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	secret, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = secret.Close() }()
	info, err := secret.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s permissions must not grant group/world access", path)
	}
	if info.Size() > maxAgentSecretFileBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte secret file limit", path, maxAgentSecretFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(secret, maxAgentSecretFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAgentSecretFileBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte secret file limit", path, maxAgentSecretFileBytes)
	}
	return data, nil
}

func rootedSecretFileLocation(path string) (directory, name string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", errors.New("secret file path is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", fmt.Errorf("resolve secret file path: %w", err)
	}
	name = filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) {
		return "", "", errors.New("secret file path must name a file")
	}
	return filepath.Dir(absolute), name, nil
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstCLIValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

var signalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
}
