package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const driftResultSelect = `
	SELECT dr.id, dr.tenant_id, COALESCE(dr.instance_id, ''), COALESCE(dr.stack_id, ''),
		COALESCE(dr.job_id, ''), s.owner_subject_id, s.name, dr.status,
		dr.affected_resources::text, dr.plan_summary::text, dr.details_json::text, dr.created_at
	FROM drift_results dr
	JOIN stacks s ON s.id = dr.stack_id AND s.tenant_id = dr.tenant_id
`

func (s *PostgresStore) CreateDriftResult(ctx context.Context, result DriftResult) (*DriftResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	result.TenantID = strings.TrimSpace(result.TenantID)
	result.OwnerSubjectID = strings.TrimSpace(result.OwnerSubjectID)
	result.ID = strings.TrimSpace(result.ID)
	result.StackID = strings.TrimSpace(result.StackID)
	if result.ID == "" || result.TenantID == "" || result.OwnerSubjectID == "" || result.StackID == "" {
		return nil, fmt.Errorf("controlplane: drift result id, tenant, owner, and stack required")
	}
	affected, err := marshalArray(result.AffectedResources)
	if err != nil {
		return nil, err
	}
	plan, err := marshalObject(result.PlanSummary)
	if err != nil {
		return nil, err
	}
	details, err := marshalObject(result.Details)
	if err != nil {
		return nil, err
	}

	var out *DriftResult
	err = s.withTenant(ctx, result.TenantID, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			INSERT INTO drift_results (
				id, tenant_id, instance_id, stack_id, job_id, status,
				affected_resources, plan_summary, details_json
			)
			SELECT $1, $2, COALESCE(NULLIF($3, ''), st.instance_id), st.id, j.id, $6, $7::jsonb, $8::jsonb, $9::jsonb
			FROM stacks st
			LEFT JOIN jobs j ON j.id = NULLIF($5, '') AND j.tenant_id = $2 AND j.stack_id = st.id
			WHERE st.tenant_id = $2 AND st.id = $4 AND st.owner_subject_id = $10 AND st.deleted_at IS NULL
				AND ($5 = '' OR j.id IS NOT NULL)
			RETURNING id
		`, result.ID, result.TenantID, result.InstanceID, result.StackID, result.JobID,
			firstNonEmpty(result.Status, "unknown"), affected, plan, details, result.OwnerSubjectID)
		var id string
		if err := row.Scan(&id); isUniqueViolation(err) {
			return ErrConflict
		} else if err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		saved, err := scanDriftResult(tx.QueryRowContext(ctx, driftResultSelect+`
			WHERE dr.tenant_id = $1 AND dr.id = $2 AND s.owner_subject_id = $3 AND s.deleted_at IS NULL
		`, result.TenantID, id, result.OwnerSubjectID))
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

func (s *PostgresStore) GetDriftResult(ctx context.Context, tenantID, ownerSubjectID, resultID string) (*DriftResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerSubjectID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID)
	var out *DriftResult
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := scanDriftResult(tx.QueryRowContext(ctx, driftResultSelect+`
			WHERE dr.tenant_id = $1 AND dr.id = $2 AND s.owner_subject_id = $3 AND s.deleted_at IS NULL
		`, tenantID, resultID, ownerSubjectID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		out = result
		return err
	})
	return out, err
}

func (s *PostgresStore) ListDriftResults(ctx context.Context, tenantID, ownerSubjectID, stackID string, limit, offset int) ([]DriftResult, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerSubjectID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID), strings.TrimSpace(stackID)
	limit, offset = normalizeDriftResultPage(limit, offset)
	var out []DriftResult
	var total int
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM drift_results dr
			JOIN stacks s ON s.id = dr.stack_id AND s.tenant_id = dr.tenant_id
			WHERE dr.tenant_id = $1 AND s.owner_subject_id = $2 AND s.deleted_at IS NULL
				AND ($3 = '' OR dr.stack_id = $3)
		`, tenantID, ownerSubjectID, stackID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, driftResultSelect+`
			WHERE dr.tenant_id = $1 AND s.owner_subject_id = $2 AND s.deleted_at IS NULL
				AND ($3 = '' OR dr.stack_id = $3)
			ORDER BY dr.created_at DESC, dr.id DESC
			LIMIT $4 OFFSET $5
		`, tenantID, ownerSubjectID, stackID, limit, offset)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			result, err := scanDriftResult(rows)
			if err != nil {
				return err
			}
			out = append(out, *result)
		}
		return rows.Err()
	})
	return out, total, err
}

func (s *PostgresStore) DeleteDriftResult(ctx context.Context, tenantID, ownerSubjectID, resultID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerSubjectID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID)
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM drift_results dr
			USING stacks s
			WHERE dr.tenant_id = $1 AND dr.id = $2 AND s.id = dr.stack_id
				AND s.tenant_id = dr.tenant_id AND s.owner_subject_id = $3 AND s.deleted_at IS NULL
		`, tenantID, resultID, ownerSubjectID)
		if err != nil {
			return err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func scanDriftResult(row rowScanner) (*DriftResult, error) {
	var result DriftResult
	var affected, plan, details []byte
	if err := row.Scan(&result.ID, &result.TenantID, &result.InstanceID, &result.StackID,
		&result.JobID, &result.OwnerSubjectID, &result.StackName, &result.Status, &affected, &plan, &details, &result.CreatedAt); err != nil {
		return nil, err
	}
	if err := decodeArray(affected, &result.AffectedResources); err != nil {
		return nil, err
	}
	if err := decodeObject(plan, &result.PlanSummary); err != nil {
		return nil, err
	}
	if err := decodeObject(details, &result.Details); err != nil {
		return nil, err
	}
	return &result, nil
}

var _ DriftResultStore = (*PostgresStore)(nil)
