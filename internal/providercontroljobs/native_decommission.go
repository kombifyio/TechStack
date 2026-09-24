package providercontroljobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	nativeDecommissionSource      = "provider_control_decommission_intent"
	nativeDecommissionWaitReason  = "waiting_provider_decommission"
	nativeDecommissionResumeDelay = 5 * time.Second
	maxNativeDecommissionBatch    = 100

	// maxNativeDecommissionAttempts bounds how many decommission operations one
	// resource generation may open within nativeDecommissionAttemptWindow.
	// Deleting an already-deleted resource is harmless -- the adapter reads
	// before it deletes and treats a provider 404 as absence -- but an unbounded
	// retry would let a stuck job open a new provider operation on every pass.
	// When the budget is exhausted the capacity slot stays reserved and the
	// recovery loop reports the lease: the provider has refused to confirm
	// absence repeatedly and a human has to look.
	maxNativeDecommissionAttempts = 4

	// nativeDecommissionAttemptWindow is how long a failed attempt counts
	// against the budget. A budget that never re-arms turns a fixed cause into a
	// permanently billing VM: lease-5a45844e exhausted four attempts on
	// 2026-08-31 against an adapter defect, and after the defect was fixed no
	// product path could ever try again. Every later attempt still re-reads the
	// provider and deletes only exact recorded handles, so re-arming after the
	// window costs at most one bounded batch of attempts per window.
	nativeDecommissionAttemptWindow = 24 * time.Hour
)

var (
	errNativeDecommissionPending = errors.New("provider-control decommission is still converging")
	errNativeDecommissionCustody = errors.New("provider-control decommission custody is incomplete")
	// ErrNativeDecommissionFailed reports a decommission operation that reached
	// a terminal failure. It must surface as a job error rather than another
	// wait: the operation will never advance again, and an at-most-once
	// idempotency key returns that same terminal record on every retry.
	ErrNativeDecommissionFailed = errors.New("provider-control decommission failed terminally")
	// ErrNativeDecommissionAttemptsExhausted reports a generation whose
	// decommission attempt budget is spent. It is deliberately distinct from
	// ErrNativeDecommissionFailed: the last operation failed, but the reason the
	// job stops now is the budget, and the capacity slot stays reserved.
	ErrNativeDecommissionAttemptsExhausted = errors.New("provider-control decommission attempts are exhausted")
)

// NativeDecommissionPendingError is returned only after the exact
// generation-bound provider operation is durable and can be resumed by the
// provider-control reconciler. Preparation deadlocks and retry-attempt
// failures deliberately remain plain JobWaitErrors so callers cannot mistake
// a timer suggestion for persisted teardown custody.
type NativeDecommissionPendingError struct {
	Wait *jobs.JobWaitError
}

func (e *NativeDecommissionPendingError) Error() string {
	if e == nil || e.Wait == nil {
		return "provider-control decommission is pending"
	}
	return e.Wait.Error()
}

func (e *NativeDecommissionPendingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Wait
}

// NativeDecommissionConfig composes the durable provider-control teardown
// application. The same database and application instances used by Runtime
// are mandatory so teardown intent, operation claims, receipts, registry
// projection, and capacity release share one authority boundary.
type NativeDecommissionConfig struct {
	Database            *sql.DB
	Application         providercontrol.OperationApplication
	Ledger              *providercontrol.PostgresLedger
	ProvisionResolution providercontrol.ProvisionResolutionApplication
	ResolutionSubject   string
}

// NativeDecommissioner turns a stack/lease destroy request into an exact
// provider-control decommission operation. It never treats desired state,
// local cancellation, a missing workspace, or a provider delete acceptance as
// success. Only a terminal provider receipt projected into the RuntimeServer
// tombstone and capacity-release ledger produces a proof.
type NativeDecommissioner struct {
	database    *sql.DB
	application providercontrol.OperationApplication
	ledger      *providercontrol.PostgresLedger
	resolution  providercontrol.ProvisionResolutionApplication
	subject     string
	servers     *controlplane.PostgresStore
}

func NewNativeDecommissioner(cfg NativeDecommissionConfig) (*NativeDecommissioner, error) {
	cfg.ResolutionSubject = strings.TrimSpace(cfg.ResolutionSubject)
	if cfg.Database == nil || cfg.Application == nil || cfg.Ledger == nil || cfg.ProvisionResolution == nil || cfg.ResolutionSubject == "" {
		return nil, fmt.Errorf("providercontroljobs: native decommission database, application, ledger, provision resolution, and subject are required")
	}
	return &NativeDecommissioner{
		database: cfg.Database, application: cfg.Application, ledger: cfg.Ledger, resolution: cfg.ProvisionResolution,
		subject: cfg.ResolutionSubject,
		servers: controlplane.NewPostgresStore(cfg.Database),
	}, nil
}

