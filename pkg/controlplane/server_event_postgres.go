package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ApplyServerEvent serializes one aggregate event under a row lock and commits
// the head, transition timeline, inventory snapshot, and secret-free outbox
// event in one tenant-scoped transaction.
func (s *PostgresStore) ApplyServerEvent(ctx context.Context, event ServerEvent) (*ServerEventResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	serverID := strings.TrimSpace(event.ServerID)
	if tenantID == "" || serverID == "" {
		return nil, fmt.Errorf("controlplane: server event tenant and server id required")
	}

	var result *ServerEventResult
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var databaseNow time.Time
		if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
			return fmt.Errorf("controlplane: read server event database time: %w", err)
		}
		var applyErr error
		result, applyErr = applyServerEventTx(ctx, tx, event, databaseNow.UTC())
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

// ApplyServerEventTx applies an aggregate command inside a caller-owned SQL
// transaction. It sets the tenant RLS context and reads the transition time
// from the same database transaction, but never commits or rolls back. This is
// reserved for application services which must atomically combine registry
// state with another durable authority record, such as provider decommission
// proof finalization.
func (s *PostgresStore) ApplyServerEventTx(
	ctx context.Context,
	tx *sql.Tx,
	event ServerEvent,
) (*ServerEventResult, error) {
	if s == nil || s.db == nil || tx == nil {
		return nil, fmt.Errorf("controlplane: database and transaction required")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	if tenantID == "" || strings.TrimSpace(event.ServerID) == "" {
		return nil, fmt.Errorf("controlplane: server event tenant and server id required")
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return nil, fmt.Errorf("controlplane: set server event tenant context: %w", err)
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
		return nil, fmt.Errorf("controlplane: read server event database time: %w", err)
	}
	return applyServerEventTx(ctx, tx, event, databaseNow.UTC())
}

func applyServerEventTx(
	ctx context.Context,
	tx *sql.Tx,
	event ServerEvent,
	now time.Time,
) (*ServerEventResult, error) {
	tenantID := strings.TrimSpace(event.TenantID)
	serverID := strings.TrimSpace(event.ServerID)
	current, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
		SELECT `+serverRuntimeColumns+` FROM servers
		WHERE tenant_id = $1 AND id = $2
		FOR UPDATE
	`, tenantID, serverID))
	if errors.Is(rowErr, sql.ErrNoRows) {
		current = nil
	} else if rowErr != nil {
		return nil, rowErr
	}

	sourceEpochSeen, epochErr := serverGuardEpochSeenTx(ctx, tx, current, event)
	if epochErr != nil {
		return nil, epochErr
	}
	prepared, prepareErr := prepareServerEvent(current, event, now, sourceEpochSeen)
	if prepareErr != nil {
		return nil, prepareErr
	}
	if !prepared.applied {
		return &ServerEventResult{Server: cloneServerRuntime(prepared.server), Applied: false}, nil
	}

	persisted, persistErr := persistServerEventHead(ctx, tx, current, prepared.server)
	if persistErr != nil {
		return nil, persistErr
	}
	transitions := make([]ServerStateTransition, 0, len(prepared.transitions))
	for _, transition := range prepared.transitions {
		stored, transitionErr := insertServerTransitionTx(ctx, tx, transition)
		if transitionErr != nil {
			return nil, transitionErr
		}
		transitions = append(transitions, *stored)
	}
	var inventory *ServerInventorySnapshot
	if prepared.inventory != nil {
		stored, inventoryErr := insertServerInventoryTx(ctx, tx, *prepared.inventory)
		if inventoryErr != nil {
			return nil, inventoryErr
		}
		inventory = stored
	}
	if event.Authority == ServerEventAuthorityGuard {
		if epochErr := recordServerGuardEpochTx(ctx, tx, event); epochErr != nil {
			return nil, epochErr
		}
	}
	outbox, outboxErr := insertServerRegistryOutboxTx(ctx, tx, *prepared.outbox)
	if outboxErr != nil {
		return nil, outboxErr
	}
	return &ServerEventResult{
		Server: persisted, Transitions: transitions, Inventory: inventory, Outbox: outbox, Applied: true,
	}, nil
}

func serverGuardEpochSeenTx(ctx context.Context, tx *sql.Tx, current *ServerRuntime, event ServerEvent) (bool, error) {
	if event.Authority != ServerEventAuthorityGuard || current == nil ||
		strings.TrimSpace(current.SourceEpoch) == "" ||
		strings.TrimSpace(event.SourceEpoch) == strings.TrimSpace(current.SourceEpoch) {
		return false, nil
	}
	var seen bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM server_guard_source_epochs
			WHERE tenant_id = $1 AND server_id = $2 AND generation = $3
				AND source_id = $4 AND source_epoch = $5
		)
	`, strings.TrimSpace(event.TenantID), strings.TrimSpace(event.ServerID), event.Generation,
		strings.TrimSpace(event.SourceID), strings.TrimSpace(event.SourceEpoch)).Scan(&seen)
	return seen, err
}

