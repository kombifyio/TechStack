package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const serverRuntimeColumns = `id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id,
	lease_id, provider_ref, environment_class, offering, provider_id, provider_target_ref,
	availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
	name, lifecycle_state, desired_state, connection_state,
	health_state, reason_code, connection_changed_at, last_heartbeat_at,
	inventory_revision, revision, generation, source_authority, source_id,
	source_epoch, source_sequence, source_observed_at, channels_json::text, metadata_json::text,
	last_outcome_json::text, outcome_changed_at, decommissioned_at, lifecycle_reason_code, desired_reason_code,
	connection_reason_code, health_reason_code, lifecycle_changed_at,
	desired_changed_at, health_changed_at, created_at, updated_at`

func (s *PostgresStore) UpsertServerRuntime(ctx context.Context, server ServerRuntime) (*ServerRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" || strings.TrimSpace(server.ID) == "" {
		return nil, fmt.Errorf("controlplane: tenant id and server id required")
	}
	targetOmitted := !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget)
	serverRuntimeDefaults(&server, time.Now().UTC())
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	channelsJSON, err := json.Marshal(server.Channels)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalObject(server.Metadata)
	if err != nil {
		return nil, err
	}
	lastOutcomeJSON, outcomeChangedAt, err := marshalServerOutcome(&server)
	if err != nil {
		return nil, err
	}
	var out *ServerRuntime
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			INSERT INTO servers (
				id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id, lease_id,
				provider_ref, environment_class, offering, provider_id, provider_target_ref,
				availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
				name, lifecycle_state, desired_state, connection_state,
				health_state, reason_code, connection_changed_at, last_heartbeat_at,
				inventory_revision, revision, generation, source_authority, source_id,
				source_epoch, source_sequence, source_observed_at, channels_json, metadata_json,
				last_outcome_json, outcome_changed_at, decommissioned_at,
				lifecycle_reason_code, desired_reason_code, connection_reason_code, health_reason_code,
				lifecycle_changed_at, desired_changed_at, health_changed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
				NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), $17,
				$18, $19, $20, $21, $22, NULLIF($23, ''), $24, $25,
				$26, $27, $28, NULLIF($29, ''), NULLIF($30, ''), NULLIF($31, ''), $32, $33,
				$34::jsonb, $35::jsonb, $36::jsonb, $37, $38, NULLIF($39, ''), NULLIF($40, ''),
				NULLIF($41, ''), NULLIF($42, ''), $43, $44, $45
			)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				owner_subject_id = EXCLUDED.owner_subject_id,
				worker_id = EXCLUDED.worker_id,
				node_id = EXCLUDED.node_id,
				lease_id = EXCLUDED.lease_id,
				provider_ref = EXCLUDED.provider_ref,
				environment_class = CASE WHEN $46 THEN servers.environment_class ELSE EXCLUDED.environment_class END,
				offering = CASE WHEN $46 THEN servers.offering ELSE EXCLUDED.offering END,
				provider_id = CASE WHEN $46 THEN servers.provider_id ELSE EXCLUDED.provider_id END,
				provider_target_ref = CASE WHEN $46 THEN servers.provider_target_ref ELSE EXCLUDED.provider_target_ref END,
				availability_owner = CASE WHEN $46 THEN servers.availability_owner ELSE EXCLUDED.availability_owner END,
				operations_owner = CASE WHEN $46 THEN servers.operations_owner ELSE EXCLUDED.operations_owner END,
				runtime_target_evidence_ref = CASE WHEN $46 THEN servers.runtime_target_evidence_ref ELSE EXCLUDED.runtime_target_evidence_ref END,
				runtime_target_observed_at = CASE WHEN $46 THEN servers.runtime_target_observed_at ELSE EXCLUDED.runtime_target_observed_at END,
				name = EXCLUDED.name,
				lifecycle_state = EXCLUDED.lifecycle_state,
				desired_state = EXCLUDED.desired_state,
				connection_state = EXCLUDED.connection_state,
				health_state = EXCLUDED.health_state,
				reason_code = EXCLUDED.reason_code,
				connection_changed_at = EXCLUDED.connection_changed_at,
				last_heartbeat_at = EXCLUDED.last_heartbeat_at,
				inventory_revision = GREATEST(servers.inventory_revision, EXCLUDED.inventory_revision),
				revision = servers.revision + 1,
				generation = GREATEST(servers.generation, EXCLUDED.generation),
				source_authority = COALESCE(EXCLUDED.source_authority, servers.source_authority),
				source_id = COALESCE(EXCLUDED.source_id, servers.source_id),
				source_epoch = COALESCE(EXCLUDED.source_epoch, servers.source_epoch),
				source_sequence = CASE WHEN EXCLUDED.source_epoch IS NULL THEN servers.source_sequence ELSE EXCLUDED.source_sequence END,
				source_observed_at = COALESCE(EXCLUDED.source_observed_at, servers.source_observed_at),
				channels_json = EXCLUDED.channels_json,
				metadata_json = EXCLUDED.metadata_json,
				last_outcome_json = COALESCE(EXCLUDED.last_outcome_json, servers.last_outcome_json),
				outcome_changed_at = COALESCE(EXCLUDED.outcome_changed_at, servers.outcome_changed_at),
				decommissioned_at = EXCLUDED.decommissioned_at,
				lifecycle_reason_code = COALESCE(EXCLUDED.lifecycle_reason_code, servers.lifecycle_reason_code),
				desired_reason_code = COALESCE(EXCLUDED.desired_reason_code, servers.desired_reason_code),
				connection_reason_code = COALESCE(EXCLUDED.connection_reason_code, servers.connection_reason_code),
				health_reason_code = COALESCE(EXCLUDED.health_reason_code, servers.health_reason_code),
				lifecycle_changed_at = EXCLUDED.lifecycle_changed_at,
				desired_changed_at = EXCLUDED.desired_changed_at,
				health_changed_at = EXCLUDED.health_changed_at,
				updated_at = now()
			WHERE servers.tenant_id = EXCLUDED.tenant_id
		RETURNING `+serverRuntimeColumns,
			server.ID, tenantID, server.InstanceID, server.StackID, server.OwnerSubjectID, server.WorkerID, server.NodeID,
			server.LeaseID, server.ProviderRef, string(server.RuntimeTarget.EnvironmentClass), string(server.RuntimeTarget.Offering),
			server.RuntimeTarget.ProviderID, server.RuntimeTarget.ProviderTargetRef,
			string(server.RuntimeTarget.AvailabilityOwner), string(server.RuntimeTarget.OperationsOwner),
			server.RuntimeTarget.EvidenceRef, nullableTime(server.RuntimeTarget.ObservedAt),
			server.Name, server.LifecycleState, server.DesiredState, server.ConnectionState,
			server.HealthState, server.ReasonCode, server.ConnectionChangedAt, nullableTime(server.LastHeartbeatAt),
			server.InventoryRevision, server.Revision, server.Generation, server.SourceAuthority, server.SourceID,
			server.SourceEpoch, server.SourceSequence, nullableTime(server.SourceObservedAt),
			channelsJSON, metadataJSON, lastOutcomeJSON, outcomeChangedAt, nullableTime(server.DecommissionedAt),
			server.LifecycleReasonCode, server.DesiredReasonCode, server.ConnectionReasonCode,
			server.HealthReasonCode, server.LifecycleChangedAt, server.DesiredChangedAt,
			server.HealthChangedAt, targetOmitted,
		))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) EnsureServerRuntimeProjection(ctx context.Context, server ServerRuntime) (*ServerRuntime, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(server.TenantID)
	if tenantID == "" || strings.TrimSpace(server.ID) == "" {
		return nil, false, fmt.Errorf("controlplane: tenant id and server id required")
	}
	serverRuntimeDefaults(&server, time.Now().UTC())
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, false, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	channelsJSON, err := json.Marshal(server.Channels)
	if err != nil {
		return nil, false, err
	}
	metadataJSON, err := marshalObject(server.Metadata)
	if err != nil {
		return nil, false, err
	}
	lastOutcomeJSON, outcomeChangedAt, err := marshalServerOutcome(&server)
	if err != nil {
		return nil, false, err
	}
	var (
		out     *ServerRuntime
		created bool
	)
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			INSERT INTO servers (
				id, tenant_id, instance_id, stack_id, owner_subject_id, worker_id, node_id, lease_id,
				provider_ref, environment_class, offering, provider_id, provider_target_ref,
				availability_owner, operations_owner, runtime_target_evidence_ref, runtime_target_observed_at,
				name, lifecycle_state, desired_state, connection_state,
				health_state, reason_code, connection_changed_at, last_heartbeat_at,
				inventory_revision, revision, generation, source_authority, source_id,
				source_epoch, source_sequence, source_observed_at, channels_json, metadata_json,
				last_outcome_json, outcome_changed_at, decommissioned_at,
				lifecycle_reason_code, desired_reason_code, connection_reason_code, health_reason_code,
				lifecycle_changed_at, desired_changed_at, health_changed_at
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''),
				NULLIF($14, ''), NULLIF($15, ''), NULLIF($16, ''), $17,
				$18, $19, $20, $21, $22, NULLIF($23, ''), $24, $25,
				$26, $27, $28, NULLIF($29, ''), NULLIF($30, ''), NULLIF($31, ''), $32, $33,
				$34::jsonb, $35::jsonb, $36::jsonb, $37, $38, NULLIF($39, ''), NULLIF($40, ''),
				NULLIF($41, ''), NULLIF($42, ''), $43, $44, $45
			)
			ON CONFLICT (id) DO NOTHING
		RETURNING `+serverRuntimeColumns,
			server.ID, tenantID, server.InstanceID, server.StackID, server.OwnerSubjectID, server.WorkerID, server.NodeID,
			server.LeaseID, server.ProviderRef, string(server.RuntimeTarget.EnvironmentClass), string(server.RuntimeTarget.Offering),
			server.RuntimeTarget.ProviderID, server.RuntimeTarget.ProviderTargetRef,
			string(server.RuntimeTarget.AvailabilityOwner), string(server.RuntimeTarget.OperationsOwner),
			server.RuntimeTarget.EvidenceRef, nullableTime(server.RuntimeTarget.ObservedAt),
			server.Name, server.LifecycleState, server.DesiredState, server.ConnectionState,
			server.HealthState, server.ReasonCode, server.ConnectionChangedAt, nullableTime(server.LastHeartbeatAt),
			server.InventoryRevision, server.Revision, server.Generation, server.SourceAuthority, server.SourceID,
			server.SourceEpoch, server.SourceSequence, nullableTime(server.SourceObservedAt),
			channelsJSON, metadataJSON, lastOutcomeJSON, outcomeChangedAt, nullableTime(server.DecommissionedAt),
			server.LifecycleReasonCode, server.DesiredReasonCode, server.ConnectionReasonCode,
			server.HealthReasonCode, server.LifecycleChangedAt, server.DesiredChangedAt,
			server.HealthChangedAt,
		))
		switch {
		case rowErr == nil:
			out = row
			created = true
			return nil
		case !errors.Is(rowErr, sql.ErrNoRows):
			return rowErr
		}
		row, rowErr = scanServerRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serverRuntimeColumns+` FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, server.ID))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

func (s *PostgresStore) GetServerRuntime(ctx context.Context, tenantID, serverID string) (*ServerRuntime, error) {
	if strings.TrimSpace(serverID) == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `SELECT `+serverRuntimeColumns+`
		FROM servers WHERE tenant_id = $1 AND id = $2`, scanServerRuntime, serverID)
}

func (s *PostgresStore) ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]ServerRuntime, error) {
	query := `SELECT ` + serverRuntimeColumns + ` FROM servers WHERE tenant_id = $1`
	args := []any{}
	if strings.TrimSpace(stackID) != "" {
		query += ` AND stack_id = $2`
		args = append(args, strings.TrimSpace(stackID))
	}
	query += ` ORDER BY updated_at DESC, id ASC`
	return queryTenantRows(ctx, s, tenantID, query, scanServerRuntime, args...)
}

func (s *PostgresStore) AppendServerTransition(ctx context.Context, transition ServerStateTransition) (*ServerStateTransition, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(transition.TenantID)
	if tenantID == "" || strings.TrimSpace(transition.ServerID) == "" {
		return nil, fmt.Errorf("controlplane: tenant id and server id required")
	}
	if transition.ObservedAt.IsZero() {
		transition.ObservedAt = time.Now().UTC()
	}
	evidenceJSON, err := marshalObject(transition.Evidence)
	if err != nil {
		return nil, err
	}
	var out *ServerStateTransition
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		row, rowErr := scanServerTransition(tx.QueryRowContext(ctx, `
			INSERT INTO server_state_transitions (
				tenant_id, server_id, dimension, from_state, to_state, reason_code,
				source, observed_at, evidence_json
			) VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, $9::jsonb)
			RETURNING id, tenant_id, server_id, dimension, from_state, to_state,
				reason_code, source, observed_at, evidence_json::text, created_at
		`, tenantID, transition.ServerID, transition.Dimension, transition.FromState,
			transition.ToState, transition.ReasonCode, transition.Source,
			transition.ObservedAt, evidenceJSON))
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServerTransitions(ctx context.Context, tenantID, serverID string, limit int) ([]ServerStateTransition, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, dimension, from_state, to_state,
			reason_code, source, observed_at, evidence_json::text, created_at
		FROM server_state_transitions
		WHERE tenant_id = $1 AND server_id = $2
		ORDER BY observed_at DESC, id DESC LIMIT $3
	`, scanServerTransition, serverID, limit)
}

