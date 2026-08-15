package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const tenantGUC = "app.tenant_id"

// userGUC scopes the membership self-lookup RLS policy (migration 043).
const userGUC = "app.user_id"

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) CreateStack(ctx context.Context, req CreateStackRequest) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		configJSON, err := marshalObject(req.Config)
		if err != nil {
			return err
		}
		servicesJSON, err := marshalArray(req.Services)
		if err != nil {
			return err
		}
		stack, err := scanStack(tx.QueryRowContext(ctx, `
			INSERT INTO stacks (
				id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json, services_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, NULLIF($7, ''),
				$8, $9, $10::jsonb, $11::jsonb
			)
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`,
			req.ID,
			tenantID,
			req.InstanceID,
			req.OwnerSubjectID,
			req.HomelabID,
			req.Name,
			req.Description,
			firstNonEmpty(req.Mode, "easy"),
			firstNonEmpty(req.Status, "draft"),
			configJSON,
			servicesJSON,
		))
		if isUniqueViolation(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = stack
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetStack(ctx context.Context, tenantID, stackID string) (*Stack, error) {
	return s.getStack(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
			mode, status, config_json::text, services_json::text,
			runtime_summary_json::text, drift_status, drift_checked_at,
			created_at, updated_at, deleted_at
		FROM stacks
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, stackID)
}

// GetStackIncludingDeleted is restricted to exact tenant/id receipt
// authorization. Active product inventory intentionally uses GetStack.
func (s *PostgresStore) GetStackIncludingDeleted(ctx context.Context, tenantID, stackID string) (*Stack, error) {
	return s.getStack(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
			mode, status, config_json::text, services_json::text,
			runtime_summary_json::text, drift_status, drift_checked_at,
			created_at, updated_at, deleted_at
		FROM stacks
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, stackID)
}

func (s *PostgresStore) GetActiveStackByName(ctx context.Context, tenantID, ownerSubjectID, name string) (*Stack, error) {
	return s.getStack(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
			mode, status, config_json::text, services_json::text,
			runtime_summary_json::text, drift_status, drift_checked_at,
			created_at, updated_at, deleted_at
		FROM stacks
		WHERE tenant_id = $1
			AND owner_subject_id = NULLIF($2, '')
			AND lower(name) = lower($3)
			AND deleted_at IS NULL
	`, tenantID, ownerSubjectID, strings.TrimSpace(name))
}

func (s *PostgresStore) ListStacksByTenant(ctx context.Context, tenantID string) ([]Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
			FROM stacks
			WHERE tenant_id = $1 AND deleted_at IS NULL
			ORDER BY created_at DESC
		`, tenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			stack, err := scanStack(rows)
			if err != nil {
				return err
			}
			out = append(out, *stack)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) SoftDeleteStack(ctx context.Context, tenantID, stackID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}

	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE stacks
			SET deleted_at = now(), status = 'stopped', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		`, tenantID, stackID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) UpdateStackRuntime(ctx context.Context, tenantID, stackID string, runtime RuntimeUpdate) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		runtimeJSON, err := marshalObject(runtime.RuntimeSummary)
		if err != nil {
			return err
		}
		stack, err := scanStack(tx.QueryRowContext(ctx, `
			UPDATE stacks
			SET status = COALESCE(NULLIF($3, ''), status),
				runtime_summary_json = $4::jsonb,
				drift_status = NULLIF($5, ''),
				drift_checked_at = $6,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`, tenantID, stackID, runtime.Status, runtimeJSON, runtime.DriftStatus, nullableTime(runtime.DriftCheckedAt)))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = stack
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpdateStackConfig(ctx context.Context, tenantID, stackID string, config map[string]any) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		configJSON, err := marshalObject(config)
		if err != nil {
			return err
		}
		stack, err := scanStack(tx.QueryRowContext(ctx, `
			UPDATE stacks
			SET config_json = $3::jsonb,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`, tenantID, stackID, configJSON))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = stack
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) SetStackHomelab(ctx context.Context, tenantID, stackID, homelabID string) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	homelabID = strings.TrimSpace(homelabID)
	if homelabID == "" {
		return nil, fmt.Errorf("controlplane: homelab id required")
	}

	var out *Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		stack, err := scanStack(tx.QueryRowContext(ctx, `
			UPDATE stacks
			SET homelab_id = $3,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`, tenantID, stackID, homelabID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = stack
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) CreateJob(ctx context.Context, req UpsertJobRequest) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" || strings.TrimSpace(req.ID) == "" {
		return nil, fmt.Errorf("controlplane: tenant id and job id required")
	}
	if jobWriteBypassesExecutionClaim(req.State) {
		return nil, fmt.Errorf("%w: running jobs must be admitted with StartJob", ErrConflict)
	}
	logsJSON, err := marshalArray(req.Logs)
	if err != nil {
		return nil, err
	}
	resultJSON, err := marshalObject(req.Result)
	if err != nil {
		return nil, err
	}
	scheduledFor := req.ScheduledFor
	if scheduledFor.IsZero() {
		scheduledFor = time.Now().UTC()
	}

	var out *Job
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			INSERT INTO jobs (
				id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json,
				result_json, scheduled_for
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7,
				$8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''),
				NULLIF($12, ''), $13::jsonb, $14::jsonb, $15
			)
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.ID, tenantID, req.InstanceID, req.StackID, req.Type, firstNonEmpty(req.State, jobStatePending),
			req.Priority, req.Progress, req.Step, req.Message, req.Error, req.ErrorDetails, logsJSON, resultJSON, scheduledFor))
		if isUniqueViolation(scanErr) {
			return ErrConflict
		}
		if scanErr != nil {
			return scanErr
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertJob(ctx context.Context, req UpsertJobRequest) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	if jobWriteBypassesExecutionClaim(req.State) {
		return nil, fmt.Errorf("%w: running jobs must be admitted with StartJob", ErrConflict)
	}

	var out *Job
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		logsJSON, err := marshalArray(req.Logs)
		if err != nil {
			return err
		}
		resultJSON, err := marshalObject(req.Result)
		if err != nil {
			return err
		}
		scheduledFor := req.ScheduledFor
		if scheduledFor.IsZero() {
			scheduledFor = time.Now().UTC()
		}
		job, err := scanJob(tx.QueryRowContext(ctx, `
			INSERT INTO jobs (
				id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json,
				result_json, scheduled_for
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7,
				$8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''),
				NULLIF($12, ''), $13::jsonb, $14::jsonb, $15
			)
			ON CONFLICT (id) DO UPDATE SET
				state = EXCLUDED.state,
				priority = EXCLUDED.priority,
				progress = EXCLUDED.progress,
				step = EXCLUDED.step,
				message = EXCLUDED.message,
				error = EXCLUDED.error,
				error_details = EXCLUDED.error_details,
				logs_json = EXCLUDED.logs_json,
				result_json = EXCLUDED.result_json,
				scheduled_for = EXCLUDED.scheduled_for,
				updated_at = now()
			WHERE jobs.tenant_id = EXCLUDED.tenant_id AND jobs.state <> 'running'
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`,
			req.ID,
			tenantID,
			req.InstanceID,
			req.StackID,
			req.Type,
			firstNonEmpty(req.State, jobStatePending),
			req.Priority,
			req.Progress,
			req.Step,
			req.Message,
			req.Error,
			req.ErrorDetails,
			logsJSON,
			resultJSON,
			scheduledFor,
		))
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) SyncJobSnapshot(ctx context.Context, syncReq SyncJobSnapshotRequest) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	req := syncReq.Job
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" || strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Type) == "" {
		return nil, fmt.Errorf("controlplane: exact job snapshot identity required")
	}
	observedState := strings.ToLower(strings.TrimSpace(syncReq.ObservedState))
	switch observedState {
	case jobStatePending, jobStateRunning, jobStateWaiting, jobStateCompleted, jobStateFailed, jobStateCanceled, jobStateCancelled:
	default:
		return nil, fmt.Errorf("controlplane: unsupported observed job state %q", syncReq.ObservedState)
	}
	if !validJobSnapshotProjection(observedState, req.State) {
		return nil, fmt.Errorf("%w: observed state %q cannot project as %q", ErrConflict, observedState, req.State)
	}
	logsJSON, err := marshalArray(req.Logs)
	if err != nil {
		return nil, err
	}
	resultJSON, err := marshalObject(req.Result)
	if err != nil {
		return nil, err
	}
	scheduledFor := req.ScheduledFor
	if scheduledFor.IsZero() {
		scheduledFor = time.Now().UTC()
	}
	updatedAt := time.Now().UTC()

	var out *Job
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET instance_id = COALESCE(NULLIF($3, ''), instance_id),
				type = $5, state = $6, priority = $7, progress = $8,
				step = NULLIF($9, ''), message = NULLIF($10, ''),
				error = NULLIF($11, ''), error_details = NULLIF($12, ''),
				logs_json = $13::jsonb, result_json = $14::jsonb,
				scheduled_for = $15, completed_at = $18, updated_at = $19,
				execution_owner_id = CASE WHEN $6 = 'running' THEN execution_owner_id ELSE NULL END,
				execution_lease_expires_at = CASE WHEN $6 = 'running'
					THEN clock_timestamp() + interval '`+jobExecutionLeaseTTLInterval+`'
					ELSE NULL END
			WHERE tenant_id = $1 AND id = $2
				AND stack_id IS NOT DISTINCT FROM NULLIF($4, '')
				AND (type = $5 OR (type = 'provision' AND $5 = 'deploy'))
				AND state IN ('pending', 'running')
				AND started_at IS NOT DISTINCT FROM $17
				AND (
					($16 = 'pending' AND (state = 'pending' OR (state = 'running' AND $17 IS NOT NULL))) OR
					($16 = 'running' AND state = 'running') OR
					$16 IN ('waiting', 'completed', 'failed', 'canceled', 'cancelled')
				)
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, tenantID, strings.TrimSpace(req.ID), req.InstanceID, strings.TrimSpace(req.StackID),
			strings.TrimSpace(req.Type), firstNonEmpty(req.State, jobStatePending), req.Priority, req.Progress,
			req.Step, req.Message, req.Error, req.ErrorDetails, logsJSON, resultJSON, scheduledFor,
			observedState, nullableTime(syncReq.AttemptStartedAt), nullableTime(syncReq.CompletedAt), updatedAt))
		if scanErr == sql.ErrNoRows {
			return ErrConflict
		}
		if scanErr != nil {
			return scanErr
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ClaimWaitingJobResume(ctx context.Context, req ClaimWaitingJobResumeRequest) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	req = normalizeClaimWaitingJobResumeRequest(req)
	if !validClaimWaitingJobResumeRequest(req) {
		return nil, fmt.Errorf("controlplane: exact waiting job identity required")
	}
	patchJSON, err := marshalObject(req.ResultPatch)
	if err != nil {
		return nil, err
	}

	var out *Job
	claimedAt := req.ClaimedAt.UTC()
	if claimedAt.IsZero() {
		claimedAt = time.Now().UTC()
	}
	err = s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'cancelled', result_json = result_json || $7::jsonb,
				message = 'Superseded by deterministic managed rollout recovery',
				completed_at = $8, updated_at = $8
			WHERE tenant_id = $1 AND id = $2 AND stack_id = $3 AND type = $4
				AND state = 'pending'
				AND result_json->'job_wait'->>'state' = 'waiting'
				AND result_json->'job_wait'->>'reason' = $5
				AND result_json->'job_wait'->>'next_resume_at' = $6
				AND COALESCE(NULLIF(result_json->>'lease_id', ''), NULLIF(result_json->>'runtime_lease_id', ''),
					NULLIF(result_json->>'enrollment_resume_lease_id', '')) = $9
				AND COALESCE(result_json->>'lease_id', '') IN ('', $9)
				AND COALESCE(result_json->>'runtime_lease_id', '') IN ('', $9)
				AND COALESCE(result_json->>'enrollment_resume_lease_id', '') IN ('', $9)
				AND COALESCE(result_json->>'server_id', '') IN ('', $10)
				AND COALESCE(result_json->>'runtime_server_id', '') IN ('', $10)
				AND COALESCE(result_json->>'enrollment_resume_server_id', '') IN ('', $10)
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.TenantID, req.JobID, req.StackID, req.JobType, req.WaitReason, req.NextResumeAt, patchJSON, claimedAt,
			req.LeaseID, req.ServerID))
		if scanErr == sql.ErrNoRows {
			return ErrConflict
		}
		if scanErr != nil {
			return scanErr
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeClaimWaitingJobResumeRequest(req ClaimWaitingJobResumeRequest) ClaimWaitingJobResumeRequest {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.JobID = strings.TrimSpace(req.JobID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.JobType = strings.TrimSpace(req.JobType)
	req.WaitReason = strings.TrimSpace(req.WaitReason)
	req.NextResumeAt = strings.TrimSpace(req.NextResumeAt)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.ServerID = strings.TrimSpace(req.ServerID)
	return req
}

func validClaimWaitingJobResumeRequest(req ClaimWaitingJobResumeRequest) bool {
	return req.TenantID != "" && req.JobID != "" && req.StackID != "" && req.JobType != "" &&
		req.WaitReason != "" && req.NextResumeAt != "" && req.LeaseID != "" && req.ServerID != ""
}

// ReclaimStaleManagedDestroyRecovery is deliberately narrower than a generic
// stale-job retry: it can only move one server-marked managed destroy from a
// silent running generation back to pending. The exact marker, tenant, stack,
// type and heartbeat cutoff all participate in the same UPDATE, so a live
// execution or an arbitrary result JSON row cannot be reclaimed.
func (s *PostgresStore) ReclaimStaleManagedDestroyRecovery(
	ctx context.Context,
	req ReclaimStaleManagedDestroyRecoveryRequest,
) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	req = normalizeReclaimStaleManagedDestroyRecoveryRequest(req)
	if !validReclaimStaleManagedDestroyRecoveryRequest(req) {
		return nil, fmt.Errorf("controlplane: exact stale managed destroy recovery identity required")
	}
	reclaimedAt := req.ReclaimedAt
	if reclaimedAt.IsZero() {
		reclaimedAt = time.Now().UTC()
	}

	var out *Job
	err := s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'pending', scheduled_for = $6, started_at = NULL,
				completed_at = NULL,
				message = 'Recovering stale managed provider decommission execution',
				error = NULL, error_details = NULL, updated_at = $6,
				execution_owner_id = NULL, execution_lease_expires_at = NULL
			WHERE tenant_id = $1 AND id = $2 AND stack_id = $3
				AND type = 'destroy' AND state = 'running' AND started_at IS NOT NULL
				AND updated_at <= $7
				AND (result_json -> $4::text ->> 'schema') = $5
				AND (result_json -> $4::text ->> 'tenant_id') = $1
				AND (result_json -> $4::text ->> 'stack_id') = $3
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.TenantID, req.JobID, req.StackID, req.RecoveryMarkerKey,
			req.RecoveryMarkerSchema, reclaimedAt, req.StaleBefore))
		if scanErr == sql.ErrNoRows {
			return ErrConflict
		}
		if scanErr != nil {
			return scanErr
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetJob(ctx context.Context, tenantID, jobID string) (*Job, error) {
	return s.getJob(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, jobID)
}

func (s *PostgresStore) ListJobsByTenant(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, tenantID, limit)
}

func (s *PostgresStore) ListProviderProvisionRecoveryCandidates(
	ctx context.Context,
	tenantID, operationID string,
	limit int,
) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	if tenantID == "" || operationID == "" {
		return nil, fmt.Errorf("controlplane: tenant and provider operation identity required")
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1
			AND type = 'provision'
			AND state = 'pending'
			AND result_json->>'operation_id' = $2
			AND result_json->'job_wait'->>'state' = 'waiting'
			AND result_json->'job_wait'->>'reason' = 'waiting_provider_provision'
		ORDER BY scheduled_for ASC, id ASC
		LIMIT $3
	`, tenantID, operationID, limit)
}

