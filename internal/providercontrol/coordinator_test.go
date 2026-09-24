package providercontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

var contractNow = time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)

const testResourceGenerationID = "11111111-1111-4111-8111-111111111111"

func TestCoordinatorBootsWithEmptyRegistryAndFailsExecutionClosed(t *testing.T) {
	registry := NewRegistry()
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("missing")},
		Ledger:   ledger,
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if registry.Len() != 0 {
		t.Fatalf("empty registry has %d adapters", registry.Len())
	}
	_, _, err = coordinator.Start(t.Context(), planRequest())
	if !errors.Is(err, ErrAdapterNotRegistered) {
		t.Fatalf("Start error = %v, want ErrAdapterNotRegistered", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatal("missing adapter stranded an operation in the ledger")
	}
}

func TestCoordinatorOwnsInitialAndAppendReceipts(t *testing.T) {
	registry := NewRegistry()
	executor := &queueExecutor{results: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusSucceeded,
		Phase:  providerexecutor.PhasePlanned,
	}}}
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	now := contractNow
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	req := planRequest()
	record, created, err := coordinator.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !created || record.Head.Sequence != 1 || record.Head.Phase != providerexecutor.PhaseRequested {
		t.Fatalf("initial record = %+v, created=%v", record.Head, created)
	}

	record, advanced, err := coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance accepted: %v", err)
	}
	if !advanced || record.Head.Sequence != 2 || record.Head.Phase != providerexecutor.PhaseAccepted || executor.calls != 0 {
		t.Fatalf("accepted step = %+v, adapter calls=%d", record.Head, executor.calls)
	}

	record, advanced, err = coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance planned: %v", err)
	}
	if !advanced || record.Head.Sequence != 3 || record.Head.Phase != providerexecutor.PhasePlanned ||
		executor.calls != 1 || executor.readOnlyCalls != 1 || executor.mutationCalls != 0 {
		t.Fatalf("planned step = %+v, adapter calls=%d read=%d mutation=%d", record.Head, executor.calls, executor.readOnlyCalls, executor.mutationCalls)
	}

	terminal, advanced, err := coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance terminal: %v", err)
	}
	if advanced || terminal.Head.ReceiptDigest != record.Head.ReceiptDigest || executor.calls != 1 {
		t.Fatalf("terminal operation advanced unexpectedly: %+v", terminal.Head)
	}
}

func TestCoordinatorRoutesSideEffectingClaimOnlyToMutationCapability(t *testing.T) {
	executor := &queueExecutor{results: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending,
		Phase:  providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{{
			BindingID: "server", Kind: "compute", NativeRef: "provider-server-routing",
			OwnershipHash: digest("provider-server-routing-owner"),
			Disposition:   providerexecutor.DispositionDelete,
			Observation:   providerexecutor.ObservationUnknown,
			Cleanup:       providerexecutor.CleanupPending,
		}},
	}}}
	registry := NewRegistry()
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || record.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted step = %+v, err=%v", record.Head, err)
	}
	record, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("mutation step: %v", err)
	}
	if !advanced || record.Head.Phase != providerexecutor.PhaseResourcesBound ||
		executor.mutationCalls != 1 || executor.readOnlyCalls != 0 {
		t.Fatalf("mutation step = %+v, read=%d mutation=%d", record.Head, executor.readOnlyCalls, executor.mutationCalls)
	}
	ledger.mu.Lock()
	claimAccess := ledger.claims[record.Command.OperationID].claim.Access
	ledger.mu.Unlock()
	if claimAccess != ExecutionClaimSideEffecting {
		t.Fatalf("persisted claim access = %q, want %q", claimAccess, ExecutionClaimSideEffecting)
	}
}

func TestCoordinatorAtMostOnceProvisionDispatchNeverRearms(t *testing.T) {
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending,
		Phase:  providerexecutor.PhaseAccepted,
	}}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: profile},
		Ledger:   newMemoryLedger(),
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || record.Head.Phase != providerexecutor.PhaseAccepted || len(record.Head.Resources) != 0 {
		t.Fatalf("accepted head = %+v, error=%v", record.Head, err)
	}
	record, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("first guarded Advance: %v", err)
	}
	if advanced || record.AutomationState != OperationAutomationManualReconcileRequired || executor.dispatchCalls != 1 {
		t.Fatalf("guarded pending result = state=%q advanced=%v calls=%d", record.AutomationState, advanced, executor.dispatchCalls)
	}
	if _, _, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); !errors.Is(err, ErrProvisionManualReconcile) {
		t.Fatalf("second Advance error = %v, want ErrProvisionManualReconcile", err)
	}
	if executor.dispatchCalls != 1 {
		t.Fatalf("dispatch calls = %d, want exactly one", executor.dispatchCalls)
	}
}

func TestCoordinatorPreparesProvisionBeforeConsumingDispatchCustody(t *testing.T) {
	executor := &atMostOnceExecutor{prepareErr: errors.New("local request build failed")}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, _, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); err == nil {
		t.Fatal("Advance accepted a failed side-effect-free preparation")
	}
	if len(ledger.dispatchGuards) != 0 || executor.dispatchCalls != 0 {
		t.Fatalf("failed preparation guards=%d dispatches=%d, want zero", len(ledger.dispatchGuards), executor.dispatchCalls)
	}
	executor.mu.Lock()
	executor.prepareErr = nil
	executor.mu.Unlock()
	result, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance after repaired preparation: %v", err)
	}
	if advanced || result.AutomationState != OperationAutomationManualReconcileRequired || executor.dispatchCalls != 1 {
		t.Fatalf("result state=%q advanced=%v dispatches=%d", result.AutomationState, advanced, executor.dispatchCalls)
	}
}