// ListServerTransitionsSince returns every transition of one dimension for a
// tenant inside a window, oldest first - the input the availability projection
// folds into segments and episodes.
//
// It is bounded like every other list here. A tenant that exceeds the cap gets
// the OLDEST rows, not the newest: the projection needs the window start to
// establish the state it began in, and a truncated tail merely shortens the
// report rather than inventing an outage at the beginning of it.
func (s *PostgresStore) ListServerTransitionsSince(
	ctx context.Context,
	tenantID, dimension string,
	since time.Time,
	limit int,
) ([]ServerStateTransition, error) {
	if limit <= 0 || limit > 20000 {
		limit = 20000
	}
	return queryTenantRows(ctx, s, tenantID, `
		SELECT id, tenant_id, server_id, dimension, from_state, to_state,
			reason_code, source, observed_at, evidence_json::text, created_at
		FROM server_state_transitions
		WHERE tenant_id = $1 AND dimension = $2 AND observed_at >= $3
		ORDER BY observed_at ASC, id ASC LIMIT $4
	`, scanServerTransition, dimension, since.UTC(), limit)
}

func (s *PostgresStore) RecordServerInventory(ctx context.Context, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(snapshot.TenantID)
	if tenantID == "" || strings.TrimSpace(snapshot.ServerID) == "" || snapshot.Revision <= 0 {
		return nil, fmt.Errorf("controlplane: tenant, server, and positive revision required")
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	inventoryJSON, err := marshalObject(snapshot.Inventory)
	if err != nil {
		return nil, err
	}
	var out *ServerInventorySnapshot
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx, `UPDATE servers SET inventory_revision = $3, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND inventory_revision < $3`, tenantID, snapshot.ServerID, snapshot.Revision)
		if execErr != nil {
			return execErr
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrConflict
		}
		row, rowErr := scanServerInventory(tx.QueryRowContext(ctx, `
			INSERT INTO server_inventory_snapshots (
				tenant_id, server_id, revision, source, observed_at, inventory_json
			) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
			RETURNING id, tenant_id, server_id, revision, source, observed_at,
				inventory_json::text, created_at
		`, tenantID, snapshot.ServerID, snapshot.Revision, snapshot.Source,
			snapshot.ObservedAt, inventoryJSON))
		if rowErr != nil {
			return rowErr
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanServerRuntime(row rowScanner) (*ServerRuntime, error) {
	var server ServerRuntime
	var instanceID, stackID, workerID, nodeID, leaseID, providerRef, reasonCode sql.NullString
	var environmentClass, offering, providerID, providerTargetRef sql.NullString
	var availabilityOwner, operationsOwner, runtimeTargetEvidenceRef sql.NullString
	var lifecycleReasonCode, desiredReasonCode, connectionReasonCode, healthReasonCode sql.NullString
	var sourceAuthority, sourceID, sourceEpoch sql.NullString
	var heartbeatAt, sourceObservedAt, outcomeChangedAt, decommissionedAt, runtimeTargetObservedAt sql.NullTime
	var channelsJSON, metadataJSON, lastOutcomeJSON []byte
	if err := row.Scan(
		&server.ID, &server.TenantID, &instanceID, &stackID, &server.OwnerSubjectID, &workerID, &nodeID,
		&leaseID, &providerRef, &environmentClass, &offering, &providerID, &providerTargetRef,
		&availabilityOwner, &operationsOwner, &runtimeTargetEvidenceRef, &runtimeTargetObservedAt,
		&server.Name, &server.LifecycleState,
		&server.DesiredState, &server.ConnectionState, &server.HealthState,
		&reasonCode, &server.ConnectionChangedAt, &heartbeatAt,
		&server.InventoryRevision, &server.Revision, &server.Generation,
		&sourceAuthority, &sourceID, &sourceEpoch, &server.SourceSequence, &sourceObservedAt,
		&channelsJSON, &metadataJSON, &lastOutcomeJSON, &outcomeChangedAt,
		&decommissionedAt, &lifecycleReasonCode, &desiredReasonCode,
		&connectionReasonCode, &healthReasonCode, &server.LifecycleChangedAt,
		&server.DesiredChangedAt, &server.HealthChangedAt, &server.CreatedAt, &server.UpdatedAt,
	); err != nil {
		return nil, err
	}
	server.InstanceID, server.StackID, server.WorkerID, server.NodeID = instanceID.String, stackID.String, workerID.String, nodeID.String
	server.LeaseID, server.ProviderRef, server.ReasonCode = leaseID.String, providerRef.String, reasonCode.String
	server.RuntimeTarget = serverregistry.RuntimeTarget{
		EnvironmentClass:  serverregistry.EnvironmentClass(environmentClass.String),
		Offering:          serverregistry.Offering(offering.String),
		ProviderID:        providerID.String,
		ProviderTargetRef: providerTargetRef.String,
		AvailabilityOwner: serverregistry.AvailabilityOwner(availabilityOwner.String),
		OperationsOwner:   serverregistry.OperationsOwner(operationsOwner.String),
		EvidenceRef:       runtimeTargetEvidenceRef.String,
	}
	if runtimeTargetObservedAt.Valid {
		server.RuntimeTarget.ObservedAt = &runtimeTargetObservedAt.Time
	}
	server.RuntimeTarget = serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	server.LifecycleReasonCode, server.DesiredReasonCode = lifecycleReasonCode.String, desiredReasonCode.String
	server.ConnectionReasonCode, server.HealthReasonCode = connectionReasonCode.String, healthReasonCode.String
	server.SourceAuthority, server.SourceID, server.SourceEpoch = sourceAuthority.String, sourceID.String, sourceEpoch.String
	if heartbeatAt.Valid {
		server.LastHeartbeatAt = &heartbeatAt.Time
	}
	if sourceObservedAt.Valid {
		server.SourceObservedAt = &sourceObservedAt.Time
	}
	if outcomeChangedAt.Valid {
		at := outcomeChangedAt.Time.UTC()
		server.OutcomeChangedAt = &at
	}
	if decommissionedAt.Valid {
		server.DecommissionedAt = &decommissionedAt.Time
	}
	if len(channelsJSON) > 0 {
		if err := json.Unmarshal(channelsJSON, &server.Channels); err != nil {
			return nil, err
		}
	}
	if err := decodeObject(metadataJSON, &server.Metadata); err != nil {
		return nil, err
	}
	if len(lastOutcomeJSON) > 0 && string(lastOutcomeJSON) != "null" {
		var decision outcome.Decision
		if err := json.Unmarshal(lastOutcomeJSON, &decision); err != nil {
			return nil, err
		}
		if err := outcome.Validate(decision); err != nil {
			return nil, err
		}
		server.LastOutcome = outcome.Clone(&decision)
	}
	return &server, nil
}

func marshalServerOutcome(server *ServerRuntime) ([]byte, any, error) {
	if server == nil {
		return nil, nil, nil
	}
	if err := normalizeServerRuntimeOutcome(server, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	if server.LastOutcome == nil {
		return nil, nil, nil
	}
	encoded, err := json.Marshal(server.LastOutcome)
	if err != nil {
		return nil, nil, err
	}
	return encoded, *server.OutcomeChangedAt, nil
}

func scanServerTransition(row rowScanner) (*ServerStateTransition, error) {
	var transition ServerStateTransition
	var fromState, reasonCode sql.NullString
	var evidenceJSON []byte
	if err := row.Scan(
		&transition.ID, &transition.TenantID, &transition.ServerID,
		&transition.Dimension, &fromState, &transition.ToState, &reasonCode,
		&transition.Source, &transition.ObservedAt, &evidenceJSON, &transition.CreatedAt,
	); err != nil {
		return nil, err
	}
	transition.FromState, transition.ReasonCode = fromState.String, reasonCode.String
	if err := decodeObject(evidenceJSON, &transition.Evidence); err != nil {
		return nil, err
	}
	return &transition, nil
}

func scanServerInventory(row rowScanner) (*ServerInventorySnapshot, error) {
	var snapshot ServerInventorySnapshot
	var inventoryJSON []byte
	if err := row.Scan(
		&snapshot.ID, &snapshot.TenantID, &snapshot.ServerID, &snapshot.Revision,
		&snapshot.Source, &snapshot.ObservedAt, &inventoryJSON, &snapshot.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := decodeObject(inventoryJSON, &snapshot.Inventory); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

var _ ServerRuntimeStore = (*PostgresStore)(nil)

var _ ServerRuntimeProjectionStore = (*PostgresStore)(nil)
