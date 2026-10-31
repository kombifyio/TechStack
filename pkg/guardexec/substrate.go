package guardexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
)

// substrateHandoffDir holds what a substrate Guard downloads before it
// authenticates it, the update status and the trial handshake. It lives below
// the root-only state directory: this Guard is root and shares it with nobody.
const substrateHandoffDir = "substrate-handoff"

// SubstrateSelfUpdate converges the executable of a Proxmox substrate Guard to
// the signed release its control plane publishes, under the release policy of
// the privileged executor. It touches nothing else: no StackKits executor, no
// runtime or package convergence and no operations executable reach the
// hypervisor. A new release runs under the supervised trial, so a Guard that
// does not complete an authenticated round trip is replaced by the previous one
// instead of restarting in a loop on a host nobody may disturb.
type SubstrateSelfUpdate struct {
	opts Options
	mu   sync.Mutex
}

// NewSubstrateSelfUpdate selects the state directory, the release trust and the
// running version; the executable path comes from each convergence request.
func NewSubstrateSelfUpdate(opts Options) *SubstrateSelfUpdate {
	opts.defaults()
	opts.GuardOnly = true
	opts.HandoffDir = filepath.Join(opts.StateDir, substrateHandoffDir)
	return &SubstrateSelfUpdate{opts: opts}
}

// HandoffDir is where this Guard confirms a trial and reads its update status.
func (s *SubstrateSelfUpdate) HandoffDir() string { return s.opts.HandoffDir }

// EnsureGuardExecutable moves the Guard to the signed release the control plane
// publishes. AgentUpdated means a trial started and is about to restart this
// Guard, so the hold quiesce took stays taken. A candidate the policy refuses
// leaves the executable as it is and is reported as *UpdateDeniedError.
func (s *SubstrateSelfUpdate) EnsureGuardExecutable(ctx context.Context, cfg agentpkg.TechstackRuntimeConvergenceConfig, quiesce func(context.Context) (func(), error)) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := agentpkg.TechstackRuntimeConvergenceResult{Version: strings.TrimSpace(cfg.Version)}
	opts := s.opts
	opts.AgentPath = cfg.AgentPath
	if err := os.MkdirAll(opts.HandoffDir, 0o700); err != nil {
		return result, err
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return result, err
	}
	staged, err := stageSignedRelease(ctx, updater, opts.HandoffDir, cfg, &result)
	if err != nil || staged == nil || staged.AlreadyCurrent {
		return result, err
	}
	release, err := quiesce(ctx)
	if err != nil {
		return result, err
	}
	if _, err := updater.Commit(ctx, CommitParams{Version: staged.Version, SHA256: staged.SHA256}); err != nil {
		release()
		return result, err
	}
	result.AgentUpdated = true
	result.Version = staged.Version
	return result, nil
}
