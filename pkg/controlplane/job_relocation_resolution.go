package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type WorkloadRelocationResolution struct {
	TenantID, OwnerID, JobID, PlanSHA256 string
	Outcome                              string
	Proof                                map[string]any
	At                                   time.Time
}
type WorkloadRelocationResolutionStore interface {
	ResolveWorkloadRelocation(context.Context, WorkloadRelocationResolution) (*Job, error)
	ClaimWorkloadRelocationRecovery(context.Context, WorkloadRelocationResolution) error
	RenewWorkloadRelocationRecovery(context.Context, string, string) error
	FailWorkloadRelocationRecovery(context.Context, string, string, string) error
}

func resolveRelocationResult(job *Job, input WorkloadRelocationResolution) error {
	if job.Type != "workload_relocation" || job.Payload["owner_id"] != input.OwnerID || input.PlanSHA256 == "" || len(input.Proof) == 0 || (job.State == jobStateRunning && job.Step != "stackkit_relocation_recovery") {
		return ErrConflict
	}
	outcome, ok := job.Result["workload_relocation"].(map[string]any)
	if !ok || outcome["plan_sha256"] != input.PlanSHA256 {
		return ErrConflict
	}
	if input.Outcome != "moved" && input.Outcome != "aborted" {
		return ErrConflict
	}
	if !relocationHoldsExecution(*job) && outcome["state"] != "moved" && outcome["state"] != "aborted" {
		return ErrConflict
	}
	outcome["state"], outcome["source_retained"], outcome["source_selected_writers_stopped"], outcome["node_verification"] = input.Outcome, true, input.Outcome == "moved", input.Proof
	job.Result["success"] = true
	job.Result["relocation_resolved_at"] = input.At.UTC().Format(time.RFC3339Nano)
	job.State = "completed"
	job.Progress = 100
	job.Error = ""
	job.ErrorDetails = ""
	job.CompletedAt = &input.At
	return nil
}

