package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *PostgresStore) GetInventoryServer(ctx context.Context, scope InventoryReadScope, serverID string) (*ServerRuntime, error) {
	serverID = strings.TrimSpace(serverID)
	if !inventoryScopeAllowsServerRead(scope, serverID) {
		return nil, ErrInvalidInventoryReadScope
	}
	return queryOneTenantRow(ctx, s, scope.tenantID, `
		SELECT `+serverRuntimeColumns+`
		FROM servers
		WHERE tenant_id = $1 AND ($2 = '' OR owner_subject_id = $2) AND id = $3
			AND (NULLIF(btrim(stack_id), '') IS NULL OR EXISTS (
				SELECT 1 FROM stacks owned_stack
				WHERE owned_stack.tenant_id = servers.tenant_id
					AND owned_stack.id = servers.stack_id
					AND owned_stack.owner_subject_id = servers.owner_subject_id
					AND ($2 = '' OR owned_stack.owner_subject_id = $2)
					AND owned_stack.deleted_at IS NULL
			))
	`, scanServerRuntime, scope.ownerSubjectID, serverID)
}

func (s *PostgresStore) GetInventoryStack(ctx context.Context, scope InventoryReadScope, stackID string) (*Stack, error) {
	stackID = strings.TrimSpace(stackID)
	if stackID == "" || !inventoryScopeAllowsStackRead(scope) {
		return nil, ErrInvalidInventoryReadScope
	}
	return queryOneTenantRow(ctx, s, scope.tenantID, `
		SELECT id, tenant_id, instance_id, owner_subject_id, homelab_id, name, description,
			mode, status, config_json::text, services_json::text,
			runtime_summary_json::text, drift_status, drift_checked_at,
			created_at, updated_at, deleted_at
		FROM stacks
		WHERE tenant_id = $1 AND ($2 = '' OR owner_subject_id = $2) AND id = $3 AND deleted_at IS NULL
			AND ($4 = '' OR EXISTS (
				SELECT 1 FROM servers authorized_server
				WHERE authorized_server.tenant_id = stacks.tenant_id
					AND authorized_server.id = $4
					AND authorized_server.stack_id = stacks.id
					AND authorized_server.owner_subject_id = stacks.owner_subject_id
			))
	`, scanStack, scope.ownerSubjectID, stackID, inventoryScopeBoundServerID(scope))
}

func (s *PostgresStore) ListInventoryServers(ctx context.Context, scope InventoryReadScope, page InventoryPageRequest) (InventoryServerPage, error) {
	if s == nil || s.db == nil {
		return InventoryServerPage{}, fmt.Errorf("controlplane: database not configured")
	}
	if !inventoryScopeAllowsServerCollection(scope) {
		return InventoryServerPage{}, ErrInvalidInventoryReadScope
	}
	page = normalizeInventoryPageRequest(page)
	if page.FrozenEmpty {
		return InventoryServerPage{}, nil
	}
	result := InventoryServerPage{Watermark: page.Watermark}
	err := s.withTenantInventorySnapshot(ctx, scope.tenantID, func(tx *sql.Tx) error {
		const visible = `
			FROM servers
			WHERE tenant_id = $1 AND ($2 = '' OR owner_subject_id = $2)
				AND (NULLIF(btrim(stack_id), '') IS NULL OR EXISTS (
					SELECT 1 FROM stacks owned_stack
					WHERE owned_stack.tenant_id = servers.tenant_id
						AND owned_stack.id = servers.stack_id
						AND owned_stack.owner_subject_id = servers.owner_subject_id
						AND ($2 = '' OR owned_stack.owner_subject_id = $2)
						AND owned_stack.deleted_at IS NULL
				))`
		if result.Watermark.IsZero() {
			var key InventoryPageKey
			err := tx.QueryRowContext(ctx, `SELECT created_at, id `+visible+` ORDER BY created_at DESC, id DESC LIMIT 1`, scope.tenantID, scope.ownerSubjectID).Scan(&key.CreatedAt, &key.ID)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil
			case err != nil:
				return err
			default:
				result.Watermark = inventoryPageKey(key.CreatedAt, key.ID)
			}
		}
		query := `SELECT ` + serverRuntimeColumns + visible + `
			AND (created_at, id) <= ($3, $4)`
		args := []any{scope.tenantID, scope.ownerSubjectID, result.Watermark.CreatedAt, result.Watermark.ID}
		if !page.After.IsZero() {
			query += ` AND (created_at, id) > ($5, $6) ORDER BY created_at, id LIMIT $7`
			args = append(args, page.After.CreatedAt, page.After.ID, page.Limit+1)
		} else {
			query += ` ORDER BY created_at, id LIMIT $5`
			args = append(args, page.Limit+1)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			server, scanErr := scanServerRuntime(rows)
			if scanErr != nil {
				return scanErr
			}
			result.Servers = append(result.Servers, *server)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(result.Servers) > page.Limit {
			result.Servers = result.Servers[:page.Limit]
			next := inventoryPageKey(result.Servers[len(result.Servers)-1].CreatedAt, result.Servers[len(result.Servers)-1].ID)
			result.Next = &next
		}
		return nil
	})
	return result, err
}

