package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

var _ JobExecutionReclaimStore = (*PostgresStore)(nil)

// ListJobExecutionReclaimTenants pages the secret-free wake-up directory. It
// returns tenant IDs only; the job rows themselves are read afterwards inside
// the tenant-scoped RLS boundary.
func (s *PostgresStore) ListJobExecutionReclaimTenants(
	ctx context.Context,
	afterTenantID string,
	limit int,
	leaseCutoff time.Time,
) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: job execution reclaim tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id
		FROM job_execution_reclaim_tenants
		WHERE tenant_id > $1 AND earliest_lease_expires_at <= $2
		ORDER BY tenant_id ASC
		LIMIT $3
	`, strings.TrimSpace(afterTenantID), leaseCutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list job execution reclaim tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

// CompactJobExecutionReclaimTenant recomputes the tenant's wake-up entry from
// its own leased executions and retires the entry once none remain, so a tenant
// with no running work stops appearing in the due directory.
func (s *PostgresStore) CompactJobExecutionReclaimTenant(ctx context.Context, tenantID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: job execution reclaim tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var earliest sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			SELECT MIN(execution_lease_expires_at)
			FROM jobs
			WHERE tenant_id = $1 AND state = 'running'
			  AND execution_lease_expires_at IS NOT NULL
		`, tenantID).Scan(&earliest); err != nil {
			return fmt.Errorf("controlplane: compact job execution reclaim tenant: %w", err)
		}
		if !earliest.Valid {
			_, err := tx.ExecContext(ctx, `
				DELETE FROM job_execution_reclaim_tenants WHERE tenant_id = $1
			`, tenantID)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE job_execution_reclaim_tenants
			SET earliest_lease_expires_at = $2, refreshed_at = clock_timestamp()
			WHERE tenant_id = $1
		`, tenantID, earliest.Time.UTC())
		return err
	})
}

// ListExpiredJobExecutionLeases returns the tenant's running jobs whose
// execution lease has lapsed, oldest lease first. Discovery only: the store
// compare-and-set in ReclaimExpiredJobExecution is the authority.
func (s *PostgresStore) ListExpiredJobExecutionLeases(
	ctx context.Context,
	tenantID string,
	expiredBefore time.Time,
	limit int,
) ([]JobExecutionLease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	if limit <= 0 {
		limit = 50
	}
	var out []JobExecutionLease
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT id, COALESCE(stack_id, ''), type, COALESCE(step, ''),
				COALESCE(execution_owner_id, ''), started_at, updated_at,
				execution_lease_expires_at, result_json::text
			FROM jobs
			WHERE tenant_id = $1 AND state = 'running'
			  AND execution_lease_expires_at IS NOT NULL
			  AND execution_lease_expires_at <= $2
			ORDER BY execution_lease_expires_at ASC, id ASC
			LIMIT $3
		`, tenantID, expiredBefore.UTC(), limit)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			lease := JobExecutionLease{TenantID: tenantID}
			var startedAt sql.NullTime
			var resultJSON []byte
			if scanErr := rows.Scan(
				&lease.JobID, &lease.StackID, &lease.Type, &lease.Step,
				&lease.OwnerID, &startedAt, &lease.UpdatedAt,
				&lease.LeaseExpiresAt, &resultJSON,
			); scanErr != nil {
				return scanErr
			}
			if startedAt.Valid {
				lease.StartedAt = &startedAt.Time
			}
			if decodeErr := decodeObject(resultJSON, &lease.Result); decodeErr != nil {
				return decodeErr
			}
			out = append(out, lease)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("controlplane: list expired job execution leases: %w", err)
	}
	return out, nil
}

// ReclaimExpiredJobExecution terminalizes exactly one running job whose lease
// has expired and releases the per-stack execution claim it was holding.
//
// It never completes a job and never re-dispatches one: the only transition it
// can make is running -> failed, carrying an operator-readable reason. Every
// observed fact from the scan (owner, lease deadline, stack, state) is part of
// the same conditional UPDATE, so an execution that renewed its lease in the
// meantime is refused with ErrConflict rather than stolen.
func (s *PostgresStore) ReclaimExpiredJobExecution(
	ctx context.Context,
	req ReclaimExpiredJobExecutionRequest,
) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	req = normalizeReclaimExpiredJobExecutionRequest(req)
	if !validReclaimExpiredJobExecutionRequest(req) {
		return nil, fmt.Errorf("controlplane: exact expired job execution identity required")
	}
	patchJSON, err := marshalObject(req.ResultPatch)
	if err != nil {
		return nil, err
	}
	reclaimedAt := req.ReclaimedAt.UTC()
	if reclaimedAt.IsZero() {
		reclaimedAt = time.Now().UTC()
	}

	var out *Job
	err = s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'failed', error = $4, error_details = NULLIF($5, ''),
				result_json = result_json || $6::jsonb,
				completed_at = $7, updated_at = $7,
				execution_owner_id = NULL, execution_lease_expires_at = NULL
			WHERE tenant_id = $1 AND id = $2 AND stack_id = $3
				AND state = 'running'
				AND execution_lease_expires_at IS NOT NULL
				AND execution_lease_expires_at <= $8
				AND COALESCE(execution_owner_id, '') = $9
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.TenantID, req.JobID, req.StackID, req.Error, req.ErrorDetails,
			patchJSON, reclaimedAt, req.LeaseExpiredBefore.UTC(), req.ExpectedOwnerID))
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

func (s *PostgresStore) ResumeExpiredJobExecution(
	ctx context.Context,
	req ResumeExpiredJobExecutionRequest,
) (*Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	req = normalizeResumeExpiredJobExecutionRequest(req)
	if !validResumeExpiredJobExecutionRequest(req) {
		return nil, fmt.Errorf("controlplane: exact resumable job execution identity required")
	}
	resumedAt := req.ResumedAt.UTC()
	if resumedAt.IsZero() {
		resumedAt = time.Now().UTC()
	}
	var out *Job
	err := s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		job, scanErr := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET state = 'pending', scheduled_for = $4, updated_at = $4,
				execution_owner_id = NULL, execution_lease_expires_at = NULL,
				completed_at = NULL, error = NULL, error_details = NULL
			WHERE tenant_id = $1 AND id = $2 AND stack_id = $3
				AND state = 'running'
				AND execution_lease_expires_at IS NOT NULL
				AND execution_lease_expires_at <= $5
				AND COALESCE(execution_owner_id, '') = $6
			RETURNING id, tenant_id, instance_id, stack_id, type, state, priority,
				progress, step, message, error, error_details, logs_json::text,
				result_json::text, scheduled_for, started_at, completed_at,
				created_at, updated_at
		`, req.TenantID, req.JobID, req.StackID, resumedAt,
			req.LeaseExpiredBefore.UTC(), req.ExpectedOwnerID))
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