func (s *PostgresStore) ResolveWorkloadRelocation(ctx context.Context, input WorkloadRelocationResolution) (*Job, error) {
	var out *Job
	err := s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		var payload, result []byte
		job := Job{ID: input.JobID, TenantID: input.TenantID}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(stack_id,''),type,state,COALESCE(step,''),payload_json::text,result_json::text FROM jobs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, input.TenantID, input.JobID).Scan(&job.StackID, &job.Type, &job.State, &job.Step, &payload, &result); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if json.Unmarshal(payload, &job.Payload) != nil || json.Unmarshal(result, &job.Result) != nil {
			return ErrConflict
		}
		targets, err := jobExecutionTargets(job)
		if err != nil {
			return err
		}
		if err := lockExecutionTargetsTx(ctx, tx, input.TenantID, targets); err != nil {
			return err
		}
		if job.State == jobStateRunning {
			var valid bool
			if err := tx.QueryRowContext(ctx, `SELECT execution_owner_id=$3 AND execution_lease_expires_at>clock_timestamp() FROM jobs WHERE tenant_id=$1 AND id=$2`, input.TenantID, input.JobID, processExecutionOwnerID).Scan(&valid); err != nil || !valid {
				return ErrConflict
			}
		}
		if err := resolveRelocationResult(&job, input); err != nil {
			return err
		}
		raw, err := json.Marshal(job.Result)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='completed',progress=100,error=NULL,error_details=NULL,result_json=$3::jsonb,completed_at=$4,updated_at=$4,execution_owner_id=NULL,execution_lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2`, input.TenantID, input.JobID, raw, input.At); err != nil {
			return fmt.Errorf("resolve relocation: %w", err)
		}
		out = &job
		return nil
	})
	return out, err
}

func (s *MemoryStore) ResolveWorkloadRelocation(_ context.Context, input WorkloadRelocationResolution) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[input.JobID]
	if !ok || job.TenantID != input.TenantID {
		return nil, ErrNotFound
	}
	job = *cloneJob(job)
	if job.State == jobStateRunning {
		lease := s.jobLeases[job.ID]
		if lease.OwnerID != processExecutionOwnerID || !lease.ExpiresAt.After(s.now()) {
			return nil, ErrConflict
		}
	}
	if err := resolveRelocationResult(&job, input); err != nil {
		return nil, err
	}
	s.jobs[input.JobID] = job
	s.releaseJobExecutionLeaseLocked(job.ID)
	return cloneJob(job), nil
}

// Recovery reuses this same durable job row, node admission locks and existing
// execution lease. No concurrent recovery may release a live operation fence.
func (s *PostgresStore) ClaimWorkloadRelocationRecovery(ctx context.Context, v WorkloadRelocationResolution) error {
	return s.withTenant(ctx, v.TenantID, func(tx *sql.Tx) error {
		var j Job
		var p, r []byte
		j.ID = v.JobID
		j.TenantID = v.TenantID
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(stack_id,''),type,state,payload_json::text,result_json::text FROM jobs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, v.TenantID, v.JobID).Scan(&j.StackID, &j.Type, &j.State, &p, &r); err != nil {
			return err
		}
		if json.Unmarshal(p, &j.Payload) != nil || json.Unmarshal(r, &j.Result) != nil {
			return ErrConflict
		}
		o, _ := j.Result["workload_relocation"].(map[string]any)
		if j.Type != "workload_relocation" || j.Payload["owner_id"] != v.OwnerID || j.State == "running" || o["plan_sha256"] != v.PlanSHA256 || !relocationHoldsExecution(j) {
			return ErrConflict
		}
		targets, err := jobExecutionTargets(j)
		if err != nil {
			return err
		}
		if err = lockExecutionTargetsTx(ctx, tx, v.TenantID, targets); err != nil {
			return err
		}
		if err = refuseExecutionTargetConflictTx(ctx, tx, v.TenantID, v.JobID, targets); err != nil {
			return err
		}
		for _, target := range targets {
			if err := refuseNodeUnderMaintenanceTx(ctx, tx, v.TenantID, target.AgentID, target.StackID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='running',step='stackkit_relocation_recovery',updated_at=clock_timestamp(),execution_owner_id=$3,execution_lease_expires_at=clock_timestamp()+interval '90 seconds' WHERE tenant_id=$1 AND id=$2`, v.TenantID, v.JobID, processExecutionOwnerID)
		return err
	})
}
func (s *PostgresStore) RenewWorkloadRelocationRecovery(ctx context.Context, tenant, id string) error {
	return s.withTenant(ctx, tenant, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE jobs SET execution_lease_expires_at=clock_timestamp()+interval '90 seconds',updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND state='running' AND step='stackkit_relocation_recovery' AND execution_owner_id=$3 AND execution_lease_expires_at>clock_timestamp()`, tenant, id, processExecutionOwnerID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err == nil && n != 1 {
			return ErrConflict
		}
		return err
	})
}
func (s *PostgresStore) FailWorkloadRelocationRecovery(ctx context.Context, tenant, id, message string) error {
	return s.withTenant(ctx, tenant, func(tx *sql.Tx) error {
		job, err := scanJob(tx.QueryRowContext(ctx, `UPDATE jobs SET state='failed',error=$4,updated_at=clock_timestamp(),execution_owner_id=NULL,execution_lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2 AND state='running' AND step='stackkit_relocation_recovery' AND execution_owner_id=$3
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority, progress, step, message, error, error_details, logs_json::text, result_json::text, scheduled_for, started_at, completed_at, created_at, updated_at`, tenant, id, processExecutionOwnerID, message))
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		recordFailureOccurrenceTx(ctx, tx, job, failureOccurrenceForStoredJob(job, ""))
		return nil
	})
}
func (s *MemoryStore) ClaimWorkloadRelocationRecovery(_ context.Context, v WorkloadRelocationResolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[v.JobID]
	if !ok || j.TenantID != v.TenantID {
		return ErrNotFound
	}
	o, _ := j.Result["workload_relocation"].(map[string]any)
	if j.Type != "workload_relocation" || j.Payload["owner_id"] != v.OwnerID || j.State == "running" || o["plan_sha256"] != v.PlanSHA256 || !relocationHoldsExecution(j) {
		return ErrConflict
	}
	targets, err := jobExecutionTargets(j)
	if err != nil {
		return err
	}
	for _, other := range s.jobs {
		if other.ID != j.ID && other.TenantID == j.TenantID && (other.State == "running" || relocationHoldsExecution(other)) && executionTargetsOverlap(targets, existingJobExecutionTargets(other)) {
			return ErrStackExecutionBusy
		}
	}
	for _, target := range targets {
		if s.memoryNodeUnderMaintenanceLocked(v.TenantID, target.AgentID, target.StackID) {
			return ErrNodeUnderMaintenance
		}
	}
	j.State = "running"
	j.Step = "stackkit_relocation_recovery"
	j.UpdatedAt = s.now()
	s.jobs[j.ID] = j
	s.issueJobExecutionLeaseLocked(j.ID, j.UpdatedAt)
	return nil
}
func (s *MemoryStore) RenewWorkloadRelocationRecovery(_ context.Context, tenant, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	lease := s.jobLeases[id]
	if !ok || j.TenantID != tenant || j.State != "running" || j.Step != "stackkit_relocation_recovery" || lease.OwnerID != processExecutionOwnerID || !lease.ExpiresAt.After(s.now()) {
		return ErrConflict
	}
	s.renewJobExecutionLeaseLocked(id, s.now())
	return nil
}
func (s *MemoryStore) FailWorkloadRelocationRecovery(_ context.Context, tenant, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.TenantID != tenant {
		return ErrNotFound
	}
	if j.State != "running" || j.Step != "stackkit_relocation_recovery" || s.jobLeases[id].OwnerID != processExecutionOwnerID {
		return ErrConflict
	}
	j.State = "failed"
	j.Error = message
	j.UpdatedAt = s.now()
	s.jobs[id] = j
	s.releaseJobExecutionLeaseLocked(id)
	return nil
}
