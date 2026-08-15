package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) ApplyServerEnrollment(ctx context.Context, command ServerEnrollment) (*ServerEventResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := prepareServerEnrollment(command)
	if err != nil {
		return nil, err
	}
	var result *ServerEventResult
	err = s.withTenant(ctx, prepared.Event.TenantID, func(tx *sql.Tx) error {
		var databaseNow time.Time
		if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
			return err
		}
		if err := ensureEnrollmentNodeTx(ctx, tx, prepared.Node); err != nil {
			return err
		}
		var applyErr error
		result, applyErr = applyServerEventTx(ctx, tx, prepared.Event, databaseNow.UTC())
		return applyErr
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return result, nil
}

func ensureEnrollmentNodeTx(ctx context.Context, tx *sql.Tx, node Node) error {
	var existing Node
	var metadataJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, COALESCE(instance_id, ''), COALESCE(stack_id, ''), COALESCE(worker_id, ''),
			name, role, status, COALESCE(address, ''), metadata_json::text, created_at, updated_at
		FROM nodes WHERE id = $1 FOR UPDATE
	`, node.ID).Scan(&existing.ID, &existing.TenantID, &existing.InstanceID, &existing.StackID, &existing.WorkerID,
		&existing.Name, &existing.Role, &existing.Status, &existing.Address, &metadataJSON, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		return validateExistingEnrollmentNode(existing, node)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	metadata, err := marshalObject(node.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO nodes (id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address, metadata_json)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7, 'pending', NULLIF($8, ''), $9::jsonb)
	`, node.ID, node.TenantID, node.InstanceID, node.StackID, node.WorkerID,
		firstNonEmpty(strings.TrimSpace(node.Name), node.ID), node.Role, node.Address, metadata)
	return err
}

var _ ServerEnrollmentStore = (*PostgresStore)(nil)
