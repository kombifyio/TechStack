package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	workerEnrollmentServerIDKey       = "server_id"
	workerEnrollmentRuntimeAgentIDKey = "runtime_agent_id"
	workerEnrollmentLeaseIDKey        = "lease_id"
)

// WorkerEnrollmentBinding is the immutable tenant-scoped identity claimed by
// Connect before server projection or credential issuance.
type WorkerEnrollmentBinding struct {
	TenantID       string
	WorkerID       string
	OwnerSubjectID string
	StackID        string
	ServerID       string
	RuntimeAgentID string
	LeaseID        string
}

// WorkerEnrollmentClaim carries the immutable binding and the declarative
// worker row to create when that binding has not been claimed yet.
type WorkerEnrollmentClaim struct {
	Binding WorkerEnrollmentBinding
	Worker  Worker
}

// WorkerEnrollmentClaimResult reports the canonical worker bound to the claim.
// An identical existing binding is an idempotent success and returns Created
// false without changing heartbeat or Guard-observed fields.
type WorkerEnrollmentClaimResult struct {
	Worker  *Worker
	Created bool
}

// WorkerEnrollmentStore atomically claims one tenant+worker enrollment
// identity. Implementations must reject a different existing binding with
// ErrConflict and must not invent LastSeenAt for a declarative enrollment.
type WorkerEnrollmentStore interface {
	ClaimWorkerEnrollment(ctx context.Context, claim WorkerEnrollmentClaim) (*WorkerEnrollmentClaimResult, error)
}

func normalizeWorkerEnrollmentClaim(claim WorkerEnrollmentClaim) (WorkerEnrollmentClaim, error) {
	binding := &claim.Binding
	binding.TenantID = strings.TrimSpace(binding.TenantID)
	binding.WorkerID = strings.TrimSpace(binding.WorkerID)
	binding.OwnerSubjectID = strings.TrimSpace(binding.OwnerSubjectID)
	binding.StackID = strings.TrimSpace(binding.StackID)
	binding.ServerID = strings.TrimSpace(binding.ServerID)
	binding.RuntimeAgentID = strings.TrimSpace(binding.RuntimeAgentID)
	binding.LeaseID = strings.TrimSpace(binding.LeaseID)
	if binding.TenantID == "" || binding.WorkerID == "" ||
		binding.OwnerSubjectID == "" || binding.ServerID == "" ||
		binding.RuntimeAgentID == "" {
		return claim, fmt.Errorf("%w: complete worker enrollment binding is required", ErrConflict)
	}
	if binding.RuntimeAgentID != binding.WorkerID {
		return claim, fmt.Errorf("%w: runtime agent must equal worker identity", ErrConflict)
	}

	worker := &claim.Worker
	worker.ID = strings.TrimSpace(worker.ID)
	worker.TenantID = strings.TrimSpace(worker.TenantID)
	worker.OwnerSubjectID = strings.TrimSpace(worker.OwnerSubjectID)
	worker.StackID = strings.TrimSpace(worker.StackID)
	if worker.ID != binding.WorkerID ||
		worker.TenantID != binding.TenantID ||
		worker.OwnerSubjectID != binding.OwnerSubjectID ||
		worker.StackID != binding.StackID ||
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentServerIDKey])) != binding.ServerID ||
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentRuntimeAgentIDKey])) != binding.RuntimeAgentID ||
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentLeaseIDKey])) != binding.LeaseID {
		return claim, fmt.Errorf("%w: worker projection does not match enrollment binding", ErrConflict)
	}
	if worker.LastSeenAt != nil {
		return claim, fmt.Errorf("%w: declarative enrollment cannot contain heartbeat evidence", ErrConflict)
	}
	return claim, nil
}

