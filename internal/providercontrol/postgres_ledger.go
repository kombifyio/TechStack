package providercontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const (
	tenantContextKey          = "app.tenant_id"
	claimCapabilityContextKey = "app.provider_execution_claim_token"
	claimOwnerContextKey      = "app.provider_execution_claim_owner"
	operationEnvelopeVersion  = "techstack.provider-control-operation/v1"
)

type operationEnvelope struct {
	SchemaVersion           string                   `json:"schema_version"`
	ExecutionAuthority      ExecutionAuthority       `json:"execution_authority"`
	ExecutionProfile        ExecutionProfileSnapshot `json:"execution_profile"`
	RuntimeServerGeneration int64                    `json:"runtime_server_generation,omitempty"`
	Command                 providerexecutor.Command `json:"command"`
}

// PostgresLedger persists commands, the append-only receipt chain, resource
// custody, and immutable evidence in tenant-RLS-scoped tables.
type PostgresLedger struct {
	db                *sql.DB
	verifier          providerexecutor.EvidenceVerifier
	claimCredentials  claimCredentialAuthorizer
	runtimeProjection providerReceiptRuntimeProjector
}

// NewPostgresLedger creates a tenant-RLS-scoped provider execution ledger.
func NewPostgresLedger(db *sql.DB, verifier providerexecutor.EvidenceVerifier) (*PostgresLedger, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalidRequest)
	}
	return &PostgresLedger{
		db: db, verifier: verifier, claimCredentials: postgresClaimCredentialAuthorizer{},
		runtimeProjection: newPostgresProviderReceiptRuntimeProjector(db),
	}, nil
}

// BeginOperation atomically persists an immutable command, optional desired
// spec revision, and its coordinator-owned initial receipt.
func (l *PostgresLedger) BeginOperation(
	ctx context.Context,
	authority ExecutionAuthority,
	profile ExecutionProfileSnapshot,
	provisionDispatch ProvisionDispatchMode,
	command providerexecutor.Command,
	initial providerexecutor.Receipt,
	desiredSpec *DesiredSpecRevision,
) (OperationRecord, bool, error) {
	if l == nil || l.db == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	var record OperationRecord
	var created bool
	err := l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		var txErr error
		record, created, txErr = l.beginOperationInTx(
			ctx, tx, authority, profile, provisionDispatch, command, initial, desiredSpec,
		)
		return txErr
	})
	if err != nil {
		return OperationRecord{}, false, err
	}
	return record, created, nil
}

func (l *PostgresLedger) beginOperationInTx(
	ctx context.Context,
	tx *sql.Tx,
	authority ExecutionAuthority,
	profile ExecutionProfileSnapshot,
	provisionDispatch ProvisionDispatchMode,
	command providerexecutor.Command,
	initial providerexecutor.Receipt,
	desiredSpec *DesiredSpecRevision,
) (OperationRecord, bool, error) {
	if l == nil || l.db == nil || tx == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: ledger database and transaction are required", ErrInvalidRequest)
	}
	if validationErr := validateBeginOperation(ctx, authority, profile, provisionDispatch, command, initial, desiredSpec); validationErr != nil {
		return OperationRecord{}, false, validationErr
	}
	fence, fenceErr := loadLeaseExecutionFenceTx(ctx, tx, command)
	if fenceErr != nil {
		return OperationRecord{}, false, fenceErr
	}
	if command.Operation == providerexecutor.OperationReconcile ||
		command.Operation == providerexecutor.OperationDecommission {
		if l.runtimeProjection == nil {
			return OperationRecord{}, false, fmt.Errorf(
				"%w: mutation resource projection is not configured",
				ErrInvalidRequest,
			)
		}
		if validationErr := l.runtimeProjection.ValidateMutationStartTx(
			ctx, tx, command, fence.ServerGeneration,
		); validationErr != nil {
			return OperationRecord{}, false, validationErr
		}
	}
	envelope := operationEnvelope{
		SchemaVersion: operationEnvelopeVersion, ExecutionAuthority: authority,
		ExecutionProfile: profile, RuntimeServerGeneration: fence.ServerGeneration,
		Command: command,
	}
	commandJSON, err := json.Marshal(envelope)
	if err != nil {
		return OperationRecord{}, false, fmt.Errorf("providercontrol: encode operation envelope: %w", err)
	}
	receiptJSON, err := json.Marshal(initial)
	if err != nil {
		return OperationRecord{}, false, fmt.Errorf("providercontrol: encode initial receipt: %w", err)
	}
	return l.beginOperationTx(
		ctx, tx, authority, profile, provisionDispatch, command, initial, desiredSpec,
		fence.ServerGeneration, commandJSON, receiptJSON,
	)
}

func validateBeginOperation(
	ctx context.Context,
	authority ExecutionAuthority,
	profile ExecutionProfileSnapshot,
	provisionDispatch ProvisionDispatchMode,
	command providerexecutor.Command,
	initial providerexecutor.Receipt,
	desiredSpec *DesiredSpecRevision,
) error {
	if err := validateExecutionAuthority(authority); err != nil {
		return err
	}
	if validationErr := command.Validate(); validationErr != nil {
		return validationErr
	}
	if validationErr := validateExecutionProfileSnapshot(profile, command); validationErr != nil {
		return validationErr
	}
	if validationErr := validateProvisionDispatchMode(command.Operation, provisionDispatch); validationErr != nil {
		return validationErr
	}
	if profile.ProvisionDispatchMode != provisionDispatch {
		return fmt.Errorf("%w: operation dispatch mode must copy its execution-profile pin", ErrLedgerConflict)
	}
	if command.LedgerRevision > providerexecutor.MaxJSONSafeInteger {
		return fmt.Errorf("%w: ledger revision must fit the JSON-safe integer range", ErrInvalidRequest)
	}
	if validationErr := initial.ValidateFor(ctx, command, nil); validationErr != nil {
		return validationErr
	}
	return validateDesiredSpecCustody(command, desiredSpec)
}

func (l *PostgresLedger) beginOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	authority ExecutionAuthority,
	profile ExecutionProfileSnapshot,
	provisionDispatch ProvisionDispatchMode,
	command providerexecutor.Command,
	initial providerexecutor.Receipt,
	desiredSpec *DesiredSpecRevision,
	runtimeServerGeneration int64,
	commandJSON []byte,
	receiptJSON []byte,
) (OperationRecord, bool, error) {
	if runtimeServerGeneration < 1 {
		return OperationRecord{}, false, fmt.Errorf("%w: runtime server generation pin is required", ErrLeaseFence)
	}
	if desiredSpec != nil {
		if insertErr := insertDesiredSpecTx(ctx, tx, *desiredSpec); insertErr != nil {
			return OperationRecord{}, false, insertErr
		}
	}
	desiredRevision := desiredSpecRevision(desiredSpec)
	result, insertErr := tx.ExecContext(ctx, `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, provision_dispatch_mode, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14,$15,$16,now(),now())
		ON CONFLICT DO NOTHING
	`,
		command.TenantID, command.OperationID, command.LeaseID, command.Operation,
		command.IdempotencyKey, command.AdapterID, command.CommandDigest, commandJSON,
		command.LedgerRevision, desiredRevision, provisionDispatch, initial.Status, initial.Phase,
		initial.Sequence, initial.ReceiptDigest, command.RequestedAt,
	)
	if insertErr != nil {
		return OperationRecord{}, false, fmt.Errorf("providercontrol: insert operation: %w", insertErr)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return OperationRecord{}, false, fmt.Errorf("providercontrol: inspect operation insert: %w", rowsErr)
	}
	if rows == 1 {
		if receiptErr := insertReceiptTx(ctx, tx, command.TenantID, initial, receiptJSON); receiptErr != nil {
			return OperationRecord{}, false, receiptErr
		}
		return OperationRecord{
			ExecutionAuthority: authority, ExecutionProfile: profile,
			ProvisionDispatch: provisionDispatch, AutomationState: OperationAutomationRunnable,
			RuntimeServerGeneration: runtimeServerGeneration,
			Command:                 command, Head: initial,
		}, true, nil
	}

	existing, loadErr := loadOperationTx(ctx, tx, command.TenantID, command.OperationID, false)
	if errors.Is(loadErr, ErrOperationNotFound) {
		existing, loadErr = loadOperationByScopeTx(ctx, tx, command)
	}
	if loadErr != nil {
		return OperationRecord{}, false, loadErr
	}
	if err := requireExecutionAuthority(existing, authority); err != nil {
		return OperationRecord{}, false, err
	}
	if existing.ExecutionProfile != profile {
		return OperationRecord{}, false, fmt.Errorf("%w: execution profile changed across replay", ErrLedgerConflict)
	}
	if existing.ProvisionDispatch != provisionDispatch {
		return OperationRecord{}, false, fmt.Errorf("%w: provision dispatch mode changed across replay", ErrLedgerConflict)
	}
	if replayErr := providerexecutor.ValidateReplay(existing.Command, command); replayErr != nil {
		return OperationRecord{}, false, replayErr
	}
	if validationErr := existing.Head.ValidateFor(ctx, existing.Command, l.verifier); validationErr != nil {
		return OperationRecord{}, false, fmt.Errorf("providercontrol: invalid stored ledger head: %w", validationErr)
	}
	return existing, false, nil
}

