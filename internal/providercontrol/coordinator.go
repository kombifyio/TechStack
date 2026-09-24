package providercontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/google/uuid"
)

// Structured log field names shared by the provider-adapter execution record.
const (
	logFieldOperation   = "operation"
	logFieldOperationID = "operation_id"
	logFieldTenantID    = "tenant_id"
	logFieldLeaseID     = "lease_id"
)

// logAdapterExecution records every provider-adapter execution at the one
// boundary all of them cross. Without it a stalled operation is indivisible
// from an idle one: the adapters log only their own failures, so a result that
// is rejected, or accepted but carries no progress, leaves no trace anywhere.
//
// Diagnosed in production on 2026-07-26: two decommissions took a
// side-effecting claim every ~15s for hours and released it again without a
// receipt, and no query over any adapter, provider, or operation term returned
// a single line.
func logAdapterExecution(
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	result providerexecutor.ExecutionResult,
	outcome string,
	detail error,
) {
	logger.Get().Info(
		"provider_adapter_execution",
		adapterExecutionAttrs(command, head, result, outcome, detail)...,
	)
}

const terminalFailureObservationEvent = "provider_operation_terminal_failure"

type TerminalFailureObserver func(providerexecutor.Command, providerexecutor.Receipt, providerexecutor.Receipt)

func logTerminalFailureObservation(command providerexecutor.Command, previous, next providerexecutor.Receipt) {
	logger.Get().Info(
		terminalFailureObservationEvent,
		terminalFailureObservationAttrs(command, previous, next)...,
	)
}

func terminalFailureObservationAttrs(command providerexecutor.Command, previous, next providerexecutor.Receipt) []any {
	reasonCode := ""
	retryable := false
	if next.Reason != nil {
		reasonCode = next.Reason.Code
		retryable = next.Reason.Retryable
	}
	return []any{
		logFieldOperation, string(command.Operation),
		logFieldOperationID, command.OperationID,
		logFieldLeaseID, command.LeaseID,
		"provider_id", command.ProviderID,
		"resource_generation_id", command.ResourceGenerationID,
		"correlation_id", command.OperationID,
		"phase", string(next.Phase),
		"previous_phase", string(previous.Phase),
		"reason_code", reasonCode,
		"retryable", retryable,
		"receipt_sequence", next.Sequence,
	}
}

func shouldObserveTerminalProvisionFailure(command providerexecutor.Command, previous, next providerexecutor.Receipt) bool {
	return command.Operation == providerexecutor.OperationProvision &&
		!isTerminal(previous) &&
		next.Status == providerexecutor.StatusFailed &&
		isTerminal(next)
}

// adapterExecutionAttrs is separated from emission so the exact field set is
// assertable without capturing process stdout.
func adapterExecutionAttrs(
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	result providerexecutor.ExecutionResult,
	outcome string,
	detail error,
) []any {
	reasonCode := ""
	retryable := false
	if result.Reason != nil {
		reasonCode = result.Reason.Code
		retryable = result.Reason.Retryable
	}
	message := ""
	if detail != nil {
		message = detail.Error()
	}
	return []any{
		logFieldOperation, string(command.Operation),
		logFieldOperationID, command.OperationID,
		logFieldTenantID, command.TenantID,
		logFieldLeaseID, command.LeaseID,
		"phase_before", string(head.Phase),
		"phase_after", string(result.Phase),
		"result_status", string(result.Status),
		"reason_code", reasonCode,
		"retryable", retryable,
		"resources", len(result.Resources),
		"outcome", outcome,
		"detail", message,
	}
}

// CoordinatorConfig supplies TechStack-owned adapter, profile, ledger, and
// evidence-verification dependencies.
type CoordinatorConfig struct {
	Registry                *Registry
	Profiles                ExecutionProfileResolver
	Ledger                  Ledger
	ActivationGate          MutationActivationGate
	EvidenceVerifier        providerexecutor.EvidenceVerifier
	Now                     func() time.Time
	ClaimOwner              string
	ClaimTTL                time.Duration
	TerminalFailureObserver TerminalFailureObserver
}

// Coordinator is TechStack's provider-operation authority. Start persists the
// immutable command and requested receipt before any adapter runs. Advance
// moves exactly one ledger step and exposes intermediate cleanup state. Every
// adapter-bearing advance holds a durable claim for the exact current head.
type Coordinator struct {
	registry                *Registry
	profiles                ExecutionProfileResolver
	ledger                  Ledger
	activation              MutationActivationGate
	verifier                providerexecutor.EvidenceVerifier
	now                     func() time.Time
	claimOwner              string
	claimTTL                time.Duration
	terminalFailureObserver TerminalFailureObserver
}

type preparedStart struct {
	request           StartRequest
	desiredSpec       *DesiredSpecRevision
	profileSnapshot   ExecutionProfileSnapshot
	provisionDispatch ProvisionDispatchMode
	command           providerexecutor.Command
	initial           providerexecutor.Receipt
}