func TestCoordinatorAtMostOnceDispatchDurablyRetainsHandleFreeFailure(t *testing.T) {
	result := providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
		Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTimeout, Retryable: true},
	}
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{result}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	failed, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || !advanced || failed.Head.Status != providerexecutor.StatusFailed ||
		failed.Head.Phase != providerexecutor.PhaseFailed || len(failed.Head.Resources) != 0 {
		t.Fatalf("durable AMO failure = %#v advanced=%v error=%v", failed.Head, advanced, err)
	}
	if replay, advanced, err := coordinator.Advance(
		t.Context(), record.Command.TenantID, record.Command.OperationID,
	); err != nil || advanced || replay.Head.ReceiptDigest != failed.Head.ReceiptDigest {
		t.Fatalf("failed AMO replay = %#v advanced=%v error=%v", replay.Head, advanced, err)
	}
	if executor.dispatchCalls != 1 {
		t.Fatalf("dispatch calls = %d, want one", executor.dispatchCalls)
	}
}

func TestCoordinatorObservesTerminalFailureExactlyOnceAfterDurableAppend(t *testing.T) {
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusFailed,
		Phase:  providerexecutor.PhaseFailed,
		Reason: &providerexecutor.Reason{
			Code:      providerexecutor.ReasonCodeProviderTimeout,
			Retryable: true,
		},
	}}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	var observations int
	var observedAttrs []any
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: newMemoryLedger(),
		TerminalFailureObserver: func(command providerexecutor.Command, previous, next providerexecutor.Receipt) {
			observations++
			observedAttrs = terminalFailureObservationAttrs(command, previous, next)
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	failed, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || !advanced || failed.Head.Status != providerexecutor.StatusFailed {
		t.Fatalf("terminal failure = %#v advanced=%v error=%v", failed.Head, advanced, err)
	}
	if _, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); err != nil || advanced {
		t.Fatalf("terminal replay advanced=%v error=%v", advanced, err)
	}
	if observations != 1 {
		t.Fatalf("terminal failure observations = %d, want exactly one", observations)
	}
	wantAttrs := []any{
		logFieldOperation, string(providerexecutor.OperationProvision),
		logFieldOperationID, record.Command.OperationID,
		logFieldLeaseID, record.Command.LeaseID,
		"provider_id", record.Command.ProviderID,
		"resource_generation_id", record.Command.ResourceGenerationID,
		"correlation_id", record.Command.OperationID,
		"phase", string(providerexecutor.PhaseFailed),
		"previous_phase", string(providerexecutor.PhaseAccepted),
		"reason_code", providerexecutor.ReasonCodeProviderTimeout,
		"retryable", true,
		"receipt_sequence", failed.Head.Sequence,
	}
	if len(observedAttrs) != len(wantAttrs) {
		t.Fatalf("observation attributes = %#v, want %#v", observedAttrs, wantAttrs)
	}
	for i := range wantAttrs {
		if observedAttrs[i] != wantAttrs[i] {
			t.Fatalf("observation attributes = %#v, want %#v", observedAttrs, wantAttrs)
		}
	}
}

func TestShouldObserveTerminalProvisionFailureScopesEvent(t *testing.T) {
	baseCommand := providerexecutor.Command{Operation: providerexecutor.OperationProvision}
	accepted := providerexecutor.Receipt{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted}
	failed := providerexecutor.Receipt{Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed}
	cases := []struct {
		name    string
		command providerexecutor.Command
		next    providerexecutor.Receipt
		want    bool
	}{
		{name: "provision failed", command: baseCommand, next: failed, want: true},
		{name: "non provision failed", command: providerexecutor.Command{Operation: providerexecutor.OperationPlan}, next: failed},
		{name: "provision pending", command: baseCommand, next: accepted},
		{name: "provision succeeded", command: baseCommand, next: providerexecutor.Receipt{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent}},
		{name: "manual reconcile pending", command: baseCommand, next: accepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldObserveTerminalProvisionFailure(tc.command, accepted, tc.next); got != tc.want {
				t.Fatalf("shouldObserveTerminalProvisionFailure() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCoordinatorAtMostOnceDispatchRejectsEmptyResourcesBound(t *testing.T) {
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
	}}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, _, err := coordinator.Advance(
		t.Context(), record.Command.TenantID, record.Command.OperationID,
	); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("empty resources-bound result error = %v, want ErrCleanupCustody", err)
	}
}

func TestPreparedProvisionBindsSeparateAdapterManifest(t *testing.T) {
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", &atMostOnceExecutor{}); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: newMemoryLedger(),
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	invocation, err := newAdapterInvocation(providerexecutor.ExecutionRequest{Command: record.Command, Previous: record.Head})
	if err != nil {
		t.Fatalf("newAdapterInvocation: %v", err)
	}
	prepared := preparedProvisionForInvocation(invocation)
	prepared.binding.AdapterManifestHash = digest("different-manifest")
	if _, err := validatePreparedProvisionRequest(prepared, invocation, profile.AdapterManifestHash); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("manifest mismatch error = %v, want ErrAdapterSafety", err)
	}
}

