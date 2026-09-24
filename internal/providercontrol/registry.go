package providercontrol

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// Registry contains explicitly enabled TechStack-owned provider adapters. An
// empty registry is a valid boot state; execution fails closed until an
// adapter is registered. One TechStack authority selects operation-specific
// crash-recoverable or at-most-once provision execution paths.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]registeredAdapter
}

type registeredAdapter struct {
	crashReadOnly CrashRecoverableReadOnlyExecutor
	crashMutation CrashRecoverableMutationExecutor
	crashRecovery CrashRecoveryCapability
	atMostOnce    AtMostOnceProvisionExecutor
	reconcile     ExactHandleReconcileExecutor
	resolution    ProvisionResolutionProvider
	manifestHash  string
	// continuationManifestHashes is an explicit allow-list for historical
	// manifests that can be resumed only at a read-only, post-dispatch phase.
	// Keeping this separate from manifestHash makes it impossible for a stale
	// catalog pin to authorize a new create.
	continuationManifestHashes map[string]struct{}
}

// The registry stores method-value wrappers rather than the original composite
// adapter. A caller holding the read-only view therefore cannot recover the
// mutation interface through a Go type assertion on the dynamic value.
type crashRecoverableReadOnlyView struct {
	execute func(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

func (v crashRecoverableReadOnlyView) ExecuteCrashRecoverableReadOnly(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return v.execute(ctx, invocation)
}

type crashRecoverableMutationView struct {
	execute func(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

// provisionResolutionProviderView retains only the certified read/evidence
// methods required by Operator Reconcile. Returning this view prevents route
// composition from recovering the registered adapter's create or delete
// methods through a type assertion.
type provisionResolutionProviderView struct {
	build  func(context.Context, OperationRecord, uint64, string, string) (RecordProvisionDiscoveryRequest, error)
	verify func(context.Context, OperationRecord, ProvisionDiscoveryObservation) error
	decide func(context.Context, OperationRecord, ProvisionDiscoveryObservation, ProvisionResolutionRequest) error
}

func (v provisionResolutionProviderView) BuildProvisionDiscoveryRequest(ctx context.Context, record OperationRecord, revision uint64, subjectID, key string) (RecordProvisionDiscoveryRequest, error) {
	return v.build(ctx, record, revision, subjectID, key)
}

func (v provisionResolutionProviderView) VerifyProvisionDiscovery(ctx context.Context, record OperationRecord, observation ProvisionDiscoveryObservation) error {
	return v.verify(ctx, record, observation)
}

func (v provisionResolutionProviderView) VerifyProvisionDecision(ctx context.Context, record OperationRecord, observation ProvisionDiscoveryObservation, request ProvisionResolutionRequest) error {
	return v.decide(ctx, record, observation, request)
}

func (v crashRecoverableMutationView) ExecuteCrashRecoverableMutation(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return v.execute(ctx, invocation)
}

// exactHandleReconcileView keeps only the two reconcile methods, so the
// coordinator's reconcile path cannot reach create or delete methods.
type exactHandleReconcileView struct {
	reconcile func(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
	poll      func(context.Context, AdapterInvocation) providerexecutor.ExecutionResult
}

func (v exactHandleReconcileView) ReconcileExactHandles(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return v.reconcile(ctx, invocation)
}

func (v exactHandleReconcileView) PollReconcileConvergence(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return v.poll(ctx, invocation)
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]registeredAdapter)}
}

// Register adds one explicitly configured adapter. Adapter identifiers are
// TechStack configuration keys, not provider or StackKit architecture fields.
func (r *Registry) Register(adapterID string, executor CrashRecoverableExecutor) error {
	adapterID = strings.TrimSpace(adapterID)
	if adapterID == "" || executor == nil || nilCrashRecoverableExecutor(executor) {
		return fmt.Errorf("%w: adapter id and executor are required", ErrInvalidRequest)
	}
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidRequest)
	}
	capability, err := admitCrashRecoverableExecutor(executor)
	if err != nil {
		return fmt.Errorf("%w: adapter %s: %v", ErrCrashRecovery, adapterID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = make(map[string]registeredAdapter)
	}
	if _, exists := r.adapters[adapterID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateAdapter, adapterID)
	}
	r.adapters[adapterID] = registeredAdapter{
		crashReadOnly: crashRecoverableReadOnlyView{
			execute: executor.ExecuteCrashRecoverableReadOnly,
		},
		crashMutation: crashRecoverableMutationView{
			execute: executor.ExecuteCrashRecoverableMutation,
		},
		crashRecovery: capability,
		manifestHash:  capability.AdapterManifestHash,
	}
	return nil
}

