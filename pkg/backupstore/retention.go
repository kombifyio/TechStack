package backupstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Retirement is the durable decommission state of one managed backup store.
type Retirement struct {
	TokenID   string
	WipeAfter time.Time
	Wiped     bool
}

// WipeDue names one store whose retention deadline has passed.
type WipeDue struct {
	TenantID, StackID, TokenID string
}

// Retire records the retention deadline durably. An earlier deadline already
// on the row is kept, so a retried decommission never extends retention.
func (s *PostgresCustodyStore) Retire(ctx context.Context, tenantID, stackID string, wipeAfter time.Time) (Retirement, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" || wipeAfter.IsZero() {
		return Retirement{}, fmt.Errorf("%w: tenant id, stack id and wipe deadline are required", ErrInvalidCustody)
	}
	var out Retirement
	var wiped sql.NullTime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			UPDATE stack_backup_stores
			SET wipe_after = COALESCE(wipe_after, $3)
			WHERE tenant_id = $1 AND stack_id = $2
			RETURNING token_id, wipe_after, wiped_at
		`, tenantID, stackID, wipeAfter.UTC()).Scan(&out.TokenID, &out.WipeAfter, &wiped)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCustodyNotFound
		}
		return err
	})
	if err != nil {
		return Retirement{}, fmt.Errorf("retire managed backup custody: %w", err)
	}
	out.Wiped = wiped.Valid
	return out, nil
}

// MarkTokenRevoked records that the per-stack token is gone at the provider.
// From then on the custody row is no longer served to backup operations.
func (s *PostgresCustodyStore) MarkTokenRevoked(ctx context.Context, tenantID, stackID string) error {
	return s.markRetirement(ctx, tenantID, stackID, "token_revoked_at = COALESCE(token_revoked_at, now())")
}

// MarkWiped records that the bucket and token are gone at the provider.
func (s *PostgresCustodyStore) MarkWiped(ctx context.Context, tenantID, stackID string) error {
	return s.markRetirement(ctx, tenantID, stackID,
		"wiped_at = COALESCE(wiped_at, now()), token_revoked_at = COALESCE(token_revoked_at, now())")
}

func (s *PostgresCustodyStore) markRetirement(ctx context.Context, tenantID, stackID, assignment string) error {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE stack_backup_stores SET `+assignment+`
			WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID)
		return err
	})
}

// DueWipes lists stores past their retention deadline across tenants.
func (s *PostgresCustodyStore) DueWipes(ctx context.Context, now time.Time, afterTenant, afterStack string, limit int) ([]WipeDue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, stack_id, token_id
		FROM techstack_list_due_backup_wipes($1, $2, $3, $4)
	`, now.UTC(), strings.TrimSpace(afterTenant), strings.TrimSpace(afterStack), limit)
	if err != nil {
		return nil, fmt.Errorf("list due backup wipes: %w", err)
	}
	defer rows.Close()
	var due []WipeDue
	for rows.Next() {
		var item WipeDue
		if err := rows.Scan(&item.TenantID, &item.StackID, &item.TokenID); err != nil {
			return nil, fmt.Errorf("scan due backup wipe: %w", err)
		}
		due = append(due, item)
	}
	return due, rows.Err()
}

// BackupStoreAbsent reports whether the stack's off-site bucket is provably
// gone: either it never had one, or the wipe completed. A store inside its
// retention window is present.
func (s *PostgresCustodyStore) BackupStoreAbsent(ctx context.Context, tenantID, stackID string) (bool, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return false, fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	absent := false
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var wiped sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT wiped_at FROM stack_backup_stores
			WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID).Scan(&wiped)
		if errors.Is(err, sql.ErrNoRows) {
			absent = true
			return nil
		}
		absent = wiped.Valid
		return err
	})
	return absent, err
}