type nativeDecommissionCandidate struct {
	TenantID           string
	StackID            string
	LeaseID            string
	LeaseRevision      uint64
	ServerID           string
	ResourceGeneration string
	ResourceDigest     string
	ProviderID         string
	OwnerID            string
	CancelledAt        time.Time
	ServerRevision     int64
	ServerGeneration   int64
	ServerLifecycle    string
	ServerDesired      string
	ProvisionOperation string
	Targets            []providerexecutor.ResourceTarget
	ResourceFree       bool
	// FailedAttempts counts the decommission operations this generation has
	// already terminalized as failed. It is what makes a retry a new operation
	// instead of a replay of the old verdict.
	FailedAttempts int
	// RecentFailedAttempts counts only the failures inside
	// nativeDecommissionAttemptWindow; it alone is charged against the budget.
	RecentFailedAttempts int
}

func (candidate nativeDecommissionCandidate) alreadyDecommissioned() bool {
	return candidate.ServerLifecycle == string(serverregistry.LifecycleDecommissioned)
}

type lockedNativeDecommissionCandidate struct {
	candidate nativeDecommissionCandidate
	leaseJSON string
}

func (d *NativeDecommissioner) DecommissionManagedLeases(
	ctx context.Context,
	req jobs.ManagedLeaseDecommissionRequest,
) (*jobs.ManagedLeaseDecommissionResult, error) {
	if d == nil || d.database == nil || d.application == nil || d.ledger == nil || d.servers == nil {
		return nil, ErrNativeDecommissionUnavailable
	}
	req.StackID = strings.TrimSpace(req.StackID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.ResourceGenerationDigest = strings.TrimSpace(req.ResourceGenerationDigest)
	if req.TenantID == "" || req.OwnerID == "" || (req.StackID == "" && req.LeaseID == "") {
		return nil, fmt.Errorf("%w: tenant, owner, and stack or lease are required", errNativeDecommissionCustody)
	}

	candidates, err := d.prepareCandidates(ctx, req)
	if err != nil {
		if retry := nativeDecommissionDeadlockWait(err); retry != nil {
			return nil, retry
		}
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no authoritative native lease matched the destroy request", errNativeDecommissionCustody)
	}

	result := &jobs.ManagedLeaseDecommissionResult{}
	for _, candidate := range candidates {
		proof, terminal, durable, proofErr := d.convergeCandidate(ctx, candidate)
		if proofErr != nil {
			if retry := nativeDecommissionDeadlockWait(proofErr); retry != nil {
				return result, retry
			}
			return result, proofErr
		}
		if !terminal {
			wait := &jobs.JobWaitError{
				Reason: nativeDecommissionWaitReason,
				Message: fmt.Sprintf(
					"Managed server %s is being removed at %s; provider absence is not confirmed yet.",
					candidate.ServerID,
					candidate.ProviderID,
				),
				ResumeAfter: nativeDecommissionResumeDelay,
				Cause:       errNativeDecommissionPending,
			}
			if !durable {
				return result, wait
			}
			return result, &NativeDecommissionPendingError{
				Wait: wait,
			}
		}
		result.LeaseIDs = append(result.LeaseIDs, candidate.LeaseID)
		result.Proofs = append(result.Proofs, proof)
		if proof.ObservedState == jobs.ManagedLeaseDecommissionObservedDecommissioned {
			result.Decommissioned++
		}
	}
	return result, nil
}

// nativeDecommissionDeadlockWait converts only PostgreSQL's transaction
// deadlock victim into the existing resumable cleanup state. PostgreSQL has
// already rolled the transaction back, so no local intent or receipt is
// partially committed. A preparation-path retry has not started a provider
// operation; a convergence-path retry replays the same sealed idempotency key,
// then can advance only through its exact durable claim and receipt head. It
// therefore cannot turn a post-provider-call append deadlock into a second
// unbound delete dispatch. Other database errors remain terminal job errors
// rather than being masked as a retry.
func nativeDecommissionDeadlockWait(err error) *jobs.JobWaitError {
	var databaseErr *pgconn.PgError
	if !errors.As(err, &databaseErr) || databaseErr == nil || databaseErr.Code != "40P01" {
		return nil
	}
	return &jobs.JobWaitError{
		Reason:      nativeDecommissionWaitReason,
		Message:     "Managed server cleanup is retrying after a concurrent control-plane transaction.",
		ResumeAfter: nativeDecommissionResumeDelay,
		Cause:       err,
	}
}

func (d *NativeDecommissioner) prepareCandidates(
	ctx context.Context,
	req jobs.ManagedLeaseDecommissionRequest,
) ([]nativeDecommissionCandidate, error) {
	tx, err := d.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: begin native decommission intent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, req.TenantID); err != nil {
		return nil, fmt.Errorf("providercontroljobs: scope native decommission tenant: %w", err)
	}

	locked, err := lockNativeDecommissionCandidates(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	prepared := make([]nativeDecommissionCandidate, 0, len(locked))
	for _, item := range locked {
		candidate, err := validateLockedNativeDecommissionCandidate(req, item)
		if err != nil {
			return nil, err
		}
		candidate, err = d.persistNativeDecommissionIntent(ctx, tx, req.TenantID, candidate)
		if err != nil {
			return nil, err
		}
		candidate, err = loadNativeDecommissionCustody(ctx, tx, req.TenantID, candidate)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, candidate)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: commit native decommission intent: %w", err)
	}
	return prepared, nil
}

