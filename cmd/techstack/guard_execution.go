package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardexec"
)

// Guard execution modes.
const (
	// guardModeSplit is the hardened layout: this daemon is unprivileged and every
	// host effect crosses the typed boundary to the privileged executor.
	guardModeSplit = "split"
	// guardModeLegacyRoot is a host that still runs the whole Guard as root. It is
	// migrated to the split layout; until that succeeds it keeps running.
	guardModeLegacyRoot = "legacy_root"
	// guardModeUnavailable is an unprivileged Guard without an executor: it
	// observes and reports, and refuses every host effect with recovery guidance.
	guardModeUnavailable = "unavailable"
)

// guardRuntime is everything the Guard needs from whoever applies host effects.
type guardRuntime interface {
	currentStackKitCommandRuntime
	runtimePackageConverger
	HostMaintenanceCapabilities() []string
	ApplicationCapabilities() []string
}

type guardExecution struct {
	mode    string
	runtime guardRuntime
	// remote is set in split mode.
	remote     *guardexec.Remote
	handoffDir string
}

func executorSocket() string {
	return envDefault("TECHSTACK_GUARD_EXECUTOR_SOCKET", guardexec.DefaultSocket)
}

func handoffDir() string {
	return envDefault("TECHSTACK_GUARD_HANDOFF_DIR", guardexec.DefaultHandoffDir)
}

// selectGuardExecution decides how this Guard applies host effects. A reachable
// executor always wins. Without one, a root process keeps the legacy in-process
// executor (and is migrated), and an unprivileged process refuses host effects.
func selectGuardExecution(ctx context.Context, log *slog.Logger) guardExecution {
	if runtime.GOOS == "linux" {
		remote := guardexec.NewRemote(executorSocket(), handoffDir())
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		ready := remote.Available(probe)
		cancel()
		if ready {
			log.Info("guard_execution_mode", "mode", guardModeSplit)
			return guardExecution{mode: guardModeSplit, runtime: remote, remote: remote, handoffDir: handoffDir()}
		}
		if os.Geteuid() != 0 {
			log.Warn("guard_execution_mode", "mode", guardModeUnavailable, "reason", "privileged executor is not reachable")
			return guardExecution{mode: guardModeUnavailable, runtime: unavailableRuntime{}}
		}
	}
	executor := agentpkg.NewStackKitExecutorFromEnv()
	if runtime.GOOS == "linux" {
		// A root Guard updates itself only to a signed release, like the split
		// executor; a control plane without one leaves it on its version.
		log.Info("guard_execution_mode", "mode", guardModeLegacyRoot, "update_policy", "signed_release_required")
		return guardExecution{mode: guardModeLegacyRoot, runtime: guardexec.NewLegacyRoot(executor, guardexec.Options{
			Version: version, StateDir: envDefault("TECHSTACK_GUARD_EXECUTOR_STATE_DIR", ""), Log: log,
		})}
	}
	log.Info("guard_execution_mode", "mode", guardModeLegacyRoot)
	return guardExecution{mode: guardModeLegacyRoot, runtime: executor}
}

// unavailableRuntime refuses host effects of a Guard that has no executor.
type unavailableRuntime struct{}

var errExecutorUnavailable = errors.New("the privileged executor is not available on this host; run `sudo techstack guard layout apply` to repair the kombify Guard installation")

func (unavailableRuntime) EnsureRuntime(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) error {
	return errExecutorUnavailable
}

func (unavailableRuntime) Execute(_ context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return (&agentpkg.StackKitExecutor{}).FailureResult(command, errExecutorUnavailable)
}

func (unavailableRuntime) FailureResult(command *agentpb.StackKitCommand, err error) *agentpb.StackKitResult {
	return (&agentpkg.StackKitExecutor{}).FailureResult(command, err)
}

func (unavailableRuntime) EnsureTechstackRuntime(context.Context, agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	return agentpkg.TechstackRuntimeConvergenceResult{}, errExecutorUnavailable
}

func (unavailableRuntime) EnsureRuntimeEvidence(context.Context, agentpkg.StackKitRuntimeBootstrapConfig) (agentpkg.StackKitRuntimeConvergenceResult, error) {
	return agentpkg.StackKitRuntimeConvergenceResult{}, errExecutorUnavailable
}

func (unavailableRuntime) HostMaintenanceCapabilities() []string { return nil }
func (unavailableRuntime) ApplicationCapabilities() []string     { return nil }

// manifestReader reads an access manifest directly and, when the daemon may not
// see it (a root-only path, or a home hidden from the unit), through the
// privileged executor.
func (e guardExecution) manifestReader() func(string) ([]byte, error) {
	if e.remote == nil {
		return nil
	}
	return func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err == nil || !(errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)) {
			return data, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		viaExecutor, remoteErr := e.remote.ReadManifest(ctx, path)
		switch {
		case remoteErr == nil:
			return viaExecutor, nil
		case errors.Is(remoteErr, os.ErrNotExist):
			return nil, os.ErrNotExist
		default:
			return nil, err
		}
	}
}

// dockerProbe routes container probes through the executor in split mode.
func (e guardExecution) dockerProbe() func(context.Context, string, ...string) ([]byte, error) {
	if e.remote == nil {
		return nil
	}
	return e.remote.ProbeDocker
}

func describeGuardExecution(e guardExecution) string {
	return fmt.Sprintf("%s(%s)", e.mode, strings.TrimSpace(executorSocket()))
}

// posture projects how this Guard is isolated and how its latest update went.
func (e guardExecution) posture() *guardbinding.Posture {
	posture := &guardbinding.Posture{Mode: guardbinding.PostureLegacyRoot}
	switch e.mode {
	case guardModeSplit:
		posture.Mode = guardbinding.PostureSplit
		status := guardexec.ReadUpdateStatus(e.handoffDir)
		posture.UpdateState, posture.UpdateVersion, posture.UpdateReason = status.State, status.Version, status.Reason
		posture.DenyMutations = status.State == guardexec.StateRecoveryNeeded
		if e.remote != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if live, err := e.remote.RemoteStatus(ctx); err == nil {
				posture.DenyMutations = live.DenyMutations
				posture.BindingPin = live.BindingPin
			}
		}
	case guardModeUnavailable:
		posture.Mode = guardbinding.PostureUnavailable
	}
	return posture
}
