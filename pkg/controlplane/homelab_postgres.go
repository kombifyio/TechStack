package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const homelabColumns = `id, tenant_id, owner_subject_id, name, intent_json::text,
		created_at, updated_at, deleted_at, named_at`

func (s *PostgresStore) CreateHomelab(ctx context.Context, req CreateHomelabRequest) (*Homelab, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if err := validateHomelabRequest(req); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.TenantID)

	var out *Homelab
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		homelab, err := s.insertHomelab(ctx, tx, req)
		if err != nil {
			return err
		}
		if homelab == nil {
			return ErrConflict
		}
		out = homelab
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetHomelabByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*Homelab, error) {
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

	var out *Homelab
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		homelab, err := selectHomelabByOwner(ctx, tx, tenantID, ownerSubjectID)
		if err != nil {
			return err
		}
		out = homelab
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetOrCreateHomelabForOwner inserts the owner's homelab or, when the active
// (tenant, owner) singleton already exists, returns that row unchanged. Both
// outcomes happen inside one tenant-scoped transaction.
func (s *PostgresStore) GetOrCreateHomelabForOwner(ctx context.Context, req CreateHomelabRequest) (*Homelab, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if err := validateHomelabRequest(req); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.TenantID)
	ownerSubjectID := strings.TrimSpace(req.OwnerSubjectID)

	var out *Homelab
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		homelab, err := s.insertHomelab(ctx, tx, req)
		if err != nil {
			return err
		}
		if homelab == nil {
			homelab, err = selectHomelabByOwner(ctx, tx, tenantID, ownerSubjectID)
			if errors.Is(err, ErrNotFound) {
				// The id collided with a row that does not belong to this
				// owner; surface the conflict instead of inventing state.
				return fmt.Errorf("%w: homelab id already in use", ErrConflict)
			}
			if err != nil {
				return err
			}
		}
		out = homelab
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpdateHomelabIntent(ctx context.Context, tenantID, homelabID string, intent map[string]any) (*Homelab, error) {
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

	var out *Homelab
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		intentJSON, err := marshalObject(intent)
		if err != nil {
			return err
		}
		homelab, err := scanHomelab(tx.QueryRowContext(ctx, `
			UPDATE homelabs
			SET intent_json = $3::jsonb, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			RETURNING `+homelabColumns+`
		`, tenantID, homelabID, intentJSON))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = homelab
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// wizard-run reader; each owns its own table and SQL, so collapsing them would
// hide the statement rather than remove duplication.
//
//nolint:dupl // shares the store's validate -> withTenant -> scan idiom with the
func (s *PostgresStore) UpdateHomelabName(ctx context.Context, tenantID, homelabID, name string) (*Homelab, error) {
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
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("controlplane: homelab name required")
	}

	var out *Homelab
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		homelab, err := scanHomelab(tx.QueryRowContext(ctx, `
			UPDATE homelabs
			SET name = $3, updated_at = now(), named_at = now()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			RETURNING `+homelabColumns+`
		`, tenantID, homelabID, name))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = homelab
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// insertHomelab returns (nil, nil) when the insert lost against the active
// (tenant, owner) singleton or an id collision; callers decide what that means.
func (s *PostgresStore) insertHomelab(ctx context.Context, tx *sql.Tx, req CreateHomelabRequest) (*Homelab, error) {
	intentJSON, err := marshalObject(req.Intent)
	if err != nil {
		return nil, err
	}
	homelab, err := scanHomelab(tx.QueryRowContext(ctx, `
		INSERT INTO homelabs (id, tenant_id, owner_subject_id, name, intent_json)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT DO NOTHING
		RETURNING `+homelabColumns+`
	`,
		strings.TrimSpace(req.ID),
		strings.TrimSpace(req.TenantID),
		strings.TrimSpace(req.OwnerSubjectID),
		strings.TrimSpace(req.Name),
		intentJSON,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return homelab, nil
}

func selectHomelabByOwner(ctx context.Context, tx *sql.Tx, tenantID, ownerSubjectID string) (*Homelab, error) {
	homelab, err := scanHomelab(tx.QueryRowContext(ctx, `
		SELECT `+homelabColumns+`
		FROM homelabs
		WHERE tenant_id = $1 AND owner_subject_id = $2 AND deleted_at IS NULL
	`, tenantID, ownerSubjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return homelab, nil
}

func validateHomelabRequest(req CreateHomelabRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return fmt.Errorf("controlplane: homelab id required")
	}
	if strings.TrimSpace(req.TenantID) == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	if strings.TrimSpace(req.OwnerSubjectID) == "" {
		return fmt.Errorf("controlplane: owner subject id required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("controlplane: homelab name required")
	}
	return nil
}

func scanHomelab(row rowScanner) (*Homelab, error) {
	var (
		homelab   Homelab
		intentRaw []byte
		deletedAt sql.NullTime
		namedAt   sql.NullTime
	)
	if err := row.Scan(
		&homelab.ID,
		&homelab.TenantID,
		&homelab.OwnerSubjectID,
		&homelab.Name,
		&intentRaw,
		&homelab.CreatedAt,
		&homelab.UpdatedAt,
		&deletedAt,
		&namedAt,
	); err != nil {
		return nil, err
	}
	if err := decodeObject(intentRaw, &homelab.Intent); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		homelab.DeletedAt = &t
	}
	if namedAt.Valid {
		t := namedAt.Time
		homelab.NamedAt = &t
	}
	return &homelab, nil
}

var _ HomelabStore = (*PostgresStore)(nil)