// lockNativeDecommissionCandidates acquires the exact lease and RuntimeServer
// rows inside the caller's tenant-scoped transaction. No later stage may
// broaden this candidate set or reacquire authority through another store.
func lockNativeDecommissionCandidates(
	ctx context.Context,
	tx *sql.Tx,
	req jobs.ManagedLeaseDecommissionRequest,
) ([]lockedNativeDecommissionCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
		    lease.id,
		    lease.lease_revision,
		    lease.server_id,
		    lease.resource_generation_id::text,
		    LOWER(BTRIM(lease.provider_id)),
		    lease.owner_subject_id,
		    lease.lease_json::text,
		    COALESCE(lease.cancelled_at, 'epoch'::timestamptz),
		    server.revision,
		    server.generation,
		    server.lifecycle_state,
		    server.desired_state,
		    COALESCE(lease.lease_json->'metadata'->>'stack_id', '')
		FROM techstack_vm_leases AS lease
		JOIN runtime_lease_execution_authorities AS authority
		  ON authority.tenant_id = lease.tenant_id
		 AND authority.lease_id = lease.id
		 AND authority.execution_authority = 'techstack_provider_control'
		JOIN servers AS server
		  ON server.tenant_id = lease.tenant_id
		 AND server.id = lease.server_id
		 AND server.lease_id = lease.id
		WHERE lease.tenant_id = $1
		  AND (
		      (NULLIF($2, '') IS NOT NULL AND lease.id = $2)
		      OR
		      (NULLIF($2, '') IS NULL
		       AND COALESCE(lease.lease_json->'metadata'->>'stack_id', '') = $3)
		  )
		ORDER BY lease.id
		FOR UPDATE OF lease, server
	`, req.TenantID, req.LeaseID, req.StackID)
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: load native decommission candidates: %w", err)
	}
	defer rows.Close()

	locked := make([]lockedNativeDecommissionCandidate, 0, 4)
	for rows.Next() {
		var item lockedNativeDecommissionCandidate
		var leaseRevision int64
		if err := rows.Scan(
			&item.candidate.LeaseID,
			&leaseRevision,
			&item.candidate.ServerID,
			&item.candidate.ResourceGeneration,
			&item.candidate.ProviderID,
			&item.candidate.OwnerID,
			&item.leaseJSON,
			&item.candidate.CancelledAt,
			&item.candidate.ServerRevision,
			&item.candidate.ServerGeneration,
			&item.candidate.ServerLifecycle,
			&item.candidate.ServerDesired,
			&item.candidate.StackID,
		); err != nil {
			return nil, fmt.Errorf("providercontroljobs: scan native decommission candidate: %w", err)
		}
		nativeLeaseRevision, valid := positiveRevision(leaseRevision)
		if !valid {
			return nil, fmt.Errorf("%w: lease %s has no native revision", errNativeDecommissionCustody, item.candidate.LeaseID)
		}
		item.candidate.LeaseRevision = nativeLeaseRevision
		locked = append(locked, item)
		if len(locked) > maxNativeDecommissionBatch {
			return nil, fmt.Errorf("%w: destroy request exceeds %d native leases", errNativeDecommissionCustody, maxNativeDecommissionBatch)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: iterate native decommission candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: close native decommission candidates: %w", err)
	}
	return locked, nil
}

func validateLockedNativeDecommissionCandidate(
	req jobs.ManagedLeaseDecommissionRequest,
	locked lockedNativeDecommissionCandidate,
) (nativeDecommissionCandidate, error) {
	candidate := locked.candidate
	if candidate.OwnerID != req.OwnerID {
		return candidate, fmt.Errorf("%w: lease %s is not owned by the request subject", errNativeDecommissionCustody, candidate.LeaseID)
	}
	if req.StackID != "" && candidate.StackID != req.StackID {
		return candidate, fmt.Errorf("%w: lease %s does not belong to stack %s", errNativeDecommissionCustody, candidate.LeaseID, req.StackID)
	}
	var lease vmlease.Lease
	if err := json.Unmarshal([]byte(locked.leaseJSON), &lease); err != nil {
		return candidate, fmt.Errorf("%w: decode lease %s: %v", errNativeDecommissionCustody, candidate.LeaseID, err)
	}
	digest, err := vmleases.ResourceGenerationDigest(req.TenantID, lease)
	if err != nil {
		return candidate, fmt.Errorf("%w: lease %s generation digest: %v", errNativeDecommissionCustody, candidate.LeaseID, err)
	}
	candidate.ResourceDigest = digest
	candidate.TenantID = req.TenantID
	if req.ResourceGenerationDigest != "" && req.ResourceGenerationDigest != digest {
		return candidate, vmleases.ErrResourceGenerationSuperseded
	}
	if candidate.ServerLifecycle == string(serverregistry.LifecycleDecommissioning) &&
		candidate.ServerDesired != string(serverregistry.DesiredAbsent) {
		return candidate, fmt.Errorf(
			"%w: server %s has inconsistent decommissioning desired state %q",
			errNativeDecommissionCustody,
			candidate.ServerID,
			candidate.ServerDesired,
		)
	}
	return candidate, nil
}

// persistNativeDecommissionIntent writes the lease and RuntimeServer intent in
// the same transaction that still owns both row locks.
func (d *NativeDecommissioner) persistNativeDecommissionIntent(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	candidate nativeDecommissionCandidate,
) (nativeDecommissionCandidate, error) {
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return candidate, fmt.Errorf("providercontroljobs: read native decommission time: %w", err)
	}
	databaseNow = databaseNow.UTC()
	var cancellationTime time.Time
	var updatedLeaseJSON string
	err := tx.QueryRowContext(ctx, `
		WITH cancellation AS (
		    SELECT clock_timestamp() AS requested_at
		)
		UPDATE techstack_vm_leases AS lease
		SET desired_state = 'absent',
		    cancelled_at = COALESCE(lease.cancelled_at, LEAST(cancellation.requested_at, lease.valid_until)),
		    lease_json = jsonb_set(
		        jsonb_set(
		            jsonb_set(
		                lease.lease_json,
		                '{metadata}',
		                COALESCE(lease.lease_json->'metadata', '{}'::jsonb) ||
		                jsonb_build_object('resource_decommission_claim_digest', $5::text),
		                true
		            ),
		            '{desired_state}',
		            to_jsonb('archived'::text),
		            true
		        ),
		        '{cancelled_at}',
		        to_jsonb(COALESCE(lease.cancelled_at, LEAST(cancellation.requested_at, lease.valid_until))),
		        true
		    ),
		    updated_at = clock_timestamp()
		FROM cancellation
		WHERE lease.tenant_id = $1
		  AND lease.id = $2
		  AND lease.lease_revision = $3
		  AND lease.resource_generation_id = $4::uuid
		  AND (
		      NULLIF(lease.lease_json->'metadata'->>'resource_decommission_claim_digest', '') IS NULL
		      OR lease.lease_json->'metadata'->>'resource_decommission_claim_digest' = $5
		  )
		RETURNING lease.cancelled_at, lease.lease_json::text
	`, tenantID, candidate.LeaseID, candidate.LeaseRevision,
		candidate.ResourceGeneration, candidate.ResourceDigest).Scan(&cancellationTime, &updatedLeaseJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return candidate, vmleases.ErrResourceGenerationSuperseded
	}
	if err != nil {
		return candidate, fmt.Errorf("providercontroljobs: persist native decommission lease intent: %w", err)
	}
	if err := validateNativeDecommissionLeaseProjection(updatedLeaseJSON, cancellationTime, candidate.ResourceDigest); err != nil {
		return candidate, fmt.Errorf("providercontroljobs: validate native decommission lease projection: %w", err)
	}
	candidate.CancelledAt = cancellationTime.UTC()

	if serverregistry.HasDecommissionIntent(serverregistry.LifecycleState(candidate.ServerLifecycle)) {
		return candidate, nil
	}
	event, err := d.servers.ApplyServerEventTx(ctx, tx, controlplane.ServerEvent{
		TenantID: tenantID, ServerID: candidate.ServerID,
		ExpectedRevision: candidate.ServerRevision,
		Generation:       candidate.ServerGeneration,
		Authority:        controlplane.ServerEventAuthorityControlPlane,
		Source:           nativeDecommissionSource,
		SourceID:         nativeDecommissionSource + ":" + candidate.LeaseID,
		ObservedAt:       databaseNow,
		Runtime: controlplane.ServerRuntime{
			LifecycleState:      string(serverregistry.LifecycleDecommissioning),
			LifecycleReasonCode: "provider_cleanup_requested",
			DesiredState:        string(serverregistry.DesiredAbsent),
			DesiredReasonCode:   "owner_requested_absence",
		},
		Evidence: map[string]any{
			"lease_id":                   candidate.LeaseID,
			"lease_revision":             candidate.LeaseRevision,
			"resource_generation_id":     candidate.ResourceGeneration,
			"resource_generation_digest": candidate.ResourceDigest,
			"provider_id":                candidate.ProviderID,
		},
	})
	if err != nil {
		return candidate, fmt.Errorf("providercontroljobs: persist RuntimeServer decommission intent: %w", err)
	}
	if event == nil || event.Server == nil {
		return candidate, fmt.Errorf("%w: RuntimeServer decommission intent produced no aggregate", errNativeDecommissionCustody)
	}
	candidate.ServerRevision = event.Server.Revision
	candidate.ServerGeneration = event.Server.Generation
	candidate.ServerLifecycle = event.Server.LifecycleState
	candidate.ServerDesired = event.Server.DesiredState
	return candidate, nil
}

func loadNativeDecommissionCustody(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	candidate nativeDecommissionCandidate,
) (nativeDecommissionCandidate, error) {
	attempts, recentAttempts, err := loadNativeDecommissionFailedAttemptsTx(
		ctx, tx, tenantID, candidate.LeaseID, candidate.ResourceGeneration,
	)
	if err != nil {
		return candidate, err
	}
	if recentAttempts >= maxNativeDecommissionAttempts {
		return candidate, fmt.Errorf(
			"%w: lease %s exhausted %d decommission attempts within %s without proving provider absence",
			ErrNativeDecommissionAttemptsExhausted, candidate.LeaseID, recentAttempts, nativeDecommissionAttemptWindow,
		)
	}
	candidate.FailedAttempts = attempts
	candidate.RecentFailedAttempts = recentAttempts

	provisionOperation, targets, err := loadNativeGenerationTargetsTx(ctx, tx, tenantID, candidate)
	if err != nil {
		return candidate, err
	}
	candidate.ProvisionOperation = provisionOperation
	candidate.Targets = targets
	candidate.ResourceFree = len(targets) == 0
	return candidate, nil
}

func validateNativeDecommissionLeaseProjection(raw string, cancelledAt time.Time, generationDigest string) error {
	var lease vmlease.Lease
	if err := json.Unmarshal([]byte(raw), &lease); err != nil {
		return fmt.Errorf("decode updated lease: %w", err)
	}
	if lease.CancelledAt == nil || !lease.CancelledAt.Equal(cancelledAt) {
		return errors.New("updated lease JSON does not contain the indexed cancellation time")
	}
	if lease.DesiredState != vmlease.DesiredStateArchived {
		return fmt.Errorf("updated lease JSON desired state is %q, want archived", lease.DesiredState)
	}
	if strings.TrimSpace(lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest]) != strings.TrimSpace(generationDigest) {
		return errors.New("updated lease JSON does not contain the exact decommission generation claim")
	}
	return nil
}

func loadNativeGenerationTargetsTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	candidate nativeDecommissionCandidate,
) (string, []providerexecutor.ResourceTarget, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT
		    operation.operation_id,
		    resource.binding_id,
		    resource.kind,
		    resource.native_ref,
		    COALESCE(resource.parent_binding_id, ''),
		    resource.ownership_hash,
		    resource.disposition
		FROM provider_operations AS operation
		LEFT JOIN provider_operation_resources AS resource
		  ON resource.tenant_id = operation.tenant_id
		 AND resource.operation_id = operation.operation_id
		WHERE operation.tenant_id = $1
		  AND operation.lease_id = $2
		  AND operation.operation = 'provision'
		  AND operation.command_json #>> '{command,resource_generation_id}' = $3
		ORDER BY operation.operation_id, resource.binding_id
	`, tenantID, candidate.LeaseID, candidate.ResourceGeneration)
	if err != nil {
		return "", nil, fmt.Errorf("providercontroljobs: load native generation targets: %w", err)
	}
	defer rows.Close()
	operationIDs := map[string]struct{}{}
	targets := make([]providerexecutor.ResourceTarget, 0, 4)
	for rows.Next() {
		var operationID string
		var bindingID, kind, nativeRef, parentBindingID, ownershipHash, disposition sql.NullString
		if err := rows.Scan(
			&operationID, &bindingID, &kind, &nativeRef, &parentBindingID, &ownershipHash, &disposition,
		); err != nil {
			return "", nil, fmt.Errorf("providercontroljobs: scan native generation target: %w", err)
		}
		operationIDs[operationID] = struct{}{}
		if !bindingID.Valid {
			continue
		}
		targets = append(targets, providerexecutor.ResourceTarget{
			BindingID: bindingID.String, Kind: kind.String, NativeRef: nativeRef.String,
			ParentBindingID: parentBindingID.String, OwnershipHash: ownershipHash.String,
			Disposition: providerexecutor.ResourceDisposition(disposition.String),
		})
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("providercontroljobs: iterate native generation targets: %w", err)
	}
	if len(operationIDs) != 1 {
		return "", nil, fmt.Errorf(
			"%w: generation %s has %d terminal provision authorities",
			errNativeDecommissionCustody,
			candidate.ResourceGeneration,
			len(operationIDs),
		)
	}
	var operationID string
	for id := range operationIDs {
		operationID = id
	}
	return operationID, targets, nil
}