func loadOperationByScopeTx(ctx context.Context, tx *sql.Tx, command providerexecutor.Command) (OperationRecord, error) {
	var operationID string
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id
		FROM provider_operations
		WHERE tenant_id = $1 AND lease_id = $2 AND operation = $3 AND idempotency_key = $4
	`, command.TenantID, command.LeaseID, command.Operation, command.IdempotencyKey).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return OperationRecord{}, ErrOperationNotFound
	}
	if err != nil {
		return OperationRecord{}, fmt.Errorf("providercontrol: load operation by idempotency scope: %w", err)
	}
	return loadOperationTx(ctx, tx, command.TenantID, operationID, false)
}

func desiredSpecRevision(spec *DesiredSpecRevision) any {
	if spec == nil {
		return nil
	}
	return spec.Revision
}

// LoadOperation loads the validated current ledger head for one tenant.
func (l *PostgresLedger) LoadOperation(ctx context.Context, tenantID, operationID string) (OperationRecord, error) {
	if l == nil || l.db == nil {
		return OperationRecord{}, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	if tenantID == "" || operationID == "" {
		return OperationRecord{}, fmt.Errorf("%w: tenant and operation id are required", ErrInvalidRequest)
	}
	var record OperationRecord
	err := l.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// Read projections remain available after an exact terminal
		// decommission tombstone. Every write/claim path independently reloads
		// the strict live lease fence before an adapter or mutation can run.
		loaded, loadErr := loadOperationProjectionTx(
			ctx, tx, tenantID, operationID, false, false,
		)
		if loadErr != nil {
			return loadErr
		}
		if validationErr := validateOperationRecord(ctx, loaded, l.verifier); validationErr != nil {
			return validationErr
		}
		record = loaded
		return nil
	})
	return record, err
}

// ListRunnableOperations returns a bounded tenant-scoped projection. The JSON
// predicate accepts only the immutable native authority envelope; Advance
// revalidates the full envelope, lease authority, command, and receipt before
// any adapter can be resolved.
func (l *PostgresLedger) ListRunnableOperations(ctx context.Context, tenantID string, limit int) ([]OperationRef, error) {
	if l == nil || l.db == nil {
		return nil, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: tenant id and runnable-operation limit from 1 to 100 are required", ErrInvalidRequest)
	}
	operations := make([]OperationRef, 0, limit)
	err := l.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT tenant_id, operation_id FROM (
			SELECT tenant_id, operation_id, updated_at
			FROM provider_operations
			WHERE tenant_id = $1
			  AND command_json->>'schema_version' = $2
			  AND command_json->>'execution_authority' = $3
			  AND CASE
			      WHEN jsonb_typeof(command_json->'runtime_server_generation') = 'number'
			       AND command_json->>'runtime_server_generation' ~ '^[1-9][0-9]{0,18}$'
			      THEN (command_json->>'runtime_server_generation')::numeric <= 9223372036854775807
			      ELSE false
			  END
			  AND provision_dispatch_mode <> 'blocked'
			  AND status = 'pending'
			  AND phase NOT IN ('planned', 'present', 'absent', 'failed', 'denied')
			  AND NOT EXISTS (
			      SELECT 1
			      FROM provider_operation_resource_free_terminalizations AS terminalization
			      WHERE terminalization.tenant_id = provider_operations.tenant_id
			        AND terminalization.operation_id = provider_operations.operation_id
			        AND terminalization.head_sequence = provider_operations.head_sequence
			        AND terminalization.head_receipt_digest =
			              provider_operations.head_receipt_digest
			  )
			  -- Superseded by teardown: once the lease is cancelled or absent or
			  -- the server is being or has been decommissioned, the lease fence
			  -- and the receipt and claim triggers refuse every further step of
			  -- an operation other than provision or decommission (for example
			  -- a power reconcile still converging when the owner decommissions).
			  -- Such an operation can never advance, so it is not runnable;
			  -- listing it made every sweep fail on it forever and, ordered
			  -- first, it took a batch slot. The one exception mirrors the claim
			  -- trigger: an unsettled side-effecting claim at the current head
			  -- may still be recovered until the server is tombstoned, so that
			  -- head stays runnable to settle its provider custody.
			  AND NOT (
				operation NOT IN ('provision', 'decommission')
				AND EXISTS (
					SELECT 1
					FROM techstack_vm_leases AS lease
					JOIN servers AS server
					  ON server.tenant_id = lease.tenant_id
					 AND server.id = lease.server_id
					WHERE lease.tenant_id = provider_operations.tenant_id
					  AND lease.id = provider_operations.lease_id
					  AND (
					      server.lifecycle_state = 'decommissioned'
					      OR server.decommissioned_at IS NOT NULL
					      OR (
					          (
					              lease.cancelled_at IS NOT NULL
					              OR lease.desired_state = 'absent'
					              OR server.desired_state = 'absent'
					              OR server.lifecycle_state = 'decommissioning'
					          )
					          AND NOT EXISTS (
					              SELECT 1
					              FROM provider_operation_execution_claims AS claim
					              WHERE claim.tenant_id = provider_operations.tenant_id
					                AND claim.operation_id = provider_operations.operation_id
					                AND claim.head_sequence = provider_operations.head_sequence
					                AND claim.head_receipt_digest =
					                      provider_operations.head_receipt_digest
					                AND claim.claim_access = 'side_effecting'
					                AND claim.state IN ('active', 'released')
					          )
					      )
					  )
				)
			  )
			  AND NOT (
				operation = 'provision'
				AND phase = 'accepted'
				AND EXISTS (
					SELECT 1 FROM provider_provision_dispatch_guards AS dispatch_guard
					WHERE dispatch_guard.tenant_id = provider_operations.tenant_id
					  AND dispatch_guard.lease_id = provider_operations.lease_id
					  AND dispatch_guard.resource_generation_id =
					      (provider_operations.command_json #>> '{command,resource_generation_id}')::uuid
					  AND (
						dispatch_guard.operation_id <> provider_operations.operation_id
						OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
						OR dispatch_guard.guard_origin = 'migration_quarantine'
					  )
				)
				AND NOT (
					EXISTS (
						SELECT 1
						FROM provider_provision_resolution_decisions AS decision
						WHERE decision.tenant_id = provider_operations.tenant_id
						  AND decision.operation_id = provider_operations.operation_id
						  AND decision.expected_head_sequence = provider_operations.head_sequence
						  AND decision.expected_head_receipt_digest =
						        provider_operations.head_receipt_digest
						  AND decision.outcome = 'no_candidate_observed'
					)
					AND EXISTS (
						SELECT 1
						FROM techstack_vm_leases AS lease
						JOIN servers AS server
						  ON server.tenant_id = lease.tenant_id
						 AND server.id = lease.server_id
						WHERE lease.tenant_id = provider_operations.tenant_id
						  AND lease.id = provider_operations.lease_id
						  AND (
						      lease.cancelled_at IS NOT NULL
						      OR lease.desired_state = 'absent'
						      OR server.desired_state = 'absent'
						      OR server.lifecycle_state = 'decommissioning'
						  )
					)
				)
			  )

			UNION

			-- Leaked capacity: a provision that failed before binding any
			-- resource still holds its managed-runtime slot. The slot is
			-- released only by a decommission (which needs a provisioned
			-- server) or by resource-free teardown, so without this arm the
			-- reconciler never revisits the operation and the slot is lost
			-- permanently -- after the limit is reached the owner can never
			-- create another managed server.
			--
			-- Advancing a terminal head performs no provider work: the
			-- coordinator only offers it to the teardown finalizer, whose
			-- transaction commits solely when the database proves no provider
			-- resource can exist for that generation.
			SELECT tenant_id, operation_id, updated_at
			FROM provider_operations
			WHERE tenant_id = $1
			  AND command_json->>'schema_version' = $2
			  AND command_json->>'execution_authority' = $3
			  AND operation = 'provision'
			  AND status = 'failed'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM provider_operation_resource_free_terminalizations AS terminalization
			      WHERE terminalization.tenant_id = provider_operations.tenant_id
			        AND terminalization.operation_id = provider_operations.operation_id
			        AND terminalization.head_sequence = provider_operations.head_sequence
			        AND terminalization.head_receipt_digest =
			              provider_operations.head_receipt_digest
			  )
			  AND EXISTS (
			      SELECT 1
			      FROM managed_runtime_capacity_reservations AS reservation
			      WHERE reservation.tenant_id = provider_operations.tenant_id
			        AND reservation.lease_id = provider_operations.lease_id
			        AND reservation.resource_generation_id =
			            (provider_operations.command_json #>> '{command,resource_generation_id}')::uuid
			        AND NOT EXISTS (
			            SELECT 1
			            FROM managed_runtime_capacity_release_facts AS release
			            WHERE release.tenant_id = reservation.tenant_id
			              AND release.lease_id = reservation.lease_id
			              AND release.resource_generation_id = reservation.resource_generation_id
			        )
			  )
			) AS runnable
			ORDER BY updated_at ASC, operation_id ASC
			LIMIT $4
		`, tenantID, operationEnvelopeVersion, ExecutionAuthorityTechstackProviderControl, limit)
		if queryErr != nil {
			return fmt.Errorf("providercontrol: list runnable operations: %w", queryErr)
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var operation OperationRef
			if scanErr := rows.Scan(&operation.TenantID, &operation.OperationID); scanErr != nil {
				return fmt.Errorf("providercontrol: scan runnable operation: %w", scanErr)
			}
			operations = append(operations, operation)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return fmt.Errorf("providercontrol: iterate runnable operations: %w", rowsErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return operations, nil
}

// AppendReceipt validates and atomically compare-and-swaps one receipt while
// retaining every resource handle and immutable evidence envelope it carries.
func (l *PostgresLedger) AppendReceipt(
	ctx context.Context,
	command providerexecutor.Command,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
) error {
	if l == nil || l.db == nil {
		return fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	if previous.Phase != providerexecutor.PhaseRequested || next.Phase != providerexecutor.PhaseAccepted {
		return ErrExecutionClaimNeeded
	}
	if validationErr := providerexecutor.ValidateAppend(ctx, command, previous, next, l.verifier); validationErr != nil {
		return validationErr
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("providercontrol: encode receipt: %w", err)
	}
	return l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		return l.appendReceiptTx(ctx, tx, command, previous, next, nextJSON)
	})
}

