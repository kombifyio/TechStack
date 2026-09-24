package providercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestRegistryRejectsDuplicateAndUnknownAdapters(t *testing.T) {
	registry := NewRegistry()
	executor := &queueExecutor{}
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := registry.Register("managed-compute", executor); !errors.Is(err, ErrDuplicateAdapter) {
		t.Fatalf("duplicate Register error = %v", err)
	}
	registered, err := registry.resolveAdapter("managed-compute")
	if err != nil {
		t.Fatalf("resolveAdapter error = %v", err)
	}
	if _, exposesMutation := registered.crashReadOnly.(CrashRecoverableMutationExecutor); exposesMutation {
		t.Fatalf("read-only registry view %T exposes mutation capability", registered.crashReadOnly)
	}
	if _, exposesReadOnly := registered.crashMutation.(CrashRecoverableReadOnlyExecutor); exposesReadOnly {
		t.Fatalf("mutation registry view %T exposes read-only capability", registered.crashMutation)
	}
	if _, err := registry.resolveAdapter("simulate"); !errors.Is(err, ErrAdapterNotRegistered) {
		t.Fatalf("unknown resolveAdapter error = %v", err)
	}
}

func TestRegistryRequiresCrashRecoverableAdmission(t *testing.T) {
	validCorrelation := CrashRecoveryCapability{
		AdapterManifestHash:          digest("test-adapter-manifest"),
		Mode:                         CrashRecoveryProviderCorrelation,
		PerHeadInvocationKey:         true,
		ProviderPersistedCorrelation: true,
		UniqueCorrelation:            true,
		RecoveryByCorrelation:        true,
	}
	tests := []struct {
		name       string
		executor   CrashRecoverableExecutor
		wantErr    error
		wantLength int
	}{
		{name: "native without per-head key", executor: &capabilityExecutor{capability: CrashRecoveryCapability{AdapterManifestHash: digest("test-adapter-manifest"), Mode: CrashRecoveryNativeIdempotency}}, wantErr: ErrCrashRecovery},
		{name: "local-only correlation", executor: &capabilityExecutor{capability: CrashRecoveryCapability{AdapterManifestHash: digest("test-adapter-manifest"), Mode: CrashRecoveryProviderCorrelation, PerHeadInvocationKey: true}}, wantErr: ErrCrashRecovery},
		{name: "provider correlation", executor: &capabilityExecutor{capability: validCorrelation}, wantLength: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			err := registry.Register("ionos-v1", test.executor)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Register error = %v, want %v", err, test.wantErr)
			}
			if got := registry.Len(); got != test.wantLength {
				t.Fatalf("Registry.Len = %d, want %d", got, test.wantLength)
			}
		})
	}
}

func TestRegistryRejectsTypedNilExecutor(t *testing.T) {
	var executor *capabilityExecutor
	if err := NewRegistry().Register("ionos-v1", executor); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Register typed nil error = %v, want ErrInvalidRequest", err)
	}
}

func TestRegistryCapturesCrashRecoveryCapabilityAtRegistration(t *testing.T) {
	original := CrashRecoveryCapability{
		AdapterManifestHash:  digest("test-adapter-manifest"),
		Mode:                 CrashRecoveryNativeIdempotency,
		PerHeadInvocationKey: true,
	}
	executor := &capabilityExecutor{capability: original}
	registry := NewRegistry()
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	executor.capability = CrashRecoveryCapability{
		AdapterManifestHash:  digest("mutated-adapter-manifest"),
		Mode:                 CrashRecoveryProviderCorrelation,
		PerHeadInvocationKey: true,
	}
	registered, err := registry.resolveAdapter("managed-compute")
	if err != nil {
		t.Fatalf("resolveAdapter: %v", err)
	}
	if err := registered.validateProvisionDispatchPin(
		providerexecutor.OperationProvision,
		ProvisionDispatchNativeIdempotency,
		original.AdapterManifestHash,
	); err != nil {
		t.Fatalf("registered capability changed after adapter mutation: %v", err)
	}
	if err := registered.validateProvisionDispatchPin(
		providerexecutor.OperationProvision,
		ProvisionDispatchNativeIdempotency,
		digest("different-adapter-manifest"),
	); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("manifest mismatch error = %v, want ErrAdapterSafety", err)
	}
}