func (d *NativeDecommissioner) convergeCandidate(
	ctx context.Context,
	candidate nativeDecommissionCandidate,
) (jobs.ManagedLeaseDecommissionProof, bool, bool, error) {
	if candidate.alreadyDecommissioned() {
		// A tombstoned server used to end here unconditionally. loadTerminalProof
		// only READS managed_runtime_capacity_release_facts, so a lease whose
		// server was already decommissioned without a release fact returned
		// not-terminal on every cycle, forever - it waited for a receipt that
		// only this job could have produced, and Start() was never reached.
		// Live effect: the demo tenant's cancelled leases all bound tombstoned
		// servers and held their capacity slots permanently.
		//
		// A receipt that already exists is still the answer. When it does not,
		// fall through and open the operation, so the adapter records its
		// provider-API absence read and the ledger seals a receipt through the
		// existing provider_absence authority. No second release authority and
		// no hand-inserted facts.
		proof, terminal, err := d.loadTerminalProof(ctx, candidate)
		if err != nil || terminal {
			return proof, terminal, terminal, err
		}
	}
	if candidate.ResourceFree {
		source, err := d.application.Get(ctx, candidate.TenantID, candidate.ProvisionOperation)
		if err != nil {
			return jobs.ManagedLeaseDecommissionProof{}, false, false, err
		}
		// Only a still-pending resource-free provision can be finalized by
		// adopt-resolution. A terminal failed provision has no in-flight
		// create to adopt; calling Resolve on it 502s the owner decommission
		// and leaves the managed-runtime slot reserved forever. Those heads
		// fall through so a generation-bound decommission can read provider
		// absence and seal the existing capacity-release receipt.
		if resourceFreeProvisionCanFinalize(source) {
			terminal, handled, err := convergeResourceFreeProvision(
				ctx, source, d.ledger, d.resolution, d.subject,
			)
			if err != nil {
				return jobs.ManagedLeaseDecommissionProof{}, false, false, err
			}
			if !handled {
				return jobs.ManagedLeaseDecommissionProof{}, false, false, nil
			}
			candidate.ProvisionOperation = terminal.Command.OperationID
			proof, terminalState, proofErr := d.loadTerminalProof(ctx, candidate)
			return proof, terminalState, true, proofErr
		}
	}

	idempotencyKey := nativeDecommissionIdempotencyKey(candidate)
	operationID := providerexecutor.ComputeOperationID(
		candidate.TenantID,
		candidate.LeaseID,
		candidate.ResourceGeneration,
		providerexecutor.OperationDecommission,
		idempotencyKey,
	)
	record, err := d.application.Get(ctx, candidate.TenantID, operationID)
	if errors.Is(err, providercontrol.ErrOperationNotFound) {
		ledgerRevision, valid := positiveRevision(candidate.ServerRevision)
		if !valid {
			return jobs.ManagedLeaseDecommissionProof{}, false, false, fmt.Errorf(
				"%w: RuntimeServer %s has no native revision", errNativeDecommissionCustody, candidate.ServerID,
			)
		}
		record, _, err = d.application.Start(ctx, providercontrol.StartRequest{
			TenantID:             candidate.TenantID,
			LeaseID:              candidate.LeaseID,
			LeaseRevision:        candidate.LeaseRevision,
			RuntimeServerID:      candidate.ServerID,
			ResourceGenerationID: candidate.ResourceGeneration,
			Operation:            providerexecutor.OperationDecommission,
			IdempotencyKey:       idempotencyKey,
			LedgerRevision:       ledgerRevision,
			Targets:              candidate.Targets,
			RequestedAt:          candidate.CancelledAt.UTC().Truncate(time.Microsecond),
		})
	}
	if err != nil {
		return jobs.ManagedLeaseDecommissionProof{}, false, false, err
	}
	if nativeDecommissionTerminal(record) {
		candidate.ProvisionOperation = record.Command.OperationID
		proof, terminalState, proofErr := d.loadTerminalProof(ctx, candidate)
		return proof, terminalState, true, proofErr
	}
	if nativeDecommissionTerminallyFailed(record) {
		return jobs.ManagedLeaseDecommissionProof{}, false, true, nativeDecommissionFailure(candidate, record)
	}
	advanced, _, err := d.application.Advance(ctx, record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		if errors.Is(err, providercontrol.ErrExecutionClaimHeld) {
			return jobs.ManagedLeaseDecommissionProof{}, false, true, nil
		}
		return jobs.ManagedLeaseDecommissionProof{}, false, true, err
	}
	if nativeDecommissionTerminallyFailed(advanced) {
		return jobs.ManagedLeaseDecommissionProof{}, false, true, nativeDecommissionFailure(candidate, advanced)
	}
	if !nativeDecommissionTerminal(advanced) {
		return jobs.ManagedLeaseDecommissionProof{}, false, true, nil
	}
	candidate.ProvisionOperation = advanced.Command.OperationID
	proof, terminalState, proofErr := d.loadTerminalProof(ctx, candidate)
	return proof, terminalState, true, proofErr
}

