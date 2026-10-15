package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

func (s *PostgresStore) GetServerMaintenanceImpact(ctx context.Context, tenantID, serverID, action string, at time.Time) (*ServerMaintenanceImpact, error) {
	var impact *ServerMaintenanceImpact
	err := s.withTenant(ctx, strings.TrimSpace(tenantID), func(tx *sql.Tx) error {
		var err error
		impact, err = serverMaintenanceImpactTx(ctx, tx, tenantID, serverID, action, at)
		return err
	})
	return impact, err
}

// The canonical server lock is the Guard inventory transaction's serialization
// boundary. Keep it through maintenance admission/claim, rather than comparing
// an independently read preview immediately before a later write.
func serverMaintenanceImpactTx(ctx context.Context, tx *sql.Tx, tenantID, serverID, action string, at time.Time) (*ServerMaintenanceImpact, error) {
	server, err := scanServerRuntime(tx.QueryRowContext(ctx, `SELECT `+serverRuntimeColumns+`
		FROM servers WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	scope, err := NewOwnerInventoryReadScope(tenantID, server.OwnerSubjectID)
	if err != nil {
		return nil, err
	}
	// Service authority writes lock these same rows. Acquire locks in ID order.
	rows, err := tx.QueryContext(ctx, `SELECT `+serviceRuntimeColumns+` FROM services
		WHERE tenant_id = $1 AND server_id = $2 AND EXISTS (
			SELECT 1 FROM stacks WHERE stacks.tenant_id = services.tenant_id AND stacks.id = services.stack_id
				AND stacks.owner_subject_id = $3 AND stacks.deleted_at IS NULL)
		ORDER BY id LIMIT 501 FOR UPDATE`, tenantID, serverID, server.OwnerSubjectID)
	if err != nil {
		return nil, err
	}
	known := []ServiceRuntime{}
	for rows.Next() {
		var service *ServiceRuntime
		service, err = scanServiceRuntime(rows)
		if err != nil {
			break
		}
		known = append(known, *service)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var observed sql.NullTime
	var snapshotJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT observed_at, inventory_json::text FROM server_inventory_snapshots
		WHERE tenant_id = $1 AND server_id = $2 AND revision = $3 ORDER BY observed_at DESC LIMIT 1`, tenantID, serverID, server.InventoryRevision).Scan(&observed, &snapshotJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var observedAt *time.Time
	if observed.Valid {
		t := observed.Time.UTC()
		observedAt = &t
	}
	page := InventoryServicePage{}
	err = listInventoryServicesSnapshot(ctx, tx, scope, serverID, InventoryPageRequest{Limit: 500}, &page)
	complete := err == nil && page.Next == nil
	var snapshot map[string]any
	if err := decodeObject(snapshotJSON, &snapshot); err != nil {
		return nil, err
	}
	complete = complete && maintenanceDiscoveryComplete(snapshot)
	if err != nil && !errors.Is(err, ErrInventoryProjectionPending) {
		return nil, err
	}
	var consistent bool
	page.Services, consistent = mergeMaintenanceImpactServices(page.Services, known, server.InventoryRevision)
	complete = complete && consistent
	ids := map[string]bool{}
	if server.StackID != "" {
		ids[server.StackID] = true
	}
	for _, service := range page.Services {
		if service.StackID != "" {
			ids[service.StackID] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	stacks := []Stack{}
	for _, id := range ordered {
		var stack Stack
		err := tx.QueryRowContext(ctx, `SELECT id, name FROM stacks
			WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3 AND deleted_at IS NULL FOR SHARE`, tenantID, id, server.OwnerSubjectID).Scan(&stack.ID, &stack.Name)
		if errors.Is(err, sql.ErrNoRows) {
			complete = false
			continue
		}
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, stack)
	}
	return buildServerMaintenanceImpact(*server, action, observedAt, page.Services, stacks, complete, at.UTC()), nil
}
