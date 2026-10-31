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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/substrate"
	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/devicereadiness"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardexec"
	"github.com/kombifyio/techstack/pkg/privatechannel"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/sdnotify"
)

const maxAgentSecretFileBytes = 1 << 20

// identityMaintenanceInterval paces key-age checks and, for a Guard still on
// the bearer credential, the retry of host-key binding.
const identityMaintenanceInterval = 10 * time.Minute
const stackKitRuntimeConvergenceInterval = 5 * time.Minute
const runtimePackageConvergenceRetryInterval = 15 * time.Second

// runtimePackageConvergenceMaxRetryInterval bounds the backoff of a Guard whose
// runtime packages have not converged yet. It equals the control plane's
// StackKits download budget window (Retry-After 900), so a refused or
// rate-limited node asks at most once per window instead of every 15 s for the
// token lifetime (platform-f4ine: about 33,000 refused downloads from one node
// in 13 days).
const runtimePackageConvergenceMaxRetryInterval = 15 * time.Minute

// runtimePackageConvergenceRetryDelay doubles the first retry interval for each
// consecutive failed attempt before the first convergence: 15 s, 30 s, 1 min,
// 2 min, 4 min, 8 min, then 15 min until the packages converge.
func runtimePackageConvergenceRetryDelay(consecutiveFailures int) time.Duration {
	delay := runtimePackageConvergenceRetryInterval
	for i := 1; i < consecutiveFailures && delay < runtimePackageConvergenceMaxRetryInterval; i++ {
		delay *= 2
	}
	return min(delay, runtimePackageConvergenceMaxRetryInterval)
}

type currentStackKitCommandRuntime interface {
	EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error
	Execute(context.Context, *agentpb.StackKitCommand) *agentpb.StackKitResult
	FailureResult(*agentpb.StackKitCommand, error) *agentpb.StackKitResult
}

type runtimePackageConverger interface {
	EnsureTechstackRuntime(context.Context, agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error)
	EnsureRuntimeEvidence(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) (agentpkg.StackKitRuntimeConvergenceResult, error)
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
	GuardRole          string `json:"guard_role"`
	WorkerID           string `json:"worker_id"`
	ServerID           string `json:"server_id"`
	RuntimeAgentID     string `json:"runtime_agent_id"`
	TenantID           string `json:"tenant_id"`
	OwnerID            string `json:"owner_id"`
	StackID            string `json:"stack_id"`
	LeaseID            string `json:"lease_id"`
	AgentToken         string `json:"agent_token"`
	HeartbeatURL       string `json:"heartbeat_url"`
	InventoryURL       string `json:"inventory_url"`
	CommandURL         string `json:"command_url"`
	CommandResultURL   string `json:"command_result_url"`
	StackKitReleaseURL string `json:"stackkit_release_url"`
	PrivateLANOrigin   string `json:"private_lan_origin"`
	PrivateLANPin      string `json:"private_lan_pin"`
	IdentityTokenURL   string `json:"identity_token_url"`
	IdentityBindURL    string `json:"identity_bind_url"`
	IdentityRotateURL  string `json:"identity_rotate_url"`
	ChannelBootstrap   struct {
		IdentityTokenURL   string `json:"identity_token_url"`
		IdentityBindURL    string `json:"identity_bind_url"`
		IdentityRotateURL  string `json:"identity_rotate_url"`
		TenantID           string `json:"tenant_id"`
		OwnerID            string `json:"owner_id"`
		StackID            string `json:"stack_id"`
		LeaseID            string `json:"lease_id"`
		ServerID           string `json:"server_id"`
		RuntimeAgentID     string `json:"runtime_agent_id"`
		HeartbeatURL       string `json:"heartbeat_url"`
		InventoryURL       string `json:"inventory_url"`
		CommandURL         string `json:"command_url"`
		CommandResultURL   string `json:"command_result_url"`
		StackKitReleaseURL string `json:"stackkit_release_url"`
		PrivateLANOrigin   string `json:"private_lan_origin"`
		PrivateLANPin      string `json:"private_lan_pin"`
	} `json:"channel_bootstrap"`
	// BindingSigningKeys pins the control plane's command-binding keys. They
	// arrive with the pairing redemption and are read only from the enrollment
	// file, never from a poll or heartbeat response.
	BindingSigningKeys []guardbinding.PinnedKey `json:"binding_signing_keys,omitempty"`
}

