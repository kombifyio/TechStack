package providercontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	nativeProvisionOperationScope = "providercontrol.provision"
	nativeAdmissionDigestVersion  = "providercontrol/native-provision-admission/v2"
	nativeInitialLedgerRevision   = uint64(1)
	nativeAdmissionSource         = "providercontrol.native-admission"
)

// ErrNativeAdmissionConflict identifies an idempotency replay whose durable
// request digest differs from the newly supplied request.
var ErrNativeAdmissionConflict = errors.New("providercontrol: native admission conflict")

// NativeAdmissionServer carries the secret-free server identity fields needed
// to create the canonical runtime-server aggregate with a native lease.
type NativeAdmissionServer struct {
	InstanceID string
	StackID    string
	Name       string
	Metadata   map[string]any
}

// NativeProvisionAdmissionRequest contains caller intent only. Lease Authority
// generates ResourceGenerationID and database time; callers cannot supply
// either authority value.
type NativeProvisionAdmissionRequest struct {
	TenantID              string
	RuntimeSlotKey        string
	RuntimeSlotID         string
	RuntimeSlotGeneration uint64
	RuntimeServerID       string
	OwnerSubjectID        string
	IdempotencyKey        string
	// ValidFor is measured from the transaction's database time. Zero selects
	// vmleases.DefaultNativeAdmissionValidity; absolute caller times are rejected.
	ValidFor       time.Duration
	Lease          vmlease.Lease
	Server         NativeAdmissionServer
	DesiredSpecRef string
	DesiredSpec    json.RawMessage
}

// NativeProvisionAdmissionResult identifies the exact lease/server/generation
// and requested provider operation committed by one admission transaction.
type NativeProvisionAdmissionResult struct {
	RuntimeSlotKey        string
	RuntimeSlotID         string
	RuntimeSlotGeneration uint64
	LeaseID               string
	LeaseRevision         uint64
	RuntimeServerID       string
	ResourceGenerationID  string
	Operation             OperationRecord
	RequestDigest         string
	Created               bool
}

// NativeAdmissionConfig supplies the single durable store, coordinator, and
// mandatory mutation-activation decision used by native admission.
type NativeAdmissionConfig struct {
	Database                     *sql.DB
	Coordinator                  *Coordinator
	ActivationGate               MutationActivationGate
	ProviderCreates              ProviderCreateAuthorizer
	CapacityPolicy               CapacityPolicyResolver
	ManagedCredentialAuthorities ManagedCredentialAuthoritySet
}

// NativeAdmission atomically creates a native runtime lease, runtime-server
// aggregate, immutable execution authority, idempotency record, desired spec,
// and requested provider-control operation. It never invokes a provider.
type NativeAdmission struct {
	db                           *sql.DB
	coordinator                  *Coordinator
	ledger                       *PostgresLedger
	servers                      *controlplane.PostgresStore
	leases                       *vmleases.PostgresStore
	activation                   MutationActivationGate
	providerCreates              ProviderCreateAuthorizer
	capacity                     CapacityPolicyResolver
	managedCredentialAuthorities ManagedCredentialAuthoritySet
}

// NewNativeAdmission creates the Postgres-only native admission module. The
// coordinator must use the same Postgres ledger and activation gate, and its
// profile resolver must support same-transaction resolution.
func NewNativeAdmission(cfg NativeAdmissionConfig) (*NativeAdmission, error) {
	if cfg.Database == nil || cfg.Coordinator == nil || nilProviderControlInterface(cfg.ActivationGate) ||
		nilProviderControlInterface(cfg.ProviderCreates) ||
		nilProviderControlInterface(cfg.CapacityPolicy) {
		return nil, fmt.Errorf("%w: database, coordinator, activation gate, provider create policy, and capacity policy are required", ErrInvalidRequest)
	}
	ledger, ok := cfg.Coordinator.ledger.(*PostgresLedger)
	if !ok || ledger == nil || ledger.db != cfg.Database {
		return nil, fmt.Errorf("%w: coordinator must use the admission database ledger", ErrInvalidRequest)
	}
	if nilProviderControlInterface(cfg.Coordinator.activation) ||
		!sameComparableDependency(cfg.ActivationGate, cfg.Coordinator.activation) {
		return nil, fmt.Errorf("%w: admission and coordinator must use the same activation gate", ErrInvalidRequest)
	}
	resolver, ok := cfg.Coordinator.profiles.(transactionalExecutionProfileResolver)
	if !ok || nilProviderControlInterface(resolver) {
		return nil, fmt.Errorf("%w: transaction-aware execution profile resolver is required", ErrProfileUnavailable)
	}
	return &NativeAdmission{
		db: cfg.Database, coordinator: cfg.Coordinator, ledger: ledger,
		servers: controlplane.NewPostgresStore(cfg.Database), activation: cfg.ActivationGate,
		leases:          vmleases.NewPostgresStore(cfg.Database),
		providerCreates: cfg.ProviderCreates,
		capacity:        cfg.CapacityPolicy, managedCredentialAuthorities: cfg.ManagedCredentialAuthorities,
	}, nil
}

