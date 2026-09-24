package agentcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// SendWorkerExecution transports an already admitted invocation. The existing
// operation, current lease revision and live execution claim are revalidated
// transactionally. Cancellation never deletes dispatch custody: retry recovers
// the same invocation rather than allocating another guest.
func (h *Hub) SendWorkerExecution(ctx context.Context, command providerexecutor.WorkerExecution) (providerexecutor.WorkerExecutionResult, error) {
	if h == nil || h.db == nil {
		return providerexecutor.WorkerExecutionResult{}, errors.New("provider worker transport requires durable database")
	}
	if err := command.Validate(time.Now()); err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	tx, err := h.tenantTx(ctx, command.Command.TenantID)
	if err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	defer tx.Rollback()
	if err := validateWorkerFence(ctx, tx, command); err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO typed_agent_commands(command_id,tenant_id,agent_id,command_family,command_json,expires_at)
VALUES($1,$2,$3,'provider',$4::jsonb,$5) ON CONFLICT(command_id) DO UPDATE
SET expires_at=EXCLUDED.expires_at, command_json=EXCLUDED.command_json
WHERE typed_agent_commands.command_family='provider'
AND typed_agent_commands.tenant_id=EXCLUDED.tenant_id AND typed_agent_commands.agent_id=EXCLUDED.agent_id
AND typed_agent_commands.command_json - 'expires_at' = EXCLUDED.command_json - 'expires_at'`, command.InvocationID, command.Command.TenantID, command.WorkerID, payload, command.ExpiresAt)
	if err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	var same bool
	if err := tx.QueryRowContext(ctx, `SELECT command_family='provider' AND agent_id=$3 AND command_json - 'expires_at' = $4::jsonb - 'expires_at' FROM typed_agent_commands WHERE tenant_id=$1 AND command_id=$2`, command.Command.TenantID, command.InvocationID, command.WorkerID, payload).Scan(&same); err != nil || !same {
		return providerexecutor.WorkerExecutionResult{}, ErrResultRejected
	}
	if err := tx.Commit(); err != nil {
		return providerexecutor.WorkerExecutionResult{}, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, done, err := h.workerOutcome(ctx, command.Command.TenantID, command.InvocationID)
		if err != nil || done {
			return result, err
		}
		select {
		case <-ctx.Done():
			return providerexecutor.WorkerExecutionResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// PollWorkerExecution permits replay after an interrupted delivery only while
// the same provider-control claim and lease fence remain valid. The executor
// must implement provider-native correlation recovery for the reserved id.
func (h *Hub) PollWorkerExecution(ctx context.Context, tenantID, workerID string) (*providerexecutor.WorkerExecution, error) {
	if h == nil || h.db == nil {
		return nil, errors.New("provider worker transport unavailable")
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT command_json FROM typed_agent_commands
WHERE tenant_id=$1 AND agent_id=$2 AND command_family='provider' AND expires_at>clock_timestamp()
AND (state='queued' OR (state='dispatched' AND dispatched_at<clock_timestamp()-interval '30 seconds'))
ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, tenantID, workerID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var command providerexecutor.WorkerExecution
	if err := json.Unmarshal(payload, &command); err != nil {
		return nil, err
	}
	if command.WorkerID != workerID || command.Command.TenantID != tenantID {
		return nil, ErrResultRejected
	}
	if err := command.Validate(time.Now()); err != nil {
		return nil, err
	}
	if err := validateWorkerFence(ctx, tx, command); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE typed_agent_commands SET state='dispatched',dispatched_at=clock_timestamp() WHERE tenant_id=$1 AND command_id=$2`, tenantID, command.InvocationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &command, nil
}