func TestCoordinatorAtMostOnceGuardCoversEveryOperationInResourceGeneration(t *testing.T) {
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted,
	}}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
		Now: func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	firstRequest := provisionRequest()
	first, _, err := coordinator.Start(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	first, _, err = coordinator.Advance(t.Context(), first.Command.TenantID, first.Command.OperationID)
	if err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if _, _, err := coordinator.Advance(t.Context(), first.Command.TenantID, first.Command.OperationID); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}

	secondRequest := provisionRequest()
	secondRequest.IdempotencyKey = "provision-2"
	second, _, err := coordinator.Start(t.Context(), secondRequest)
	if err != nil {
		t.Fatalf("Start second: %v", err)
	}
	second, _, err = coordinator.Advance(t.Context(), second.Command.TenantID, second.Command.OperationID)
	if err != nil {
		t.Fatalf("accept second: %v", err)
	}
	if _, _, err := coordinator.Advance(t.Context(), second.Command.TenantID, second.Command.OperationID); !errors.Is(err, ErrProvisionManualReconcile) {
		t.Fatalf("second generation dispatch error = %v, want ErrProvisionManualReconcile", err)
	}
	loaded, err := ledger.LoadOperation(t.Context(), second.Command.TenantID, second.Command.OperationID)
	if err != nil || loaded.AutomationState != OperationAutomationManualReconcileRequired {
		t.Fatalf("second operation state=%q error=%v", loaded.AutomationState, err)
	}
	if executor.dispatchCalls != 1 {
		t.Fatalf("generation dispatch calls = %d, want exactly one", executor.dispatchCalls)
	}
}

func TestCoordinatorAtMostOnceProvisionPollIsReadOnlyAndIdentityStable(t *testing.T) {
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "provider-server-1",
		OwnershipHash: digest("owner-server"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	present := resource
	present.Observation = providerexecutor.ObservationPresent
	present.Cleanup = providerexecutor.CleanupRequired
	executor := &atMostOnceExecutor{
		dispatchResults: []providerexecutor.ExecutionResult{{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
			Resources: []providerexecutor.ResourceBinding{resource},
		}},
		pollResults: []providerexecutor.ExecutionResult{{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
			Resources: []providerexecutor.ResourceBinding{present},
		}},
	}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile},
		Ledger: newMemoryLedger(), Now: func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range 3 {
		record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}
	if record.Head.Phase != providerexecutor.PhasePresent || executor.dispatchCalls != 1 || executor.pollCalls != 1 {
		t.Fatalf("final head=%q dispatch=%d poll=%d", record.Head.Phase, executor.dispatchCalls, executor.pollCalls)
	}
}

func TestProvisionPollRejectsResourceIdentityDrift(t *testing.T) {
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "provider-server-1",
		OwnershipHash: digest("owner-server"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	previous := providerexecutor.Receipt{
		Operation: providerexecutor.OperationProvision, Phase: providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{resource},
	}
	drifted := resource
	drifted.NativeRef = "provider-server-2"
	if err := validateProvisionPollIdentity(previous, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: []providerexecutor.ResourceBinding{drifted},
	}); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("validateProvisionPollIdentity error = %v, want ErrCleanupCustody", err)
	}
}

func TestProvisionPollAllowsOnlyMonotonicChildrenBelowDurableRoot(t *testing.T) {
	root := providerexecutor.ResourceBinding{
		BindingID: "datacenter", Kind: "ionos.datacenter", NativeRef: "provider-datacenter-1",
		OwnershipHash: digest("owner-datacenter"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupRequired,
	}
	server := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "ionos.server", NativeRef: "provider-server-1",
		ParentBindingID: "datacenter", OwnershipHash: digest("owner-server"),
		Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupRequired,
	}
	ip := providerexecutor.ResourceBinding{
		BindingID: "public-ip", Kind: "ionos.public-ip", NativeRef: "ionos-ip://192.0.2.10",
		ParentBindingID: "server", OwnershipHash: digest("owner-ip"),
		Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupRequired,
	}
	previous := providerexecutor.Receipt{
		Operation: providerexecutor.OperationProvision, Phase: providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{root},
	}
	if err := validateProvisionPollIdentity(previous, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: []providerexecutor.ResourceBinding{root, server, ip},
	}); err != nil {
		t.Fatalf("monotonic child discovery rejected: %v", err)
	}

	unrelated := server
	unrelated.BindingID = "other-datacenter"
	unrelated.Kind = "ionos.datacenter"
	unrelated.NativeRef = "provider-datacenter-2"
	unrelated.ParentBindingID = ""
	unrelated.OwnershipHash = digest("owner-other")
	if err := validateProvisionPollIdentity(previous, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{root, unrelated},
	}); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("unrelated root error = %v, want ErrCleanupCustody", err)
	}
}

func TestExecutePermitCannotBeSerialized(t *testing.T) {
	claim := ExecutionClaim{
		TenantID: "tenant-1", OperationID: "operation-1", ResourceGenerationID: testResourceGenerationID,
		HeadSequence: 2, HeadReceiptDigest: digest("accepted"), Access: ExecutionClaimSideEffecting,
		Owner: "worker-1", Token: "secret-token",
	}
	binding := PreparedProvisionBinding{RequestDigest: digest("request")}
	if _, err := json.Marshal(newExecutePermit(claim, binding)); err == nil {
		t.Fatal("json.Marshal accepted an execute permit")
	}
}

func TestCoordinatorRejectsCatalogAdapterDispatchModeMismatch(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, _, err := coordinator.Start(t.Context(), provisionRequest()); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("Start error = %v, want ErrAdapterSafety", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatalf("dispatch-mode mismatch wrote %d operations", ledger.beginCalls)
	}
}

