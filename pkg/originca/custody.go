package originca

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

const custodyTenantGUC = "app.tenant_id"

// ErrNotFound reports a stack without an origin certificate record.
var ErrNotFound = errors.New("origin certificate custody not found")

// Record is the custody of one stack's origin certificate: Cloudflare ids and
// public facts only. It has no key and no certificate body.
type Record struct {
	TenantID          string
	StackID           string
	CertificateID     string
	Hosts             []string
	CertificateSHA256 string
	NotAfter          time.Time
	InstalledAt       time.Time
	// RetiredIDs are superseded certificates awaiting Cloudflare revocation.
	RetiredIDs []string
}

// Store persists Records. Implementations scope every call to the tenant.
type Store interface {
	Get(ctx context.Context, tenantID, stackID string) (Record, error)
	// Put replaces the stack's record.
	Put(ctx context.Context, record Record) error
	// MarkInstalled records the node's install confirmation for the current id.
	MarkInstalled(ctx context.Context, tenantID, stackID, certificateID, certificateSHA256 string, installedAt time.Time) error
	// ClearRetired removes one revoked id from the retired list.
	ClearRetired(ctx context.Context, tenantID, stackID, certificateID string) error
	Delete(ctx context.Context, tenantID, stackID string) error
}

// PostgresStore keeps Records under tenant row-level security.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("origin certificate custody: database not configured")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", custodyTenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, stackID string) (Record, error) {
	var record Record
	var sha sql.NullString
	var installed sql.NullTime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT tenant_id, stack_id, certificate_id, hostnames, certificate_sha256, not_after, installed_at, retired_certificate_ids
			FROM stack_origin_certificates WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID).
			Scan(&record.TenantID, &record.StackID, &record.CertificateID, pq.Array(&record.Hosts), &sha, &record.NotAfter, &installed, pq.Array(&record.RetiredIDs))
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load origin certificate custody: %w", err)
	}
	record.CertificateSHA256, record.InstalledAt = sha.String, installed.Time
	return record, nil
}

func (s *PostgresStore) Put(ctx context.Context, record Record) error {
	hosts, err := NormalizeHosts(record.Hosts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.CertificateID) == "" || record.TenantID == "" || record.StackID == "" {
		return fmt.Errorf("origin certificate custody requires tenant, stack and certificate id")
	}
	retired := record.RetiredIDs
	if retired == nil {
		retired = []string{}
	}
	err = s.withTenant(ctx, record.TenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO stack_origin_certificates (tenant_id, stack_id, certificate_id, hostnames, certificate_sha256, not_after, installed_at, retired_certificate_ids)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8)
			ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
				certificate_id = EXCLUDED.certificate_id, hostnames = EXCLUDED.hostnames,
				certificate_sha256 = EXCLUDED.certificate_sha256, not_after = EXCLUDED.not_after,
				installed_at = EXCLUDED.installed_at, retired_certificate_ids = EXCLUDED.retired_certificate_ids`,
			record.TenantID, record.StackID, record.CertificateID, pq.Array(hosts), record.CertificateSHA256,
			record.NotAfter.UTC(), nullTime(record.InstalledAt), pq.Array(retired))
		return execErr
	})
	if err != nil {
		return fmt.Errorf("persist origin certificate custody: %w", err)
	}
	return nil
}

func nullTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func (s *PostgresStore) MarkInstalled(ctx context.Context, tenantID, stackID, certificateID, certificateSHA256 string, installedAt time.Time) error {
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE stack_origin_certificates SET installed_at = $4, certificate_sha256 = NULLIF($5, '')
			WHERE tenant_id = $1 AND stack_id = $2 AND certificate_id = $3`,
			tenantID, stackID, certificateID, installedAt.UTC(), certificateSHA256)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) ClearRetired(ctx context.Context, tenantID, stackID, certificateID string) error {
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE stack_origin_certificates SET retired_certificate_ids = array_remove(retired_certificate_ids, $3)
			WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID, certificateID)
		return err
	})
}

func (s *PostgresStore) Delete(ctx context.Context, tenantID, stackID string) error {
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM stack_origin_certificates WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID)
		return err
	})
}