func recordServerGuardEpochTx(ctx context.Context, tx *sql.Tx, event ServerEvent) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO server_guard_source_epochs (
			tenant_id, server_id, generation, source_id, source_epoch,
			first_observed_at, last_observed_at, last_sequence
		) VALUES ($1, $2, $3, $4, $5, $6, $6, $7)
		ON CONFLICT (tenant_id, server_id, generation, source_id, source_epoch)
		DO UPDATE SET
			last_observed_at = GREATEST(server_guard_source_epochs.last_observed_at, EXCLUDED.last_observed_at),
			last_sequence = GREATEST(server_guard_source_epochs.last_sequence, EXCLUDED.last_sequence)
	`, strings.TrimSpace(event.TenantID), strings.TrimSpace(event.ServerID), event.Generation,
		strings.TrimSpace(event.SourceID), strings.TrimSpace(event.SourceEpoch), event.ObservedAt.UTC(), event.SourceSequence)
	return err
}

func insertServerRegistryOutboxTx(ctx context.Context, tx *sql.Tx, item ServerRegistryOutboxItem) (*ServerRegistryOutboxItem, error) {
	payloadJSON, err := marshalObject(item.Payload)
	if err != nil {
		return nil, err
	}
	stored := &ServerRegistryOutboxItem{}
	var storedPayload string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO server_registry_outbox (
			tenant_id, server_id, aggregate_revision, generation, authority,
			source, event_type, payload_json, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
		RETURNING id, tenant_id, server_id, aggregate_revision, generation,
			authority, source, event_type, payload_json::text, occurred_at,
			created_at, published_at, attempts
	`, item.TenantID, item.ServerID, item.AggregateRevision, item.Generation,
		item.Authority, item.Source, item.EventType, payloadJSON, item.OccurredAt).Scan(
		&stored.ID, &stored.TenantID, &stored.ServerID, &stored.AggregateRevision,
		&stored.Generation, &stored.Authority, &stored.Source, &stored.EventType,
		&storedPayload, &stored.OccurredAt, &stored.CreatedAt, &stored.PublishedAt,
		&stored.Attempts,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(storedPayload), &stored.Payload); err != nil {
		return nil, err
	}
	return stored, nil
}

