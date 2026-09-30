package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	serverMaintenanceMutationFence = "server_maintenance_jobs_one_active_mutation"
	serverMaintenancePlanFence     = "server_maintenance_jobs_one_active_plan"
)

const serverMaintenanceColumns = `tenant_id, id, server_id, agent_id, stack_id, owner_subject_id, action,
	request_digest, state, inventory_revision, plan_digest, reason_code, result_json::text,
	deadline_at, created_at, updated_at, completed_at`

const serverMaintenanceActiveStates = `('queued', 'running', 'waiting', 'awaiting_node_return')`

const serverMaintenanceInsert = `
	INSERT INTO server_maintenance_jobs (
		tenant_id, id, server_id, agent_id, stack_id, owner_subject_id, action,
		request_digest, state, inventory_revision, plan_digest, result_json, deadline_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::timestamptz)
	RETURNING ` + serverMaintenanceColumns

func serverMaintenanceInsertArgs(job ServerMaintenanceJob) ([]any, error) {
	result, err := marshalObject(job.Result)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(job.TenantID), strings.TrimSpace(job.ID), strings.TrimSpace(job.ServerID), strings.TrimSpace(job.AgentID),
		strings.TrimSpace(job.StackID), strings.TrimSpace(job.OwnerSubjectID), job.Action,
		job.RequestDigest, job.State, job.InventoryRevision, job.PlanDigest, string(result), nullableTime(job.DeadlineAt),
	}, nil
}

// serverMaintenanceInsertError maps the fence and primary-key violations.
func serverMaintenanceInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if pgErr.ConstraintName == serverMaintenanceMutationFence || pgErr.ConstraintName == serverMaintenancePlanFence {
			return ErrServerMaintenanceActive
		}
		return ErrConflict
	}
	return fmt.Errorf("controlplane: create server maintenance job: %w", err)
}

func (s *PostgresStore) CreateServerMaintenanceJob(ctx context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error) {
	return s.admitServerMaintenanceJob(ctx, job, false)
}

func (s *PostgresStore) AdmitServerMaintenanceJob(ctx context.Context, job ServerMaintenanceJob) (*ServerMaintenanceJob, error) {
	return s.admitServerMaintenanceJob(ctx, job, true)
}