func (h *Hub) SubmitWorkerExecutionResult(ctx context.Context, tenantID, workerID string, result providerexecutor.WorkerExecutionResult) error {
	if h == nil || h.db == nil || result.InvocationID == "" || len(result.Observation) > 4<<20 || (result.Succeeded && !result.Complete) {
		return ErrResultRejected
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	var state string
	var oldResult []byte
	err = tx.QueryRowContext(ctx, `SELECT command_json,state,COALESCE(result_json,'null'::jsonb) FROM typed_agent_commands WHERE tenant_id=$1 AND agent_id=$2 AND command_id=$3 AND command_family='provider' FOR UPDATE`, tenantID, workerID, result.InvocationID).Scan(&payload, &state, &oldResult)
	if err != nil {
		return ErrCommandNotPending
	}
	var command providerexecutor.WorkerExecution
	if json.Unmarshal(payload, &command) != nil || command.Command.CommandDigest != result.CommandDigest {
		return ErrResultRejected
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if state == "completed" {
		var same bool
		if err := tx.QueryRowContext(ctx, `SELECT $1::jsonb=$2::jsonb`, oldResult, encoded).Scan(&same); err != nil {
			return err
		}
		if !same {
			return ErrResultRejected
		}
		return nil
	}
	if state != "dispatched" {
		return ErrCommandNotPending
	}
	// Late facts may be retained after claim cancellation. They cannot dispatch
	// new work or advance the provider ledger without its current fence.
	if _, err := tx.ExecContext(ctx, `UPDATE typed_agent_commands SET state='completed',result_json=$3::jsonb,completed_at=clock_timestamp() WHERE tenant_id=$1 AND command_id=$2`, tenantID, result.InvocationID, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *Hub) workerOutcome(ctx context.Context, tenantID, id string) (providerexecutor.WorkerExecutionResult, bool, error) {
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return providerexecutor.WorkerExecutionResult{}, false, err
	}
	defer tx.Rollback()
	var state string
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(result_json,'null'::jsonb) FROM typed_agent_commands WHERE tenant_id=$1 AND command_id=$2 AND command_family='provider'`, tenantID, id).Scan(&state, &payload); err != nil {
		return providerexecutor.WorkerExecutionResult{}, false, err
	}
	if state != "completed" {
		return providerexecutor.WorkerExecutionResult{}, false, nil
	}
	var result providerexecutor.WorkerExecutionResult
	err = json.Unmarshal(payload, &result)
	return result, true, err
}

func validateWorkerFence(ctx context.Context, tx *sql.Tx, execution providerexecutor.WorkerExecution) error {
	command := execution.Command
	access := "read_only"
	if execution.Phase == providerexecutor.WorkerPhaseSubmit {
		access = "side_effecting"
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM provider_operations op
JOIN techstack_vm_leases lease ON lease.tenant_id=op.tenant_id AND lease.id=op.lease_id
JOIN provider_operation_execution_claims claim ON claim.tenant_id=op.tenant_id AND claim.operation_id=op.operation_id
WHERE op.tenant_id=$1 AND op.operation_id=$2 AND op.command_digest=$3
AND op.head_sequence=$4 AND op.head_receipt_digest=$5
AND claim.head_sequence=op.head_sequence AND claim.head_receipt_digest=op.head_receipt_digest
AND claim.state='active' AND (claim.claim_access=$6 OR ($6='read_only' AND claim.claim_access='side_effecting')) AND claim.lease_expires_at>clock_timestamp()
AND lease.lease_revision=$7 AND lease.server_id=$8 AND lease.resource_generation_id=$9::uuid
AND (op.command_json #>> '{command,operation}'='decommission' OR (lease.cancelled_at IS NULL AND lease.valid_from<=clock_timestamp() AND lease.valid_until>clock_timestamp()))
AND op.command_json #>> '{execution_profile,credential_mode}'='worker_held'
AND op.command_json #>> '{command,connection_ref}'=$10
)`, command.TenantID, command.OperationID, command.CommandDigest, execution.HeadSequence, execution.HeadDigest, access, command.LeaseRevision, command.RuntimeServerID, command.ResourceGenerationID, "provider-connection://substrate/"+execution.WorkerID+"/"+command.LeaseID).Scan(&valid)
	if err != nil {
		return fmt.Errorf("provider worker fence lookup: %w", err)
	}
	if !valid {
		return errors.New("provider worker execution is not admitted at the current lease and claim")
	}
	if execution.Prerequisite != nil {
		payload, err := json.Marshal(execution.Prerequisite)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM typed_agent_commands WHERE tenant_id=$1 AND agent_id=$2 AND command_id=$3 AND command_family='provider' AND state='completed' AND result_json=$4::jsonb)`, command.TenantID, execution.WorkerID, execution.Prerequisite.InvocationID, payload).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("provider worker prerequisite is not a retained result")
		}
	}
	return nil
}

// ReadWorkerExecutionResult recovers the retained result of an exact stage.
func (h *Hub) ReadWorkerExecutionResult(ctx context.Context, tenantID, id string) (providerexecutor.WorkerExecutionResult, bool, error) {
	result, found, err := h.workerOutcome(ctx, tenantID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	return result, found, err
}
