package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const wizardRunColumns = `id, tenant_id, owner_subject_id, idempotency_key, request_sha256,
		run_kind, requested_run_kind, homelab_id, stack_id, node_id, job_id,
		pairing_job_id, status, intent_json::text, result_json::text, error_reason,
		created_at, updated_at, dismissed_at`

func (s *PostgresStore) GetWizardRunByKey(ctx context.Context, tenantID, ownerSubjectID, idempotencyKey string) (*WizardRun, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: owner subject id required")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("controlplane: idempotency key required")
	}

	var out *WizardRun
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		run, err := scanWizardRun(tx.QueryRowContext(ctx, `
			SELECT `+wizardRunColumns+`
			FROM wizard_runs
			WHERE tenant_id = $1 AND owner_subject_id = $2 AND idempotency_key = $3
		`, tenantID, ownerSubjectID, idempotencyKey))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetLatestWizardRunByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*WizardRun, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: owner subject id required")
	}

	var out *WizardRun
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		run, err := scanWizardRun(tx.QueryRowContext(ctx, `
			SELECT `+wizardRunColumns+`
			FROM wizard_runs
			WHERE tenant_id = $1 AND owner_subject_id = $2
			ORDER BY updated_at DESC
			LIMIT 1
		`, tenantID, ownerSubjectID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertWizardRun(ctx context.Context, run WizardRun) (*WizardRun, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if err := validateWizardRun(run); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(run.TenantID)

	var out *WizardRun
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		intentJSON, err := marshalObject(run.Intent)
		if err != nil {
			return err
		}
		resultJSON, err := marshalObject(run.Result)
		if err != nil {
			return err
		}
		args := []any{
			strings.TrimSpace(run.ID),
			tenantID,
			strings.TrimSpace(run.OwnerSubjectID),
			strings.TrimSpace(run.IdempotencyKey),
			strings.TrimSpace(run.RequestSHA256),
			strings.TrimSpace(run.RunKind),
			strings.TrimSpace(run.RequestedRunKind),
			strings.TrimSpace(run.HomelabID),
			strings.TrimSpace(run.StackID),
			strings.TrimSpace(run.NodeID),
			strings.TrimSpace(run.JobID),
			strings.TrimSpace(run.PairingJobID),
			strings.TrimSpace(run.Status),
			intentJSON,
			resultJSON,
			strings.TrimSpace(run.ErrorReason),
		}
		// A keyed run upserts on the (tenant, owner, key) partial-unique index
		// so a retried run replaces its earlier failed row; keyless runs are
		// audit-only inserts and never conflict on anything but the id.
		query := `
			INSERT INTO wizard_runs (
				id, tenant_id, owner_subject_id, idempotency_key, request_sha256,
				run_kind, requested_run_kind, homelab_id, stack_id, node_id, job_id,
				pairing_job_id, status, intent_json, result_json, error_reason
			) VALUES (
				$1, $2, $3, NULLIF($4, ''), $5,
				$6, $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''),
				NULLIF($12, ''), $13, $14::jsonb, $15::jsonb, NULLIF($16, '')
			)`
		if strings.TrimSpace(run.IdempotencyKey) != "" {
			query += `
			ON CONFLICT (tenant_id, owner_subject_id, idempotency_key)
				WHERE idempotency_key IS NOT NULL
			DO UPDATE SET
				request_sha256 = EXCLUDED.request_sha256,
				run_kind = EXCLUDED.run_kind,
				requested_run_kind = EXCLUDED.requested_run_kind,
				homelab_id = EXCLUDED.homelab_id,
				stack_id = EXCLUDED.stack_id,
				node_id = EXCLUDED.node_id,
				job_id = EXCLUDED.job_id,
				pairing_job_id = EXCLUDED.pairing_job_id,
				status = EXCLUDED.status,
				intent_json = EXCLUDED.intent_json,
				result_json = EXCLUDED.result_json,
				error_reason = EXCLUDED.error_reason,
				dismissed_at = NULL`
		}
		query += `
			RETURNING ` + wizardRunColumns
		stored, err := scanWizardRun(tx.QueryRowContext(ctx, query, args...))
		if isUniqueViolation(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) DismissWizardRun(ctx context.Context, tenantID, ownerSubjectID, runID string, at time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerSubjectID, runID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID), strings.TrimSpace(runID)
	if tenantID == "" || ownerSubjectID == "" || runID == "" {
		return fmt.Errorf("controlplane: tenant, owner and run id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// updated_at stays: dismissing is not run activity.
		res, err := tx.ExecContext(ctx, `
			UPDATE wizard_runs SET dismissed_at = $4
			WHERE tenant_id = $1 AND owner_subject_id = $2 AND id = $3
		`, tenantID, ownerSubjectID, runID, at.UTC())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func validateWizardRun(run WizardRun) error {
	if strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("controlplane: wizard run id required")
	}
	if strings.TrimSpace(run.TenantID) == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	if strings.TrimSpace(run.OwnerSubjectID) == "" {
		return fmt.Errorf("controlplane: owner subject id required")
	}
	if strings.TrimSpace(run.RequestSHA256) == "" {
		return fmt.Errorf("controlplane: wizard run request hash required")
	}
	if strings.TrimSpace(run.RunKind) == "" || strings.TrimSpace(run.RequestedRunKind) == "" {
		return fmt.Errorf("controlplane: wizard run kind required")
	}
	if strings.TrimSpace(run.Status) == "" {
		return fmt.Errorf("controlplane: wizard run status required")
	}
	return nil
}

func scanWizardRun(row rowScanner) (*WizardRun, error) {
	var (
		run                                        WizardRun
		idempotencyKey, homelabID, stackID, nodeID sql.NullString
		jobID, pairingJobID, errorReason           sql.NullString
		intentRaw, resultRaw                       []byte
		dismissedAt                                sql.NullTime
	)
	if err := row.Scan(
		&run.ID,
		&run.TenantID,
		&run.OwnerSubjectID,
		&idempotencyKey,
		&run.RequestSHA256,
		&run.RunKind,
		&run.RequestedRunKind,
		&homelabID,
		&stackID,
		&nodeID,
		&jobID,
		&pairingJobID,
		&run.Status,
		&intentRaw,
		&resultRaw,
		&errorReason,
		&run.CreatedAt,
		&run.UpdatedAt,
		&dismissedAt,
	); err != nil {
		return nil, err
	}
	run.IdempotencyKey = idempotencyKey.String
	run.HomelabID = homelabID.String
	run.StackID = stackID.String
	run.NodeID = nodeID.String
	run.JobID = jobID.String
	run.PairingJobID = pairingJobID.String
	run.ErrorReason = errorReason.String
	if dismissedAt.Valid {
		at := dismissedAt.Time
		run.DismissedAt = &at
	}
	if err := decodeObject(intentRaw, &run.Intent); err != nil {
		return nil, err
	}
	if err := decodeObject(resultRaw, &run.Result); err != nil {
		return nil, err
	}
	return &run, nil
}

var _ WizardRunStore = (*PostgresStore)(nil)