// admitServerMaintenanceJob inserts on one connection in one transaction.
// With checkNode it first takes the node's transaction-scoped advisory lock
// and refuses a busy node; the lock is released at commit or rollback.
func (s *PostgresStore) admitServerMaintenanceJob(ctx context.Context, job ServerMaintenanceJob, checkNode bool) (*ServerMaintenanceJob, error) {
	if err := validateNewServerMaintenanceJob(job); err != nil {
		return nil, err
	}
	args, err := serverMaintenanceInsertArgs(job)
	if err != nil {
		return nil, err
	}
	var created *ServerMaintenanceJob
	err = s.withTenant(ctx, strings.TrimSpace(job.TenantID), func(tx *sql.Tx) error {
		if checkNode {
			if err := lockNodeAdmissionTx(ctx, tx, job.TenantID, job.AgentID, job.StackID); err != nil {
				return err
			}
			if err := refuseBusyNodeTx(ctx, tx, job.TenantID, job.AgentID, job.StackID); err != nil {
				return err
			}
		}
		row, scanErr := scanServerMaintenanceJob(tx.QueryRowContext(ctx, serverMaintenanceInsert, args...))
		if scanErr != nil {
			return serverMaintenanceInsertError(scanErr)
		}
		created = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *PostgresStore) ClaimServerMaintenanceJob(ctx context.Context, tenantID, jobID, commandID string, deadline, at time.Time) (*ServerMaintenanceJob, error) {
	tenantID, jobID = strings.TrimSpace(tenantID), strings.TrimSpace(jobID)
	var claimed *ServerMaintenanceJob
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var agentID, stackID, state string
		if err := tx.QueryRowContext(ctx, `
			SELECT agent_id, stack_id, state FROM server_maintenance_jobs WHERE tenant_id = $1 AND id = $2
		`, tenantID, jobID).Scan(&agentID, &stackID, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if state != ServerMaintenanceStateQueued {
			return ErrConflict
		}
		if err := lockNodeAdmissionTx(ctx, tx, tenantID, agentID, stackID); err != nil {
			return err
		}
		if err := refuseBusyNodeTx(ctx, tx, tenantID, agentID, stackID); err != nil {
			return err
		}
		row, err := scanServerMaintenanceJob(tx.QueryRowContext(ctx, `
			UPDATE server_maintenance_jobs
			SET state = 'running', reason_code = '', deadline_at = $3::timestamptz, updated_at = $4::timestamptz,
				result_json = result_json || jsonb_build_object('command_id', $5::text)
			WHERE tenant_id = $1 AND id = $2 AND state = 'queued'
			RETURNING `+serverMaintenanceColumns,
			tenantID, jobID, deadline.UTC(), at.UTC(), strings.TrimSpace(commandID)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		claimed = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// lockNodeAdmissionTx takes the node's advisory locks for the rest of tx,
// agent key first, then stack key, and waits at most 10 seconds for them.
func lockNodeAdmissionTx(ctx context.Context, tx *sql.Tx, tenantID, agentID, stackID string) error {
	keys := nodeAdmissionKeys(tenantID, agentID, stackID)
	if len(keys) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', '10s', true)`); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return fmt.Errorf("controlplane: node admission lock: %w", err)
		}
	}
	return nil
}

// refuseBusyNodeTx returns a *NodeBusyError when a pending or running stack
// or service job targets agentID, or names no agent and belongs to stackID.
func refuseBusyNodeTx(ctx context.Context, tx *sql.Tx, tenantID, agentID, stackID string) error {
	var busyID string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE tenant_id = $1 AND state IN ('pending', 'running', 'waiting')
			AND (($2::text <> '' AND payload_json->>'agent_id' = $2::text)
				OR ($3::text <> '' AND stack_id = $3::text AND COALESCE(payload_json->>'agent_id', '') = ''))
		ORDER BY created_at ASC
		LIMIT 1
	`, strings.TrimSpace(tenantID), strings.TrimSpace(agentID), strings.TrimSpace(stackID)).Scan(&busyID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	default:
		return &NodeBusyError{JobID: busyID}
	}
}

func (s *PostgresStore) GetServerMaintenanceJob(ctx context.Context, tenantID, jobID string) (*ServerMaintenanceJob, error) {
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT `+serverMaintenanceColumns+`
		FROM server_maintenance_jobs
		WHERE tenant_id = $1 AND id = $2
	`, scanServerMaintenanceJob, strings.TrimSpace(jobID))
}

func (s *PostgresStore) ActiveServerMaintenanceJob(ctx context.Context, tenantID string, scope ServerMaintenanceScope) (*ServerMaintenanceJob, error) {
	scope = normalizeServerMaintenanceScope(scope)
	if scope == (ServerMaintenanceScope{}) {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT `+serverMaintenanceColumns+`
		FROM server_maintenance_jobs
		WHERE tenant_id = $1 AND state IN `+serverMaintenanceActiveStates+` AND action <> 'os_update_plan'
			AND (deadline_at IS NULL OR deadline_at > now())
			AND (($2::text <> '' AND server_id = $2::text) OR ($3::text <> '' AND agent_id = $3::text) OR ($4::text <> '' AND stack_id = $4::text))
		ORDER BY created_at DESC
		LIMIT 1
	`, scanServerMaintenanceJob, scope.ServerID, scope.AgentID, scope.StackID)
}

func (s *PostgresStore) LatestServerMaintenancePlan(ctx context.Context, tenantID, serverID, planDigest string) (*ServerMaintenanceJob, error) {
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT `+serverMaintenanceColumns+`
		FROM server_maintenance_jobs
		WHERE tenant_id = $1 AND server_id = $2 AND plan_digest = $3
			AND action = 'os_update_plan' AND state = 'completed' AND completed_at IS NOT NULL
		ORDER BY completed_at DESC
		LIMIT 1
	`, scanServerMaintenanceJob, strings.TrimSpace(serverID), strings.TrimSpace(planDigest))
}

func (s *PostgresStore) UpdateServerMaintenanceJob(ctx context.Context, tenantID, jobID string, update ServerMaintenanceUpdate) (*ServerMaintenanceJob, error) {
	if !validServerMaintenanceState(update.State) {
		return nil, errors.New("controlplane: unsupported server maintenance state")
	}
	var result any
	if update.Result != nil {
		encoded, err := marshalObject(update.Result)
		if err != nil {
			return nil, err
		}
		result = string(encoded)
	}
	at := update.At.UTC()
	if update.At.IsZero() {
		at = time.Now().UTC()
	}
	updated, err := queryOneTenantRow(ctx, s, tenantID, `
		UPDATE server_maintenance_jobs
		SET state = $4::text, reason_code = $5::text,
			plan_digest = CASE WHEN $6::text <> '' THEN $6::text ELSE plan_digest END,
			result_json = COALESCE($7::jsonb, result_json),
			deadline_at = $8::timestamptz, updated_at = $9::timestamptz,
			completed_at = CASE WHEN $4::text IN ('completed', 'failed', 'cancelled') THEN $9::timestamptz ELSE NULL END
		WHERE tenant_id = $1 AND id = $2 AND state = $3::text
			AND state NOT IN ('completed', 'failed', 'cancelled')
		RETURNING `+serverMaintenanceColumns,
		scanServerMaintenanceJob,
		strings.TrimSpace(jobID), update.ExpectedState, update.State, update.ReasonCode,
		update.PlanDigest, result, nullableTime(update.DeadlineAt), at,
	)
	if errors.Is(err, ErrNotFound) {
		if _, getErr := s.GetServerMaintenanceJob(ctx, tenantID, jobID); getErr == nil {
			return nil, ErrConflict
		}
	}
	return updated, err
}

// refuseNodeUnderMaintenanceTx returns ErrNodeUnderMaintenance while a
// claimed reboot or OS update holds agentID, or stackID for a job without an
// agent. Plans and queued maintenance never block a stack job.
func refuseNodeUnderMaintenanceTx(ctx context.Context, tx *sql.Tx, tenantID, agentID, stackID string) error {
	agentID, stackID = strings.TrimSpace(agentID), strings.TrimSpace(stackID)
	if agentID == "" && stackID == "" {
		return nil
	}
	var held bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM server_maintenance_jobs
			WHERE tenant_id = $1 AND action <> 'os_update_plan'
				AND state IN ('running', 'waiting', 'awaiting_node_return')
				AND (deadline_at IS NULL OR deadline_at > now())
				AND (($2::text <> '' AND agent_id = $2::text) OR ($2::text = '' AND $3::text <> '' AND stack_id = $3::text))
		)
	`, strings.TrimSpace(tenantID), agentID, stackID).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrNodeUnderMaintenance
	}
	return nil
}

func (s *PostgresStore) ListServerMaintenanceTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: server maintenance tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id FROM server_maintenance_tenants
		WHERE tenant_id > $1
		ORDER BY tenant_id ASC
		LIMIT $2
	`, strings.TrimSpace(afterTenantID), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list server maintenance tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

func (s *PostgresStore) ListActiveServerMaintenanceJobs(ctx context.Context, tenantID string, limit int) ([]ServerMaintenanceJob, error) {
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("controlplane: active server maintenance job limit from 1 to 500 is required")
	}
	var jobs []ServerMaintenanceJob
	err := s.withTenant(ctx, strings.TrimSpace(tenantID), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT `+serverMaintenanceColumns+`
			FROM server_maintenance_jobs
			WHERE tenant_id = $1 AND state IN `+serverMaintenanceActiveStates+`
			ORDER BY created_at ASC
			LIMIT $2
		`, strings.TrimSpace(tenantID), limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			job, scanErr := scanServerMaintenanceJob(rows)
			if scanErr != nil {
				return scanErr
			}
			jobs = append(jobs, *job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("controlplane: list active server maintenance jobs: %w", err)
	}
	return jobs, nil
}

// CompactServerMaintenanceTenant removes the entry only when it was not
// refreshed in the last minute, so a job inserted concurrently (whose trigger
// refreshed the entry) is never orphaned from the directory.
func (s *PostgresStore) CompactServerMaintenanceTenant(ctx context.Context, tenantID string) error {
	tenantID = strings.TrimSpace(tenantID)
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM server_maintenance_tenants
			WHERE tenant_id = $1 AND refreshed_at < clock_timestamp() - interval '1 minute'
				AND NOT EXISTS (
					SELECT 1 FROM server_maintenance_jobs
					WHERE tenant_id = $1 AND state IN `+serverMaintenanceActiveStates+`
				)
		`, tenantID)
		return err
	})
}

func scanServerMaintenanceJob(row rowScanner) (*ServerMaintenanceJob, error) {
	var job ServerMaintenanceJob
	var resultJSON []byte
	var deadlineAt, completedAt sql.NullTime
	if err := row.Scan(
		&job.TenantID, &job.ID, &job.ServerID, &job.AgentID, &job.StackID, &job.OwnerSubjectID, &job.Action,
		&job.RequestDigest, &job.State, &job.InventoryRevision, &job.PlanDigest, &job.ReasonCode, &resultJSON,
		&deadlineAt, &job.CreatedAt, &job.UpdatedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	if deadlineAt.Valid {
		deadline := deadlineAt.Time.UTC()
		job.DeadlineAt = &deadline
	}
	if completedAt.Valid {
		completed := completedAt.Time.UTC()
		job.CompletedAt = &completed
	}
	if err := decodeObject(resultJSON, &job.Result); err != nil {
		return nil, err
	}
	return &job, nil
}