func TestCoordinatorRejectsCatalogAdapterManifestMismatchBeforeLedgerWrite(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.AdapterManifestHash = digest("different-adapter-manifest")
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, _, err := coordinator.Start(t.Context(), provisionRequest()); !errors.Is(err, ErrAdapterSafety) {
		t.Fatalf("Start error = %v, want ErrAdapterSafety", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatalf("manifest mismatch wrote %d operations", ledger.beginCalls)
	}
}

func TestCoordinatorStartIsIdempotent(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := planRequest()
	first, created, err := coordinator.Start(t.Context(), req)
	if err != nil || !created {
		t.Fatalf("first Start = %v, created=%v", err, created)
	}
	second, created, err := coordinator.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if created || second.Command.CommandDigest != first.Command.CommandDigest || ledger.beginCalls != 1 {
		t.Fatalf("idempotent Start changed command: created=%v", created)
	}
}

func TestCoordinatorRejectsDesiredSpecDigestDriftBeforeLedgerWrite(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := planRequest()
	req.DesiredSpec.Digest = digest("different")
	if _, _, err := coordinator.Start(t.Context(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start error = %v, want ErrInvalidRequest", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatal("digest drift reached ledger")
	}
}

func TestCoordinatorRequiresStableRequestedAtBeforeLedgerWrite(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := planRequest()
	req.RequestedAt = time.Time{}
	if _, _, err := coordinator.Start(t.Context(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start error = %v, want ErrInvalidRequest", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatal("unstable requested_at reached ledger")
	}
}

func TestCoordinatorNeverAppendsUnprovenAbsence(t *testing.T) {
	target := providerexecutor.ResourceTarget{
		BindingID:     "server",
		Kind:          "compute",
		NativeRef:     "native-server",
		OwnershipHash: digest("owner"),
		Disposition:   providerexecutor.DispositionDelete,
	}
	resource := providerexecutor.ResourceBinding{
		BindingID:     target.BindingID,
		Kind:          target.Kind,
		NativeRef:     target.NativeRef,
		OwnershipHash: target.OwnershipHash,
		Disposition:   target.Disposition,
		Observation:   providerexecutor.ObservationAbsent,
		Cleanup:       providerexecutor.CleanupComplete,
	}
	executor := &queueExecutor{results: []providerexecutor.ExecutionResult{
		{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted},
		{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending},
		{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent, Resources: []providerexecutor.ResourceBinding{resource}},
	}}
	registry := NewRegistry()
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	now := contractNow
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := StartRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation:      providerexecutor.OperationDecommission,
		IdempotencyKey: "delete-1",
		LedgerRevision: 4, Targets: []providerexecutor.ResourceTarget{target},
		RequestedAt: contractNow,
	}
	record, _, err := coordinator.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range 3 {
		record, _, err = coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
		if err != nil {
			t.Fatalf("intermediate Advance: %v", err)
		}
	}
	before := record.Head
	_, _, err = coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if !errors.Is(err, providerexecutor.ErrAbsenceProofRequired) {
		t.Fatalf("unproven absence error = %v, want ErrAbsenceProofRequired", err)
	}
	after, loadErr := ledger.LoadOperation(t.Context(), req.TenantID, record.Command.OperationID)
	if loadErr != nil {
		t.Fatalf("LoadOperation: %v", loadErr)
	}
	if after.Head.ReceiptDigest != before.ReceiptDigest || after.Head.Phase != providerexecutor.PhaseAbsencePending {
		t.Fatalf("unproven absence advanced ledger: before=%+v after=%+v", before, after.Head)
	}
}

func TestCoordinatorNeverAppendsUnauthorizedProviderResource(t *testing.T) {
	target := providerexecutor.ResourceTarget{
		BindingID: "server", Kind: "compute", NativeRef: "native-server",
		OwnershipHash: digest("owner"), Disposition: providerexecutor.DispositionDelete,
	}
	foreign := providerexecutor.ResourceBinding{
		BindingID: "foreign", Kind: "compute", NativeRef: "native-foreign",
		OwnershipHash: digest("foreign-owner"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupRequired,
	}
	executor := &queueExecutor{results: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted,
		Resources: []providerexecutor.ResourceBinding{foreign},
	}}}
	registry := NewRegistry()
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	now := contractNow
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("managed-compute")},
		Ledger:   ledger,
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := StartRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation:      providerexecutor.OperationDecommission,
		IdempotencyKey: "delete-foreign", LedgerRevision: 4,
		Targets: []providerexecutor.ResourceTarget{target}, RequestedAt: contractNow,
	}
	record, _, err := coordinator.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance accepted: %v", err)
	}
	before := record.Head
	if _, _, advanceErr := coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID); !errors.Is(advanceErr, providerexecutor.ErrInvalidReceipt) {
		t.Fatalf("foreign resource error = %v, want ErrInvalidReceipt", advanceErr)
	}
	after, err := ledger.LoadOperation(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("LoadOperation: %v", err)
	}
	if after.Head.ReceiptDigest != before.ReceiptDigest || after.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("foreign resource advanced ledger: before=%+v after=%+v", before, after.Head)
	}
}

func TestCoordinatorClaimPreventsDuplicateAdapterExecute(t *testing.T) {
	registry := NewRegistry()
	executor := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	if err := registry.Register("managed-compute", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	now := contractNow
	newCoordinator := func(owner string) *Coordinator {
		coordinator, err := newTestCoordinator(CoordinatorConfig{
			Registry: registry, Profiles: staticProfileResolver{profile: testProfile("managed-compute")}, Ledger: ledger,
			ClaimOwner: owner, ClaimTTL: time.Minute,
			Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("NewCoordinator(%s): %v", owner, err)
		}
		return coordinator
	}
	first, second := newCoordinator("worker-a"), newCoordinator("worker-b")
	record, _, err := first.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, advanced, advanceErr := first.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); advanceErr != nil || !advanced {
		t.Fatalf("coordinator-only accepted advance = %v, advanced=%v", advanceErr, advanced)
	}

	firstDone := make(chan struct {
		record   OperationRecord
		advanced bool
		err      error
	}, 1)
	tenantID, operationID := record.Command.TenantID, record.Command.OperationID
	go func() {
		advancedRecord, advanced, advanceErr := first.Advance(context.Background(), tenantID, operationID)
		firstDone <- struct {
			record   OperationRecord
			advanced bool
			err      error
		}{advancedRecord, advanced, advanceErr}
	}()
	select {
	case <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first executor did not start")
	}
	contended, advanced, err := second.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || advanced || contended.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("contended advance = %+v, advanced=%v, err=%v", contended.Head, advanced, err)
	}
	if calls := executor.CallCount(); calls != 1 {
		t.Fatalf("adapter calls while claim held = %d, want 1", calls)
	}
	close(executor.release)
	completed := <-firstDone
	if completed.err != nil || !completed.advanced || completed.record.Head.Phase != providerexecutor.PhasePlanned {
		t.Fatalf("claimed advance = %+v, advanced=%v, err=%v", completed.record.Head, completed.advanced, completed.err)
	}
	if calls := executor.CallCount(); calls != 1 {
		t.Fatalf("adapter calls after claimed append = %d, want 1", calls)
	}
}