// NewCoordinator creates a provider-operation authority without requiring any
// particular adapter to be registered.
func NewCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	if cfg.Registry == nil || cfg.Profiles == nil || cfg.Ledger == nil {
		return nil, fmt.Errorf("%w: registry, profile resolver, and ledger are required", ErrInvalidRequest)
	}
	if nilMutationActivationGate(cfg.ActivationGate) {
		cfg.ActivationGate = ProductionBlockedGate{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	claimTTL, err := normalizeClaimTTL(cfg.ClaimTTL)
	if err != nil {
		return nil, err
	}
	claimOwner := strings.TrimSpace(cfg.ClaimOwner)
	if claimOwner == "" {
		claimOwner, err = executionClaimToken()
		if err != nil {
			return nil, err
		}
		claimOwner = "coordinator-" + claimOwner
	}
	terminalFailureObserver := cfg.TerminalFailureObserver
	if terminalFailureObserver == nil {
		terminalFailureObserver = func(command providerexecutor.Command, previous, next providerexecutor.Receipt) {
			logTerminalFailureObservation(command, previous, next)
		}
	}
	return &Coordinator{
		registry:                cfg.Registry,
		profiles:                cfg.Profiles,
		ledger:                  cfg.Ledger,
		activation:              cfg.ActivationGate,
		verifier:                cfg.EvidenceVerifier,
		now:                     cfg.Now,
		claimOwner:              claimOwner,
		claimTTL:                claimTTL,
		terminalFailureObserver: terminalFailureObserver,
	}, nil
}

// Start resolves the execution profile, seals the command, and atomically
// persists its initial requested receipt. It never invokes an adapter.
//
//nolint:gocyclo // Keep admission, replay, and atomic ledger validation in one fail-closed path.
func (c *Coordinator) Start(ctx context.Context, req StartRequest) (OperationRecord, bool, error) {
	if c == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: coordinator is nil", ErrInvalidRequest)
	}
	normalizedRequest, requestedAt, err := normalizeStartRequest(req)
	if err != nil {
		return OperationRecord{}, false, err
	}
	req = normalizedRequest
	if activationErr := c.activation.Require(ctx); activationErr != nil {
		return OperationRecord{}, false, activationErr
	}
	desiredSpec, err := normalizeDesiredSpec(req, requestedAt)
	if err != nil {
		return OperationRecord{}, false, err
	}
	operationID := providerexecutor.ComputeOperationID(
		req.TenantID,
		req.LeaseID,
		req.ResourceGenerationID,
		req.Operation,
		req.IdempotencyKey,
	)
	if existing, loadErr := c.ledger.LoadOperation(ctx, req.TenantID, operationID); loadErr == nil {
		return c.validateStartReplay(ctx, existing, req, desiredSpec)
	} else if !errors.Is(loadErr, ErrOperationNotFound) {
		return OperationRecord{}, false, loadErr
	}
	prepared, err := c.prepareNormalizedStart(
		ctx,
		req,
		requestedAt,
		desiredSpec,
		c.profiles.ResolveExecutionProfile,
	)
	if err != nil {
		return OperationRecord{}, false, err
	}
	record, created, err := c.ledger.BeginOperation(
		ctx,
		ExecutionAuthorityTechstackProviderControl,
		prepared.profileSnapshot,
		prepared.provisionDispatch,
		prepared.command,
		prepared.initial,
		prepared.desiredSpec,
	)
	if err != nil {
		// Another replica may have won creation after the preflight lookup.
		if existing, loadErr := c.ledger.LoadOperation(ctx, req.TenantID, operationID); loadErr == nil {
			return c.validateStartReplay(ctx, existing, req, desiredSpec)
		}
		return OperationRecord{}, false, err
	}
	if !created {
		return c.validateStartReplay(ctx, record, req, desiredSpec)
	}
	if record.ExecutionProfile != prepared.profileSnapshot {
		return OperationRecord{}, false, fmt.Errorf("%w: execution profile changed while creating operation", ErrLedgerConflict)
	}
	if record.ProvisionDispatch != prepared.provisionDispatch {
		return OperationRecord{}, false, fmt.Errorf("%w: provision dispatch mode changed while creating operation", ErrLedgerConflict)
	}
	if replayErr := providerexecutor.ValidateReplay(prepared.command, record.Command); replayErr != nil {
		return OperationRecord{}, false, replayErr
	}
	if validationErr := validateOperationRecord(ctx, record, c.verifier); validationErr != nil {
		return OperationRecord{}, false, validationErr
	}
	return record, created, nil
}

type executionProfileResolveFunc func(context.Context, ProfileRequest) (ExecutionProfile, error)

func (c *Coordinator) prepareStartTx(ctx context.Context, tx *sql.Tx, req StartRequest) (preparedStart, error) {
	if c == nil || tx == nil {
		return preparedStart{}, fmt.Errorf("%w: coordinator and transaction are required", ErrInvalidRequest)
	}
	resolver, ok := c.profiles.(transactionalExecutionProfileResolver)
	if !ok || nilProviderControlInterface(resolver) {
		return preparedStart{}, fmt.Errorf("%w: execution profile resolver is not transaction-aware", ErrProfileUnavailable)
	}
	normalized, requestedAt, err := normalizeStartRequest(req)
	if err != nil {
		return preparedStart{}, err
	}
	desiredSpec, err := normalizeDesiredSpec(normalized, requestedAt)
	if err != nil {
		return preparedStart{}, err
	}
	return c.prepareNormalizedStart(
		ctx,
		normalized,
		requestedAt,
		desiredSpec,
		func(ctx context.Context, request ProfileRequest) (ExecutionProfile, error) {
			return resolver.ResolveExecutionProfileTx(ctx, tx, request)
		},
	)
}

