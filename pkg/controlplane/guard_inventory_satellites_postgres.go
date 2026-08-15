package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *PostgresStore) ApplyGuardInventorySatellites(ctx context.Context, command GuardInventorySatelliteProjection) (*GuardInventorySatelliteResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeGuardInventorySatelliteProjection(command)
	if err != nil {
		return nil, err
	}
	tagsJSON, err := marshalObject(prepared.Worker.Tags)
	if err != nil {
		return nil, err
	}
	capabilitiesJSON, err := marshalObject(prepared.Worker.Capabilities)
	if err != nil {
		return nil, err
	}
	resourcesJSON, err := marshalObject(prepared.Worker.Resources)
	if err != nil {
		return nil, err
	}
	healthJSON, err := marshalObject(prepared.RILServer.Health)
	if err != nil {
		return nil, err
	}
	inventoryJSON, err := marshalObject(prepared.RILServer.Inventory)
	if err != nil {
		return nil, err
	}

	var result GuardInventorySatelliteResult
	err = s.withTenant(ctx, prepared.TenantID, func(tx *sql.Tx) error {
		var marker int
		err := tx.QueryRowContext(ctx, `
			SELECT 1
			FROM servers
			WHERE tenant_id = $1 AND id = $2 AND generation = $3
				AND source_authority = 'guard' AND source_id = $4 AND source_epoch = $5
				AND source_sequence = $6 AND inventory_revision = $7 AND source_observed_at = $8
				AND lifecycle_state NOT IN ('decommissioning', 'decommissioned')
			FOR SHARE
		`, prepared.TenantID, prepared.ServerID, prepared.Generation, prepared.SourceID,
			prepared.SourceEpoch, prepared.SourceSequence, prepared.InventoryRevision, prepared.SourceObservedAt).Scan(&marker)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		worker := prepared.Worker
		lastSeen := nullableTime(worker.LastSeenAt)
		if worker.LastSeenAt == nil {
			lastSeen = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workers (
				id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json, owner_subject_id, capabilities_json, resources_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12, $13, $14, $15, $16,
				NULLIF($17, ''), $18, $19, NULLIF($20, ''), NULLIF($21, ''), NULLIF($22, ''),
				$23::jsonb, NULLIF($24, ''), $25::jsonb, $26::jsonb
			)
			ON CONFLICT (tenant_id, id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id, stack_id = EXCLUDED.stack_id,
				hostname = EXCLUDED.hostname, ip = EXCLUDED.ip, os = EXCLUDED.os, arch = EXCLUDED.arch,
				token_hash = EXCLUDED.token_hash,
				status = CASE WHEN workers.approved AND NULLIF(EXCLUDED.status, 'pending') IS NULL THEN workers.status ELSE EXCLUDED.status END,
				approved = workers.approved OR EXCLUDED.approved,
				approved_at = COALESCE(workers.approved_at, EXCLUDED.approved_at),
				last_seen_at = EXCLUDED.last_seen_at, cpu_cores = EXCLUDED.cpu_cores,
				ram_mb = EXCLUDED.ram_mb, disk_gb = EXCLUDED.disk_gb, gpu = EXCLUDED.gpu,
				has_nvme = EXCLUDED.has_nvme, has_hw_transcode = EXCLUDED.has_hw_transcode,
				docker_version = EXCLUDED.docker_version, type = EXCLUDED.type, provider = EXCLUDED.provider,
				tags_json = EXCLUDED.tags_json,
				owner_subject_id = COALESCE(workers.owner_subject_id, EXCLUDED.owner_subject_id),
				capabilities_json = EXCLUDED.capabilities_json,
				resources_json = (
					EXCLUDED.resources_json
					- 'agent_token_sha256'
					- 'enrollment_idempotency_sha256'
					- 'enrollment_request_sha256'
					- 'credential_generation'
				) || jsonb_strip_nulls(jsonb_build_object(
					'agent_token_sha256', workers.resources_json->'agent_token_sha256',
					'enrollment_idempotency_sha256', workers.resources_json->'enrollment_idempotency_sha256',
					'enrollment_request_sha256', workers.resources_json->'enrollment_request_sha256',
					'credential_generation', workers.resources_json->'credential_generation'
				)),
				updated_at = now()
		`, worker.ID, prepared.TenantID, worker.InstanceID, worker.StackID, worker.Hostname, worker.IP,
			worker.OS, worker.Arch, worker.TokenHash, firstNonEmpty(worker.Status, "pending"), worker.Approved,
			nullableTime(worker.ApprovedAt), lastSeen, worker.CPUCores, worker.RAMMB, worker.DiskGB,
			worker.GPU, worker.HasNVME, worker.HasHWTranscode, worker.DockerVersion, worker.Type,
			worker.Provider, tagsJSON, worker.OwnerSubjectID, capabilitiesJSON, resourcesJSON); err != nil {
			return err
		}

		ril := prepared.RILServer
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ril_servers (
				id, tenant_id, instance_id, stack_id, node_id, name, status,
				health_json, inventory_json, last_seen_at
			) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8::jsonb, $9::jsonb, $10)
			ON CONFLICT (id) DO UPDATE SET
				instance_id = EXCLUDED.instance_id, stack_id = EXCLUDED.stack_id, node_id = EXCLUDED.node_id,
				name = EXCLUDED.name, status = EXCLUDED.status, health_json = EXCLUDED.health_json,
				inventory_json = EXCLUDED.inventory_json, last_seen_at = EXCLUDED.last_seen_at, updated_at = now()
		`, ril.ID, prepared.TenantID, ril.InstanceID, ril.StackID, ril.NodeID, ril.Name,
			firstNonEmpty(ril.Status, "unknown"), healthJSON, inventoryJSON, nullableTime(ril.LastSeenAt)); err != nil {
			return err
		}

		saved, err := scanWorker(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
			FROM workers WHERE tenant_id = $1 AND id = $2
		`, prepared.TenantID, worker.ID))
		if err != nil {
			return err
		}
		result.Worker = saved
		result.Applied = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

var _ GuardInventorySatelliteStore = (*PostgresStore)(nil)
var _ GuardInventorySatelliteStore = (*MemoryStore)(nil)
