package providercontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// ReadManagedRuntimeCleanup returns a redacted, tenant-RLS-scoped cleanup
// projection. It is a read-only database path: no adapter, provider API, or
// credential resolver is invoked.
func (r *Runtime) ReadManagedRuntimeCleanup(
	ctx context.Context,
	tenantID string,
	leaseID vmlease.LeaseID,
) (*monthlyruntime.CleanupReadbackFacts, error) {
	if r == nil || r.ledger == nil {
		return nil, monthlyruntime.ErrCleanupReadbackUnavailable
	}
	return r.ledger.ReadManagedRuntimeCleanup(ctx, tenantID, leaseID)
}

// ReadManagedRuntimeCleanup binds readback to one exact tenant, lease, and
// current resource generation. The SQL intentionally returns no provider
// handle, command JSON, receipt JSON, evidence document, or credential data.
func (l *PostgresLedger) ReadManagedRuntimeCleanup(
	ctx context.Context,
	tenantID string,
	leaseID vmlease.LeaseID,
) (*monthlyruntime.CleanupReadbackFacts, error) {
	if l == nil || l.db == nil {
		return nil, monthlyruntime.ErrCleanupReadbackUnavailable
	}
	tenantID = strings.TrimSpace(tenantID)
	leaseID = vmlease.LeaseID(strings.TrimSpace(string(leaseID)))
	if tenantID == "" || leaseID == "" {
		return nil, fmt.Errorf("%w: tenant id and lease id are required", ErrInvalidRequest)
	}

	facts := &monthlyruntime.CleanupReadbackFacts{}
	err := l.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT
				(server.id IS NOT NULL AND server.lease_id = lease.id) AS server_bound,
				(
					server.id IS NOT NULL
					AND server.lease_id = lease.id
					AND server.lifecycle_state = 'decommissioned'
					AND server.desired_state = 'absent'
					AND server.decommissioned_at IS NOT NULL
				) AS server_terminal,
				operation.operation_id IS NOT NULL AS provider_operation_found,
				(
					operation.operation_id IS NOT NULL
					AND (
						operation.status IN ('succeeded', 'failed', 'denied')
						OR operation.phase IN ('absent', 'failed', 'denied')
					)
				) AS provider_operation_terminal,
				COALESCE(absence.evidence_ref, '') AS absence_evidence_ref,
				capacity.release_operation_id IS NOT NULL AS capacity_released
			FROM techstack_vm_leases AS lease
			JOIN runtime_lease_execution_authorities AS authority
			  ON authority.tenant_id = lease.tenant_id
			 AND authority.lease_id = lease.id
			 AND authority.execution_authority = $3
			LEFT JOIN servers AS server
			  ON server.tenant_id = lease.tenant_id
			 AND server.id = lease.server_id
			LEFT JOIN LATERAL (
				SELECT release_operation_id
				FROM managed_runtime_capacity_release_facts AS release
				WHERE release.tenant_id = lease.tenant_id
				  AND release.lease_id = lease.id
				  AND release.resource_generation_id = lease.resource_generation_id
				LIMIT 1
			) AS capacity ON TRUE
			LEFT JOIN LATERAL (
				SELECT candidate.operation_id, candidate.status, candidate.phase
				FROM provider_operations AS candidate
				WHERE candidate.tenant_id = lease.tenant_id
				  AND candidate.lease_id = lease.id
				  AND candidate.command_json->>'schema_version' = $4
				  AND candidate.command_json->>'execution_authority' = $3
				  AND candidate.command_json #>> '{command,resource_generation_id}' = lease.resource_generation_id::text
				  AND (
					candidate.operation = 'decommission'
					OR candidate.operation_id = capacity.release_operation_id
				  )
				ORDER BY
					CASE WHEN candidate.operation_id = capacity.release_operation_id THEN 0 ELSE 1 END,
					candidate.updated_at DESC,
					candidate.operation_id DESC
				LIMIT 1
			) AS operation ON TRUE
			LEFT JOIN LATERAL (
				SELECT observation.evidence_ref
				FROM provider_absence_observations AS observation
				WHERE observation.tenant_id = lease.tenant_id
				  AND observation.operation_id = operation.operation_id
				  AND observation.observation = 'absent'
				  AND observation.source = 'provider_api'
				ORDER BY observation.binding_id ASC, observation.evidence_ref ASC
				LIMIT 1
			) AS absence ON TRUE
			WHERE lease.tenant_id = $1
			  AND lease.id = $2
		`, tenantID, string(leaseID), string(ExecutionAuthorityTechstackProviderControl), operationEnvelopeVersion).Scan(
			&facts.ServerBound,
			&facts.ServerTerminal,
			&facts.ProviderOperationFound,
			&facts.ProviderOperationTerminal,
			&facts.AbsenceEvidenceRef,
			&facts.CapacityReleased,
		)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOperationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("providercontrol: read managed runtime cleanup: %w", err)
	}
	return facts, nil
}