func (c *Coordinator) prepareNormalizedStart(
	ctx context.Context,
	req StartRequest,
	requestedAt time.Time,
	desiredSpec *DesiredSpecRevision,
	resolve executionProfileResolveFunc,
) (preparedStart, error) {
	profile, err := resolve(ctx, ProfileRequest{
		TenantID: req.TenantID, LeaseID: req.LeaseID,
		LeaseRevision: req.LeaseRevision, RuntimeServerID: req.RuntimeServerID,
		ResourceGenerationID: req.ResourceGenerationID, Operation: req.Operation,
	})
	if err != nil {
		return preparedStart{}, fmt.Errorf("providercontrol: resolve profile: %w", err)
	}
	profile, profileSnapshot, err := normalizeExecutionProfile(profile)
	if err != nil {
		return preparedStart{}, fmt.Errorf("providercontrol: resolve profile: %w", err)
	}
	// Adapter admission precedes every durable operation write. Missing or
	// uncertified adapters must roll the surrounding native admission back.
	adapter, resolveErr := c.registry.resolveAdapter(profile.AdapterID)
	if resolveErr != nil {
		return preparedStart{}, resolveErr
	}
	if dispatchErr := adapter.validateProvisionDispatchPin(
		req.Operation, profile.ProvisionDispatchMode, profile.AdapterManifestHash,
	); dispatchErr != nil {
		return preparedStart{}, dispatchErr
	}
	provisionDispatch := profile.ProvisionDispatchMode
	desiredRef, desiredHash := "", ""
	if desiredSpec != nil {
		desiredRef, desiredHash = desiredSpec.Ref, desiredSpec.Digest
	}
	command, err := providerexecutor.SealCommand(providerexecutor.Command{
		Operation:              req.Operation,
		TenantID:               req.TenantID,
		LeaseID:                req.LeaseID,
		LeaseRevision:          req.LeaseRevision,
		RuntimeServerID:        req.RuntimeServerID,
		ResourceGenerationID:   req.ResourceGenerationID,
		IdempotencyKey:         req.IdempotencyKey,
		ProviderID:             profile.ProviderID,
		AdapterID:              profile.AdapterID,
		CustodyRef:             profile.CustodyRef,
		CustodyHash:            profile.CustodyHash,
		ConnectionRef:          profile.ConnectionRef,
		ConnectionHash:         profile.ConnectionHash,
		CapabilitySnapshotHash: profile.CapabilitySnapshotHash,
		ExecutionProfileHash:   profile.ExecutionProfileHash,
		LedgerRevision:         req.LedgerRevision,
		DesiredSpecRef:         desiredRef,
		DesiredSpecHash:        desiredHash,
		Targets:                req.Targets,
		RequestedAt:            requestedAt,
	})
	if err != nil {
		return preparedStart{}, err
	}
	initial, err := providerexecutor.InitialReceipt(command, requestedAt)
	if err != nil {
		return preparedStart{}, err
	}
	return preparedStart{
		request: req, desiredSpec: desiredSpec, profileSnapshot: profileSnapshot,
		provisionDispatch: provisionDispatch, command: command, initial: initial,
	}, nil
}

