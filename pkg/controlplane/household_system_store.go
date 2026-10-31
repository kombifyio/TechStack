package controlplane

import (
	"context"
	"database/sql"
	"errors"

	"github.com/kombifyio/techstack/internal/systemdb"
)

const HouseholdSystemRole = "techstack_household_worker"

// This constructor checks a separate login. It never upgrades an application
// connection and does not put a fabricated owner token into a context.
type HouseholdSystemStore struct{ *PostgresStore }

// NewHouseholdSystemStore is for the household worker only: its role check
// admits the household role beside unreachable widening policies
// (systemdb.AssertSystemRoleBesideWidening), after that worker verified its
// exact privileges.
func NewHouseholdSystemStore(ctx context.Context, database *sql.DB) (*HouseholdSystemStore, error) {
	if database == nil {
		return nil, ErrHouseholdProjectionUnavailable
	}
	if err := systemdb.AssertSystemRoleBesideWidening(ctx, database, HouseholdSystemRole); err != nil {
		return nil, err
	}
	return &HouseholdSystemStore{PostgresStore: NewPostgresStore(database)}, nil
}

func (s *HouseholdSystemStore) ResolveProjectionOwnerTenant(ctx context.Context, owner, member string) (string, error) {
	// A retained pair remains authoritative for removal after inventory or
	// membership changes. Never resolve a retry onto a different organization.
	var retained string
	err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM household_projection_bindings WHERE owner_subject_id=$1 AND cloud_subject=$2`, owner, member).Scan(&retained)
	if err == nil {
		return retained, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return s.ResolveOwnerTenant(ctx, "", owner)
}

func (s *HouseholdSystemStore) ResolveOwnerTenant(ctx context.Context, hint, owner string) (string, error) {
	if owner == "" {
		return "", ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT m.tenant_id FROM techstack_memberships m JOIN techstack_tenants t ON t.id=m.tenant_id
  WHERE m.subject_id=$1 AND m.user_id=$1 AND m.status='active' AND t.status='active' ORDER BY m.tenant_id LIMIT 2`, owner)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return "", err
		}
		matches = append(matches, tenant)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(matches) != 1 || (hint != "" && hint != matches[0]) {
		return "", ErrHouseholdProjectionUnavailable
	}
	return matches[0], nil
}

func verifyHouseholdSystemScope(ctx context.Context, tx *sql.Tx, intent HouseholdProjectionIntent) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT current_user=session_user AND session_user=$1
  AND EXISTS(SELECT 1 FROM kombify_ctx.role_class WHERE role_name=session_user AND class='system')
  AND (EXISTS(SELECT 1 FROM household_projection_bindings WHERE tenant_id=$2 AND owner_subject_id=$3 AND cloud_subject=$4)
   OR EXISTS(SELECT 1 FROM techstack_memberships WHERE tenant_id=$2 AND user_id=$3 AND subject_id=$3 AND status='active'))`,
		HouseholdSystemRole, intent.TenantID, intent.OwnerSubjectID, intent.CloudSubject).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrHouseholdProjectionUnavailable
	}
	return nil
}
