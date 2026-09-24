package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *PostgresStore) UpsertWorkerHeartbeat(ctx context.Context, worker Worker) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(worker.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *Worker
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var upsertErr error
		out, upsertErr = upsertWorkerHeartbeatTx(ctx, tx, worker)
		return upsertErr
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func upsertWorkerHeartbeatTx(ctx context.Context, tx *sql.Tx, worker Worker) (*Worker, error) {
	tenantID := strings.TrimSpace(worker.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	tagsJSON, err := marshalObject(worker.Tags)
	if err != nil {
		return nil, err
	}
	capabilitiesJSON, err := marshalObject(worker.Capabilities)
	if err != nil {
		return nil, err
	}
	resourcesJSON, err := marshalObject(worker.Resources)
	if err != nil {
		return nil, err
	}
	lastSeen := nullableTime(worker.LastSeenAt)
	if worker.LastSeenAt == nil {
		lastSeen = time.Now().UTC()
	}

	return scanWorker(tx.QueryRowContext(ctx, `
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
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				hostname = EXCLUDED.hostname,
				ip = EXCLUDED.ip,
				os = EXCLUDED.os,
				arch = EXCLUDED.arch,
				token_hash = EXCLUDED.token_hash,
				status = CASE
					WHEN workers.approved AND NULLIF(EXCLUDED.status, 'pending') IS NULL THEN workers.status
					ELSE EXCLUDED.status
				END,
				approved = workers.approved OR EXCLUDED.approved,
				approved_at = COALESCE(workers.approved_at, EXCLUDED.approved_at),
				last_seen_at = EXCLUDED.last_seen_at,
				cpu_cores = EXCLUDED.cpu_cores,
				ram_mb = EXCLUDED.ram_mb,
				disk_gb = EXCLUDED.disk_gb,
				gpu = EXCLUDED.gpu,
				has_nvme = EXCLUDED.has_nvme,
				has_hw_transcode = EXCLUDED.has_hw_transcode,
				docker_version = EXCLUDED.docker_version,
				type = EXCLUDED.type,
				provider = EXCLUDED.provider,
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
			RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
		`,
		worker.ID, tenantID, worker.InstanceID, worker.StackID, worker.Hostname, worker.IP,
		worker.OS, worker.Arch, worker.TokenHash, firstNonEmpty(worker.Status, "pending"),
		worker.Approved, nullableTime(worker.ApprovedAt), lastSeen, worker.CPUCores,
		worker.RAMMB, worker.DiskGB, worker.GPU, worker.HasNVME, worker.HasHWTranscode,
		worker.DockerVersion, worker.Type, worker.Provider, tagsJSON, worker.OwnerSubjectID,
		capabilitiesJSON, resourcesJSON,
	))
}

func (s *PostgresStore) CompareAndSwapWorkerCredential(ctx context.Context, command WorkerCredentialCAS) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeWorkerCredentialCAS(command)
	if err != nil {
		return nil, err
	}
	nextJSON, err := marshalObject(workerCredentialResources(prepared.Next))
	if err != nil {
		return nil, err
	}

	var out *Worker
	err = s.withTenant(ctx, prepared.TenantID, func(tx *sql.Tx) error {
		saved, queryErr := scanWorker(tx.QueryRowContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) || $7::jsonb,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2
				AND COALESCE(resources_json->>'credential_generation', '0') = $3
				AND COALESCE(resources_json->>'agent_token_sha256', '') = $4
				AND COALESCE(resources_json->>'enrollment_idempotency_sha256', '') = $5
				AND COALESCE(resources_json->>'enrollment_request_sha256', '') = $6
			RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
		`, prepared.TenantID, prepared.WorkerID, strconv.FormatInt(prepared.Expected.Generation, 10),
			prepared.Expected.TokenSHA256, prepared.Expected.IdempotencySHA256,
			prepared.Expected.RequestSHA256, nextJSON))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if queryErr != nil {
			return queryErr
		}
		out = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetWorker(ctx context.Context, tenantID, workerID string) (*Worker, error) {
	return s.getWorker(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
			status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
			gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
			tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
			created_at, updated_at
		FROM workers
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, workerID)
}

func (s *PostgresStore) ListWorkersByTenant(ctx context.Context, tenantID string) ([]Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out []Worker
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
			FROM workers
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`, tenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			worker, err := scanWorker(rows)
			if err != nil {
				return err
			}
			out = append(out, *worker)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ApproveWorker(ctx context.Context, tenantID, workerID, ownerSubjectID string, approvedAt time.Time) (*Worker, error) {
	return s.getWorker(ctx, tenantID, `
		UPDATE workers
		SET approved = true,
			status = 'approved',
			approved_at = $4,
			owner_subject_id = $3,
			updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
			status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
			gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
			tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
			created_at, updated_at
	`, tenantID, workerID, ownerSubjectID, approvedAt)
}

func (s *PostgresStore) UpsertPairingToken(ctx context.Context, token PairingToken) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID := strings.TrimSpace(token.TenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	metadataJSON, err := marshalObject(token.Metadata)
	if err != nil {
		return nil, err
	}

	var out *PairingToken
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		saved, err := scanPairingToken(tx.QueryRowContext(ctx, `
			INSERT INTO pairing_tokens (
				id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), $7,
				$8, $9, $10, $11::jsonb
			)
			ON CONFLICT (tenant_id, token_hash) DO UPDATE SET
				instance_id = EXCLUDED.instance_id,
				stack_id = EXCLUDED.stack_id,
				owner_subject_id = EXCLUDED.owner_subject_id,
				name = EXCLUDED.name,
				status = EXCLUDED.status,
				expires_at = EXCLUDED.expires_at,
				used_at = EXCLUDED.used_at,
				metadata_json = EXCLUDED.metadata_json,
				updated_at = now()
			RETURNING id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
		`,
			token.ID, tenantID, token.InstanceID, token.StackID, token.OwnerSubjectID, token.Name,
			token.TokenHash, firstNonEmpty(token.Status, "active"), nullableTime(token.ExpiresAt),
			nullableTime(token.UsedAt), metadataJSON,
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

func (s *PostgresStore) ListPairingTokensByOwner(ctx context.Context, tenantID, ownerSubjectID string) ([]PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if tenantID == "" || ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: tenant and owner subject ids required")
	}
	var out []PairingToken
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
			FROM pairing_tokens
			WHERE tenant_id = $1 AND owner_subject_id = $2
			ORDER BY created_at DESC, id DESC
		`, tenantID, ownerSubjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			token, scanErr := scanPairingToken(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *token)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresStore) GetPairingTokenByHash(ctx context.Context, tenantID, tokenHash string) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	if tenantID == "" {
		token, err := scanPairingToken(s.db.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
			FROM pairing_tokens
			WHERE token_hash = $1
			ORDER BY created_at DESC
			LIMIT 1
		`, tokenHash))
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return token, err
	}

	var out *PairingToken
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		token, err := scanPairingToken(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
			FROM pairing_tokens
			WHERE tenant_id = $1 AND token_hash = $2
		`, tenantID, tokenHash))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = token
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ClaimPairingToken(ctx context.Context, tenantID, tokenHash string, claimedAt time.Time) (*PairingToken, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" || claimedAt.IsZero() {
		return nil, ErrNotFound
	}
	claimedAt = claimedAt.UTC()

	var out *PairingToken
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		claimed, err := scanPairingToken(tx.QueryRowContext(ctx, `
			UPDATE pairing_tokens
			SET status = 'used', used_at = $3, updated_at = $3
			WHERE tenant_id = $1 AND token_hash = $2
				AND status = 'active' AND used_at IS NULL
				AND (expires_at IS NULL OR expires_at > $3)
			RETURNING id, tenant_id, instance_id, stack_id, owner_subject_id, name, token_hash,
				status, expires_at, used_at, metadata_json::text, created_at, updated_at
		`, tenantID, tokenHash, claimedAt))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = claimed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReleasePairingTokenClaim returns a claimed token to active after a
// post-claim enrollment failure that issued no credential. The store does not
// know whether a credential was issued, so callers must only use it on their
// pre-success path.
func (s *PostgresStore) ReleasePairingTokenClaim(ctx context.Context, tenantID, tokenHash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" {
		return ErrNotFound
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE pairing_tokens
			SET status = 'active', used_at = NULL, updated_at = now()
			WHERE tenant_id = $1 AND token_hash = $2 AND status = 'used'
		`, tenantID, tokenHash)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) RevokePairingToken(ctx context.Context, tenantID, ownerSubjectID, tokenID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	tokenID = strings.TrimSpace(tokenID)
	if tenantID == "" || ownerSubjectID == "" || tokenID == "" {
		return ErrNotFound
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE pairing_tokens
			SET status = 'revoked', updated_at = now()
			WHERE tenant_id = $1 AND owner_subject_id = $2 AND id = $3
		`, tenantID, ownerSubjectID, tokenID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) getWorker(ctx context.Context, tenantID, query string, args ...any) (*Worker, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}

	var out *Worker
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		worker, err := scanWorker(tx.QueryRowContext(ctx, query, args...))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = worker
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanWorker(row rowScanner) (*Worker, error) {
	var worker Worker
	var instanceID, stackID, ip, osName, arch, tokenHash, gpu, dockerVersion, workerType, provider, ownerSubjectID sql.NullString
	var approvedAt, lastSeenAt sql.NullTime
	var tagsJSON, capabilitiesJSON, resourcesJSON []byte
	if err := row.Scan(
		&worker.ID,
		&worker.TenantID,
		&instanceID,
		&stackID,
		&worker.Hostname,
		&ip,
		&osName,
		&arch,
		&tokenHash,
		&worker.Status,
		&worker.Approved,
		&approvedAt,
		&lastSeenAt,
		&worker.CPUCores,
		&worker.RAMMB,
		&worker.DiskGB,
		&gpu,
		&worker.HasNVME,
		&worker.HasHWTranscode,
		&dockerVersion,
		&workerType,
		&provider,
		&tagsJSON,
		&ownerSubjectID,
		&capabilitiesJSON,
		&resourcesJSON,
		&worker.CreatedAt,
		&worker.UpdatedAt,
	); err != nil {
		return nil, err
	}
	worker.InstanceID = instanceID.String
	worker.StackID = stackID.String
	worker.IP = ip.String
	worker.OS = osName.String
	worker.Arch = arch.String
	worker.TokenHash = tokenHash.String
	worker.GPU = gpu.String
	worker.DockerVersion = dockerVersion.String
	worker.Type = workerType.String
	worker.Provider = provider.String
	worker.OwnerSubjectID = ownerSubjectID.String
	if approvedAt.Valid {
		worker.ApprovedAt = &approvedAt.Time
	}
	if lastSeenAt.Valid {
		worker.LastSeenAt = &lastSeenAt.Time
	}
	if err := decodeObject(tagsJSON, &worker.Tags); err != nil {
		return nil, err
	}
	if err := decodeObject(capabilitiesJSON, &worker.Capabilities); err != nil {
		return nil, err
	}
	if err := decodeObject(resourcesJSON, &worker.Resources); err != nil {
		return nil, err
	}
	return &worker, nil
}

func scanPairingToken(row rowScanner) (*PairingToken, error) {
	var token PairingToken
	var instanceID, stackID, name sql.NullString
	var expiresAt, usedAt sql.NullTime
	var metadataJSON []byte
	if err := row.Scan(
		&token.ID,
		&token.TenantID,
		&instanceID,
		&stackID,
		&token.OwnerSubjectID,
		&name,
		&token.TokenHash,
		&token.Status,
		&expiresAt,
		&usedAt,
		&metadataJSON,
		&token.CreatedAt,
		&token.UpdatedAt,
	); err != nil {
		return nil, err
	}
	token.InstanceID = instanceID.String
	token.StackID = stackID.String
	token.Name = name.String
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if usedAt.Valid {
		token.UsedAt = &usedAt.Time
	}
	if err := decodeObject(metadataJSON, &token.Metadata); err != nil {
		return nil, err
	}
	return &token, nil
}

var _ WorkerStore = (*PostgresStore)(nil)

var _ WorkerCredentialStore = (*PostgresStore)(nil)