func (c *Coordinator) validateStartReplay(
	ctx context.Context,
	record OperationRecord,
	req StartRequest,
	desiredSpec *DesiredSpecRevision,
) (OperationRecord, bool, error) {
	if err := validateOperationRecord(ctx, record, c.verifier); err != nil {
		return OperationRecord{}, false, err
	}
	adapter, err := c.registry.resolveAdapter(record.Command.AdapterID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if record.ProvisionDispatch != record.ExecutionProfile.ProvisionDispatchMode {
		return OperationRecord{}, false, fmt.Errorf("%w: operation and execution-profile dispatch pins differ", ErrLedgerConflict)
	}
	if err := adapter.validateProvisionContinuationPin(
		req.Operation, record.ProvisionDispatch, record.ExecutionProfile.AdapterManifestHash,
		record.Head.Phase,
	); err != nil {
		return OperationRecord{}, false, err
	}
	desiredRef, desiredHash := "", ""
	if desiredSpec != nil {
		desiredRef, desiredHash = desiredSpec.Ref, desiredSpec.Digest
	}
	replay, err := providerexecutor.SealCommand(providerexecutor.Command{
		Operation:              req.Operation,
		TenantID:               req.TenantID,
		LeaseID:                req.LeaseID,
		LeaseRevision:          req.LeaseRevision,
		RuntimeServerID:        req.RuntimeServerID,
		ResourceGenerationID:   req.ResourceGenerationID,
		IdempotencyKey:         req.IdempotencyKey,
		ProviderID:             record.Command.ProviderID,
		AdapterID:              record.Command.AdapterID,
		CustodyRef:             record.Command.CustodyRef,
		CustodyHash:            record.Command.CustodyHash,
		ConnectionRef:          record.Command.ConnectionRef,
		ConnectionHash:         record.Command.ConnectionHash,
		CapabilitySnapshotHash: record.Command.CapabilitySnapshotHash,
		ExecutionProfileHash:   record.Command.ExecutionProfileHash,
		LedgerRevision:         req.LedgerRevision,
		DesiredSpecRef:         desiredRef,
		DesiredSpecHash:        desiredHash,
		Targets:                req.Targets,
		RequestedAt:            req.RequestedAt,
	})
	if err != nil {
		return OperationRecord{}, false, err
	}
	if err := providerexecutor.ValidateReplay(record.Command, replay); err != nil {
		return OperationRecord{}, false, err
	}
	return record, false, nil
}

func validateOperationRecord(ctx context.Context, record OperationRecord, verifier providerexecutor.EvidenceVerifier) error {
	if err := requireExecutionAuthority(record, ExecutionAuthorityTechstackProviderControl); err != nil {
		return err
	}
	if err := record.Command.Validate(); err != nil {
		return err
	}
	if err := validateProvisionDispatchMode(record.Command.Operation, record.ProvisionDispatch); err != nil {
		return err
	}
	if err := validateExecutionProfileSnapshot(record.ExecutionProfile, record.Command); err != nil {
		return err
	}
	if record.ProvisionDispatch != record.ExecutionProfile.ProvisionDispatchMode {
		return fmt.Errorf("%w: operation and execution-profile dispatch pins differ", ErrLedgerConflict)
	}
	if err := record.Head.ValidateFor(ctx, record.Command, verifier); err != nil {
		return fmt.Errorf("providercontrol: invalid ledger head: %w", err)
	}
	return nil
}

// Advance moves one operation one legal receipt step. The requested->accepted
// transition is coordinator-owned; all later results come from the selected
// adapter and are sealed against the current ledger head before persistence.
func (c *Coordinator) Advance(ctx context.Context, tenantID, operationID string) (OperationRecord, bool, error) {
	if c == nil {
		return OperationRecord{}, false, fmt.Errorf("%w: coordinator is nil", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	if tenantID == "" || operationID == "" {
		return OperationRecord{}, false, fmt.Errorf("%w: tenant and operation id are required", ErrInvalidRequest)
	}
	record, err := c.ledger.LoadOperation(ctx, tenantID, operationID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if validationErr := validateOperationRecord(ctx, record, c.verifier); validationErr != nil {
		return OperationRecord{}, false, validationErr
	}
	// Terminal is checked before AutomationComplete: deriveOperationAutomationState
	// reports Complete for every terminal head, so an earlier Complete return
	// would make the teardown below unreachable.
	if isTerminal(record.Head) {
		// A terminally failed provision still holds its managed-runtime capacity
		// reservation. The reservation is released only by a decommission (which
		// needs a provisioned server) or by resource-free teardown, so a provision
		// that failed before binding any resource had neither path and leaked its
		// slot permanently -- after `limit` such failures the owner can never
		// create another managed server.
		//
		// Offering a terminal head to the teardown finalizer is safe: its
		// transaction commits only when the database proves no provider resource
		// can exist for this generation (dispatch custody never existed, or
		// Operator Reconcile verified an exact zero-candidate observation). It
		// refuses otherwise, so a real partial create is never released.
		//
		// Deliberately before the activation gate: reclaiming a slot for work the
		// provider never received must not depend on provider activation.
		return c.tryFinalizeResourceFreeTeardown(ctx, record)
	}
	if record.AutomationState == OperationAutomationComplete {
		return record, false, nil
	}
	if err := c.activation.Require(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	finalized, changed, finalizeErr := c.tryFinalizeResourceFreeTeardown(ctx, record)
	if finalizeErr != nil {
		return OperationRecord{}, false, finalizeErr
	}
	if changed {
		return finalized, true, nil
	}
	if record.AutomationState == OperationAutomationManualReconcileRequired {
		return record, false, ErrProvisionManualReconcile
	}

	if record.Head.Phase == providerexecutor.PhaseRequested {
		result := providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending,
			Phase:  providerexecutor.PhaseAccepted,
		}
		next, err := providerexecutor.AssembleReceipt(
			ctx,
			providerexecutor.ExecutionRequest{Command: record.Command, Previous: record.Head},
			result, c.now(), c.verifier,
		)
		if err != nil {
			return OperationRecord{}, false, err
		}
		if err := c.ledger.AppendReceipt(ctx, record.Command, record.Head, next); err != nil {
			return OperationRecord{}, false, err
		}
		return OperationRecord{
			ExecutionAuthority:      record.ExecutionAuthority,
			ExecutionProfile:        record.ExecutionProfile,
			ProvisionDispatch:       record.ProvisionDispatch,
			AutomationState:         OperationAutomationRunnable,
			RuntimeServerGeneration: record.RuntimeServerGeneration,
			Command:                 record.Command,
			Head:                    next,
		}, true, nil
	}

	return c.advanceClaimed(ctx, record)
}

func (c *Coordinator) tryFinalizeResourceFreeTeardown(
	ctx context.Context,
	record OperationRecord,
) (OperationRecord, bool, error) {
	finalizer, ok := c.ledger.(ResourceFreeTeardownFinalizer)
	if !ok || nilProviderControlInterface(finalizer) {
		return record, false, nil
	}
	finalized, changed, err := finalizer.FinalizeResourceFreeTeardown(ctx, record.Command, record.Head)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if changed {
		return finalized, true, nil
	}
	return record, false, nil
}

func (c *Coordinator) advanceClaimed(ctx context.Context, record OperationRecord) (OperationRecord, bool, error) {
	access, accessErr := classifyExecutionClaimAccess(record.Command.Operation, record.Head.Phase)
	if accessErr != nil {
		return OperationRecord{}, false, accessErr
	}
	adapter, err := c.registry.resolveAdapter(record.Command.AdapterID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if record.ProvisionDispatch != record.ExecutionProfile.ProvisionDispatchMode {
		return OperationRecord{}, false, fmt.Errorf("%w: operation and execution-profile dispatch pins differ", ErrLedgerConflict)
	}
	if err := adapter.validateProvisionContinuationPin(
		record.Command.Operation, record.ProvisionDispatch, record.ExecutionProfile.AdapterManifestHash,
		record.Head.Phase,
	); err != nil {
		return OperationRecord{}, false, err
	}

	if adapter.crashReadOnly != nil || adapter.crashMutation != nil {
		return c.advanceCrashRecoverable(ctx, record, access, adapter)
	}
	if adapter.atMostOnce == nil {
		return OperationRecord{}, false, ErrAdapterNotRegistered
	}

	switch record.Command.Operation {
	case providerexecutor.OperationProvision:
		return c.advanceAtMostOnceProvision(ctx, record, access, adapter.atMostOnce)
	case providerexecutor.OperationPlan, providerexecutor.OperationObserve:
		return c.advanceWithClaim(ctx, record, access, nil,
			func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
				return adapter.atMostOnce.ObserveReadOnly(executionCtx, invocation)
			}, nil, nil)
	case providerexecutor.OperationDecommission:
		switch record.Head.Phase {
		case providerexecutor.PhaseAccepted:
			return c.advanceWithClaim(ctx, record, access, nil,
				func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
					return adapter.atMostOnce.DecommissionExactHandles(executionCtx, invocation)
				}, nil, nil)
		case providerexecutor.PhaseDeleteAccepted, providerexecutor.PhaseAbsencePending:
			return c.advanceWithClaim(ctx, record, access, nil,
				func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
					return adapter.atMostOnce.PollDecommissionAbsence(executionCtx, invocation)
				}, validateDecommissionPollIdentity, nil)
		default:
			return OperationRecord{}, false, fmt.Errorf("%w: unsupported decommission head %q", ErrAdapterSafety, record.Head.Phase)
		}
	case providerexecutor.OperationReconcile:
		return c.advanceExactHandleReconcile(ctx, record, access, adapter.reconcile)
	default:
		return OperationRecord{}, false, fmt.Errorf("%w: operation %q is not admitted for an at-most-once provision adapter", ErrAdapterSafety, record.Command.Operation)
	}
}

func (c *Coordinator) advanceAtMostOnceProvision(
	ctx context.Context,
	record OperationRecord,
	access ExecutionClaimAccess,
	executor AtMostOnceProvisionExecutor,
) (OperationRecord, bool, error) {
	switch record.Head.Phase {
	case providerexecutor.PhaseAccepted:
		invocation, err := newAdapterInvocation(providerexecutor.ExecutionRequest{
			Command: record.Command, Previous: record.Head,
		})
		if err != nil {
			return OperationRecord{}, false, err
		}
		prepared, err := executor.PrepareProvision(ctx, invocation)
		if err != nil {
			return OperationRecord{}, false, fmt.Errorf("providercontrol: prepare provision request: %w", err)
		}
		binding, err := validatePreparedProvisionRequest(
			prepared, invocation, record.ExecutionProfile.AdapterManifestHash,
		)
		if err != nil {
			return OperationRecord{}, false, err
		}
		acquirePrepared := func(
			acquireCtx context.Context,
			command providerexecutor.Command,
			head providerexecutor.Receipt,
			owner string,
			token string,
			ttl time.Duration,
		) (ProvisionDispatchGrant, error) {
			return c.ledger.AcquireProvisionDispatchClaim(
				acquireCtx, command, head, binding, owner, token, ttl,
			)
		}
		return c.advanceWithClaim(ctx, record, access, acquirePrepared,
			func(executionCtx context.Context, _ AdapterInvocation, permit *ExecutePermit) providerexecutor.ExecutionResult {
				return executor.DispatchPreparedProvision(executionCtx, prepared, *permit)
			}, validateAtMostOnceProvisionDispatchResult, &binding)
	case providerexecutor.PhaseResourcesBound:
		return c.advanceWithClaim(ctx, record, access, nil,
			func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
				return executor.PollProvisionPresence(executionCtx, invocation)
			}, validateProvisionPollIdentity, nil)
	default:
		return OperationRecord{}, false, fmt.Errorf(
			"%w: unsupported at-most-once provision head %q", ErrAdapterSafety, record.Head.Phase,
		)
	}
}

// advanceExactHandleReconcile runs the one side-effecting reconcile step under
// the accepted-head claim, then observes convergence read-only. Neither step
// may change the exact target graph the ledger admitted at start.
func (c *Coordinator) advanceExactHandleReconcile(
	ctx context.Context,
	record OperationRecord,
	access ExecutionClaimAccess,
	executor ExactHandleReconcileExecutor,
) (OperationRecord, bool, error) {
	if executor == nil {
		return OperationRecord{}, false, fmt.Errorf(
			"%w: operation %q is not admitted for an at-most-once provision adapter", ErrAdapterSafety, record.Command.Operation,
		)
	}
	switch record.Head.Phase {
	case providerexecutor.PhaseAccepted:
		return c.advanceWithClaim(ctx, record, access, nil,
			func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
				return executor.ReconcileExactHandles(executionCtx, invocation)
			}, validateReconcileDispatchResult, nil)
	case providerexecutor.PhaseResourcesBound:
		return c.advanceWithClaim(ctx, record, access, nil,
			func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
				return executor.PollReconcileConvergence(executionCtx, invocation)
			}, validateReconcilePollIdentity, nil)
	default:
		return OperationRecord{}, false, fmt.Errorf("%w: unsupported reconcile head %q", ErrAdapterSafety, record.Head.Phase)
	}
}