func runHTTPAgentMode(ctx context.Context, cfg *agentModeConfig, log *slog.Logger) (result error) {
	runCtx, stop := signalContext(ctx)
	defer stop()
	cfg.stateDir = resolveGuardStateDir(cfg.stateDir)
	manager, stateErr := hostidentity.Open(cfg.stateDir)
	if stateErr != nil {
		return fmt.Errorf("admit Guard identity custody: %w", stateErr)
	}
	if manager != nil && strings.TrimSpace(cfg.enrollmentFile) != "" {
		resumeBootstrapEnrollment(runCtx, manager, cfg.enrollmentFile, log)
	}
	if manager != nil && manager.Record().State == hostidentity.StateRevoked {
		// A revoked Guard holds no secrets and starts no work: it finishes
		// destroying them (a restart can interrupt that) and idles until it is
		// paired again.
		decommissionRevokedGuard(manager, cfg, log)
		return holdUntilStopped(runCtx, hostidentity.ErrRevoked, log, cfg.progress)
	}
	if cfg.waitForEnrollment {
		if err := waitForInstanceEnrollment(runCtx, cfg, log); err != nil {
			if runCtx.Err() != nil {
				return nil
			}
			return err
		}
		// Enrollment runs in the installer process. Reload its confirmed record
		// and key instead of writing over it with the pre-enrollment Manager.
		manager, err := hostidentity.Open(cfg.stateDir)
		if err != nil {
			return err
		}
		return runEnrolledHTTPAgentMode(runCtx, stop, cfg, log, manager)
	}
	return runEnrolledHTTPAgentMode(runCtx, stop, cfg, log, manager)
}

