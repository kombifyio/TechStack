package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func (s *PostgresStore) UpsertServiceRuntime(ctx context.Context, service ServiceRuntime) (*ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(service.TenantID)
	if tenantID == "" || strings.TrimSpace(service.ID) == "" {
		return nil, fmt.Errorf("controlplane: tenant and service id required")
	}
	// Every service state write goes through the aggregate boundary: it owns
	// the canonical vocabulary, the derived legacy status projection, the
	// compare-and-swap revision, and the change-only transition timeline.
	result, err := s.ApplyServiceEvent(ctx, serviceRuntimeObservationEvent(service))
	if err != nil {
		return nil, err
	}
	return result.Service, nil
}

// serviceRuntimeObservationEvent adapts a measured service runtime projection
// to the aggregate command boundary. It is a Guard observation: it may seed
// desired state while the aggregate is created but never overwrites a stored
// intent.
func serviceRuntimeObservationEvent(service ServiceRuntime) ServiceEvent {
	accessURL, _ := service.Access["url"].(string)
	observedAt := time.Time{}
	if service.ObservedAt != nil {
		observedAt = service.ObservedAt.UTC()
	}
	return ServiceEvent{
		TenantID:   strings.TrimSpace(service.TenantID),
		ServiceID:  strings.TrimSpace(service.ID),
		Authority:  ServiceEventAuthorityGuard,
		Source:     firstNonEmpty(strings.TrimSpace(service.Source), "observed"),
		ObservedAt: observedAt,
		Runtime:    service,
		URL:        strings.TrimSpace(accessURL),
	}
}

