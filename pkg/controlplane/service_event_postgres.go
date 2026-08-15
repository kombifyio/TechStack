package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// serviceRuntimeColumns is the canonical service runtime projection.
const serviceRuntimeColumns = `id, tenant_id, instance_id, stack_id, server_id,
	target_kind, provider_id, managed_target_ref, provider_receipt_ref, sla_policy_ref,
	backup_policy_ref, placement_evidence_ref, placement_observed_at,
	service_key, service_instance, name, desired_state, observed_state, health_state, management_state,
	observed_at, stackkit_version, access_json::text, capabilities_json::text,
	source, metadata_json::text, created_at, updated_at`

// serviceAggregateColumns adds the compare-and-swap fence and the legacy
// compatibility columns of the same physical row.
const serviceAggregateColumns = serviceRuntimeColumns + `, revision, status,
	COALESCE(migration_status, ''), COALESCE(node_id, ''), COALESCE(url, '')`

// ApplyServiceEvent is the single write boundary for service state. It
// serializes one aggregate command under a row lock and commits the head plus
// its change-only transition timeline in one tenant-scoped transaction.
func (s *PostgresStore) ApplyServiceEvent(ctx context.Context, event ServiceEvent) (*ServiceEventResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	if tenantID == "" || strings.TrimSpace(event.ServiceID) == "" {
		return nil, fmt.Errorf("controlplane: service event tenant and service id required")
	}
	var result *ServiceEventResult
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var databaseNow time.Time
		if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
			return fmt.Errorf("controlplane: read service event database time: %w", err)
		}
		var applyErr error
		result, applyErr = applyServiceEventTx(ctx, tx, event, databaseNow.UTC())
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

// applyServiceEventTx applies one aggregate command inside a caller-owned
// transaction that already carries the tenant RLS context. It never commits or
// rolls back, so a projection can bind the service head to other durable rows
// in the same transaction.
func applyServiceEventTx(
	ctx context.Context,
	tx *sql.Tx,
	event ServiceEvent,
	now time.Time,
) (*ServiceEventResult, error) {
	tenantID := strings.TrimSpace(event.TenantID)
	serviceID := strings.TrimSpace(event.ServiceID)
	current, rowErr := scanServiceAggregateHead(tx.QueryRowContext(ctx, `
		SELECT `+serviceAggregateColumns+` FROM services
		WHERE tenant_id = $1 AND id = $2
		FOR UPDATE
	`, tenantID, serviceID))
	if errors.Is(rowErr, sql.ErrNoRows) {
		current = nil
	} else if rowErr != nil {
		return nil, rowErr
	}

	prepared, prepareErr := prepareServiceEvent(current, event, now)
	if prepareErr != nil {
		return nil, prepareErr
	}
	if !prepared.applied {
		head := prepared.head
		return &ServiceEventResult{
			Service:  cloneServiceRuntime(head.Runtime),
			Revision: head.Revision, Status: head.Status, Applied: false,
		}, nil
	}

	persisted, persistErr := persistServiceEventHead(ctx, tx, current, prepared.head)
	if persistErr != nil {
		return nil, persistErr
	}
	transitions := make([]ServiceStateTransition, 0, len(prepared.transitions))
	for _, transition := range prepared.transitions {
		stored, transitionErr := insertServiceTransitionTx(ctx, tx, transition)
		if transitionErr != nil {
			return nil, transitionErr
		}
		transitions = append(transitions, *stored)
	}
	return &ServiceEventResult{
		Service: cloneServiceRuntime(persisted.Runtime), Revision: persisted.Revision,
		Status: persisted.Status, Transitions: transitions, Applied: true,
	}, nil
}

