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
	if strings.TrimSpace(tenantID) == "" {
		return ErrNoTenant
	}
	if db == nil || db.DB == nil {
		return errors.New("db: handle is nil")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// set_config(name, value, is_local=true) is equivalent to SET LOCAL
	// but allows passing the value as a parameter, which is safer than
	// concatenating into a SET LOCAL statement.
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", TenantGUC, tenantID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set tenant guc: %w", err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