func (c *Coordinator) advanceCrashRecoverable(
	ctx context.Context,
	record OperationRecord,
	access ExecutionClaimAccess,
	adapter registeredAdapter,
) (OperationRecord, bool, error) {
	switch access {
	case ExecutionClaimReadOnly:
		return c.advanceCrashRecoverableReadOnly(ctx, record, adapter.crashReadOnly)
	case ExecutionClaimSideEffecting:
		return c.advanceCrashRecoverableMutation(ctx, record, adapter.crashMutation)
	default:
		return OperationRecord{}, false, fmt.Errorf(
			"%w: unsupported crash-recoverable claim access %q", ErrAdapterSafety, access,
		)
	}
}

func (c *Coordinator) advanceCrashRecoverableReadOnly(
	ctx context.Context,
	record OperationRecord,
	executor CrashRecoverableReadOnlyExecutor,
) (OperationRecord, bool, error) {
	if executor == nil {
		return OperationRecord{}, false, ErrAdapterNotRegistered
	}
	return c.advanceWithClaim(ctx, record, ExecutionClaimReadOnly, nil,
		func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
			return executor.ExecuteCrashRecoverableReadOnly(executionCtx, invocation)
		}, nil, nil)
}

func (c *Coordinator) advanceCrashRecoverableMutation(
	ctx context.Context,
	record OperationRecord,
	executor CrashRecoverableMutationExecutor,
) (OperationRecord, bool, error) {
	if executor == nil {
		return OperationRecord{}, false, ErrAdapterNotRegistered
	}
	return c.advanceWithClaim(ctx, record, ExecutionClaimSideEffecting, nil,
		func(executionCtx context.Context, invocation AdapterInvocation, _ *ExecutePermit) providerexecutor.ExecutionResult {
			return executor.ExecuteCrashRecoverableMutation(executionCtx, invocation)
		}, nil, nil)
}