// PreflightProvision checks a fresh request's native availability before a
// caller persists or queues product workflow state. It performs only reads:
// request normalization, mutation-activation, request-bound product policy,
// and the current authoritative reservation count. A successful preflight is
// never an allow token; AdmitProvision repeats these mutable checks inside the
// transaction that creates the reservation and provider-control operation.
func (a *NativeAdmission) PreflightProvision(
	ctx context.Context,
	request NativeProvisionAdmissionRequest,
) error {
	if !a.configured() {
		return fmt.Errorf("%w: native admission is not configured", ErrInvalidRequest)
	}
	normalized, _, _, normalizeErr := normalizeNativeProvisionAdmission(request)
	if normalizeErr != nil {
		return normalizeErr
	}
	if normalized.Lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		return a.preflightSubstrate(ctx, normalized)
	}
	if createErr := a.providerCreates.RequireProviderCreate(ctx, normalized.Lease.Resource.ProviderID); createErr != nil {
		return createErr
	}
	capacity, capacityErr := resolveNativeAdmissionCapacity(ctx, a.capacity, normalized)
	if capacityErr != nil {
		return capacityErr
	}
	if activationErr := a.activation.Require(ctx); activationErr != nil {
		return activationErr
	}

	tx, beginErr := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if beginErr != nil {
		return fmt.Errorf("providercontrol: begin native admission preflight: %w", beginErr)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, setTenantErr := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, tenantContextKey, normalized.TenantID); setTenantErr != nil {
		return fmt.Errorf("providercontrol: set native admission preflight tenant context: %w", setTenantErr)
	}
	if custodyErr := requireNoUnclassifiedManagedRuntimeCustodyTx(ctx, tx, normalized); custodyErr != nil {
		return custodyErr
	}
	if capacityErr := requireManagedRuntimeCapacityTx(ctx, tx, normalized.TenantID, capacity); capacityErr != nil {
		return capacityErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("providercontrol: commit native admission preflight: %w", commitErr)
	}
	committed = true
	return nil
}

// AdmitProvision persists one native provision intent before any provider call.
// A same-digest replay returns the exact existing operation; a different digest
// under the same tenant/scope/key fails without changing durable state.
func (a *NativeAdmission) AdmitProvision(
	ctx context.Context,
	request NativeProvisionAdmissionRequest,
) (NativeProvisionAdmissionResult, error) {
	if !a.configured() {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("%w: native admission is not configured", ErrInvalidRequest)
	}
	normalized, digest, digestText, normalizeErr := normalizeNativeProvisionAdmission(request)
	if normalizeErr != nil {
		return NativeProvisionAdmissionResult{}, normalizeErr
	}
	// The transaction-scoped advisory lock serializes the exact
	// tenant/scope/idempotency identity. READ COMMITTED is intentional: a
	// replica which began while another holder owned the lock must take a fresh
	// statement snapshot after acquiring it and observe the committed replay.
	tx, beginErr := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if beginErr != nil {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: begin native admission: %w", beginErr)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, setTenantErr := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, tenantContextKey, normalized.TenantID); setTenantErr != nil {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: set native admission tenant context: %w", setTenantErr)
	}
	if lockErr := lockNativeAdmissionTx(ctx, tx, normalized.TenantID, normalized.IdempotencyKey); lockErr != nil {
		return NativeProvisionAdmissionResult{}, lockErr
	}
	replay, found, replayErr := a.loadNativeAdmissionReplayTx(ctx, tx, normalized, digest, digestText)
	if replayErr != nil {
		return NativeProvisionAdmissionResult{}, replayErr
	}
	if found {
		if commitErr := tx.Commit(); commitErr != nil {
			return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: commit native admission replay: %w", commitErr)
		}
		committed = true
		return replay, nil
	}
	// This is the last read-only point. A closed gate therefore performs no
	// writes and does not consume a resource-generation UUID.
	capacity, createErr := a.requireNativeAdmissionCreateTx(ctx, tx, normalized)
	if createErr != nil {
		return NativeProvisionAdmissionResult{}, createErr
	}
	result, insertErr := a.insertNativeAdmissionTx(ctx, tx, normalized, digest, digestText, capacity)
	if insertErr != nil {
		return NativeProvisionAdmissionResult{}, insertErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: commit native admission: %w", commitErr)
	}
	committed = true
	return result, nil
}

