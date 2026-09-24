package providercontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	providerReceiptFinalizerSource          = "provider-control-finalizer"
	providerResourceFreeTeardownSource      = "provider-control-resource-free-teardown"
	resourceFreeNoDispatchAuthority         = "no_dispatch_custody"
	resourceFreeNoCandidateAuthority        = "no_candidate_observed"
	resourceFreeCapacityReleaseAuthority    = "resource_free_teardown"
	providerAbsenceCapacityReleaseAuthority = "provider_absence"
	// Managed runtime adapters keep provider-native Kind values for custody,
	// but these graph-stable BindingIDs are the executable receipt roles used
	// by the provider-neutral enrollment projection. Each present receipt must
	// carry exactly one of each role and pass the shared NativeRef validators.
	runtimeServerBindingID   = "server"
	runtimePublicIPBindingID = "public-ip"
)

var sharedIPv4AddressSpace = netip.MustParsePrefix("100.64.0.0/10")

type providerReceiptRuntimeProjector interface {
	ValidateMutationStartTx(
		context.Context,
		*sql.Tx,
		providerexecutor.Command,
		int64,
	) error
	PrepareTx(
		context.Context,
		*sql.Tx,
		OperationRecord,
		providerexecutor.Receipt,
		providerexecutor.Receipt,
	) (preparedProviderReceiptRuntimeProjection, error)
	ApplyTx(context.Context, *sql.Tx, preparedProviderReceiptRuntimeProjection) error
}

type preparedProviderReceiptRuntimeProjection struct {
	serverEvent     *controlplane.ServerEvent
	capacityRelease *managedRuntimeCapacityReleaseFact
	runtimeTarget   *providerRuntimeTargetProjection
}

type providerRuntimeTargetProjection struct {
	TenantID             string
	LeaseID              string
	RuntimeServerID      string
	ResourceGenerationID string
	ProviderID           string
	EngineVMID           string
	PublicIP             string
	ObservedAt           time.Time
}

type managedRuntimeCapacityReleaseFact struct {
	TenantID             string
	LeaseID              string
	ResourceGenerationID string
	ServerID             string
	ServerGeneration     int64
	OperationID          string
	Authority            string
	ReceiptSequence      uint64
	ReceiptDigest        string
	ReleasedAt           time.Time
}

type postgresProviderReceiptRuntimeProjector struct {
	servers *controlplane.PostgresStore
}

func newPostgresProviderReceiptRuntimeProjector(db *sql.DB) providerReceiptRuntimeProjector {
	return &postgresProviderReceiptRuntimeProjector{servers: controlplane.NewPostgresStore(db)}
}

func (p *postgresProviderReceiptRuntimeProjector) ValidateMutationStartTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	serverGeneration int64,
) error {
	if p == nil || p.servers == nil || tx == nil ||
		(command.Operation != providerexecutor.OperationReconcile &&
			command.Operation != providerexecutor.OperationDecommission) ||
		serverGeneration < 1 {
		return fmt.Errorf("%w: mutation resource projection is not configured", ErrInvalidRequest)
	}
	if lockErr := lockProviderResourceGenerationTx(ctx, tx, command); lockErr != nil {
		return lockErr
	}
	if claimErr := requireNoUnsettledGenerationSideEffectsTx(ctx, tx, command); claimErr != nil {
		return claimErr
	}
	return requireExactMutationTargetsTx(ctx, tx, command)
}

type receiptRuntimeServerHead struct {
	Revision         int64
	Generation       int64
	LeaseID          string
	ProviderRef      string
	LifecycleState   string
	DesiredState     string
	DecommissionedAt *time.Time
}

type receiptRuntimeLeaseHead struct {
	DesiredState string
	CancelledAt  *time.Time
}

// resourceFreeTeardownServerGeneration preserves the immutable generation on
// the provision operation while allowing the later control-plane decommission
// intent to advance the RuntimeServer aggregate. The current generation may
// only move forward and only after teardown intent is durably visible; lease,
// provider, and resource-generation custody are validated by the caller.
func resourceFreeTeardownServerGeneration(
	operationGeneration int64,
	currentGeneration int64,
	teardownRequested bool,
) (int64, error) {
	if operationGeneration < 1 || currentGeneration < operationGeneration ||
		(currentGeneration != operationGeneration && !teardownRequested) {
		return 0, fmt.Errorf("%w: resource-free teardown lost runtime server generation custody", ErrLeaseFence)
	}
	return currentGeneration, nil
}

func (p *postgresProviderReceiptRuntimeProjector) PrepareTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
) (preparedProviderReceiptRuntimeProjection, error) {
	if p == nil || p.servers == nil || tx == nil {
		return preparedProviderReceiptRuntimeProjection{}, fmt.Errorf(
			"%w: provider receipt runtime projector is not configured",
			ErrInvalidRequest,
		)
	}
	if len(next.Resources) == 0 && !receiptProjectsRuntimeLifecycle(record.Command, next) {
		return preparedProviderReceiptRuntimeProjection{}, nil
	}
	if record.RuntimeServerGeneration < 1 {
		return preparedProviderReceiptRuntimeProjection{}, fmt.Errorf(
			"%w: provider operation has no immutable runtime server generation pin",
			ErrLeaseFence,
		)
	}
	if lockErr := lockProviderResourceGenerationTx(ctx, tx, record.Command); lockErr != nil {
		return preparedProviderReceiptRuntimeProjection{}, lockErr
	}
	if bindingErr := bindReceiptResourcesTx(
		ctx,
		tx,
		record,
		next,
	); bindingErr != nil {
		return preparedProviderReceiptRuntimeProjection{}, bindingErr
	}

	head, eligible, err := loadProviderReceiptProjectionHeadTx(ctx, tx, record)
	if err != nil || !eligible {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	return prepareProviderReceiptLifecycleProjectionTx(ctx, tx, record, previous, next, head)
}

func loadProviderReceiptProjectionHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
) (receiptRuntimeServerHead, bool, error) {
	leaseHead, err := loadReceiptRuntimeLeaseHeadTx(ctx, tx, record.Command)
	if err != nil {
		return receiptRuntimeServerHead{}, false, err
	}
	head, err := loadReceiptRuntimeServerHeadTx(ctx, tx, record.Command.RuntimeServerID)
	if err != nil {
		return receiptRuntimeServerHead{}, false, err
	}
	if head.Generation < record.RuntimeServerGeneration || head.LeaseID != record.Command.LeaseID {
		// RuntimeServer generation fences Guard bindings; provider resource
		// generation is the immutable UUID already checked on the lease above.
		// Enrollment may legitimately advance the Guard generation while an
		// asynchronous provider receipt is still pending. That receipt remains
		// current for the same lease/resource UUID, but may never target an older
		// server generation or another lease.
		return receiptRuntimeServerHead{}, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(head.ProviderRef), strings.TrimSpace(record.Command.ProviderID)) {
		return receiptRuntimeServerHead{}, false, fmt.Errorf(
			"%w: runtime server provider %q does not match operation provider %q",
			ErrLeaseFence,
			head.ProviderRef,
			record.Command.ProviderID,
		)
	}
	teardownRequested := leaseHead.CancelledAt != nil ||
		leaseHead.DesiredState == string(serverregistry.DesiredAbsent) ||
		head.DesiredState == string(serverregistry.DesiredAbsent) ||
		head.LifecycleState == string(serverregistry.LifecycleDecommissioning)
	if record.Command.Operation != providerexecutor.OperationDecommission && teardownRequested {
		// An exact late result still binds provider-resource custody, but
		// teardown intent always wins over provision/reconcile lifecycle
		// projection. This also covers lease-only cancellation before the
		// RuntimeServer desired/lifecycle projection catches up.
		return receiptRuntimeServerHead{}, false, nil
	}
	return head, true, nil
}

func prepareProviderReceiptLifecycleProjectionTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
) (preparedProviderReceiptRuntimeProjection, error) {
	switch {
	case record.Command.Operation == providerexecutor.OperationProvision &&
		next.Status == providerexecutor.StatusSucceeded &&
		next.Phase == providerexecutor.PhasePresent:
		return preparePresentProviderReceiptProjection(record, next, head)
	case record.Command.Operation == providerexecutor.OperationProvision &&
		next.Status == providerexecutor.StatusFailed &&
		next.Phase == providerexecutor.PhaseFailed:
		return prepareFailedProviderReceiptProjection(record, next, head), nil
	case record.Command.Operation == providerexecutor.OperationDecommission &&
		previous.Phase == providerexecutor.PhaseAbsencePending &&
		next.Status == providerexecutor.StatusSucceeded &&
		next.Phase == providerexecutor.PhaseAbsent:
		return prepareAbsentProviderReceiptProjectionTx(ctx, tx, record, next, head)
	default:
		return preparedProviderReceiptRuntimeProjection{}, nil
	}
}