func (l *PostgresLedger) appendReceiptTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
	nextJSON []byte,
) error {
	current, loadErr := loadOperationTx(ctx, tx, command.TenantID, command.OperationID, true)
	if loadErr != nil {
		return loadErr
	}
	if replayErr := providerexecutor.ValidateReplay(current.Command, command); replayErr != nil {
		return replayErr
	}
	if validationErr := validateOperationRecord(ctx, current, l.verifier); validationErr != nil {
		return validationErr
	}
	if current.Head.Sequence != previous.Sequence || current.Head.ReceiptDigest != previous.ReceiptDigest {
		return fmt.Errorf("%w: receipt head changed", ErrLedgerConflict)
	}
	return l.persistAcceptedReceiptTx(
		ctx, tx, current, previous, next, nextJSON, "advance operation head",
	)
}

func persistReceiptResourcesTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	next providerexecutor.Receipt,
) error {
	for _, resource := range next.Resources {
		if resourceErr := upsertResourceTx(ctx, tx, command.TenantID, command.OperationID, next.Sequence, resource); resourceErr != nil {
			return resourceErr
		}
		for _, evidence := range resource.Evidence {
			if evidenceErr := insertEvidenceTx(ctx, tx, command.TenantID, command.OperationID, resource.BindingID, next.Sequence, evidence); evidenceErr != nil {
				return evidenceErr
			}
		}
	}
	return nil
}

func advanceOperationHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
	operation string,
) error {
	result, updateErr := tx.ExecContext(ctx, `
		UPDATE provider_operations
		SET status = $4,
			phase = $5,
			head_sequence = $6,
			head_receipt_digest = $7,
			updated_at = $8
		WHERE tenant_id = $1
		  AND operation_id = $2
		  AND head_sequence = $3
		  AND head_receipt_digest = $9
	`, command.TenantID, command.OperationID, previous.Sequence, next.Status,
		next.Phase, next.Sequence, next.ReceiptDigest, next.IssuedAt,
		previous.ReceiptDigest)
	if updateErr != nil {
		return fmt.Errorf(
			"providercontrol: %s: %w",
			operation,
			normalizeLeaseFenceDatabaseError(updateErr),
		)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil || rows != 1 {
		return fmt.Errorf("%w: operation head compare-and-swap failed", ErrLedgerConflict)
	}
	return nil
}

func normalizeLeaseFenceDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"provider operation head is stale against the live typed runtime lease",
		"provider operation head is stale against the live typed runtime generation",
		"provider operation head is fenced by cancellation or teardown intent",
		"provider decommission head has no teardown intent",
		"provider execution claim is fenced by cancellation or teardown intent",
		"provider execution claim is fenced by a terminal server decommission tombstone",
		"new provider execution claim is fenced by teardown intent",
		"provider execution claim is stale against the runtime generation",
		"provider decommission claim has no teardown intent",
	} {
		if strings.Contains(message, marker) {
			return fmt.Errorf("%w: %v", ErrLeaseFence, err)
		}
	}
	return err
}

// AcquireExecutionClaim atomically acquires the sole adapter-execution
// capability for the operation's exact current head. Expired and explicitly
// released claims are recoverable only for retry-safe heads; AMO provision
// dispatch requires AcquireProvisionDispatchClaim and is never rearmed.
func (l *PostgresLedger) AcquireExecutionClaim(
	ctx context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	access ExecutionClaimAccess,
	owner, token string,
	ttl time.Duration,
) (ExecutionClaim, error) {
	if l == nil || l.db == nil {
		return ExecutionClaim{}, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	claim, normalizedTTL, validationErr := prepareExecutionClaim(ctx, command, head, access, owner, token, ttl, l.verifier)
	if validationErr != nil {
		return ExecutionClaim{}, validationErr
	}
	transactionErr := l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		return l.acquireExecutionClaimTx(ctx, tx, command, head, &claim, normalizedTTL)
	})
	if transactionErr != nil {
		return ExecutionClaim{}, transactionErr
	}
	return claim, nil
}

// AcquireProvisionDispatchClaim atomically inserts the immutable AMO dispatch
// guard and acquires its first accepted-head claim. Only the transaction which
// creates both records receives an in-memory execute permit. Any existing
// operation- or generation-bound guard requires manual reconciliation.
func (l *PostgresLedger) AcquireProvisionDispatchClaim(
	ctx context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	binding PreparedProvisionBinding,
	owner, token string,
	ttl time.Duration,
) (ProvisionDispatchGrant, error) {
	if l == nil || l.db == nil {
		return ProvisionDispatchGrant{}, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	claim, normalizedTTL, validationErr := prepareExecutionClaim(
		ctx, command, head, ExecutionClaimSideEffecting, owner, token, ttl, l.verifier,
	)
	if validationErr != nil {
		return ProvisionDispatchGrant{}, validationErr
	}
	if !isResourceFreePendingProvisionAcceptedHead(command, head) {
		return ProvisionDispatchGrant{}, fmt.Errorf("%w: AMO dispatch requires a resource-free pending provision accepted head", ErrDispatchPermitNeeded)
	}
	invocation, invocationErr := newAdapterInvocation(providerexecutor.ExecutionRequest{Command: command, Previous: head})
	if invocationErr != nil {
		return ProvisionDispatchGrant{}, invocationErr
	}
	transactionErr := l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		return l.acquireProvisionDispatchClaimTx(
			ctx, tx, command, head, binding, invocation, &claim, normalizedTTL,
		)
	})
	if transactionErr != nil {
		return ProvisionDispatchGrant{}, transactionErr
	}
	return ProvisionDispatchGrant{Claim: claim, Permit: newExecutePermit(claim, binding)}, nil
}

func isResourceFreePendingProvisionAcceptedHead(
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) bool {
	return command.Operation == providerexecutor.OperationProvision &&
		head.Status == providerexecutor.StatusPending && head.Phase == providerexecutor.PhaseAccepted &&
		len(head.Resources) == 0
}

func (l *PostgresLedger) acquireProvisionDispatchClaimTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	binding PreparedProvisionBinding,
	invocation AdapterInvocation,
	claim *ExecutionClaim,
	ttl time.Duration,
) error {
	current, err := l.loadProvisionDispatchClaimHeadTx(ctx, tx, command, head, binding, invocation, claim.Access)
	if err != nil {
		return err
	}
	if err := l.authorizeProvisionDispatchClaimTx(ctx, tx, current, *claim); err != nil {
		return err
	}
	return insertProvisionDispatchGuardTx(ctx, tx, command, head, binding, claim, ttl)
}

func (l *PostgresLedger) loadProvisionDispatchClaimHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	binding PreparedProvisionBinding,
	invocation AdapterInvocation,
	access ExecutionClaimAccess,
) (OperationRecord, error) {
	current, err := loadOperationTx(ctx, tx, command.TenantID, command.OperationID, true)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := providerexecutor.ValidateReplay(current.Command, command); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationRecord(ctx, current, l.verifier); err != nil {
		return OperationRecord{}, err
	}
	if err := validatePreparedProvisionBinding(
		binding, invocation, current.ExecutionProfile.AdapterManifestHash,
	); err != nil {
		return OperationRecord{}, err
	}
	if current.Head.Sequence != head.Sequence || current.Head.ReceiptDigest != head.ReceiptDigest {
		return OperationRecord{}, fmt.Errorf("%w: provision dispatch head changed", ErrLedgerConflict)
	}
	if current.ProvisionDispatch != ProvisionDispatchAtMostOnceManualReconcile ||
		!isResourceFreePendingProvisionAcceptedHead(current.Command, current.Head) {
		return OperationRecord{}, fmt.Errorf("%w: operation is not an executable AMO provision accepted head", ErrDispatchPermitNeeded)
	}
	expectedAccess, err := classifyExecutionClaimAccess(current.Command.Operation, current.Head.Phase)
	if err != nil {
		return OperationRecord{}, err
	}
	if expectedAccess != access {
		return OperationRecord{}, fmt.Errorf("%w: execution claim access does not match the current head", ErrAdapterSafety)
	}
	return current, nil
}

func (l *PostgresLedger) authorizeProvisionDispatchClaimTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	claim ExecutionClaim,
) error {
	if err := l.authorizeClaimCredentialTx(ctx, tx, current, claim.Access); err != nil {
		return err
	}
	if err := setExecutionClaimContextTx(ctx, tx, claim); err != nil {
		return err
	}
	if err := lockProvisionGenerationCustodyTx(ctx, tx, current.Command); err != nil {
		return err
	}
	_, _, exists, err := loadProvisionGenerationCustodyTx(ctx, tx, current.Command)
	if err != nil {
		return err
	}
	if exists {
		return ErrProvisionManualReconcile
	}
	return nil
}

func insertProvisionDispatchGuardTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	binding PreparedProvisionBinding,
	claim *ExecutionClaim,
	ttl time.Duration,
) error {
	var guardedAt time.Time
	err := tx.QueryRowContext(ctx, `
			INSERT INTO provider_provision_dispatch_guards (
				tenant_id, operation_id, lease_id, lease_revision, server_id,
				resource_generation_id, head_sequence, head_receipt_digest,
				capability_snapshot_hash, execution_profile_hash, dispatch_mode,
				prepared_request_digest, credential_version_hash,
				provider_scope_hash, correlation_hash, adapter_manifest_hash,
				guard_origin, first_claim_token_digest, first_claim_owner, guarded_at
			) VALUES (
				$1,$2,$3,$4,$5,$6::uuid,$7,$8,$9,$10,$11,
				$12,$13,$14,$15,$16,'first_claim',$17,$18,clock_timestamp()
			)
			ON CONFLICT DO NOTHING
			RETURNING guarded_at
		`, command.TenantID, command.OperationID, command.LeaseID, int64(command.LeaseRevision), // #nosec G115 -- prepareExecutionClaim validates the sealed command's JSON-safe revision.
		command.RuntimeServerID, command.ResourceGenerationID, int64(head.Sequence), head.ReceiptDigest, // #nosec G115 -- prepareExecutionClaim validates the receipt's JSON-safe sequence.
		command.CapabilitySnapshotHash, command.ExecutionProfileHash,
		ProvisionDispatchAtMostOnceManualReconcile,
		binding.RequestDigest, binding.CredentialVersionHash, binding.ProviderScopeHash,
		binding.CorrelationHash, binding.AdapterManifestHash,
		executionClaimTokenDigest(claim.Token), claim.Owner,
	).Scan(&guardedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProvisionManualReconcile
	}
	if err != nil {
		return fmt.Errorf("providercontrol: create provision dispatch guard: %w", err)
	}
	return upsertExecutionClaimTx(ctx, tx, claim, ttl)
}