func (a *NativeAdmission) configured() bool {
	return a != nil && a.db != nil && a.coordinator != nil && a.ledger != nil && a.servers != nil && a.leases != nil &&
		!nilProviderControlInterface(a.providerCreates) && !nilProviderControlInterface(a.capacity)
}

func (a *NativeAdmission) requireNativeAdmissionCreateTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeProvisionAdmissionRequest,
) (normalizedCapacityGrant, error) {
	if request.Lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		if err := a.activation.Require(ctx); err != nil {
			return normalizedCapacityGrant{}, err
		}
		_, err := requireSubstrateBindingTx(ctx, tx, request.TenantID, request.OwnerSubjectID, request.Lease)
		return normalizedCapacityGrant{}, err
	}
	if err := a.providerCreates.RequireProviderCreate(ctx, request.Lease.Resource.ProviderID); err != nil {
		return normalizedCapacityGrant{}, err
	}
	if err := a.activation.Require(ctx); err != nil {
		return normalizedCapacityGrant{}, err
	}
	capacity, err := resolveNativeAdmissionCapacity(ctx, a.capacity, request)
	if err != nil {
		return normalizedCapacityGrant{}, err
	}
	if err := requireNoUnclassifiedManagedRuntimeCustodyTx(ctx, tx, request); err != nil {
		return normalizedCapacityGrant{}, err
	}
	if err := lockManagedRuntimeCapacityTx(ctx, tx, request.TenantID, capacity); err != nil {
		return normalizedCapacityGrant{}, err
	}
	if err := requireManagedRuntimeCapacityTx(ctx, tx, request.TenantID, capacity); err != nil {
		return normalizedCapacityGrant{}, err
	}
	return capacity, nil
}