type provisionDispatchClaimAcquirer func(
	context.Context,
	providerexecutor.Command,
	providerexecutor.Receipt,
	string,
	string,
	time.Duration,
) (ProvisionDispatchGrant, error)

type claimedAdapterCall func(context.Context, AdapterInvocation, *ExecutePermit) providerexecutor.ExecutionResult

type adapterResultValidator func(providerexecutor.Receipt, providerexecutor.ExecutionResult) error

func (c *Coordinator) advanceWithClaim(
	ctx context.Context,
	record OperationRecord,
	access ExecutionClaimAccess,
	dispatchAcquire provisionDispatchClaimAcquirer,
	execute claimedAdapterCall,
	validateResult adapterResultValidator,
	preparedBinding *PreparedProvisionBinding,
) (OperationRecord, bool, error) {
	token, err := executionClaimToken()
	if err != nil {
		return OperationRecord{}, false, err
	}
	request := providerexecutor.ExecutionRequest{Command: record.Command, Previous: record.Head}
	invocation, err := newAdapterInvocation(request)
	if err != nil {
		return OperationRecord{}, false, err
	}
	acquisition := c.acquireAdapterExecutionClaim(
		ctx, record, access, dispatchAcquire, preparedBinding, token,
	)
	if acquisition.done {
		return acquisition.record, false, acquisition.err
	}
	claim, permit := acquisition.claim, acquisition.permit

	executionCtx, heartbeat := startClaimHeartbeat(ctx, c.ledger, claim, c.claimTTL)
	consumed := false
	defer func() {
		latest, _ := heartbeat.stop()
		if consumed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.ledger.ReleaseExecutionClaim(cleanupCtx, latest)
	}()
	result := execute(executionCtx, invocation, permit)
	if _, lost := heartbeat.snapshot(); lost {
		return OperationRecord{}, false, ErrExecutionClaimLost
	}
	if custodyErr := validateAdapterResultCustody(request.Command, result); custodyErr != nil {
		logAdapterExecution(record.Command, record.Head, result, "rejected_custody", custodyErr)
		return OperationRecord{}, false, custodyErr
	}
	if validateResult != nil {
		if validationErr := validateResult(record.Head, result); validationErr != nil {
			logAdapterExecution(record.Command, record.Head, result, "rejected_identity", validationErr)
			return OperationRecord{}, false, validationErr
		}
	}
	next, err := providerexecutor.AssembleReceipt(
		executionCtx,
		request,
		result, c.now(), c.verifier,
	)
	if err != nil {
		logAdapterExecution(record.Command, record.Head, result, "receipt_assembly_failed", err)
		return OperationRecord{}, false, err
	}
	if isNoProgressPendingReceipt(record.Head, next) {
		// Preserve the head. Crash-recoverable adapters may recover by their
		// invocation key; an AMO dispatch remains guarded and becomes manual.
		//
		// This branch is the silent-stall signature: the claim is taken and
		// released, no receipt is written, and no error is returned, so the
		// caller retries forever. It must always announce itself.
		logAdapterExecution(record.Command, record.Head, result, "no_progress", nil)
		if dispatchAcquire != nil {
			record.AutomationState = OperationAutomationManualReconcileRequired
			record.AutomationReasonCode = providerexecutor.ReasonCodeProviderPartialCreate
		}
		return record, false, nil
	}
	logAdapterExecution(record.Command, record.Head, result, "advanced", nil)
	_, lost, err := heartbeat.finalize(func(current ExecutionClaim) error {
		return c.ledger.AppendClaimedReceipt(ctx, record.Command, record.Head, next, current)
	})
	if lost {
		return OperationRecord{}, false, ErrExecutionClaimLost
	}
	if err != nil {
		return OperationRecord{}, false, err
	}
	consumed = true
	if shouldObserveTerminalProvisionFailure(record.Command, record.Head, next) {
		c.terminalFailureObserver(record.Command, record.Head, next)
	}
	return OperationRecord{
		ExecutionAuthority:      record.ExecutionAuthority,
		ExecutionProfile:        record.ExecutionProfile,
		ProvisionDispatch:       record.ProvisionDispatch,
		AutomationState:         deriveOperationAutomationState(OperationRecord{Command: record.Command, Head: next, ProvisionDispatch: record.ProvisionDispatch}, false),
		AutomationReasonCode:    "",
		RuntimeServerGeneration: record.RuntimeServerGeneration,
		Command:                 record.Command,
		Head:                    next,
	}, true, nil
}

