package controlplane

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// GuardTenantHandoff is the prior owner's confirmation that the command
// evidence a retired server still holds may leave it by export or quarantine
// (Guard G04, guardbinding/handoff.go). It belongs to the prior tenant and is
// bound to the host key the prior worker's identity used. The one-time code is
// stored only as its hash; a rebind of that server to another tenant consumes
// it exactly once, and the consuming tenant, worker and key are recorded.
type GuardTenantHandoff struct {
	TenantID           string
	HandoffID          string
	WorkerID           string
	ServerID           string
	Disposition        string
	ConfirmationSHA256 string
	ConfirmedBy        string
	ConfirmedAt        time.Time
	ExpiresAt          time.Time
	// KeyThumbprint is the prior worker's host key at confirmation; empty for
	// a worker that never bound one.
	KeyThumbprint  string
	ReceiptDigest  string
	ReceiptEntries int
	ReceiptedAt    *time.Time
}

// GuardTenantHandoffConsume presents one removal receipt for a confirmation
// on behalf of one registering pairing.
type GuardTenantHandoffConsume struct {
	TenantID           string
	HandoffID          string
	ConfirmationSHA256 string
	Disposition        string
	ReceiptDigest      string
	ReceiptEntries     int
	// ConsumerTenantID and ConsumerWorkerID name the binding being issued;
	// KeyThumbprint is the host key that pairing proved (empty without one).
	ConsumerTenantID string
	ConsumerWorkerID string
	KeyThumbprint    string
	Now              time.Time
}

// GuardTenantHandoffStore records and consumes custodian confirmations and
// answers which other tenants already used a host key.
type GuardTenantHandoffStore interface {
	ConfirmGuardTenantHandoff(ctx context.Context, handoff GuardTenantHandoff) error
	// ConsumeGuardTenantHandoff accepts a receipt once, before the
	// confirmation expires, from a pairing that proved the confirmed key.
	// Only the same pairing may repeat it, within
	// guardbinding.HandoffRepeatWindow. Anything else is ErrConflict.
	ConsumeGuardTenantHandoff(ctx context.Context, consume GuardTenantHandoffConsume) (*GuardTenantHandoff, error)
	// GuardKeyCustodyTenants lists the tenants other than tenantID whose
	// workers used the host key and were not released by a consumed handoff.
	GuardKeyCustodyTenants(ctx context.Context, thumbprint, tenantID string) ([]string, error)
	// VerifyGuardTenantHandoff checks an unconsumed, unexpired confirmation
	// for the key a server proved, before that server removes anything.
	VerifyGuardTenantHandoff(ctx context.Context, tenantID, handoffID, confirmationSHA256, disposition, keyThumbprint string, now time.Time) error
}

// ConfirmGuardTenantHandoff stores a new confirmation of the prior tenant.
func (s *PostgresStore) ConfirmGuardTenantHandoff(ctx context.Context, h GuardTenantHandoff) error {
	if err := s.identityReady(); err != nil {
		return err
	}
	if strings.TrimSpace(h.TenantID) == "" || strings.TrimSpace(h.HandoffID) == "" || strings.TrimSpace(h.WorkerID) == "" ||
		h.ConfirmationSHA256 == "" || strings.TrimSpace(h.ConfirmedBy) == "" {
		return fmt.Errorf("controlplane: incomplete guard tenant handoff")
	}
	return s.withTenant(ctx, h.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO guard_tenant_handoffs
			    (tenant_id, handoff_id, worker_id, server_id, disposition, confirmation_sha256, confirmed_by, confirmed_at, expires_at, key_thumbprint)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			h.TenantID, h.HandoffID, h.WorkerID, h.ServerID, h.Disposition, h.ConfirmationSHA256, h.ConfirmedBy,
			h.ConfirmedAt.UTC(), h.ExpiresAt.UTC(), h.KeyThumbprint)
		return err
	})
}

