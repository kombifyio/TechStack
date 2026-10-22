package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/postureagent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardexec"
)

// guardPostureAgentEnv is the Guard-side kill switch of posture reporting.
const guardPostureAgentEnv = "TECHSTACK_GUARD_POSTURE_ENABLED"

// guardLocalRepairEnv is the Guard-side kill switch of repair under a local
// policy. Off, the Guard only observes while the control plane is unreachable.
const guardLocalRepairEnv = "TECHSTACK_GUARD_LOCAL_REPAIR_ENABLED"

// startGuardPosture runs the Guard side of posture: it observes the host
// baseline and the backup repository through the read-only StackKits
// observations and reports them. It is never a precondition of the Guard's own
// work, and a Guard without an executor reports the observation as unavailable
// instead of staying silent.
func startGuardPosture(ctx context.Context, custodyRoot *os.Root, enrollment agentEnrollment, identity guardIdentityRuntime, manager *hostidentity.Manager, execution guardExecution, log *slog.Logger) {
	if !envBoolDefault(guardPostureAgentEnv, true) {
		return
	}
	url := derivedAgentControlURL(enrollment.HeartbeatURL, "posture")
	if url == "" {
		return
	}
	agent, err := postureagent.New(postureagent.Config{
		URL: url, HTTPClient: identity.httpClient(30 * time.Second), AgentToken: enrollment.AgentToken, TenantID: enrollment.TenantID,
		Observer: execution.postureObserver(), Version: version, Signed: execution.signedUpdates, Logger: log,
		Local: guardLocalRepair(custodyRoot, enrollment, manager, execution, log),
	})
	if err != nil {
		log.Warn("guard_posture_unavailable", "error", err.Error())
		return
	}
	go agent.Run(ctx)
	log.Info("guard_posture_started")
}

// guardLocalRepair enables repair under the owner's local policy while the
// control plane is unreachable. It needs the pinned binding keys (a grant is
// never admitted unsigned), a durable ledger in node custody and a way to run
// the typed repair; without any of them the Guard only observes.
func guardLocalRepair(custodyRoot *os.Root, enrollment agentEnrollment, manager *hostidentity.Manager, execution guardExecution, log *slog.Logger) *postureagent.LocalConfig {
	if !envBoolDefault(guardLocalRepairEnv, true) || len(enrollment.BindingSigningKeys) == 0 || custodyRoot == nil {
		return nil
	}
	repairer := execution.localRepairer(enrollment.BindingSigningKeys)
	if repairer == nil {
		return nil
	}
	ledger := postureagent.OpenLedger(custodyRoot, nil)
	if err := ledger.Err(); err != nil {
		// The ledger keeps refusing new local repairs; its bounds stay.
		log.Warn("guard_local_repair_ledger_unavailable", "error", err.Error())
	}
	return &postureagent.LocalConfig{
		Pins: enrollment.BindingSigningKeys,
		Expect: guardbinding.Expectation{
			TenantID: enrollment.TenantID, WorkerID: enrollment.RuntimeAgentID,
			ServerID: firstCLIValue(enrollment.ServerID, enrollment.ChannelBootstrap.ServerID),
		},
		Revoked:  func() bool { return guardIdentityRevoked(manager) },
		Repairer: repairer,
		Ledger:   ledger,
	}
}

// localRepairer runs a local repair through the privileged executor, which
// verifies the grant itself, or in process on a Guard that still runs as root.
func (e guardExecution) localRepairer(pins []guardbinding.PinnedKey) postureagent.LocalRepairer {
	switch {
	case e.remote != nil:
		return e.remote
	case e.mode == guardModeLegacyRoot:
		switch local := e.runtime.(type) {
		case *guardexec.LegacyRoot:
			return legacyLocalRepairer{executor: local.StackKitExecutor, pins: pins}
		case *agentpkg.StackKitExecutor:
			return legacyLocalRepairer{executor: local, pins: pins}
		}
	}
	return nil
}

// legacyLocalRepairer is the local repair of a Guard that runs as root: there
// is no privileged boundary, so the daemon applies the executor's grant check
// before it runs the typed command in process.
type legacyLocalRepairer struct {
	executor *agentpkg.StackKitExecutor
	pins     []guardbinding.PinnedKey
}

func (l legacyLocalRepairer) LocalHostSecurityRepair(ctx context.Context, grant guardbinding.LocalRepairGrant, operationID string, controls []string) (*agentpb.StackKitResult, error) {
	if denial := grant.AdmitAtExecutor(l.pins, operationID, controls, time.Now().UTC()); denial != nil {
		return nil, fmt.Errorf("%w: %s", guardexec.ErrLocalRepairNotStarted, denial.Error())
	}
	command, err := guardexec.LocalRepairCommand(grant, operationID, controls)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", guardexec.ErrLocalRepairNotStarted, err)
	}
	return l.executor.Execute(ctx, command), nil
}

// postureObserver selects who observes the host: the privileged executor in the
// split layout, the in-process StackKits runtime on a Guard that still runs as
// root, and an observer that reports the gap when neither exists.
func (e guardExecution) postureObserver() postureagent.Observer {
	switch {
	case e.remote != nil:
		return e.remote
	case e.mode == guardModeLegacyRoot:
		switch local := e.runtime.(type) {
		case *guardexec.LegacyRoot:
			return guardexec.NewObserver(local.StackKitExecutor)
		case *agentpkg.StackKitExecutor:
			return guardexec.NewObserver(local)
		}
	}
	return unobservable{}
}

// signedUpdates reports whether this Guard's update path admits only signed
// releases: the split layout and a Linux root Guard, unless the host opted into
// unsigned local builds.
func (e guardExecution) signedUpdates() bool {
	if legacy, ok := e.runtime.(*guardexec.LegacyRoot); ok {
		return !legacy.AdmitsUnsigned()
	}
	if e.mode != guardModeSplit || e.remote == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := e.remote.RemoteStatus(ctx)
	return err == nil && !status.AdmitsUnsigned
}

// unobservable is the observer of a Guard that has no way to read the host.
type unobservable struct{}

func (unobservable) HostSecurityVerify(context.Context) (guardexec.Observation, error) {
	return guardexec.Observation{Reason: "executor_unavailable"}, nil
}

func (unobservable) BackupStatus(context.Context) (guardexec.Observation, error) {
	return guardexec.Observation{Reason: "executor_unavailable"}, nil
}
