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
	tenantID := prepared.command.Event.TenantID
	var result *GuardInventoryProjectionResult
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var databaseNow time.Time
		if queryErr := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); queryErr != nil {
			return fmt.Errorf("controlplane: read Guard inventory database time: %w", queryErr)
		}
		current, currentErr := scanServerRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serverRuntimeColumns+` FROM servers
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, prepared.command.Event.ServerID))
		if errors.Is(currentErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: Guard inventory requires a canonical server", ErrConflict)
		}
		if currentErr != nil {
			return currentErr
		}
		if bindingErr := validateGuardInventoryCanonicalServer(*current, prepared.command); bindingErr != nil {
			return bindingErr
		}
		retainedServiceIDs := []string{}
		if prepared.command.ManifestObserved && !guardInventoryProjectionFenced(*current, prepared.command.Event) {
			retainedServiceIDs, err = listGuardInventoryRetainedServiceIDsTx(ctx, tx, prepared.command)
			if err != nil {
				return err
			}
		}
		observation := guardInventoryServerObservationEventWithExpectedServices(
			prepared.command.Event, int64(len(prepared.command.Services)+len(retainedServiceIDs)),
		)
		serverEvent, applyErr := applyServerEventTx(
			ctx, tx, observation, databaseNow.UTC(),
		)
		if applyErr != nil {
			return applyErr
		}
		result = &GuardInventoryProjectionResult{ServerEvent: serverEvent}
		if serverEvent == nil || serverEvent.Server == nil {
			return fmt.Errorf("%w: Guard inventory aggregate result is missing", ErrConflict)
		}
		if !serverEvent.Applied {
			snapshot, replayed, replayErr := guardInventoryReplayTx(ctx, tx, prepared, *serverEvent.Server)
			if replayed {
				serverEvent.Inventory = snapshot
			}
			result.Replayed = replayed
			return replayErr
		}
		if serverEvent.Inventory == nil || serverEvent.Inventory.Revision <= 0 ||
			serverEvent.Server.InventoryRevision != serverEvent.Inventory.Revision {
			return fmt.Errorf("%w: accepted Guard inventory revision is unavailable", ErrConflict)
		}

		projection := guardInventoryProjectionAtRevision(prepared, serverEvent.Inventory.Revision)
		if nodeErr := upsertGuardInventoryNodeTx(ctx, tx, projection.Node); nodeErr != nil {
			return nodeErr
		}
		observedServiceIDs := make([]string, 0, len(projection.Services))
		for _, service := range projection.Services {
			// Provenance is per row: normalizeGuardInventoryServiceProjection has
			// already resolved and validated it, defaulting to the batch source.
			if serviceErr := upsertGuardInventoryServiceTx(
				ctx, tx, service, service.Runtime.Source, databaseNow.UTC(),
			); serviceErr != nil {
				return serviceErr
			}
			observedServiceIDs = append(observedServiceIDs, service.Legacy.ID)
		}
		if projection.ManifestObserved {
			// Absence of service evidence is not evidence of absence: a manifest
			// that exists but reports no services (e.g. written by a failed or
			// partial apply) must not delete observed history. Only a manifest
			// with at least one observed service may prune stale rows.
			if len(observedServiceIDs) > 0 {
				if pruneErr := pruneGuardInventoryServicesTx(
					ctx, tx, tenantID, projection.Event.Runtime.StackID,
					projection.Event.ServerID, projection.ServiceSource, observedServiceIDs,
					serverEvent.Inventory.Revision,
				); pruneErr != nil {
					return pruneErr
				}
			} else if markErr := markGuardInventoryServicesUnavailableTx(
				ctx, tx, tenantID, projection.Event.Runtime.StackID,
				projection.Event.ServerID, projection.ServiceSource,
				serverEvent.Inventory.Revision,
			); markErr != nil {
				return markErr
			}
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
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

func listGuardInventoryRetainedServiceIDsTx(
	ctx context.Context,
	tx *sql.Tx,
	command GuardInventoryProjection,
) ([]string, error) {
	observed := make([]string, 0, len(command.Services))
	for _, service := range command.Services {
		observed = append(observed, service.Legacy.ID)
	}
	sort.Strings(observed)
	observedJSON, err := json.Marshal(observed)
	if err != nil {
		return nil, err
	}
	// With observed services, only control-state rows survive the prune. With
	// zero observed services, no prune runs at all (absence of evidence is not
	// evidence of absence) and every existing row is retained.
	controlStateFilter := `
			AND (
				lower(btrim(COALESCE(migration_status, ''))) IN ('migrating', 'deploying', 'pending_verification', 'archived') OR
				lower(btrim(status)) IN ('migrating', 'deploying', 'pending_verification', 'archived')
			)`
	if len(command.Services) == 0 {
		controlStateFilter = ""
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM services
		WHERE tenant_id = $1 AND stack_id = $2
			AND (server_id = $3 OR (server_id IS NULL AND node_id = $3))
			AND lower(btrim(source)) = lower(btrim($4))`+controlStateFilter+`
			AND NOT EXISTS (
				SELECT 1 FROM jsonb_array_elements_text($5::jsonb) AS observed(service_id)
				WHERE observed.service_id = services.id
			)
		ORDER BY id
		FOR UPDATE
	`, command.Event.TenantID, command.Event.Runtime.StackID, command.Event.ServerID, command.ServiceSource, observedJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