func (a *NativeAdmission) insertNativeAdmissionTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeProvisionAdmissionRequest,
	requestDigest [sha256.Size]byte,
	requestDigestText string,
	capacity normalizedCapacityGrant,
) (NativeProvisionAdmissionResult, error) {
	admittedLease, admitLeaseErr := a.leases.AdmitNativeLeaseTx(ctx, tx, vmleases.NativeAdmissionRequest{
		Lease: request.Lease, OwnerSubjectID: request.OwnerSubjectID,
		ServerID: request.RuntimeServerID, IdempotencyKey: request.IdempotencyKey,
		ValidFor: request.ValidFor,
	})
	if admitLeaseErr != nil {
		if errors.Is(admitLeaseErr, vmleases.ErrLeaseIdentityConflict) {
			return NativeProvisionAdmissionResult{}, fmt.Errorf("%w: native lease identity already exists", ErrNativeAdmissionConflict)
		}
		return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: admit native lease: %w", admitLeaseErr)
	}
	lease := admittedLease.Lease
	generationID := admittedLease.ResourceGenerationID
	databaseNow := admittedLease.DatabaseTime
	leaseRevision := admittedLease.LeaseRevision
	serverDesired, desiredStateErr := nativeServerDesiredState(lease.DesiredState)
	if desiredStateErr != nil {
		return NativeProvisionAdmissionResult{}, desiredStateErr
	}
	target := serverregistry.ManagedVPSRuntimeTarget(lease.Resource.ProviderID, lease.Resource.EngineVMID, string(lease.ID), databaseNow)
	if lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		target = serverregistry.SubstrateVMRuntimeTarget(lease.Resource.EngineVMID, string(lease.ID), databaseNow)
	}
	serverEvent := controlplane.ServerEvent{
		TenantID: request.TenantID, ServerID: request.RuntimeServerID,
		ExpectedRevision: 0, Generation: 1,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    nativeAdmissionSource, SourceID: "admission:" + hex.EncodeToString(requestDigest[:16]),
		ObservedAt: databaseNow,
		Outcome: providerProvisioningOutcome(lease.Resource.ProviderID, map[string]any{
			"runtime_server_id": request.RuntimeServerID,
			"lease_id":          string(lease.ID),
		}),
		Runtime: controlplane.ServerRuntime{
			ID: request.RuntimeServerID, TenantID: request.TenantID,
			InstanceID: request.Server.InstanceID, StackID: request.Server.StackID,
			OwnerSubjectID: request.OwnerSubjectID, LeaseID: string(lease.ID),
			ProviderRef:   lease.Resource.ProviderID,
			RuntimeTarget: target,
			Name:          request.Server.Name, LifecycleState: string(serverregistry.LifecyclePlanned),
			DesiredState: string(serverDesired), ConnectionState: string(serverregistry.ConnectionPending),
			HealthState: string(serverregistry.HealthUnknown), Metadata: request.Server.Metadata,
		},
		Evidence: map[string]any{
			"operation_scope": nativeProvisionOperationScope,
			"request_digest":  requestDigestText,
		},
	}
	serverResult, applyServerErr := a.servers.ApplyServerEventTx(ctx, tx, serverEvent)
	if applyServerErr != nil {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("providercontrol: create native runtime server: %w", applyServerErr)
	}
	if !nativeServerAdmissionMatches(serverResult, string(lease.ID)) {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("%w: runtime server admission projection mismatch", ErrNativeAdmissionConflict)
	}
	if lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		if err := ensureSubstrateCredentialAuthorityTx(ctx, tx, request.TenantID, request.OwnerSubjectID, lease); err != nil {
			return NativeProvisionAdmissionResult{}, err
		}
	} else if credentialErr := ensureManagedCredentialAuthorityTx(
		ctx, tx, request.TenantID, lease, databaseNow, a.managedCredentialAuthorities,
	); credentialErr != nil {
		return NativeProvisionAdmissionResult{}, credentialErr
	}
	desiredSpec := &DesiredSpecRevision{
		TenantID: request.TenantID, LeaseID: string(lease.ID), Revision: leaseRevision,
		Ref: request.DesiredSpecRef, Digest: sha256Digest(request.DesiredSpec),
		Payload: request.DesiredSpec, CreatedAt: databaseNow,
	}
	prepared, prepareStartErr := a.coordinator.prepareStartTx(ctx, tx, StartRequest{
		TenantID: request.TenantID, LeaseID: string(lease.ID),
		LeaseRevision: leaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: generationID, Operation: providerexecutor.OperationProvision,
		IdempotencyKey: request.IdempotencyKey, LedgerRevision: nativeInitialLedgerRevision,
		DesiredSpec: desiredSpec, RequestedAt: databaseNow,
	})
	if prepareStartErr != nil {
		return NativeProvisionAdmissionResult{}, prepareStartErr
	}
	if prepared.command.ProviderID != lease.Resource.ProviderID {
		return NativeProvisionAdmissionResult{}, fmt.Errorf(
			"%w: execution profile provider %q does not match lease provider %q",
			ErrProfileUnavailable, prepared.command.ProviderID, lease.Resource.ProviderID,
		)
	}
	if authorityErr := insertNativeExecutionAuthorityTx(ctx, tx, request.TenantID, string(lease.ID), databaseNow); authorityErr != nil {
		return NativeProvisionAdmissionResult{}, authorityErr
	}
	record, created, beginOperationErr := a.ledger.beginOperationInTx(
		ctx, tx, ExecutionAuthorityTechstackProviderControl, prepared.profileSnapshot,
		prepared.provisionDispatch, prepared.command, prepared.initial, prepared.desiredSpec,
	)
	if beginOperationErr != nil {
		return NativeProvisionAdmissionResult{}, beginOperationErr
	}
	if !created {
		return NativeProvisionAdmissionResult{}, fmt.Errorf("%w: provider operation already exists without admission custody", ErrNativeAdmissionConflict)
	}
	if idempotencyErr := insertNativeIdempotencyTx(
		ctx, tx, request.TenantID, request.IdempotencyKey, requestDigest[:], string(lease.ID), record.Command.OperationID,
	); idempotencyErr != nil {
		return NativeProvisionAdmissionResult{}, idempotencyErr
	}
	result := NativeProvisionAdmissionResult{
		RuntimeSlotKey: request.RuntimeSlotKey, RuntimeSlotID: request.RuntimeSlotID,
		RuntimeSlotGeneration: request.RuntimeSlotGeneration,
		LeaseID:               string(lease.ID), LeaseRevision: leaseRevision,
		RuntimeServerID: request.RuntimeServerID, ResourceGenerationID: generationID,
		Operation: record, RequestDigest: requestDigestText, Created: true,
	}
	if lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		return result, insertSubstrateGuestCustodyTx(ctx, tx, request, result)
	}
	if capacityErr := insertManagedRuntimeCapacityReservationTx(ctx, tx, request, result, capacity, databaseNow); capacityErr != nil {
		return NativeProvisionAdmissionResult{}, capacityErr
	}
	if slotErr := insertManagedRuntimeServerSlotTx(ctx, tx, request, result, databaseNow); slotErr != nil {
		return NativeProvisionAdmissionResult{}, slotErr
	}
	return result, nil
}