func preparePresentProviderReceiptProjection(
	record OperationRecord,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
) (preparedProviderReceiptRuntimeProjection, error) {
	runtimeTarget, err := runtimeTargetFromProviderReceipt(record, next)
	if err != nil {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	switch serverregistry.LifecycleState(head.LifecycleState) {
	case serverregistry.LifecyclePlanned, serverregistry.LifecycleProvisioning:
		serverEvent := providerReceiptServerEvent(
			record,
			next,
			head,
			string(serverregistry.LifecycleEnrolling),
			"provider_present",
			nil,
		)
		serverEvent.Runtime.Metadata = map[string]any{
			"engine_vm_id":      runtimeTarget.EngineVMID,
			"runtime_public_ip": runtimeTarget.PublicIP,
			"runtime_ssh_host":  runtimeTarget.PublicIP,
			"runtime_ssh_user":  "kombify",
			"runtime_ssh_port":  22,
		}
		serverEvent.Runtime.RuntimeTarget = serverregistry.ManagedVPSRuntimeTarget(
			runtimeTarget.ProviderID, runtimeTarget.EngineVMID, runtimeTarget.LeaseID, runtimeTarget.ObservedAt,
		)
		if record.ExecutionProfile.CredentialMode == CredentialModeWorkerHeld {
			serverEvent.Runtime.RuntimeTarget = serverregistry.SubstrateVMRuntimeTarget(runtimeTarget.EngineVMID, runtimeTarget.LeaseID, runtimeTarget.ObservedAt)
		}
		return preparedProviderReceiptRuntimeProjection{
			serverEvent:   serverEvent,
			runtimeTarget: runtimeTarget,
		}, nil
	case serverregistry.LifecycleEnrolling, serverregistry.LifecycleActive:
		return preparedProviderReceiptRuntimeProjection{
			serverEvent:   providerReceiptRuntimeTargetServerEvent(record, next, head, runtimeTarget),
			runtimeTarget: runtimeTarget,
		}, nil
	case serverregistry.LifecycleFailed,
		serverregistry.LifecycleDecommissioning,
		serverregistry.LifecycleDecommissioned:
		return preparedProviderReceiptRuntimeProjection{}, nil
	default:
		return preparedProviderReceiptRuntimeProjection{}, fmt.Errorf(
			"%w: unsupported runtime server lifecycle %q",
			ErrLeaseFence,
			head.LifecycleState,
		)
	}
}

func prepareFailedProviderReceiptProjection(
	record OperationRecord,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
) preparedProviderReceiptRuntimeProjection {
	switch serverregistry.LifecycleState(head.LifecycleState) {
	case serverregistry.LifecyclePlanned,
		serverregistry.LifecycleProvisioning,
		serverregistry.LifecycleEnrolling:
		return preparedProviderReceiptRuntimeProjection{
			serverEvent: providerReceiptServerEvent(
				record,
				next,
				head,
				string(serverregistry.LifecycleFailed),
				"provider_failed",
				nil,
			),
		}
	default:
		return preparedProviderReceiptRuntimeProjection{}
	}
}

func prepareAbsentProviderReceiptProjectionTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
) (preparedProviderReceiptRuntimeProjection, error) {
	projectServerEvent, err := decommissionFinalizationServerEventRequired(head)
	if err != nil {
		return preparedProviderReceiptRuntimeProjection{}, fmt.Errorf(
			"%w: definitive provider absence requires an exact terminal RuntimeServer head: %v",
			ErrLeaseFence, err,
		)
	}
	if err := requireNoUnsettledGenerationSideEffectsTx(ctx, tx, record.Command); err != nil {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	if err := requireDefinitiveGenerationAbsenceTx(ctx, tx, record, next); err != nil {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return preparedProviderReceiptRuntimeProjection{}, fmt.Errorf(
			"providercontrol: read decommission finalization database time: %w",
			err,
		)
	}
	databaseNow = databaseNow.UTC()
	if err := lockManagedRuntimeCapacityReservationTx(
		ctx,
		tx,
		record.Command.TenantID,
		record.Command.LeaseID,
		record.Command.ResourceGenerationID,
	); err != nil {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	prepared := preparedProviderReceiptRuntimeProjection{
		capacityRelease: &managedRuntimeCapacityReleaseFact{
			TenantID: record.Command.TenantID, LeaseID: record.Command.LeaseID,
			ResourceGenerationID: record.Command.ResourceGenerationID,
			ServerID:             record.Command.RuntimeServerID, ServerGeneration: record.RuntimeServerGeneration,
			OperationID: record.Command.OperationID, Authority: providerAbsenceCapacityReleaseAuthority, ReceiptSequence: next.Sequence,
			ReceiptDigest: next.ReceiptDigest, ReleasedAt: databaseNow,
		},
	}
	if projectServerEvent {
		prepared.serverEvent = providerReceiptServerEvent(
			record,
			next,
			head,
			string(serverregistry.LifecycleDecommissioned),
			"provider_absent",
			&databaseNow,
		)
	}
	return prepared, nil
}

func decommissionFinalizationServerEventRequired(head receiptRuntimeServerHead) (bool, error) {
	if head.DesiredState != string(serverregistry.DesiredAbsent) {
		return false, fmt.Errorf("runtime server desired state is %q", head.DesiredState)
	}
	switch serverregistry.LifecycleState(head.LifecycleState) {
	case serverregistry.LifecycleDecommissioning:
		if head.DecommissionedAt != nil {
			return false, fmt.Errorf("decommissioning runtime server already has a tombstone time")
		}
		return true, nil
	case serverregistry.LifecycleDecommissioned:
		if head.DecommissionedAt == nil {
			return false, fmt.Errorf("decommissioned runtime server has no tombstone time")
		}
		return false, nil
	default:
		return false, fmt.Errorf("runtime server lifecycle is %q", head.LifecycleState)
	}
}

// FinalizeResourceFreeTeardown closes a canceled provision without contacting
// the provider. The transaction succeeds only when the database proves either
// that dispatch custody never existed or that Operator Reconcile verified an
// exact zero-candidate observation after dispatch authority ended.
func (l *PostgresLedger) FinalizeResourceFreeTeardown(
	ctx context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) (OperationRecord, bool, error) {
	if l == nil || l.db == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: ledger is not configured", ErrInvalidRequest)
	}
	if validationErr := command.Validate(); validationErr != nil {
		return OperationRecord{}, false, validationErr
	}
	if validationErr := head.ValidateFor(ctx, command, l.verifier); validationErr != nil {
		return OperationRecord{}, false, validationErr
	}

	handled := false
	err := l.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		prepared, eligible, prepareErr := l.prepareResourceFreeTeardownTx(ctx, tx, command, head)
		if prepareErr != nil || !eligible {
			return prepareErr
		}
		serverHead := prepared.serverHead
		terminalGeneration := prepared.terminalGeneration
		projectionRecord := prepared.projectionRecord
		projector := prepared.projector

		replayed, replayErr := loadResourceFreeTeardownReplayTx(ctx, tx, command)
		if replayErr != nil {
			return replayErr
		}
		if replayed {
			handled = true
			return nil
		}
		if quiescenceErr := requireResourceFreeGenerationQuiescenceTx(
			ctx,
			tx,
			command,
		); quiescenceErr != nil {
			return quiescenceErr
		}

		if lifecycleErr := admitResourceFreeTeardownServerHead(serverHead); lifecycleErr != nil {
			return lifecycleErr
		}

		authority, authorized, authorityErr := proveResourceFreeTeardownAuthorityTx(ctx, tx, command, head)
		if authorityErr != nil || !authorized {
			return authorityErr
		}

		if capacityErr := lockManagedRuntimeCapacityReservationTx(
			ctx,
			tx,
			command.TenantID,
			command.LeaseID,
			command.ResourceGenerationID,
		); capacityErr != nil {
			return capacityErr
		}

		terminalizedAt, terminalizeErr := persistResourceFreeTerminalizationTx(
			ctx, tx, command, head, terminalGeneration, authority,
		)
		if terminalizeErr != nil {
			return terminalizeErr
		}
		if projectErr := projectResourceFreeTeardownTx(
			ctx, tx, command, head, projectionRecord, serverHead, projector, authority.name, terminalizedAt,
		); projectErr != nil {
			return projectErr
		}
		handled = true
		return nil
	})
	if err != nil {
		return OperationRecord{}, false, err
	}
	if !handled {
		return OperationRecord{}, false, nil
	}
	record, loadErr := l.LoadOperation(ctx, command.TenantID, command.OperationID)
	if loadErr != nil {
		return OperationRecord{}, false, loadErr
	}
	return record, true, nil
}

type resourceFreeTeardownContext struct {
	serverHead         receiptRuntimeServerHead
	terminalGeneration int64
	projectionRecord   OperationRecord
	projector          *postgresProviderReceiptRuntimeProjector
}

type resourceFreeTeardownAuthority struct {
	name               string
	resolutionRevision sql.NullInt64
	decisionDigest     sql.NullString
}