func (s *PostgresStore) GetServiceRuntime(ctx context.Context, tenantID, serviceID string) (*ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *ServiceRuntime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		service, queryErr := scanServiceRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serviceRuntimeColumns+`
			FROM services WHERE tenant_id = $1 AND id = $2
				AND (server_id IS NOT NULL OR target_kind = 'managed_workload')
		`, tenantID, serviceID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			service, queryErr = scanServiceRuntime(tx.QueryRowContext(ctx, legacyServiceRuntimeBackfillQuery+`
				AND legacy.id = $2
			`, tenantID, serviceID))
			if errors.Is(queryErr, sql.ErrNoRows) {
				return ErrNotFound
			}
		}
		if queryErr != nil {
			return queryErr
		}
		out = service
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListServiceRuntimes(ctx context.Context, tenantID, stackID, serverID string) ([]ServiceRuntime, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	out := make([]ServiceRuntime, 0)
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT `+serviceRuntimeColumns+`
			FROM services
			WHERE tenant_id = $1 AND (server_id IS NOT NULL OR target_kind = 'managed_workload')
				AND ($2 = '' OR stack_id = $2) AND ($3 = '' OR server_id = $3)
			ORDER BY stack_id, service_key, service_instance, id
		`, tenantID, strings.TrimSpace(stackID), strings.TrimSpace(serverID))
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			service, scanErr := scanServiceRuntime(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *service)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		backfillRows, queryErr := tx.QueryContext(ctx, legacyServiceRuntimeBackfillQuery+`
			AND ($2 = '' OR legacy.stack_id = $2) AND ($3 = '' OR mapped_server.id = $3)
			ORDER BY legacy.stack_id, lower(btrim(legacy.service_key)), legacy.id
		`, tenantID, strings.TrimSpace(stackID), strings.TrimSpace(serverID))
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = backfillRows.Close() }()
		for backfillRows.Next() {
			service, scanErr := scanServiceRuntime(backfillRows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *service)
		}
		return backfillRows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StackID != out[j].StackID {
			return out[i].StackID < out[j].StackID
		}
		if out[i].ServiceKey != out[j].ServiceKey {
			return out[i].ServiceKey < out[j].ServiceKey
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

const legacyServiceRuntimeBackfillQuery = `
	SELECT legacy.id, legacy.tenant_id, legacy.instance_id, legacy.stack_id,
		mapped_server.id,
		'server', NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::timestamptz,
		lower(btrim(legacy.service_key)), 'default', legacy.name,
		CASE WHEN lower(btrim(legacy.status)) IN ('stopped', 'exited', 'dead', 'archived', 'decommissioned') THEN 'stopped' ELSE 'running' END,
		'unknown', 'unknown', legacy.management_state, NULL::timestamptz, NULL::text,
		'{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}'::jsonb::text,
		'[]'::jsonb::text, 'legacy-registry-backfill',
		(legacy.metadata_json || jsonb_build_object('backfill', true, 'legacy_status', legacy.status, 'legacy_source', legacy.source))::text,
		legacy.created_at, legacy.updated_at
	FROM services legacy
	JOIN LATERAL (
		SELECT candidate.id
		FROM servers candidate
		WHERE candidate.tenant_id = legacy.tenant_id AND candidate.stack_id = legacy.stack_id
			AND (candidate.id = legacy.node_id OR candidate.node_id = legacy.node_id)
		ORDER BY (candidate.id = legacy.node_id) DESC, candidate.updated_at DESC, candidate.id
		LIMIT 1
	) mapped_server ON true
	WHERE legacy.tenant_id = $1 AND legacy.server_id IS NULL
		AND legacy.node_id IS NOT NULL AND btrim(legacy.node_id) <> '' AND btrim(legacy.service_key) <> ''
		AND NOT EXISTS (
			SELECT 1 FROM services measured
			WHERE measured.tenant_id = legacy.tenant_id AND measured.stack_id = legacy.stack_id
				AND measured.server_id = mapped_server.id
				AND lower(btrim(measured.service_key)) = lower(btrim(legacy.service_key))
				AND lower(btrim(measured.service_instance)) = 'default'
		)
`

func scanServiceRuntime(row rowScanner) (*ServiceRuntime, error) {
	var service ServiceRuntime
	var instanceID, serverID, stackKitVersion sql.NullString
	var targetKind, providerID, managedTargetRef, providerReceiptRef sql.NullString
	var slaPolicyRef, backupPolicyRef, placementEvidenceRef sql.NullString
	var observedAt, placementObservedAt, mutationLockChangedAt sql.NullTime
	var mutationLockReasonCode, mutationLockActor sql.NullString
	var mutationLockState string
	var accessJSON, capabilitiesJSON, metadataJSON []byte
	if err := row.Scan(
		&service.ID, &service.TenantID, &instanceID, &service.StackID, &serverID,
		&targetKind, &providerID, &managedTargetRef, &providerReceiptRef, &slaPolicyRef,
		&backupPolicyRef, &placementEvidenceRef, &placementObservedAt,
		&service.ServiceKey, &service.ServiceInstance, &service.Name, &service.DesiredState,
		&service.ObservedState, &service.HealthState, &service.ManagementState,
		&mutationLockState, &mutationLockReasonCode, &mutationLockActor, &mutationLockChangedAt,
		&observedAt, &stackKitVersion,
		&accessJSON, &capabilitiesJSON, &service.Source, &metadataJSON,
		&service.CreatedAt, &service.UpdatedAt,
	); err != nil {
		return nil, err
	}
	service.InstanceID = instanceID.String
	service.ServerID = serverID.String
	service.Placement = serviceregistry.Placement{
		TargetKind:         serviceregistry.TargetKind(targetKind.String),
		ProviderID:         providerID.String,
		ManagedTargetRef:   managedTargetRef.String,
		ProviderReceiptRef: providerReceiptRef.String,
		SLAPolicyRef:       slaPolicyRef.String,
		BackupPolicyRef:    backupPolicyRef.String,
		EvidenceRef:        placementEvidenceRef.String,
	}
	if placementObservedAt.Valid {
		service.Placement.ObservedAt = &placementObservedAt.Time
	}
	service.Placement = serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	service.StackKitVersion = stackKitVersion.String
	service.MutationLock = ServiceMutationLock{
		State:      serviceregistry.CanonicalMutationLockState(mutationLockState),
		ReasonCode: mutationLockReasonCode.String,
		Actor:      mutationLockActor.String,
	}
	if mutationLockChangedAt.Valid {
		service.MutationLock.ChangedAt = &mutationLockChangedAt.Time
	}
	if observedAt.Valid {
		service.ObservedAt = &observedAt.Time
	}
	if err := decodeObject(accessJSON, &service.Access); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(capabilitiesJSON, &service.Capabilities); err != nil {
		return nil, err
	}
	if err := decodeObject(metadataJSON, &service.Metadata); err != nil {
		return nil, err
	}
	return &service, nil
}

var _ ServiceRuntimeStore = (*PostgresStore)(nil)
