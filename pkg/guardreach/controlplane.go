package guardreach

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// ControlPlane resolves the live worker and host-key binding from the
// control-plane stores.
type ControlPlane struct {
	Workers    controlplane.WorkerStore
	Identities controlplane.WorkerIdentityStore
	// Servers, when set, lets a retired server (decommissioning or
	// decommissioned) end its reach even if the worker record lingers.
	Servers controlplane.ServerRuntimeStore
}

// Resolve reports the worker and identity state. A worker that is gone, or
// whose status is revoked or rejected, is not active: its reach must end.
func (c ControlPlane) Resolve(ctx context.Context, tenantID, workerID, serverID string) (Subject, error) {
	worker, err := c.Workers.GetWorker(ctx, tenantID, workerID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && worker == nil) {
		return Subject{}, nil
	}
	if err != nil {
		return Subject{}, err
	}
	subject := Subject{Found: true, WorkerActive: true}
	switch strings.ToLower(strings.TrimSpace(worker.Status)) {
	case "revoked", "rejected":
		subject.WorkerActive = false
	}
	if c.Servers != nil && strings.TrimSpace(serverID) != "" {
		server, serverErr := c.Servers.GetServerRuntime(ctx, tenantID, serverID)
		switch {
		case errors.Is(serverErr, controlplane.ErrNotFound):
		case serverErr != nil:
			return Subject{}, serverErr
		case server != nil && retiredServer(*server):
			subject.WorkerActive = false
		}
	}
	identity, err := c.Identities.GetWorkerIdentity(ctx, tenantID, workerID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return subject, nil
	case err != nil:
		return Subject{}, err
	}
	subject.IdentityState = identity.State
	subject.Thumbprint = identity.Thumbprint
	subject.CredentialEpoch = identity.CredentialEpoch
	return subject, nil
}

func retiredServer(server controlplane.ServerRuntime) bool {
	switch strings.ToLower(strings.TrimSpace(server.LifecycleState)) {
	case string(serverregistry.LifecycleDecommissioning), string(serverregistry.LifecycleDecommissioned):
		return true
	}
	return strings.EqualFold(strings.TrimSpace(server.DesiredState), string(serverregistry.DesiredAbsent))
}
