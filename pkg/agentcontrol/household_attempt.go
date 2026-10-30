package agentcontrol

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
)

var ErrHouseholdCommandUnresolved = errors.New("original household command remains unresolved")

// HouseholdCommandSender retains the actual original command, including its
// release and runtime binding. A timeout never grants a replacement mutation.
// Enqueue and result validation remain owned by the existing Hub.
type HouseholdCommandSender struct{ Hub *Hub }

func (s *HouseholdCommandSender) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	return nil, errors.New("household command requires explicit tenant")
}

func (s *HouseholdCommandSender) SendStackKitCommandForTenant(ctx context.Context, tenant, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if s == nil || s.Hub == nil || s.Hub.db == nil || command == nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT || tenant == "" || agent == "" {
		return nil, ErrResultRejected
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	conn, err := s.Hub.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var role string
	if err = conn.QueryRowContext(ctx, `SELECT session_user WHERE current_user=session_user`).Scan(&role); err != nil || role != "techstack_household_worker" {
		return nil, ErrResultRejected
	}
	// A session lock spans the network round trip; intent commits before enqueue.
	lockKey := "household-attempt:" + tenant + ":" + command.HouseholdProjectionKey
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockKey); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var priorID, priorAgent, priorInstance string
	var priorRevision uint64
	var priorRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT command_id,agent_id,stackkit_instance_id,projection_revision,command_json
  FROM household_projection_attempts WHERE tenant_id=$1 AND projection_key=$2 FOR UPDATE`, tenant, command.HouseholdProjectionKey).
		Scan(&priorID, &priorAgent, &priorInstance, &priorRevision, &priorRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if priorAgent != agent || priorInstance != command.StackkitInstanceId || priorRevision > command.HouseholdProjectionRevision {
			return nil, ErrResultRejected
		}
		var state string
		var raw []byte
		var failure sql.NullString
		e := tx.QueryRowContext(ctx, `SELECT state,result_json,error FROM typed_agent_commands WHERE tenant_id=$1 AND command_id=$2`, tenant, priorID).Scan(&state, &raw, &failure)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if e == nil && state == "queued" {
			withdrawal, e := s.Hub.WithdrawQueuedStackKitCommandForTenant(ctx, tenant, priorID)
			if e != nil {
				return nil, e
			}
			if !withdrawal.Withdrawn {
				return nil, ErrHouseholdCommandUnresolved
			}
			state = "failed"
			failure = sql.NullString{Valid: true, String: cancelledBeforeDispatchPrefix + "withdrawn"}
		}
		if e == nil {
			switch state {
			case "completed", "failed":
				if !(state == "failed" && strings.HasPrefix(failure.String, cancelledBeforeDispatchPrefix)) {
					old := &agentpb.StackKitCommand{}
					result := &agentpb.StackKitResult{}
					if protojson.Unmarshal(priorRaw, old) != nil || protojson.Unmarshal(raw, result) != nil || stackkitcommand.ValidateResult(result, old) != nil {
						return nil, ErrHouseholdCommandUnresolved
					}
					clear(result.SensitiveResultJson)
					result.SensitiveResultJson = nil

				}
			default:
				return nil, ErrHouseholdCommandUnresolved
			}
		}
		// With the attempt row locked, an old enqueue either committed before this
		// read or must re-check this exact intent after we commit its replacement.
	}
	payload, err := protojson.Marshal(command)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO household_projection_attempts(tenant_id,projection_key,projection_revision,command_id,agent_id,stackkit_instance_id,command_json)
  VALUES($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(tenant_id,projection_key) DO UPDATE SET
  projection_revision=EXCLUDED.projection_revision,command_id=EXCLUDED.command_id,agent_id=EXCLUDED.agent_id,
  stackkit_instance_id=EXCLUDED.stackkit_instance_id,command_json=EXCLUDED.command_json`, tenant, command.HouseholdProjectionKey, command.HouseholdProjectionRevision, command.CommandId, agent, command.StackkitInstanceId, payload)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Hub.SendStackKitCommandForTenant(ctx, tenant, agent, command)
}

// Runs inside the Hub's ORIGINAL enqueue transaction, not a second command
// path. It closes prepared-intent/unknown-commit races across worker restarts.
func checkHouseholdEnqueueIntent(ctx context.Context, tx *sql.Tx, tenant, agent string, command *agentpb.StackKitCommand, payload []byte) error {
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT session_user`).Scan(&role); err != nil {
		return err
	}
	if role != "techstack_household_worker" {
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT command_id FROM household_projection_attempts
  WHERE tenant_id=$1 AND command_id=$2 AND agent_id=$3 AND command_json=$4::jsonb FOR SHARE`, tenant, command.CommandId, agent, payload).Scan(&id)
	if err != nil {
		return ErrHouseholdCommandUnresolved
	}
	return nil
}