func nativeServerAdmissionMatches(result *controlplane.ServerEventResult, leaseID string) bool {
	return result != nil && result.Applied && result.Server != nil &&
		result.Server.Revision == 1 && result.Server.Generation == 1 && result.Server.LeaseID == leaseID
}

func (a *NativeAdmission) loadNativeAdmissionReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeProvisionAdmissionRequest,
	requestDigest [sha256.Size]byte,
	requestDigestText string,
) (NativeProvisionAdmissionResult, bool, error) {
	var storedDigest []byte
	var leaseID, operationID string
	err := tx.QueryRowContext(ctx, `
		SELECT request_digest, lease_id, operation_id
		FROM runtime_lease_idempotency_records
		WHERE tenant_id = $1 AND operation_scope = $2 AND idempotency_key = $3
	`, request.TenantID, nativeProvisionOperationScope, request.IdempotencyKey).Scan(
		&storedDigest, &leaseID, &operationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeProvisionAdmissionResult{}, false, nil
	}
	if err != nil {
		return NativeProvisionAdmissionResult{}, false, fmt.Errorf("providercontrol: load native admission replay: %w", err)
	}
	if !nativeAdmissionReplayIdentityMatches(storedDigest, requestDigest, leaseID, operationID, request) {
		return NativeProvisionAdmissionResult{}, false, fmt.Errorf(
			"%w: idempotency key %q was admitted with a different request",
			ErrNativeAdmissionConflict, request.IdempotencyKey,
		)
	}
	record, err := loadOperationAdmissionReplayTx(ctx, tx, request.TenantID, operationID)
	if err != nil {
		return NativeProvisionAdmissionResult{}, false, err
	}
	if validationErr := validateOperationRecord(ctx, record, a.ledger.verifier); validationErr != nil {
		return NativeProvisionAdmissionResult{}, false, validationErr
	}
	var executionAuthority string
	if err := tx.QueryRowContext(ctx, `
		SELECT execution_authority
		FROM runtime_lease_execution_authorities
		WHERE tenant_id = $1 AND lease_id = $2
	`, request.TenantID, leaseID).Scan(&executionAuthority); err != nil {
		return NativeProvisionAdmissionResult{}, false, fmt.Errorf("providercontrol: load native admission authority: %w", err)
	}
	if !nativeAdmissionReplayCustodyMatches(executionAuthority, record, leaseID, request) {
		return NativeProvisionAdmissionResult{}, false, fmt.Errorf("%w: replay custody projection mismatch", ErrNativeAdmissionConflict)
	}
	result := NativeProvisionAdmissionResult{
		RuntimeSlotKey: request.RuntimeSlotKey, RuntimeSlotID: request.RuntimeSlotID,
		RuntimeSlotGeneration: request.RuntimeSlotGeneration,
		LeaseID:               leaseID, LeaseRevision: record.Command.LeaseRevision,
		RuntimeServerID:      record.Command.RuntimeServerID,
		ResourceGenerationID: record.Command.ResourceGenerationID, Operation: record,
		RequestDigest: requestDigestText, Created: false,
	}
	if request.Lease.CustodyClass == vmlease.CustodyCustomerSubstrate {
		return result, true, validateSubstrateGuestReplayTx(ctx, tx, request, result)
	}
	if capacityErr := validateManagedRuntimeCapacityReplayTx(ctx, tx, request, result); capacityErr != nil {
		return NativeProvisionAdmissionResult{}, false, capacityErr
	}
	if slotErr := validateManagedRuntimeServerSlotReplayTx(ctx, tx, request, result); slotErr != nil {
		return NativeProvisionAdmissionResult{}, false, slotErr
	}
	return result, true, nil
}