func positiveRevision(value int64) (uint64, bool) {
	if value < 1 {
		return 0, false
	}
	// #nosec G115 -- positive int64 revisions are representable by uint64.
	return uint64(value), true
}

func resourceFreeProvisionCanFinalize(record providercontrol.OperationRecord) bool {
	if record.Command.Operation != providerexecutor.OperationProvision ||
		len(record.Head.Resources) != 0 {
		return false
	}
	switch record.Head.Status {
	case providerexecutor.StatusPending:
		return record.Head.Phase == providerexecutor.PhaseRequested ||
			record.Head.Phase == providerexecutor.PhaseAccepted
	case providerexecutor.StatusFailed:
		return true
	default:
		return false
	}
}

func convergeResourceFreeProvision(
	ctx context.Context,
	source providercontrol.OperationRecord,
	finalizer providercontrol.ResourceFreeTeardownFinalizer,
	resolution providercontrol.ProvisionResolutionApplication,
	subject string,
) (providercontrol.OperationRecord, bool, error) {
	terminal, handled, err := finalizer.FinalizeResourceFreeTeardown(ctx, source.Command, source.Head)
	if err != nil || handled {
		return terminal, handled, err
	}
	if _, err := resolution.ResolveProvisionOperation(ctx, providercontrol.ProvisionResolutionWorkflowRequest{
		TenantID: source.Command.TenantID, OperationID: source.Command.OperationID,
		OperatorSubjectID: subject,
		Confirmation:      "adopt-provision:" + source.Command.OperationID,
	}); err != nil {
		return providercontrol.OperationRecord{}, false, err
	}
	return finalizer.FinalizeResourceFreeTeardown(ctx, source.Command, source.Head)
}

