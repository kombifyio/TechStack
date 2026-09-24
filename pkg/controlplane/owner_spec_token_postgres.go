package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) StoreOwnerSpecToken(ctx context.Context, token OwnerSpecToken) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if tenantID == "" || strings.TrimSpace(token.TokenHash) == "" || strings.TrimSpace(token.StackID) == "" || strings.TrimSpace(token.OwnerID) == "" || token.ExpiresAt.IsZero() {
		return fmt.Errorf("controlplane: incomplete owner spec token")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO owner_spec_tokens (token_hash, tenant_id, stack_id, owner_id, status, expires_at)
			VALUES ($1, $2, $3, $4, 'issued', $5)
			ON CONFLICT (token_hash) DO UPDATE SET
				status = 'issued', expires_at = EXCLUDED.expires_at, consumed_at = NULL
			WHERE owner_spec_tokens.tenant_id = EXCLUDED.tenant_id
				AND owner_spec_tokens.stack_id = EXCLUDED.stack_id
				AND owner_spec_tokens.owner_id = EXCLUDED.owner_id
		`, token.TokenHash, tenantID, token.StackID, token.OwnerID, token.ExpiresAt.UTC())
		return err
	})
}

func (s *PostgresStore) ConsumeOwnerSpecToken(ctx context.Context, token OwnerSpecToken, consumedAt time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if consumedAt.IsZero() {
		consumedAt = time.Now().UTC()
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE owner_spec_tokens
			SET status = 'consumed', consumed_at = $5
			WHERE token_hash = $1 AND tenant_id = $2 AND stack_id = $3 AND owner_id = $4
				AND status = 'issued' AND expires_at > $5
		`, token.TokenHash, tenantID, token.StackID, token.OwnerID, consumedAt.UTC())
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrNotFound
		}
		return nil
	})
}

var _ OwnerSpecTokenStore = (*PostgresStore)(nil)