func workerMatchesEnrollmentBinding(worker Worker, binding WorkerEnrollmentBinding) bool {
	return strings.TrimSpace(worker.ID) == binding.WorkerID &&
		strings.TrimSpace(worker.TenantID) == binding.TenantID &&
		strings.TrimSpace(worker.OwnerSubjectID) == binding.OwnerSubjectID &&
		strings.TrimSpace(worker.StackID) == binding.StackID &&
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentServerIDKey])) == binding.ServerID &&
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentRuntimeAgentIDKey])) == binding.RuntimeAgentID &&
		strings.TrimSpace(stringValue(worker.Capabilities[workerEnrollmentLeaseIDKey])) == binding.LeaseID
}

func (s *MemoryStore) ClaimWorkerEnrollment(_ context.Context, claim WorkerEnrollmentClaim) (*WorkerEnrollmentClaimResult, error) {
	prepared, err := normalizeWorkerEnrollmentClaim(claim)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := workerKey(prepared.Binding.TenantID, prepared.Binding.WorkerID)
	if existing, ok := s.worker[key]; ok {
		if !workerMatchesEnrollmentBinding(existing, prepared.Binding) {
			return nil, ErrConflict
		}
		return &WorkerEnrollmentClaimResult{Worker: cloneWorker(existing)}, nil
	}

	worker := prepared.Worker
	now := s.now()
	worker.CreatedAt = now
	worker.UpdatedAt = now
	if worker.Status == "" {
		worker.Status = "pending"
	}
	worker.ApprovedAt = cloneTime(worker.ApprovedAt)
	worker.LastSeenAt = nil
	worker.Tags = cloneMap(worker.Tags)
	worker.Capabilities = cloneMap(worker.Capabilities)
	worker.Resources = cloneMap(worker.Resources)
	s.worker[key] = worker
	return &WorkerEnrollmentClaimResult{Worker: cloneWorker(worker), Created: true}, nil
}

func (s *PostgresStore) ClaimWorkerEnrollment(ctx context.Context, claim WorkerEnrollmentClaim) (*WorkerEnrollmentClaimResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeWorkerEnrollmentClaim(claim)
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

	var result WorkerEnrollmentClaimResult
	err = s.withTenant(ctx, prepared.Binding.TenantID, func(tx *sql.Tx) error {
		worker := prepared.Worker
		created, insertErr := scanWorker(tx.QueryRowContext(ctx, `
			INSERT INTO workers (
				id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json, owner_subject_id, capabilities_json, resources_json
			) VALUES (
				$1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''),
				NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12, NULL, $13, $14, $15,
				NULLIF($16, ''), $17, $18, NULLIF($19, ''), NULLIF($20, ''), NULLIF($21, ''),
				$22::jsonb, NULLIF($23, ''), $24::jsonb, $25::jsonb
			)
			ON CONFLICT (tenant_id, id) DO NOTHING
			RETURNING id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
		`,
			worker.ID, prepared.Binding.TenantID, worker.InstanceID, worker.StackID, worker.Hostname,
			worker.IP, worker.OS, worker.Arch, worker.TokenHash, firstNonEmpty(worker.Status, "pending"),
			worker.Approved, nullableTime(worker.ApprovedAt), worker.CPUCores, worker.RAMMB,
			worker.DiskGB, worker.GPU, worker.HasNVME, worker.HasHWTranscode,
			worker.DockerVersion, worker.Type, worker.Provider, tagsJSON,
			worker.OwnerSubjectID, capabilitiesJSON, resourcesJSON,
		))
		switch {
		case insertErr == nil:
			result.Worker = created
			result.Created = true
			return nil
		case !errors.Is(insertErr, sql.ErrNoRows):
			return insertErr
		}

		existing, selectErr := scanWorker(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
			FROM workers
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, prepared.Binding.TenantID, prepared.Binding.WorkerID))
		if errors.Is(selectErr, sql.ErrNoRows) {
			return ErrConflict
		}
		if selectErr != nil {
			return selectErr
		}
		if !workerMatchesEnrollmentBinding(*existing, prepared.Binding) {
			return ErrConflict
		}
		result.Worker = existing
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

var _ WorkerEnrollmentStore = (*MemoryStore)(nil)
var _ WorkerEnrollmentStore = (*PostgresStore)(nil)