func nativeDecommissionFailure(
	candidate nativeDecommissionCandidate,
	record providercontrol.OperationRecord,
) error {
	cause := fmt.Errorf(
		"%w: operation %s for server %s",
		ErrNativeDecommissionFailed,
		record.Command.OperationID,
		candidate.ServerID,
	)
	completedAttempts := candidate.RecentFailedAttempts + 1
	if completedAttempts < maxNativeDecommissionAttempts {
		return &jobs.JobWaitError{
			Reason: nativeDecommissionWaitReason,
			Message: fmt.Sprintf(
				"Managed server %s cleanup attempt %d failed; a new provider-safe attempt will start.",
				candidate.ServerID,
				completedAttempts,
			),
			ResumeAfter: nativeDecommissionResumeDelay,
			Cause:       cause,
		}
	}
	return fmt.Errorf(
		"%w: lease %s exhausted %d decommission attempts; last failure: %v",
		ErrNativeDecommissionAttemptsExhausted,
		candidate.LeaseID,
		completedAttempts,
		cause,
	)
}

// nativeDecommissionBaseIdempotencyKey is the first attempt's key and the
// prefix every later attempt extends, so counting past attempts is a prefix
// scan rather than a second bookkeeping table.
func nativeDecommissionBaseIdempotencyKey(leaseID, resourceGeneration string) string {
	return "native-decommission:" + leaseID + ":" + resourceGeneration
}