// RegisterAtMostOnceProvision adds an adapter whose create API has neither
// documented idempotency nor authoritative unique correlation. This path is
// admitted only with separate dispatch, read-only polling, and exact-handle
// cleanup methods. Reconcile mutations are admitted only when the adapter
// declares and implements the exact-handle reconcile capability.
func (r *Registry) RegisterAtMostOnceProvision(adapterID string, executor AtMostOnceProvisionExecutor) error {
	adapterID = strings.TrimSpace(adapterID)
	if adapterID == "" || executor == nil || nilAtMostOnceProvisionExecutor(executor) {
		return fmt.Errorf("%w: adapter id and executor are required", ErrInvalidRequest)
	}
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidRequest)
	}
	capability := executor.AtMostOnceProvisionCapability()
	if err := validateAtMostOnceProvisionCapability(capability); err != nil {
		return fmt.Errorf("%w: adapter %s: %v", ErrAdapterSafety, adapterID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = make(map[string]registeredAdapter)
	}
	if _, exists := r.adapters[adapterID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateAdapter, adapterID)
	}
	var resolution ProvisionResolutionProvider
	if provider, ok := executor.(ProvisionResolutionProvider); ok && provider != nil {
		resolution = provisionResolutionProviderView{
			build: provider.BuildProvisionDiscoveryRequest, verify: provider.VerifyProvisionDiscovery,
			decide: provider.VerifyProvisionDecision,
		}
	}
	var reconcile ExactHandleReconcileExecutor
	if capability.ExactHandleReconcile {
		reconciler, ok := executor.(ExactHandleReconcileExecutor)
		if !ok || reconciler == nil {
			return fmt.Errorf("%w: adapter %s declares exact-handle reconcile without implementing it", ErrAdapterSafety, adapterID)
		}
		reconcile = exactHandleReconcileView{
			reconcile: reconciler.ReconcileExactHandles, poll: reconciler.PollReconcileConvergence,
		}
	}
	r.adapters[adapterID] = registeredAdapter{
		atMostOnce: executor, reconcile: reconcile, resolution: resolution, manifestHash: capability.AdapterManifestHash,
		continuationManifestHashes: continuationManifestHashSet(capability.CompatibleContinuationManifestHashes),
	}
	return nil
}

// ProvisionResolutionProviderForOperation returns a read/evidence-only view
// for the exact adapter and immutable manifest pinned by a parked provision.
// It never returns the registered mutation-capable adapter itself.
func (r *Registry) ProvisionResolutionProviderForOperation(record OperationRecord) (ProvisionResolutionProvider, error) {
	if record.Command.Operation != providerexecutor.OperationProvision ||
		strings.TrimSpace(record.Command.AdapterID) == "" ||
		record.Command.AdapterID != record.ExecutionProfile.AdapterID {
		return nil, fmt.Errorf("%w: operation has no exact provision adapter pin", ErrAdapterSafety)
	}
	adapter, err := r.resolveAdapter(record.Command.AdapterID)
	if err != nil {
		return nil, err
	}
	if err := adapter.validateProvisionReadOnlyPin(
		record.Command.Operation,
		record.ProvisionDispatch,
		record.ExecutionProfile.AdapterManifestHash,
	); err != nil {
		return nil, err
	}
	if adapter.resolution == nil {
		return nil, fmt.Errorf("%w: adapter has no certified provision discovery", ErrAdapterSafety)
	}
	return adapter.resolution, nil
}

