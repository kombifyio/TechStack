package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var walletMetadataKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RecordWalletItemMigration records, under metadataKey in the row's metadata,
// the kombify Wallet item the row moved to (Wallet plan slice P3.3). It is a
// compare-and-swap on the row revision (updated_at): when the row changed since
// the caller read it, or already names another Wallet item, nothing is written
// and ErrConflict is returned. The secret and TOTP ciphertext stay untouched.
func (s *PostgresStore) RecordWalletItemMigration(ctx context.Context, tenantID, itemID string, expectedUpdatedAt time.Time, metadataKey, kombifyWalletItemID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID, itemID, kombifyWalletItemID = strings.TrimSpace(tenantID), strings.TrimSpace(itemID), strings.TrimSpace(kombifyWalletItemID)
	if tenantID == "" || itemID == "" || kombifyWalletItemID == "" {
		return fmt.Errorf("controlplane: tenant id, item id and Wallet item id required")
	}
	if !walletMetadataKeyPattern.MatchString(metadataKey) {
		return fmt.Errorf("controlplane: invalid wallet item metadata key")
	}
	if expectedUpdatedAt.IsZero() {
		return fmt.Errorf("controlplane: expected wallet item revision required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE wallet_items
			SET metadata_json = metadata_json || jsonb_build_object($5::text, $4::text),
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND updated_at = $3
				AND COALESCE(metadata_json->>$5::text, '') IN ('', $4::text)
		`, tenantID, itemID, expectedUpdatedAt, kombifyWalletItemID, metadataKey)
		if err != nil {
			return err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return ErrConflict
		}
		return nil
	})
}