func persistServerEventHead(ctx context.Context, tx *sql.Tx, current *ServerRuntime, server ServerRuntime) (*ServerRuntime, error) {
	channelsJSON, err := json.Marshal(server.Channels)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalObject(server.Metadata)
	if err != nil {
		return nil, err
	}
	args := []any{
		server.ID, server.TenantID, server.InstanceID, server.StackID, server.OwnerSubjectID,
		server.WorkerID, server.NodeID, server.LeaseID, server.ProviderRef,
		string(server.RuntimeTarget.EnvironmentClass), string(server.RuntimeTarget.Offering),
		server.RuntimeTarget.ProviderID, server.RuntimeTarget.ProviderTargetRef,
		string(server.RuntimeTarget.AvailabilityOwner), string(server.RuntimeTarget.OperationsOwner),
		server.RuntimeTarget.EvidenceRef, nullableTime(server.RuntimeTarget.ObservedAt),
		server.Name, server.LifecycleState, server.DesiredState, server.ConnectionState,
		server.HealthState, server.ReasonCode, server.ConnectionChangedAt, nullableTime(server.LastHeartbeatAt),
		server.InventoryRevision, server.Revision, server.Generation, server.SourceAuthority,
		server.SourceID, server.SourceEpoch, server.SourceSequence, nullableTime(server.SourceObservedAt),
		channelsJSON, metadataJSON, nullableTime(server.DecommissionedAt),
		server.LifecycleReasonCode, server.DesiredReasonCode, server.ConnectionReasonCode,
		server.HealthReasonCode, server.LifecycleChangedAt, server.DesiredChangedAt,
		server.HealthChangedAt,
	}
	if current == nil {
		stored, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			INSERT INTO servers (
				id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id, lease_id,
				provider_ref, environment_class, offering, provider_id, provider_target_ref,
				availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
				name, lifecycle_state, desired_state, connection_state, health_state,
				reason_code, connection_changed_at, last_heartbeat_at, inventory_revision,
				revision, generation, source_authority, source_id, source_epoch, source_sequence,
				source_observed_at, channels_json, metadata_json, decommissioned_at,
				lifecycle_reason_code, desired_reason_code, connection_reason_code, health_reason_code,
				lifecycle_changed_at, desired_changed_at, health_changed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
				NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), $17,
				$18, $19, $20, $21, $22, NULLIF($23, ''), $24, $25,
				$26, $27, $28, NULLIF($29, ''), NULLIF($30, ''), NULLIF($31, ''),
				$32, $33, $34::jsonb, $35::jsonb, $36, NULLIF($37, ''), NULLIF($38, ''),
				NULLIF($39, ''), NULLIF($40, ''), $41, $42, $43
			)
			ON CONFLICT (id) DO NOTHING
			RETURNING `+serverRuntimeColumns,
			args...,
		))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return nil, ErrConflict
		}
		return stored, rowErr
	}

	args = append(args, current.Revision)
	stored, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
		UPDATE servers SET
			instance_id = NULLIF($3, ''), stack_id = NULLIF($4, ''), owner_subject_id = $5,
			worker_id = NULLIF($6, ''), node_id = NULLIF($7, ''), lease_id = NULLIF($8, ''),
			provider_ref = NULLIF($9, ''), environment_class = $10, offering = NULLIF($11, ''),
			provider_id = NULLIF($12, ''), provider_target_ref = NULLIF($13, ''),
			availability_owner = NULLIF($14, ''), operations_owner = NULLIF($15, ''),
			runtime_target_evidence_ref = NULLIF($16, ''), runtime_target_observed_at = $17,
			name = $18, lifecycle_state = $19, desired_state = $20, connection_state = $21, health_state = $22,
			reason_code = NULLIF($23, ''), connection_changed_at = $24, last_heartbeat_at = $25,
			inventory_revision = $26, revision = $27, generation = $28,
			source_authority = NULLIF($29, ''), source_id = NULLIF($30, ''),
			source_epoch = NULLIF($31, ''), source_sequence = $32, source_observed_at = $33,
			channels_json = $34::jsonb, metadata_json = $35::jsonb, decommissioned_at = $36,
			lifecycle_reason_code = NULLIF($37, ''), desired_reason_code = NULLIF($38, ''),
			connection_reason_code = NULLIF($39, ''), health_reason_code = NULLIF($40, ''),
			lifecycle_changed_at = $41, desired_changed_at = $42, health_changed_at = $43,
			updated_at = now()
		WHERE tenant_id = $2 AND id = $1 AND revision = $44
		RETURNING `+serverRuntimeColumns,
		args...,
	))
	if errors.Is(rowErr, sql.ErrNoRows) {
		return nil, ErrConflict
	}
	return stored, rowErr
}

func insertServerTransitionTx(ctx context.Context, tx *sql.Tx, transition ServerStateTransition) (*ServerStateTransition, error) {
	evidenceJSON, err := marshalObject(transition.Evidence)
	if err != nil {
		return nil, err
	}
	return scanServerTransition(tx.QueryRowContext(ctx, `
		INSERT INTO server_state_transitions (
			tenant_id, server_id, dimension, from_state, to_state, reason_code,
			source, observed_at, evidence_json
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, $9::jsonb)
		RETURNING id, tenant_id, server_id, dimension, from_state, to_state,
			reason_code, source, observed_at, evidence_json::text, created_at
	`, transition.TenantID, transition.ServerID, transition.Dimension, transition.FromState,
		transition.ToState, transition.ReasonCode, transition.Source,
		transition.ObservedAt, evidenceJSON))
}

func insertServerInventoryTx(ctx context.Context, tx *sql.Tx, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error) {
	inventoryJSON, err := marshalObject(snapshot.Inventory)
	if err != nil {
		return nil, err
	}
	return scanServerInventory(tx.QueryRowContext(ctx, `
		INSERT INTO server_inventory_snapshots (
			tenant_id, server_id, revision, source, observed_at, inventory_json
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING id, tenant_id, server_id, revision, source, observed_at,
			inventory_json::text, created_at
	`, snapshot.TenantID, snapshot.ServerID, snapshot.Revision, snapshot.Source,
		snapshot.ObservedAt, inventoryJSON))
}