func nilCrashRecoverableExecutor(executor CrashRecoverableExecutor) bool {
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilAtMostOnceProvisionExecutor(executor AtMostOnceProvisionExecutor) bool {
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *Registry) resolveAdapter(adapterID string) (registeredAdapter, error) {
	adapterID = strings.TrimSpace(adapterID)
	if r == nil || adapterID == "" {
		return registeredAdapter{}, fmt.Errorf("%w: %s", ErrAdapterNotRegistered, adapterID)
	}
	r.mu.RLock()
	adapter, exists := r.adapters[adapterID]
	r.mu.RUnlock()
	if !exists || ((adapter.crashReadOnly == nil || adapter.crashMutation == nil) && adapter.atMostOnce == nil) {
		return registeredAdapter{}, fmt.Errorf("%w: %s", ErrAdapterNotRegistered, adapterID)
	}
	return adapter, nil
}

func (a registeredAdapter) validateProvisionDispatchPin(
	operation providerexecutor.Operation,
	pinned ProvisionDispatchMode,
	pinnedManifestHash string,
) error {
	if !executionProfileDigestPattern.MatchString(pinnedManifestHash) || a.manifestHash != pinnedManifestHash {
		return fmt.Errorf("%w: catalog adapter manifest does not match registered implementation", ErrAdapterSafety)
	}
	return a.validateProvisionDispatchMode(operation, pinned)
}

func (a registeredAdapter) validateProvisionDispatchMode(
	operation providerexecutor.Operation,
	pinned ProvisionDispatchMode,
) error {
	if !admittedProvisionDispatchMode(pinned) {
		return fmt.Errorf("%w: catalog provision dispatch mode %q is not admitted", ErrAdapterSafety, pinned)
	}
	if a.atMostOnce != nil {
		if pinned != ProvisionDispatchAtMostOnceManualReconcile {
			return fmt.Errorf("%w: catalog mode %q does not match at-most-once adapter", ErrAdapterSafety, pinned)
		}
		if operation == providerexecutor.OperationReconcile && a.reconcile == nil {
			return fmt.Errorf("%w: at-most-once provision adapter has no exact-handle reconcile capability", ErrAdapterSafety)
		}
		return nil
	}
	if a.crashReadOnly == nil || a.crashMutation == nil {
		return ErrAdapterNotRegistered
	}
	expected := ProvisionDispatchMode("")
	switch a.crashRecovery.Mode {
	case CrashRecoveryNativeIdempotency:
		expected = ProvisionDispatchNativeIdempotency
	case CrashRecoveryProviderCorrelation:
		expected = ProvisionDispatchProviderCorrelation
	default:
		return ErrCrashRecovery
	}
	if pinned != expected {
		return fmt.Errorf("%w: catalog mode %q does not match adapter capability %q", ErrAdapterSafety, pinned, expected)
	}
	return nil
}

// validateProvisionContinuationPin validates an already-persisted operation.
// Historical manifests are accepted only for the read-only resources_bound
// provision poll or exact-handle decommission. Fresh starts and every phase
// that can prepare or dispatch create still require the current manifest.
func (a registeredAdapter) validateProvisionContinuationPin(
	operation providerexecutor.Operation,
	pinned ProvisionDispatchMode,
	pinnedManifestHash string,
	phase providerexecutor.Phase,
) error {
	// Reconcile polling is read-only, so a manifest successor deployed while a
	// power transition converges may observe it; the side-effecting accepted
	// step still requires the current manifest.
	historicalContinuationAllowed := operation == providerexecutor.OperationDecommission ||
		(operation == providerexecutor.OperationProvision && phase == providerexecutor.PhaseResourcesBound) ||
		(operation == providerexecutor.OperationReconcile && phase == providerexecutor.PhaseResourcesBound)
	if !historicalContinuationAllowed ||
		!executionProfileDigestPattern.MatchString(pinnedManifestHash) {
		return a.validateProvisionDispatchPin(operation, pinned, pinnedManifestHash)
	}
	if a.manifestHash != pinnedManifestHash {
		if _, compatible := a.continuationManifestHashes[pinnedManifestHash]; !compatible {
			return fmt.Errorf("%w: catalog adapter manifest does not match registered implementation", ErrAdapterSafety)
		}
	}
	return a.validateProvisionDispatchMode(operation, pinned)
}

// validateProvisionReadOnlyPin admits the resolution provider for a parked
// operation. A historical pin may be observed here even while the head is
// accepted, because this path exposes discovery only on the current adapter;
// adoption still goes through the revision-bound resolution CAS and never
// dispatches a create.
func (a registeredAdapter) validateProvisionReadOnlyPin(
	operation providerexecutor.Operation,
	pinned ProvisionDispatchMode,
	pinnedManifestHash string,
) error {
	if !executionProfileDigestPattern.MatchString(pinnedManifestHash) {
		return fmt.Errorf("%w: catalog adapter manifest does not match registered implementation", ErrAdapterSafety)
	}
	return a.validateProvisionDispatchMode(operation, pinned)
}

func continuationManifestHashSet(hashes []string) map[string]struct{} {
	if len(hashes) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		set[hash] = struct{}{}
	}
	return set
}

