package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var _ serverregistry.SweepStore = (*PostgresStore)(nil)
var _ serverregistry.OutboxMaintenanceStore = (*PostgresStore)(nil)

// trackedSweepConnectionStates mirrors the servers_wake_registry_sweep trigger
// predicate: only these connection states carry a demotable heartbeat promise.
const trackedSweepConnectionStates = "('connected', 'degraded', 'stale')"

// ListObservationSweepTenants pages the secret-free wake-up directory. It
// returns tenant IDs only; server rows are read afterwards inside the
// tenant-scoped RLS boundary.
func (s *PostgresStore) ListObservationSweepTenants(ctx context.Context, afterTenantID string, limit int, heartbeatCutoff time.Time) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: sweep tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id
		FROM server_registry_sweep_tenants
		WHERE tenant_id > $1 AND earliest_heartbeat_at < $2
		ORDER BY tenant_id ASC
		LIMIT $3
	`, strings.TrimSpace(afterTenantID), heartbeatCutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list observation sweep tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

// CompactObservationSweepTenant recomputes the tenant's wake-up entry from its
// tracked servers (tenant-scoped) and retires the entry when nothing demotable
// remains, so idle tenants stop appearing in the due directory.
func (s *PostgresStore) CompactObservationSweepTenant(ctx context.Context, tenantID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: sweep tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var earliest sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			SELECT MIN(last_heartbeat_at)
			FROM servers
			WHERE tenant_id = $1
			  AND last_heartbeat_at IS NOT NULL
			  AND connection_state IN `+trackedSweepConnectionStates+`
			  AND lifecycle_state NOT IN ('decommissioning', 'decommissioned')
		`, tenantID).Scan(&earliest); err != nil {
			return fmt.Errorf("controlplane: compact sweep tenant: %w", err)
		}
		if !earliest.Valid {
			_, err := tx.ExecContext(ctx, `
				DELETE FROM server_registry_sweep_tenants WHERE tenant_id = $1
			`, tenantID)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE server_registry_sweep_tenants
			SET earliest_heartbeat_at = $2, refreshed_at = clock_timestamp()
			WHERE tenant_id = $1
		`, tenantID, earliest.Time.UTC())
		return err
	})
}

// ListOutboxPruneTenants pages tenants whose oldest outbox row fell behind the
// retention window.
func (s *PostgresStore) ListOutboxPruneTenants(ctx context.Context, afterTenantID string, limit int, retentionCutoff time.Time) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: outbox prune tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id
		FROM server_registry_outbox_prune_tenants
		WHERE tenant_id > $1 AND oldest_created_at < $2
		ORDER BY tenant_id ASC
		LIMIT $3
	`, strings.TrimSpace(afterTenantID), retentionCutoff.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list outbox prune tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

// PruneServerRegistryOutbox deletes one bounded batch of retention-expired
// outbox rows inside the tenant's RLS scope. Pruning unclaimed history is safe:
// the future K6 projector bootstraps via full aggregate resync, never by
// replaying server_registry_outbox history (bead kombify-Techstack-nzy1.1).
func (s *PostgresStore) PruneServerRegistryOutbox(ctx context.Context, tenantID string, retentionCutoff time.Time, batchLimit int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || batchLimit < 1 || batchLimit > 10000 {
		return 0, fmt.Errorf("controlplane: outbox prune requires a tenant and a batch limit from 1 to 10000")
	}
	var deleted int64
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			DELETE FROM server_registry_outbox
			WHERE id IN (
				SELECT id FROM server_registry_outbox
				WHERE tenant_id = $1 AND created_at < $2
				ORDER BY created_at, id
				LIMIT $3
			)
		`, tenantID, retentionCutoff.UTC(), batchLimit)
		if execErr != nil {
			return fmt.Errorf("controlplane: prune server registry outbox: %w", execErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		deleted = rows
		return nil
	})
	return deleted, err
}

// CompactOutboxPruneTenant recomputes the tenant's oldest remaining outbox row
// and retires the wake-up entry when the tenant holds no outbox history.
func (s *PostgresStore) CompactOutboxPruneTenant(ctx context.Context, tenantID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: outbox prune tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var oldest sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			SELECT MIN(created_at) FROM server_registry_outbox WHERE tenant_id = $1
		`, tenantID).Scan(&oldest); err != nil {
			return fmt.Errorf("controlplane: compact outbox prune tenant: %w", err)
		}
		if !oldest.Valid {
			_, err := tx.ExecContext(ctx, `
				DELETE FROM server_registry_outbox_prune_tenants WHERE tenant_id = $1
			`, tenantID)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE server_registry_outbox_prune_tenants
			SET oldest_created_at = $2, refreshed_at = clock_timestamp()
			WHERE tenant_id = $1
		`, tenantID, oldest.Time.UTC())
		return err
	})
}

// ServerRegistryOutboxStats reports the secret-free outbox size signal: the
// planner row estimate (cheap enough for a 30s gauge) plus the oldest tracked
// row from the wake-up directory.
func (s *PostgresStore) ServerRegistryOutboxStats(ctx context.Context) (serverregistry.OutboxStats, error) {
	var stats serverregistry.OutboxStats
	if s == nil || s.db == nil {
		return stats, fmt.Errorf("controlplane: database not configured")
	}
	var estimate sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT c.reltuples
		FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE c.relname = 'server_registry_outbox' AND n.nspname = current_schema()
	`).Scan(&estimate); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stats, fmt.Errorf("controlplane: estimate outbox rows: %w", err)
	}
	if estimate.Valid && estimate.Float64 > 0 {
		stats.EstimatedRows = int64(estimate.Float64)
	}
	var oldest sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT MIN(oldest_created_at) FROM server_registry_outbox_prune_tenants
	`).Scan(&oldest); err != nil {
		return stats, fmt.Errorf("controlplane: read oldest outbox row: %w", err)
	}
	if oldest.Valid {
		oldestAt := oldest.Time.UTC()
		stats.OldestCreatedAt = &oldestAt
	}
	return stats, nil
}

func scanTenantIDPage(rows *sql.Rows, limit int) ([]string, error) {
	defer func() { _ = rows.Close() }()
	tenantIDs := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("controlplane: scan sweep tenant: %w", err)
		}
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			return nil, fmt.Errorf("controlplane: sweep directory returned an empty tenant id")
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("controlplane: iterate sweep tenants: %w", err)
	}
	return tenantIDs, nil
}