//nolint:gocyclo // The head write enumerates every column of one physical row.
func persistServiceEventHead(
	ctx context.Context,
	tx *sql.Tx,
	current *serviceAggregateHead,
	head serviceAggregateHead,
) (*serviceAggregateHead, error) {
	accessJSON, err := marshalObject(head.Runtime.Access)
	if err != nil {
		return nil, err
	}
	capabilities := head.Runtime.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalObject(head.Runtime.Metadata)
	if err != nil {
		return nil, err
	}
	args := []any{
		head.Runtime.ID, head.Runtime.TenantID, head.Runtime.InstanceID, head.Runtime.StackID,
		head.NodeID, head.Runtime.ServerID, string(head.Runtime.Placement.TargetKind),
		head.Runtime.Placement.ProviderID, head.Runtime.Placement.ManagedTargetRef,
		head.Runtime.Placement.ProviderReceiptRef, head.Runtime.Placement.SLAPolicyRef,
		head.Runtime.Placement.BackupPolicyRef, head.Runtime.Placement.EvidenceRef,
		nullableTime(head.Runtime.Placement.ObservedAt),
		head.Runtime.ServiceKey, head.Runtime.ServiceInstance, head.Runtime.Name, head.Status,
		head.Runtime.Source, head.URL, head.MigrationStatus, metadataJSON,
		head.Runtime.DesiredState, head.Runtime.ObservedState, head.Runtime.HealthState,
		nullableTime(head.Runtime.ObservedAt), head.Runtime.StackKitVersion, accessJSON,
		capabilitiesJSON, head.Revision, head.Runtime.ManagementState,
	}
	if current == nil || !current.Exists {
		stored, rowErr := scanServiceAggregateHead(tx.QueryRowContext(ctx, `
			INSERT INTO services (
				id, tenant_id, instance_id, stack_id, node_id, server_id,
				target_kind, provider_id, managed_target_ref, provider_receipt_ref, sla_policy_ref,
				backup_policy_ref, placement_evidence_ref, placement_observed_at,
				service_key,
				service_instance, name, status, source, url, migration_status, metadata_json,
				desired_state, observed_state, health_state, observed_at, stackkit_version,
				access_json, capabilities_json, revision, management_state
			) VALUES (
				$1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''), $7,
				NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''),
				NULLIF($13, ''), $14, $15, $16, $17, $18, $19, NULLIF($20, ''),
				NULLIF($21, ''), $22::jsonb, $23, $24, $25, $26, NULLIF($27, ''),
				$28::jsonb, $29::jsonb, $30, $31
			)
			ON CONFLICT (id) DO NOTHING
			RETURNING `+serviceAggregateColumns,
			args...,
		))
		if errors.Is(rowErr, sql.ErrNoRows) {
			return nil, ErrConflict
		}
		return stored, rowErr
	}

	args = append(args, current.Revision)
	stored, rowErr := scanServiceAggregateHead(tx.QueryRowContext(ctx, `
		UPDATE services SET
			instance_id = NULLIF($3, ''), stack_id = $4, node_id = NULLIF($5, ''),
			server_id = NULLIF($6, ''), target_kind = $7, provider_id = NULLIF($8, ''),
			managed_target_ref = NULLIF($9, ''), provider_receipt_ref = NULLIF($10, ''),
			sla_policy_ref = NULLIF($11, ''), backup_policy_ref = NULLIF($12, ''),
			placement_evidence_ref = NULLIF($13, ''), placement_observed_at = $14,
			service_key = $15, service_instance = $16, name = $17, status = $18,
			source = $19, url = NULLIF($20, ''), migration_status = NULLIF($21, ''),
			metadata_json = $22::jsonb, desired_state = $23, observed_state = $24,
			health_state = $25, observed_at = $26, stackkit_version = NULLIF($27, ''),
			access_json = $28::jsonb, capabilities_json = $29::jsonb,
			revision = $30, management_state = $31, updated_at = now()
		WHERE tenant_id = $2 AND id = $1 AND revision = $32
		RETURNING `+serviceAggregateColumns,
		args...,
	))
	if errors.Is(rowErr, sql.ErrNoRows) {
		return nil, ErrConflict
	}
	return stored, rowErr
}

func insertServiceTransitionTx(
	ctx context.Context,
	tx *sql.Tx,
	transition ServiceStateTransition,
) (*ServiceStateTransition, error) {
	evidenceJSON, err := marshalObject(transition.Evidence)
	if err != nil {
		return nil, err
	}
	return scanServiceTransition(tx.QueryRowContext(ctx, `
		INSERT INTO service_state_transitions (
			tenant_id, service_id, dimension, from_state, to_state, reason_code,
			source, observed_at, evidence_json
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, $9::jsonb)
		RETURNING id, tenant_id, service_id, dimension, from_state, to_state,
			reason_code, source, observed_at, evidence_json::text, created_at
	`, transition.TenantID, transition.ServiceID, transition.Dimension, transition.FromState,
		transition.ToState, transition.ReasonCode, transition.Source,
		transition.ObservedAt, evidenceJSON))
}

