package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

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
				id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
				mode, status, config_json, services_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, ''),
				$9, $10, $11::jsonb, $12::jsonb
			)
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`,
			req.ID,
			tenantID,
			req.InstanceID,
			req.OwnerSubjectID,
			req.HomelabID,
			req.StackKitInstanceID,
			req.Name,
			req.Description,
			firstNonEmpty(req.Mode, "easy"),
			firstNonEmpty(req.Status, "draft"),
			configJSON,
			servicesJSON,
		))
		if isUniqueViolationOn(err, "idx_stacks_homelab_stackkit_instance_active") {
			return ErrStackKitInstanceConflict
		}
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
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
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
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
			mode, status, config_json::text, services_json::text,
			runtime_summary_json::text, drift_status, drift_checked_at,
			created_at, updated_at, deleted_at
		FROM stacks
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, stackID)
}

func (s *PostgresStore) GetActiveStackByName(ctx context.Context, tenantID, ownerSubjectID, name string) (*Stack, error) {
	return s.getStack(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
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
			SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
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
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
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

func (s *PostgresStore) CompareAndSwapStackConfig(ctx context.Context, command StackConfigCAS) (*Stack, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeStackConfigCAS(command)
	if err != nil {
		return nil, err
	}

	var out *Stack
	err = s.withTenant(ctx, prepared.TenantID, func(tx *sql.Tx) error {
		configJSON, marshalErr := marshalObject(prepared.Config)
		if marshalErr != nil {
			return marshalErr
		}
		stack, queryErr := scanStack(tx.QueryRowContext(ctx, `
			UPDATE stacks
			SET config_json = $3::jsonb,
				updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
				AND updated_at = $4
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
				mode, status, config_json::text, services_json::text,
				runtime_summary_json::text, drift_status, drift_checked_at,
				created_at, updated_at, deleted_at
		`, prepared.TenantID, prepared.StackID, configJSON, prepared.ExpectedUpdatedAt))
		if queryErr == sql.ErrNoRows {
			return ErrConflict
		}
		if queryErr != nil {
			return queryErr
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
			RETURNING id, tenant_id, instance_id, owner_subject_id, homelab_id, stackkit_instance_id, name, description,
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
	if isUniqueViolationOn(err, "idx_stacks_homelab_stackkit_instance_active") {
		return nil, ErrStackKitInstanceConflict
	}
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

func scanStack(row rowScanner) (*Stack, error) {
	var stack Stack
	var instanceID, ownerSubjectID, homelabID, stackKitInstanceID, description, driftStatus sql.NullString
	var driftCheckedAt, deletedAt sql.NullTime
	var configJSON, servicesJSON, runtimeJSON []byte
	if err := row.Scan(
		&stack.ID,
		&stack.TenantID,
		&instanceID,
		&ownerSubjectID,
		&homelabID,
		&stackKitInstanceID,
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
	stack.StackKitInstanceID = stackKitInstanceID.String
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

var _ StackStore = (*PostgresStore)(nil)

func isUniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