func runEnrolledHTTPAgentMode(runCtx context.Context, stop context.CancelFunc, cfg *agentModeConfig, log *slog.Logger, manager *hostidentity.Manager) (result error) {
	if manager != nil && manager.Record().State == hostidentity.StateRevoked {
		decommissionRevokedGuard(manager, cfg, log)
		return holdUntilStopped(runCtx, hostidentity.ErrRevoked, log, cfg.progress)
	}
	if manager == nil {
		return errors.New("Guard identity custody is required")
	}
	custodyRoot, err := manager.OpenCustodyRoot()
	if err != nil {
		return fmt.Errorf("retain Guard custody: %w", err)
	}
	defer custodyRoot.Close()
	enrollment, err := resolveHTTPAgentEnrollmentWithIdentity(cfg, manager)
	if err != nil {
		return err
	}
	// Before any request: a LAN-paired Guard reaches its control plane only
	// through the pinned bridge key.
	if err := pinPrivateLAN(enrollment); err != nil {
		return fmt.Errorf("pin the private LAN bridge: %w", err)
	}
	sdnotify.Status("establishing kombify Guard identity")
	identity := establishGuardIdentity(runCtx, manager, enrollment, log, cfg.progress)
	if identity.held != nil {
		if runCtx.Err() != nil {
			return nil
		}
		if errors.Is(identity.held, hostidentity.ErrRevoked) {
			decommissionRevokedGuard(manager, cfg, log)
		}
		return holdUntilStopped(runCtx, identity.held, log, cfg.progress)
	}
	if identity.client != nil {
		go identity.client.Maintain(runCtx, identityMaintenanceInterval)
	}
	// A revocation observed while running stops every loop and any command in
	// flight; the Guard then idles until it is paired again.
	execCtx, cancelExec := context.WithCancel(runCtx)
	defer cancelExec()
	if manager != nil {
		go watchIdentityRevocation(execCtx, manager, cancelExec)
	}
	defer func() {
		if revoked := guardIdentityRevoked(manager); revoked && runCtx.Err() == nil {
			log.Error("guard_identity_revoked_stopping_execution")
			decommissionRevokedGuard(manager, cfg, log)
			result = holdUntilStopped(runCtx, hostidentity.ErrRevoked, log, cfg.progress)
		}
	}()
	guardHTTP := identity.httpClient(15 * time.Second)
	downloadHTTP := identity.httpClient(5 * time.Minute)
	sdnotify.Status("connected to the control plane")
	_, hypervisorErr := os.Stat("/etc/pve/local")
	if enrollment.GuardRole == "substrate" {
		if hypervisorErr != nil {
			return errors.New("substrate enrollment requires a Proxmox host")
		}
		// The substrate executor uses only the locally configured API token.
		// No StackKits executor or package convergence runs on the hypervisor:
		// only the Guard's own executable converges, from the first accepted
		// heartbeat on, which is also what confirms an update trial.
		var substrateClient *httpguard.Client
		selfUpdate := guardexec.NewSubstrateSelfUpdate(guardexec.Options{
			Version: version, StateDir: envDefault("TECHSTACK_GUARD_EXECUTOR_STATE_DIR", ""), Log: log,
		})
		guardConfig := httpguard.Config{
			HeartbeatURL: enrollment.HeartbeatURL, InventoryURL: enrollment.InventoryURL,
			AgentToken: enrollment.AgentToken, RuntimeAgentID: enrollment.RuntimeAgentID,
			ServerID: enrollment.ServerID, TenantID: enrollment.TenantID,
			OwnerID: enrollment.OwnerID, StackID: enrollment.StackID,
			AgentVersion: version, Interval: cfg.heartbeatInterval, Logger: log,
			Collector:  httpguard.NewSystemCollector(httpguard.SystemCollectorConfig{HostHardware: (&localHardwareCache{observe: devicereadiness.ObserveLocalHardware}).current}),
			HTTPClient: guardHTTP, OnProgress: cfg.progress,
			Posture: func() *guardbinding.Posture {
				status := guardexec.ReadUpdateStatus(selfUpdate.HandoffDir())
				return &guardbinding.Posture{Mode: guardbinding.PostureLegacyRoot, UpdateState: status.State, UpdateVersion: status.Version, UpdateReason: status.Reason}
			},
			OnFirstHeartbeat: func() {
				if err := guardexec.ConfirmTrial(selfUpdate.HandoffDir(), version, time.Now()); err != nil {
					log.Warn("guard_update_trial_confirmation_failed", "error", err.Error())
				}
				go convergeSubstrateGuard(execCtx, stop, selfUpdate, substrateClient.HoldWorker, agentpkg.TechstackRuntimeConvergenceConfig{
					URL:            derivedAgentBinaryURL(enrollment.HeartbeatURL),
					AgentToken:     enrollment.AgentToken,
					RuntimeAgentID: enrollment.RuntimeAgentID,
					TenantID:       enrollment.TenantID,
					AgentPath:      envDefault("TECHSTACK_AGENT_EXECUTABLE_PATH", "/usr/local/bin/techstack"),
					Version:        version,
					HTTPClient:     downloadHTTP,
				}, stackKitRuntimeConvergenceInterval, log)
			},
		}
		localConfigPath := envDefault("TECHSTACK_SUBSTRATE_CONFIG", "/var/lib/kombify/substrate.json")
		localConfig, localErr := substrate.LoadConfig(localConfigPath)
		if localErr == nil {
			native, nativeErr := substrate.NewClient(localConfig)
			if nativeErr != nil {
				return nativeErr
			}
			guardConfig.Collector = substrateInventoryCollector{base: guardConfig.Collector, client: native, config: localConfig, accelerator: &localAcceleratorCache{observe: devicereadiness.ObserveLocalAccelerator}}
			guardConfig.WorkerExecutor = substrate.Executor{Client: native, WorkerID: enrollment.RuntimeAgentID, TenantID: enrollment.TenantID}
			guardConfig.WorkerCommandURL = strings.TrimSuffix(enrollment.CommandURL, "/commands/next") + "/provider/next"
			guardConfig.WorkerResultURL = strings.TrimSuffix(enrollment.CommandResultURL, "/commands/result") + "/provider/result"
		} else if !errors.Is(localErr, os.ErrNotExist) {
			return localErr
		} else {
			log.Info("substrate_execution_pending_local_configuration")
		}
		var err error
		substrateClient, err = httpguard.New(guardConfig)
		if err != nil {
			return err
		}
		return substrateClient.Run(execCtx)
	}
	if hypervisorErr == nil {
		return errors.New("Proxmox requires an explicit substrate enrollment")
	}
	execution := selectGuardExecution(runCtx, log)
	tryMigration := func(ctx context.Context) bool {
		return execution.mode == guardModeLegacyRoot && guardLayoutMigrationEligible("") &&
			signedGuardReleaseAvailable(ctx, downloadHTTP, enrollment, log) && migrateGuardLayout(ctx, log)
	}
	// Signed layout migration is package convergence. Start it after the first
	// authenticated heartbeat, with the existing convergence readiness fence.
	manifestPaths := splitNonEmpty(cfg.accessManifest)
	collector := httpguard.NewSystemCollector(httpguard.SystemCollectorConfig{
		AccessManifestFiles: manifestPaths,
		ReadManifest:        execution.manifestReader(),
		// The machine's own hardware (CPU level, GPU, disk, chassis) is read from
		// procfs and sysfs and repeated only every half hour in the background.
		HostHardware: (&localHardwareCache{observe: devicereadiness.ObserveLocalHardware}).current,
		PublicIP:     cfg.publicIP,
		// Unmanaged service discovery is on by default: a host without a
		// StackKit must still report what it is actually running. The kill
		// switch exists so a fleet operator can silence host probes without
		// waiting for an agent release.
		// The HTTP fingerprint only reaches the host's own published container
		// ports; its switch silences those requests on its own.
		Discovery: httpguard.DiscoveryConfig{
			Disabled:           !envBoolDefault("TECHSTACK_AGENT_SERVICE_DISCOVERY", true),
			DisableFingerprint: !envBoolDefault("TECHSTACK_AGENT_SERVICE_FINGERPRINT", true),
			Probe:              execution.dockerProbe(),
		},
	})
	interval := cfg.heartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	stackKitReleaseURL := firstCLIValue(enrollment.StackKitReleaseURL, enrollment.ChannelBootstrap.StackKitReleaseURL, derivedAgentReleaseURL(enrollment.HeartbeatURL))
	stackKitExecutor := execution.runtime
	techstackRuntimeConfig := agentpkg.TechstackRuntimeConvergenceConfig{
		URL:            derivedAgentBinaryURL(enrollment.HeartbeatURL),
		AgentToken:     enrollment.AgentToken,
		RuntimeAgentID: enrollment.RuntimeAgentID,
		TenantID:       enrollment.TenantID,
		AgentPath:      envDefault("TECHSTACK_AGENT_EXECUTABLE_PATH", "/usr/local/bin/techstack"),
		OperationsPath: envDefault("TECHSTACK_OPERATIONS_EXECUTABLE_PATH", "/usr/local/libexec/techstack-stackkit-operations"),
		Version:        version,
		HTTPClient:     downloadHTTP,
	}
	stackKitRuntimeConfig := agentpkg.StackKitRuntimeBootstrapConfig{
		URL: stackKitReleaseURL, AgentToken: enrollment.AgentToken,
		RuntimeAgentID: enrollment.RuntimeAgentID, TenantID: enrollment.TenantID,
		HTTPClient: downloadHTTP,
	}
	var runtimeReady atomic.Bool
	runtimeConvergence := runtimeconvergence.NewTracker(time.Now().UTC())
	relay, err := newAgentRelayLifecycle(execCtx, cfg, enrollment.RuntimeAgentID, log)
	if err != nil {
		return err
	}
	defer relay.Stop()
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
		// Once the control plane's key is pinned, a command binding without
		// its signature is refused.
		BindingSigningKeys: enrollment.BindingSigningKeys,
		// A revocation the identity transport observed denies the next effect
		// at once, without waiting for the revocation watcher to stop the loop.
		AuthorityRevoked: func() bool { return guardIdentityRevoked(manager) },
		// Only a privileged executor without NoNewPrivileges whose pinned
		// StackKits ships `stackkit host` advertises host maintenance.
		HostMaintenanceCapabilities: stackKitExecutor.HostMaintenanceCapabilities,
		ApplicationCapabilities:     stackKitExecutor.ApplicationCapabilities,
		CommandExecutor: currentStackKitCommandExecutor{
			runtime: stackKitExecutor,
			cfg:     stackKitRuntimeConfig,
			ready:   runtimeReady.Load,
		},
		RuntimeConvergence: runtimeConvergence.Snapshot,
		Posture: func() *guardbinding.Posture {
			posture := execution.posture()
			// An enrollment that carried no signing key cannot have written a
			// pin. Do not claim a pin from the enrollment alone.
			if posture != nil && posture.BindingPin == "" && len(enrollment.BindingSigningKeys) == 0 {
				posture.BindingPin = guardbinding.BindingPinAbsent
			}
			return posture
		},
		OnFirstHeartbeat: func() {
			confirmGuardUpdateTrial(execution, log)
			relay.Start()
			startGuardReach(execCtx, cfg, enrollment, identity, manager, log)
			startGuardPosture(execCtx, custodyRoot, enrollment, identity, manager, execution, log)
			startGuardTelemetry(execCtx, cfg, custodyRoot, enrollment, identity, execution, log)
			if forwarder := newHTTPSNoticeForwarder(enrollment, identity); forwarder != nil {
				startUserNotificationListener(execCtx, cfg, forwarder, log)
			}
			go convergeRuntimePackages(execCtx, stop, stackKitExecutor, techstackRuntimeConfig, stackKitRuntimeConfig, stackKitRuntimeConvergenceInterval, runtimeReady.Store, log, tryMigration, runtimeConvergence.Set)
		},
		Logger:     log,
		HTTPClient: guardHTTP,
		OnProgress: cfg.progress,
	})
	if err != nil {
		return fmt.Errorf("configure HTTPS guard: %w", err)
	}
	if execution.mode == guardModeSplit {
		// The privileged side reaches the control plane through this daemon only.
		opsHTTP := identity.httpClient(2 * time.Minute)
		go func() {
			if err := serveStackKitOperationsProxy(execCtx, envDefault(stackKitOperationsSocketEnv, guardexec.DefaultOperationsSocket), enrollment, opsHTTP, log); err != nil {
				log.Warn("guard_operations_proxy_stopped", "error", err.Error())
			}
		}()
	}
	enableGuardDurableOperations(custodyRoot, client, stackKitReconciler{executor: currentStackKitCommandExecutor{
		runtime: stackKitExecutor, cfg: stackKitRuntimeConfig, ready: runtimeReady.Load,
	}}, log)
	log.Info("agent_mode_starting", "transport", httpsScheme, "agent_id", enrollment.RuntimeAgentID)
	if err := client.Run(execCtx); err != nil {
		return err
	}
	log.Info("agent_mode_stopped", "transport", httpsScheme)
	return nil
}

