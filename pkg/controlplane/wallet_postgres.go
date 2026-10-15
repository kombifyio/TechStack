package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *PostgresStore) UpsertWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(ctx, item, false, false)
}

func (s *PostgresStore) CreateWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(ctx, item, true, false)
}

func (s *PostgresStore) UpdateWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(ctx, item, false, true)
}

// The identity predicate executes with the write, including concurrent inserts.
// Legacy aliases must all agree; an ownerless record cannot be claimed by upsert.
const walletImmutableOwnerSQL = `
	COALESCE(NULLIF(btrim(wallet_items.metadata_json->>'owner_id'), ''),
		NULLIF(btrim(wallet_items.metadata_json->>'owner_subject_id'), ''),
		NULLIF(btrim(wallet_items.metadata_json->>'user_id'), '')) = $9
	AND NOT EXISTS (
		SELECT 1 FROM jsonb_each(wallet_items.metadata_json) AS identity(key, value)
		WHERE identity.key IN ('owner_id', 'owner_subject_id', 'user_id')
			AND identity.value <> 'null'::jsonb
			AND (jsonb_typeof(identity.value) <> 'string'
				OR btrim(identity.value #>> '{}') NOT IN ('', $9))
	)`

const walletReturningSQL = `
	RETURNING id, tenant_id, instance_id, stack_id, item_type, provider,
		external_ref, metadata_json::text, created_at, updated_at`

func (s *PostgresStore) saveWalletItem(ctx context.Context, item WalletItem, createOnly, updateOnly bool) (*WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	item, err := normalizeWalletIdentity(item)
	if err != nil {
		return nil, err
	}
	tenantID := item.TenantID

	var out *WalletItem
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(item.Metadata)
		if err != nil {
			return err
		}
		query := `
			INSERT INTO wallet_items (
				id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''),
				NULLIF($7, ''), $8::jsonb
			)`
		args := []any{item.ID, tenantID, item.InstanceID, item.StackID,
			item.ItemType, item.Provider, item.ExternalRef, metadataJSON}
		if createOnly {
			query += ` ON CONFLICT (id) DO NOTHING`
		} else {
			args = append(args, WalletOwnerID(item.Metadata))
			query += ` ON CONFLICT (id) DO UPDATE SET
				stack_id = EXCLUDED.stack_id,
				provider = EXCLUDED.provider,
				external_ref = EXCLUDED.external_ref,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			WHERE wallet_items.tenant_id = EXCLUDED.tenant_id
				AND wallet_items.item_type = EXCLUDED.item_type AND ` + walletImmutableOwnerSQL
		}
		if updateOnly {
			query = `UPDATE wallet_items SET
				stack_id = NULLIF($4, ''), provider = NULLIF($6, ''),
				external_ref = NULLIF($7, ''), metadata_json = $8::jsonb, updated_at = now()
				WHERE id = $1 AND tenant_id = $2 AND item_type = $5
					AND (NULLIF($3, '') IS NULL OR instance_id = $3) AND ` + walletImmutableOwnerSQL
		}
		walletItem, err := scanWalletItem(tx.QueryRowContext(ctx, query+walletReturningSQL, args...))
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		out = walletItem
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetWalletItem(ctx context.Context, tenantID, itemID string) (*WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		item, err := scanWalletItem(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
			FROM wallet_items
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, itemID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListWalletItems(ctx context.Context, tenantID, stackID string) ([]WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	stackID = strings.TrimSpace(stackID)

	var out []WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		query := `
			SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
			FROM wallet_items
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`
		args := []any{tenantID}
		if stackID != "" {
			query = `
				SELECT id, tenant_id, instance_id, stack_id, item_type, provider,
					external_ref, metadata_json::text, created_at, updated_at
				FROM wallet_items
				WHERE tenant_id = $1 AND stack_id = $2
				ORDER BY created_at DESC
			`
			args = append(args, stackID)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			item, err := scanWalletItem(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) DeleteWalletItem(ctx context.Context, tenantID, itemID string) error {
	return s.deleteWalletItem(ctx, tenantID, itemID, "", "", false)
}

func (s *PostgresStore) DeleteWalletItemForOwner(ctx context.Context, tenantID, itemID, ownerID, itemType string) error {
	if strings.TrimSpace(ownerID) == "" {
		return ErrConflict
	}
	return s.deleteWalletItem(ctx, tenantID, itemID, strings.TrimSpace(ownerID), itemType, true)
}

func (s *PostgresStore) deleteWalletItem(ctx context.Context, tenantID, itemID, ownerID, itemType string, checkOwner bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}

	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		query := `
			DELETE FROM wallet_items
			WHERE tenant_id = $1 AND id = $2
		`
		args := []any{tenantID, itemID}
		if checkOwner {
			query += ` AND item_type = $4 AND ` + strings.ReplaceAll(walletImmutableOwnerSQL, "$9", "$3")
			args = append(args, ownerID, itemType)
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			if checkOwner {
				return ErrConflict
			}
			return ErrNotFound
		}
		return nil
	})
}

func scanWalletItem(row rowScanner) (*WalletItem, error) {
	var item WalletItem
	var instanceID, stackID, provider, externalRef sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&item.ID,
		&item.TenantID,
		&instanceID,
		&stackID,
		&item.ItemType,
		&provider,
		&externalRef,
		&metadataJSON,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.InstanceID = instanceID.String
	item.StackID = stackID.String
	item.Provider = provider.String
	item.ExternalRef = externalRef.String
	if err := decodeObject(metadataJSON, &item.Metadata); err != nil {
		return nil, err
	}
	return &item, nil
}

var _ WalletStore = (*PostgresStore)(nil)