type adapterExecutionClaimAcquisition struct {
	claim  ExecutionClaim
	permit *ExecutePermit
	record OperationRecord
	done   bool
	err    error
}

func (c *Coordinator) acquireAdapterExecutionClaim(
	ctx context.Context,
	record OperationRecord,
	access ExecutionClaimAccess,
	dispatchAcquire provisionDispatchClaimAcquirer,
	preparedBinding *PreparedProvisionBinding,
	token string,
) adapterExecutionClaimAcquisition {
	if dispatchAcquire == nil {
		claim, err := c.ledger.AcquireExecutionClaim(
			ctx, record.Command, record.Head, access, c.claimOwner, token, c.claimTTL,
		)
		if err == nil {
			return adapterExecutionClaimAcquisition{claim: claim}
		}
		replay, _, replayErr := c.reconcileExecutionClaimAcquisitionError(ctx, record, err)
		return adapterExecutionClaimAcquisition{record: replay, done: true, err: replayErr}
	}
	if access != ExecutionClaimSideEffecting || preparedBinding == nil {
		return adapterExecutionClaimAcquisition{done: true, err: ErrDispatchPermitNeeded}
	}
	grant, err := dispatchAcquire(ctx, record.Command, record.Head, c.claimOwner, token, c.claimTTL)
	if err != nil {
		return adapterExecutionClaimAcquisition{done: true, err: err}
	}
	if !validExecutePermit(grant.Permit, grant.Claim, *preparedBinding) {
		return adapterExecutionClaimAcquisition{done: true, err: ErrDispatchPermitNeeded}
	}
	return adapterExecutionClaimAcquisition{claim: grant.Claim, permit: &grant.Permit}
}

func (c *Coordinator) reconcileExecutionClaimAcquisitionError(
	ctx context.Context,
	record OperationRecord,
	claimErr error,
) (OperationRecord, bool, error) {
	if !errors.Is(claimErr, ErrExecutionClaimHeld) && !errors.Is(claimErr, ErrLedgerConflict) {
		return OperationRecord{}, false, claimErr
	}
	latest, err := c.ledger.LoadOperation(ctx, record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		return OperationRecord{}, false, claimErr
	}
	// The adapter never ran, so no result exists to describe. This still has to
	// speak: contention that never clears is the same forever-retry from the
	// caller's side as a no-progress result.
	logAdapterExecution(
		record.Command, record.Head,
		providerexecutor.ExecutionResult{Status: latest.Head.Status, Phase: latest.Head.Phase},
		"claim_contended", claimErr,
	)
	return latest, false, nil
}

func isNoProgressPendingReceipt(previous, next providerexecutor.Receipt) bool {
	return previous.Status == providerexecutor.StatusPending &&
		next.Status == providerexecutor.StatusPending &&
		previous.Phase == next.Phase &&
		reflect.DeepEqual(previous.Resources, next.Resources)
}

