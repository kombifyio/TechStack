package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *PostgresStore) UpsertNode(ctx context.Context, node Node) (*Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(node.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(node.Metadata)
	if err != nil {
		return nil, err
	}

	var out *Node
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanNode(tx.QueryRowContext(ctx, `
			INSERT INTO nodes (
				id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), $10::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				worker_id = EXCLUDED.worker_id,
				name = EXCLUDED.name,
				role = EXCLUDED.role,
				status = EXCLUDED.status,
				address = EXCLUDED.address,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
		`,
			node.ID, tenantID, node.InstanceID, node.StackID, node.WorkerID, node.Name,
			firstNonEmpty(node.Role, "foundation"), firstNonEmpty(node.Status, "pending"),
			node.Address, metadataJSON,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) UpsertService(ctx context.Context, service Service) (*Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(service.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(service.Metadata)
	if err != nil {
		return nil, err
	}
	resolved := resolvedLegacyServiceOwnership(service)

	var out *Service
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanService(tx.QueryRowContext(ctx, `
			INSERT INTO services (
				id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json, management_state
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8,
				$9, NULLIF($10, ''), NULLIF($11, ''), $12::jsonb, $13
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				node_id = EXCLUDED.node_id,
				service_key = EXCLUDED.service_key,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				source = EXCLUDED.source,
				url = EXCLUDED.url,
				migration_status = EXCLUDED.migration_status,
				metadata_json = EXCLUDED.metadata_json,
				management_state = EXCLUDED.management_state,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
		`,
			service.ID, tenantID, service.InstanceID, service.StackID, service.NodeID,
			service.ServiceKey, service.Name, resolved.Status,
			resolved.Source, service.URL, service.MigrationStatus, metadataJSON,
			resolved.ManagementState,
		))
		if err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetNode(ctx context.Context, tenantID, nodeID string) (*Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *Node
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		node, err := scanNode(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
			FROM nodes
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, nodeID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = node
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetService(ctx context.Context, tenantID, serviceID string) (*Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *Service
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		service, err := scanService(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
			FROM services
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, serviceID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = service
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListNodesByStack(ctx context.Context, tenantID, stackID string) ([]Node, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Node
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address,
				metadata_json::text, created_at, updated_at
			FROM nodes
			WHERE tenant_id = $1 AND stack_id = $2
			ORDER BY created_at ASC
		`, tenantID, stackID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			node, err := scanNode(rows)
			if err != nil {
				return err
			}
			out = append(out, *node)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServicesByStack(ctx context.Context, tenantID, stackID string) ([]Service, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Service
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, node_id, service_key, name, status,
				source, url, migration_status, metadata_json::text, management_state,
				created_at, updated_at
			FROM services
			WHERE tenant_id = $1 AND stack_id = $2
			ORDER BY created_at ASC
		`, tenantID, stackID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			service, err := scanService(rows)
			if err != nil {
				return err
			}
			out = append(out, *service)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) DeleteService(ctx context.Context, tenantID, serviceID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM services
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, serviceID)
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

func scanNode(row rowScanner) (*Node, error) {
	var node Node
	var instanceID, workerID, address sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&node.ID,
		&node.TenantID,
		&instanceID,
		&node.StackID,
		&workerID,
		&node.Name,
		&node.Role,
		&node.Status,
		&address,
		&metadataJSON,
		&node.CreatedAt,
		&node.UpdatedAt,
	); err != nil {
		return nil, err
	}
	node.InstanceID = instanceID.String
	node.WorkerID = workerID.String
	node.Address = address.String
	if err := decodeObject(metadataJSON, &node.Metadata); err != nil {
		return nil, err
	}
	return &node, nil
}

func scanService(row rowScanner) (*Service, error) {
	var service Service
	var instanceID, nodeID, url, migrationStatus sql.NullString
	var metadataJSON []byte
	if err := row.Scan(
		&service.ID,
		&service.TenantID,
		&instanceID,
		&service.StackID,
		&nodeID,
		&service.ServiceKey,
		&service.Name,
		&service.Status,
		&service.Source,
		&url,
		&migrationStatus,
		&metadataJSON,
		&service.ManagementState,
		&service.CreatedAt,
		&service.UpdatedAt,
	); err != nil {
		return nil, err
	}
	service.InstanceID = instanceID.String
	service.NodeID = nodeID.String
	service.URL = url.String
	service.MigrationStatus = migrationStatus.String
	if err := decodeObject(metadataJSON, &service.Metadata); err != nil {
		return nil, err
	}
	return &service, nil
}

var _ RegistryStore = (*PostgresStore)(nil)
