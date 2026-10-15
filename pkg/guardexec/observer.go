package guardexec

import (
	"context"
	"errors"
	"sync"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
)

const hostSecuritySupportTTL = 5 * time.Minute

// Observer makes the Guard's fixed, read-only StackKits observations: the host
// security baseline and the backup repository. The privileged executor owns one
// for the split layout; a Guard that still runs as root uses one in process.
type Observer struct {
	stackKit *agentpkg.StackKitExecutor

	mu        sync.Mutex
	supported bool
	at        time.Time
}

// NewObserver returns an observer over the pinned StackKits runtime.
func NewObserver(stackKit *agentpkg.StackKitExecutor) *Observer {
	return &Observer{stackKit: stackKit}
}

// Supported reports whether the pinned StackKits release ships
// `stackkit host security`, cached: the pinned release only changes through a
// runtime install.
func (o *Observer) Supported(ctx context.Context) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.at.IsZero() && time.Since(o.at) < hostSecuritySupportTTL {
		return o.supported
	}
	o.supported = o.stackKit.HostSecuritySupported(ctx)
	o.at = time.Now()
	return o.supported
}

// HostSecurityVerify observes the host baseline. The argument vector is
// constant; the workspace (which decides the site expectations) is added only
// when it exists as a real directory. The freshness budget covers three
// observation cycles, so one missed cycle never turns a pass into unknown.
func (o *Observer) HostSecurityVerify(ctx context.Context) (Observation, error) {
	if !o.Supported(ctx) {
		if !o.stackKit.Available() {
			return Observation{Reason: ReasonRuntimeUnavailable}, nil
		}
		return Observation{Reason: ReasonCLIUnsupported}, nil
	}
	args := []string{"--no-log", "host", "security", "verify", "--mode", "advanced", "--json", "--no-record", "--freshness", "45m"}
	if workspace := agentpkg.HostSecurityWorkspace(); workspace != "" {
		args = append(args, "--chdir", workspace)
	}
	return o.observe(ctx, agentpkg.ReadOnlyRun{Args: args, Timeout: 2 * time.Minute})
}

// BackupStatus observes the local backup repository.
func (o *Observer) BackupStatus(ctx context.Context) (Observation, error) {
	workspace := agentpkg.HostSecurityWorkspace()
	if workspace == "" {
		return Observation{Reason: ReasonWorkspaceUnavailable}, nil
	}
	return o.observe(ctx, agentpkg.ReadOnlyRun{
		Dir:     workspace,
		Args:    []string{"--no-log", "--chdir", workspace, "--spec", "stack-spec.yaml", "backup", "status", "--json"},
		Timeout: 3 * time.Minute,
	})
}

func (o *Observer) observe(ctx context.Context, run agentpkg.ReadOnlyRun) (Observation, error) {
	result, err := o.stackKit.RunReadOnly(ctx, run)
	if errors.Is(err, agentpkg.ErrStackKitRuntimeUnavailable) {
		return Observation{Reason: ReasonRuntimeUnavailable}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	return Observation{Supported: true, ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr, ReleaseVersion: result.ReleaseVersion}, nil
}
