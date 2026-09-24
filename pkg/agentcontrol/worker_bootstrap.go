package agentcontrol

import (
	"context"
	"encoding/json"
	"errors"
	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/internal/substrate"
	"time"
)

// WorkerBootstrap returns a transient capability only to the exact admitted Guard.
// Durable records contain its one-use hash; raw cloud-init never enters a queue.
func (h *Hub) WorkerBootstrap(ctx context.Context, tenant, worker, invocation string) ([]byte, error) {
	tx, err := h.tenantTx(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT command_json FROM typed_agent_commands WHERE tenant_id=$1 AND agent_id=$2 AND command_id=$3 AND command_family='provider' AND state='dispatched' AND expires_at>clock_timestamp()`, tenant, worker, invocation).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var execution providerexecutor.WorkerExecution
	if json.Unmarshal(raw, &execution) != nil || execution.Validate(time.Now()) != nil || execution.WorkerID != worker || execution.Command.TenantID != tenant || execution.Phase != providerexecutor.WorkerPhaseSubmit || execution.Stage != "create" || execution.Command.Operation != providerexecutor.OperationProvision {
		return nil, errors.New("bootstrap dispatch invalid")
	}
	if err = validateWorkerFence(ctx, tx, execution); err != nil {
		return nil, err
	}
	var spec substrate.ExecutionSpec
	if json.Unmarshal(execution.Payload, &spec) != nil || !spec.Guest.Enrollment || spec.Guest.ProfileID != "ubuntu-24.04" {
		return nil, errors.New("bootstrap profile invalid")
	}
	var stack string
	err = tx.QueryRowContext(ctx, `SELECT s.stack_id FROM servers s JOIN techstack_vm_leases l ON l.tenant_id=s.tenant_id AND l.server_id=s.id WHERE l.tenant_id=$1 AND l.id=$2 AND s.id=$3`, tenant, execution.Command.LeaseID, execution.Command.RuntimeServerID).Scan(&stack)
	if err != nil {
		return nil, err
	}
	issuer, err := guardbootstrap.NewEnrollmentIssuer(h.db, nil)
	if err != nil {
		return nil, err
	}
	request := guardbootstrap.EnrollmentRequest{TenantID: tenant, OperationID: execution.Command.OperationID, LeaseID: execution.Command.LeaseID, StackID: stack, Hostname: spec.Guest.Name, HostPrepProfile: guardbootstrap.HostPrepProfileStackKitReadyUbuntu2404V1}
	payload, err := issuer.RenderPayload(request)
	if err != nil {
		return nil, err
	}
	if err = issuer.RecordCapability(ctx, request); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return payload, nil
}