func validateAdapterResultCustody(command providerexecutor.Command, result providerexecutor.ExecutionResult) error {
	if result.Status == providerexecutor.StatusDenied || result.Phase == providerexecutor.PhaseDenied {
		return fmt.Errorf("%w: adapter denial is not an execution result", ErrInvalidRequest)
	}
	if result.Status != providerexecutor.StatusFailed || len(result.Resources) == 0 {
		return nil
	}
	if command.Operation == providerexecutor.OperationProvision && (result.Reason == nil ||
		(result.Reason.Code != providerexecutor.ReasonCodeProviderPartialCreate &&
			result.Reason.Code != providerexecutor.ReasonCodeProviderCleanupRequired)) {
		return fmt.Errorf("%w: failed result with provider handles requires partial-create or cleanup-required reason", ErrCleanupCustody)
	}
	for _, resource := range result.Resources {
		if resource.Cleanup != providerexecutor.CleanupRequired &&
			resource.Cleanup != providerexecutor.CleanupPending &&
			resource.Cleanup != providerexecutor.CleanupBlocked {
			return fmt.Errorf("%w: failed provider resource %q is not retained for cleanup", ErrCleanupCustody, resource.BindingID)
		}
	}
	return nil
}

func normalizeStartRequest(req StartRequest) (StartRequest, time.Time, error) {
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.RuntimeServerID = strings.TrimSpace(req.RuntimeServerID)
	req.ResourceGenerationID = strings.TrimSpace(req.ResourceGenerationID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.TenantID == "" || req.LeaseID == "" || req.RuntimeServerID == "" || req.IdempotencyKey == "" {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: tenant, lease, runtime server, and idempotency key are required", ErrInvalidRequest)
	}
	generationID, generationErr := uuid.Parse(req.ResourceGenerationID)
	if generationErr != nil || generationID == uuid.Nil || generationID.String() != req.ResourceGenerationID {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: resource_generation_id must be a canonical lowercase UUID", ErrInvalidRequest)
	}
	if !validOperation(req.Operation) {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: unsupported operation %q", ErrInvalidRequest, req.Operation)
	}
	if req.RequestedAt.IsZero() {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: requested_at is required for stable idempotent retries", ErrInvalidRequest)
	}
	if req.LeaseRevision == 0 || req.LeaseRevision > providerexecutor.MaxJSONSafeInteger {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: lease revision must fit the JSON-safe integer range", ErrInvalidRequest)
	}
	if req.LedgerRevision == 0 || req.LedgerRevision > providerexecutor.MaxJSONSafeInteger {
		return StartRequest{}, time.Time{}, fmt.Errorf("%w: ledger revision must fit the JSON-safe integer range", ErrInvalidRequest)
	}
	return req, req.RequestedAt.UTC(), nil
}

func normalizeDesiredSpec(req StartRequest, requestedAt time.Time) (*DesiredSpecRevision, error) {
	needsSpec := req.Operation == providerexecutor.OperationPlan ||
		req.Operation == providerexecutor.OperationProvision ||
		req.Operation == providerexecutor.OperationReconcile
	if !needsSpec {
		if req.DesiredSpec != nil {
			return nil, fmt.Errorf("%w: desired spec is not allowed for %s", ErrInvalidRequest, req.Operation)
		}
		return nil, nil
	}
	if req.DesiredSpec == nil {
		return nil, fmt.Errorf("%w: desired spec required for %s", ErrInvalidRequest, req.Operation)
	}
	spec := *req.DesiredSpec
	spec.TenantID = strings.TrimSpace(spec.TenantID)
	spec.LeaseID = strings.TrimSpace(spec.LeaseID)
	spec.Ref = strings.TrimSpace(spec.Ref)
	spec.Digest = strings.ToLower(strings.TrimSpace(spec.Digest))
	if spec.TenantID != req.TenantID || spec.LeaseID != req.LeaseID ||
		spec.Revision == 0 || spec.Revision > providerexecutor.MaxJSONSafeInteger {
		return nil, fmt.Errorf("%w: desired spec authority does not match operation", ErrInvalidRequest)
	}
	canonical, err := canonicalJSON(spec.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: desired spec payload must be valid JSON: %v", ErrInvalidRequest, err)
	}
	expected := sha256Digest(canonical)
	if spec.Digest != expected {
		return nil, fmt.Errorf("%w: desired spec digest does not match canonical payload", ErrInvalidRequest)
	}
	spec.Payload = canonical
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = requestedAt
	} else {
		spec.CreatedAt = spec.CreatedAt.UTC()
	}
	if spec.CreatedAt.After(requestedAt) {
		return nil, fmt.Errorf("%w: desired spec cannot be newer than its operation", ErrInvalidRequest)
	}
	return &spec, nil
}

func canonicalJSON(payload []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func isTerminal(receipt providerexecutor.Receipt) bool {
	return receipt.Status == providerexecutor.StatusSucceeded ||
		receipt.Status == providerexecutor.StatusFailed ||
		receipt.Status == providerexecutor.StatusDenied ||
		receipt.Phase == providerexecutor.PhaseAbsent ||
		receipt.Phase == providerexecutor.PhasePresent ||
		receipt.Phase == providerexecutor.PhasePlanned
}

func validOperation(operation providerexecutor.Operation) bool {
	switch operation {
	case providerexecutor.OperationPlan,
		providerexecutor.OperationProvision,
		providerexecutor.OperationObserve,
		providerexecutor.OperationReconcile,
		providerexecutor.OperationDecommission:
		return true
	default:
		return false
	}
}