func (s *PostgresStore) ListManagedDestroyRecoveryCandidates(
	ctx context.Context,
	tenantID, markerKey, markerSchema string,
	limit int,
) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	tenantID = strings.TrimSpace(tenantID)
	markerKey = strings.TrimSpace(markerKey)
	markerSchema = strings.TrimSpace(markerSchema)
	if tenantID == "" || markerKey == "" || markerSchema == "" {
		return nil, fmt.Errorf("controlplane: tenant and managed destroy recovery marker required")
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND type = 'destroy' AND state IN ('pending', 'running')
			AND (result_json -> $2::text ->> 'schema') = $3
			AND (result_json -> $2::text ->> 'tenant_id') = $1
			AND (result_json -> $2::text ->> 'stack_id') = stack_id
			AND (state = 'running' OR scheduled_for <= now())
		ORDER BY CASE WHEN state = 'running' THEN updated_at ELSE scheduled_for END ASC, id ASC
		LIMIT $4
	`, tenantID, markerKey, markerSchema, limit)
}

func (s *PostgresStore) ListJobsByStack(ctx context.Context, tenantID, stackID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND stack_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`, tenantID, stackID, limit)
}

func (s *PostgresStore) ListPendingJobs(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND state = 'pending' AND scheduled_for <= now()
		ORDER BY priority DESC, created_at ASC
		LIMIT $2
	`, tenantID, limit)
}

func (s *PostgresStore) StartJob(ctx context.Context, tenantID, jobID string, at time.Time) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	jobID = strings.TrimSpace(jobID)
	if tenantID == "" || jobID == "" {
		return nil, fmt.Errorf("controlplane: tenant and job id required")
	}
	var out *Job
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var state, stackID string
		if err := tx.QueryRowContext(ctx, `
			SELECT state, COALESCE(stack_id, '') FROM jobs
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, jobID).Scan(&state, &stackID); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if state != jobStatePending {
			return ErrConflict
		}
		if stackID != "" {
			// PostgreSQL text values reject NUL bytes. Length-prefix the tenant
			// component to keep the composite advisory-lock key unambiguous while
			// remaining valid UTF-8 text.
			lockKey := fmt.Sprintf("%d:%s%s", len(tenantID), tenantID, stackID)
			if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockKey); err != nil {
				return err
			}
			var busy bool
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM jobs
					WHERE tenant_id = $1 AND stack_id = $2 AND id <> $3 AND state = 'running'
				)
			`, tenantID, stackID, jobID).Scan(&busy); err != nil {
				return err
			}
			if busy {
				return ErrStackExecutionBusy
			}
		}
		// StartJob is the only transition into 'running', so it is the only
		// place an execution lease is issued. The deadline comes from the
		// database clock, never a caller clock, so replicas with skewed clocks
		// cannot shorten or extend another replica's fence.
		job, err := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'running', started_at = $3, updated_at = $3,
				execution_owner_id = NULLIF($4, ''),
				execution_lease_expires_at = clock_timestamp() + interval '`+jobExecutionLeaseTTLInterval+`'
			WHERE tenant_id = $1 AND id = $2 AND state = 'pending'
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, tenantID, jobID, at, processExecutionOwnerID))
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) CompleteJob(ctx context.Context, tenantID, jobID string, result map[string]any, at time.Time) (*Job, error) {
	resultJSON, err := marshalObject(result)
	if err != nil {
		return nil, err
	}
	return s.updateRunningJob(ctx, tenantID, `
		UPDATE jobs
		SET state = 'completed', progress = 100, result_json = $3::jsonb,
			completed_at = $4, updated_at = $4,
			execution_owner_id = NULL, execution_lease_expires_at = NULL
		WHERE tenant_id = $1 AND id = $2 AND state = 'running'
		RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
	`, tenantID, jobID, resultJSON, at)
}

func (s *PostgresStore) FailJob(ctx context.Context, tenantID, jobID string, message, details string, at time.Time) (*Job, error) {
	return s.updateRunningJob(ctx, tenantID, `
		UPDATE jobs
		SET state = 'failed', error = NULLIF($3, ''), error_details = NULLIF($4, ''),
			completed_at = $5, updated_at = $5,
			execution_owner_id = NULL, execution_lease_expires_at = NULL
		WHERE tenant_id = $1 AND id = $2 AND state = 'running'
		RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		`, tenantID, jobID, message, details, at)
}

func (s *PostgresStore) UpsertWorkerHeartbeat(ctx context.Context, worker Worker) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(worker.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	tagsJSON, err := marshalObject(worker.Tags)
	if err != nil {
		return nil, err
	}
	capabilitiesJSON, err := marshalObject(worker.Capabilities)
	if err != nil {
		return nil, err
	}
	resourcesJSON, err := marshalObject(worker.Resources)
	if err != nil {
		return nil, err
	}
	lastSeen := nullableTime(worker.LastSeenAt)
	if worker.LastSeenAt == nil {
		lastSeen = time.Now().UTC()
	}

	var out *Worker
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanWorker(tx.QueryRowContext(ctx, `
			INSERT INTO workers (
				id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json, owner_subject_id, capabilities_json, resources_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12, $13, $14, $15, $16,
				NULLIF($17, ''), $18, $19, NULLIF($20, ''), NULLIF($21, ''), NULLIF($22, ''),
				$23::jsonb, NULLIF($24, ''), $25::jsonb, $26::jsonb
			)
			ON CONFLICT (tenant_id, id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				hostname = EXCLUDED.hostname,
				ip = EXCLUDED.ip,
				os = EXCLUDED.os,
				arch = EXCLUDED.arch,
				token_hash = EXCLUDED.token_hash,
				status = CASE
					WHEN workers.approved AND NULLIF(EXCLUDED.status, 'pending') IS NULL THEN workers.status
					ELSE EXCLUDED.status
				END,
				approved = workers.approved OR EXCLUDED.approved,
				approved_at = COALESCE(workers.approved_at, EXCLUDED.approved_at),
				last_seen_at = EXCLUDED.last_seen_at,
				cpu_cores = EXCLUDED.cpu_cores,
				ram_mb = EXCLUDED.ram_mb,
				disk_gb = EXCLUDED.disk_gb,
				gpu = EXCLUDED.gpu,
				has_nvme = EXCLUDED.has_nvme,
				has_hw_transcode = EXCLUDED.has_hw_transcode,
				docker_version = EXCLUDED.docker_version,
				type = EXCLUDED.type,
				provider = EXCLUDED.provider,
				tags_json = EXCLUDED.tags_json,
				owner_subject_id = COALESCE(workers.owner_subject_id, EXCLUDED.owner_subject_id),
				capabilities_json = EXCLUDED.capabilities_json,
				resources_json = (
					EXCLUDED.resources_json
					- 'agent_token_sha256'
					- 'enrollment_idempotency_sha256'
					- 'enrollment_request_sha256'
					- 'credential_generation'
				) || jsonb_strip_nulls(jsonb_build_object(
					'agent_token_sha256', workers.resources_json->'agent_token_sha256',
					'enrollment_idempotency_sha256', workers.resources_json->'enrollment_idempotency_sha256',
					'enrollment_request_sha256', workers.resources_json->'enrollment_request_sha256',
					'credential_generation', workers.resources_json->'credential_generation'
				)),
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
		`,
			worker.ID, tenantID, worker.InstanceID, worker.StackID, worker.Hostname, worker.IP,
			worker.OS, worker.Arch, worker.TokenHash, firstNonEmpty(worker.Status, "pending"),
			worker.Approved, nullableTime(worker.ApprovedAt), lastSeen, worker.CPUCores,
			worker.RAMMB, worker.DiskGB, worker.GPU, worker.HasNVME, worker.HasHWTranscode,
			worker.DockerVersion, worker.Type, worker.Provider, tagsJSON, worker.OwnerSubjectID,
			capabilitiesJSON, resourcesJSON,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) CompareAndSwapWorkerCredential(ctx context.Context, command WorkerCredentialCAS) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeWorkerCredentialCAS(command)
	if err != nil {
		return nil, err
	}
	nextJSON, err := marshalObject(workerCredentialResources(prepared.Next))
	if err != nil {
		return nil, err
	}

	var out *Worker
	err = s.withTenant(ctx, prepared.TenantID, func(tx *sql.Tx) error {
		saved, queryErr := scanWorker(tx.QueryRowContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) || $7::jsonb,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2
				AND COALESCE(resources_json->>'credential_generation', '0') = $3
				AND COALESCE(resources_json->>'agent_token_sha256', '') = $4
				AND COALESCE(resources_json->>'enrollment_idempotency_sha256', '') = $5
				AND COALESCE(resources_json->>'enrollment_request_sha256', '') = $6
			RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
		`, prepared.TenantID, prepared.WorkerID, strconv.FormatInt(prepared.Expected.Generation, 10),
			prepared.Expected.TokenSHA256, prepared.Expected.IdempotencySHA256,
			prepared.Expected.RequestSHA256, nextJSON))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if queryErr != nil {
			return queryErr
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetWorker(ctx context.Context, tenantID, workerID string) (*Worker, error) {
	return s.getWorker(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
			status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
			gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
			tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
			created_at, updated_at
		FROM workers
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, workerID)
}

func (s *PostgresStore) ListWorkersByTenant(ctx context.Context, tenantID string) ([]Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Worker
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
			FROM workers
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`, tenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			worker, err := scanWorker(rows)
			if err != nil {
				return err
			}
			out = append(out, *worker)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ApproveWorker(ctx context.Context, tenantID, workerID, ownerSubjectID string, approvedAt time.Time) (*Worker, error) {
	return s.getWorker(ctx, tenantID, `
		UPDATE workers
		SET approved = true,
			status = 'approved',
			approved_at = $4,
			owner_subject_id = $3,
			updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
			status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
			gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
			tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
			created_at, updated_at
	`, tenantID, workerID, ownerSubjectID, approvedAt)
}

func (s *PostgresStore) UpsertPairingToken(ctx context.Context, token PairingToken) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(token.Metadata)
	if err != nil {
		return nil, err
	}

	var out *PairingToken
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanPairingToken(tx.QueryRowContext(ctx, `
			INSERT INTO pairing_tokens (
				id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), $7,
				$8, $9, $10, $11::jsonb
			)
			ON CONFLICT (tenant_id, token_hash) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				owner_subject_id = EXCLUDED.owner_subject_id,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				expires_at = EXCLUDED.expires_at,
				used_at = EXCLUDED.used_at,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
		`,
			token.ID, tenantID, token.InstanceID, token.StackID, token.OwnerSubjectID, token.Name,
			token.TokenHash, firstNonEmpty(token.Status, "active"), nullableTime(token.ExpiresAt),
			nullableTime(token.UsedAt), metadataJSON,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetPairingTokenByHash(ctx context.Context, tenantID, tokenHash string) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	if tenantID == "" {
		token, err := scanPairingToken(s.db.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
			FROM pairing_tokens
			WHERE token_hash = $1
			ORDER BY created_at DESC
			LIMIT 1
		`, tokenHash))
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return token, err
	}

	var out *PairingToken
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		token, err := scanPairingToken(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
			FROM pairing_tokens
			WHERE tenant_id = $1 AND token_hash = $2
		`, tenantID, tokenHash))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = token
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ClaimPairingToken(ctx context.Context, tenantID, tokenHash string, claimedAt time.Time) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" || claimedAt.IsZero() {
		return nil, ErrNotFound
	}
	claimedAt = claimedAt.UTC()

	var out *PairingToken
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		claimed, err := scanPairingToken(tx.QueryRowContext(ctx, `
			UPDATE pairing_tokens
			SET status = 'used', used_at = $3, updated_at = $3
			WHERE tenant_id = $1 AND token_hash = $2
				AND status = 'active' AND used_at IS NULL
				AND (expires_at IS NULL OR expires_at > $3)
			RETURNING id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
		`, tenantID, tokenHash, claimedAt))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = claimed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) RevokePairingToken(ctx context.Context, tenantID, tokenID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE pairing_tokens
			SET status = 'revoked', updated_at = now()
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, tokenID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) UpsertNode(ctx context.Context, node Node) (*Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(node.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(node.Metadata)
	if err != nil {
		return nil, err
	}

	var out *Node
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanNode(tx.QueryRowContext(ctx, `
			INSERT INTO nodes (
				id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), $10::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				worker_id = EXCLUDED.worker_id,
				name = EXCLUDED.name,
				role = EXCLUDED.role,
				status = EXCLUDED.status,
				address = EXCLUDED.address,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
		`,
			node.ID, tenantID, node.InstanceID, node.StackID, node.WorkerID, node.Name,
			firstNonEmpty(node.Role, "foundation"), firstNonEmpty(node.Status, "pending"),
			node.Address, metadataJSON,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertService(ctx context.Context, service Service) (*Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(service.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(service.Metadata)
	if err != nil {
		return nil, err
	}
	resolved := resolvedLegacyServiceOwnership(service)

	var out *Service
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanService(tx.QueryRowContext(ctx, `
			INSERT INTO services (
				id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json, management_state
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8,
				$9, NULLIF($10, ''), NULLIF($11, ''), $12::jsonb, $13
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				node_id = EXCLUDED.node_id,
				service_key = EXCLUDED.service_key,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				source = EXCLUDED.source,
				url = EXCLUDED.url,
				migration_status = EXCLUDED.migration_status,
				metadata_json = EXCLUDED.metadata_json,
				management_state = EXCLUDED.management_state,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
		`,
			service.ID, tenantID, service.InstanceID, service.StackID, service.NodeID,
			service.ServiceKey, service.Name, resolved.Status,
			resolved.Source, service.URL, service.MigrationStatus, metadataJSON,
			resolved.ManagementState,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetNode(ctx context.Context, tenantID, nodeID string) (*Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *Node
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		node, err := scanNode(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
			FROM nodes
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, nodeID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = node
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetService(ctx context.Context, tenantID, serviceID string) (*Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *Service
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		service, err := scanService(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
			FROM services
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, serviceID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = service
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListNodesByStack(ctx context.Context, tenantID, stackID string) ([]Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Node
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
			FROM nodes
			WHERE tenant_id = $1 AND stack_id = $2
			ORDER BY created_at ASC
		`, tenantID, stackID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			node, err := scanNode(rows)
			if err != nil {
				return err
			}
			out = append(out, *node)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServicesByStack(ctx context.Context, tenantID, stackID string) ([]Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Service
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
			FROM services
			WHERE tenant_id = $1 AND stack_id = $2
			ORDER BY created_at ASC
		`, tenantID, stackID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			service, err := scanService(rows)
			if err != nil {
				return err
			}
			out = append(out, *service)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertServiceRuntime(ctx context.Context, service ServiceRuntime) (*ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(service.TenantID)
	if tenantID == "" || strings.TrimSpace(service.ID) == "" {
		return nil, fmt.Errorf("controlplane: tenant and service id required")
	}
	// Every service state write goes through the aggregate boundary: it owns
	// the canonical vocabulary, the derived legacy status projection, the
	// compare-and-swap revision, and the change-only transition timeline.
	result, err := s.ApplyServiceEvent(ctx, serviceRuntimeObservationEvent(service))
	if err != nil {
		return nil, err
	}
	return result.Service, nil
}

// serviceRuntimeObservationEvent adapts a measured service runtime projection
// to the aggregate command boundary. It is a Guard observation: it may seed
// desired state while the aggregate is created but never overwrites a stored
// intent.
func serviceRuntimeObservationEvent(service ServiceRuntime) ServiceEvent {
	accessURL, _ := service.Access["url"].(string)
	observedAt := time.Time{}
	if service.ObservedAt != nil {
		observedAt = service.ObservedAt.UTC()
	}
	return ServiceEvent{
		TenantID:   strings.TrimSpace(service.TenantID),
		ServiceID:  strings.TrimSpace(service.ID),
		Authority:  ServiceEventAuthorityGuard,
		Source:     firstNonEmpty(strings.TrimSpace(service.Source), "observed"),
		ObservedAt: observedAt,
		Runtime:    service,
		URL:        strings.TrimSpace(accessURL),
	}
}

func (s *PostgresStore) GetServiceRuntime(ctx context.Context, tenantID, serviceID string) (*ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *ServiceRuntime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		service, queryErr := scanServiceRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serviceRuntimeColumns+`
			FROM services WHERE tenant_id = $1 AND id = $2
				AND (server_id IS NOT NULL OR target_kind = 'managed_workload')
		`, tenantID, serviceID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			service, queryErr = scanServiceRuntime(tx.QueryRowContext(ctx, legacyServiceRuntimeBackfillQuery+`
				AND legacy.id = $2
			`, tenantID, serviceID))
			if errors.Is(queryErr, sql.ErrNoRows) {
				return ErrNotFound
			}
		}
		if queryErr != nil {
			return queryErr
		}
		out = service
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServiceRuntimes(ctx context.Context, tenantID, stackID, serverID string) ([]ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	out := make([]ServiceRuntime, 0)
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT `+serviceRuntimeColumns+`
			FROM services
			WHERE tenant_id = $1 AND (server_id IS NOT NULL OR target_kind = 'managed_workload')
				AND ($2 = '' OR stack_id = $2) AND ($3 = '' OR server_id = $3)
			ORDER BY stack_id, service_key, service_instance, id
		`, tenantID, strings.TrimSpace(stackID), strings.TrimSpace(serverID))
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			service, scanErr := scanServiceRuntime(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *service)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		backfillRows, queryErr := tx.QueryContext(ctx, legacyServiceRuntimeBackfillQuery+`
			AND ($2 = '' OR legacy.stack_id = $2) AND ($3 = '' OR mapped_server.id = $3)
			ORDER BY legacy.stack_id, lower(btrim(legacy.service_key)), legacy.id
		`, tenantID, strings.TrimSpace(stackID), strings.TrimSpace(serverID))
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = backfillRows.Close() }()
		for backfillRows.Next() {
			service, scanErr := scanServiceRuntime(backfillRows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *service)
		}
		return backfillRows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StackID != out[j].StackID {
			return out[i].StackID < out[j].StackID
		}
		if out[i].ServiceKey != out[j].ServiceKey {
			return out[i].ServiceKey < out[j].ServiceKey
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

const legacyServiceRuntimeBackfillQuery = `
	SELECT legacy.id, legacy.tenant_id, legacy.instance_id, legacy.stack_id,
		mapped_server.id,
		'server', NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::timestamptz,
		lower(btrim(legacy.service_key)), 'default', legacy.name,
		CASE WHEN lower(btrim(legacy.status)) IN ('stopped', 'exited', 'dead', 'archived', 'decommissioned') THEN 'stopped' ELSE 'running' END,
		'unknown', 'unknown', legacy.management_state, NULL::timestamptz, NULL::text,
		'{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}'::jsonb::text,
		'[]'::jsonb::text, 'legacy-registry-backfill',
		(legacy.metadata_json || jsonb_build_object('backfill', true, 'legacy_status', legacy.status, 'legacy_source', legacy.source))::text,
		legacy.created_at, legacy.updated_at
	FROM services legacy
	JOIN LATERAL (
		SELECT candidate.id
		FROM servers candidate
		WHERE candidate.tenant_id = legacy.tenant_id AND candidate.stack_id = legacy.stack_id
			AND (candidate.id = legacy.node_id OR candidate.node_id = legacy.node_id)
		ORDER BY (candidate.id = legacy.node_id) DESC, candidate.updated_at DESC, candidate.id
		LIMIT 1
	) mapped_server ON true
	WHERE legacy.tenant_id = $1 AND legacy.server_id IS NULL
		AND legacy.node_id IS NOT NULL AND btrim(legacy.node_id) <> '' AND btrim(legacy.service_key) <> ''
		AND NOT EXISTS (
			SELECT 1 FROM services measured
			WHERE measured.tenant_id = legacy.tenant_id AND measured.stack_id = legacy.stack_id
				AND measured.server_id = mapped_server.id
				AND lower(btrim(measured.service_key)) = lower(btrim(legacy.service_key))
				AND lower(btrim(measured.service_instance)) = 'default'
		)
`

func (s *PostgresStore) DeleteService(ctx context.Context, tenantID, serviceID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM services
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, serviceID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const serverRuntimeColumns = `id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id,
	lease_id, provider_ref, environment_class, offering, provider_id, provider_target_ref,
	availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
	name, lifecycle_state, desired_state, connection_state,
	health_state, reason_code, connection_changed_at, last_heartbeat_at,
	inventory_revision, revision, generation, source_authority, source_id,
	source_epoch, source_sequence, source_observed_at, channels_json::text, metadata_json::text,
	decommissioned_at, lifecycle_reason_code, desired_reason_code,
	connection_reason_code, health_reason_code, lifecycle_changed_at,
	desired_changed_at, health_changed_at, created_at, updated_at`

func (s *PostgresStore) UpsertServerRuntime(ctx context.Context, server ServerRuntime) (*ServerRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" || strings.TrimSpace(server.ID) == "" {
		return nil, fmt.Errorf("controlplane: tenant id and server id required")
	}
	targetOmitted := !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget)
	serverRuntimeDefaults(&server, time.Now().UTC())
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	channelsJSON, err := json.Marshal(server.Channels)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalObject(server.Metadata)
	if err != nil {
		return nil, err
	}
	var out *ServerRuntime
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			INSERT INTO servers (
				id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id, lease_id,
				provider_ref, environment_class, offering, provider_id, provider_target_ref,
				availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
				name, lifecycle_state, desired_state, connection_state,
				health_state, reason_code, connection_changed_at, last_heartbeat_at,
				inventory_revision, revision, generation, source_authority, source_id,
				source_epoch, source_sequence, source_observed_at, channels_json, metadata_json, decommissioned_at,
				lifecycle_reason_code, desired_reason_code, connection_reason_code, health_reason_code,
				lifecycle_changed_at, desired_changed_at, health_changed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
				NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), $17,
				$18, $19, $20, $21, $22, NULLIF($23, ''), $24, $25,
				$26, $27, $28, NULLIF($29, ''), NULLIF($30, ''), NULLIF($31, ''), $32, $33,
				$34::jsonb, $35::jsonb, $36, NULLIF($37, ''), NULLIF($38, ''),
				NULLIF($39, ''), NULLIF($40, ''), $41, $42, $43
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				owner_subject_id = EXCLUDED.owner_subject_id,
				worker_id = EXCLUDED.worker_id,
				node_id = EXCLUDED.node_id,
				lease_id = EXCLUDED.lease_id,
				provider_ref = EXCLUDED.provider_ref,
				environment_class = CASE WHEN $44 THEN servers.environment_class ELSE EXCLUDED.environment_class END,
				offering = CASE WHEN $44 THEN servers.offering ELSE EXCLUDED.offering END,
				provider_id = CASE WHEN $44 THEN servers.provider_id ELSE EXCLUDED.provider_id END,
				provider_target_ref = CASE WHEN $44 THEN servers.provider_target_ref ELSE EXCLUDED.provider_target_ref END,
				availability_owner = CASE WHEN $44 THEN servers.availability_owner ELSE EXCLUDED.availability_owner END,
				operations_owner = CASE WHEN $44 THEN servers.operations_owner ELSE EXCLUDED.operations_owner END,
				runtime_target_evidence_ref = CASE WHEN $44 THEN servers.runtime_target_evidence_ref ELSE EXCLUDED.runtime_target_evidence_ref END,
				runtime_target_observed_at = CASE WHEN $44 THEN servers.runtime_target_observed_at ELSE EXCLUDED.runtime_target_observed_at END,
				name = EXCLUDED.name,
				lifecycle_state = EXCLUDED.lifecycle_state,
				desired_state = EXCLUDED.desired_state,
				connection_state = EXCLUDED.connection_state,
				health_state = EXCLUDED.health_state,
				reason_code = EXCLUDED.reason_code,
				connection_changed_at = EXCLUDED.connection_changed_at,
				last_heartbeat_at = EXCLUDED.last_heartbeat_at,
				inventory_revision = GREATEST(servers.inventory_revision, EXCLUDED.inventory_revision),
				revision = servers.revision + 1,
				generation = GREATEST(servers.generation, EXCLUDED.generation),
				source_authority = COALESCE(EXCLUDED.source_authority, servers.source_authority),
				source_id = COALESCE(EXCLUDED.source_id, servers.source_id),
				source_epoch = COALESCE(EXCLUDED.source_epoch, servers.source_epoch),
				source_sequence = CASE WHEN EXCLUDED.source_epoch IS NULL THEN servers.source_sequence ELSE EXCLUDED.source_sequence END,
				source_observed_at = COALESCE(EXCLUDED.source_observed_at, servers.source_observed_at),
				channels_json = EXCLUDED.channels_json,
				metadata_json = EXCLUDED.metadata_json,
				decommissioned_at = EXCLUDED.decommissioned_at,
				lifecycle_reason_code = COALESCE(EXCLUDED.lifecycle_reason_code, servers.lifecycle_reason_code),
				desired_reason_code = COALESCE(EXCLUDED.desired_reason_code, servers.desired_reason_code),
				connection_reason_code = COALESCE(EXCLUDED.connection_reason_code, servers.connection_reason_code),
				health_reason_code = COALESCE(EXCLUDED.health_reason_code, servers.health_reason_code),
				lifecycle_changed_at = EXCLUDED.lifecycle_changed_at,
				desired_changed_at = EXCLUDED.desired_changed_at,
				health_changed_at = EXCLUDED.health_changed_at,
				updated_at = now()
			WHERE servers.tenant_id = EXCLUDED.tenant_id
		RETURNING `+serverRuntimeColumns,
			server.ID, tenantID, server.InstanceID, server.StackID, server.OwnerSubjectID, server.WorkerID, server.NodeID,
			server.LeaseID, server.ProviderRef, string(server.RuntimeTarget.EnvironmentClass), string(server.RuntimeTarget.Offering),
			server.RuntimeTarget.ProviderID, server.RuntimeTarget.ProviderTargetRef,
			string(server.RuntimeTarget.AvailabilityOwner), string(server.RuntimeTarget.OperationsOwner),
			server.RuntimeTarget.EvidenceRef, nullableTime(server.RuntimeTarget.ObservedAt),
			server.Name, server.LifecycleState, server.DesiredState, server.ConnectionState,
			server.HealthState, server.ReasonCode, server.ConnectionChangedAt, nullableTime(server.LastHeartbeatAt),
			server.InventoryRevision, server.Revision, server.Generation, server.SourceAuthority, server.SourceID,
			server.SourceEpoch, server.SourceSequence, nullableTime(server.SourceObservedAt),
			channelsJSON, metadataJSON, nullableTime(server.DecommissionedAt),
			server.LifecycleReasonCode, server.DesiredReasonCode, server.ConnectionReasonCode,
			server.HealthReasonCode, server.LifecycleChangedAt, server.DesiredChangedAt,
			server.HealthChangedAt, targetOmitted,
		))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) EnsureServerRuntimeProjection(ctx context.Context, server ServerRuntime) (*ServerRuntime, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" || strings.TrimSpace(server.ID) == "" {
		return nil, false, fmt.Errorf("controlplane: tenant id and server id required")
	}
	serverRuntimeDefaults(&server, time.Now().UTC())
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, false, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	channelsJSON, err := json.Marshal(server.Channels)
	if err != nil {
		return nil, false, err
	}
	metadataJSON, err := marshalObject(server.Metadata)
	if err != nil {
		return nil, false, err
	}
	var (
		out     *ServerRuntime
		created bool
	)
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			INSERT INTO servers (
				id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id, lease_id,
				provider_ref, environment_class, offering, provider_id, provider_target_ref,
				availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
				name, lifecycle_state, desired_state, connection_state,
				health_state, reason_code, connection_changed_at, last_heartbeat_at,
				inventory_revision, revision, generation, source_authority, source_id,
				source_epoch, source_sequence, source_observed_at, channels_json, metadata_json, decommissioned_at,
				lifecycle_reason_code, desired_reason_code, connection_reason_code, health_reason_code,
				lifecycle_changed_at, desired_changed_at, health_changed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
				NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), $17,
				$18, $19, $20, $21, $22, NULLIF($23, ''), $24, $25,
				$26, $27, $28, NULLIF($29, ''), NULLIF($30, ''), NULLIF($31, ''), $32, $33,
				$34::jsonb, $35::jsonb, $36, NULLIF($37, ''), NULLIF($38, ''),
				NULLIF($39, ''), NULLIF($40, ''), $41, $42, $43
			)
			ON CONFLICT (id) DO NOTHING
		RETURNING `+serverRuntimeColumns,
			server.ID, tenantID, server.InstanceID, server.StackID, server.OwnerSubjectID, server.WorkerID, server.NodeID,
			server.LeaseID, server.ProviderRef, string(server.RuntimeTarget.EnvironmentClass), string(server.RuntimeTarget.Offering),
			server.RuntimeTarget.ProviderID, server.RuntimeTarget.ProviderTargetRef,
			string(server.RuntimeTarget.AvailabilityOwner), string(server.RuntimeTarget.OperationsOwner),
			server.RuntimeTarget.EvidenceRef, nullableTime(server.RuntimeTarget.ObservedAt),
			server.Name, server.LifecycleState, server.DesiredState, server.ConnectionState,
			server.HealthState, server.ReasonCode, server.ConnectionChangedAt, nullableTime(server.LastHeartbeatAt),
			server.InventoryRevision, server.Revision, server.Generation, server.SourceAuthority, server.SourceID,
			server.SourceEpoch, server.SourceSequence, nullableTime(server.SourceObservedAt),
			channelsJSON, metadataJSON, nullableTime(server.DecommissionedAt),
			server.LifecycleReasonCode, server.DesiredReasonCode, server.ConnectionReasonCode,
			server.HealthReasonCode, server.LifecycleChangedAt, server.DesiredChangedAt,
			server.HealthChangedAt,
		))
		switch {
		case rowErr == nil:
			out = row
			created = true
			return nil
		case !errors.Is(rowErr, sql.ErrNoRows):
			return rowErr
		}
		row, rowErr = scanServerRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serverRuntimeColumns+` FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, server.ID))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

func (s *PostgresStore) GetServerRuntime(ctx context.Context, tenantID, serverID string) (*ServerRuntime, error) {
	if strings.TrimSpace(serverID) == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `SELECT `+serverRuntimeColumns+`
		FROM servers WHERE tenant_id = $1 AND id = $2`, scanServerRuntime, serverID)
}

func (s *PostgresStore) ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]ServerRuntime, error) {
	query := `SELECT ` + serverRuntimeColumns + ` FROM servers WHERE tenant_id = $1`
	args := []any{}
	if strings.TrimSpace(stackID) != "" {
		query += ` AND stack_id = $2`
		args = append(args, strings.TrimSpace(stackID))
	}
	query += ` ORDER BY updated_at DESC, id ASC`
	return queryTenantRows(ctx, s, tenantID, query, scanServerRuntime, args...)
}

func (s *PostgresStore) AppendServerTransition(ctx context.Context, transition ServerStateTransition) (*ServerStateTransition, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(transition.TenantID)
	if tenantID == "" || strings.TrimSpace(transition.ServerID) == "" {
		return nil, fmt.Errorf("controlplane: tenant id and server id required")
	}
	if transition.ObservedAt.IsZero() {
		transition.ObservedAt = time.Now().UTC()
	}
	evidenceJSON, err := marshalObject(transition.Evidence)
	if err != nil {
		return nil, err
	}
	var out *ServerStateTransition
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerTransition(tx.QueryRowContext(ctx, `
			INSERT INTO server_state_transitions (
				tenant_id, server_id, dimension, from_state, to_state, reason_code,
				source, observed_at, evidence_json
			) VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, $9::jsonb)
			RETURNING id, tenant_id, server_id, dimension, from_state, to_state,
				reason_code, source, observed_at, evidence_json::text, created_at
		`, tenantID, transition.ServerID, transition.Dimension, transition.FromState,
			transition.ToState, transition.ReasonCode, transition.Source,
			transition.ObservedAt, evidenceJSON))
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServerTransitions(ctx context.Context, tenantID, serverID string, limit int) ([]ServerStateTransition, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, dimension, from_state, to_state,
			reason_code, source, observed_at, evidence_json::text, created_at
		FROM server_state_transitions
		WHERE tenant_id = $1 AND server_id = $2
		ORDER BY observed_at DESC, id DESC LIMIT $3
	`, scanServerTransition, serverID, limit)
}

