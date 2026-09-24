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

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	guardInventoryMigrationUnavailableJSON = `{"mode":"unavailable","reason_code":"migration_in_progress"}`
	guardInventoryArchivedUnavailableJSON  = `{"mode":"unavailable","reason_code":"service_archived"}`
	guardInventoryNoEvidenceJSON           = `{"mode":"unavailable","reason_code":"inventory_evidence_missing"}`
)

// ApplyGuardInventoryProjection commits one complete Guard observation. The
// aggregate row lock acquired by applyServerEventTx also serializes the node,
// service, and authoritative-prune projections in this tenant transaction.
func (s *PostgresStore) ApplyGuardInventoryProjection(
	ctx context.Context,
	command GuardInventoryProjection,
) (*GuardInventoryProjectionResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := prepareGuardInventoryProjection(command)
	if err != nil {
		return nil, err
	}
	result, err := s.applyGuardInventoryProjection(ctx, prepared)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return result, nil
}

func (s *PostgresStore) applyGuardInventoryProjection(
	ctx context.Context,
	prepared *preparedGuardInventoryProjection,
) (*GuardInventoryProjectionResult, error) {
	tenantID := prepared.command.Event.TenantID
	var result *GuardInventoryProjectionResult
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var applyErr error
		result, applyErr = applyGuardInventoryProjectionTx(ctx, tx, prepared, tenantID, s.serverEventProjector)
		return applyErr
	})
	return result, err
}

func applyGuardInventoryProjectionTx(
	ctx context.Context,
	tx *sql.Tx,
	prepared *preparedGuardInventoryProjection,
	tenantID string,
	projector ServerEventProjector,
) (*GuardInventoryProjectionResult, error) {
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
		return nil, fmt.Errorf("controlplane: read Guard inventory database time: %w", err)
	}
	current, err := scanServerRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serverRuntimeColumns+` FROM servers
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, prepared.command.Event.ServerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: Guard inventory requires a canonical server", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	if err := validateGuardInventoryCanonicalServer(*current, prepared.command); err != nil {
		return nil, err
	}
	retainedServices := guardInventoryRetainedServices{}
	if !guardInventoryProjectionFenced(*current, prepared.command.Event) {
		retainedServices, err = listGuardInventoryRetainedServicesTx(ctx, tx, prepared.command)
		if err != nil {
			return nil, err
		}
	}
	observation := guardInventoryServerObservationEventWithExpectedServices(
		prepared.command.Event, int64(len(prepared.command.Services)+len(retainedServices.IDs)),
	)
	serverEvent, err := applyServerEventTx(ctx, tx, observation, databaseNow.UTC(), projector)
	if err != nil {
		return nil, err
	}
	result := &GuardInventoryProjectionResult{ServerEvent: serverEvent}
	if serverEvent == nil || serverEvent.Server == nil {
		return nil, fmt.Errorf("%w: Guard inventory aggregate result is missing", ErrConflict)
	}
	if !serverEvent.Applied {
		snapshot, replayed, replayErr := guardInventoryReplayTx(ctx, tx, prepared, *serverEvent.Server)
		if replayed {
			serverEvent.Inventory = snapshot
		}
		result.Replayed = replayed
		return result, replayErr
	}
	if serverEvent.Inventory == nil || serverEvent.Inventory.Revision <= 0 ||
		serverEvent.Server.InventoryRevision != serverEvent.Inventory.Revision {
		return nil, fmt.Errorf("%w: accepted Guard inventory revision is unavailable", ErrConflict)
	}

	projection := guardInventoryProjectionAtRevision(prepared, serverEvent.Inventory.Revision)
	if err := upsertGuardInventoryNodeTx(ctx, tx, projection.Node); err != nil {
		return nil, err
	}
	for _, service := range projection.Services {
		// Provenance is per row: normalizeGuardInventoryServiceProjection has
		// already resolved and validated it, defaulting to the batch source.
		if err := upsertGuardInventoryServiceTx(
			ctx, tx, service, service.Runtime.Source, databaseNow.UTC(),
		); err != nil {
			return nil, err
		}
	}
	managedServiceIDs := guardInventoryServiceIDsBySource(projection, projection.ServiceSource)
	if projection.ManifestObserved {
		// Absence of service evidence is not evidence of absence: a manifest
		// that exists but reports no services (e.g. written by a failed or
		// partial apply) must not delete observed history. Only a manifest
		// with at least one observed service may prune stale rows.
		if len(managedServiceIDs) > 0 {
			if err := pruneGuardInventoryServicesTx(
				ctx, tx, tenantID, projection.Event.Runtime.StackID,
				projection.Event.ServerID, projection.ServiceSource, managedServiceIDs,
				serverEvent.Inventory.Revision,
			); err != nil {
				return nil, err
			}
		} else if err := markGuardInventoryServicesUnavailableTx(
			ctx, tx, tenantID, projection.Event.Runtime.StackID,
			projection.Event.ServerID, projection.ServiceSource,
			serverEvent.Inventory.Revision,
		); err != nil {
			return nil, err
		}
	}
	if len(retainedServices.DiscoveredIDs) > 0 {
		if guardInventoryDiscoveryCanReconcile(projection) {
			if err := markGuardDiscoveredServicesAbsentTx(
				ctx, tx, projection, retainedServices.DiscoveredIDs,
				serverEvent.Inventory.Revision, databaseNow.UTC(),
			); err != nil {
				return nil, err
			}
		}
		if err := stampGuardInventoryServicesRevisionTx(
			ctx, tx, tenantID, projection.Event.Runtime.StackID,
			projection.Event.ServerID, serviceSourceObserved, retainedServices.DiscoveredIDs,
			serverEvent.Inventory.Revision,
		); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func guardInventoryReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	prepared *preparedGuardInventoryProjection,
	current ServerRuntime,
) (*ServerInventorySnapshot, bool, error) {
	if current.LifecycleState == string(serverregistry.LifecycleDecommissioning) ||
		current.LifecycleState == string(serverregistry.LifecycleDecommissioned) {
		return nil, false, nil
	}
	if !guardInventoryProjectionAtCurrentPosition(current, prepared.command.Event) {
		return nil, false, nil
	}
	if current.InventoryRevision <= 0 {
		return nil, false, fmt.Errorf("%w: exact Guard replay has no durable inventory revision", ErrConflict)
	}
	snapshot, err := scanServerInventory(tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, server_id, revision, source, observed_at,
			inventory_json::text, created_at
		FROM server_inventory_snapshots
		WHERE tenant_id = $1 AND server_id = $2 AND revision = $3
	`, current.TenantID, current.ID, current.InventoryRevision))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: exact Guard replay snapshot is missing", ErrConflict)
	}
	if err != nil {
		return nil, false, err
	}
	digest, ok := guardInventoryProjectionDigestFromInventory(snapshot.Inventory)
	if !ok || digest != prepared.digest {
		return nil, false, fmt.Errorf("%w: Guard source position already contains a different projection", ErrConflict)
	}
	return snapshot, true, nil
}