func prepareExecutionClaim(
	ctx context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	access ExecutionClaimAccess,
	owner string,
	token string,
	ttl time.Duration,
	verifier providerexecutor.EvidenceVerifier,
) (ExecutionClaim, time.Duration, error) {
	if validationErr := command.Validate(); validationErr != nil {
		return ExecutionClaim{}, 0, validationErr
	}
	if validationErr := head.ValidateFor(ctx, command, verifier); validationErr != nil {
		return ExecutionClaim{}, 0, validationErr
	}
	if head.Sequence > providerexecutor.MaxJSONSafeInteger {
		return ExecutionClaim{}, 0, fmt.Errorf("%w: receipt sequence must fit the JSON-safe integer range", ErrInvalidRequest)
	}
	if !validExecutionClaimAccess(access) {
		return ExecutionClaim{}, 0, fmt.Errorf("%w: execution claim access is invalid", ErrInvalidRequest)
	}
	normalizedTTL, ttlErr := normalizeClaimTTL(ttl)
	if ttlErr != nil {
		return ExecutionClaim{}, 0, ttlErr
	}
	owner = strings.TrimSpace(owner)
	token = strings.TrimSpace(token)
	if owner == "" || token == "" {
		return ExecutionClaim{}, 0, fmt.Errorf("%w: claim owner and token are required", ErrInvalidRequest)
	}
	if head.Phase == providerexecutor.PhaseRequested {
		return ExecutionClaim{}, 0, fmt.Errorf("%w: requested to accepted is coordinator-owned", ErrInvalidRequest)
	}
	return ExecutionClaim{
		TenantID: command.TenantID, OperationID: command.OperationID,
		ResourceGenerationID: command.ResourceGenerationID,
		HeadSequence:         head.Sequence, HeadReceiptDigest: head.ReceiptDigest,
		Access: access, Owner: owner, Token: token,
	}, normalizedTTL, nil
}

func (l *PostgresLedger) acquireExecutionClaimTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	claim *ExecutionClaim,
	ttl time.Duration,
) error {
	current, err := l.loadExecutionClaimHeadTx(ctx, tx, command, head, claim.Access)
	if err != nil {
		return err
	}
	if err := validateExecutionClaimLeaseFenceTx(ctx, tx, current, *claim); err != nil {
		return err
	}
	if credentialErr := l.authorizeClaimCredentialTx(ctx, tx, current, claim.Access); credentialErr != nil {
		return credentialErr
	}
	if contextErr := setExecutionClaimContextTx(ctx, tx, *claim); contextErr != nil {
		return contextErr
	}
	if current.Command.Operation == providerexecutor.OperationProvision &&
		current.Head.Status == providerexecutor.StatusPending &&
		current.Head.Phase == providerexecutor.PhaseAccepted {
		if custodyErr := ensureCrashRecoverableProvisionCustodyTx(ctx, tx, current, *claim); custodyErr != nil {
			return custodyErr
		}
	}
	return upsertExecutionClaimTx(ctx, tx, claim, ttl)
}

func (l *PostgresLedger) loadExecutionClaimHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	access ExecutionClaimAccess,
) (OperationRecord, error) {
	current, err := loadOperationProjectionTx(
		ctx, tx, command.TenantID, command.OperationID, true, false,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := providerexecutor.ValidateReplay(current.Command, command); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationRecord(ctx, current, l.verifier); err != nil {
		return OperationRecord{}, err
	}
	if current.Head.Sequence != head.Sequence || current.Head.ReceiptDigest != head.ReceiptDigest {
		return OperationRecord{}, fmt.Errorf("%w: claim head changed", ErrLedgerConflict)
	}
	expectedAccess, err := classifyExecutionClaimAccess(current.Command.Operation, current.Head.Phase)
	if err != nil {
		return OperationRecord{}, err
	}
	if expectedAccess != access {
		return OperationRecord{}, fmt.Errorf("%w: execution claim access does not match the current head", ErrAdapterSafety)
	}
	return current, nil
}

func validateExecutionClaimLeaseFenceTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	claim ExecutionClaim,
) error {
	recoveringUnsettled, err := isExactUnsettledClaimRecoveryTx(ctx, tx, current, claim)
	if err != nil {
		return err
	}
	allowTeardownContinuation := recoveringUnsettled ||
		(claim.Access == ExecutionClaimReadOnly &&
			current.Command.Operation == providerexecutor.OperationProvision &&
			current.Head.Phase == providerexecutor.PhaseResourcesBound)
	fence, err := loadLeaseExecutionFenceTx(ctx, tx, current.Command, allowTeardownContinuation)
	if err != nil {
		return err
	}
	allowHistoricalGeneration := claim.Access == ExecutionClaimReadOnly || recoveringUnsettled
	if current.RuntimeServerGeneration < 1 ||
		fence.ServerGeneration < current.RuntimeServerGeneration ||
		(current.RuntimeServerGeneration != fence.ServerGeneration && !allowHistoricalGeneration) {
		return fmt.Errorf(
			"%w: operation runtime server generation %d is not admitted against live generation %d",
			ErrLeaseFence,
			current.RuntimeServerGeneration,
			fence.ServerGeneration,
		)
	}
	if current.AutomationState == OperationAutomationManualReconcileRequired {
		return ErrProvisionManualReconcile
	}
	if current.Command.Operation == providerexecutor.OperationProvision &&
		current.ProvisionDispatch == ProvisionDispatchAtMostOnceManualReconcile &&
		current.Head.Phase == providerexecutor.PhaseAccepted {
		return ErrDispatchPermitNeeded
	}
	return nil
}

// isExactUnsettledClaimRecoveryTx recognizes only a takeover which preserves
// the same immutable operation head and side-effect access. A released claim,
// or an active claim whose execution capability expired, is still durable
// ambiguity. The exact crash-recovery invocation may reacquire that head even
// after teardown intent so it can correlate and journal the provider result;
// no new operation receives that exception.
func isExactUnsettledClaimRecoveryTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	claim ExecutionClaim,
) (bool, error) {
	if claim.Access != ExecutionClaimSideEffecting {
		return false, nil
	}
	var recoverable bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM provider_operation_execution_claims AS prior
			WHERE prior.tenant_id = $1
			  AND prior.operation_id = $2
			  AND prior.head_sequence = $3
			  AND prior.head_receipt_digest = $4
			  AND prior.claim_access = 'side_effecting'
			  AND (
			      prior.state = 'released'
			      OR (
			          prior.state = 'active'
			          AND prior.lease_expires_at <= clock_timestamp()
			      )
			  )
		)
	`, record.Command.TenantID, record.Command.OperationID,
		int64(record.Head.Sequence), record.Head.ReceiptDigest).Scan(&recoverable); err != nil { // #nosec G115 -- validateOperationRecord bounds the stored receipt sequence.
		return false, fmt.Errorf("providercontrol: inspect unsettled claim recovery: %w", err)
	}
	return recoverable, nil
}

func ensureCrashRecoverableProvisionCustodyTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	claim ExecutionClaim,
) error {
	if record.ProvisionDispatch != ProvisionDispatchNativeIdempotency &&
		record.ProvisionDispatch != ProvisionDispatchProviderCorrelation {
		return fmt.Errorf("%w: provision mode is not crash recoverable", ErrCrashRecovery)
	}
	if lockErr := lockProvisionGenerationCustodyTx(ctx, tx, record.Command); lockErr != nil {
		return lockErr
	}
	operationID, dispatchMode, exists, loadErr := loadProvisionGenerationCustodyTx(ctx, tx, record.Command)
	if loadErr != nil {
		return loadErr
	}
	if exists {
		if operationID == record.Command.OperationID && dispatchMode == record.ProvisionDispatch {
			return nil
		}
		return ErrProvisionManualReconcile
	}
	_, insertErr := tx.ExecContext(ctx, `
		INSERT INTO provider_provision_dispatch_guards (
			tenant_id, operation_id, lease_id, lease_revision, server_id,
			resource_generation_id, head_sequence, head_receipt_digest,
			capability_snapshot_hash, execution_profile_hash, dispatch_mode,
			adapter_manifest_hash, guard_origin,
			first_claim_token_digest, first_claim_owner, guarded_at
		) VALUES (
			$1,$2,$3,$4,$5,$6::uuid,$7,$8,$9,$10,$11,$12,
			'first_claim',$13,$14,clock_timestamp()
		)
	`, record.Command.TenantID, record.Command.OperationID, record.Command.LeaseID,
		int64(record.Command.LeaseRevision), record.Command.RuntimeServerID, // #nosec G115 -- validateOperationRecord validates the sealed command revision.
		record.Command.ResourceGenerationID, int64(record.Head.Sequence), record.Head.ReceiptDigest, // #nosec G115 -- validateOperationRecord validates the sealed receipt sequence.
		record.Command.CapabilitySnapshotHash, record.Command.ExecutionProfileHash,
		record.ProvisionDispatch, record.ExecutionProfile.AdapterManifestHash,
		executionClaimTokenDigest(claim.Token), claim.Owner,
	)
	if insertErr != nil {
		return fmt.Errorf("providercontrol: create crash-recoverable provision custody: %w", insertErr)
	}
	return nil
}

func lockProvisionGenerationCustodyTx(ctx context.Context, tx *sql.Tx, command providerexecutor.Command) error {
	_, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))
	`, command.TenantID, command.LeaseID+":"+command.ResourceGenerationID)
	if err != nil {
		return fmt.Errorf("providercontrol: lock provision generation custody: %w", err)
	}
	return nil
}

func loadProvisionGenerationCustodyTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) (string, ProvisionDispatchMode, bool, error) {
	// Every caller holds lockProvisionGenerationCustodyTx for this exact
	// tenant/lease/generation. The guard row is immutable, so an additional row
	// lock would add no serialization and would wrongly require UPDATE rights.
	var operationID string
	var dispatchMode ProvisionDispatchMode
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id, dispatch_mode
		FROM provider_provision_dispatch_guards
		WHERE tenant_id = $1 AND lease_id = $2 AND resource_generation_id = $3::uuid
	`, command.TenantID, command.LeaseID, command.ResourceGenerationID).Scan(&operationID, &dispatchMode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("providercontrol: load provision generation custody: %w", err)
	}
	return operationID, dispatchMode, true, nil
}

func upsertExecutionClaimTx(ctx context.Context, tx *sql.Tx, claim *ExecutionClaim, ttl time.Duration) error {
	tokenDigest := executionClaimTokenDigest(claim.Token)
	takeoverErr := tx.QueryRowContext(ctx, `
		WITH db_time AS (SELECT clock_timestamp() AS at)
		UPDATE provider_operation_execution_claims AS claim
		SET claim_token_digest = $5,
			claim_owner = $6,
			claim_access = $7,
			state = 'active',
			claimed_at = db_time.at,
			lease_expires_at = db_time.at + $8::interval,
			released_at = NULL
		FROM db_time
		WHERE claim.tenant_id = $1
		  AND claim.operation_id = $2
		  AND claim.head_sequence = $3
		  AND claim.head_receipt_digest = $4
		  AND (
		      claim.state = 'released'
		      OR (
		          claim.state = 'active'
		          AND claim.lease_expires_at <= db_time.at
		      )
		  )
		RETURNING claim.lease_expires_at
	`, claim.TenantID, claim.OperationID, claim.HeadSequence, claim.HeadReceiptDigest,
		tokenDigest, claim.Owner, claim.Access, ttl.String()).Scan(&claim.ExpiresAt)
	if takeoverErr == nil {
		return nil
	}
	if !errors.Is(takeoverErr, sql.ErrNoRows) {
		return fmt.Errorf(
			"providercontrol: recover execution claim: %w",
			normalizeLeaseFenceDatabaseError(takeoverErr),
		)
	}

	insertErr := tx.QueryRowContext(ctx, `
		WITH db_time AS (SELECT clock_timestamp() AS at)
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, claim_access, state, claimed_at, lease_expires_at
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,'active',db_time.at,db_time.at + $8::interval
		FROM db_time
		ON CONFLICT (tenant_id, operation_id, head_receipt_digest) DO NOTHING
		RETURNING lease_expires_at
	`, claim.TenantID, claim.OperationID, claim.HeadSequence, claim.HeadReceiptDigest,
		tokenDigest, claim.Owner, claim.Access, ttl.String()).Scan(&claim.ExpiresAt)
	if errors.Is(insertErr, sql.ErrNoRows) {
		return ErrExecutionClaimHeld
	}
	if insertErr != nil {
		return fmt.Errorf(
			"providercontrol: acquire execution claim: %w",
			normalizeLeaseFenceDatabaseError(insertErr),
		)
	}
	return nil
}

// RenewExecutionClaim extends a live claim. A missing, stale, expired, or
// superseded token is reported as a fail-closed loss of execution authority.
func (l *PostgresLedger) RenewExecutionClaim(ctx context.Context, claim ExecutionClaim, ttl time.Duration) (ExecutionClaim, error) {
	if l == nil || l.db == nil || !validClaim(claim) {
		return ExecutionClaim{}, fmt.Errorf("%w: execution claim is invalid", ErrInvalidRequest)
	}
	ttl, err := normalizeClaimTTL(ttl)
	if err != nil {
		return ExecutionClaim{}, err
	}
	renewed := claim
	err = l.withTenant(ctx, claim.TenantID, func(tx *sql.Tx) error {
		if generationErr := validateClaimGenerationTx(ctx, tx, claim); generationErr != nil {
			return generationErr
		}
		if contextErr := setExecutionClaimContextTx(ctx, tx, claim); contextErr != nil {
			return contextErr
		}
		renewErr := tx.QueryRowContext(ctx, `
			WITH db_time AS (SELECT clock_timestamp() AS at)
			UPDATE provider_operation_execution_claims
			SET lease_expires_at = db_time.at + $6::interval
			FROM db_time
			WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
			  AND claim_token_digest = $4 AND claim_owner = $5
			  AND state = 'active' AND lease_expires_at > db_time.at
			RETURNING lease_expires_at
		`, claim.TenantID, claim.OperationID, claim.HeadReceiptDigest,
			executionClaimTokenDigest(claim.Token), claim.Owner, ttl.String()).Scan(&renewed.ExpiresAt)
		if errors.Is(renewErr, sql.ErrNoRows) {
			return ErrExecutionClaimLost
		}
		if renewErr != nil {
			return fmt.Errorf(
				"providercontrol: renew execution claim: %w",
				normalizeLeaseFenceDatabaseError(renewErr),
			)
		}
		return nil
	})
	if err != nil {
		return ExecutionClaim{}, err
	}
	return renewed, nil
}

// ReleaseExecutionClaim abandons a live head capability after validation or
// adapter result failure. It never removes an AMO dispatch guard; that head
// remains manual-reconcile-only and cannot be rearmed by claim release.
func (l *PostgresLedger) ReleaseExecutionClaim(ctx context.Context, claim ExecutionClaim) error {
	if l == nil || l.db == nil || !validClaim(claim) {
		return fmt.Errorf("%w: execution claim is invalid", ErrInvalidRequest)
	}
	return l.withTenant(ctx, claim.TenantID, func(tx *sql.Tx) error {
		if generationErr := validateClaimGenerationTx(ctx, tx, claim); generationErr != nil {
			return generationErr
		}
		if contextErr := setExecutionClaimContextTx(ctx, tx, claim); contextErr != nil {
			return contextErr
		}
		var releasedAt time.Time
		err := tx.QueryRowContext(ctx, `
			WITH db_time AS (SELECT clock_timestamp() AS at)
			UPDATE provider_operation_execution_claims
			SET state = 'released', released_at = db_time.at
			FROM db_time
			WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
			  AND claim_token_digest = $4 AND claim_owner = $5
			  AND state = 'active' AND lease_expires_at > db_time.at
			RETURNING released_at
		`, claim.TenantID, claim.OperationID, claim.HeadReceiptDigest,
			executionClaimTokenDigest(claim.Token), claim.Owner).Scan(&releasedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrExecutionClaimLost
		}
		if err != nil {
			return fmt.Errorf(
				"providercontrol: release execution claim: %w",
				normalizeLeaseFenceDatabaseError(err),
			)
		}
		return nil
	})
}

func validateClaimGenerationTx(ctx context.Context, tx *sql.Tx, claim ExecutionClaim) error {
	record, err := loadOperationCustodyTx(ctx, tx, claim.TenantID, claim.OperationID, false)
	if err != nil {
		return err
	}
	if record.Command.ResourceGenerationID != claim.ResourceGenerationID ||
		record.Head.Sequence != claim.HeadSequence ||
		record.Head.ReceiptDigest != claim.HeadReceiptDigest {
		return ErrExecutionClaimLost
	}
	return nil
}

// loadOperationCustodyTx admits only the immutable operation/server generation
// that still owns an in-flight result. Cancellation or absent intent may begin
// while a provider call is running, so the exact claim may be renewed,
// released, or journalled; a terminal tombstone or identity drift still wins.
func loadOperationCustodyTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID string,
	forUpdate bool,
) (OperationRecord, error) {
	record, err := loadOperationProjectionTx(
		ctx, tx, tenantID, operationID, forUpdate, false,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	fence, err := loadLeaseExecutionFenceTx(ctx, tx, record.Command, true)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.RuntimeServerGeneration < 1 ||
		fence.ServerGeneration < record.RuntimeServerGeneration {
		return OperationRecord{}, fmt.Errorf(
			"%w: operation runtime server generation %d is newer than custody generation %d",
			ErrLeaseFence,
			record.RuntimeServerGeneration,
			fence.ServerGeneration,
		)
	}
	return record, nil
}

// AppendClaimedReceipt appends an adapter result only while its exact claim is
// live. Releasing the claim and the receipt-head compare-and-swap are one
// transaction, so a competing worker cannot execute the next head early.
func (l *PostgresLedger) AppendClaimedReceipt(
	ctx context.Context,
	command providerexecutor.Command,
	previous, next providerexecutor.Receipt,
	claim ExecutionClaim,
) error {
	if l == nil || l.db == nil {
		return fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	if !validClaim(claim) || claim.TenantID != command.TenantID || claim.OperationID != command.OperationID ||
		claim.ResourceGenerationID != command.ResourceGenerationID ||
		claim.HeadSequence != previous.Sequence || claim.HeadReceiptDigest != previous.ReceiptDigest {
		return fmt.Errorf("%w: execution claim does not bind the append head", ErrInvalidRequest)
	}
	if validationErr := providerexecutor.ValidateAppend(ctx, command, previous, next, l.verifier); validationErr != nil {
		return validationErr
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("providercontrol: encode receipt: %w", err)
	}
	return l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		return l.appendClaimedReceiptTx(ctx, tx, command, previous, next, claim, nextJSON)
	})
}

func (l *PostgresLedger) appendClaimedReceiptTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
	claim ExecutionClaim,
	nextJSON []byte,
) error {
	current, loadErr := loadOperationProjectionTx(
		ctx,
		tx,
		command.TenantID,
		command.OperationID,
		true,
		false,
	)
	if loadErr != nil {
		return loadErr
	}
	if replayErr := providerexecutor.ValidateReplay(current.Command, command); replayErr != nil {
		return replayErr
	}
	if validationErr := validateOperationRecord(ctx, current, l.verifier); validationErr != nil {
		return validationErr
	}
	if current.Head.Sequence != previous.Sequence || current.Head.ReceiptDigest != previous.ReceiptDigest {
		return fmt.Errorf("%w: receipt head changed", ErrLedgerConflict)
	}
	if fenceErr := requireClaimedReceiptCustodyFenceTx(ctx, tx, current, claim); fenceErr != nil {
		return fenceErr
	}
	if consumeErr := consumeExecutionClaimTx(ctx, tx, claim); consumeErr != nil {
		return consumeErr
	}
	return l.persistAcceptedReceiptTx(
		ctx, tx, current, previous, next, nextJSON, "advance claimed operation head",
	)
}

func (l *PostgresLedger) persistAcceptedReceiptTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
	nextJSON []byte,
	operation string,
) error {
	if l.runtimeProjection == nil {
		return fmt.Errorf("%w: provider receipt runtime projection is not configured", ErrInvalidRequest)
	}
	command := current.Command
	if receiptErr := insertReceiptTx(ctx, tx, command.TenantID, next, nextJSON); receiptErr != nil {
		return receiptErr
	}
	if resourceErr := persistReceiptResourcesTx(ctx, tx, command, next); resourceErr != nil {
		return resourceErr
	}
	prepared, prepareErr := l.runtimeProjection.PrepareTx(ctx, tx, current, previous, next)
	if prepareErr != nil {
		return prepareErr
	}
	if headErr := advanceOperationHeadTx(ctx, tx, command, previous, next, operation); headErr != nil {
		return headErr
	}
	return l.runtimeProjection.ApplyTx(ctx, tx, prepared)
}

func consumeExecutionClaimTx(ctx context.Context, tx *sql.Tx, claim ExecutionClaim) error {
	if contextErr := setExecutionClaimContextTx(ctx, tx, claim); contextErr != nil {
		return contextErr
	}
	var consumedAt time.Time
	consumeErr := tx.QueryRowContext(ctx, `
		WITH db_time AS (SELECT clock_timestamp() AS at)
		UPDATE provider_operation_execution_claims
		SET state = 'consumed', released_at = db_time.at
		FROM db_time
		WHERE tenant_id = $1 AND operation_id = $2 AND head_receipt_digest = $3
		  AND claim_token_digest = $4 AND claim_owner = $5
		  AND state = 'active' AND lease_expires_at > db_time.at
		RETURNING released_at
	`, claim.TenantID, claim.OperationID, claim.HeadReceiptDigest,
		executionClaimTokenDigest(claim.Token), claim.Owner).Scan(&consumedAt)
	if errors.Is(consumeErr, sql.ErrNoRows) {
		return ErrExecutionClaimLost
	}
	if consumeErr != nil {
		return fmt.Errorf("providercontrol: consume execution claim: %w", consumeErr)
	}
	return nil
}

func setExecutionClaimContextTx(ctx context.Context, tx *sql.Tx, claim ExecutionClaim) error {
	if !validClaimCapability(claim) {
		return fmt.Errorf("%w: execution claim is invalid", ErrInvalidRequest)
	}
	_, contextErr := tx.ExecContext(ctx, `
		SELECT set_config($1, $2, true), set_config($3, $4, true)
	`, claimCapabilityContextKey, claim.Token, claimOwnerContextKey, claim.Owner)
	if contextErr != nil {
		return fmt.Errorf("providercontrol: bind execution claim transaction context: %w", contextErr)
	}
	return nil
}

func executionClaimTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (l *PostgresLedger) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	return withTenantDatabase(ctx, l.db, tenantID, fn)
}

func withTenantDatabase(ctx context.Context, database *sql.DB, tenantID string, fn func(*sql.Tx) error) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("%w: tenant id required", ErrInvalidRequest)
	}
	if database == nil {
		return fmt.Errorf("%w: database is required", ErrInvalidRequest)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, tenantContextKey, tenantID); err != nil {
		return fmt.Errorf("providercontrol: set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

type leaseExecutionFence struct {
	LeaseRevision        uint64
	RuntimeServerID      string
	ResourceGenerationID string
	LeaseDesiredState    string
	LeaseCancelledAt     *time.Time
	ServerLeaseID        string
	ServerRevision       int64
	ServerGeneration     int64
	ServerProviderRef    string
	ServerDesiredState   string
	ServerLifecycleState string
	ServerDecommissioned *time.Time
}

type leaseExecutionFenceRow struct {
	leaseRevision        sql.NullInt64
	runtimeServerID      sql.NullString
	resourceGenerationID sql.NullString
	leaseDesiredState    string
	cancelledAt          sql.NullTime
	serverLeaseID        sql.NullString
	serverRevision       sql.NullInt64
	serverGeneration     sql.NullInt64
	serverProviderRef    sql.NullString
	serverDesiredState   string
	serverLifecycleState string
	decommissionedAt     sql.NullTime
}

func loadLeaseExecutionFenceTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	allowClaimedResultAfterTeardown ...bool,
) (leaseExecutionFence, error) {
	if authorityErr := requireLeaseExecutionAuthorityTx(ctx, tx, command); authorityErr != nil {
		return leaseExecutionFence{}, authorityErr
	}
	row, rowErr := loadLeaseExecutionFenceRowTx(ctx, tx, command)
	if rowErr != nil {
		return leaseExecutionFence{}, rowErr
	}
	fence, bindingErr := row.validatedFence(command)
	if bindingErr != nil {
		return leaseExecutionFence{}, bindingErr
	}
	if operationErr := validateLeaseExecutionFenceOperation(command, allowClaimedResultAfterTeardown, fence); operationErr != nil {
		return leaseExecutionFence{}, operationErr
	}
	return fence, nil
}

func requireLeaseExecutionAuthorityTx(ctx context.Context, tx *sql.Tx, command providerexecutor.Command) error {
	// Execution authority is immutable after binding. A row lock adds no
	// protection and would make the SELECT-only authority privilege unusable.
	var authority ExecutionAuthority
	err := tx.QueryRowContext(ctx, `
		SELECT execution_authority
		FROM runtime_lease_execution_authorities
		WHERE tenant_id = $1 AND lease_id = $2
	`, command.TenantID, command.LeaseID).Scan(&authority)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"%w: lease %q has no native execution authority",
			ErrExecutionAuthority,
			command.LeaseID,
		)
	}
	if err != nil {
		return fmt.Errorf("providercontrol: load lease execution authority: %w", err)
	}
	return validateExecutionAuthority(authority)
}

func loadLeaseExecutionFenceRowTx(ctx context.Context, tx *sql.Tx, command providerexecutor.Command) (leaseExecutionFenceRow, error) {
	// BeginOperation persists a requested command but performs no provider side
	// effect. Keep the mutable canonical server/tombstone row locked through the
	// insert. Lease teardown is checked again by the fail-closed claim gate
	// before any adapter can run, while immutable lease identity needs no lock.
	var row leaseExecutionFenceRow
	err := tx.QueryRowContext(ctx, `
		SELECT
			runtime_lease.lease_revision,
			runtime_lease.server_id,
			runtime_lease.resource_generation_id::text,
			runtime_lease.desired_state,
			runtime_lease.cancelled_at,
			server.lease_id,
			server.revision,
			server.generation,
			server.provider_ref,
			server.desired_state,
			server.lifecycle_state,
			server.decommissioned_at
		FROM techstack_vm_leases AS runtime_lease
		JOIN servers AS server
		  ON server.tenant_id = runtime_lease.tenant_id
		 AND server.id = runtime_lease.server_id
		WHERE runtime_lease.tenant_id = $1 AND runtime_lease.id = $2
		FOR SHARE OF server
	`, command.TenantID, command.LeaseID).Scan(
		&row.leaseRevision,
		&row.runtimeServerID,
		&row.resourceGenerationID,
		&row.leaseDesiredState,
		&row.cancelledAt,
		&row.serverLeaseID,
		&row.serverRevision,
		&row.serverGeneration,
		&row.serverProviderRef,
		&row.serverDesiredState,
		&row.serverLifecycleState,
		&row.decommissionedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return leaseExecutionFenceRow{}, fmt.Errorf(
			"%w: lease %q has no canonical runtime binding",
			ErrLeaseFence,
			command.LeaseID,
		)
	}
	if err != nil {
		return leaseExecutionFenceRow{}, fmt.Errorf("providercontrol: load runtime lease fence: %w", err)
	}
	return row, nil
}

func (row leaseExecutionFenceRow) validatedFence(command providerexecutor.Command) (leaseExecutionFence, error) {
	commandProviderID := strings.ToLower(strings.TrimSpace(command.ProviderID))
	liveProviderID := strings.ToLower(strings.TrimSpace(row.serverProviderRef.String))
	if !row.leaseRevision.Valid || row.leaseRevision.Int64 < 1 || uint64(row.leaseRevision.Int64) != command.LeaseRevision ||
		!row.runtimeServerID.Valid || row.runtimeServerID.String != command.RuntimeServerID ||
		!row.resourceGenerationID.Valid || row.resourceGenerationID.String != command.ResourceGenerationID ||
		!row.serverLeaseID.Valid || row.serverLeaseID.String != command.LeaseID ||
		!row.serverRevision.Valid || row.serverRevision.Int64 < 1 ||
		!row.serverGeneration.Valid || row.serverGeneration.Int64 < 1 ||
		!row.serverProviderRef.Valid || liveProviderID == "" ||
		commandProviderID == "" || liveProviderID != commandProviderID {
		return leaseExecutionFence{}, fmt.Errorf(
			"%w: lease %q revision, server, provider, resource generation, or server binding changed",
			ErrLeaseFence,
			command.LeaseID,
		)
	}
	return leaseExecutionFence{
		LeaseRevision: command.LeaseRevision, RuntimeServerID: row.runtimeServerID.String,
		ResourceGenerationID: row.resourceGenerationID.String,
		LeaseDesiredState:    row.leaseDesiredState, LeaseCancelledAt: nullableFenceTime(row.cancelledAt),
		ServerLeaseID: row.serverLeaseID.String, ServerRevision: row.serverRevision.Int64,
		ServerGeneration: row.serverGeneration.Int64, ServerProviderRef: row.serverProviderRef.String,
		ServerDesiredState: row.serverDesiredState, ServerLifecycleState: row.serverLifecycleState,
		ServerDecommissioned: nullableFenceTime(row.decommissionedAt),
	}, nil
}

func validateLeaseExecutionFenceOperation(
	command providerexecutor.Command,
	allowClaimedResultAfterTeardown []bool,
	fence leaseExecutionFence,
) error {
	terminalTombstone := fence.ServerLifecycleState == "decommissioned" || fence.ServerDecommissioned != nil
	if terminalTombstone && !allowsTerminalOperationContinuation(command, allowClaimedResultAfterTeardown) {
		return fmt.Errorf(
			"%w: server %q has a terminal decommission tombstone",
			ErrLeaseFence,
			command.RuntimeServerID,
		)
	}
	teardownRequested := fence.LeaseCancelledAt != nil || fence.LeaseDesiredState == "absent" ||
		fence.ServerDesiredState == "absent" || fence.ServerLifecycleState == "decommissioning"
	if command.Operation == providerexecutor.OperationDecommission {
		if !teardownRequested {
			return fmt.Errorf(
				"%w: lease %q has no cancellation, absent intent, or server tombstone for decommission",
				ErrLeaseFence,
				command.LeaseID,
			)
		}
		return nil
	}
	if teardownRequested && !(len(allowClaimedResultAfterTeardown) == 1 && allowClaimedResultAfterTeardown[0]) {
		return fmt.Errorf(
			"%w: lease %q is cancelled, absent, or server-decommissioned for %s",
			ErrLeaseFence,
			command.LeaseID,
			command.Operation,
		)
	}
	return nil
}

func allowsTerminalOperationContinuation(
	command providerexecutor.Command,
	allowClaimedResultAfterTeardown []bool,
) bool {
	// A decommission command can only mutate the immutable target graph sealed
	// into its operation. Keeping that exact operation runnable after a local
	// tombstone lets it prove provider absence and seal the missing capacity
	// release; it cannot prepare, create, or discover an unbound resource.
	if command.Operation == providerexecutor.OperationDecommission {
		return true
	}
	return command.Operation == providerexecutor.OperationProvision &&
		len(allowClaimedResultAfterTeardown) == 1 &&
		allowClaimedResultAfterTeardown[0]
}

func requireClaimedReceiptCustodyFenceTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	claim ExecutionClaim,
) error {
	if contextErr := setExecutionClaimContextTx(ctx, tx, claim); contextErr != nil {
		return contextErr
	}
	var live bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM provider_operation_execution_claims
			WHERE tenant_id = $1
			  AND operation_id = $2
			  AND head_sequence = $3
			  AND head_receipt_digest = $4
			  AND claim_token_digest = $5
			  AND claim_owner = $6
			  AND state = 'active'
			  AND lease_expires_at > clock_timestamp()
		)
	`, claim.TenantID, claim.OperationID, int64(claim.HeadSequence), // #nosec G115 -- AppendClaimedReceipt validates claim capability against MaxJSONSafeInteger.
		claim.HeadReceiptDigest, executionClaimTokenDigest(claim.Token),
		claim.Owner).Scan(&live); err != nil {
		return fmt.Errorf("providercontrol: validate claimed receipt custody: %w", err)
	}
	if !live {
		return ErrExecutionClaimLost
	}
	fence, fenceErr := loadLeaseExecutionFenceTx(ctx, tx, record.Command, true)
	if fenceErr != nil {
		return fenceErr
	}
	if record.RuntimeServerGeneration < 1 ||
		fence.ServerGeneration < record.RuntimeServerGeneration {
		return fmt.Errorf(
			"%w: claimed receipt runtime server generation %d is newer than live generation %d",
			ErrLeaseFence,
			record.RuntimeServerGeneration,
			fence.ServerGeneration,
		)
	}
	return nil
}

func nullableFenceTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

func insertDesiredSpecTx(ctx context.Context, tx *sql.Tx, spec DesiredSpecRevision) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO provider_desired_spec_revisions (
			tenant_id, lease_id, revision, spec_ref, spec_digest, spec_json, created_at
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		ON CONFLICT (tenant_id, lease_id, revision) DO UPDATE
		SET spec_ref = EXCLUDED.spec_ref
		WHERE provider_desired_spec_revisions.spec_ref = EXCLUDED.spec_ref
		  AND provider_desired_spec_revisions.spec_digest = EXCLUDED.spec_digest
		  AND provider_desired_spec_revisions.spec_json = EXCLUDED.spec_json
		  AND provider_desired_spec_revisions.created_at = EXCLUDED.created_at
	`, spec.TenantID, spec.LeaseID, spec.Revision, spec.Ref, spec.Digest, []byte(spec.Payload), spec.CreatedAt)
	if err != nil {
		return fmt.Errorf("providercontrol: persist desired spec revision: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: desired spec revision is immutable", ErrLedgerConflict)
	}
	return nil
}

func insertReceiptTx(ctx context.Context, tx *sql.Tx, tenantID string, receipt providerexecutor.Receipt, payload []byte) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_receipts (
			tenant_id, operation_id, sequence, previous_receipt_digest,
			receipt_digest, status, phase, receipt_json, issued_at
		) VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8::jsonb,$9)
	`, tenantID, receipt.OperationID, receipt.Sequence, receipt.PreviousReceiptDigest,
		receipt.ReceiptDigest, receipt.Status, receipt.Phase, payload, receipt.IssuedAt)
	if err != nil {
		return fmt.Errorf("providercontrol: persist receipt: %w", err)
	}
	return nil
}

func upsertResourceTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	operationID string,
	sequence uint64,
	resource providerexecutor.ResourceBinding,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_resources (
			tenant_id, operation_id, binding_id, kind, native_ref,
			parent_binding_id, ownership_hash, disposition, observation,
			cleanup_state, first_receipt_sequence, last_receipt_sequence
		) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$11)
		ON CONFLICT (tenant_id, operation_id, binding_id) DO UPDATE
		SET observation = EXCLUDED.observation,
			cleanup_state = EXCLUDED.cleanup_state,
			last_receipt_sequence = EXCLUDED.last_receipt_sequence
		WHERE provider_operation_resources.kind = EXCLUDED.kind
		  AND provider_operation_resources.native_ref = EXCLUDED.native_ref
		  AND provider_operation_resources.parent_binding_id IS NOT DISTINCT FROM EXCLUDED.parent_binding_id
		  AND provider_operation_resources.ownership_hash = EXCLUDED.ownership_hash
		  AND provider_operation_resources.disposition = EXCLUDED.disposition
	`, tenantID, operationID, resource.BindingID, resource.Kind, resource.NativeRef,
		resource.ParentBindingID, resource.OwnershipHash, resource.Disposition,
		resource.Observation, resource.Cleanup, sequence)
	if err != nil {
		return fmt.Errorf("providercontrol: persist resource %s: %w", resource.BindingID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: resource identity changed for %s", ErrLedgerConflict, resource.BindingID)
	}
	return nil
}

func insertEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	operationID string,
	bindingID string,
	sequence uint64,
	evidence providerexecutor.Evidence,
) error {
	payload, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("providercontrol: encode evidence: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_evidence (
			tenant_id, operation_id, binding_id, evidence_ref, evidence_digest,
			attestation_ref, attestation_digest, observation, source,
			definitive, collected_at, first_receipt_sequence, evidence_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb)
		ON CONFLICT (tenant_id, operation_id, evidence_ref) DO UPDATE
		SET evidence_ref = EXCLUDED.evidence_ref
		WHERE provider_operation_evidence.binding_id = EXCLUDED.binding_id
		  AND provider_operation_evidence.evidence_digest = EXCLUDED.evidence_digest
		  AND provider_operation_evidence.attestation_ref = EXCLUDED.attestation_ref
		  AND provider_operation_evidence.attestation_digest = EXCLUDED.attestation_digest
		  AND provider_operation_evidence.evidence_json = EXCLUDED.evidence_json
	`, tenantID, operationID, bindingID, evidence.Ref, evidence.Digest,
		evidence.AttestationRef, evidence.AttestationDigest, evidence.Observation,
		evidence.Source, evidence.Definitive, evidence.CollectedAt, sequence, payload)
	if err != nil {
		return fmt.Errorf("providercontrol: persist evidence %s: %w", evidence.Ref, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: evidence envelope changed for %s", ErrLedgerConflict, evidence.Ref)
	}
	return nil
}

