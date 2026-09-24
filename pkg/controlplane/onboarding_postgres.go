package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const onboardingStateColumns = `id, tenant_id, owner_subject_id, product, journey_id,
		journey_version, schema_version, status, revision, state_json::text,
		created_at, updated_at`

func (s *PostgresStore) GetOnboardingState(ctx context.Context, tenantID, ownerSubjectID, product, journeyID string) (*OnboardingState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	product = strings.TrimSpace(product)
	journeyID = strings.TrimSpace(journeyID)
	if tenantID == "" || ownerSubjectID == "" || product == "" || journeyID == "" {
		return nil, fmt.Errorf("controlplane: tenant, owner, product and journey are required")
	}

	var out *OnboardingState
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, err := scanOnboardingState(tx.QueryRowContext(ctx, `
			SELECT `+onboardingStateColumns+`
			FROM user_onboarding_state
			WHERE tenant_id = $1 AND owner_subject_id = $2 AND product = $3 AND journey_id = $4
		`, tenantID, ownerSubjectID, product, journeyID))
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

func (s *PostgresStore) UpsertOnboardingState(ctx context.Context, state OnboardingState, expectRevision *int) (*OnboardingState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if err := validateOnboardingState(state); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(state.TenantID)

	var out *OnboardingState
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		stateJSON, err := marshalObject(state.State)
		if err != nil {
			return err
		}
		if expectRevision != nil {
			// Compare-and-swap on a row that must already exist.
			row, err := scanOnboardingState(tx.QueryRowContext(ctx, `
				UPDATE user_onboarding_state
				SET journey_version = $3, status = $4, revision = $5, state_json = $6::jsonb
				WHERE id = $1 AND tenant_id = $2 AND revision = $7
				RETURNING `+onboardingStateColumns,
				strings.TrimSpace(state.ID), tenantID, state.JourneyVersion, state.Status,
				state.Revision, stateJSON, *expectRevision))
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: onboarding state revision moved", ErrConflict)
			}
			if err != nil {
				return err
			}
			out = row
			return nil
		}
		row, err := scanOnboardingState(tx.QueryRowContext(ctx, `
			INSERT INTO user_onboarding_state (
				id, tenant_id, owner_subject_id, product, journey_id,
				journey_version, schema_version, status, revision, state_json
			) VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $9::jsonb)
			ON CONFLICT (tenant_id, owner_subject_id, product, journey_id)
			DO UPDATE SET
				journey_version = EXCLUDED.journey_version,
				status = EXCLUDED.status,
				revision = EXCLUDED.revision,
				state_json = EXCLUDED.state_json
			RETURNING `+onboardingStateColumns,
			strings.TrimSpace(state.ID), tenantID, strings.TrimSpace(state.OwnerSubjectID),
			strings.TrimSpace(state.Product), strings.TrimSpace(state.JourneyID),
			state.JourneyVersion, state.Status, state.Revision, stateJSON))
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

func scanOnboardingState(row rowScanner) (*OnboardingState, error) {
	var (
		out       OnboardingState
		stateJSON string
	)
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.OwnerSubjectID, &out.Product, &out.JourneyID,
		&out.JourneyVersion, &out.SchemaVersion, &out.Status, &out.Revision, &stateJSON,
		&out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	out.State = map[string]any{}
	if strings.TrimSpace(stateJSON) != "" {
		if err := json.Unmarshal([]byte(stateJSON), &out.State); err != nil {
			return nil, fmt.Errorf("controlplane: decode onboarding state: %w", err)
		}
	}
	return &out, nil
}

var _ OnboardingStateStore = (*PostgresStore)(nil)