func resolveHTTPAgentEnrollment(cfg *agentModeConfig) (agentEnrollment, error) {
	return resolveHTTPAgentEnrollmentWithIdentity(cfg, nil)
}

func resolveHTTPAgentEnrollmentWithIdentity(cfg *agentModeConfig, manager *hostidentity.Manager) (agentEnrollment, error) {
	var enrollment agentEnrollment
	if cfg == nil {
		return enrollment, errors.New("agent config is required")
	}
	if path := strings.TrimSpace(cfg.enrollmentFile); path != "" {
		var loaded agentEnrollment
		var err error
		if manager != nil {
			var file *os.File
			file, err = manager.OpenEnrollment(path)
			if err == nil {
				var data []byte
				data, err = readSecretHandle(file, path)
				file.Close()
				if err == nil {
					loaded, err = parseAgentEnrollment(data)
				}
			}
		} else {
			loaded, err = loadAgentEnrollment(hostidentity.LocateEnrollment(path, cfg.stateDir))
		}
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

// convergeSubstrateGuard keeps a substrate Guard on the signed release its control
// plane publishes. A started update ends this process: the trial supervisor
// restarts the service, and systemd stops only the Guard's own control group, so
// the guests in qemu.slice never notice. A provider command in flight finishes
// and reports its result first.
func convergeSubstrateGuard(ctx context.Context, stop context.CancelFunc, selfUpdate *guardexec.SubstrateSelfUpdate, holdWorker func(context.Context) (func(), error), cfg agentpkg.TechstackRuntimeConvergenceConfig, interval time.Duration, log *slog.Logger) {
	failures := 0
	for {
		result, err := selfUpdate.EnsureGuardExecutable(ctx, cfg, holdWorker)
		delay := interval
		var denied *guardexec.UpdateDeniedError
		switch {
		case err == nil && result.AgentUpdated:
			log.Info("techstack_runtime_updated", "version", result.Version, "sha256", result.SHA256, "restart", true)
			stop()
			return
		case err == nil:
			failures = 0
		case errors.As(err, &denied):
			// A refused candidate leaves the installed Guard exactly as it was.
			failures = 0
			log.Warn("guard_update_denied", "code", denied.Code, "reason", denied.Message)
		default:
			failures++
			delay = runtimePackageConvergenceRetryDelay(failures)
			log.Warn("techstack_runtime_convergence_failed", "error", err.Error(), "attempt", failures, "delay", delay.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
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
	tryMigration func(context.Context) bool,
	statusPublishers ...func(runtimeconvergence.Snapshot),
) {
	if interval < time.Minute {
		interval = time.Minute
	}
	ready := false
	failures := 0
	for {
		status := convergeRuntimePackagesOnceStatus(ctx, stop, executor, techstackCfg, stackKitCfg, log, tryMigration)
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
			ready, failures = true, 0
			if markReady != nil {
				markReady(true)
			}
		}
		delay := interval
		if !ready {
			failures++
			delay = runtimePackageConvergenceRetryDelay(failures)
			log.Info("runtime_convergence_retry_scheduled", "attempt", failures, "delay", delay.String())
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

func convergeRuntimePackagesOnceStatus(
	ctx context.Context,
	stop context.CancelFunc,
	executor runtimePackageConverger,
	techstackCfg agentpkg.TechstackRuntimeConvergenceConfig,
	stackKitCfg agentpkg.StackKitRuntimeBootstrapConfig,
	log *slog.Logger,
	migration ...func(context.Context) bool,
) runtimePackageConvergenceStatus {
	observedAt := time.Now().UTC()
	if len(migration) != 0 && migration[0] != nil && migration[0](ctx) {
		stop()
		return runtimePackageConvergenceStatus{snapshot: runtimeconvergence.Aggregate(observedAt,
			runtimeconvergence.Component{Name: runtimeconvergence.TechstackRuntimeComponent, State: runtimeconvergence.ComponentPending, ObservedAt: observedAt, ErrorCode: runtimeconvergence.AgentRestartRequiredError},
			runtimeconvergence.Component{Name: runtimeconvergence.StackKitsRuntimeComponent, State: runtimeconvergence.ComponentPending, ObservedAt: observedAt})}
	}
	result, err := executor.EnsureTechstackRuntime(ctx, techstackCfg)
	techstackReady := err == nil
	var denied *guardexec.UpdateDeniedError
	if errors.As(err, &denied) {
		// A refused candidate leaves the installed Guard exactly as it was, so the
		// runtime is as ready as it was before the candidate appeared.
		log.Warn("guard_update_denied", "code", denied.Code, "reason", denied.Message)
		techstackReady, err = true, nil
	}
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
	stackKitResult, stackKitErr := executor.EnsureRuntimeEvidence(ctx, stackKitCfg)
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
			Version: func() string {
				if techstackReady {
					return result.Version
				}
				return ""
			}(),
			ArtifactSHA256: func() string {
				if techstackReady {
					return result.SHA256
				}
				return ""
			}(),
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
			Version: func() string {
				if stackKitErr == nil {
					return stackKitResult.Version
				}
				return ""
			}(),
			ArtifactSHA256: func() string {
				if stackKitErr == nil {
					return stackKitResult.SHA256
				}
				return ""
			}(),
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

// pinPrivateLAN makes this process trust the LAN bridge of a LAN-paired
// enrollment only by its key pin. Other enrollments leave it unchanged.
func pinPrivateLAN(enrollment agentEnrollment) error {
	return privatechannel.InstallProcessPin(
		firstCLIValue(enrollment.PrivateLANOrigin, enrollment.ChannelBootstrap.PrivateLANOrigin),
		firstCLIValue(enrollment.PrivateLANPin, enrollment.ChannelBootstrap.PrivateLANPin),
	)
}

func loadAgentEnrollment(path string) (agentEnrollment, error) {
	data, err := readSecretFile(path)
	if err != nil {
		return agentEnrollment{}, fmt.Errorf("read enrollment file: %w", err)
	}
	return parseAgentEnrollment(data)
}

func parseAgentEnrollment(data []byte) (agentEnrollment, error) {
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
	return readSecretHandle(secret, path)
}

func readSecretHandle(secret *os.File, path string) ([]byte, error) {
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

// The existing authenticated inventory channel transports observations only.
type substrateInventoryCollector struct {
	base   httpguard.Collector
	client *substrate.Client
	config substrate.Config
	// accelerator is nil only in tests that do not look at the PCI bus.
	accelerator *localAcceleratorCache
}

func (c substrateInventoryCollector) Collect(ctx context.Context) (httpguard.Snapshot, error) {
	snapshot, err := c.base.Collect(ctx)
	if err != nil {
		return snapshot, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	inventory, err := c.client.Inventory(ctx)
	if err != nil {
		return snapshot, err
	}
	images := map[string]substrate.Image{}
	for _, profile := range substrate.Profiles() {
		image, err := substrate.DefaultImage(profile.ID)
		if err != nil {
			return snapshot, err
		}
		images[profile.ID] = image
	}
	if c.accelerator != nil {
		inventory.Accelerator = c.accelerator.current(ctx)
	}
	snapshot.Substrate, err = json.Marshal(substrate.GuardObservation{Node: c.config.Node, MinGuestID: c.config.MinGuestID, MaxGuestID: c.config.MaxGuestID, Images: images, Inventory: inventory, OwnedGuests: c.client.ObserveOwnedGuests(ctx, inventory)})
	return snapshot, err
}

// localAcceleratorCache repeats the node's GPU observation only every few
// minutes: the inventory goes out far more often than a GPU is plugged in or
// prepared. A failed look reports nothing rather than an old answer.
type localAcceleratorCache struct {
	observe func(context.Context) (substrate.AcceleratorObservation, error)
	mu      sync.Mutex
	at      time.Time
	last    *substrate.AcceleratorObservation
}

const localAcceleratorRefresh = 5 * time.Minute

func (c *localAcceleratorCache) current(ctx context.Context) *substrate.AcceleratorObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < localAcceleratorRefresh {
		return c.last
	}
	c.at, c.last = time.Now(), nil
	if observed, err := c.observe(ctx); err == nil {
		c.last = &observed
	}
	return c.last
}

// localHardwareCache repeats the machine's hardware probe only every half hour:
// a CPU, a GPU, a disk or a chassis does not change between heartbeats. The
// probe runs in the background, so a slow shell never delays a heartbeat; the
// first inventory after start goes out without hardware facts and the next one
// carries them. A failed probe keeps the last good observation for a day (its
// own ObservedAt says how old it is) and then reports nothing rather than an
// old answer.
type localHardwareCache struct {
	observe func(context.Context) (*httpguard.HostHardware, error)
	mu      sync.Mutex
	started time.Time
	good    time.Time
	last    *httpguard.HostHardware
	running bool
}

const (
	localHardwareRefresh = 30 * time.Minute
	localHardwareKeep    = 24 * time.Hour
)

func (c *localHardwareCache) current(context.Context) *httpguard.HostHardware {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running && (c.started.IsZero() || time.Since(c.started) >= localHardwareRefresh) {
		c.running, c.started = true, time.Now()
		go func() {
			observed, err := c.observe(context.Background())
			c.mu.Lock()
			defer c.mu.Unlock()
			c.running = false
			if err == nil && observed != nil {
				c.last, c.good = observed, time.Now()
			}
		}()
	}
	if c.last != nil && time.Since(c.good) > localHardwareKeep {
		c.last = nil
	}
	return c.last
}