func TestRegistryRequiresOperationSpecificAtMostOnceCapabilities(t *testing.T) {
	registry := NewRegistry()
	incomplete := &atMostOnceCapabilityExecutor{capability: AtMostOnceProvisionCapability{
		AdapterManifestHash:  digest("test-adapter-manifest"),
		PerHeadInvocationKey: true,
	}}
	if err := registry.RegisterAtMostOnceProvision("ionos-amo", incomplete); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("incomplete AMO registration error = %v, want ErrAdapterSafety", err)
	}
	complete := incomplete.capability
	complete.SideEffectFreePreparation = true
	complete.PreparedRequestDigestBinding = true
	complete.ReadOnlyProvisionPolling = true
	complete.ReadOnlyGeneralObservation = true
	complete.ExactHandleDecommission = true
	complete.ReadOnlyAbsencePolling = true
	if err := registry.RegisterAtMostOnceProvision("ionos-amo", &atMostOnceCapabilityExecutor{capability: complete}); err != nil {
		t.Fatalf("complete AMO registration: %v", err)
	}
	registered, err := registry.resolveAdapter("ionos-amo")
	if err != nil || registered.atMostOnce == nil || registered.crashReadOnly != nil || registered.crashMutation != nil {
		t.Fatalf("at-most-once registration exposed wrong capabilities: %+v, err=%v", registered, err)
	}
}

func TestRegistryLimitsHistoricalManifestToSafeContinuation(t *testing.T) {
	current := digest("current-adapter-manifest")
	historical := digest("historical-adapter-manifest")
	capability := AtMostOnceProvisionCapability{
		AdapterManifestHash:                  current,
		CompatibleContinuationManifestHashes: []string{historical},
		PerHeadInvocationKey:                 true,
		SideEffectFreePreparation:            true,
		PreparedRequestDigestBinding:         true,
		ReadOnlyProvisionPolling:             true,
		ReadOnlyGeneralObservation:           true,
		ExactHandleDecommission:              true,
		ReadOnlyAbsencePolling:               true,
	}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("centron-v1", &atMostOnceCapabilityExecutor{capability: capability}); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	registered, err := registry.resolveAdapter("centron-v1")
	if err != nil {
		t.Fatalf("resolveAdapter: %v", err)
	}
	if err := registered.validateProvisionContinuationPin(
		providerexecutor.OperationProvision,
		ProvisionDispatchAtMostOnceManualReconcile,
		historical,
		providerexecutor.PhaseResourcesBound,
	); err != nil {
		t.Fatalf("historical resources-bound continuation rejected: %v", err)
	}
	if err := registered.validateProvisionContinuationPin(
		providerexecutor.OperationProvision,
		ProvisionDispatchAtMostOnceManualReconcile,
		historical,
		providerexecutor.PhaseAccepted,
	); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("historical accepted dispatch error = %v, want ErrAdapterSafety", err)
	}
	if err := registered.validateProvisionDispatchPin(
		providerexecutor.OperationProvision,
		ProvisionDispatchAtMostOnceManualReconcile,
		historical,
	); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("historical fresh-start dispatch error = %v, want ErrAdapterSafety", err)
	}
	if err := registered.validateProvisionContinuationPin(
		providerexecutor.OperationDecommission,
		ProvisionDispatchAtMostOnceManualReconcile,
		historical,
		providerexecutor.PhaseAccepted,
	); err != nil {
		t.Fatalf("historical decommission continuation rejected: %v", err)
	}
	if err := registered.validateProvisionContinuationPin(
		providerexecutor.OperationPlan,
		ProvisionDispatchAtMostOnceManualReconcile,
		historical,
		providerexecutor.PhaseResourcesBound,
	); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("historical non-provision continuation error = %v, want ErrAdapterSafety", err)
	}
}

func TestRegistryRejectsInvalidContinuationManifestAllowList(t *testing.T) {
	base := AtMostOnceProvisionCapability{
		AdapterManifestHash:          digest("current-adapter-manifest"),
		PerHeadInvocationKey:         true,
		SideEffectFreePreparation:    true,
		PreparedRequestDigestBinding: true,
		ReadOnlyProvisionPolling:     true,
		ReadOnlyGeneralObservation:   true,
		ExactHandleDecommission:      true,
		ReadOnlyAbsencePolling:       true,
	}
	for name, hashes := range map[string][]string{
		"malformed": {"sha256:not-a-digest"},
		"current":   {base.AdapterManifestHash},
		"duplicate": {digest("historical"), digest("historical")},
	} {
		t.Run(name, func(t *testing.T) {
			capability := base
			capability.CompatibleContinuationManifestHashes = hashes
			if err := NewRegistry().RegisterAtMostOnceProvision(
				"centron-v1", &atMostOnceCapabilityExecutor{capability: capability},
			); !errors.Is(err, ErrAdapterSafety) {
				t.Fatalf("registration error = %v, want ErrAdapterSafety", err)
			}
		})
	}
}