// RetentionRepository is the durable state the Retirer drives.
type RetentionRepository interface {
	Retire(ctx context.Context, tenantID, stackID string, wipeAfter time.Time) (Retirement, error)
	MarkTokenRevoked(ctx context.Context, tenantID, stackID string) error
	MarkWiped(ctx context.Context, tenantID, stackID string) error
	DueWipes(ctx context.Context, now time.Time, afterTenant, afterStack string, limit int) ([]WipeDue, error)
}

// StoreWiper is the provider side of decommission.
type StoreWiper interface {
	RevokeToken(ctx context.Context, tokenID string) error
	Wipe(ctx context.Context, stackID, tokenID string, purger ObjectPurger) error
}

const wipeSweepBatch = 50

// Retirer decommissions a managed backup store: the token is revoked at once,
// the bucket is wiped when the stored retention deadline passes.
type Retirer struct {
	repo   RetentionRepository
	wiper  StoreWiper
	purger ObjectPurger
	now    func() time.Time
}

// NewRetirer needs a purger to ever wipe; with a nil purger a wipe fails
// closed rather than leaving objects behind a deleted bucket record.
func NewRetirer(repo RetentionRepository, wiper StoreWiper, purger ObjectPurger) (*Retirer, error) {
	if repo == nil || wiper == nil {
		return nil, errors.New("managed backup retirer requires a retention repository and a store wiper")
	}
	return &Retirer{repo: repo, wiper: wiper, purger: purger, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Decommission is idempotent. A stack that never had an off-site store is a
// no-op. With retention <= 0 the bucket is wiped before it returns.
func (r *Retirer) Decommission(ctx context.Context, tenantID, stackID string, retention time.Duration) error {
	if retention < 0 {
		retention = 0
	}
	now := r.now()
	retirement, err := r.repo.Retire(ctx, tenantID, stackID, now.Add(retention))
	if errors.Is(err, ErrCustodyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if retirement.Wiped {
		return nil
	}
	if err := r.wiper.RevokeToken(ctx, retirement.TokenID); err != nil {
		return fmt.Errorf("revoke managed backup token: %w", err)
	}
	if err := r.repo.MarkTokenRevoked(ctx, tenantID, stackID); err != nil {
		return fmt.Errorf("record managed backup token revocation: %w", err)
	}
	if retirement.WipeAfter.After(now) {
		return nil
	}
	return r.wipe(ctx, tenantID, stackID, retirement.TokenID)
}

func (r *Retirer) wipe(ctx context.Context, tenantID, stackID, tokenID string) error {
	if r.purger == nil {
		return errors.New("wipe managed backup store: object purger is not configured")
	}
	if err := r.wiper.Wipe(ctx, stackID, tokenID, r.purger); err != nil {
		return fmt.Errorf("wipe managed backup store: %w", err)
	}
	return r.repo.MarkWiped(ctx, tenantID, stackID)
}

// SweepDue wipes every store past its deadline. A failing store does not stop
// the others; the joined error reports each, and the next pass retries them.
func (r *Retirer) SweepDue(ctx context.Context) (int, error) {
	var (
		wiped             int
		failures          []error
		afterTenant, last string
	)
	now := r.now()
	for {
		due, err := r.repo.DueWipes(ctx, now, afterTenant, last, wipeSweepBatch)
		if err != nil {
			return wiped, errors.Join(append(failures, err)...)
		}
		for _, item := range due {
			afterTenant, last = item.TenantID, item.StackID
			if err := r.wipe(ctx, item.TenantID, item.StackID, item.TokenID); err != nil {
				failures = append(failures, fmt.Errorf("stack %s: %w", item.StackID, err))
				continue
			}
			wiped++
		}
		if len(due) < wipeSweepBatch {
			return wiped, errors.Join(failures...)
		}
	}
}

// Run sweeps on an interval until the context ends.
func (r *Retirer) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if r == nil {
		return
	}
	pass := func() {
		if _, err := r.SweepDue(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	pass()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass()
		}
	}
}

var _ RetentionRepository = (*PostgresCustodyStore)(nil)
var _ StoreWiper = (*Client)(nil)