func upsertGuardInventoryNodeTx(ctx context.Context, tx *sql.Tx, node Node) error {
	metadataJSON, err := marshalObject(node.Metadata)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO nodes (
			id, tenant_id, instance_id, stack_id, worker_id, name, role, status, address, metadata_json
		) VALUES (
			$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, NULLIF($9, ''), $10::jsonb
		)
		ON CONFLICT (id) DO UPDATE SET
			stack_id = COALESCE(nodes.stack_id, EXCLUDED.stack_id),
			status = EXCLUDED.status,
			address = EXCLUDED.address,
			metadata_json = EXCLUDED.metadata_json,
			updated_at = now()
		WHERE nodes.tenant_id = EXCLUDED.tenant_id
			AND (nodes.stack_id IS NULL OR nodes.stack_id = EXCLUDED.stack_id)
			AND (nodes.instance_id IS NULL OR nodes.instance_id = EXCLUDED.instance_id)
			AND (nodes.worker_id IS NULL OR nodes.worker_id = EXCLUDED.worker_id)
	`, node.ID, node.TenantID, node.InstanceID, node.StackID, node.WorkerID,
		firstNonEmpty(node.Name, node.ID), firstNonEmpty(node.Role, "foundation"),
		firstNonEmpty(node.Status, "pending"), node.Address, metadataJSON)
	if err != nil {
		return err
	}
	return requireTenantBoundUpsert(result, "Guard inventory node")
}

// upsertGuardInventoryServiceTx routes one observed service through the service
// aggregate boundary. The boundary owns the canonical state vocabulary, the
// derived legacy status projection, the compare-and-swap revision, and the
// change-only transition timeline; nothing here writes a state column directly.
func upsertGuardInventoryServiceTx(
	ctx context.Context,
	tx *sql.Tx,
	projection GuardInventoryServiceProjection,
	serviceSource string,
	now time.Time,
) error {
	_, err := applyServiceEventTx(ctx, tx, guardInventoryServiceEvent(projection, serviceSource), now)
	return err
}

// guardInventoryServiceEvent binds the legacy compatibility row and the
// measured runtime row of one Guard service observation into a single
// authority-labeled command against the shared physical row.
func guardInventoryServiceEvent(
	projection GuardInventoryServiceProjection,
	serviceSource string,
) ServiceEvent {
	legacy, runtime := projection.Legacy, projection.Runtime
	patch := *cloneServiceRuntime(runtime)
	patch.InstanceID = firstNonEmpty(runtime.InstanceID, legacy.InstanceID)
	patch.Name = firstNonEmpty(runtime.Name, legacy.Name, runtime.ServiceKey)
	patch.Source = serviceSource
	patch.Metadata = mergeMaps(legacy.Metadata, runtime.Metadata)
	if patch.Capabilities == nil {
		patch.Capabilities = []string{}
	}
	if patch.Access == nil {
		patch.Access = map[string]any{}
	}
	accessURL, _ := runtime.Access["url"].(string)
	observedAt := time.Time{}
	if runtime.ObservedAt != nil {
		observedAt = runtime.ObservedAt.UTC()
	}
	return ServiceEvent{
		TenantID:               legacy.TenantID,
		ServiceID:              legacy.ID,
		Authority:              ServiceEventAuthorityGuard,
		Source:                 serviceSource,
		ObservedAt:             observedAt,
		Runtime:                patch,
		NodeID:                 legacy.NodeID,
		URL:                    firstNonEmpty(strings.TrimSpace(accessURL), legacy.URL),
		MigrationStatus:        legacy.MigrationStatus,
		EnforceIdentityBinding: true,
	}
}

func listGuardInventoryRetainedServicesTx(
	ctx context.Context,
	tx *sql.Tx,
	command GuardInventoryProjection,
) (guardInventoryRetainedServices, error) {
	managed := guardInventoryServiceIDsBySource(command, command.ServiceSource)
	discovered := guardInventoryServiceIDsBySource(command, serviceSourceObserved)
	managedJSON, err := json.Marshal(managed)
	if err != nil {
		return guardInventoryRetainedServices{}, err
	}
	discoveredJSON, err := json.Marshal(discovered)
	if err != nil {
		return guardInventoryRetainedServices{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, lower(btrim(source)) FROM services
		WHERE tenant_id = $1 AND stack_id = $2
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND (
				(
					$6::boolean
					AND lower(btrim(source)) = lower(btrim($4))
					AND NOT EXISTS (
						SELECT 1 FROM jsonb_array_elements_text($5::jsonb) AS managed(service_id)
						WHERE managed.service_id = services.id
					)
					AND (
						NOT $7::boolean OR
						lower(btrim(COALESCE(migration_status, ''))) IN ('migrating', 'deploying', 'pending_verification', 'archived') OR
						lower(btrim(status)) IN ('migrating', 'deploying', 'pending_verification', 'archived')
					)
				) OR (
					lower(btrim(source)) = lower(btrim($8))
					AND NOT EXISTS (
						SELECT 1 FROM jsonb_array_elements_text($9::jsonb) AS discovered(service_id)
						WHERE discovered.service_id = services.id
					)
				)
			)
		ORDER BY id
		FOR UPDATE
	`, command.Event.TenantID, command.Event.Runtime.StackID, command.Event.ServerID,
		command.ServiceSource, managedJSON, command.ManifestObserved, len(managed) > 0,
		serviceSourceObserved, discoveredJSON)
	if err != nil {
		return guardInventoryRetainedServices{}, err
	}
	defer rows.Close()
	retained := guardInventoryRetainedServices{}
	for rows.Next() {
		var id, source string
		if err := rows.Scan(&id, &source); err != nil {
			return guardInventoryRetainedServices{}, err
		}
		retained.IDs = append(retained.IDs, id)
		if strings.EqualFold(source, serviceSourceObserved) {
			retained.DiscoveredIDs = append(retained.DiscoveredIDs, id)
		}
	}
	return retained, rows.Err()
}