func (l *PostgresLedger) prepareResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) (resourceFreeTeardownContext, bool, error) {
	if _, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))
	`, command.TenantID, command.OperationID); err != nil {
		return resourceFreeTeardownContext{}, false,
			fmt.Errorf("providercontrol: lock resource-free teardown operation: %w", err)
	}
	if err := lockProviderResourceGenerationTx(ctx, tx, command); err != nil {
		return resourceFreeTeardownContext{}, false, err
	}
	current, err := loadOperationProjectionTx(
		ctx, tx, command.TenantID, command.OperationID, true, false,
	)
	if err != nil {
		return resourceFreeTeardownContext{}, false, err
	}
	eligible, err := admitResourceFreeTeardownOperation(current, command, head)
	if err != nil || !eligible {
		return resourceFreeTeardownContext{}, eligible, err
	}
	leaseHead, err := loadReceiptRuntimeLeaseHeadTx(ctx, tx, command)
	if err != nil {
		return resourceFreeTeardownContext{}, false, err
	}
	serverHead, err := loadReceiptRuntimeServerHeadTx(ctx, tx, command.RuntimeServerID)
	if err != nil {
		return resourceFreeTeardownContext{}, false, err
	}
	if serverHead.LeaseID != command.LeaseID ||
		!strings.EqualFold(strings.TrimSpace(serverHead.ProviderRef), strings.TrimSpace(command.ProviderID)) {
		return resourceFreeTeardownContext{}, false,
			fmt.Errorf("%w: resource-free teardown lost runtime server custody", ErrLeaseFence)
	}
	teardownRequested := leaseHead.CancelledAt != nil ||
		leaseHead.DesiredState == string(serverregistry.DesiredAbsent) ||
		serverHead.DesiredState == string(serverregistry.DesiredAbsent) ||
		serverHead.LifecycleState == string(serverregistry.LifecycleDecommissioning)
	if !teardownRequested {
		return resourceFreeTeardownContext{}, false, nil
	}
	terminalGeneration, err := resourceFreeTeardownServerGeneration(
		current.RuntimeServerGeneration, serverHead.Generation, teardownRequested,
	)
	if err != nil {
		return resourceFreeTeardownContext{}, false, err
	}
	projector, ok := l.runtimeProjection.(*postgresProviderReceiptRuntimeProjector)
	if !ok || projector == nil || projector.servers == nil {
		return resourceFreeTeardownContext{}, false, fmt.Errorf(
			"%w: resource-free RuntimeServer projection is not configured",
			ErrInvalidRequest,
		)
	}
	projectionRecord := current
	projectionRecord.RuntimeServerGeneration = terminalGeneration
	return resourceFreeTeardownContext{
		serverHead:         serverHead,
		terminalGeneration: terminalGeneration, projectionRecord: projectionRecord,
		projector: projector,
	}, true, nil
}

func admitResourceFreeTeardownOperation(
	current OperationRecord,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) (bool, error) {
	if err := providerexecutor.ValidateReplay(current.Command, command); err != nil {
		return false, err
	}
	if current.Head.Sequence != head.Sequence || current.Head.ReceiptDigest != head.ReceiptDigest {
		// Another worker may advance the exact same provision between the
		// decommissioner's read and this transaction (for example while an
		// async provider failure is sealed). That is ordinary durable
		// convergence: leave the newer head for the next cycle instead of
		// terminally failing cleanup with a stale-read conflict.
		if head.Sequence < current.Head.Sequence {
			return false, nil
		}
		return false, fmt.Errorf("%w: resource-free teardown head changed", ErrLedgerConflict)
	}
	if current.Command.Operation != providerexecutor.OperationProvision ||
		current.Head.Status != providerexecutor.StatusPending ||
		(current.Head.Phase != providerexecutor.PhaseRequested &&
			current.Head.Phase != providerexecutor.PhaseAccepted) ||
		len(current.Head.Resources) != 0 {
		return false, nil
	}
	return true, nil
}

func loadResourceFreeTeardownReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) (bool, error) {
	var existingAuthority, existingDigest string
	var existingRevision sql.NullInt64
	var existingComplete bool
	err := tx.QueryRowContext(ctx, `
		SELECT terminalization.authority,
		       terminalization.resolution_revision,
		       COALESCE(terminalization.decision_digest, ''),
		       EXISTS (
		           SELECT 1
		           FROM servers AS server
		           JOIN managed_runtime_capacity_release_facts AS release
		             ON release.tenant_id = terminalization.tenant_id
		            AND release.lease_id = terminalization.lease_id
		            AND release.resource_generation_id =
		                terminalization.resource_generation_id
		            AND release.server_id = terminalization.server_id
		            AND release.server_generation =
		                terminalization.server_generation
		            AND release.release_operation_id =
		                terminalization.operation_id
		            AND release.release_authority =
		                'resource_free_teardown'
		            AND release.receipt_sequence =
		                terminalization.head_sequence
		            AND release.receipt_digest =
		                terminalization.head_receipt_digest
		           WHERE server.tenant_id = terminalization.tenant_id
		             AND server.id = terminalization.server_id
		             AND server.lease_id = terminalization.lease_id
		             AND server.generation =
		                 terminalization.server_generation
		             AND server.desired_state = 'absent'
		             AND server.lifecycle_state = 'decommissioned'
		             AND server.decommissioned_at IS NOT NULL
		       )
		FROM provider_operation_resource_free_terminalizations AS terminalization
		WHERE terminalization.tenant_id = $1
		  AND terminalization.operation_id = $2
	`, command.TenantID, command.OperationID).Scan(
		&existingAuthority,
		&existingRevision,
		&existingDigest,
		&existingComplete,
	)
	if err == nil {
		if !existingComplete {
			return false, fmt.Errorf(
				"%w: resource-free teardown fact lacks its exact RuntimeServer tombstone and capacity release",
				ErrLedgerConflict,
			)
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("providercontrol: load resource-free teardown fact: %w", err)
	}
	return false, nil
}

func admitResourceFreeTeardownServerHead(head receiptRuntimeServerHead) error {
	switch serverregistry.LifecycleState(head.LifecycleState) {
	case serverregistry.LifecyclePlanned,
		serverregistry.LifecycleProvisioning,
		serverregistry.LifecycleFailed,
		serverregistry.LifecycleDecommissioning:
		return nil
	default:
		return fmt.Errorf(
			"%w: resource-free teardown cannot tombstone server lifecycle %q",
			ErrLeaseFence,
			head.LifecycleState,
		)
	}
}

func proveResourceFreeTeardownAuthorityTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) (resourceFreeTeardownAuthority, bool, error) {
	authority := resourceFreeTeardownAuthority{name: resourceFreeNoDispatchAuthority}
	decisionErr := tx.QueryRowContext(ctx, `
			SELECT decision.resolution_revision, decision.decision_digest
			FROM provider_provision_resolution_decisions AS decision
			WHERE decision.tenant_id = $1
			  AND decision.operation_id = $2
			  AND decision.expected_head_sequence = $3
			  AND decision.expected_head_receipt_digest = $4
			  AND decision.outcome = 'no_candidate_observed'
			ORDER BY decision.resolution_revision DESC
			LIMIT 1
		`, command.TenantID, command.OperationID,
		int64(head.Sequence), head.ReceiptDigest).Scan( // #nosec G115 -- FinalizeResourceFreeTeardown validates the sealed receipt.
		&authority.resolutionRevision,
		&authority.decisionDigest,
	)
	if decisionErr == nil {
		authority.name = resourceFreeNoCandidateAuthority
		if err := requireNoCandidateClaimQuiescenceTx(ctx, tx, command, head); err != nil {
			return resourceFreeTeardownAuthority{}, false, err
		}
	} else if !errors.Is(decisionErr, sql.ErrNoRows) {
		return resourceFreeTeardownAuthority{}, false,
			fmt.Errorf("providercontrol: load no-candidate teardown authority: %w", decisionErr)
	} else {
		noDispatchCustody, err := proveNoDispatchTeardownAuthorityTx(ctx, tx, command)
		if err != nil || !noDispatchCustody {
			return resourceFreeTeardownAuthority{}, noDispatchCustody, err
		}
	}
	return authority, true, nil
}

func requireNoCandidateClaimQuiescenceTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) error {
	var liveClaim bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM provider_operation_execution_claims AS claim
			WHERE claim.tenant_id = $1
			  AND claim.operation_id = $2
			  AND claim.head_sequence = $3
			  AND claim.head_receipt_digest = $4
			  AND claim.state = 'active'
			  AND claim.lease_expires_at > clock_timestamp()
		)
	`, command.TenantID, command.OperationID,
		int64(head.Sequence), head.ReceiptDigest).Scan(&liveClaim); err != nil { // #nosec G115 -- FinalizeResourceFreeTeardown validates the sealed receipt.
		return fmt.Errorf("providercontrol: inspect no-candidate claim quiescence: %w", err)
	}
	if liveClaim {
		return fmt.Errorf("%w: no-candidate decision still has a live claim", ErrLeaseFence)
	}
	return nil
}

func proveNoDispatchTeardownAuthorityTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) (bool, error) {
	var noDispatchCustody bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
			NOT EXISTS (
				SELECT 1 FROM provider_operation_execution_claims AS claim
				WHERE claim.tenant_id = $1 AND claim.operation_id = $2
				  AND claim.claim_access = 'side_effecting'
			)
			AND NOT EXISTS (
				SELECT 1 FROM provider_provision_dispatch_guards AS dispatch_guard
				WHERE dispatch_guard.tenant_id = $1 AND dispatch_guard.operation_id = $2
			)
			AND NOT EXISTS (
				SELECT 1 FROM provider_operation_resources AS resource
				WHERE resource.tenant_id = $1 AND resource.operation_id = $2
			)
			AND NOT EXISTS (
				SELECT 1 FROM server_provider_resource_bindings AS binding
				WHERE binding.tenant_id = $1
				  AND binding.lease_id = $3
				  AND binding.resource_generation_id = $4::uuid
			)
			AND NOT EXISTS (
				SELECT 1 FROM provider_provision_resolution_decisions AS decision
				WHERE decision.tenant_id = $1 AND decision.operation_id = $2
			)
	`, command.TenantID, command.OperationID,
		command.LeaseID, command.ResourceGenerationID).Scan(&noDispatchCustody); err != nil {
		return false, fmt.Errorf("providercontrol: prove no-dispatch teardown authority: %w", err)
	}
	return noDispatchCustody, nil
}

func persistResourceFreeTerminalizationTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	terminalGeneration int64,
	authority resourceFreeTeardownAuthority,
) (time.Time, error) {
	var terminalizedAt time.Time
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO provider_operation_resource_free_terminalizations (
			tenant_id, operation_id, lease_id, lease_revision,
			server_id, server_generation, resource_generation_id,
			head_sequence, head_receipt_digest, authority,
			resolution_revision, decision_digest, terminalized_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7::uuid,$8,$9,$10,$11,$12,clock_timestamp()
		)
		RETURNING terminalized_at
	`, command.TenantID, command.OperationID, command.LeaseID,
		int64(command.LeaseRevision), command.RuntimeServerID, // #nosec G115 -- FinalizeResourceFreeTeardown validates the sealed command.
		terminalGeneration, command.ResourceGenerationID,
		int64(head.Sequence), head.ReceiptDigest, authority.name, // #nosec G115 -- FinalizeResourceFreeTeardown validates the sealed receipt.
		authority.resolutionRevision, authority.decisionDigest).Scan(&terminalizedAt); err != nil {
		return time.Time{}, fmt.Errorf("providercontrol: persist resource-free teardown fact: %w", err)
	}
	return terminalizedAt.UTC(), nil
}

func projectResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	projectionRecord OperationRecord,
	serverHead receiptRuntimeServerHead,
	projector *postgresProviderReceiptRuntimeProjector,
	authority string,
	terminalizedAt time.Time,
) error {
	reason := "provider_dispatch_not_started"
	if authority == resourceFreeNoCandidateAuthority {
		reason = "provider_no_candidate"
	}
	if serverHead.LifecycleState != string(serverregistry.LifecycleDecommissioning) {
		decommissioning, err := projector.servers.ApplyServerEventTx(
			ctx,
			tx,
			resourceFreeTeardownServerEvent(
				projectionRecord,
				serverHead,
				string(serverregistry.LifecycleDecommissioning),
				reason,
				nil,
				terminalizedAt,
			),
		)
		if err != nil {
			return fmt.Errorf("providercontrol: project resource-free decommissioning: %w", err)
		}
		if decommissioning == nil || !decommissioning.Applied || decommissioning.Server == nil {
			return fmt.Errorf("%w: resource-free decommissioning projection was not applied", ErrLedgerConflict)
		}
		serverHead.Revision = decommissioning.Server.Revision
		serverHead.Generation = decommissioning.Server.Generation
		serverHead.LifecycleState = decommissioning.Server.LifecycleState
		serverHead.DesiredState = decommissioning.Server.DesiredState
	}
	decommissionedAt := terminalizedAt
	decommissioned, err := projector.servers.ApplyServerEventTx(
		ctx,
		tx,
		resourceFreeTeardownServerEvent(
			projectionRecord,
			serverHead,
			string(serverregistry.LifecycleDecommissioned),
			reason,
			&decommissionedAt,
			terminalizedAt,
		),
	)
	if err != nil {
		return fmt.Errorf("providercontrol: project resource-free tombstone: %w", err)
	}
	if decommissioned == nil || !decommissioned.Applied || decommissioned.Server == nil ||
		decommissioned.Server.LifecycleState != string(serverregistry.LifecycleDecommissioned) {
		return fmt.Errorf("%w: resource-free tombstone projection was not applied", ErrLedgerConflict)
	}
	return insertManagedRuntimeCapacityReleaseTx(
		ctx,
		tx,
		managedRuntimeCapacityReleaseFact{
			TenantID: command.TenantID, LeaseID: command.LeaseID,
			ResourceGenerationID: command.ResourceGenerationID,
			ServerID:             command.RuntimeServerID,
			ServerGeneration:     projectionRecord.RuntimeServerGeneration,
			OperationID:          command.OperationID,
			Authority:            resourceFreeCapacityReleaseAuthority,
			ReceiptSequence:      head.Sequence,
			ReceiptDigest:        head.ReceiptDigest,
			ReleasedAt:           terminalizedAt,
		},
	)
}

// finalizeFailedAMOResourceFreeTeardownTx settles only an exact terminal AMO
// failure whose provider absence was certified by the decision inserted in the
// same transaction. A failed receipt alone never proves absence.
func (l *PostgresLedger) finalizeFailedAMOResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	decision ProvisionResolutionDecision,
) (bool, error) {
	if l == nil || tx == nil || !isTerminalHandleFreeAMOProvisionHead(current) ||
		decision.TenantID != current.Command.TenantID ||
		decision.OperationID != current.Command.OperationID ||
		decision.ExpectedHeadSequence != current.Head.Sequence ||
		decision.ExpectedHeadReceiptDigest != current.Head.ReceiptDigest ||
		decision.Outcome != ProvisionResolutionNoCandidateObserved {
		return false, ErrProvisionResolutionConflict
	}
	prepared, err := l.prepareFailedAMOResourceFreeTeardownTx(ctx, tx, current)
	if err != nil {
		return false, err
	}
	proof, err := proveFailedAMOResourceFreeTeardownTx(ctx, tx, current, decision)
	if err != nil {
		return false, err
	}
	terminalizedAt, err := persistFailedAMOResourceFreeTerminalizationTx(
		ctx, tx, current, prepared.terminalGeneration, proof,
	)
	if err != nil {
		return false, err
	}
	if err := projectFailedAMOResourceFreeTeardownTx(ctx, tx, current, prepared, terminalizedAt); err != nil {
		return false, err
	}
	return true, nil
}

type failedAMOResourceFreeTeardownContext struct {
	terminalGeneration    int64
	projectionRecord      OperationRecord
	serverHead            receiptRuntimeServerHead
	projector             *postgresProviderReceiptRuntimeProjector
	alreadyDecommissioned bool
}

type failedAMOResourceFreeTeardownProof struct {
	resolutionRevision int64
	decisionDigest     string
}

func (l *PostgresLedger) prepareFailedAMOResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
) (failedAMOResourceFreeTeardownContext, error) {
	if err := lockProviderResourceGenerationTx(ctx, tx, current.Command); err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	leaseHead, err := loadReceiptRuntimeLeaseHeadTx(ctx, tx, current.Command)
	if err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	serverHead, err := loadReceiptRuntimeServerHeadTx(ctx, tx, current.Command.RuntimeServerID)
	if err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	if serverHead.LeaseID != current.Command.LeaseID ||
		!strings.EqualFold(
			strings.TrimSpace(serverHead.ProviderRef),
			strings.TrimSpace(current.Command.ProviderID),
		) {
		return failedAMOResourceFreeTeardownContext{}, fmt.Errorf(
			"%w: failed AMO settlement lost runtime server custody",
			ErrLeaseFence,
		)
	}
	teardownRequested := leaseHead.CancelledAt != nil ||
		leaseHead.DesiredState == string(serverregistry.DesiredAbsent) ||
		serverHead.DesiredState == string(serverregistry.DesiredAbsent) ||
		serverHead.LifecycleState == string(serverregistry.LifecycleDecommissioning)
	if !teardownRequested {
		return failedAMOResourceFreeTeardownContext{}, fmt.Errorf(
			"%w: failed AMO settlement requires exact teardown intent",
			ErrLeaseFence,
		)
	}
	terminalGeneration, err := resourceFreeTeardownServerGeneration(
		current.RuntimeServerGeneration,
		serverHead.Generation,
		teardownRequested,
	)
	if err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	projector, ok := l.runtimeProjection.(*postgresProviderReceiptRuntimeProjector)
	if !ok || projector == nil || projector.servers == nil {
		return failedAMOResourceFreeTeardownContext{}, fmt.Errorf(
			"%w: resource-free RuntimeServer projection is not configured",
			ErrInvalidRequest,
		)
	}
	if err := requireResourceFreeGenerationQuiescenceTx(ctx, tx, current.Command); err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	alreadyDecommissioned, err := admitFailedAMOResourceFreeServerHead(serverHead)
	if err != nil {
		return failedAMOResourceFreeTeardownContext{}, err
	}
	projectionRecord := current
	projectionRecord.RuntimeServerGeneration = terminalGeneration
	return failedAMOResourceFreeTeardownContext{
		terminalGeneration:    terminalGeneration,
		projectionRecord:      projectionRecord,
		serverHead:            serverHead,
		projector:             projector,
		alreadyDecommissioned: alreadyDecommissioned,
	}, nil
}

func admitFailedAMOResourceFreeServerHead(head receiptRuntimeServerHead) (bool, error) {
	switch serverregistry.LifecycleState(head.LifecycleState) {
	case serverregistry.LifecyclePlanned,
		serverregistry.LifecycleProvisioning,
		serverregistry.LifecycleFailed,
		serverregistry.LifecycleDecommissioning:
		return false, nil
	case serverregistry.LifecycleDecommissioned:
		if head.DesiredState != string(serverregistry.DesiredAbsent) || head.DecommissionedAt == nil {
			return false, fmt.Errorf(
				"%w: failed AMO settlement requires an exact server tombstone",
				ErrLeaseFence,
			)
		}
		return true, nil
	default:
		return false, fmt.Errorf(
			"%w: failed AMO settlement cannot tombstone server lifecycle %q",
			ErrLeaseFence,
			head.LifecycleState,
		)
	}
}

func proveFailedAMOResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	decision ProvisionResolutionDecision,
) (failedAMOResourceFreeTeardownProof, error) {
	var proof failedAMOResourceFreeTeardownProof
	var guardSequence int64
	var guardDigest string
	if err := tx.QueryRowContext(ctx, `
		SELECT decision.resolution_revision, decision.decision_digest,
		       observation.head_sequence, observation.head_receipt_digest
		FROM provider_provision_resolution_decisions AS decision
		JOIN provider_provision_discovery_observations AS observation
		  ON observation.tenant_id = decision.tenant_id
		 AND observation.operation_id = decision.operation_id
		 AND observation.observation_id = decision.observation_id
		 AND observation.snapshot_digest = decision.observation_snapshot_digest
		JOIN provider_provision_dispatch_guards AS dispatch_guard
		  ON dispatch_guard.tenant_id = observation.tenant_id
		 AND dispatch_guard.operation_id = observation.operation_id
		 AND dispatch_guard.head_sequence = observation.head_sequence
		 AND dispatch_guard.head_receipt_digest = observation.head_receipt_digest
		WHERE decision.tenant_id = $1
		  AND decision.operation_id = $2
		  AND decision.resolution_revision = $3
		  AND decision.decision_digest = $4
		  AND decision.expected_head_sequence = $5
		  AND decision.expected_head_receipt_digest = $6
		  AND decision.outcome = 'no_candidate_observed'
		  AND observation.candidate_count = 0
		  AND dispatch_guard.dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
		  AND dispatch_guard.guard_origin = 'first_claim'
	`, current.Command.TenantID, current.Command.OperationID,
		int64(decision.ResolutionRevision), decision.DecisionDigest, // #nosec G115 -- the decision is derived from a bounded resolution request.
		int64(current.Head.Sequence), current.Head.ReceiptDigest).Scan( // #nosec G115 -- loadProvisionResolutionContextTx validates the stored receipt.
		&proof.resolutionRevision,
		&proof.decisionDigest,
		&guardSequence,
		&guardDigest,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return failedAMOResourceFreeTeardownProof{}, ErrProvisionResolutionConflict
		}
		return failedAMOResourceFreeTeardownProof{}, fmt.Errorf(
			"providercontrol: prove failed AMO zero-candidate settlement: %w",
			err,
		)
	}
	if !isImmediateJSONSafeSuccessor(guardSequence, current.Head.Sequence) ||
		current.Head.PreviousReceiptDigest != guardDigest {
		return failedAMOResourceFreeTeardownProof{}, ErrProvisionResolutionConflict
	}
	var liveClaim bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM provider_operation_execution_claims AS claim
			WHERE claim.tenant_id = $1
			  AND claim.operation_id = $2
			  AND claim.head_sequence = $3
			  AND claim.head_receipt_digest = $4
			  AND claim.state = 'active'
			  AND claim.lease_expires_at > clock_timestamp()
		)
	`, current.Command.TenantID, current.Command.OperationID,
		guardSequence, guardDigest).Scan(&liveClaim); err != nil {
		return failedAMOResourceFreeTeardownProof{}, fmt.Errorf(
			"providercontrol: inspect failed AMO claim quiescence: %w",
			err,
		)
	} else if liveClaim {
		return failedAMOResourceFreeTeardownProof{}, fmt.Errorf(
			"%w: failed AMO decision still has a live claim",
			ErrLeaseFence,
		)
	}
	return proof, nil
}

func persistFailedAMOResourceFreeTerminalizationTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	terminalGeneration int64,
	proof failedAMOResourceFreeTeardownProof,
) (time.Time, error) {
	if err := lockManagedRuntimeCapacityReservationTx(
		ctx,
		tx,
		current.Command.TenantID,
		current.Command.LeaseID,
		current.Command.ResourceGenerationID,
	); err != nil {
		return time.Time{}, err
	}

	var terminalizedAt time.Time
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO provider_operation_resource_free_terminalizations (
			tenant_id, operation_id, lease_id, lease_revision,
			server_id, server_generation, resource_generation_id,
			head_sequence, head_receipt_digest, authority,
			resolution_revision, decision_digest, terminalized_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7::uuid,$8,$9,
			'no_candidate_observed',$10,$11,clock_timestamp()
		)
		RETURNING terminalized_at
	`, current.Command.TenantID, current.Command.OperationID,
		current.Command.LeaseID, int64(current.Command.LeaseRevision), // #nosec G115 -- loadProvisionResolutionContextTx validates the sealed command.
		current.Command.RuntimeServerID, terminalGeneration,
		current.Command.ResourceGenerationID, int64(current.Head.Sequence), // #nosec G115 -- loadProvisionResolutionContextTx validates the stored receipt.
		current.Head.ReceiptDigest, proof.resolutionRevision, proof.decisionDigest).Scan(
		&terminalizedAt,
	); err != nil {
		return time.Time{}, fmt.Errorf(
			"providercontrol: persist failed AMO terminalization: %w",
			err,
		)
	}
	return terminalizedAt.UTC(), nil
}

func projectFailedAMOResourceFreeTeardownTx(
	ctx context.Context,
	tx *sql.Tx,
	current OperationRecord,
	prepared failedAMOResourceFreeTeardownContext,
	terminalizedAt time.Time,
) error {
	serverHead := prepared.serverHead
	if !prepared.alreadyDecommissioned &&
		serverHead.LifecycleState != string(serverregistry.LifecycleDecommissioning) {
		decommissioning, err := prepared.projector.servers.ApplyServerEventTx(
			ctx,
			tx,
			resourceFreeTeardownServerEvent(
				prepared.projectionRecord,
				serverHead,
				string(serverregistry.LifecycleDecommissioning),
				"provider_no_candidate",
				nil,
				terminalizedAt,
			),
		)
		if err != nil {
			return fmt.Errorf(
				"providercontrol: project failed AMO decommissioning: %w",
				err,
			)
		}
		if decommissioning == nil || !decommissioning.Applied ||
			decommissioning.Server == nil {
			return fmt.Errorf(
				"%w: failed AMO decommissioning projection was not applied",
				ErrLedgerConflict,
			)
		}
		serverHead.Revision = decommissioning.Server.Revision
		serverHead.Generation = decommissioning.Server.Generation
		serverHead.LifecycleState = decommissioning.Server.LifecycleState
		serverHead.DesiredState = decommissioning.Server.DesiredState
	}
	if !prepared.alreadyDecommissioned {
		decommissionedAt := terminalizedAt
		decommissioned, err := prepared.projector.servers.ApplyServerEventTx(
			ctx,
			tx,
			resourceFreeTeardownServerEvent(
				prepared.projectionRecord,
				serverHead,
				string(serverregistry.LifecycleDecommissioned),
				"provider_no_candidate",
				&decommissionedAt,
				terminalizedAt,
			),
		)
		if err != nil {
			return fmt.Errorf(
				"providercontrol: project failed AMO tombstone: %w",
				err,
			)
		}
		if decommissioned == nil || !decommissioned.Applied ||
			decommissioned.Server == nil ||
			decommissioned.Server.LifecycleState != string(serverregistry.LifecycleDecommissioned) {
			return fmt.Errorf(
				"%w: failed AMO tombstone projection was not applied",
				ErrLedgerConflict,
			)
		}
	}
	if err := insertManagedRuntimeCapacityReleaseTx(
		ctx,
		tx,
		managedRuntimeCapacityReleaseFact{
			TenantID: current.Command.TenantID, LeaseID: current.Command.LeaseID,
			ResourceGenerationID: current.Command.ResourceGenerationID,
			ServerID:             current.Command.RuntimeServerID,
			ServerGeneration:     prepared.terminalGeneration,
			OperationID:          current.Command.OperationID,
			Authority:            resourceFreeCapacityReleaseAuthority,
			ReceiptSequence:      current.Head.Sequence,
			ReceiptDigest:        current.Head.ReceiptDigest,
			ReleasedAt:           terminalizedAt,
		},
	); err != nil {
		return err
	}
	return nil
}

func isImmediateJSONSafeSuccessor(previous int64, next uint64) bool {
	if previous <= 0 || next > providerexecutor.MaxJSONSafeInteger {
		return false
	}
	// #nosec G115 -- previous is checked positive and next's JSON-safe bound is below MaxInt64.
	return uint64(previous)+1 == next
}

func resourceFreeTeardownServerEvent(
	record OperationRecord,
	head receiptRuntimeServerHead,
	lifecycle string,
	reason string,
	decommissionedAt *time.Time,
	observedAt time.Time,
) controlplane.ServerEvent {
	stage := lifecycle
	return controlplane.ServerEvent{
		TenantID: record.Command.TenantID, ServerID: record.Command.RuntimeServerID,
		ExpectedRevision: head.Revision, Generation: head.Generation,
		Authority:    controlplane.ServerEventAuthorityControlPlane,
		Source:       providerResourceFreeTeardownSource,
		SourceID:     record.Command.OperationID + ":" + stage,
		ObservedAt:   observedAt,
		ClearOutcome: true, OutcomeResetReason: "provider_resources_absent",
		Runtime: controlplane.ServerRuntime{
			LifecycleState:      lifecycle,
			LifecycleReasonCode: reason,
			DesiredState:        string(serverregistry.DesiredAbsent),
			DecommissionedAt:    decommissionedAt,
		},
		Evidence: map[string]any{
			"operation_id":            record.Command.OperationID,
			"operation":               string(record.Command.Operation),
			"head_sequence":           record.Head.Sequence,
			"head_receipt_digest":     record.Head.ReceiptDigest,
			"lease_id":                record.Command.LeaseID,
			"lease_revision":          record.Command.LeaseRevision,
			"resource_generation_id":  record.Command.ResourceGenerationID,
			"provider_resource_state": "absent",
		},
	}
}

func (p *postgresProviderReceiptRuntimeProjector) ApplyTx(
	ctx context.Context,
	tx *sql.Tx,
	prepared preparedProviderReceiptRuntimeProjection,
) error {
	if prepared.serverEvent != nil {
		result, eventErr := p.servers.ApplyServerEventTx(ctx, tx, *prepared.serverEvent)
		if eventErr != nil {
			return fmt.Errorf("providercontrol: project provider receipt to runtime server: %w", eventErr)
		}
		if result == nil || !result.Applied || result.Server == nil {
			return fmt.Errorf("%w: provider receipt runtime server projection was not applied", ErrLedgerConflict)
		}
	}
	if prepared.capacityRelease != nil {
		if releaseErr := insertManagedRuntimeCapacityReleaseTx(ctx, tx, *prepared.capacityRelease); releaseErr != nil {
			return releaseErr
		}
	}
	if prepared.runtimeTarget != nil {
		if targetErr := projectProviderRuntimeTargetTx(ctx, tx, *prepared.runtimeTarget); targetErr != nil {
			return targetErr
		}
	}
	return nil
}

