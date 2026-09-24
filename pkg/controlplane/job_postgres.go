package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

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
	payloadJSON, err := marshalObject(req.Payload)
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
				result_json, payload_json, scheduled_for
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7,
				$8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''),
				NULLIF($12, ''), $13::jsonb, $14::jsonb, $15::jsonb, $16
			)
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.ID, tenantID, req.InstanceID, req.StackID, req.Type, firstNonEmpty(req.State, jobStatePending),
			req.Priority, req.Progress, req.Step, req.Message, req.Error, req.ErrorDetails, logsJSON, resultJSON, payloadJSON, scheduledFor))
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

func (s *PostgresStore) CancelPendingStackOperations(
	ctx context.Context,
	tenantID, stackID string,
	cancelledAt time.Time,
) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return nil, fmt.Errorf("controlplane: tenant and stack id required")
	}
	if cancelledAt.IsZero() {
		cancelledAt = time.Now().UTC()
	}
	cancelledAt = cancelledAt.UTC()

	var cancelled []string
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			UPDATE jobs
			SET state = 'cancelled',
				message = CASE WHEN type = 'destroy'
					THEN 'Superseded by newer stack destroy'
					ELSE 'Superseded by stack destroy' END,
				error = CASE WHEN type = 'destroy'
					THEN 'Job superseded by newer stack destroy'
					ELSE 'Job superseded by stack destroy' END,
				error_details = NULL,
				result_json = result_json || CASE WHEN type = 'destroy'
					THEN jsonb_build_object(
						'destroy_supersession', jsonb_build_object(
							'schema', 'techstack.stack-destroy-supersession/v1',
							'stack_id', $2,
							'superseded_at', to_char($3::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
							'authority', 'new_destroy_admission'
						)
					)
					ELSE jsonb_build_object(
						'destroy_cancellation', jsonb_build_object(
							'schema', 'techstack.stack-rollout-destroy-cancellation/v1',
							'stack_id', $2,
							'cancelled_at', to_char($3::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
						)
					) END,
				completed_at = $3, updated_at = $3,
				execution_owner_id = NULL, execution_lease_expires_at = NULL
			WHERE tenant_id = $1 AND stack_id = $2
				AND type IN ('provision', 'deploy', 'destroy') AND state = 'pending'
			RETURNING id
		`, tenantID, stackID, cancelledAt)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				return scanErr
			}
			cancelled = append(cancelled, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("controlplane: cancel pending stack operations: %w", err)
	}
	sort.Strings(cancelled)
	return cancelled, nil
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
	return s.listJobsWithPayload(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, payload_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND state = 'pending' AND scheduled_for <= now()
		ORDER BY priority DESC, created_at ASC
		LIMIT $2
	`, tenantID, limit)
}

// ListPendingJobTenants pages the secret-free pending-tenant directory. It
// returns tenant IDs only; the job rows themselves are read afterwards inside
// the tenant-scoped RLS boundary.
func (s *PostgresStore) ListPendingJobTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: pending job tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id
		FROM pending_job_tenants
		WHERE tenant_id > $1
		ORDER BY tenant_id ASC
		LIMIT $2
	`, strings.TrimSpace(afterTenantID), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list pending job tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

// CompactPendingJobTenant retires the directory entry once the tenant has no
// pending job left, so a completed tenant stops appearing in the scan.
func (s *PostgresStore) CompactPendingJobTenant(ctx context.Context, tenantID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: pending job tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var remaining int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM jobs WHERE tenant_id = $1 AND state = 'pending'
		`, tenantID).Scan(&remaining); err != nil {
			return fmt.Errorf("controlplane: compact pending job tenant: %w", err)
		}
		if remaining > 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM pending_job_tenants WHERE tenant_id = $1
		`, tenantID)
		return err
	})
}

// SetJobPayload replaces the recovery payload of a pending job.
func (s *PostgresStore) SetJobPayload(ctx context.Context, tenantID, jobID string, payload map[string]any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	jobID = strings.TrimSpace(jobID)
	if tenantID == "" || jobID == "" {
		return fmt.Errorf("controlplane: tenant and job id required")
	}
	if len(payload) == 0 {
		return nil
	}
	payloadJSON, err := marshalObject(payload)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET payload_json = $3::jsonb, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND state = 'pending'
		`, tenantID, jobID, payloadJSON)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("%w: job %s is missing or no longer pending", ErrConflict, jobID)
		}
		return nil
	})
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

func scanJob(row rowScanner) (*Job, error) {
	return scanJobProjection(row, false)
}

// scanJobWithPayload scans the payload-inclusive projection used by the
// pending-job list; every other query keeps the original column set.
func scanJobWithPayload(row rowScanner) (*Job, error) {
	return scanJobProjection(row, true)
}

func scanJobProjection(row rowScanner, includePayload bool) (*Job, error) {
	var job Job
	var instanceID, stackID, step, message, jobError, errorDetails sql.NullString
	var startedAt, completedAt sql.NullTime
	var logsJSON, resultJSON, payloadJSON []byte
	dest := []any{
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
	}
	if includePayload {
		dest = append(dest, &payloadJSON)
	}
	dest = append(dest,
		&job.ScheduledFor,
		&startedAt,
		&completedAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err := row.Scan(dest...); err != nil {
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
	if includePayload {
		if err := decodeObject(payloadJSON, &job.Payload); err != nil {
			return nil, err
		}
	}
	return &job, nil
}

// listJobsWithPayload mirrors listJobs for the payload-inclusive projection.
func (s *PostgresStore) listJobsWithPayload(ctx context.Context, tenantID, query string, args ...any) ([]Job, error) {
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
			job, err := scanJobWithPayload(rows)
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

var _ JobStore = (*PostgresStore)(nil)
