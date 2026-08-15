package clientpairing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const tenantGUC = "app.tenant_id"

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Create(ctx context.Context, code Code) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("client pairing: database not configured")
	}
	return s.withTenant(ctx, code.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO client_pairing_codes (
				id, tenant_id, instance_id, code_hash,
				tls_fingerprint_sha256, issued_by_subject_id,
				issued_at, expires_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`,
			code.ID,
			code.TenantID,
			code.InstanceID,
			code.CodeHash,
			code.TLSFingerprintSHA256,
			code.IssuedBySubjectID,
			code.IssuedAt,
			code.ExpiresAt,
		)
		if err != nil {
			return fmt.Errorf("insert client pairing code: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) Consume(ctx context.Context, req ConsumeRequest) (*Code, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("client pairing: database not configured")
	}
	var out *Code
	err := s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		consumed, err := scanCode(tx.QueryRowContext(ctx, `
			UPDATE client_pairing_codes
			SET consumed_at = $5, updated_at = $5
			WHERE tenant_id = $1
			  AND code_hash = $2
			  AND instance_id = $3
			  AND tls_fingerprint_sha256 = $4
			  AND consumed_at IS NULL
			  AND expires_at > $5
			RETURNING id, tenant_id, instance_id, code_hash,
				tls_fingerprint_sha256, issued_by_subject_id,
				issued_at, expires_at, consumed_at
		`, req.TenantID, req.CodeHash, req.InstanceID, req.TLSFingerprintSHA256, req.Now))
		if err == nil {
			out = consumed
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("consume client pairing code: %w", err)
		}

		existing, lookupErr := scanCode(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, code_hash,
				tls_fingerprint_sha256, issued_by_subject_id,
				issued_at, expires_at, consumed_at
			FROM client_pairing_codes
			WHERE tenant_id = $1 AND code_hash = $2
			LIMIT 1
		`, req.TenantID, req.CodeHash))
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return ErrInvalidCode
		}
		if lookupErr != nil {
			return fmt.Errorf("inspect client pairing code: %w", lookupErr)
		}
		switch {
		case existing.InstanceID != req.InstanceID || existing.TLSFingerprintSHA256 != req.TLSFingerprintSHA256:
			return ErrBindingMismatch
		case existing.ConsumedAt != nil:
			return ErrAlreadyConsumed
		case !existing.ExpiresAt.After(req.Now):
			return ErrExpired
		default:
			// A concurrent transaction may have consumed the row between the
			// UPDATE and diagnostic SELECT under READ COMMITTED. Treat this as
			// replay rather than allowing a second success.
			return ErrAlreadyConsumed
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("%w: tenant id required", ErrInvalidRequest)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCode(row rowScanner) (*Code, error) {
	var code Code
	var consumedAt sql.NullTime
	if err := row.Scan(
		&code.ID,
		&code.TenantID,
		&code.InstanceID,
		&code.CodeHash,
		&code.TLSFingerprintSHA256,
		&code.IssuedBySubjectID,
		&code.IssuedAt,
		&code.ExpiresAt,
		&consumedAt,
	); err != nil {
		return nil, err
	}
	if consumedAt.Valid {
		value := consumedAt.Time.UTC()
		code.ConsumedAt = &value
	}
	return &code, nil
}

var _ Store = (*PostgresStore)(nil)
var _ Store = (*MemoryStore)(nil)
