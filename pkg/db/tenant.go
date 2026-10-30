package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
)

// ErrNoTenant is returned when a tenant-scoped operation is invoked without a
// tenant ID. RLS-aware code paths must always provide one to defend against
// cross-tenant leakage.
var ErrNoTenant = errors.New("no tenant id")

// TenantGUC remains the local/system compatibility scope. Hosted policies
// additionally require the database-verified Gateway context.
const TenantGUC = "app.tenant_id"

// WithTenant runs fn inside a transaction bound to the Gateway context when
// present. Its database-verified tenant must equal tenantID before payload SQL.
// Native local and separately authorized system work retain their own scope;
// that scope alone never satisfies a hosted verified-context RLS policy.
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
	if err = tenantcontext.ApplyTenant(ctx, tx, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