func TestRegistryReturnsPinnedReadOnlyProvisionResolutionView(t *testing.T) {
	record, _ := guardedProvisionResolutionRecord(t)
	executor := &resolutionAtMostOnceExecutor{atMostOnceExecutor: &atMostOnceExecutor{}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision(record.Command.AdapterID, executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	view, err := registry.ProvisionResolutionProviderForOperation(record)
	if err != nil {
		t.Fatalf("ProvisionResolutionProviderForOperation: %v", err)
	}
	if _, exposesMutation := view.(AtMostOnceProvisionExecutor); exposesMutation {
		t.Fatalf("resolution view %T exposes provider mutation capability", view)
	}

	stale := record
	stale.ExecutionProfile.AdapterManifestHash = digest("different-adapter-manifest")
	if _, err := registry.ProvisionResolutionProviderForOperation(stale); err != nil {
		t.Fatalf("stale read-only pin must still admit current-adapter discovery: %v", err)
	}
	tampered := record
	tampered.ExecutionProfile.AdapterManifestHash = "sha256:not-a-digest"
	if _, err := registry.ProvisionResolutionProviderForOperation(tampered); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("malformed manifest error = %v, want ErrAdapterSafety", err)
	}
	tampered = record
	tampered.ExecutionProfile.AdapterID = "other-adapter"
	if _, err := registry.ProvisionResolutionProviderForOperation(tampered); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("adapter mismatch error = %v, want ErrAdapterSafety", err)
	}
}

type resolutionAtMostOnceExecutor struct{ *atMostOnceExecutor }

func (*resolutionAtMostOnceExecutor) BuildProvisionDiscoveryRequest(context.Context, OperationRecord, uint64, string, string) (RecordProvisionDiscoveryRequest, error) {
	return RecordProvisionDiscoveryRequest{}, nil
}

func (*resolutionAtMostOnceExecutor) VerifyProvisionDiscovery(context.Context, OperationRecord, ProvisionDiscoveryObservation) error {
	return nil
}

func (*resolutionAtMostOnceExecutor) VerifyProvisionDecision(context.Context, OperationRecord, ProvisionDiscoveryObservation, ProvisionResolutionRequest) error {
	return nil
}

type capabilityExecutor struct{ capability CrashRecoveryCapability }

func (e *capabilityExecutor) CrashRecoveryCapability() CrashRecoveryCapability { return e.capability }

func (*capabilityExecutor) result() providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed, Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTransient, Retryable: true}}
}

func (e *capabilityExecutor) ExecuteCrashRecoverableReadOnly(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.result()
}

func (e *capabilityExecutor) ExecuteCrashRecoverableMutation(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.result()
}

type atMostOnceCapabilityExecutor struct{ capability AtMostOnceProvisionCapability }

func (e *atMostOnceCapabilityExecutor) AtMostOnceProvisionCapability() AtMostOnceProvisionCapability {
	return e.capability
}

func (*atMostOnceCapabilityExecutor) PrepareProvision(_ context.Context, invocation AdapterInvocation) (PreparedProvisionRequest, error) {
	return preparedProvisionForInvocation(invocation), nil
}

func (*atMostOnceCapabilityExecutor) DispatchPreparedProvision(context.Context, PreparedProvisionRequest, ExecutePermit) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted}
}

func (*atMostOnceCapabilityExecutor) PollProvisionPresence(_ context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: invocation.Request.Previous.Phase, Resources: invocation.Request.Previous.Resources}
}

func (*atMostOnceCapabilityExecutor) ObserveReadOnly(_ context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: invocation.Request.Previous.Phase, Resources: invocation.Request.Previous.Resources}
}

func (*atMostOnceCapabilityExecutor) DecommissionExactHandles(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted}
}

func (*atMostOnceCapabilityExecutor) PollDecommissionAbsence(_ context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: invocation.Request.Previous.Phase, Resources: invocation.Request.Previous.Resources}
}