func nativeAdmissionReplayIdentityMatches(
	storedDigest []byte,
	requestDigest [sha256.Size]byte,
	leaseID string,
	operationID string,
	request NativeProvisionAdmissionRequest,
) bool {
	return bytes.Equal(storedDigest, requestDigest[:]) &&
		leaseID == string(request.Lease.ID) && strings.TrimSpace(operationID) != ""
}

func nativeAdmissionReplayCustodyMatches(
	executionAuthority string,
	record OperationRecord,
	leaseID string,
	request NativeProvisionAdmissionRequest,
) bool {
	return executionAuthority == string(ExecutionAuthorityTechstackProviderControl) &&
		record.Command.LeaseRevision == vmleases.NativeAdmissionLeaseRevision &&
		record.Command.RuntimeServerID == request.RuntimeServerID && record.Command.LeaseID == leaseID &&
		record.Command.Operation == providerexecutor.OperationProvision &&
		record.Command.IdempotencyKey == request.IdempotencyKey
}

func insertNativeExecutionAuthorityTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	leaseID string,
	boundAt time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING
	`, tenantID, leaseID, ExecutionAuthorityTechstackProviderControl, boundAt)
	if err != nil {
		return fmt.Errorf("providercontrol: bind native execution authority: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: native execution authority already exists", ErrNativeAdmissionConflict)
	}
	return nil
}

func insertNativeIdempotencyTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	idempotencyKey string,
	requestDigest []byte,
	leaseID string,
	operationID string,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_idempotency_records (
			tenant_id, operation_scope, idempotency_key, request_digest, lease_id, operation_id
		) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT DO NOTHING
	`, tenantID, nativeProvisionOperationScope, idempotencyKey, requestDigest, leaseID, operationID)
	if err != nil {
		return fmt.Errorf("providercontrol: persist native admission idempotency: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: native admission idempotency custody already exists", ErrNativeAdmissionConflict)
	}
	return nil
}

func lockNativeAdmissionTx(ctx context.Context, tx *sql.Tx, tenantID, idempotencyKey string) error {
	_, err := tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		tenantID,
		nativeProvisionOperationScope+":"+idempotencyKey,
	)
	if err != nil {
		return fmt.Errorf("providercontrol: lock native admission idempotency: %w", err)
	}
	return nil
}

