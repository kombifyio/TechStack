package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const householdRefColumns = `tenant_id, stack_id, idp, username, role, owner_subject_id, cloud_subject,
	email, display_name, status, invited_at, linked_at, updated_at`

func scanHouseholdRef(row rowScanner) (*HomelabIdentityRef, error) {
	var ref HomelabIdentityRef
	var linkedAt sql.NullTime
	if err := row.Scan(&ref.TenantID, &ref.StackID, &ref.IdP, &ref.Username, &ref.Role, &ref.OwnerSubjectID,
		&ref.CloudSubject, &ref.Email, &ref.DisplayName, &ref.Status, &ref.InvitedAt, &linkedAt, &ref.UpdatedAt); err != nil {
		return nil, err
	}
	if linkedAt.Valid {
		at := linkedAt.Time.UTC()
		ref.LinkedAt = &at
	}
	ref.InvitedAt, ref.UpdatedAt = ref.InvitedAt.UTC(), ref.UpdatedAt.UTC()
	return &ref, nil
}

func (s *PostgresStore) UpsertHouseholdInvite(ctx context.Context, ref HomelabIdentityRef) (*HomelabIdentityRef, error) {
	ref, err := normalizeHouseholdRef(ref)
	if err != nil {
		return nil, err
	}
	// The username is unique per stack and the cloud subject is unique per
	// stack: a conflict on the username with a different person is refused, a
	// conflict on the cloud subject refreshes the mutable fields and keeps a
	// linked state.
	var holder string
	err = s.withTenant(ctx, ref.TenantID, func(tx *sql.Tx) error {
		scanErr := tx.QueryRowContext(ctx, `
			SELECT cloud_subject FROM household_identity_refs
			WHERE tenant_id = $1 AND stack_id = $2 AND username = $3`,
			ref.TenantID, ref.StackID, ref.Username).Scan(&holder)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil
		}
		return scanErr
	})
	if err != nil {
		return nil, err
	}
	if holder != "" && holder != ref.CloudSubject {
		return nil, ErrHouseholdUsernameTaken
	}
	return queryOneTenantRow(ctx, s, ref.TenantID, `
		INSERT INTO household_identity_refs (
			tenant_id, stack_id, idp, username, role, owner_subject_id, cloud_subject, email, display_name, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'invited')
		ON CONFLICT (tenant_id, stack_id, cloud_subject) DO UPDATE SET
			username = EXCLUDED.username, email = EXCLUDED.email,
			display_name = EXCLUDED.display_name, owner_subject_id = EXCLUDED.owner_subject_id
		RETURNING `+householdRefColumns,
		scanHouseholdRef, ref.StackID, ref.IdP, ref.Username, ref.Role, ref.OwnerSubjectID, ref.CloudSubject, ref.Email, ref.DisplayName)
}

func (s *PostgresStore) MarkHouseholdLinked(ctx context.Context, tenantID, stackID, cloudSubject string, at time.Time) (*HomelabIdentityRef, error) {
	if at.IsZero() {
		at = time.Now()
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		UPDATE household_identity_refs
		SET status = 'linked', linked_at = COALESCE(linked_at, $4)
		WHERE tenant_id = $1 AND stack_id = $2 AND cloud_subject = $3
		RETURNING `+householdRefColumns,
		scanHouseholdRef, strings.TrimSpace(stackID), strings.TrimSpace(cloudSubject), at.UTC())
}

func (s *PostgresStore) DeleteHouseholdRef(ctx context.Context, tenantID, stackID, cloudSubject string) (*HomelabIdentityRef, error) {
	return queryOneTenantRow(ctx, s, tenantID, `
		DELETE FROM household_identity_refs
		WHERE tenant_id = $1 AND stack_id = $2 AND cloud_subject = $3
		RETURNING `+householdRefColumns,
		scanHouseholdRef, strings.TrimSpace(stackID), strings.TrimSpace(cloudSubject))
}

func (s *PostgresStore) ListHouseholdRefsByOwner(ctx context.Context, tenantID, ownerSubjectID string) ([]HomelabIdentityRef, error) {
	return queryTenantRows(ctx, s, tenantID, `
		SELECT `+householdRefColumns+`
		FROM household_identity_refs
		WHERE tenant_id = $1 AND owner_subject_id = $2
		ORDER BY stack_id, username`,
		scanHouseholdRef, strings.TrimSpace(ownerSubjectID))
}

var (
	_ HouseholdLinkStore = (*PostgresStore)(nil)
	_ HouseholdLinkStore = (*MemoryHouseholdLinkStore)(nil)
)
