package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s *PostgresStore) AppendActivity(ctx context.Context, event ActivityEvent) (*ActivityEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	event = normalizeActivityEvent(event)
	tenantID := event.TenantID
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *ActivityEvent
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		detailsJSON, err := marshalObject(event.Details)
		if err != nil {
			return err
		}
		created, err := scanActivityEvent(tx.QueryRowContext(ctx, `
			INSERT INTO activity_log (
				id, tenant_id, instance_id, stack_id, actor_subject_id, action,
				category, severity, message, details_json, runtime_scope_key,
				server_scope_key, service_scope_key, correlation_id
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6,
				$7, $8, NULLIF($9, ''), $10::jsonb, NULLIF($11, ''),
				NULLIF($12, ''), NULLIF($13, ''), NULLIF($14, '')
			)
			RETURNING id, tenant_id, instance_id, stack_id, actor_subject_id,
				action, category, severity, message, details_json::text,
				runtime_scope_key, server_scope_key, service_scope_key, correlation_id,
				created_at
		`,
			event.ID,
			tenantID,
			event.InstanceID,
			event.StackID,
			event.ActorSubjectID,
			event.Action,
			firstNonEmpty(event.Category, "system"),
			firstNonEmpty(event.Severity, "info"),
			event.Message,
			detailsJSON,
			event.RuntimeScopeKey,
			event.ServerScopeKey,
			event.ServiceScopeKey,
			event.CorrelationID,
		))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListActivity(ctx context.Context, tenantID, stackID string, limit int) ([]ActivityEvent, error) {
	return s.ListActivityScoped(ctx, tenantID, ActivityFilter{StackID: stackID, Limit: limit})
}

func (s *PostgresStore) ListActivityScoped(ctx context.Context, tenantID string, filter ActivityFilter) ([]ActivityEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	filter.StackID = strings.TrimSpace(filter.StackID)
	filter.RuntimeScopeKey = strings.TrimSpace(filter.RuntimeScopeKey)
	filter.ServerScopeKey = strings.TrimSpace(filter.ServerScopeKey)
	filter.ServiceScopeKey = strings.TrimSpace(filter.ServiceScopeKey)
	if filter.Limit <= 0 {
		filter.Limit = defaultActivityLimit
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if !filter.CursorCreatedAt.IsZero() && strings.TrimSpace(filter.CursorID) == "" {
		return nil, fmt.Errorf("controlplane: activity cursor id required")
	}

	var out []ActivityEvent
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var cursor any
		if !filter.CursorCreatedAt.IsZero() {
			cursor = filter.CursorCreatedAt.UTC()
		}
		query := `
			SELECT id, tenant_id, instance_id, stack_id, actor_subject_id,
				action, category, severity, message, details_json::text,
				runtime_scope_key, server_scope_key, service_scope_key, correlation_id,
				created_at
			FROM activity_log
			WHERE tenant_id = $1
				AND ($2 = '' OR stack_id = $2)
				AND ($3 = '' OR runtime_scope_key = $3)
				AND ($4 = '' OR server_scope_key = $4)
				AND ($5 = '' OR service_scope_key = $5)
				AND ($6::timestamptz IS NULL OR (created_at, id) < ($6::timestamptz, $7))
			ORDER BY created_at DESC, id DESC
			LIMIT $8
		`
		args := []any{tenantID, filter.StackID, filter.RuntimeScopeKey, filter.ServerScopeKey, filter.ServiceScopeKey, cursor, filter.CursorID, filter.Limit}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			event, err := scanActivityEvent(rows)
			if err != nil {
				return err
			}
			out = append(out, *event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanActivityEvent(row rowScanner) (*ActivityEvent, error) {
	var event ActivityEvent
	var instanceID, stackID, actorSubjectID, message sql.NullString
	var runtimeScopeKey, serverScopeKey, serviceScopeKey, correlationID sql.NullString
	var detailsJSON []byte
	if err := row.Scan(
		&event.ID,
		&event.TenantID,
		&instanceID,
		&stackID,
		&actorSubjectID,
		&event.Action,
		&event.Category,
		&event.Severity,
		&message,
		&detailsJSON,
		&runtimeScopeKey,
		&serverScopeKey,
		&serviceScopeKey,
		&correlationID,
		&event.CreatedAt,
	); err != nil {
		return nil, err
	}
	event.InstanceID = instanceID.String
	event.StackID = stackID.String
	event.ActorSubjectID = actorSubjectID.String
	event.Message = message.String
	event.RuntimeScopeKey = runtimeScopeKey.String
	event.ServerScopeKey = serverScopeKey.String
	event.ServiceScopeKey = serviceScopeKey.String
	event.CorrelationID = correlationID.String
	if err := decodeObject(detailsJSON, &event.Details); err != nil {
		return nil, err
	}
	return &event, nil
}

var _ ActivityStore = (*PostgresStore)(nil)