// ListServiceTransitions returns the newest transitions of one service
// aggregate, newest first.
func (s *PostgresStore) ListServiceTransitions(
	ctx context.Context,
	tenantID, serviceID string,
	limit int,
) ([]ServiceStateTransition, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || strings.TrimSpace(serviceID) == "" {
		return nil, fmt.Errorf("controlplane: tenant and service id required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]ServiceStateTransition, 0)
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT id, tenant_id, service_id, dimension, from_state, to_state,
				reason_code, source, observed_at, evidence_json::text, created_at
			FROM service_state_transitions
			WHERE tenant_id = $1 AND service_id = $2
			ORDER BY id DESC
			LIMIT $3
		`, tenantID, strings.TrimSpace(serviceID), limit)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			transition, scanErr := scanServiceTransition(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *transition)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanServiceTransition(row rowScanner) (*ServiceStateTransition, error) {
	var transition ServiceStateTransition
	var fromState, reasonCode sql.NullString
	var evidenceJSON []byte
	if err := row.Scan(
		&transition.ID, &transition.TenantID, &transition.ServiceID, &transition.Dimension,
		&fromState, &transition.ToState, &reasonCode, &transition.Source,
		&transition.ObservedAt, &evidenceJSON, &transition.CreatedAt,
	); err != nil {
		return nil, err
	}
	transition.FromState = fromState.String
	transition.ReasonCode = reasonCode.String
	if err := decodeObject(evidenceJSON, &transition.Evidence); err != nil {
		return nil, err
	}
	return &transition, nil
}

func scanServiceAggregateHead(row rowScanner) (*serviceAggregateHead, error) {
	head := serviceAggregateHead{Exists: true}
	var instanceID, serverID, stackKitVersion sql.NullString
	var targetKind, providerID, managedTargetRef, providerReceiptRef sql.NullString
	var slaPolicyRef, backupPolicyRef, placementEvidenceRef sql.NullString
	var observedAt, placementObservedAt sql.NullTime
	var accessJSON, capabilitiesJSON, metadataJSON []byte
	if err := row.Scan(
		&head.Runtime.ID, &head.Runtime.TenantID, &instanceID, &head.Runtime.StackID, &serverID,
		&targetKind, &providerID, &managedTargetRef, &providerReceiptRef, &slaPolicyRef,
		&backupPolicyRef, &placementEvidenceRef, &placementObservedAt,
		&head.Runtime.ServiceKey, &head.Runtime.ServiceInstance, &head.Runtime.Name,
		&head.Runtime.DesiredState, &head.Runtime.ObservedState, &head.Runtime.HealthState,
		&head.Runtime.ManagementState,
		&observedAt, &stackKitVersion, &accessJSON, &capabilitiesJSON, &head.Runtime.Source,
		&metadataJSON, &head.Runtime.CreatedAt, &head.Runtime.UpdatedAt,
		&head.Revision, &head.Status, &head.MigrationStatus, &head.NodeID, &head.URL,
	); err != nil {
		return nil, err
	}
	head.Runtime.InstanceID = instanceID.String
	head.Runtime.ServerID = serverID.String
	head.Runtime.Placement = serviceregistry.Placement{
		TargetKind:         serviceregistry.TargetKind(targetKind.String),
		ProviderID:         providerID.String,
		ManagedTargetRef:   managedTargetRef.String,
		ProviderReceiptRef: providerReceiptRef.String,
		SLAPolicyRef:       slaPolicyRef.String,
		BackupPolicyRef:    backupPolicyRef.String,
		EvidenceRef:        placementEvidenceRef.String,
	}
	if placementObservedAt.Valid {
		head.Runtime.Placement.ObservedAt = &placementObservedAt.Time
	}
	head.Runtime.Placement = serviceregistry.NormalizePlacement(head.Runtime.ServerID, head.Runtime.Placement)
	head.Runtime.StackKitVersion = stackKitVersion.String
	if observedAt.Valid {
		head.Runtime.ObservedAt = &observedAt.Time
	}
	if err := decodeObject(accessJSON, &head.Runtime.Access); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilitiesJSON, &head.Runtime.Capabilities); err != nil {
		return nil, err
	}
	if err := decodeObject(metadataJSON, &head.Runtime.Metadata); err != nil {
		return nil, err
	}
	return &head, nil
}