func TestExecutionClaimRejectsStaleHeadAndWrongTokenAppend(t *testing.T) {
	ledger := newMemoryLedger()
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: testProfile("managed-compute")}, Ledger: ledger,
		ClaimOwner: "worker-a", Now: func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	staleHead := record.Head
	record, advanced, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || !advanced {
		t.Fatalf("accepted advance = %v, advanced=%v", err, advanced)
	}
	if _, claimErr := ledger.AcquireExecutionClaim(t.Context(), record.Command, staleHead, ExecutionClaimReadOnly, "worker-a", "stale", time.Minute); !errors.Is(claimErr, ErrLedgerConflict) {
		t.Fatalf("stale claim error = %v, want ErrLedgerConflict", claimErr)
	}
	claim, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-a", "right-token", time.Minute)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}
	next, err := providerexecutor.AssembleReceipt(
		t.Context(),
		providerexecutor.ExecutionRequest{Command: record.Command, Previous: record.Head},
		providerexecutor.ExecutionResult{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePlanned},
		contractNow.Add(time.Second), nil,
	)
	if err != nil {
		t.Fatalf("AssembleReceipt: %v", err)
	}
	claim.Token = "wrong-token"
	if appendErr := ledger.AppendClaimedReceipt(t.Context(), record.Command, record.Head, next, claim); !errors.Is(appendErr, ErrExecutionClaimLost) {
		t.Fatalf("wrong-token append error = %v, want ErrExecutionClaimLost", appendErr)
	}
	loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || loaded.Head.ReceiptDigest != record.Head.ReceiptDigest {
		t.Fatalf("wrong-token append changed head: loaded=%+v err=%v", loaded.Head, err)
	}
}

func TestExecutionClaimExpiryTakeoverAndRenewalLoss(t *testing.T) {
	ledger := newMemoryLedger()
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: testProfile("managed-compute")}, Ledger: ledger,
		ClaimOwner: "worker-a", Now: func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accepted advance: %v", err)
	}
	claim, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-a", "token-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}
	expiredAt := contractNow.Add(time.Minute)
	ledger.now = func() time.Time { return expiredAt }
	if _, renewErr := ledger.RenewExecutionClaim(t.Context(), claim, time.Minute); !errors.Is(renewErr, ErrExecutionClaimLost) {
		t.Fatalf("expired renewal error = %v, want ErrExecutionClaimLost", renewErr)
	}
	takeover, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-b", "token-b", time.Minute)
	if err != nil {
		t.Fatalf("expired claim takeover: %v", err)
	}
	if takeover.Token != "token-b" || takeover.Owner != "worker-b" || !takeover.ExpiresAt.After(expiredAt) {
		t.Fatalf("takeover claim = %+v", takeover)
	}
}

func TestCoordinatorReleasesClaimAfterInvalidAdapterResult(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("managed-compute", &queueExecutor{results: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
	}}}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: testProfile("managed-compute")}, Ledger: ledger,
		ClaimOwner: "worker-a", Now: func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("accepted advance: %v", err)
	}
	if _, _, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); !errors.Is(err, providerexecutor.ErrInvalidReceipt) {
		t.Fatalf("invalid adapter result error = %v, want ErrInvalidReceipt", err)
	}
	if _, err := ledger.AcquireExecutionClaim(t.Context(), record.Command, record.Head, ExecutionClaimReadOnly, "worker-b", "retry-token", time.Minute); err != nil {
		t.Fatalf("invalid adapter result stranded claim: %v", err)
	}
}

func planRequest() StartRequest {
	return StartRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation: providerexecutor.OperationPlan, IdempotencyKey: "plan-1",
		LedgerRevision: 3,
		DesiredSpec: &DesiredSpecRevision{
			TenantID: "tenant-1", LeaseID: "lease-1", Revision: 2,
			Ref:    "desired-spec://techstack/leases/lease-1/revisions/2",
			Digest: digest(`{"kit":"cloud"}`), Payload: []byte(`{"kit":"cloud"}`), CreatedAt: contractNow,
		},
		RequestedAt: contractNow,
	}
}

func provisionRequest() StartRequest {
	req := planRequest()
	req.Operation = providerexecutor.OperationProvision
	req.IdempotencyKey = "provision-1"
	return req
}

func testProfile(adapterID string) ExecutionProfile {
	handle := testCredentialHandle()
	custodyHash, _ := credentialCustodyHash("tenant-1", handle)
	connectionHash, _ := credentialConnectionHash("tenant-1", handle)
	return ExecutionProfile{
		ProviderID: "ionos", AdapterID: adapterID, CredentialMode: CredentialModeManaged,
		RuntimeProfileID: "ionos-managed-pvm-monthly", OfferingID: "monthly-runtime-standard",
		CatalogVersion: "catalog-2026-07-21", CapabilitySnapshotHash: digest("capabilities"),
		AdapterManifestHash:   digest("test-adapter-manifest"),
		ProvisionDispatchMode: ProvisionDispatchNativeIdempotency,
		CustodyRef:            handle.CustodyRef, CustodyHash: custodyHash,
		ConnectionRef: handle.ConnectionRef, ConnectionHash: connectionHash,
		ExecutionProfileHash: digest("profile"),
	}
}