// CountInventoryServers applies tenant RLS and owner visibility in SQL so the
// availability result cannot disclose a foreign server collection.
func (s *PostgresStore) CountInventoryServers(ctx context.Context, scope InventoryReadScope) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("controlplane: database not configured")
	}
	if !inventoryScopeAllowsServerCollection(scope) {
		return 0, ErrInvalidInventoryReadScope
	}
	var count int64
	err := s.withTenantInventorySnapshot(ctx, scope.tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM servers
			WHERE tenant_id = $1 AND ($2 = '' OR owner_subject_id = $2)
				AND (NULLIF(btrim(stack_id), '') IS NULL OR EXISTS (
					SELECT 1 FROM stacks owned_stack
					WHERE owned_stack.tenant_id = servers.tenant_id
						AND owned_stack.id = servers.stack_id
						AND owned_stack.owner_subject_id = servers.owner_subject_id
						AND ($2 = '' OR owned_stack.owner_subject_id = $2)
						AND owned_stack.deleted_at IS NULL
				))
		`, scope.tenantID, scope.ownerSubjectID).Scan(&count)
	})
	return count, err
}

const inventoryServiceReadCTE = `
	WITH inventory_services AS (
		SELECT measured.id, measured.tenant_id, measured.instance_id, measured.stack_id,
			measured.server_id, measured.service_key, measured.service_instance, measured.name,
			measured.desired_state, measured.observed_state, measured.health_state,
			measured.management_state,
			measured.observed_at, measured.stackkit_version, measured.access_json::text,
			measured.capabilities_json::text, measured.source, measured.metadata_json::text,
			measured.created_at, measured.updated_at
		FROM services measured
		JOIN servers owned_server
			ON owned_server.tenant_id = measured.tenant_id AND owned_server.id = measured.server_id
			AND ($2 = '' OR owned_server.owner_subject_id = $2)
			AND ($4 = '' OR owned_server.id = $4)
		JOIN stacks owned_stack
			ON owned_stack.tenant_id = measured.tenant_id AND owned_stack.id = measured.stack_id
			AND owned_stack.owner_subject_id = owned_server.owner_subject_id
			AND ($2 = '' OR owned_stack.owner_subject_id = $2) AND owned_stack.deleted_at IS NULL
		WHERE measured.tenant_id = $1 AND measured.server_id IS NOT NULL
			AND ($3 = '' OR measured.server_id = $3)
			AND (NULLIF(btrim(owned_server.stack_id), '') IS NULL OR owned_server.stack_id = measured.stack_id)
			AND (owned_server.inventory_revision = 0 OR (
				COALESCE(measured.metadata_json->>'inventory_revision', '') ~ '^[0-9]+$'
				AND (measured.metadata_json->>'inventory_revision')::bigint = owned_server.inventory_revision
			))
	)
`

func (s *PostgresStore) ListInventoryServices(ctx context.Context, scope InventoryReadScope, serverID string, page InventoryPageRequest) (InventoryServicePage, error) {
	if s == nil || s.db == nil {
		return InventoryServicePage{}, fmt.Errorf("controlplane: database not configured")
	}
	serverID = strings.TrimSpace(serverID)
	if !inventoryScopeAllowsServiceCollection(scope, serverID) {
		return InventoryServicePage{}, ErrInvalidInventoryReadScope
	}
	page = normalizeInventoryPageRequest(page)
	if page.FrozenEmpty {
		return InventoryServicePage{}, nil
	}
	result := InventoryServicePage{Watermark: page.Watermark}
	err := s.withTenantInventorySnapshot(ctx, scope.tenantID, func(tx *sql.Tx) error {
		return listInventoryServicesSnapshot(ctx, tx, scope, serverID, page, &result)
	})
	return result, err
}

func listInventoryServicesSnapshot(ctx context.Context, tx *sql.Tx, scope InventoryReadScope, serverID string, page InventoryPageRequest, result *InventoryServicePage) error {
	baseArgs := []any{scope.tenantID, scope.ownerSubjectID, serverID, inventoryScopeBoundServerID(scope)}
	var projectionPending bool
	if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM servers owned_server
				WHERE owned_server.tenant_id = $1 AND ($2 = '' OR owned_server.owner_subject_id = $2)
					AND ($3 = '' OR owned_server.id = $3)
					AND ($4 = '' OR owned_server.id = $4)
					AND owned_server.inventory_revision > 0
					AND (NULLIF(btrim(owned_server.stack_id), '') IS NULL OR EXISTS (
						SELECT 1 FROM stacks owned_stack
						WHERE owned_stack.tenant_id = owned_server.tenant_id
							AND owned_stack.id = owned_server.stack_id
							AND owned_stack.owner_subject_id = owned_server.owner_subject_id
							AND ($2 = '' OR owned_stack.owner_subject_id = $2)
							AND owned_stack.deleted_at IS NULL
					))
					AND (
						COALESCE(owned_server.metadata_json->>'service_projection_expected', '') !~ '^[0-9]+$'
						OR (owned_server.metadata_json->>'service_projection_expected')::bigint <> (
							SELECT count(*) FROM services projected
							WHERE projected.tenant_id = owned_server.tenant_id
								AND projected.server_id = owned_server.id
								AND (NULLIF(btrim(owned_server.stack_id), '') IS NULL OR projected.stack_id = owned_server.stack_id)
								AND EXISTS (
									SELECT 1 FROM stacks projected_stack
									WHERE projected_stack.tenant_id = projected.tenant_id
										AND projected_stack.id = projected.stack_id
										AND projected_stack.owner_subject_id = owned_server.owner_subject_id
										AND ($2 = '' OR projected_stack.owner_subject_id = $2)
										AND projected_stack.deleted_at IS NULL
								)
								AND COALESCE(projected.metadata_json->>'inventory_revision', '') ~ '^[0-9]+$'
								AND (projected.metadata_json->>'inventory_revision')::bigint = owned_server.inventory_revision
						)
					)
			)
	`, baseArgs...).Scan(&projectionPending); err != nil {
		return err
	}
	if projectionPending {
		return ErrInventoryProjectionPending
	}
	if result.Watermark.IsZero() {
		var key InventoryPageKey
		err := tx.QueryRowContext(ctx, inventoryServiceReadCTE+`
				SELECT created_at, id FROM inventory_services ORDER BY created_at DESC, id DESC LIMIT 1
			`, baseArgs...).Scan(&key.CreatedAt, &key.ID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		default:
			result.Watermark = inventoryPageKey(key.CreatedAt, key.ID)
		}
	}
	query := inventoryServiceReadCTE + `
			SELECT id, tenant_id, instance_id, stack_id, server_id, service_key,
				service_instance, name, desired_state, observed_state, health_state,
				management_state, observed_at, stackkit_version, access_json, capabilities_json,
				source, metadata_json, created_at, updated_at
			FROM inventory_services
			WHERE (created_at, id) <= ($5, $6)`
	args := append(baseArgs, result.Watermark.CreatedAt, result.Watermark.ID)
	if !page.After.IsZero() {
		query += ` AND (created_at, id) > ($7, $8) ORDER BY created_at, id LIMIT $9`
		args = append(args, page.After.CreatedAt, page.After.ID, page.Limit+1)
	} else {
		query += ` ORDER BY created_at, id LIMIT $7`
		args = append(args, page.Limit+1)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		service, scanErr := scanServiceRuntime(rows)
		if scanErr != nil {
			return scanErr
		}
		result.Services = append(result.Services, *service)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(result.Services) > page.Limit {
		result.Services = result.Services[:page.Limit]
		next := inventoryPageKey(result.Services[len(result.Services)-1].CreatedAt, result.Services[len(result.Services)-1].ID)
		result.Next = &next
	}
	return nil
}

// withTenantInventorySnapshot keeps the revision gate, high watermark, and
// page rows on one MVCC snapshot. The general store intentionally uses READ
// COMMITTED for command paths, so inventory owns this read-only boundary.
func (s *PostgresStore) withTenantInventorySnapshot(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
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

var _ InventoryReadStore = (*PostgresStore)(nil)
