package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *PostgresStore) UpsertRILServer(ctx context.Context, server RILServer) (*RILServer, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	healthJSON, err := marshalObject(server.Health)
	if err != nil {
		return nil, err
	}
	inventoryJSON, err := marshalObject(server.Inventory)
	if err != nil {
		return nil, err
	}

	var out *RILServer
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILServer(tx.QueryRowContext(ctx, `
			INSERT INTO ril_servers (
				id, tenant_id, instance_id, stack_id, node_id, name, status,
				health_json, inventory_json, last_seen_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7,
				$8::jsonb, $9::jsonb, $10
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				node_id = EXCLUDED.node_id,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				health_json = EXCLUDED.health_json,
				inventory_json = EXCLUDED.inventory_json,
				last_seen_at = EXCLUDED.last_seen_at,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, node_id, name, status,
				health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		`,
			server.ID, tenantID, server.InstanceID, server.StackID, server.NodeID,
			server.Name, firstNonEmpty(server.Status, "unknown"), healthJSON,
			inventoryJSON, nullableTime(server.LastSeenAt),
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

func (s *PostgresStore) ListRILServersByTenant(ctx context.Context, tenantID string) ([]RILServer, error) {
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, node_id, name, status,
			health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		FROM ril_servers
		WHERE tenant_id = $1
		ORDER BY last_seen_at DESC NULLS LAST, created_at DESC
	`, scanRILServer)
}

func (s *PostgresStore) GetRILServer(ctx context.Context, tenantID, serverID string) (*RILServer, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, node_id, name, status,
			health_json::text, inventory_json::text, last_seen_at, created_at, updated_at
		FROM ril_servers
		WHERE tenant_id = $1 AND (id = $2 OR node_id = $2)
		ORDER BY last_seen_at DESC NULLS LAST
		LIMIT 1
	`, scanRILServer, serverID)
}

func (s *PostgresStore) GetRILCommand(ctx context.Context, tenantID, commandID string) (*RILCommand, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, actor_subject_id, command_class, status,
			request_json::text, result_json::text, error, created_at, updated_at, completed_at
		FROM ril_commands
		WHERE tenant_id = $1 AND id = $2
	`, scanRILCommand, commandID)
}

func (s *PostgresStore) EnqueueRILCommand(ctx context.Context, command RILCommand) (*RILCommand, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(command.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	requestJSON, err := marshalObject(command.Request)
	if err != nil {
		return nil, err
	}
	resultJSON, err := marshalObject(command.Result)
	if err != nil {
		return nil, err
	}

	var out *RILCommand
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILCommand(tx.QueryRowContext(ctx, `
			INSERT INTO ril_commands (
				id, tenant_id, server_id, actor_subject_id, command_class, status,
				request_json, result_json, error, completed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6,
				$7::jsonb, $8::jsonb, NULLIF($9, ''), $10
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				actor_subject_id = EXCLUDED.actor_subject_id,
				command_class = EXCLUDED.command_class,
				status = EXCLUDED.status,
				request_json = EXCLUDED.request_json,
				result_json = EXCLUDED.result_json,
				error = EXCLUDED.error,
				completed_at = EXCLUDED.completed_at,
				updated_at = now()
			RETURNING id, tenant_id, server_id, actor_subject_id, command_class, status,
				request_json::text, result_json::text, error, created_at, updated_at, completed_at
		`,
			command.ID, tenantID, command.ServerID, command.ActorSubjectID,
			command.CommandClass, firstNonEmpty(command.Status, "queued"),
			requestJSON, resultJSON, command.Error, nullableTime(command.CompletedAt),
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

func (s *PostgresStore) UpsertActionCard(ctx context.Context, card RILActionCard) (*RILActionCard, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(card.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	actionJSON, err := marshalObject(card.Action)
	if err != nil {
		return nil, err
	}
	decisionJSON, err := marshalObject(card.Decision)
	if err != nil {
		return nil, err
	}

	var out *RILActionCard
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILActionCard(tx.QueryRowContext(ctx, `
			INSERT INTO ril_action_cards (
				id, tenant_id, server_id, stack_id, title, status, severity,
				action_json, decision_json, resolved_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7,
				$8::jsonb, $9::jsonb, $10
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				stack_id = EXCLUDED.stack_id,
				title = EXCLUDED.title,
				status = EXCLUDED.status,
				severity = EXCLUDED.severity,
				action_json = EXCLUDED.action_json,
				decision_json = EXCLUDED.decision_json,
				resolved_at = EXCLUDED.resolved_at,
				updated_at = now()
			RETURNING id, tenant_id, server_id, stack_id, title, status, severity,
				action_json::text, decision_json::text, created_at, updated_at, resolved_at
		`,
			card.ID, tenantID, card.ServerID, card.StackID, card.Title,
			firstNonEmpty(card.Status, "open"), firstNonEmpty(card.Severity, "info"),
			actionJSON, decisionJSON, nullableTime(card.ResolvedAt),
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

func (s *PostgresStore) RecordHealEvent(ctx context.Context, event RILHealEvent) (*RILHealEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	detailsJSON, err := marshalObject(event.Details)
	if err != nil {
		return nil, err
	}

	var out *RILHealEvent
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanRILHealEvent(tx.QueryRowContext(ctx, `
			INSERT INTO ril_heal_events (
				id, tenant_id, server_id, action_card_id, status, cause, details_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), $7::jsonb
			)
			ON CONFLICT (id) DO UPDATE SET
				server_id = EXCLUDED.server_id,
				action_card_id = EXCLUDED.action_card_id,
				status = EXCLUDED.status,
				cause = EXCLUDED.cause,
				details_json = EXCLUDED.details_json,
				updated_at = now()
			RETURNING id, tenant_id, server_id, action_card_id, status, cause,
				details_json::text, created_at, updated_at
		`,
			event.ID, tenantID, event.ServerID, event.ActionCardID, event.Status,
			event.Cause, detailsJSON,
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

func (s *PostgresStore) ListHealEvents(ctx context.Context, tenantID, serverID string) ([]RILHealEvent, error) {
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, action_card_id, status, cause,
			details_json::text, created_at, updated_at
		FROM ril_heal_events
		WHERE tenant_id = $1 AND ($2 = '' OR server_id = $2)
		ORDER BY created_at DESC
	`, scanRILHealEvent, strings.TrimSpace(serverID))
}

func scanRILServer(row rowScanner) (*RILServer, error) {
	var server RILServer
	var instanceID, stackID, nodeID sql.NullString
	var lastSeenAt sql.NullTime
	var healthJSON, inventoryJSON []byte
	if err := row.Scan(
		&server.ID,
		&server.TenantID,
		&instanceID,
		&stackID,
		&nodeID,
		&server.Name,
		&server.Status,
		&healthJSON,
		&inventoryJSON,
		&lastSeenAt,
		&server.CreatedAt,
		&server.UpdatedAt,
	); err != nil {
		return nil, err
	}
	server.InstanceID = instanceID.String
	server.StackID = stackID.String
	server.NodeID = nodeID.String
	if lastSeenAt.Valid {
		server.LastSeenAt = &lastSeenAt.Time
	}
	if err := decodeObject(healthJSON, &server.Health); err != nil {
		return nil, err
	}
	if err := decodeObject(inventoryJSON, &server.Inventory); err != nil {
		return nil, err
	}
	return &server, nil
}

func scanRILCommand(row rowScanner) (*RILCommand, error) {
	var command RILCommand
	var serverID, actorSubjectID, commandError sql.NullString
	var completedAt sql.NullTime
	var requestJSON, resultJSON []byte
	if err := row.Scan(
		&command.ID,
		&command.TenantID,
		&serverID,
		&actorSubjectID,
		&command.CommandClass,
		&command.Status,
		&requestJSON,
		&resultJSON,
		&commandError,
		&command.CreatedAt,
		&command.UpdatedAt,
		&completedAt,
	); err != nil {
		return nil, err
	}
	command.ServerID = serverID.String
	command.ActorSubjectID = actorSubjectID.String
	command.Error = commandError.String
	if completedAt.Valid {
		command.CompletedAt = &completedAt.Time
	}
	if err := decodeObject(requestJSON, &command.Request); err != nil {
		return nil, err
	}
	if err := decodeObject(resultJSON, &command.Result); err != nil {
		return nil, err
	}
	return &command, nil
}

func scanRILHealEvent(row rowScanner) (*RILHealEvent, error) {
	var event RILHealEvent
	var serverID, actionCardID, cause sql.NullString
	var detailsJSON []byte
	if err := row.Scan(
		&event.ID,
		&event.TenantID,
		&serverID,
		&actionCardID,
		&event.Status,
		&cause,
		&detailsJSON,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, err
	}
	event.ServerID = serverID.String
	event.ActionCardID = actionCardID.String
	event.Cause = cause.String
	if err := decodeObject(detailsJSON, &event.Details); err != nil {
		return nil, err
	}
	return &event, nil
}

func scanRILActionCard(row rowScanner) (*RILActionCard, error) {
	var card RILActionCard
	var serverID, stackID sql.NullString
	var resolvedAt sql.NullTime
	var actionJSON, decisionJSON []byte
	if err := row.Scan(
		&card.ID,
		&card.TenantID,
		&serverID,
		&stackID,
		&card.Title,
		&card.Status,
		&card.Severity,
		&actionJSON,
		&decisionJSON,
		&card.CreatedAt,
		&card.UpdatedAt,
		&resolvedAt,
	); err != nil {
		return nil, err
	}
	card.ServerID = serverID.String
	card.StackID = stackID.String
	if resolvedAt.Valid {
		card.ResolvedAt = &resolvedAt.Time
	}
	if err := decodeObject(actionJSON, &card.Action); err != nil {
		return nil, err
	}
	if err := decodeObject(decisionJSON, &card.Decision); err != nil {
		return nil, err
	}
	return &card, nil
}

var _ RILStore = (*PostgresStore)(nil)