func testCredentialHandle() credentialHandleRow {
	return credentialHandleRow{
		HandleID: "provider-access", HandleVersion: 4, ProviderID: "ionos",
		CredentialMode: CredentialModeManaged, SubjectKind: "org", SubjectID: "tenant-1",
		GrantID: "managed-compute", Scope: "compute",
		CustodyRef:    "custody://tenant-1/provider-access",
		ConnectionRef: "provider-connection://tenant-1/managed-compute",
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type staticProfileResolver struct {
	profile ExecutionProfile
	err     error
}

func (r staticProfileResolver) ResolveExecutionProfile(context.Context, ProfileRequest) (ExecutionProfile, error) {
	return r.profile, r.err
}

type queueExecutor struct {
	mu            sync.Mutex
	results       []providerexecutor.ExecutionResult
	calls         int
	readOnlyCalls int
	mutationCalls int
}

type blockingExecutor struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	calls   int
}

type atMostOnceExecutor struct {
	mu              sync.Mutex
	dispatchResults []providerexecutor.ExecutionResult
	pollResults     []providerexecutor.ExecutionResult
	dispatchEntered chan struct{}
	dispatchRelease chan struct{}
	prepareErr      error
	prepareCalls    int
	dispatchCalls   int
	pollCalls       int
}

type testPreparedProvision struct {
	binding PreparedProvisionBinding
}

func (p *testPreparedProvision) ProvisionBinding() PreparedProvisionBinding { return p.binding }

func (*atMostOnceExecutor) AtMostOnceProvisionCapability() AtMostOnceProvisionCapability {
	return AtMostOnceProvisionCapability{
		AdapterManifestHash:          digest("test-adapter-manifest"),
		PerHeadInvocationKey:         true,
		SideEffectFreePreparation:    true,
		PreparedRequestDigestBinding: true,
		ReadOnlyProvisionPolling:     true,
		ReadOnlyGeneralObservation:   true,
		ExactHandleDecommission:      true,
		ReadOnlyAbsencePolling:       true,
	}
}

func (e *atMostOnceExecutor) PrepareProvision(_ context.Context, invocation AdapterInvocation) (PreparedProvisionRequest, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prepareCalls++
	if e.prepareErr != nil {
		return nil, e.prepareErr
	}
	return preparedProvisionForInvocation(invocation), nil
}

func (e *atMostOnceExecutor) DispatchPreparedProvision(_ context.Context, _ PreparedProvisionRequest, _ ExecutePermit) providerexecutor.ExecutionResult {
	e.mu.Lock()
	e.dispatchCalls++
	callNumber := e.dispatchCalls
	entered := e.dispatchEntered
	release := e.dispatchRelease
	result := providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending,
		Phase:  providerexecutor.PhaseAccepted,
	}
	if len(e.dispatchResults) == 0 {
		e.mu.Unlock()
	} else {
		result = e.dispatchResults[0]
		e.dispatchResults = e.dispatchResults[1:]
		e.mu.Unlock()
	}
	if callNumber == 1 && entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return result
}

func preparedProvisionForInvocation(invocation AdapterInvocation) *testPreparedProvision {
	return &testPreparedProvision{binding: PreparedProvisionBinding{
		RequestDigest:         sha256Digest([]byte("canonical-provider-create\x00" + invocation.Key)),
		CredentialVersionHash: invocation.Request.Command.CustodyHash,
		ProviderScopeHash:     invocation.Request.Command.ConnectionHash,
		CorrelationHash:       sha256Digest([]byte(invocation.CorrelationID)),
		AdapterManifestHash:   digest("test-adapter-manifest"),
	}}
}

func preparedBindingForCommandHead(
	t testing.TB,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) PreparedProvisionBinding {
	t.Helper()
	invocation, err := newAdapterInvocation(providerexecutor.ExecutionRequest{Command: command, Previous: head})
	if err != nil {
		t.Fatalf("newAdapterInvocation: %v", err)
	}
	return preparedProvisionForInvocation(invocation).ProvisionBinding()
}

func (e *atMostOnceExecutor) PollProvisionPresence(_ context.Context, _ AdapterInvocation) providerexecutor.ExecutionResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pollCalls++
	if len(e.pollResults) == 0 {
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound}
	}
	result := e.pollResults[0]
	e.pollResults = e.pollResults[1:]
	return result
}

func (*atMostOnceExecutor) ObserveReadOnly(_ context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: invocation.Request.Previous.Phase}
}

func (*atMostOnceExecutor) DecommissionExactHandles(_ context.Context, _ AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted}
}

func (*atMostOnceExecutor) PollDecommissionAbsence(_ context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: invocation.Request.Previous.Phase, Resources: invocation.Request.Previous.Resources}
}

func (*blockingExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryNativeIdempotency, PerHeadInvocationKey: true,
	}
}

func (e *blockingExecutor) ExecuteCrashRecoverableReadOnly(ctx context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *blockingExecutor) ExecuteCrashRecoverableMutation(ctx context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *blockingExecutor) execute(context.Context, providerexecutor.ExecutionRequest) providerexecutor.ExecutionResult {
	e.mu.Lock()
	e.calls++
	if e.calls == 1 {
		close(e.entered)
	}
	e.mu.Unlock()
	<-e.release
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePlanned}
}