func (s *PostgresStore) RecordServerInventory(ctx context.Context, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(snapshot.TenantID)
	if tenantID == "" || strings.TrimSpace(snapshot.ServerID) == "" || snapshot.Revision <= 0 {
		return nil, fmt.Errorf("controlplane: tenant, server, and positive revision required")
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	inventoryJSON, err := marshalObject(snapshot.Inventory)
	if err != nil {
		return nil, err
	}
	var out *ServerInventorySnapshot
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `UPDATE servers SET inventory_revision = $3, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND inventory_revision < $3`, tenantID, snapshot.ServerID, snapshot.Revision)
		if execErr != nil {
			return execErr
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrConflict
		}
		row, rowErr := scanServerInventory(tx.QueryRowContext(ctx, `
			INSERT INTO server_inventory_snapshots (
				tenant_id, server_id, revision, source, observed_at, inventory_json
			) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
			RETURNING id, tenant_id, server_id, revision, source, observed_at,
				inventory_json::text, created_at
		`, tenantID, snapshot.ServerID, snapshot.Revision, snapshot.Source,
			snapshot.ObservedAt, inventoryJSON))
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertRILServer(ctx context.Context, server RILServer) (*RILServer, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	healthJSON, err := marshalObject(server.Health)
	if err != nil {
		return nil, err
	}
	inventoryJSON, err := marshalObject(server.Inventory)
	if err != nil {
		return nil, err
	}

	var out *RILServer
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILServer(tx.QueryRowContext(ctx, `
			INSERT INTO ril_servers (
				id, tenant_id, instance_id, stack_id, node_id, name, status,
				health_json, inventory_json, last_seen_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7,
				$8::jsonb, $9::jsonb, $10
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				node_id = EXCLUDED.node_id,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				health_json = EXCLUDED.health_json,
				inventory_json = EXCLUDED.inventory_json,
				last_seen_at = EXCLUDED.last_seen_at,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, node_id, name, status,
				health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		`,
			server.ID, tenantID, server.InstanceID, server.StackID, server.NodeID,
			server.Name, firstNonEmpty(server.Status, "unknown"), healthJSON,
			inventoryJSON, nullableTime(server.LastSeenAt),
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryOneTenantRow runs a single-row tenant-scoped query through withTenant,
// mapping sql.ErrNoRows to ErrNotFound. The query's $1 is always the tenant id;
// extra args bind from $2 onward.
func queryOneTenantRow[T any](ctx context.Context, s *PostgresStore, tenantID, query string, scan func(rowScanner) (*T, error), args ...any) (*T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *T
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		queryArgs := append([]any{tenantID}, args...)
		row, err := scan(tx.QueryRowContext(ctx, query, queryArgs...))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryTenantRows runs a multi-row tenant-scoped query through withTenant. The
// query's $1 is always the tenant id; extra args bind from $2 onward.
func queryTenantRows[T any](ctx context.Context, s *PostgresStore, tenantID, query string, scan func(rowScanner) (*T, error), args ...any) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out []T
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		queryArgs := append([]any{tenantID}, args...)
		rows, err := tx.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListRILServersByTenant(ctx context.Context, tenantID string) ([]RILServer, error) {
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, node_id, name, status,
			health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		FROM ril_servers
		WHERE tenant_id = $1
		ORDER BY last_seen_at DESC NULLS LAST, created_at DESC
	`, scanRILServer)
}

func (s *PostgresStore) GetRILServer(ctx context.Context, tenantID, serverID string) (*RILServer, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, node_id, name, status,
			health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		FROM ril_servers
		WHERE tenant_id = $1 AND (id = $2 OR node_id = $2)
		ORDER BY last_seen_at DESC NULLS LAST
		LIMIT 1
	`, scanRILServer, serverID)
}

func (s *PostgresStore) GetRILCommand(ctx context.Context, tenantID, commandID string) (*RILCommand, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, actor_subject_id, command_class, status,
			request_json::text, result_json::text, error, created_at, updated_at, completed_at
		FROM ril_commands
		WHERE tenant_id = $1 AND id = $2
	`, scanRILCommand, commandID)
}

func (s *PostgresStore) EnqueueRILCommand(ctx context.Context, command RILCommand) (*RILCommand, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(command.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	requestJSON, err := marshalObject(command.Request)
	if err != nil {
		return nil, err
	}
	resultJSON, err := marshalObject(command.Result)
	if err != nil {
		return nil, err
	}

	var out *RILCommand
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILCommand(tx.QueryRowContext(ctx, `
			INSERT INTO ril_commands (
				id, tenant_id, server_id, actor_subject_id, command_class, status,
				request_json, result_json, error, completed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6,
				$7::jsonb, $8::jsonb, NULLIF($9, ''), $10
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				actor_subject_id = EXCLUDED.actor_subject_id,
				command_class = EXCLUDED.command_class,
				status = EXCLUDED.status,
				request_json = EXCLUDED.request_json,
				result_json = EXCLUDED.result_json,
				error = EXCLUDED.error,
				completed_at = EXCLUDED.completed_at,
				updated_at = now()
			RETURNING id, tenant_id, server_id, actor_subject_id, command_class, status,
				request_json::text, result_json::text, error, created_at, updated_at, completed_at
		`,
			command.ID, tenantID, command.ServerID, command.ActorSubjectID,
			command.CommandClass, firstNonEmpty(command.Status, "queued"),
			requestJSON, resultJSON, command.Error, nullableTime(command.CompletedAt),
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertActionCard(ctx context.Context, card RILActionCard) (*RILActionCard, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(card.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	actionJSON, err := marshalObject(card.Action)
	if err != nil {
		return nil, err
	}
	decisionJSON, err := marshalObject(card.Decision)
	if err != nil {
		return nil, err
	}

	var out *RILActionCard
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILActionCard(tx.QueryRowContext(ctx, `
			INSERT INTO ril_action_cards (
				id, tenant_id, server_id, stack_id, title, status, severity,
				action_json, decision_json, resolved_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7,
				$8::jsonb, $9::jsonb, $10
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				stack_id = EXCLUDED.stack_id,
				title = EXCLUDED.title,
				status = EXCLUDED.status,
				severity = EXCLUDED.severity,
				action_json = EXCLUDED.action_json,
				decision_json = EXCLUDED.decision_json,
				resolved_at = EXCLUDED.resolved_at,
				updated_at = now()
			RETURNING id, tenant_id, server_id, stack_id, title, status, severity,
				action_json::text, decision_json::text, created_at, updated_at, resolved_at
		`,
			card.ID, tenantID, card.ServerID, card.StackID, card.Title,
			firstNonEmpty(card.Status, "open"), firstNonEmpty(card.Severity, "info"),
			actionJSON, decisionJSON, nullableTime(card.ResolvedAt),
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) RecordHealEvent(ctx context.Context, event RILHealEvent) (*RILHealEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	detailsJSON, err := marshalObject(event.Details)
	if err != nil {
		return nil, err
	}

	var out *RILHealEvent
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILHealEvent(tx.QueryRowContext(ctx, `
			INSERT INTO ril_heal_events (
				id, tenant_id, server_id, action_card_id, status, cause, details_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), $7::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				action_card_id = EXCLUDED.action_card_id,
				status = EXCLUDED.status,
				cause = EXCLUDED.cause,
				details_json = EXCLUDED.details_json,
				updated_at = now()
			RETURNING id, tenant_id, server_id, action_card_id, status, cause,
				details_json::text, created_at, updated_at
		`,
			event.ID, tenantID, event.ServerID, event.ActionCardID, event.Status,
			event.Cause, detailsJSON,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(item.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(item.Metadata)
		if err != nil {
			return err
		}
		walletItem, err := scanWalletItem(tx.QueryRowContext(ctx, `
			INSERT INTO wallet_items (
				id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''),
				NULLIF($7, ''), $8::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				stack_id = EXCLUDED.stack_id,
				item_type = EXCLUDED.item_type,
				provider = EXCLUDED.provider,
				external_ref = EXCLUDED.external_ref,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			WHERE wallet_items.tenant_id = EXCLUDED.tenant_id
			RETURNING id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
		`,
			item.ID,
			tenantID,
			item.InstanceID,
			item.StackID,
			firstNonEmpty(item.ItemType, "other"),
			item.Provider,
			item.ExternalRef,
			metadataJSON,
		))
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = walletItem
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) StoreOwnerSpecToken(ctx context.Context, token OwnerSpecToken) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if tenantID == "" || strings.TrimSpace(token.TokenHash) == "" || strings.TrimSpace(token.StackID) == "" || strings.TrimSpace(token.OwnerID) == "" || token.ExpiresAt.IsZero() {
		return fmt.Errorf("controlplane: incomplete owner spec token")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO owner_spec_tokens (token_hash, tenant_id, stack_id, owner_id, status, expires_at)
			VALUES ($1, $2, $3, $4, 'issued', $5)
			ON CONFLICT (token_hash) DO UPDATE SET
				status = 'issued', expires_at = EXCLUDED.expires_at, consumed_at = NULL
			WHERE owner_spec_tokens.tenant_id = EXCLUDED.tenant_id
				AND owner_spec_tokens.stack_id = EXCLUDED.stack_id
				AND owner_spec_tokens.owner_id = EXCLUDED.owner_id
		`, token.TokenHash, tenantID, token.StackID, token.OwnerID, token.ExpiresAt.UTC())
		return err
	})
}

func (s *PostgresStore) ConsumeOwnerSpecToken(ctx context.Context, token OwnerSpecToken, consumedAt time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if consumedAt.IsZero() {
		consumedAt = time.Now().UTC()
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE owner_spec_tokens
			SET status = 'consumed', consumed_at = $5
			WHERE token_hash = $1 AND tenant_id = $2 AND stack_id = $3 AND owner_id = $4
				AND status = 'issued' AND expires_at > $5
		`, token.TokenHash, tenantID, token.StackID, token.OwnerID, consumedAt.UTC())
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) GetWalletItem(ctx context.Context, tenantID, itemID string) (*WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		item, err := scanWalletItem(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
			FROM wallet_items
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, itemID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListWalletItems(ctx context.Context, tenantID, stackID string) ([]WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	stackID = strings.TrimSpace(stackID)

	var out []WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		query := `
			SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
			FROM wallet_items
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`
		args := []any{tenantID}
		if stackID != "" {
			query = `
				SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
					external_ref, metadata_json::text, created_at, updated_at
				FROM wallet_items
				WHERE tenant_id = $1 AND stack_id = $2
				ORDER BY created_at DESC
			`
			args = append(args, stackID)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			item, err := scanWalletItem(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) DeleteWalletItem(ctx context.Context, tenantID, itemID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}

	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM wallet_items
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, itemID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) UpsertTenant(ctx context.Context, tenant Tenant) (*Tenant, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(tenant.ID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Tenant
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(tenant.Metadata)
		if err != nil {
			return err
		}
		created, err := scanTenant(tx.QueryRowContext(ctx, `
			INSERT INTO techstack_tenants (
				id, external_org_id, display_name, kind, status, metadata_json
			) VALUES (
				$1, NULLIF($2, ''), $3, $4, $5, $6::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				external_org_id = EXCLUDED.external_org_id,
				display_name = EXCLUDED.display_name,
				kind = EXCLUDED.kind,
				status = EXCLUDED.status,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, external_org_id, display_name, kind, status,
				metadata_json::text, created_at, updated_at
		`,
			tenantID,
			tenant.ExternalOrgID,
			firstNonEmpty(tenant.DisplayName, tenantID),
			firstNonEmpty(tenant.Kind, "self_hosted"),
			firstNonEmpty(tenant.Status, "active"),
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureTenant creates a tenant when it is missing and otherwise returns the
// existing row without changing its identity, kind, status, or metadata. It is
// intended for startup paths that need the tenant foreign-key anchor before
// the first tenant-scoped runtime record is written.
func (s *PostgresStore) EnsureTenant(ctx context.Context, tenant Tenant) (*Tenant, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(tenant.ID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Tenant
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(tenant.Metadata)
		if err != nil {
			return err
		}
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO techstack_tenants (
				id, external_org_id, display_name, kind, status, metadata_json
			) VALUES (
				$1, NULLIF($2, ''), $3, $4, $5, $6::jsonb
			)
			ON CONFLICT (id) DO NOTHING
		`,
			tenantID,
			tenant.ExternalOrgID,
			firstNonEmpty(tenant.DisplayName, tenantID),
			firstNonEmpty(tenant.Kind, "self_hosted"),
			firstNonEmpty(tenant.Status, "active"),
			metadataJSON,
		); execErr != nil {
			return execErr
		}
		ensured, err := scanTenant(tx.QueryRowContext(ctx, `
			SELECT id, external_org_id, display_name, kind, status,
				metadata_json::text, created_at, updated_at
			FROM techstack_tenants
			WHERE id = $1
		`, tenantID))
		if err != nil {
			return err
		}
		out = ensured
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertUser(ctx context.Context, user User) (*User, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	userID := strings.TrimSpace(user.ID)
	if userID == "" {
		return nil, fmt.Errorf("controlplane: user id required")
	}

	metadataJSON, err := marshalObject(user.Metadata)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	out, err := scanUser(tx.QueryRowContext(ctx, `
		INSERT INTO techstack_users (
			id, primary_email, display_name, status, metadata_json
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, $5::jsonb
		)
		ON CONFLICT (id) DO UPDATE SET
			primary_email = EXCLUDED.primary_email,
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			metadata_json = EXCLUDED.metadata_json,
			updated_at = now()
		RETURNING id, primary_email, display_name, status,
			metadata_json::text, created_at, updated_at
	`, userID, user.PrimaryEmail, user.DisplayName, firstNonEmpty(user.Status, "active"), metadataJSON))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return out, nil
}

func (s *PostgresStore) UpsertMembership(ctx context.Context, membership Membership) (*Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(membership.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Membership
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(membership.Metadata)
		if err != nil {
			return err
		}
		created, err := scanMembership(tx.QueryRowContext(ctx, `
			INSERT INTO techstack_memberships (
				id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json
			) VALUES (
				$1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8::jsonb
			)
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET
				role_key = EXCLUDED.role_key,
				provider_key = EXCLUDED.provider_key,
				subject_id = EXCLUDED.subject_id,
				status = EXCLUDED.status,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json::text, created_at, updated_at
		`,
			membership.ID,
			tenantID,
			membership.UserID,
			firstNonEmpty(membership.RoleKey, "member"),
			membership.ProviderKey,
			membership.SubjectID,
			firstNonEmpty(membership.Status, "active"),
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetMembership(ctx context.Context, tenantID, userID string) (*Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return nil, ErrNotFound
	}

	var out *Membership
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		membership, err := scanMembership(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json::text, created_at, updated_at
			FROM techstack_memberships
			WHERE tenant_id = $1 AND user_id = $2
			LIMIT 1
		`, tenantID, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = membership
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListMembershipsByUser returns the user's active memberships across all
// tenants, newest first. techstack_memberships is FORCE-RLS tenant-fenced;
// the membership_self_lookup policy (migration 043) opens SELECT for exactly
// the rows whose user_id matches the request-local app.user_id GUC set here.
func (s *PostgresStore) ListMembershipsByUser(ctx context.Context, userID string) ([]Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("controlplane: user id required")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, gucErr := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", userGUC, userID); gucErr != nil {
		return nil, fmt.Errorf("set user guc: %w", gucErr)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, user_id, role_key, provider_key, subject_id,
			status, metadata_json::text, created_at, updated_at
		FROM techstack_memberships
		WHERE user_id = $1 AND status = 'active'
		ORDER BY updated_at DESC, id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var memberships []Membership
	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, *membership)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return memberships, nil
}

func (s *PostgresStore) UpsertAuthConfig(ctx context.Context, config AuthConfig) (*AuthConfig, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(config.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *AuthConfig
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		configJSON, err := marshalObject(config.Config)
		if err != nil {
			return err
		}
		created, err := scanAuthConfig(tx.QueryRowContext(ctx, `
			INSERT INTO auth_config (
				id, tenant_id, instance_id, mode, config_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5::jsonb
			)
			ON CONFLICT (tenant_id, instance_id) DO UPDATE SET
				mode = EXCLUDED.mode,
				config_json = EXCLUDED.config_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, mode, config_json::text,
				created_at, updated_at
		`,
			config.ID,
			tenantID,
			config.InstanceID,
			firstNonEmpty(config.Mode, "local"),
			configJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertBreakglassAdmin(ctx context.Context, admin BreakglassAdmin) (*BreakglassAdmin, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(admin.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *BreakglassAdmin
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(admin.Metadata)
		if err != nil {
			return err
		}
		created, err := scanBreakglassAdmin(tx.QueryRowContext(ctx, `
			INSERT INTO breakglass_admin (
				id, tenant_id, user_id, email, password_hash, locked, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, $6, $7::jsonb
			)
			ON CONFLICT (tenant_id) DO UPDATE SET
				user_id = EXCLUDED.user_id,
				email = EXCLUDED.email,
				password_hash = EXCLUDED.password_hash,
				locked = EXCLUDED.locked,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, user_id, email, password_hash, locked,
				last_used_at, metadata_json::text, created_at, updated_at
		`,
			admin.ID,
			tenantID,
			admin.UserID,
			admin.Email,
			admin.PasswordHash,
			admin.Locked,
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetBreakglassAdmin(ctx context.Context, tenantID string) (*BreakglassAdmin, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *BreakglassAdmin
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		admin, err := scanBreakglassAdmin(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, user_id, email, password_hash, locked,
				last_used_at, metadata_json::text, created_at, updated_at
			FROM breakglass_admin
			WHERE tenant_id = $1
		`, tenantID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = admin
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) AppendActivity(ctx context.Context, event ActivityEvent) (*ActivityEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	event = normalizeActivityEvent(event)
	tenantID := event.TenantID
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *ActivityEvent
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		detailsJSON, err := marshalObject(event.Details)
		if err != nil {
			return err
		}
		created, err := scanActivityEvent(tx.QueryRowContext(ctx, `
			INSERT INTO activity_log (
				id, tenant_id, instance_id, stack_id, actor_subject_id, action,
				category, severity, message, details_json, runtime_scope_key,
				server_scope_key, service_scope_key, correlation_id
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6,
				$7, $8, NULLIF($9, ''), $10::jsonb, NULLIF($11, ''),
				NULLIF($12, ''), NULLIF($13, ''), NULLIF($14, '')
			)
			RETURNING id, tenant_id, instance_id, stack_id, actor_subject_id,
				action, category, severity, message, details_json::text,
				runtime_scope_key, server_scope_key, service_scope_key, correlation_id,
				created_at
		`,
			event.ID,
			tenantID,
			event.InstanceID,
			event.StackID,
			event.ActorSubjectID,
			event.Action,
			firstNonEmpty(event.Category, "system"),
			firstNonEmpty(event.Severity, "info"),
			event.Message,
			detailsJSON,
			event.RuntimeScopeKey,
			event.ServerScopeKey,
			event.ServiceScopeKey,
			event.CorrelationID,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListActivity(ctx context.Context, tenantID, stackID string, limit int) ([]ActivityEvent, error) {
	return s.ListActivityScoped(ctx, tenantID, ActivityFilter{StackID: stackID, Limit: limit})
}

func (s *PostgresStore) ListActivityScoped(ctx context.Context, tenantID string, filter ActivityFilter) ([]ActivityEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	filter.StackID = strings.TrimSpace(filter.StackID)
	filter.RuntimeScopeKey = strings.TrimSpace(filter.RuntimeScopeKey)
	filter.ServerScopeKey = strings.TrimSpace(filter.ServerScopeKey)
	filter.ServiceScopeKey = strings.TrimSpace(filter.ServiceScopeKey)
	if filter.Limit <= 0 {
		filter.Limit = defaultActivityLimit
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if !filter.CursorCreatedAt.IsZero() && strings.TrimSpace(filter.CursorID) == "" {
		return nil, fmt.Errorf("controlplane: activity cursor id required")
	}

	var out []ActivityEvent
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var cursor any
		if !filter.CursorCreatedAt.IsZero() {
			cursor = filter.CursorCreatedAt.UTC()
		}
		query := `
			SELECT id, tenant_id, instance_id, stack_id, actor_subject_id,
				action, category, severity, message, details_json::text,
				runtime_scope_key, server_scope_key, service_scope_key, correlation_id,
				created_at
			FROM activity_log
			WHERE tenant_id = $1
				AND ($2 = '' OR stack_id = $2)
				AND ($3 = '' OR runtime_scope_key = $3)
				AND ($4 = '' OR server_scope_key = $4)
				AND ($5 = '' OR service_scope_key = $5)
				AND ($6::timestamptz IS NULL OR (created_at, id) < ($6::timestamptz, $7))
			ORDER BY created_at DESC, id DESC
			LIMIT $8
		`
		args := []any{tenantID, filter.StackID, filter.RuntimeScopeKey, filter.ServerScopeKey, filter.ServiceScopeKey, cursor, filter.CursorID, filter.Limit}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			event, err := scanActivityEvent(rows)
			if err != nil {
				return err
			}
			out = append(out, *event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) getStack(ctx context.Context, tenantID, query string, args ...any) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Stack
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		stack, err := scanStack(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = stack
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) getJob(ctx context.Context, tenantID, query string, args ...any) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Job
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		job, err := scanJob(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) listJobs(ctx context.Context, tenantID, query string, args ...any) ([]Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Job
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) updateRunningJob(ctx context.Context, tenantID, query string, args ...any) (*Job, error) {
	job, err := s.getJob(ctx, tenantID, query, args...)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return job, err
}

func (s *PostgresStore) getWorker(ctx context.Context, tenantID, query string, args ...any) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Worker
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		worker, err := scanWorker(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = worker
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	// The stack-operation advisory lock relies on seeing commits made by the
	// previous lock holder. Pin the tenant transaction to READ COMMITTED even if
	// a deployment changes the database/session default.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanStack(row rowScanner) (*Stack, error) {
	var stack Stack
	var instanceID, ownerSubjectID, homelabID, description, driftStatus sql.NullString
	var driftCheckedAt, deletedAt sql.NullTime
	var configJSON, servicesJSON, runtimeJSON []byte
	if err := row.Scan(
		&stack.ID,
		&stack.TenantID,
		&instanceID,
		&ownerSubjectID,
		&homelabID,
		&stack.Name,
		&description,
		&stack.Mode,
		&stack.Status,
		&configJSON,
		&servicesJSON,
		&runtimeJSON,
		&driftStatus,
		&driftCheckedAt,
		&stack.CreatedAt,
		&stack.UpdatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}
	stack.InstanceID = instanceID.String
	stack.OwnerSubjectID = ownerSubjectID.String
	stack.HomelabID = homelabID.String
	stack.Description = description.String
	stack.DriftStatus = driftStatus.String
	if driftCheckedAt.Valid {
		stack.DriftCheckedAt = &driftCheckedAt.Time
	}
	if deletedAt.Valid {
		stack.DeletedAt = &deletedAt.Time
	}
	if err := decodeObject(configJSON, &stack.Config); err != nil {
		return nil, err
	}
	if err := decodeArray(servicesJSON, &stack.Services); err != nil {
		return nil, err
	}
	if err := decodeObject(runtimeJSON, &stack.RuntimeSummary); err != nil {
		return nil, err
	}
	return &stack, nil
}

func scanJob(row rowScanner) (*Job, error) {
	var job Job
	var instanceID, stackID, step, message, jobError, errorDetails sql.NullString
	var startedAt, completedAt sql.NullTime
	var logsJSON, resultJSON []byte
	if err := row.Scan(
		&job.ID,
		&job.TenantID,
		&instanceID,
		&stackID,
		&job.Type,
		&job.State,
		&job.Priority,
		&job.Progress,
		&step,
		&message,
		&jobError,
		&errorDetails,
		&logsJSON,
		&resultJSON,
		&job.ScheduledFor,
		&startedAt,
		&completedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	); err != nil {
		return nil, err
	}
	job.InstanceID = instanceID.String
	job.StackID = stackID.String
	job.Step = step.String
	job.Message = message.String
	job.Error = jobError.String
	job.ErrorDetails = errorDetails.String
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	if err := decodeArray(logsJSON, &job.Logs); err != nil {
		return nil, err
	}
	if err := decodeObject(resultJSON, &job.Result); err != nil {
		return nil, err
	}
	return &job, nil
}

func scanWorker(row rowScanner) (*Worker, error) {
	var worker Worker
	var instanceID, stackID, ip, osName, arch, tokenHash, gpu, dockerVersion, workerType, provider, ownerSubjectID sql.NullString
	var approvedAt, lastSeenAt sql.NullTime
	var tagsJSON, capabilitiesJSON, resourcesJSON []byte
	if err := row.Scan(
		&worker.ID,
		&worker.TenantID,
		&instanceID,
		&stackID,
		&worker.Hostname,
		&ip,
		&osName,
		&arch,
		&tokenHash,
		&worker.Status,
		&worker.Approved,
		&approvedAt,
		&lastSeenAt,
		&worker.CPUCores,
		&worker.RAMMB,
		&worker.DiskGB,
		&gpu,
		&worker.HasNVME,
		&worker.HasHWTranscode,
		&dockerVersion,
		&workerType,
		&provider,
		&tagsJSON,
		&ownerSubjectID,
		&capabilitiesJSON,
		&resourcesJSON,
		&worker.CreatedAt,
		&worker.UpdatedAt,
	); err != nil {
		return nil, err
	}
	worker.InstanceID = instanceID.String
	worker.StackID = stackID.String
	worker.IP = ip.String
	worker.OS = osName.String
	worker.Arch = arch.String
	worker.TokenHash = tokenHash.String
	worker.GPU = gpu.String
	worker.DockerVersion = dockerVersion.String
	worker.Type = workerType.String
	worker.Provider = provider.String
	worker.OwnerSubjectID = ownerSubjectID.String
	if approvedAt.Valid {
		worker.ApprovedAt = &approvedAt.Time
	}
	if lastSeenAt.Valid {
		worker.LastSeenAt = &lastSeenAt.Time
	}
	if err := decodeObject(tagsJSON, &worker.Tags); err != nil {
		return nil, err
	}
	if err := decodeObject(capabilitiesJSON, &worker.Capabilities); err != nil {
		return nil, err
	}
	if err := decodeObject(resourcesJSON, &worker.Resources); err != nil {
		return nil, err
	}
	return &worker, nil
}

func scanPairingToken(row rowScanner) (*PairingToken, error) {
	var token PairingToken
	var instanceID, stackID, name sql.NullString
	var expiresAt, usedAt sql.NullTime
	var metadataJSON []byte
	if err := row.Scan(
		&token.ID,
		&token.TenantID,
		&instanceID,
		&stackID,
		&token.OwnerSubjectID,
		&name,
		&token.TokenHash,
		&token.Status,
		&expiresAt,
		&usedAt,
		&metadataJSON,
		&token.CreatedAt,
		&token.UpdatedAt,
	); err != nil {
		return nil, err
	}
	token.InstanceID = instanceID.String
	token.StackID = stackID.String
	token.Name = name.String
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if usedAt.Valid {
		token.UsedAt = &usedAt.Time
	}
	if err := decodeObject(metadataJSON, &token.Metadata); err != nil {
		return nil, err
	}
	return &token, nil
}

func scanNode(row rowScanner) (*Node, error) {
	var node Node
	var instanceID, workerID, address sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&node.ID,
		&node.TenantID,
		&instanceID,
		&node.StackID,
		&workerID,
		&node.Name,
		&node.Role,
		&node.Status,
		&address,
		&metadataJSON,
		&node.CreatedAt,
		&node.UpdatedAt,
	); err != nil {
		return nil, err
	}
	node.InstanceID = instanceID.String
	node.WorkerID = workerID.String
	node.Address = address.String
	if err := decodeObject(metadataJSON, &node.Metadata); err != nil {
		return nil, err
	}
	return &node, nil
}

func scanService(row rowScanner) (*Service, error) {
	var service Service
	var instanceID, nodeID, url, migrationStatus sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&service.ID,
		&service.TenantID,
		&instanceID,
		&service.StackID,
		&nodeID,
		&service.ServiceKey,
		&service.Name,
		&service.Status,
		&service.Source,
		&url,
		&migrationStatus,
		&metadataJSON,
		&service.ManagementState,
		&service.CreatedAt,
		&service.UpdatedAt,
	); err != nil {
		return nil, err
	}
	service.InstanceID = instanceID.String
	service.NodeID = nodeID.String
	service.URL = url.String
	service.MigrationStatus = migrationStatus.String
	if err := decodeObject(metadataJSON, &service.Metadata); err != nil {
		return nil, err
	}
	return &service, nil
}

func scanServiceRuntime(row rowScanner) (*ServiceRuntime, error) {
	var service ServiceRuntime
	var instanceID, serverID, stackKitVersion sql.NullString
	var targetKind, providerID, managedTargetRef, providerReceiptRef sql.NullString
	var slaPolicyRef, backupPolicyRef, placementEvidenceRef sql.NullString
	var observedAt, placementObservedAt sql.NullTime
	var accessJSON, capabilitiesJSON, metadataJSON []byte
	if err := row.Scan(
		&service.ID, &service.TenantID, &instanceID, &service.StackID, &serverID,
		&targetKind, &providerID, &managedTargetRef, &providerReceiptRef, &slaPolicyRef,
		&backupPolicyRef, &placementEvidenceRef, &placementObservedAt,
		&service.ServiceKey, &service.ServiceInstance, &service.Name, &service.DesiredState,
		&service.ObservedState, &service.HealthState, &service.ManagementState,
		&observedAt, &stackKitVersion,
		&accessJSON, &capabilitiesJSON, &service.Source, &metadataJSON,
		&service.CreatedAt, &service.UpdatedAt,
	); err != nil {
		return nil, err
	}
	service.InstanceID = instanceID.String
	service.ServerID = serverID.String
	service.Placement = serviceregistry.Placement{
		TargetKind:         serviceregistry.TargetKind(targetKind.String),
		ProviderID:         providerID.String,
		ManagedTargetRef:   managedTargetRef.String,
		ProviderReceiptRef: providerReceiptRef.String,
		SLAPolicyRef:       slaPolicyRef.String,
		BackupPolicyRef:    backupPolicyRef.String,
		EvidenceRef:        placementEvidenceRef.String,
	}
	if placementObservedAt.Valid {
		service.Placement.ObservedAt = &placementObservedAt.Time
	}
	service.Placement = serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	service.StackKitVersion = stackKitVersion.String
	if observedAt.Valid {
		service.ObservedAt = &observedAt.Time
	}
	if err := decodeObject(accessJSON, &service.Access); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilitiesJSON, &service.Capabilities); err != nil {
		return nil, err
	}
	if err := decodeObject(metadataJSON, &service.Metadata); err != nil {
		return nil, err
	}
	return &service, nil
}

func scanRILServer(row rowScanner) (*RILServer, error) {
	var server RILServer
	var instanceID, stackID, nodeID sql.NullString
	var lastSeenAt sql.NullTime
	var healthJSON, inventoryJSON []byte
	if err := row.Scan(
		&server.ID,
		&server.TenantID,
		&instanceID,
		&stackID,
		&nodeID,
		&server.Name,
		&server.Status,
		&healthJSON,
		&inventoryJSON,
		&lastSeenAt,
		&server.CreatedAt,
		&server.UpdatedAt,
	); err != nil {
		return nil, err
	}
	server.InstanceID = instanceID.String
	server.StackID = stackID.String
	server.NodeID = nodeID.String
	if lastSeenAt.Valid {
		server.LastSeenAt = &lastSeenAt.Time
	}
	if err := decodeObject(healthJSON, &server.Health); err != nil {
		return nil, err
	}
	if err := decodeObject(inventoryJSON, &server.Inventory); err != nil {
		return nil, err
	}
	return &server, nil
}

func scanServerRuntime(row rowScanner) (*ServerRuntime, error) {
	var server ServerRuntime
	var instanceID, stackID, workerID, nodeID, leaseID, providerRef, reasonCode sql.NullString
	var environmentClass, offering, providerID, providerTargetRef sql.NullString
	var availabilityOwner, operationsOwner, runtimeTargetEvidenceRef sql.NullString
	var lifecycleReasonCode, desiredReasonCode, connectionReasonCode, healthReasonCode sql.NullString
	var sourceAuthority, sourceID, sourceEpoch sql.NullString
	var heartbeatAt, sourceObservedAt, decommissionedAt, runtimeTargetObservedAt sql.NullTime
	var channelsJSON, metadataJSON []byte
	if err := row.Scan(
		&server.ID, &server.TenantID, &instanceID, &stackID, &server.OwnerSubjectID, &workerID, &nodeID,
		&leaseID, &providerRef, &environmentClass, &offering, &providerID, &providerTargetRef,
		&availabilityOwner, &operationsOwner, &runtimeTargetEvidenceRef, &runtimeTargetObservedAt,
		&server.Name, &server.LifecycleState,
		&server.DesiredState, &server.ConnectionState, &server.HealthState,
		&reasonCode, &server.ConnectionChangedAt, &heartbeatAt,
		&server.InventoryRevision, &server.Revision, &server.Generation,
		&sourceAuthority, &sourceID, &sourceEpoch, &server.SourceSequence, &sourceObservedAt,
		&channelsJSON, &metadataJSON,
		&decommissionedAt, &lifecycleReasonCode, &desiredReasonCode,
		&connectionReasonCode, &healthReasonCode, &server.LifecycleChangedAt,
		&server.DesiredChangedAt, &server.HealthChangedAt, &server.CreatedAt, &server.UpdatedAt,
	); err != nil {
		return nil, err
	}
	server.InstanceID, server.StackID, server.WorkerID, server.NodeID = instanceID.String, stackID.String, workerID.String, nodeID.String
	server.LeaseID, server.ProviderRef, server.ReasonCode = leaseID.String, providerRef.String, reasonCode.String
	server.RuntimeTarget = serverregistry.RuntimeTarget{
		EnvironmentClass:  serverregistry.EnvironmentClass(environmentClass.String),
		Offering:          serverregistry.Offering(offering.String),
		ProviderID:        providerID.String,
		ProviderTargetRef: providerTargetRef.String,
		AvailabilityOwner: serverregistry.AvailabilityOwner(availabilityOwner.String),
		OperationsOwner:   serverregistry.OperationsOwner(operationsOwner.String),
		EvidenceRef:       runtimeTargetEvidenceRef.String,
	}
	if runtimeTargetObservedAt.Valid {
		server.RuntimeTarget.ObservedAt = &runtimeTargetObservedAt.Time
	}
	server.RuntimeTarget = serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	server.LifecycleReasonCode, server.DesiredReasonCode = lifecycleReasonCode.String, desiredReasonCode.String
	server.ConnectionReasonCode, server.HealthReasonCode = connectionReasonCode.String, healthReasonCode.String
	server.SourceAuthority, server.SourceID, server.SourceEpoch = sourceAuthority.String, sourceID.String, sourceEpoch.String
	if heartbeatAt.Valid {
		server.LastHeartbeatAt = &heartbeatAt.Time
	}
	if sourceObservedAt.Valid {
		server.SourceObservedAt = &sourceObservedAt.Time
	}
	if decommissionedAt.Valid {
		server.DecommissionedAt = &decommissionedAt.Time
	}
	if len(channelsJSON) > 0 {
		if err := json.Unmarshal(channelsJSON, &server.Channels); err != nil {
			return nil, err
		}
	}
	if err := decodeObject(metadataJSON, &server.Metadata); err != nil {
		return nil, err
	}
	return &server, nil
}

func scanServerTransition(row rowScanner) (*ServerStateTransition, error) {
	var transition ServerStateTransition
	var fromState, reasonCode sql.NullString
	var evidenceJSON []byte
	if err := row.Scan(
		&transition.ID, &transition.TenantID, &transition.ServerID,
		&transition.Dimension, &fromState, &transition.ToState, &reasonCode,
		&transition.Source, &transition.ObservedAt, &evidenceJSON, &transition.CreatedAt,
	); err != nil {
		return nil, err
	}
	transition.FromState, transition.ReasonCode = fromState.String, reasonCode.String
	if err := decodeObject(evidenceJSON, &transition.Evidence); err != nil {
		return nil, err
	}
	return &transition, nil
}

func scanServerInventory(row rowScanner) (*ServerInventorySnapshot, error) {
	var snapshot ServerInventorySnapshot
	var inventoryJSON []byte
	if err := row.Scan(
		&snapshot.ID, &snapshot.TenantID, &snapshot.ServerID, &snapshot.Revision,
		&snapshot.Source, &snapshot.ObservedAt, &inventoryJSON, &snapshot.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := decodeObject(inventoryJSON, &snapshot.Inventory); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func scanRILCommand(row rowScanner) (*RILCommand, error) {
	var command RILCommand
	var serverID, actorSubjectID, commandError sql.NullString
	var completedAt sql.NullTime
	var requestJSON, resultJSON []byte
	if err := row.Scan(
		&command.ID,
		&command.TenantID,
		&serverID,
		&actorSubjectID,
		&command.CommandClass,
		&command.Status,
		&requestJSON,
		&resultJSON,
		&commandError,
		&command.CreatedAt,
		&command.UpdatedAt,
		&completedAt,
	); err != nil {
		return nil, err
	}
	command.ServerID = serverID.String
	command.ActorSubjectID = actorSubjectID.String
	command.Error = commandError.String
	if completedAt.Valid {
		command.CompletedAt = &completedAt.Time
	}
	if err := decodeObject(requestJSON, &command.Request); err != nil {
		return nil, err
	}
	if err := decodeObject(resultJSON, &command.Result); err != nil {
		return nil, err
	}
	return &command, nil
}

func scanRILHealEvent(row rowScanner) (*RILHealEvent, error) {
	var event RILHealEvent
	var serverID, actionCardID, cause sql.NullString
	var detailsJSON []byte
	if err := row.Scan(
		&event.ID,
		&event.TenantID,
		&serverID,
		&actionCardID,
		&event.Status,
		&cause,
		&detailsJSON,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, err
	}
	event.ServerID = serverID.String
	event.ActionCardID = actionCardID.String
	event.Cause = cause.String
	if err := decodeObject(detailsJSON, &event.Details); err != nil {
		return nil, err
	}
	return &event, nil
}

func scanRILActionCard(row rowScanner) (*RILActionCard, error) {
	var card RILActionCard
	var serverID, stackID sql.NullString
	var resolvedAt sql.NullTime
	var actionJSON, decisionJSON []byte
	if err := row.Scan(
		&card.ID,
		&card.TenantID,
		&serverID,
		&stackID,
		&card.Title,
		&card.Status,
		&card.Severity,
		&actionJSON,
		&decisionJSON,
		&card.CreatedAt,
		&card.UpdatedAt,
		&resolvedAt,
	); err != nil {
		return nil, err
	}
	card.ServerID = serverID.String
	card.StackID = stackID.String
	if resolvedAt.Valid {
		card.ResolvedAt = &resolvedAt.Time
	}
	if err := decodeObject(actionJSON, &card.Action); err != nil {
		return nil, err
	}
	if err := decodeObject(decisionJSON, &card.Decision); err != nil {
		return nil, err
	}
	return &card, nil
}

func scanWalletItem(row rowScanner) (*WalletItem, error) {
	var item WalletItem
	var instanceID, stackID, provider, externalRef sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&item.ID,
		&item.TenantID,
		&instanceID,
		&stackID,
		&item.ItemType,
		&provider,
		&externalRef,
		&metadataJSON,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.InstanceID = instanceID.String
	item.StackID = stackID.String
	item.Provider = provider.String
	item.ExternalRef = externalRef.String
	if err := decodeObject(metadataJSON, &item.Metadata); err != nil {
		return nil, err
	}
	return &item, nil
}

func scanTenant(row rowScanner) (*Tenant, error) {
	var tenant Tenant
	var externalOrgID sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&tenant.ID,
		&externalOrgID,
		&tenant.DisplayName,
		&tenant.Kind,
		&tenant.Status,
		&metadataJSON,
		&tenant.CreatedAt,
		&tenant.UpdatedAt,
	); err != nil {
		return nil, err
	}
	tenant.ExternalOrgID = externalOrgID.String
	if err := decodeObject(metadataJSON, &tenant.Metadata); err != nil {
		return nil, err
	}
	return &tenant, nil
}

func scanUser(row rowScanner) (*User, error) {
	var user User
	var email, displayName sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&user.ID,
		&email,
		&displayName,
		&user.Status,
		&metadataJSON,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		return nil, err
	}
	user.PrimaryEmail = email.String
	user.DisplayName = displayName.String
	if err := decodeObject(metadataJSON, &user.Metadata); err != nil {
		return nil, err
	}
	return &user, nil
}

func scanMembership(row rowScanner) (*Membership, error) {
	var membership Membership
	var providerKey, subjectID sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&membership.ID,
		&membership.TenantID,
		&membership.UserID,
		&membership.RoleKey,
		&providerKey,
		&subjectID,
		&membership.Status,
		&metadataJSON,
		&membership.CreatedAt,
		&membership.UpdatedAt,
	); err != nil {
		return nil, err
	}
	membership.ProviderKey = providerKey.String
	membership.SubjectID = subjectID.String
	if err := decodeObject(metadataJSON, &membership.Metadata); err != nil {
		return nil, err
	}
	return &membership, nil
}

func scanAuthConfig(row rowScanner) (*AuthConfig, error) {
	var config AuthConfig
	var instanceID sql.NullString
	var configJSON []byte
	if err := row.Scan(
		&config.ID,
		&config.TenantID,
		&instanceID,
		&config.Mode,
		&configJSON,
		&config.CreatedAt,
		&config.UpdatedAt,
	); err != nil {
		return nil, err
	}
	config.InstanceID = instanceID.String
	if err := decodeObject(configJSON, &config.Config); err != nil {
		return nil, err
	}
	return &config, nil
}

func scanBreakglassAdmin(row rowScanner) (*BreakglassAdmin, error) {
	var admin BreakglassAdmin
	var userID sql.NullString
	var lastUsedAt sql.NullTime
	var metadataJSON []byte
	if err := row.Scan(
		&admin.ID,
		&admin.TenantID,
		&userID,
		&admin.Email,
		&admin.PasswordHash,
		&admin.Locked,
		&lastUsedAt,
		&metadataJSON,
		&admin.CreatedAt,
		&admin.UpdatedAt,
	); err != nil {
		return nil, err
	}
	admin.UserID = userID.String
	if lastUsedAt.Valid {
		admin.LastUsedAt = &lastUsedAt.Time
	}
	if err := decodeObject(metadataJSON, &admin.Metadata); err != nil {
		return nil, err
	}
	return &admin, nil
}

func scanActivityEvent(row rowScanner) (*ActivityEvent, error) {
	var event ActivityEvent
	var instanceID, stackID, actorSubjectID, message sql.NullString
	var runtimeScopeKey, serverScopeKey, serviceScopeKey, correlationID sql.NullString
	var detailsJSON []byte
	if err := row.Scan(
		&event.ID,
		&event.TenantID,
		&instanceID,
		&stackID,
		&actorSubjectID,
		&event.Action,
		&event.Category,
		&event.Severity,
		&message,
		&detailsJSON,
		&runtimeScopeKey,
		&serverScopeKey,
		&serviceScopeKey,
		&correlationID,
		&event.CreatedAt,
	); err != nil {
		return nil, err
	}
	event.InstanceID = instanceID.String
	event.StackID = stackID.String
	event.ActorSubjectID = actorSubjectID.String
	event.Message = message.String
	event.RuntimeScopeKey = runtimeScopeKey.String
	event.ServerScopeKey = serverScopeKey.String
	event.ServiceScopeKey = serviceScopeKey.String
	event.CorrelationID = correlationID.String
	if err := decodeObject(detailsJSON, &event.Details); err != nil {
		return nil, err
	}
	return &event, nil
}

func marshalObject(value map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(value)
}

func marshalArray(value []map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte(`[]`), nil
	}
	return json.Marshal(value)
}

func decodeObject(payload []byte, out *map[string]any) error {
	if len(payload) == 0 {
		*out = map[string]any{}
		return nil
	}
	return json.Unmarshal(payload, out)
}

func decodeArray(payload []byte, out *[]map[string]any) error {
	if len(payload) == 0 {
		*out = nil
		return nil
	}
	return json.Unmarshal(payload, out)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ StackStore = (*PostgresStore)(nil)
var _ JobStore = (*PostgresStore)(nil)
var _ WorkerStore = (*PostgresStore)(nil)
var _ WorkerCredentialStore = (*PostgresStore)(nil)
var _ RegistryStore = (*PostgresStore)(nil)
var _ RILStore = (*PostgresStore)(nil)
var _ ServerRuntimeStore = (*PostgresStore)(nil)
var _ WalletStore = (*PostgresStore)(nil)
var _ AuthStore = (*PostgresStore)(nil)
var _ ActivityStore = (*PostgresStore)(nil)
