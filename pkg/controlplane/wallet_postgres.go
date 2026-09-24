package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *PostgresStore) UpsertWalletItem(ctx context.Context, item WalletItem) (*WalletItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(item.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *WalletItem
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		metadataJSON, err := marshalObject(item.Metadata)
		if err != nil {
			return err
		}
		walletItem, err := scanWalletItem(tx.QueryRowContext(ctx, `
			INSERT INTO wallet_items (
				id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''),
				NULLIF($7, ''), $8::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				stack_id = EXCLUDED.stack_id,
				item_type = EXCLUDED.item_type,
				provider = EXCLUDED.provider,
				external_ref = EXCLUDED.external_ref,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			WHERE wallet_items.tenant_id = EXCLUDED.tenant_id
			RETURNING id, tenant_id, instance_id, stack_id, item_type, provider,
				external_ref, metadata_json::text, created_at, updated_at
		`,
			item.ID,
			tenantID,
			item.InstanceID,
			item.StackID,
			firstNonEmpty(item.ItemType, "other"),
			item.Provider,
			item.ExternalRef,
			metadataJSON,
		))
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
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}

	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM wallet_items
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, itemID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
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
