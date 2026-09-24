package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *PostgresStore) UpsertTenant(ctx context.Context, tenant Tenant) (*Tenant, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(tenant.ID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Tenant
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(tenant.Metadata)
		if err != nil {
			return err
		}
		created, err := scanTenant(tx.QueryRowContext(ctx, `
			INSERT INTO techstack_tenants (
				id, external_org_id, display_name, kind, status, metadata_json
			) VALUES (
				$1, NULLIF($2, ''), $3, $4, $5, $6::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				external_org_id = EXCLUDED.external_org_id,
				display_name = EXCLUDED.display_name,
				kind = EXCLUDED.kind,
				status = EXCLUDED.status,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, external_org_id, display_name, kind, status,
				metadata_json::text, created_at, updated_at
		`,
			tenantID,
			tenant.ExternalOrgID,
			firstNonEmpty(tenant.DisplayName, tenantID),
			firstNonEmpty(tenant.Kind, "self_hosted"),
			firstNonEmpty(tenant.Status, "active"),
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureTenant creates a tenant when it is missing and otherwise returns the
// existing row without changing its identity, kind, status, or metadata. It is
// intended for startup paths that need the tenant foreign-key anchor before
// the first tenant-scoped runtime record is written.
func (s *PostgresStore) EnsureTenant(ctx context.Context, tenant Tenant) (*Tenant, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(tenant.ID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Tenant
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(tenant.Metadata)
		if err != nil {
			return err
		}
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO techstack_tenants (
				id, external_org_id, display_name, kind, status, metadata_json
			) VALUES (
				$1, NULLIF($2, ''), $3, $4, $5, $6::jsonb
			)
			ON CONFLICT (id) DO NOTHING
		`,
			tenantID,
			tenant.ExternalOrgID,
			firstNonEmpty(tenant.DisplayName, tenantID),
			firstNonEmpty(tenant.Kind, "self_hosted"),
			firstNonEmpty(tenant.Status, "active"),
			metadataJSON,
		); execErr != nil {
			return execErr
		}
		ensured, err := scanTenant(tx.QueryRowContext(ctx, `
			SELECT id, external_org_id, display_name, kind, status,
				metadata_json::text, created_at, updated_at
			FROM techstack_tenants
			WHERE id = $1
		`, tenantID))
		if err != nil {
			return err
		}
		out = ensured
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertUser(ctx context.Context, user User) (*User, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	userID := strings.TrimSpace(user.ID)
	if userID == "" {
		return nil, fmt.Errorf("controlplane: user id required")
	}

	metadataJSON, err := marshalObject(user.Metadata)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	out, err := scanUser(tx.QueryRowContext(ctx, `
		INSERT INTO techstack_users (
			id, primary_email, display_name, status, metadata_json
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, $5::jsonb
		)
		ON CONFLICT (id) DO UPDATE SET
			primary_email = EXCLUDED.primary_email,
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			metadata_json = EXCLUDED.metadata_json,
			updated_at = now()
		RETURNING id, primary_email, display_name, status,
			metadata_json::text, created_at, updated_at
	`, userID, user.PrimaryEmail, user.DisplayName, firstNonEmpty(user.Status, "active"), metadataJSON))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return out, nil
}

func (s *PostgresStore) UpsertMembership(ctx context.Context, membership Membership) (*Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(membership.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Membership
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(membership.Metadata)
		if err != nil {
			return err
		}
		created, err := scanMembership(tx.QueryRowContext(ctx, `
			INSERT INTO techstack_memberships (
				id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json
			) VALUES (
				$1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8::jsonb
			)
			ON CONFLICT (tenant_id, user_id) DO UPDATE SET
				role_key = EXCLUDED.role_key,
				provider_key = EXCLUDED.provider_key,
				subject_id = EXCLUDED.subject_id,
				status = EXCLUDED.status,
				-- A caller that carries no metadata must not erase the metadata a
				-- caller that does has written. The embedded portal open
				-- (provisionSSOControlPlaneUser) upserts the membership with no
				-- Metadata, so an unconditional EXCLUDED here blanked the
				-- entitlements that the standalone sign-in (v2CloudUserUpsert) had
				-- just stored -- and the entitlement gate for inventory reads that
				-- membership, so opening Techstack inside Cloud revoked the
				-- account's own inventory access (owner report 2026-09-18).
				metadata_json = CASE
					WHEN EXCLUDED.metadata_json IS NULL
						OR EXCLUDED.metadata_json = '{}'::jsonb
					THEN techstack_memberships.metadata_json
					ELSE EXCLUDED.metadata_json
				END,
				updated_at = now()
			RETURNING id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json::text, created_at, updated_at
		`,
			membership.ID,
			tenantID,
			membership.UserID,
			firstNonEmpty(membership.RoleKey, "member"),
			membership.ProviderKey,
			membership.SubjectID,
			firstNonEmpty(membership.Status, "active"),
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetMembership(ctx context.Context, tenantID, userID string) (*Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return nil, ErrNotFound
	}

	var out *Membership
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		membership, err := scanMembership(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, user_id, role_key, provider_key, subject_id,
				status, metadata_json::text, created_at, updated_at
			FROM techstack_memberships
			WHERE tenant_id = $1 AND user_id = $2
			LIMIT 1
		`, tenantID, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = membership
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListMembershipsByUser returns the user's active memberships across all
// tenants, newest first. techstack_memberships is FORCE-RLS tenant-fenced;
// the membership_self_lookup policy (migration 043) opens SELECT for exactly
// the rows whose user_id matches the request-local app.user_id GUC set here.
func (s *PostgresStore) ListMembershipsByUser(ctx context.Context, userID string) ([]Membership, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("controlplane: user id required")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, gucErr := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", userGUC, userID); gucErr != nil {
		return nil, fmt.Errorf("set user guc: %w", gucErr)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, tenant_id, user_id, role_key, provider_key, subject_id,
			status, metadata_json::text, created_at, updated_at
		FROM techstack_memberships
		WHERE user_id = $1 AND status = 'active'
		ORDER BY updated_at DESC, id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var memberships []Membership
	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, *membership)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return memberships, nil
}

func (s *PostgresStore) UpsertAuthConfig(ctx context.Context, config AuthConfig) (*AuthConfig, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(config.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *AuthConfig
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		configJSON, err := marshalObject(config.Config)
		if err != nil {
			return err
		}
		created, err := scanAuthConfig(tx.QueryRowContext(ctx, `
			INSERT INTO auth_config (
				id, tenant_id, instance_id, mode, config_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5::jsonb
			)
			ON CONFLICT (tenant_id, instance_id) DO UPDATE SET
				mode = EXCLUDED.mode,
				config_json = EXCLUDED.config_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, mode, config_json::text,
				created_at, updated_at
		`,
			config.ID,
			tenantID,
			config.InstanceID,
			firstNonEmpty(config.Mode, "local"),
			configJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertBreakglassAdmin(ctx context.Context, admin BreakglassAdmin) (*BreakglassAdmin, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(admin.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *BreakglassAdmin
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(admin.Metadata)
		if err != nil {
			return err
		}
		created, err := scanBreakglassAdmin(tx.QueryRowContext(ctx, `
			INSERT INTO breakglass_admin (
				id, tenant_id, user_id, email, password_hash, locked, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, $5, $6, $7::jsonb
			)
			ON CONFLICT (tenant_id) DO UPDATE SET
				user_id = EXCLUDED.user_id,
				email = EXCLUDED.email,
				password_hash = EXCLUDED.password_hash,
				locked = EXCLUDED.locked,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, user_id, email, password_hash, locked,
				last_used_at, metadata_json::text, created_at, updated_at
		`,
			admin.ID,
			tenantID,
			admin.UserID,
			admin.Email,
			admin.PasswordHash,
			admin.Locked,
			metadataJSON,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetBreakglassAdmin(ctx context.Context, tenantID string) (*BreakglassAdmin, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *BreakglassAdmin
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		admin, err := scanBreakglassAdmin(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, user_id, email, password_hash, locked,
				last_used_at, metadata_json::text, created_at, updated_at
			FROM breakglass_admin
			WHERE tenant_id = $1
		`, tenantID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = admin
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanTenant(row rowScanner) (*Tenant, error) {
	var tenant Tenant
	var externalOrgID sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&tenant.ID,
		&externalOrgID,
		&tenant.DisplayName,
		&tenant.Kind,
		&tenant.Status,
		&metadataJSON,
		&tenant.CreatedAt,
		&tenant.UpdatedAt,
	); err != nil {
		return nil, err
	}
	tenant.ExternalOrgID = externalOrgID.String
	if err := decodeObject(metadataJSON, &tenant.Metadata); err != nil {
		return nil, err
	}
	return &tenant, nil
}

func scanUser(row rowScanner) (*User, error) {
	var user User
	var email, displayName sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&user.ID,
		&email,
		&displayName,
		&user.Status,
		&metadataJSON,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		return nil, err
	}
	user.PrimaryEmail = email.String
	user.DisplayName = displayName.String
	if err := decodeObject(metadataJSON, &user.Metadata); err != nil {
		return nil, err
	}
	return &user, nil
}

func scanMembership(row rowScanner) (*Membership, error) {
	var membership Membership
	var providerKey, subjectID sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&membership.ID,
		&membership.TenantID,
		&membership.UserID,
		&membership.RoleKey,
		&providerKey,
		&subjectID,
		&membership.Status,
		&metadataJSON,
		&membership.CreatedAt,
		&membership.UpdatedAt,
	); err != nil {
		return nil, err
	}
	membership.ProviderKey = providerKey.String
	membership.SubjectID = subjectID.String
	if err := decodeObject(metadataJSON, &membership.Metadata); err != nil {
		return nil, err
	}
	return &membership, nil
}

func scanAuthConfig(row rowScanner) (*AuthConfig, error) {
	var config AuthConfig
	var instanceID sql.NullString
	var configJSON []byte
	if err := row.Scan(
		&config.ID,
		&config.TenantID,
		&instanceID,
		&config.Mode,
		&configJSON,
		&config.CreatedAt,
		&config.UpdatedAt,
	); err != nil {
		return nil, err
	}
	config.InstanceID = instanceID.String
	if err := decodeObject(configJSON, &config.Config); err != nil {
		return nil, err
	}
	return &config, nil
}

func scanBreakglassAdmin(row rowScanner) (*BreakglassAdmin, error) {
	var admin BreakglassAdmin
	var userID sql.NullString
	var lastUsedAt sql.NullTime
	var metadataJSON []byte
	if err := row.Scan(
		&admin.ID,
		&admin.TenantID,
		&userID,
		&admin.Email,
		&admin.PasswordHash,
		&admin.Locked,
		&lastUsedAt,
		&metadataJSON,
		&admin.CreatedAt,
		&admin.UpdatedAt,
	); err != nil {
		return nil, err
	}
	admin.UserID = userID.String
	if lastUsedAt.Valid {
		admin.LastUsedAt = &lastUsedAt.Time
	}
	if err := decodeObject(metadataJSON, &admin.Metadata); err != nil {
		return nil, err
	}
	return &admin, nil
}

var _ AuthStore = (*PostgresStore)(nil)

// userGUC scopes the membership self-lookup RLS policy (migration 043).
const userGUC = "app.user_id"
