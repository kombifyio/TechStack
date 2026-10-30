package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ExecutionTarget extends the existing job lease to both nodes of a move.
// There is still one durable job, owner and lease, never a second lock store.
type ExecutionTarget struct {
	StackID string `json:"stack_id"`
	AgentID string `json:"agent_id"`
}

func jobExecutionTargets(job Job) ([]ExecutionTarget, error) {
	agent, _ := job.Payload["agent_id"].(string)
	targets := []ExecutionTarget{{StackID: job.StackID, AgentID: agent}}
	if job.Type != "workload_relocation" {
		return targets, nil
	}
	raw, err := json.Marshal(job.Payload["execution_targets"])
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &targets) != nil || len(targets) != 2 {
		return nil, fmt.Errorf("relocation requires two canonical execution targets")
	}
	primary := false
	for _, target := range targets {
		if strings.TrimSpace(target.StackID) == "" || strings.TrimSpace(target.AgentID) == "" {
			return nil, ErrConflict
		}
		primary = primary || target.StackID == job.StackID
	}
	if !primary || targets[0].AgentID == targets[1].AgentID {
		return nil, ErrConflict
	}
	return targets, nil
}

func executionTargetsOverlap(left, right []ExecutionTarget) bool {
	for _, a := range left {
		for _, b := range right {
			if (a.StackID != "" && a.StackID == b.StackID) || ((len(left) > 1 || len(right) > 1) && a.AgentID != "" && a.AgentID == b.AgentID) {
				return true
			}
		}
	}
	return false
}

func relocationHoldsExecution(job Job) bool {
	if job.Type != "workload_relocation" {
		return false
	}
	outcome, _ := job.Result["workload_relocation"].(map[string]any)
	state, _ := outcome["state"].(string)
	return state == "transferring" || state == "destination_verification_required" || state == "transfer_receipt_incomplete"
}

func lockExecutionTargetsTx(ctx context.Context, tx *sql.Tx, tenantID string, targets []ExecutionTarget) error {
	set := map[string]bool{}
	for _, target := range targets {
		for _, key := range nodeAdmissionKeys(tenantID, target.AgentID, target.StackID) {
			set[key] = true
		}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', '10s', true)`); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
			return err
		}
	}
	return nil
}

func refuseExecutionTargetConflictTx(ctx context.Context, tx *sql.Tx, tenantID, jobID string, targets []ExecutionTarget, workerProjection ...bool) error {
	backupWorker := len(workerProjection) == 1 && workerProjection[0]
	query := `SELECT id, COALESCE(stack_id, ''), type, payload_json::text, result_json::text FROM jobs WHERE tenant_id=$1 AND id<>$2 AND (state='running' OR (type='workload_relocation' AND result_json->'workload_relocation'->>'state' IN ('transferring','destination_verification_required','transfer_receipt_incomplete')))`
	if backupWorker {
		// This purpose principal can see only backup payloads. The existing
		// stack/node fence consumes only the other jobs' resource identities.
		query = `SELECT id,stack_id,type,targets_json::text,'{}'::text FROM techstack_backup_execution_conflicts($1,$2)`
	}
	rows, err := tx.QueryContext(ctx, query, tenantID, jobID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate Job
		var payload, result []byte
		if err := rows.Scan(&candidate.ID, &candidate.StackID, &candidate.Type, &payload, &result); err != nil {
			return err
		}
		if json.Unmarshal(payload, &candidate.Payload) != nil || json.Unmarshal(result, &candidate.Result) != nil {
			return ErrConflict
		}
		other := existingJobExecutionTargets(candidate)
		if executionTargetsOverlap(targets, other) {
			return ErrStackExecutionBusy
		}
	}
	return rows.Err()
}

// Old persisted relocations predate the two-target schema. Preserve their known
// source/destination identities without making an unrelated tenant row a global lock.
func existingJobExecutionTargets(job Job) []ExecutionTarget {
	targets, err := jobExecutionTargets(job)
	if err == nil {
		return targets
	}
	agent, _ := job.Payload["agent_id"].(string)
	targets = []ExecutionTarget{{StackID: job.StackID, AgentID: agent}}
	raw, _ := json.Marshal(job.Payload["workload_relocation"])
	var old struct {
		Source, Destination struct {
			StackID string `json:"stack_id"`
			AgentID string `json:"agent_id"`
		}
	}
	if json.Unmarshal(raw, &old) == nil {
		for _, n := range []struct {
			StackID string `json:"stack_id"`
			AgentID string `json:"agent_id"`
		}{old.Source, old.Destination} {
			if n.StackID != "" || n.AgentID != "" {
				targets = append(targets, ExecutionTarget{StackID: n.StackID, AgentID: n.AgentID})
			}
		}
	}
	if job.Type == "workload_relocation" && len(targets) == 1 {
		targets = append(targets, ExecutionTarget{})
	}
	return targets
}

func maintenanceTargetsOverlap(node ExecutionTarget, targets []ExecutionTarget) bool {
	for _, t := range targets {
		if node.AgentID != "" && t.AgentID != "" {
			if node.AgentID == t.AgentID {
				return true
			}
		} else if node.StackID != "" && node.StackID == t.StackID {
			return true
		}
	}
	return false
}
