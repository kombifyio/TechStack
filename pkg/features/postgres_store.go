package features

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/identity"
)

const featureTenantGUC = "app.tenant_id"

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) GetUserFlag(ctx context.Context, userID, featureKey string) (*bool, error) {
	tenantID := featureTenantID(ctx, userID)
	var enabled *bool
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var value bool
		err := tx.QueryRowContext(ctx, `
			SELECT enabled
			FROM feature_preferences
			WHERE tenant_id = $1 AND subject_id = $2 AND feature_key = $3
		`, tenantID, userID, featureKey).Scan(&value)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		enabled = &value
		return nil
	})
	return enabled, err
}

func (s *PostgresStore) GetUserFlags(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(featureKeys) == 0 {
		return result, nil
	}
	tenantID := featureTenantID(ctx, userID)
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT feature_key, enabled
			FROM feature_preferences
			WHERE tenant_id = $1 AND subject_id = $2
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		allowed := stringSet(featureKeys)
		for rows.Next() {
			var key string
			var enabled bool
			if err := rows.Scan(&key, &enabled); err != nil {
				return err
			}
			if allowed[key] {
				result[key] = enabled
			}
		}
		return rows.Err()
	})
	return result, err
}

func (s *PostgresStore) GetUserConsentsMap(ctx context.Context, userID string, featureKeys []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(featureKeys) == 0 {
		return result, nil
	}
	tenantID := featureTenantID(ctx, userID)
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT feature_key
			FROM feature_consents
			WHERE tenant_id = $1 AND subject_id = $2 AND revoked_at IS NULL
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		allowed := stringSet(featureKeys)
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			if allowed[key] {
				result[key] = true
			}
		}
		return rows.Err()
	})
	return result, err
}

func (s *PostgresStore) HasUserConsent(ctx context.Context, userID, featureKey string) (bool, error) {
	tenantID := featureTenantID(ctx, userID)
	var found bool
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM feature_consents
				WHERE tenant_id = $1 AND subject_id = $2 AND feature_key = $3
					AND revoked_at IS NULL
			)
		`, tenantID, userID, featureKey).Scan(&found)
		return err
	})
	return found, err
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("features: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("features: tenant id required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", featureTenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func featureTenantID(ctx context.Context, fallback string) string {
	if id := identity.FromContext(ctx); id != nil && strings.TrimSpace(id.OrgID) != "" {
		return strings.TrimSpace(id.OrgID)
	}
	return strings.TrimSpace(fallback)
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out[trimmed] = true
		}
	}
	return out
}

var _ Store = (*PostgresStore)(nil)