// nativeDecommissionIdempotencyKey derives the key for the attempt this
// candidate is about to make.
//
// The key used to be the base alone. An at-most-once key returns the same
// durable record forever, so the first decommission that terminalized as failed
// made every later destroy replay that verdict: the provider resource stayed
// billing, the RuntimeServer never reached its tombstone, and the managed-server
// capacity slot was reserved permanently. Live consequence on 2026-07-27: the
// demo account held 12 of 12 slots against a single running server and could not
// provision at all, denied with managed_runtime_max_servers_reached.
//
// Attempt zero keeps the historical key so an in-flight decommission still
// replays instead of forking.
func nativeDecommissionIdempotencyKey(candidate nativeDecommissionCandidate) string {
	base := nativeDecommissionBaseIdempotencyKey(candidate.LeaseID, candidate.ResourceGeneration)
	if candidate.FailedAttempts <= 0 {
		return base
	}
	return base + ":retry-" + strconv.Itoa(candidate.FailedAttempts)
}

// loadNativeDecommissionFailedAttemptsTx counts the terminally failed
// decommission operations already recorded for this generation: all of them,
// which numbers the next attempt's key, and those that failed inside
// nativeDecommissionAttemptWindow, which are charged against the budget.
//
// Only failed operations count. A pending or succeeded operation must still
// replay onto its own key, so it must not shift the attempt number out from
// under an operation that is mid-flight.
func loadNativeDecommissionFailedAttemptsTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	leaseID string,
	resourceGeneration string,
) (int, int, error) {
	base := nativeDecommissionBaseIdempotencyKey(leaseID, resourceGeneration)
	var attempts, recentAttempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE updated_at > now() - make_interval(secs => $4))
		FROM provider_operations
		WHERE tenant_id = $1
		  AND lease_id = $2
		  AND operation = 'decommission'
		  AND status = 'failed'
		  AND (idempotency_key = $3 OR idempotency_key LIKE $3 || ':retry-%')
	`, tenantID, leaseID, base, nativeDecommissionAttemptWindow.Seconds()).Scan(&attempts, &recentAttempts); err != nil {
		return 0, 0, fmt.Errorf("providercontroljobs: count native decommission attempts: %w", err)
	}
	return attempts, recentAttempts, nil
}

// nativeDecommissionTerminal reports the one outcome that proves provider
// absence: a succeeded decommission whose head is absent.
func nativeDecommissionTerminal(record providercontrol.OperationRecord) bool {
	return record.Command.Operation == providerexecutor.OperationDecommission &&
		record.Head.Status == providerexecutor.StatusSucceeded &&
		record.Head.Phase == providerexecutor.PhaseAbsent
}

// nativeDecommissionTerminallyFailed reports a decommission that has stopped
// for good without proving absence.
//
// This distinction is the difference between a job that ends and a job that
// runs forever. Success is not the only terminal state, but it was the only one
// treated as terminal, so a failed decommission fell through to "not yet
// converged", the caller returned a JobWaitError, and the retry re-entered
// Start with the same at-most-once idempotency key, which returned the same
// failed record. Observed in production on 2026-07-26: destroy job
// job-1785067836867531686 polled waiting_provider_decommission every five
// seconds against op_fc6df3a12e69cefdb69ef175aeab36ca, already failed/failed,
// and blocked every other stack execution behind it with
// waiting_stack_execution.
func nativeDecommissionTerminallyFailed(record providercontrol.OperationRecord) bool {
	return record.Command.Operation == providerexecutor.OperationDecommission &&
		record.Head.Status == providerexecutor.StatusFailed
}

func (d *NativeDecommissioner) loadTerminalProof(
	ctx context.Context,
	candidate nativeDecommissionCandidate,
) (jobs.ManagedLeaseDecommissionProof, bool, error) {
	tenantID := strings.TrimSpace(candidate.TenantID)
	if tenantID == "" {
		return jobs.ManagedLeaseDecommissionProof{}, false, fmt.Errorf("%w: terminal candidate lost tenant identity", errNativeDecommissionCustody)
	}
	tx, err := d.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return jobs.ManagedLeaseDecommissionProof{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return jobs.ManagedLeaseDecommissionProof{}, false, err
	}
	var operationID, receiptDigest, lifecycle, releaseAuthority string
	var receiptSequence uint64
	var releasedAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT release.release_operation_id,
		       release.receipt_sequence,
		       release.receipt_digest,
		       release.released_at,
		       release.release_authority,
		       server.lifecycle_state
		FROM managed_runtime_capacity_release_facts AS release
		JOIN servers AS server
		  ON server.tenant_id = release.tenant_id
		 AND server.id = release.server_id
		WHERE release.tenant_id = $1
		  AND release.lease_id = $2
		  AND release.resource_generation_id = $3::uuid
		  AND server.id = $4
		  AND server.lifecycle_state = 'decommissioned'
		  AND server.decommissioned_at IS NOT NULL
	`, tenantID, candidate.LeaseID, candidate.ResourceGeneration, candidate.ServerID).Scan(
		&operationID, &receiptSequence, &receiptDigest, &releasedAt, &releaseAuthority, &lifecycle,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.ManagedLeaseDecommissionProof{}, false, nil
	}
	if err != nil {
		return jobs.ManagedLeaseDecommissionProof{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return jobs.ManagedLeaseDecommissionProof{}, false, err
	}
	receiptProofDigest := strings.TrimPrefix(strings.TrimSpace(receiptDigest), "sha256:")
	if len(receiptProofDigest) != 64 {
		return jobs.ManagedLeaseDecommissionProof{}, false, fmt.Errorf(
			"%w: terminal capacity release has an invalid receipt digest",
			errNativeDecommissionCustody,
		)
	}
	observed := jobs.ManagedLeaseDecommissionObservedDecommissioned
	if candidate.ResourceFree || releaseAuthority == "resource_free_teardown" {
		observed = jobs.ManagedLeaseDecommissionObservedNotFound
	}
	return jobs.ManagedLeaseDecommissionProof{
		StackID:                  candidate.StackID,
		TenantID:                 tenantID,
		LeaseID:                  candidate.LeaseID,
		ProviderID:               candidate.ProviderID,
		ResourceGenerationID:     candidate.ResourceGeneration,
		ResourceGenerationDigest: candidate.ResourceDigest,
		ObservedState:            observed,
		ReceiptRef: fmt.Sprintf(
			"provider-receipt://%s/%s/%d",
			tenantID,
			operationID,
			receiptSequence,
		),
		ReceiptDigest: receiptProofDigest,
		VerifiedAt:    releasedAt.UTC(),
	}, true, nil
}

var _ jobs.ManagedLeaseDecommissioner = (*NativeDecommissioner)(nil)