func validateDesiredSpecCustody(command providerexecutor.Command, spec *DesiredSpecRevision) error {
	if command.DesiredSpecHash == "" {
		if spec != nil {
			return fmt.Errorf("%w: unexpected desired spec custody record", ErrInvalidRequest)
		}
		return nil
	}
	if spec == nil || spec.TenantID != command.TenantID || spec.LeaseID != command.LeaseID ||
		spec.Ref != command.DesiredSpecRef || spec.Digest != command.DesiredSpecHash ||
		spec.Revision == 0 || spec.Revision > providerexecutor.MaxJSONSafeInteger || spec.CreatedAt.IsZero() {
		return fmt.Errorf("%w: desired spec custody does not match command", ErrInvalidRequest)
	}
	canonical, err := canonicalJSON(spec.Payload)
	if err != nil || string(canonical) != string(spec.Payload) ||
		sha256Digest(canonical) != spec.Digest {
		return fmt.Errorf("%w: desired spec custody payload or digest is invalid", ErrInvalidRequest)
	}
	return nil
}

func loadOperationTx(ctx context.Context, tx *sql.Tx, tenantID, operationID string, forUpdate bool) (OperationRecord, error) {
	return loadOperationProjectionTx(ctx, tx, tenantID, operationID, forUpdate, true)
}

// loadOperationAdmissionReplayTx validates the immutable operation envelope
// and durable projections without treating the current mutable lease head as
// execution admission. Claims and appends continue to use loadOperationTx and
// therefore retain the live lease/cancellation/tombstone fence.
func loadOperationAdmissionReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	operationID string,
) (OperationRecord, error) {
	return loadOperationProjectionTx(ctx, tx, tenantID, operationID, false, false)
}

func loadOperationProjectionTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	operationID string,
	forUpdate bool,
	requireLiveLeaseFence bool,
) (OperationRecord, error) {
	query := `
		SELECT o.tenant_id, o.operation_id, o.lease_id, o.operation,
		       o.idempotency_key, o.adapter_id, o.command_digest, o.ledger_revision,
		       o.provision_dispatch_mode, o.requested_at,
		       o.head_sequence, o.head_receipt_digest, o.status, o.phase,
		       r.sequence, r.receipt_digest, r.status, r.phase,
		       o.command_json::text, r.receipt_json::text,
		       EXISTS (
		           SELECT 1 FROM provider_provision_dispatch_guards AS dispatch_guard
		           WHERE dispatch_guard.tenant_id = o.tenant_id
		             AND dispatch_guard.lease_id = o.lease_id
		             AND dispatch_guard.resource_generation_id =
		                 (o.command_json #>> '{command,resource_generation_id}')::uuid
		             AND (
		                dispatch_guard.operation_id <> o.operation_id
		                OR dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
		                OR dispatch_guard.guard_origin = 'migration_quarantine'
		             )
		       ) AS dispatch_guarded,
		       EXISTS (
		           SELECT 1
		           FROM provider_operation_resource_free_terminalizations AS terminalization
		           WHERE terminalization.tenant_id = o.tenant_id
		             AND terminalization.operation_id = o.operation_id
		             AND terminalization.head_sequence = o.head_sequence
		             AND terminalization.head_receipt_digest = o.head_receipt_digest
		       ) AS resource_free_terminalized
		FROM provider_operations o
		JOIN provider_operation_receipts r
		  ON r.tenant_id = o.tenant_id
		 AND r.operation_id = o.operation_id
		 AND r.sequence = o.head_sequence
		WHERE o.tenant_id = $1 AND o.operation_id = $2
	`
	if forUpdate {
		query += " FOR UPDATE OF o"
	}
	stored, commandPayload, receiptPayload, scanErr := scanStoredOperation(
		tx.QueryRowContext(ctx, query, tenantID, operationID),
	)
	if scanErr != nil {
		return OperationRecord{}, scanErr
	}
	var envelope operationEnvelope
	if decodeErr := json.Unmarshal(commandPayload, &envelope); decodeErr != nil {
		return OperationRecord{}, fmt.Errorf("providercontrol: decode operation envelope: %w", decodeErr)
	}
	if envelope.SchemaVersion != operationEnvelopeVersion {
		return OperationRecord{}, fmt.Errorf("%w: unsupported or legacy operation envelope %q", ErrExecutionAuthority, envelope.SchemaVersion)
	}
	record := OperationRecord{
		ExecutionAuthority:      envelope.ExecutionAuthority,
		ExecutionProfile:        envelope.ExecutionProfile,
		ProvisionDispatch:       ProvisionDispatchMode(stored.provisionDispatch),
		RuntimeServerGeneration: envelope.RuntimeServerGeneration,
		Command:                 envelope.Command,
	}
	if decodeErr := json.Unmarshal(receiptPayload, &record.Head); decodeErr != nil {
		return OperationRecord{}, fmt.Errorf("providercontrol: decode receipt: %w", decodeErr)
	}
	record.AutomationState = deriveOperationAutomationState(record, stored.dispatchGuarded)
	record.AutomationReasonCode = deriveOperationAutomationReasonCode(record, stored.dispatchGuarded)
	if stored.resourceFreeTerminalized {
		record.AutomationState = OperationAutomationComplete
		record.AutomationReasonCode = ""
	}
	if requireLiveLeaseFence {
		fence, err := loadLeaseExecutionFenceTx(ctx, tx, record.Command)
		if err != nil {
			return OperationRecord{}, err
		}
		if record.RuntimeServerGeneration < 1 ||
			record.RuntimeServerGeneration != fence.ServerGeneration {
			return OperationRecord{}, fmt.Errorf(
				"%w: operation runtime server generation %d does not match live generation %d",
				ErrLeaseFence,
				record.RuntimeServerGeneration,
				fence.ServerGeneration,
			)
		}
	}
	if projectionErr := validateStoredOperationProjection(stored, tenantID, operationID, record); projectionErr != nil {
		return OperationRecord{}, projectionErr
	}
	return record, nil
}

type storedOperationProjection struct {
	tenantID                 string
	operationID              string
	leaseID                  string
	operation                string
	idempotencyKey           string
	adapterID                string
	commandDigest            string
	ledgerRevision           int64
	provisionDispatch        string
	requestedAt              time.Time
	headSequence             int64
	headDigest               string
	operationStatus          string
	operationPhase           string
	receiptSequence          int64
	receiptDigest            string
	receiptStatus            string
	receiptPhase             string
	dispatchGuarded          bool
	resourceFreeTerminalized bool
}

func scanStoredOperation(row *sql.Row) (storedOperationProjection, []byte, []byte, error) {
	var stored storedOperationProjection
	var commandPayload, receiptPayload []byte
	scanErr := row.Scan(
		&stored.tenantID,
		&stored.operationID,
		&stored.leaseID,
		&stored.operation,
		&stored.idempotencyKey,
		&stored.adapterID,
		&stored.commandDigest,
		&stored.ledgerRevision,
		&stored.provisionDispatch,
		&stored.requestedAt,
		&stored.headSequence,
		&stored.headDigest,
		&stored.operationStatus,
		&stored.operationPhase,
		&stored.receiptSequence,
		&stored.receiptDigest,
		&stored.receiptStatus,
		&stored.receiptPhase,
		&commandPayload,
		&receiptPayload,
		&stored.dispatchGuarded,
		&stored.resourceFreeTerminalized,
	)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return storedOperationProjection{}, nil, nil, ErrOperationNotFound
	}
	if scanErr != nil {
		return storedOperationProjection{}, nil, nil, fmt.Errorf("providercontrol: load operation: %w", scanErr)
	}
	stored.requestedAt = stored.requestedAt.UTC()
	return stored, commandPayload, receiptPayload, nil
}

func validateStoredOperationProjection(
	stored storedOperationProjection,
	tenantID string,
	operationID string,
	record OperationRecord,
) error {
	if err := requireExecutionAuthority(record, ExecutionAuthorityTechstackProviderControl); err != nil {
		return err
	}
	if err := validateExecutionProfileSnapshot(record.ExecutionProfile, record.Command); err != nil {
		return fmt.Errorf("%w: stored execution profile is invalid: %v", ErrLedgerConflict, err)
	}
	if err := validateProvisionDispatchMode(record.Command.Operation, record.ProvisionDispatch); err != nil {
		return fmt.Errorf("%w: stored provision dispatch mode is invalid: %v", ErrLedgerConflict, err)
	}
	if record.Head.Sequence > providerexecutor.MaxJSONSafeInteger ||
		record.Command.LedgerRevision > providerexecutor.MaxJSONSafeInteger {
		return fmt.Errorf("%w: operation head projection does not match immutable payloads", ErrLedgerConflict)
	}
	expected := storedOperationProjection{
		tenantID:                 tenantID,
		operationID:              operationID,
		leaseID:                  record.Command.LeaseID,
		operation:                string(record.Command.Operation),
		idempotencyKey:           record.Command.IdempotencyKey,
		adapterID:                record.Command.AdapterID,
		commandDigest:            record.Command.CommandDigest,
		ledgerRevision:           int64(record.Command.LedgerRevision),
		provisionDispatch:        string(record.ProvisionDispatch),
		requestedAt:              record.Command.RequestedAt,
		headSequence:             int64(record.Head.Sequence),
		headDigest:               record.Head.ReceiptDigest,
		operationStatus:          string(record.Head.Status),
		operationPhase:           string(record.Head.Phase),
		receiptSequence:          int64(record.Head.Sequence),
		receiptDigest:            record.Head.ReceiptDigest,
		receiptStatus:            string(record.Head.Status),
		receiptPhase:             string(record.Head.Phase),
		dispatchGuarded:          stored.dispatchGuarded,
		resourceFreeTerminalized: stored.resourceFreeTerminalized,
	}
	if stored != expected || stored.headSequence <= 0 || stored.receiptSequence <= 0 {
		return fmt.Errorf("%w: operation head projection does not match immutable payloads", ErrLedgerConflict)
	}
	return nil
}

var _ Ledger = (*PostgresLedger)(nil)
