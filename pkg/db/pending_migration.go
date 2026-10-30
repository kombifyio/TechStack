package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// executePendingMigration is shared by the normal and exact forward-batch
// engines. The caller has already excluded committed ledger entries and owns
// the migration transaction; denial rolls back without recording this version.
func executePendingMigration(ctx context.Context, tx *sql.Tx, version string, body []byte) error {
	if version == "168_household_projection_bindings.sql" {
		// Preserve the published migration and its manifest checksum. Its role
		// creation is NOLOGIN, but its existing-role branch must not adopt a
		// usable credential into the cross-tenant capability. Activation belongs
		// to the separately governed private worker, never to schema application.
		// Use the same canonical LF identity as forwardBatchBodies on Windows.
		canonical := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		if fmt.Sprintf("%x", sha256.Sum256(canonical)) != "5a5b05d69c313258a46d103862ea005441388527d5136d935d5e37ef38d5a179" {
			return fmt.Errorf("household migration source binding differs")
		}
		var canLogin bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_roles
			WHERE rolname = 'techstack_household_worker' AND rolcanlogin
		)`).Scan(&canLogin); err != nil {
			return fmt.Errorf("household migration role posture unavailable: %w", err)
		}
		if canLogin {
			return fmt.Errorf("household migration refuses an existing login credential")
		}
	}
	_, err := tx.ExecContext(ctx, string(body))
	return err
}
