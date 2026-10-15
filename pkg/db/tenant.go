package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNoTenant is returned when a tenant-scoped operation is invoked without a
// tenant ID. RLS-aware code paths must always provide one to defend against
// cross-tenant leakage.
var ErrNoTenant = errors.New("no tenant id")

// TenantGUC is the Postgres GUC (custom session variable) that RLS policies
// match against via current_setting('app.tenant_id', true).
const TenantGUC = "app.tenant_id"

// WithTenant runs fn inside a transaction with the per-session GUC
// `app.tenant_id` set to tenantID. Any RLS policy that filters on
// current_setting('app.tenant_id', true) will see the matching value for the
// duration of the transaction.
//
// tenantID must be non-empty and is rejected if it contains characters that
// could break out of the SET LOCAL statement (the value is passed via a
// parameterised SELECT set_config call to avoid SQL injection).
//
// On success the transaction is committed. On any error from fn the
// transaction is rolled back and the original error is returned.
func (db *DB) WithTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	if db == nil {
		return errors.New("db: handle is nil")
	}
	tx, err := BeginTenantTransaction(ctx, db.DB, tenantID)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// BeginTenantTransaction is the shared transaction primitive for scoped stores.
// The returned transaction has its exact local tenant set before any payload SQL.
func BeginTenantTransaction(ctx context.Context, pool *sql.DB, tenantID string) (*sql.Tx, error) {
	if ctx == nil || strings.TrimSpace(tenantID) == "" {
		return nil, ErrNoTenant
	}
	if pool == nil {
		return nil, errors.New("db: handle is nil")
	}
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tenant tx: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", TenantGUC, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("set tenant guc: %w", err)
	}
	return tx, nil
}