// ConsumeGuardTenantHandoff runs in the prior tenant's scope: the rebind
// presents that tenant's handoff id and code, and nothing else of that tenant
// is read.
func (s *PostgresStore) ConsumeGuardTenantHandoff(ctx context.Context, c GuardTenantHandoffConsume) (*GuardTenantHandoff, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.TenantID) == "" || strings.TrimSpace(c.HandoffID) == "" || c.ConfirmationSHA256 == "" || c.ReceiptDigest == "" {
		return nil, ErrConflict
	}
	var out *GuardTenantHandoff
	err := s.withTenant(ctx, c.TenantID, func(tx *sql.Tx) error {
		var h GuardTenantHandoff
		var receipt, consumerTenant, consumerWorker, consumerKey sql.NullString
		var receiptedAt sql.NullTime
		var entries sql.NullInt64
		row := tx.QueryRowContext(ctx, `
			SELECT tenant_id, handoff_id, worker_id, server_id, disposition, confirmation_sha256, confirmed_by,
			       confirmed_at, expires_at, key_thumbprint, receipt_digest, receipt_entries, receipted_at,
			       consumed_by_tenant_id, consumed_by_worker_id, consumed_by_key_thumbprint
			FROM guard_tenant_handoffs WHERE tenant_id = $1 AND handoff_id = $2 FOR UPDATE`, c.TenantID, c.HandoffID)
		if err := row.Scan(&h.TenantID, &h.HandoffID, &h.WorkerID, &h.ServerID, &h.Disposition, &h.ConfirmationSHA256,
			&h.ConfirmedBy, &h.ConfirmedAt, &h.ExpiresAt, &h.KeyThumbprint, &receipt, &entries, &receiptedAt,
			&consumerTenant, &consumerWorker, &consumerKey); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		record := guardbinding.HandoffConfirmationRecord{
			TenantID: h.TenantID, ConfirmationSHA256: h.ConfirmationSHA256, Disposition: h.Disposition,
			KeyThumbprint: h.KeyThumbprint, ExpiresAt: h.ExpiresAt,
		}
		if receipt.Valid {
			h.ReceiptDigest, h.ReceiptEntries = receipt.String, int(entries.Int64)
			at := receiptedAt.Time
			h.ReceiptedAt = &at
			record.ReceiptDigest, record.ReceiptedAt = receipt.String, &at
			record.ConsumerTenantID, record.ConsumerWorkerID, record.ConsumerKey = consumerTenant.String, consumerWorker.String, consumerKey.String
		}
		repeat, ok := guardbinding.DecideHandoffConsumption(record, guardbinding.HandoffPresentation{
			ConfirmationSHA256: c.ConfirmationSHA256, Disposition: c.Disposition, ReceiptDigest: c.ReceiptDigest,
			ConsumerTenantID: c.ConsumerTenantID, ConsumerWorkerID: c.ConsumerWorkerID, KeyThumbprint: c.KeyThumbprint, Now: c.Now,
		})
		if !ok {
			return ErrConflict
		}
		if repeat {
			out = &h
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE guard_tenant_handoffs SET receipt_digest = $3, receipt_entries = $4, receipted_at = $5,
			       consumed_by_tenant_id = $6, consumed_by_worker_id = $7, consumed_by_key_thumbprint = $8
			WHERE tenant_id = $1 AND handoff_id = $2 AND receipt_digest IS NULL`,
			c.TenantID, c.HandoffID, c.ReceiptDigest, c.ReceiptEntries, c.Now.UTC(),
			c.ConsumerTenantID, c.ConsumerWorkerID, c.KeyThumbprint); err != nil {
			return err
		}
		at := c.Now.UTC()
		h.ReceiptDigest, h.ReceiptEntries, h.ReceiptedAt = c.ReceiptDigest, c.ReceiptEntries, &at
		out = &h
		return nil
	})
	return out, err
}

// GuardKeyCustodyTenants reads the migration-owned key directory through its
// single lookup function; the directory itself is not readable by the runtime.
func (s *PostgresStore) GuardKeyCustodyTenants(ctx context.Context, thumbprint, tenantID string) ([]string, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(thumbprint) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id FROM techstack_find_guard_key_custody($1, $2)`, thumbprint, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	return tenants, rows.Err()
}

// VerifyGuardTenantHandoff answers a server's check of a confirmation before
// it removes anything: the code and disposition match, it has not expired or
// been consumed, and it was confirmed for the key the server proved. It runs
// in the prior tenant's scope and reveals nothing else of that tenant.
func (s *PostgresStore) VerifyGuardTenantHandoff(ctx context.Context, tenantID, handoffID, confirmationSHA256, disposition, keyThumbprint string, now time.Time) error {
	if err := s.identityReady(); err != nil {
		return err
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(handoffID) == "" || confirmationSHA256 == "" {
		return ErrConflict
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var stored, storedDisposition, storedKey string
		var expires time.Time
		var receipted bool
		err := tx.QueryRowContext(ctx, `
			SELECT confirmation_sha256, disposition, key_thumbprint, expires_at, receipt_digest IS NOT NULL
			FROM guard_tenant_handoffs WHERE tenant_id = $1 AND handoff_id = $2`, tenantID, handoffID).
			Scan(&stored, &storedDisposition, &storedKey, &expires, &receipted)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(confirmationSHA256)) != 1 || storedDisposition != disposition || !now.Before(expires) || receipted || storedKey == "" || storedKey != keyThumbprint {
			return ErrConflict
		}
		return nil
	})
}