func runtimeTargetFromProviderReceipt(
	record OperationRecord,
	receipt providerexecutor.Receipt,
) (*providerRuntimeTargetProjection, error) {
	serverID := ""
	publicIP := ""
	for _, resource := range receipt.Resources {
		switch resource.BindingID {
		case runtimeServerBindingID:
			if serverID != "" {
				return nil, fmt.Errorf("%w: provider receipt contains duplicate runtime server roles", ErrCleanupCustody)
			}
			parsed, parseErr := runtimeServerIDFromNativeRef(resource.NativeRef)
			if parseErr != nil {
				return nil, fmt.Errorf("%w: invalid runtime server reference: %v", ErrCleanupCustody, parseErr)
			}
			serverID = parsed
		case runtimePublicIPBindingID:
			if publicIP != "" {
				return nil, fmt.Errorf("%w: provider receipt contains duplicate runtime public IP roles", ErrCleanupCustody)
			}
			parsed, parseErr := runtimePublicIPv4FromNativeRef(resource.NativeRef)
			if parseErr != nil {
				return nil, fmt.Errorf("%w: invalid runtime public IP reference: %v", ErrCleanupCustody, parseErr)
			}
			publicIP = parsed
		}
	}
	if serverID == "" || publicIP == "" {
		return nil, fmt.Errorf("%w: provider receipt is missing an exact runtime target", ErrCleanupCustody)
	}
	return &providerRuntimeTargetProjection{
		TenantID: record.Command.TenantID, LeaseID: record.Command.LeaseID,
		RuntimeServerID:      record.Command.RuntimeServerID,
		ResourceGenerationID: record.Command.ResourceGenerationID,
		ProviderID:           record.Command.ProviderID, EngineVMID: serverID, PublicIP: publicIP,
		ObservedAt: receipt.IssuedAt.UTC(),
	}, nil
}

func runtimeServerIDFromNativeRef(nativeRef string) (string, error) {
	parsed, err := parseRuntimeNativeRef(nativeRef)
	if err != nil {
		return "", err
	}
	if parsed.RawPath != "" || parsed.Opaque != "" {
		return "", errors.New("encoded or opaque paths are not accepted")
	}
	if (parsed.Scheme == "" && (parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/"))) ||
		(parsed.Scheme != "" && parsed.Host == "") {
		return "", errors.New("server reference must be an absolute path or hierarchical URI")
	}
	if strings.HasSuffix(parsed.Path, "/") {
		return "", errors.New("server reference must end with an exact identifier")
	}
	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return "", errors.New("server path is empty")
	}
	parts := strings.Split(path, "/")
	serverID := parts[len(parts)-1]
	if serverID == "" || serverID == "." || serverID == ".." ||
		strings.IndexFunc(serverID, unicode.IsSpace) >= 0 {
		return "", errors.New("server identifier is invalid")
	}
	return serverID, nil
}

func runtimePublicIPv4FromNativeRef(nativeRef string) (string, error) {
	parsed, err := parseRuntimeNativeRef(nativeRef)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" ||
		parsed.Port() != "" {
		return "", errors.New("public IP reference must be an authority-only URI")
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() ||
		sharedIPv4AddressSpace.Contains(address) {
		return "", errors.New("public IP reference must contain a publicly routable IPv4 address")
	}
	return address.String(), nil
}

func parseRuntimeNativeRef(nativeRef string) (*url.URL, error) {
	if nativeRef == "" || strings.TrimSpace(nativeRef) != nativeRef ||
		strings.IndexFunc(nativeRef, unicode.IsControl) >= 0 {
		return nil, errors.New("native reference is empty or contains whitespace or control characters")
	}
	parsed, err := url.Parse(nativeRef)
	if err != nil {
		return nil, fmt.Errorf("parse native reference: %w", err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("userinfo, query, and fragment are not accepted")
	}
	return parsed, nil
}

func projectProviderRuntimeTargetTx(
	ctx context.Context,
	tx *sql.Tx,
	target providerRuntimeTargetProjection,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE techstack_vm_leases
SET engine_vm_id = $6,
    lease_json = jsonb_set(
      jsonb_set(
        jsonb_set(
          lease_json,
          '{resource,engine_vm_id}', to_jsonb($6::text), true
        ),
        '{resource,vm_id}', to_jsonb($6::text), true
      ),
      '{metadata}',
      COALESCE(lease_json->'metadata', '{}'::jsonb) ||
      jsonb_build_object(
        'runtime_public_ip', $7::text,
        'runtime_ssh_host', $7::text,
        'runtime_ssh_user', 'kombify',
        'runtime_ssh_port', '22'
      ),
      true
    ),
    updated_at = clock_timestamp()
WHERE tenant_id = $1
  AND id = $2
  AND server_id = $3
  AND resource_generation_id = $4::uuid
  AND provider_id = $5
  AND cancelled_at IS NULL`,
		target.TenantID, target.LeaseID, target.RuntimeServerID,
		target.ResourceGenerationID, target.ProviderID, target.EngineVMID, target.PublicIP,
	)
	if err != nil {
		return fmt.Errorf("providercontrol: project provider runtime target: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("%w: provider runtime target projection lost lease custody", ErrLeaseFence)
	}
	return nil
}

func receiptProjectsRuntimeLifecycle(
	command providerexecutor.Command,
	next providerexecutor.Receipt,
) bool {
	if command.Operation == providerexecutor.OperationProvision {
		return (next.Status == providerexecutor.StatusSucceeded && next.Phase == providerexecutor.PhasePresent) ||
			(next.Status == providerexecutor.StatusFailed && next.Phase == providerexecutor.PhaseFailed)
	}
	return command.Operation == providerexecutor.OperationDecommission &&
		next.Status == providerexecutor.StatusSucceeded &&
		next.Phase == providerexecutor.PhaseAbsent
}

func lockProviderResourceGenerationTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) error {
	_, err := tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		command.TenantID,
		command.LeaseID+":"+command.ResourceGenerationID,
	)
	if err != nil {
		return fmt.Errorf("providercontrol: lock provider resource generation: %w", err)
	}
	return nil
}

func bindReceiptResourcesTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	next providerexecutor.Receipt,
) error {
	for _, resource := range next.Resources {
		if _, insertErr := tx.ExecContext(ctx, `
			INSERT INTO server_provider_resource_bindings (
				tenant_id, server_id, server_generation, lease_id,
				resource_generation_id, operation_id, binding_id, bound_at
			) VALUES ($1,$2,$3,$4,$5::uuid,$6,$7,clock_timestamp())
			ON CONFLICT (tenant_id, operation_id, binding_id) DO NOTHING
		`, record.Command.TenantID, record.Command.RuntimeServerID,
			record.RuntimeServerGeneration, record.Command.LeaseID,
			record.Command.ResourceGenerationID, record.Command.OperationID,
			resource.BindingID); insertErr != nil {
			return fmt.Errorf("providercontrol: bind provider resource to runtime server: %w", insertErr)
		}
		var serverID, leaseID, generationID string
		var serverGeneration int64
		if validateErr := tx.QueryRowContext(ctx, `
			SELECT server_id, server_generation, lease_id, resource_generation_id::text
			FROM server_provider_resource_bindings
			WHERE tenant_id = $1 AND operation_id = $2 AND binding_id = $3
		`, record.Command.TenantID, record.Command.OperationID, resource.BindingID).Scan(
			&serverID,
			&serverGeneration,
			&leaseID,
			&generationID,
		); validateErr != nil {
			return fmt.Errorf("providercontrol: validate provider resource runtime binding: %w", validateErr)
		}
		if serverID != record.Command.RuntimeServerID ||
			serverGeneration != record.RuntimeServerGeneration ||
			leaseID != record.Command.LeaseID ||
			generationID != record.Command.ResourceGenerationID {
			return fmt.Errorf(
				"%w: provider resource %q is already bound to another runtime generation",
				ErrLedgerConflict,
				resource.BindingID,
			)
		}
	}
	return nil
}

func loadReceiptRuntimeServerHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	serverID string,
) (receiptRuntimeServerHead, error) {
	var head receiptRuntimeServerHead
	var leaseID, providerRef sql.NullString
	var decommissionedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT revision, generation, lease_id, provider_ref,
		       lifecycle_state, desired_state, decommissioned_at
		FROM servers
		WHERE tenant_id = current_setting('app.tenant_id', true) AND id = $1
		FOR UPDATE
	`, serverID).Scan(
		&head.Revision,
		&head.Generation,
		&leaseID,
		&providerRef,
		&head.LifecycleState,
		&head.DesiredState,
		&decommissionedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return receiptRuntimeServerHead{}, fmt.Errorf(
			"%w: runtime server %q no longer exists",
			ErrLeaseFence,
			serverID,
		)
	}
	if err != nil {
		return receiptRuntimeServerHead{}, fmt.Errorf(
			"providercontrol: lock runtime server receipt projection: %w",
			err,
		)
	}
	head.LeaseID = leaseID.String
	head.ProviderRef = providerRef.String
	if decommissionedAt.Valid {
		at := decommissionedAt.Time.UTC()
		head.DecommissionedAt = &at
	}
	return head, nil
}

func loadReceiptRuntimeLeaseHeadTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) (receiptRuntimeLeaseHead, error) {
	var head receiptRuntimeLeaseHead
	var cancelledAt sql.NullTime
	var runtimeServerID, resourceGenerationID sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT desired_state, cancelled_at, server_id, resource_generation_id
		FROM provider_control_lock_runtime_lease_projection($1)
	`, command.LeaseID).Scan(
		&head.DesiredState,
		&cancelledAt,
		&runtimeServerID,
		&resourceGenerationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return receiptRuntimeLeaseHead{}, fmt.Errorf(
			"%w: runtime lease %q no longer exists",
			ErrLeaseFence,
			command.LeaseID,
		)
	}
	if err != nil {
		return receiptRuntimeLeaseHead{}, fmt.Errorf(
			"providercontrol: read runtime lease receipt projection: %w",
			err,
		)
	}
	if !runtimeServerID.Valid || runtimeServerID.String != command.RuntimeServerID ||
		!resourceGenerationID.Valid ||
		resourceGenerationID.String != command.ResourceGenerationID {
		return receiptRuntimeLeaseHead{}, fmt.Errorf(
			"%w: runtime lease %q changed server or resource generation",
			ErrLeaseFence,
			command.LeaseID,
		)
	}
	if cancelledAt.Valid {
		at := cancelledAt.Time.UTC()
		head.CancelledAt = &at
	}
	return head, nil
}

func providerReceiptServerEvent(
	record OperationRecord,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
	lifecycle string,
	reason string,
	decommissionedAt *time.Time,
) *controlplane.ServerEvent {
	event := &controlplane.ServerEvent{
		TenantID: record.Command.TenantID, ServerID: record.Command.RuntimeServerID,
		ExpectedRevision: head.Revision, Generation: head.Generation,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    providerReceiptFinalizerSource, SourceID: record.Command.OperationID,
		ObservedAt: next.IssuedAt.UTC(),
		Runtime: controlplane.ServerRuntime{
			LifecycleState: lifecycle, LifecycleReasonCode: reason,
			DecommissionedAt: decommissionedAt,
		},
		Evidence: map[string]any{
			"operation_id":           record.Command.OperationID,
			"operation":              string(record.Command.Operation),
			"receipt_sequence":       next.Sequence,
			"receipt_digest":         next.ReceiptDigest,
			"receipt_phase":          string(next.Phase),
			"lease_id":               record.Command.LeaseID,
			"lease_revision":         record.Command.LeaseRevision,
			"resource_generation_id": record.Command.ResourceGenerationID,
		},
	}
	supportContext := map[string]any{
		"operation_id":     record.Command.OperationID,
		"lease_id":         record.Command.LeaseID,
		"receipt_sequence": next.Sequence,
	}
	switch serverregistry.LifecycleState(lifecycle) {
	case serverregistry.LifecycleEnrolling:
		event.Outcome = providerEnrollmentOutcome(record.Command.ProviderID, supportContext)
	case serverregistry.LifecycleFailed:
		event.Outcome = providerFailedOutcome(record.Command.ProviderID, supportContext)
	case serverregistry.LifecycleDecommissioned:
		event.ClearOutcome = true
		event.OutcomeResetReason = "provider_absence_confirmed"
	}
	return event
}

// providerReceiptRuntimeTargetServerEvent writes only the classification that
// the provider receipt has proven. It intentionally leaves lifecycle, desired
// state and Guard-owned observations untouched.
func providerReceiptRuntimeTargetServerEvent(
	record OperationRecord,
	next providerexecutor.Receipt,
	head receiptRuntimeServerHead,
	target *providerRuntimeTargetProjection,
) *controlplane.ServerEvent {
	classification := serverregistry.UnknownRuntimeTarget()
	var metadata map[string]any
	if target != nil {
		classification = serverregistry.ManagedVPSRuntimeTarget(
			target.ProviderID, target.EngineVMID, target.LeaseID, target.ObservedAt,
		)
		if record.ExecutionProfile.CredentialMode == CredentialModeWorkerHeld {
			classification = serverregistry.SubstrateVMRuntimeTarget(target.EngineVMID, target.LeaseID, target.ObservedAt)
		}
		metadata = map[string]any{
			"engine_vm_id": target.EngineVMID, "runtime_public_ip": target.PublicIP,
			"runtime_ssh_host": target.PublicIP, "runtime_ssh_user": "kombify", "runtime_ssh_port": 22,
		}
	}
	event := &controlplane.ServerEvent{
		TenantID: record.Command.TenantID, ServerID: record.Command.RuntimeServerID,
		ExpectedRevision: head.Revision, Generation: head.Generation,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    providerReceiptFinalizerSource, SourceID: record.Command.OperationID,
		ObservedAt: next.IssuedAt.UTC(),
		Runtime: controlplane.ServerRuntime{
			RuntimeTarget: classification,
			Metadata:      metadata,
		},
		Evidence: map[string]any{
			"operation_id": record.Command.OperationID, "receipt_sequence": next.Sequence,
			"receipt_digest": next.ReceiptDigest, "lease_id": record.Command.LeaseID,
			"resource_generation_id": record.Command.ResourceGenerationID,
		},
	}
	if serverregistry.LifecycleState(head.LifecycleState) == serverregistry.LifecycleEnrolling {
		event.Outcome = providerEnrollmentOutcome(record.Command.ProviderID, map[string]any{
			"operation_id":     record.Command.OperationID,
			"lease_id":         record.Command.LeaseID,
			"receipt_sequence": next.Sequence,
		})
	}
	return event
}

// requireNoUnsettledGenerationSideEffectsTx treats TTL only as execution
// capability expiry. A side-effecting claim remains ambiguous while its exact
// operation head has not advanced, even if the claim was released or expired.
// A consumed receipt, a later durable head, or an exact operator-verified
// no-candidate decision is the settlement authority.
func requireNoUnsettledGenerationSideEffectsTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) error {
	var unsettled int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		FROM provider_operation_execution_claims AS claim
		JOIN provider_operations AS operation
		  ON operation.tenant_id = claim.tenant_id
		 AND operation.operation_id = claim.operation_id
		WHERE operation.tenant_id = $1
		  AND operation.lease_id = $2
		  AND operation.command_json #>> '{command,resource_generation_id}' = $3
		  AND claim.claim_access = 'side_effecting'
		  AND claim.state IN ('active', 'released')
		  AND operation.head_sequence = claim.head_sequence
		  AND operation.head_receipt_digest = claim.head_receipt_digest
		  AND NOT EXISTS (
		      SELECT 1
		      FROM provider_provision_resolution_decisions AS decision
		      WHERE decision.tenant_id = claim.tenant_id
		        AND decision.operation_id = claim.operation_id
		        AND decision.expected_head_sequence = claim.head_sequence
		        AND decision.expected_head_receipt_digest = claim.head_receipt_digest
		        AND decision.outcome = 'no_candidate_observed'
		  )
	`, command.TenantID, command.LeaseID, command.ResourceGenerationID).Scan(&unsettled); err != nil {
		return fmt.Errorf("providercontrol: inspect generation side-effect settlement: %w", err)
	}
	if unsettled != 0 {
		return fmt.Errorf(
			"%w: resource generation still has %d unsettled side-effecting operation head(s)",
			ErrLeaseFence,
			unsettled,
		)
	}
	return nil
}

func requireResourceFreeGenerationQuiescenceTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) error {
	if err := requireNoUnsettledGenerationSideEffectsTx(ctx, tx, command); err != nil {
		return err
	}
	var unsettledGuards int64
	if err := tx.QueryRowContext(ctx, `
		SELECT provider_control_count_unsettled_generation_dispatch_guards(
		    $1,
		    $2::uuid
		)
	`, command.LeaseID, command.ResourceGenerationID).Scan(
		&unsettledGuards,
	); err != nil {
		return fmt.Errorf("providercontrol: inspect generation dispatch settlement: %w", err)
	}
	if unsettledGuards != 0 {
		return fmt.Errorf(
			"%w: resource generation still has %d unsettled provision dispatch guard(s)",
			ErrLeaseFence,
			unsettledGuards,
		)
	}
	return nil
}

type boundProviderResource struct {
	BindingID       string
	Kind            string
	NativeRef       string
	ParentBindingID string
	OwnershipHash   string
	Disposition     providerexecutor.ResourceDisposition
}

func requireDefinitiveGenerationAbsenceTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	next providerexecutor.Receipt,
) error {
	// Guard enrollment may advance the RuntimeServer's numeric projection
	// generation without replacing the provider resource. Resource identity
	// therefore follows the exact tenant/server/lease/UUID generation graph;
	// the decommission operation and lifecycle write remain independently
	// pinned to the current numeric RuntimeServer generation.
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT resource.binding_id, resource.kind, resource.native_ref,
		       coalesce(resource.parent_binding_id, ''), resource.ownership_hash,
		       resource.disposition
		FROM server_provider_resource_bindings AS binding
		JOIN provider_operation_resources AS resource
		  ON resource.tenant_id = binding.tenant_id
		 AND resource.operation_id = binding.operation_id
		 AND resource.binding_id = binding.binding_id
		JOIN provider_operations AS source_operation
		  ON source_operation.tenant_id = binding.tenant_id
		 AND source_operation.operation_id = binding.operation_id
		WHERE binding.tenant_id = $1
		  AND binding.server_id = $2
		  AND binding.lease_id = $3
		  AND binding.resource_generation_id = $4::uuid
		  AND source_operation.operation = 'provision'
		  AND (
		      (
		          source_operation.status = 'succeeded'
		          AND source_operation.phase = 'present'
		      )
		      OR (
		          source_operation.status = 'failed'
		          AND source_operation.phase = 'failed'
		      )
		      OR EXISTS (
		          SELECT 1
		          FROM provider_provision_resolution_decisions AS decision
		          WHERE decision.tenant_id = source_operation.tenant_id
		            AND decision.operation_id = source_operation.operation_id
		            AND decision.outcome = 'adopted_exact_candidate'
		            AND decision.selected_candidate_digest IS NOT NULL
		            AND decision.result_receipt_sequence = source_operation.head_sequence
		            AND decision.result_receipt_digest = source_operation.head_receipt_digest
		      )
		  )
		ORDER BY resource.binding_id, resource.kind, resource.native_ref
	`, record.Command.TenantID, record.Command.RuntimeServerID,
		record.Command.LeaseID, record.Command.ResourceGenerationID)
	if err != nil {
		return fmt.Errorf("providercontrol: load generation resource graph: %w", err)
	}
	defer rows.Close()

	bound := make(map[string]boundProviderResource)
	for rows.Next() {
		var resource boundProviderResource
		if scanErr := rows.Scan(
			&resource.BindingID,
			&resource.Kind,
			&resource.NativeRef,
			&resource.ParentBindingID,
			&resource.OwnershipHash,
			&resource.Disposition,
		); scanErr != nil {
			return fmt.Errorf("providercontrol: scan generation resource graph: %w", scanErr)
		}
		if previous, exists := bound[resource.BindingID]; exists && previous != resource {
			return fmt.Errorf(
				"%w: generation contains conflicting identities for binding %q",
				ErrCleanupCustody,
				resource.BindingID,
			)
		}
		bound[resource.BindingID] = resource
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("providercontrol: iterate generation resource graph: %w", rowsErr)
	}

	absent := make(map[string]boundProviderResource, len(next.Resources))
	for _, resource := range next.Resources {
		if resource.Observation != providerexecutor.ObservationAbsent ||
			resource.Cleanup != providerexecutor.CleanupComplete {
			return fmt.Errorf(
				"%w: resource %q has no complete definitive absence",
				ErrCleanupCustody,
				resource.BindingID,
			)
		}
		absent[resource.BindingID] = boundProviderResource{
			BindingID: resource.BindingID, Kind: resource.Kind, NativeRef: resource.NativeRef,
			ParentBindingID: resource.ParentBindingID, OwnershipHash: resource.OwnershipHash,
			Disposition: resource.Disposition,
		}
	}
	if len(bound) == 0 || len(bound) != len(absent) {
		return fmt.Errorf(
			"%w: decommission receipt covers %d of %d generation resources",
			ErrCleanupCustody,
			len(absent),
			len(bound),
		)
	}
	for bindingID, expected := range bound {
		if actual, exists := absent[bindingID]; !exists || actual != expected {
			return fmt.Errorf(
				"%w: decommission receipt does not exactly cover generation resource %q",
				ErrCleanupCustody,
				bindingID,
			)
		}
	}
	return nil
}

func requireExactMutationTargetsTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
) error {
	// The Lease Authority UUID is stable across Guard-owned enrollment
	// generations. Only a new UUID may establish a replacement provider graph.
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT resource.binding_id, resource.kind, resource.native_ref,
		       coalesce(resource.parent_binding_id, ''), resource.ownership_hash,
		       resource.disposition
		FROM server_provider_resource_bindings AS binding
		JOIN provider_operation_resources AS resource
		  ON resource.tenant_id = binding.tenant_id
		 AND resource.operation_id = binding.operation_id
		 AND resource.binding_id = binding.binding_id
		JOIN provider_operations AS source_operation
		  ON source_operation.tenant_id = binding.tenant_id
		 AND source_operation.operation_id = binding.operation_id
		WHERE binding.tenant_id = $1
		  AND binding.server_id = $2
		  AND binding.lease_id = $3
		  AND binding.resource_generation_id = $4::uuid
		  AND source_operation.operation = 'provision'
		  AND (
		      (
		          $5 = 'reconcile'
		          AND source_operation.status = 'succeeded'
		          AND source_operation.phase = 'present'
		      )
		      OR (
		          $5 = 'decommission'
		          AND (
		              (
		                  source_operation.status = 'succeeded'
		                  AND source_operation.phase = 'present'
		              )
		              OR (
		                  source_operation.status = 'failed'
		                  AND source_operation.phase = 'failed'
		              )
		              OR EXISTS (
		                  SELECT 1
		                  FROM provider_provision_resolution_decisions AS decision
		                  WHERE decision.tenant_id = source_operation.tenant_id
		                    AND decision.operation_id = source_operation.operation_id
		                    AND decision.outcome = 'adopted_exact_candidate'
		                    AND decision.selected_candidate_digest IS NOT NULL
		                    AND decision.result_receipt_sequence = source_operation.head_sequence
		                    AND decision.result_receipt_digest = source_operation.head_receipt_digest
		              )
		          )
		      )
		  )
		ORDER BY resource.binding_id, resource.kind, resource.native_ref
	`, command.TenantID, command.RuntimeServerID,
		command.LeaseID, command.ResourceGenerationID, command.Operation)
	if err != nil {
		return fmt.Errorf("providercontrol: load mutation target authority: %w", err)
	}
	defer rows.Close()

	authoritative := make(map[string]boundProviderResource)
	for rows.Next() {
		var resource boundProviderResource
		if scanErr := rows.Scan(
			&resource.BindingID,
			&resource.Kind,
			&resource.NativeRef,
			&resource.ParentBindingID,
			&resource.OwnershipHash,
			&resource.Disposition,
		); scanErr != nil {
			return fmt.Errorf("providercontrol: scan mutation target authority: %w", scanErr)
		}
		if previous, exists := authoritative[resource.BindingID]; exists && previous != resource {
			return fmt.Errorf(
				"%w: generation contains conflicting identities for binding %q",
				ErrCleanupCustody,
				resource.BindingID,
			)
		}
		authoritative[resource.BindingID] = resource
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("providercontrol: iterate mutation target authority: %w", rowsErr)
	}

	targets := make(map[string]boundProviderResource, len(command.Targets))
	for _, target := range command.Targets {
		identity := boundProviderResource{
			BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef,
			ParentBindingID: target.ParentBindingID, OwnershipHash: target.OwnershipHash,
			Disposition: target.Disposition,
		}
		if previous, exists := targets[target.BindingID]; exists && previous != identity {
			return fmt.Errorf(
				"%w: %s command has conflicting target %q",
				ErrCleanupCustody,
				command.Operation,
				target.BindingID,
			)
		}
		targets[target.BindingID] = identity
	}
	if len(authoritative) == 0 || len(authoritative) != len(targets) {
		return fmt.Errorf(
			"%w: %s command covers %d of %d authoritative generation resources",
			ErrCleanupCustody,
			command.Operation,
			len(targets),
			len(authoritative),
		)
	}
	for bindingID, expected := range authoritative {
		if actual, exists := targets[bindingID]; !exists || actual != expected {
			return fmt.Errorf(
				"%w: %s command does not exactly target generation resource %q",
				ErrCleanupCustody,
				command.Operation,
				bindingID,
			)
		}
	}
	return nil
}

func lockManagedRuntimeCapacityReservationTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	leaseID string,
	resourceGenerationID string,
) error {
	var ownerID string
	err := tx.QueryRowContext(ctx, `
		SELECT owner_subject_id
		FROM managed_runtime_capacity_reservations
		WHERE tenant_id = $1 AND lease_id = $2 AND resource_generation_id = $3::uuid
	`, tenantID, leaseID, resourceGenerationID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"%w: generation has no managed runtime capacity reservation",
			ErrLeaseFence,
		)
	}
	if err != nil {
		return fmt.Errorf("providercontrol: load managed runtime capacity reservation: %w", err)
	}
	_, err = tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		tenantID,
		"providercontrol.capacity:"+managedRuntimeCapacityScopeOwner+":"+strings.TrimSpace(ownerID),
	)
	if err != nil {
		return fmt.Errorf("providercontrol: lock managed runtime capacity release: %w", err)
	}
	return nil
}

func insertManagedRuntimeCapacityReleaseTx(
	ctx context.Context,
	tx *sql.Tx,
	release managedRuntimeCapacityReleaseFact,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO managed_runtime_capacity_release_facts (
			tenant_id, lease_id, resource_generation_id, server_id,
			server_generation, release_operation_id, release_authority,
			receipt_sequence, receipt_digest, released_at
		) VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT DO NOTHING
	`, release.TenantID, release.LeaseID, release.ResourceGenerationID,
		release.ServerID, release.ServerGeneration, release.OperationID,
		release.Authority, release.ReceiptSequence, release.ReceiptDigest, release.ReleasedAt)
	if err != nil {
		return fmt.Errorf("providercontrol: persist managed runtime capacity release: %w", err)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("providercontrol: inspect managed runtime capacity release: %w", rowsErr)
	}
	if rows != 1 {
		var operationID, authority, receiptDigest string
		var receiptSequence uint64
		if loadErr := tx.QueryRowContext(ctx, `
			SELECT release_operation_id, release_authority, receipt_sequence, receipt_digest
			FROM managed_runtime_capacity_release_facts
			WHERE tenant_id = $1 AND lease_id = $2 AND resource_generation_id = $3::uuid
		`, release.TenantID, release.LeaseID, release.ResourceGenerationID).Scan(
			&operationID,
			&authority,
			&receiptSequence,
			&receiptDigest,
		); loadErr != nil {
			return fmt.Errorf("providercontrol: validate managed runtime capacity release replay: %w", loadErr)
		}
		if operationID != release.OperationID ||
			authority != release.Authority ||
			receiptSequence != release.ReceiptSequence ||
			receiptDigest != release.ReceiptDigest {
			return fmt.Errorf("%w: managed runtime capacity release conflicts", ErrLedgerConflict)
		}
	}
	return nil
}
