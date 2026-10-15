package main

import (
	"context"
	"log/slog"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/postureagent"
	"github.com/kombifyio/techstack/pkg/guardexec"
)

// guardPostureAgentEnv is the Guard-side kill switch of posture reporting.
const guardPostureAgentEnv = "TECHSTACK_GUARD_POSTURE_ENABLED"

// startGuardPosture runs the Guard side of posture: it observes the host
// baseline and the backup repository through the read-only StackKits
// observations and reports them. It is never a precondition of the Guard's own
// work, and a Guard without an executor reports the observation as unavailable
// instead of staying silent.
func startGuardPosture(ctx context.Context, enrollment agentEnrollment, identity guardIdentityRuntime, execution guardExecution, log *slog.Logger) {
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
	})
	if err != nil {
		log.Warn("guard_posture_unavailable", "error", err.Error())
		return
	}
	go agent.Run(ctx)
	log.Info("guard_posture_started")
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