func (e *blockingExecutor) CallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *queueExecutor) execute(access ExecutionClaimAccess) providerexecutor.ExecutionResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if access == ExecutionClaimReadOnly {
		e.readOnlyCalls++
	} else {
		e.mutationCalls++
	}
	if len(e.results) == 0 {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed,
			Phase:  providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTransient, Retryable: true},
		}
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result
}

func (*queueExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryNativeIdempotency, PerHeadInvocationKey: true,
	}
}

func (e *queueExecutor) ExecuteCrashRecoverableReadOnly(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ExecutionClaimReadOnly)
}

func (e *queueExecutor) ExecuteCrashRecoverableMutation(context.Context, AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ExecutionClaimSideEffecting)
}

type memoryLedger struct {
	mu             sync.Mutex
	records        map[string]OperationRecord
	claims         map[string]memoryClaim
	dispatchGuards map[string]struct{}
	beginCalls     int
	now            func() time.Time
}

type memoryClaim struct {
	claim  ExecutionClaim
	active bool
}

func newMemoryLedger() *memoryLedger {
	return &memoryLedger{
		records: make(map[string]OperationRecord), claims: make(map[string]memoryClaim), dispatchGuards: make(map[string]struct{}),
		now: func() time.Time { return contractNow },
	}
}

func (l *memoryLedger) BeginOperation(
	_ context.Context,
	authority ExecutionAuthority,
	profile ExecutionProfileSnapshot,
	provisionDispatch ProvisionDispatchMode,
	command providerexecutor.Command,
	initial providerexecutor.Receipt,
	_ *DesiredSpecRevision,
) (OperationRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.beginCalls++
	if existing, ok := l.records[command.OperationID]; ok {
		if err := providerexecutor.ValidateReplay(existing.Command, command); err != nil {
			return OperationRecord{}, false, err
		}
		return existing, false, nil
	}
	for _, existing := range l.records {
		if existing.Command.TenantID == command.TenantID &&
			existing.Command.LeaseID == command.LeaseID &&
			existing.Command.Operation == command.Operation &&
			existing.Command.IdempotencyKey == command.IdempotencyKey {
			if err := providerexecutor.ValidateReplay(existing.Command, command); err != nil {
				return OperationRecord{}, false, err
			}
			return existing, false, nil
		}
	}
	record := OperationRecord{
		ExecutionAuthority: authority, ExecutionProfile: profile,
		ProvisionDispatch: provisionDispatch, AutomationState: OperationAutomationRunnable,
		Command: command, Head: initial,
	}
	l.records[command.OperationID] = record
	return record, true, nil
}

func (l *memoryLedger) LoadOperation(_ context.Context, tenantID, operationID string) (OperationRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[operationID]
	if !ok || record.Command.TenantID != tenantID {
		return OperationRecord{}, ErrOperationNotFound
	}
	guarded := false
	for guardedOperationID := range l.dispatchGuards {
		guardedRecord := l.records[guardedOperationID]
		if guardedRecord.Command.TenantID == record.Command.TenantID &&
			guardedRecord.Command.LeaseID == record.Command.LeaseID &&
			guardedRecord.Command.ResourceGenerationID == record.Command.ResourceGenerationID &&
			(guardedOperationID != operationID ||
				guardedRecord.ProvisionDispatch == ProvisionDispatchAtMostOnceManualReconcile) {
			guarded = true
			break
		}
	}
	record.AutomationState = deriveOperationAutomationState(record, guarded)
	record.AutomationReasonCode = deriveOperationAutomationReasonCode(record, guarded)
	return record, nil
}

func (l *memoryLedger) AppendReceipt(_ context.Context, command providerexecutor.Command, previous, next providerexecutor.Receipt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if previous.Phase != providerexecutor.PhaseRequested || next.Phase != providerexecutor.PhaseAccepted {
		return ErrExecutionClaimNeeded
	}
	record, ok := l.records[command.OperationID]
	if !ok {
		return ErrOperationNotFound
	}
	if record.Head.ReceiptDigest != previous.ReceiptDigest || record.Head.Sequence != previous.Sequence {
		return ErrLedgerConflict
	}
	record.Head = next
	record.AutomationState = deriveOperationAutomationState(record, false)
	record.AutomationReasonCode = deriveOperationAutomationReasonCode(record, false)
	l.records[command.OperationID] = record
	return nil
}

func (l *memoryLedger) AcquireExecutionClaim(_ context.Context, command providerexecutor.Command, head providerexecutor.Receipt, access ExecutionClaimAccess, owner, token string, ttl time.Duration) (ExecutionClaim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	record, ok := l.records[command.OperationID]
	if !ok || record.Command.TenantID != command.TenantID {
		return ExecutionClaim{}, ErrOperationNotFound
	}
	if record.Head.Sequence != head.Sequence || record.Head.ReceiptDigest != head.ReceiptDigest {
		return ExecutionClaim{}, ErrLedgerConflict
	}
	if record.Command.Operation == providerexecutor.OperationProvision && record.Head.Phase == providerexecutor.PhaseAccepted {
		custodyExists := false
		for guardedOperationID := range l.dispatchGuards {
			guardedRecord := l.records[guardedOperationID]
			if guardedRecord.Command.TenantID == command.TenantID &&
				guardedRecord.Command.LeaseID == command.LeaseID &&
				guardedRecord.Command.ResourceGenerationID == command.ResourceGenerationID {
				if guardedOperationID != command.OperationID ||
					guardedRecord.ProvisionDispatch != record.ProvisionDispatch ||
					record.ProvisionDispatch == ProvisionDispatchAtMostOnceManualReconcile {
					return ExecutionClaim{}, ErrProvisionManualReconcile
				}
				custodyExists = true
			}
		}
		if record.ProvisionDispatch == ProvisionDispatchAtMostOnceManualReconcile {
			return ExecutionClaim{}, ErrDispatchPermitNeeded
		}
		if !custodyExists {
			l.dispatchGuards[command.OperationID] = struct{}{}
		}
	}
	if existing, ok := l.claims[command.OperationID]; ok && existing.active && existing.claim.ExpiresAt.After(now) {
		return ExecutionClaim{}, ErrExecutionClaimHeld
	}
	claim := ExecutionClaim{
		TenantID: command.TenantID, OperationID: command.OperationID,
		ResourceGenerationID: command.ResourceGenerationID,
		HeadSequence:         head.Sequence, HeadReceiptDigest: head.ReceiptDigest,
		Access: access, Owner: owner, Token: token, ExpiresAt: now.Add(ttl),
	}
	l.claims[command.OperationID] = memoryClaim{claim: claim, active: true}
	return claim, nil
}