func normalizeNativeProvisionAdmission(
	request NativeProvisionAdmissionRequest,
) (NativeProvisionAdmissionRequest, [sha256.Size]byte, string, error) {
	request = normalizeNativeProvisionFields(request)
	if err := validateNativeAdmissionIdentity(request); err != nil {
		return NativeProvisionAdmissionRequest{}, [sha256.Size]byte{}, "", err
	}
	if err := validateNativeAdmissionLease(request); err != nil {
		return NativeProvisionAdmissionRequest{}, [sha256.Size]byte{}, "", err
	}
	canonicalDesired, err := canonicalJSON(request.DesiredSpec)
	if err != nil {
		return NativeProvisionAdmissionRequest{}, [sha256.Size]byte{}, "", fmt.Errorf("%w: desired spec payload must be valid JSON: %v", ErrInvalidRequest, err)
	}
	request.DesiredSpec = canonicalDesired
	serverMetadata, err := canonicalAdmissionMetadata(request.Server.Metadata)
	if err != nil {
		return NativeProvisionAdmissionRequest{}, [sha256.Size]byte{}, "", err
	}
	request.Server.Metadata = serverMetadata
	digestPayload, err := marshalNativeAdmissionDigest(request)
	if err != nil {
		return NativeProvisionAdmissionRequest{}, [sha256.Size]byte{}, "", err
	}
	digest := sha256.Sum256(digestPayload)
	return request, digest, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func normalizeNativeProvisionFields(request NativeProvisionAdmissionRequest) NativeProvisionAdmissionRequest {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.RuntimeSlotKey = strings.ToLower(strings.TrimSpace(request.RuntimeSlotKey))
	request.RuntimeSlotID = strings.TrimSpace(request.RuntimeSlotID)
	if request.RuntimeSlotGeneration == 0 {
		request.RuntimeSlotGeneration = 1
	}
	request.RuntimeServerID = strings.TrimSpace(request.RuntimeServerID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.DesiredSpecRef = strings.TrimSpace(request.DesiredSpecRef)
	request.Lease = cloneAdmissionLease(request.Lease)
	request.Lease.ID = vmlease.LeaseID(strings.TrimSpace(string(request.Lease.ID)))
	request.Lease.Subject.ID = strings.TrimSpace(request.Lease.Subject.ID)
	request.Lease.Subject.OrgID = strings.TrimSpace(request.Lease.Subject.OrgID)
	request.Lease.Resource.ProviderID = strings.ToLower(strings.TrimSpace(request.Lease.Resource.ProviderID))
	request.Server.InstanceID = strings.TrimSpace(request.Server.InstanceID)
	request.Server.StackID = strings.TrimSpace(request.Server.StackID)
	request.Server.Name = strings.TrimSpace(request.Server.Name)
	if request.ValidFor == 0 {
		request.ValidFor = vmleases.DefaultNativeAdmissionValidity
	}
	return request
}

func validateNativeAdmissionIdentity(request NativeProvisionAdmissionRequest) error {
	if incompleteNativeAdmissionIdentity(request) {
		return fmt.Errorf(
			"%w: tenant, runtime slot, lease, runtime server, owner, server name, idempotency key, and desired spec ref are required",
			ErrInvalidRequest,
		)
	}
	if request.Lease.Subject.OrgID != request.TenantID {
		return fmt.Errorf("%w: lease tenant does not match admission tenant", ErrInvalidRequest)
	}
	if !managedRuntimeSlotKeyPattern.MatchString(request.RuntimeSlotKey) {
		return fmt.Errorf("%w: runtime slot key is not canonical", ErrInvalidRequest)
	}
	if request.RuntimeSlotID != DeriveManagedRuntimeSlotID(request.TenantID, request.Server.StackID, request.RuntimeSlotKey) {
		return fmt.Errorf("%w: runtime slot id does not match tenant/stack/slot identity", ErrInvalidRequest)
	}
	return nil
}

func incompleteNativeAdmissionIdentity(request NativeProvisionAdmissionRequest) bool {
	return request.TenantID == "" || request.RuntimeSlotKey == "" || request.RuntimeSlotID == "" || request.RuntimeSlotGeneration == 0 ||
		request.RuntimeServerID == "" || request.OwnerSubjectID == "" || request.Server.StackID == "" ||
		request.IdempotencyKey == "" || len(request.IdempotencyKey) > 256 || request.Lease.ID == "" ||
		request.Server.Name == "" || request.DesiredSpecRef == ""
}

func validateNativeAdmissionLease(request NativeProvisionAdmissionRequest) error {
	if request.ValidFor < time.Minute || request.ValidFor > vmleases.MaxNativeAdmissionValidity {
		return fmt.Errorf("%w: native lease validity is outside the supported range", ErrInvalidRequest)
	}
	if !request.Lease.ValidFrom.IsZero() || !request.Lease.ValidUntil.IsZero() ||
		!request.Lease.RenewedAt.IsZero() || request.Lease.CancelledAt != nil {
		return fmt.Errorf("%w: native lease authority times must come from database time", ErrInvalidRequest)
	}
	if vmleases.ResourceGenerationID(request.Lease) != "" ||
		strings.TrimSpace(request.Lease.Metadata[vmleases.MetadataKeyDecommissionClaimDigest]) != "" {
		return fmt.Errorf("%w: caller-selected generation custody is forbidden", ErrInvalidRequest)
	}
	if request.Lease.DesiredState != vmlease.DesiredStateRunning &&
		request.Lease.DesiredState != vmlease.DesiredStateStopped {
		return fmt.Errorf("%w: native provision desired state must be running or stopped", ErrInvalidRequest)
	}
	return nil
}

func marshalNativeAdmissionDigest(request NativeProvisionAdmissionRequest) ([]byte, error) {
	digestPayload, err := json.Marshal(struct {
		Version               string                `json:"version"`
		OperationScope        string                `json:"operation_scope"`
		TenantID              string                `json:"tenant_id"`
		RuntimeSlotKey        string                `json:"runtime_slot_key"`
		RuntimeSlotID         string                `json:"runtime_slot_id"`
		RuntimeSlotGeneration uint64                `json:"runtime_slot_generation"`
		RuntimeServerID       string                `json:"runtime_server_id"`
		OwnerSubjectID        string                `json:"owner_subject_id"`
		IdempotencyKey        string                `json:"idempotency_key"`
		ValidForNanos         int64                 `json:"valid_for_nanos"`
		Lease                 vmlease.Lease         `json:"lease"`
		Server                NativeAdmissionServer `json:"server"`
		DesiredSpecRef        string                `json:"desired_spec_ref"`
		DesiredSpec           json.RawMessage       `json:"desired_spec"`
	}{
		Version: nativeAdmissionDigestVersion, OperationScope: nativeProvisionOperationScope,
		TenantID: request.TenantID, RuntimeSlotKey: request.RuntimeSlotKey, RuntimeSlotID: request.RuntimeSlotID,
		RuntimeSlotGeneration: request.RuntimeSlotGeneration,
		RuntimeServerID:       request.RuntimeServerID,
		OwnerSubjectID:        request.OwnerSubjectID, IdempotencyKey: request.IdempotencyKey,
		ValidForNanos: int64(request.ValidFor),
		Lease:         request.Lease, Server: request.Server,
		DesiredSpecRef: request.DesiredSpecRef, DesiredSpec: request.DesiredSpec,
	})
	if err != nil {
		return nil, fmt.Errorf("providercontrol: encode native admission digest: %w", err)
	}
	return digestPayload, nil
}

func canonicalAdmissionMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: server metadata must be valid JSON", ErrInvalidRequest)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: server metadata must be valid JSON", ErrInvalidRequest)
	}
	var normalized map[string]any
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return nil, fmt.Errorf("%w: server metadata must be a JSON object", ErrInvalidRequest)
	}
	return normalized, nil
}

func cloneAdmissionLease(lease vmlease.Lease) vmlease.Lease {
	cloned := lease
	cloned.Metadata = maps.Clone(lease.Metadata)
	if lease.CancelledAt != nil {
		cancelledAt := *lease.CancelledAt
		cloned.CancelledAt = &cancelledAt
	}
	return cloned
}

func nativeServerDesiredState(state vmlease.DesiredState) (serverregistry.DesiredState, error) {
	switch state {
	case vmlease.DesiredStateRunning:
		return serverregistry.DesiredRunning, nil
	case vmlease.DesiredStateStopped:
		return serverregistry.DesiredStopped, nil
	default:
		return "", fmt.Errorf("%w: unsupported native desired state %q", ErrInvalidRequest, state)
	}
}

func nilProviderControlInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sameComparableDependency(left, right any) bool {
	if nilProviderControlInterface(left) || nilProviderControlInterface(right) || reflect.TypeOf(left) != reflect.TypeOf(right) {
		return false
	}
	typeOf := reflect.TypeOf(left)
	return typeOf.Comparable() && reflect.ValueOf(left).Interface() == reflect.ValueOf(right).Interface()
}