func markGuardDiscoveredServicesAbsentTx(
	ctx context.Context,
	tx *sql.Tx,
	command GuardInventoryProjection,
	serviceIDs []string,
	inventoryRevision int64,
	now time.Time,
) error {
	for _, serviceID := range serviceIDs {
		event := guardDiscoveredServiceAbsentEvent(command, serviceID)
		event.Evidence[guardInventoryRevisionKey] = inventoryRevision
		if _, err := applyServiceEventTx(ctx, tx, event, now); err != nil {
			return err
		}
	}
	return nil
}

func stampGuardInventoryServicesRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, stackID, serverID, serviceSource string,
	serviceIDs []string,
	inventoryRevision int64,
) error {
	if len(serviceIDs) == 0 {
		return nil
	}
	ids := append([]string(nil), serviceIDs...)
	sort.Strings(ids)
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE services SET
			metadata_json = COALESCE(metadata_json, '{}'::jsonb) || jsonb_build_object('inventory_revision', $6::bigint)
		WHERE tenant_id = $1 AND stack_id = $2
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND lower(btrim(source)) = lower(btrim($4))
			AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text($5::jsonb) AS retained(service_id)
				WHERE retained.service_id = services.id
			)
	`, tenantID, stackID, serverID, serviceSource, idsJSON, inventoryRevision)
	return err
}

func pruneGuardInventoryServicesTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, stackID, serverID, serviceSource string,
	observedServiceIDs []string,
	inventoryRevision int64,
) error {
	ids := append([]string(nil), observedServiceIDs...)
	sort.Strings(ids)
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE services SET
			server_id = $3,
			url = NULL,
			metadata_json = COALESCE(metadata_json, '{}'::jsonb) || jsonb_build_object('inventory_revision', $6::bigint),
			access_json = CASE WHEN
				lower(btrim(COALESCE(migration_status, ''))) = 'archived' OR lower(btrim(status)) = 'archived'
				THEN '`+guardInventoryArchivedUnavailableJSON+`'::jsonb
				ELSE '`+guardInventoryMigrationUnavailableJSON+`'::jsonb END,
			updated_at = now()
		WHERE tenant_id = $1 AND stack_id = $2
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND lower(btrim(source)) = lower(btrim($4))
			AND (
				lower(btrim(COALESCE(migration_status, ''))) IN ('migrating', 'deploying', 'pending_verification', 'archived') OR
				lower(btrim(status)) IN ('migrating', 'deploying', 'pending_verification', 'archived')
			)
			AND NOT EXISTS (
				SELECT 1 FROM jsonb_array_elements_text($5::jsonb) AS observed(service_id)
				WHERE observed.service_id = services.id
			)
	`, tenantID, stackID, serverID, serviceSource, idsJSON, inventoryRevision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		DELETE FROM services
		WHERE tenant_id = $1 AND stack_id = $2 AND lower(btrim(source)) = lower(btrim($4))
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND lower(btrim(COALESCE(migration_status, ''))) NOT IN ('migrating', 'deploying', 'pending_verification', 'archived')
			AND lower(btrim(status)) NOT IN ('migrating', 'deploying', 'pending_verification', 'archived')
			AND NOT EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text($5::jsonb) AS observed(service_id)
				WHERE observed.service_id = services.id
			)
	`, tenantID, stackID, serverID, serviceSource, idsJSON)
	return err
}

// markGuardInventoryServicesUnavailableTx flips existing inventory-sourced
// service rows to access-unavailable without deleting them. Used when a
// manifest was observed but carried no service evidence: the rows stay so the
// dashboard keeps history, while their URLs stop being served as live.
func markGuardInventoryServicesUnavailableTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, stackID, serverID, serviceSource string,
	inventoryRevision int64,
) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE services SET
			url = NULL,
			metadata_json = COALESCE(metadata_json, '{}'::jsonb) || jsonb_build_object('inventory_revision', $5::bigint),
			access_json = CASE WHEN
				lower(btrim(COALESCE(migration_status, ''))) = 'archived' OR lower(btrim(status)) = 'archived'
				THEN '`+guardInventoryArchivedUnavailableJSON+`'::jsonb
				ELSE '`+guardInventoryNoEvidenceJSON+`'::jsonb END,
			updated_at = now()
		WHERE tenant_id = $1 AND stack_id = $2
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND lower(btrim(source)) = lower(btrim($4))
	`, tenantID, stackID, serverID, serviceSource, inventoryRevision)
	return err
}

func requireTenantBoundUpsert(result sql.Result, subject string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s identity belongs to another tenant", ErrConflict, subject)
	}
	return nil
}

var _ GuardInventoryProjectionStore = (*PostgresStore)(nil)