func admitCrashRecoverableExecutor(executor CrashRecoverableExecutor) (CrashRecoveryCapability, error) {
	capability := executor.CrashRecoveryCapability()
	if err := validateCrashRecoveryCapability(capability); err != nil {
		return CrashRecoveryCapability{}, err
	}
	return capability, nil
}

func validateCrashRecoveryCapability(capability CrashRecoveryCapability) error {
	if !executionProfileDigestPattern.MatchString(capability.AdapterManifestHash) {
		return fmt.Errorf("immutable adapter manifest digest is required")
	}
	if !capability.PerHeadInvocationKey {
		return fmt.Errorf("per-head invocation key is required")
	}
	switch capability.Mode {
	case CrashRecoveryNativeIdempotency:
		return nil
	case CrashRecoveryProviderCorrelation:
		if !capability.ProviderPersistedCorrelation || !capability.UniqueCorrelation || !capability.RecoveryByCorrelation {
			return fmt.Errorf("provider correlation must be persisted, unique, and recoverable")
		}
		return nil
	default:
		return fmt.Errorf("unsupported crash-recovery mode %q", capability.Mode)
	}
}

func validateAtMostOnceProvisionCapability(capability AtMostOnceProvisionCapability) error {
	if !executionProfileDigestPattern.MatchString(capability.AdapterManifestHash) {
		return fmt.Errorf("immutable adapter manifest digest is required")
	}
	seen := make(map[string]struct{}, len(capability.CompatibleContinuationManifestHashes))
	for _, hash := range capability.CompatibleContinuationManifestHashes {
		if !executionProfileDigestPattern.MatchString(hash) {
			return fmt.Errorf("compatible continuation adapter manifest must be a lowercase sha256 digest")
		}
		if hash == capability.AdapterManifestHash {
			return fmt.Errorf("compatible continuation adapter manifest must differ from current manifest")
		}
		if _, duplicate := seen[hash]; duplicate {
			return fmt.Errorf("compatible continuation adapter manifests must be unique")
		}
		seen[hash] = struct{}{}
	}
	if !capability.PerHeadInvocationKey {
		return fmt.Errorf("per-head invocation key is required")
	}
	if !capability.SideEffectFreePreparation || !capability.PreparedRequestDigestBinding {
		return fmt.Errorf("side-effect-free preparation with exact request-digest binding is required")
	}
	if !capability.ReadOnlyProvisionPolling || !capability.ReadOnlyGeneralObservation ||
		!capability.ExactHandleDecommission || !capability.ReadOnlyAbsencePolling {
		return fmt.Errorf("separate read-only polling, observation, and exact-handle cleanup are required")
	}
	if capability.ExactHandleReconcile != capability.ReadOnlyReconcilePolling {
		return fmt.Errorf("exact-handle reconcile requires separate read-only convergence polling")
	}
	return nil
}

// Len returns the number of explicitly registered adapters.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adapters)
}
