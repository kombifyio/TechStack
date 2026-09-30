package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const operatorSelfDisclosureColumns = `id, tenant_id, owner_subject_id, source,
		disclosure_json::text, created_at, updated_at`

func (s *PostgresStore) GetOperatorSelfDisclosure(ctx context.Context, tenantID, ownerSubjectID string) (*OperatorSelfDisclosure, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if tenantID == "" || ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: tenant and owner are required")
	}

	var out *OperatorSelfDisclosure
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, err := scanOperatorSelfDisclosure(tx.QueryRowContext(ctx, `
			SELECT `+operatorSelfDisclosureColumns+`
			FROM operator_self_disclosures
			WHERE tenant_id = $1 AND owner_subject_id = $2
		`, tenantID, ownerSubjectID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
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

func (s *PostgresStore) UpsertOperatorSelfDisclosure(ctx context.Context, disclosure OperatorSelfDisclosure) (*OperatorSelfDisclosure, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	normalized, err := normalizeOperatorSelfDisclosure(disclosure)
	if err != nil {
		return nil, err
	}
	answersJSON, err := json.Marshal(normalized.Disclosure)
	if err != nil {
		return nil, fmt.Errorf("controlplane: encode self-disclosure: %w", err)
	}
	tenantID := normalized.TenantID

	var out *OperatorSelfDisclosure
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, err := scanOperatorSelfDisclosure(tx.QueryRowContext(ctx, `
			INSERT INTO operator_self_disclosures (
				id, tenant_id, owner_subject_id, schema_version, source, disclosure_json
			) VALUES ($1, $2, $3, 1, $4, $5::jsonb)
			ON CONFLICT (tenant_id, owner_subject_id)
			DO UPDATE SET
				source = EXCLUDED.source,
				disclosure_json = EXCLUDED.disclosure_json
			RETURNING `+operatorSelfDisclosureColumns,
			normalized.ID, tenantID, normalized.OwnerSubjectID,
			normalized.Source, string(answersJSON)))
		if err != nil {
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

func scanOperatorSelfDisclosure(row rowScanner) (*OperatorSelfDisclosure, error) {
	var (
		out         OperatorSelfDisclosure
		answersJSON string
	)
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.OwnerSubjectID, &out.Source,
		&answersJSON, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if strings.TrimSpace(answersJSON) != "" {
		if err := json.Unmarshal([]byte(answersJSON), &out.Disclosure); err != nil {
			return nil, fmt.Errorf("controlplane: decode self-disclosure: %w", err)
		}
	}
	return &out, nil
}

var _ OperatorSelfDisclosureStore = (*PostgresStore)(nil)