func (l *memoryLedger) AcquireProvisionDispatchClaim(
	_ context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
	binding PreparedProvisionBinding,
	owner, token string,
	ttl time.Duration,
) (ProvisionDispatchGrant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	record, ok := l.records[command.OperationID]
	if !ok || record.Command.TenantID != command.TenantID {
		return ProvisionDispatchGrant{}, ErrOperationNotFound
	}
	if record.Head.Sequence != head.Sequence || record.Head.ReceiptDigest != head.ReceiptDigest {
		return ProvisionDispatchGrant{}, ErrLedgerConflict
	}
	if command.Operation != providerexecutor.OperationProvision ||
		record.ProvisionDispatch != ProvisionDispatchAtMostOnceManualReconcile ||
		head.Status != providerexecutor.StatusPending || head.Phase != providerexecutor.PhaseAccepted || len(head.Resources) != 0 {
		return ProvisionDispatchGrant{}, ErrDispatchPermitNeeded
	}
	invocation, err := newAdapterInvocation(providerexecutor.ExecutionRequest{Command: command, Previous: head})
	if err != nil {
		return ProvisionDispatchGrant{}, err
	}
	if err := validatePreparedProvisionBinding(binding, invocation, record.ExecutionProfile.AdapterManifestHash); err != nil {
		return ProvisionDispatchGrant{}, err
	}
	if _, guarded := l.dispatchGuards[command.OperationID]; guarded {
		return ProvisionDispatchGrant{}, ErrProvisionManualReconcile
	}
	for guardedOperationID := range l.dispatchGuards {
		guardedRecord := l.records[guardedOperationID]
		if guardedRecord.Command.TenantID == command.TenantID &&
			guardedRecord.Command.LeaseID == command.LeaseID &&
			guardedRecord.Command.ResourceGenerationID == command.ResourceGenerationID {
			return ProvisionDispatchGrant{}, ErrProvisionManualReconcile
		}
	}
	if existing, exists := l.claims[command.OperationID]; exists && existing.active && existing.claim.ExpiresAt.After(now) {
		return ProvisionDispatchGrant{}, ErrExecutionClaimHeld
	}
	claim := ExecutionClaim{
		TenantID: command.TenantID, OperationID: command.OperationID,
		ResourceGenerationID: command.ResourceGenerationID,
		HeadSequence:         head.Sequence, HeadReceiptDigest: head.ReceiptDigest,
		Access: ExecutionClaimSideEffecting, Owner: owner, Token: token, ExpiresAt: now.Add(ttl),
	}
	l.dispatchGuards[command.OperationID] = struct{}{}
	l.claims[command.OperationID] = memoryClaim{claim: claim, active: true}
	return ProvisionDispatchGrant{Claim: claim, Permit: newExecutePermit(claim, binding)}, nil
}

func (l *memoryLedger) RenewExecutionClaim(_ context.Context, claim ExecutionClaim, ttl time.Duration) (ExecutionClaim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	existing, ok := l.claims[claim.OperationID]
	if !ok || !existing.active || existing.claim.Token != claim.Token || existing.claim.Owner != claim.Owner || !existing.claim.ExpiresAt.After(now) {
		return ExecutionClaim{}, ErrExecutionClaimLost
	}
	existing.claim.ExpiresAt = now.Add(ttl)
	l.claims[claim.OperationID] = existing
	return existing.claim, nil
}

func (l *memoryLedger) ReleaseExecutionClaim(_ context.Context, claim ExecutionClaim) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	existing, ok := l.claims[claim.OperationID]
	if !ok || !existing.active || existing.claim.Token != claim.Token || existing.claim.Owner != claim.Owner || !existing.claim.ExpiresAt.After(now) {
		return ErrExecutionClaimLost
	}
	existing.active = false
	l.claims[claim.OperationID] = existing
	return nil
}

func (l *memoryLedger) AppendClaimedReceipt(_ context.Context, command providerexecutor.Command, previous, next providerexecutor.Receipt, claim ExecutionClaim) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	record, ok := l.records[command.OperationID]
	if !ok {
		return ErrOperationNotFound
	}
	existing, ok := l.claims[command.OperationID]
	if !ok || !existing.active || existing.claim.Token != claim.Token || existing.claim.Owner != claim.Owner ||
		!existing.claim.ExpiresAt.After(now) || existing.claim.HeadSequence != previous.Sequence || existing.claim.HeadReceiptDigest != previous.ReceiptDigest {
		return ErrExecutionClaimLost
	}
	if record.Head.ReceiptDigest != previous.ReceiptDigest || record.Head.Sequence != previous.Sequence {
		return ErrLedgerConflict
	}
	record.Head = next
	record.AutomationState = deriveOperationAutomationState(record, false)
	l.records[command.OperationID] = record
	existing.active = false
	l.claims[command.OperationID] = existing
	return nil
}

var _ Ledger = (*memoryLedger)(nil)
