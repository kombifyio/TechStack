package providercontrol

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// teardownLedger is a memoryLedger that also offers resource-free teardown, so
// the coordinator's ResourceFreeTeardownFinalizer branch is exercised.
type teardownLedger struct {
	*memoryLedger
	calls    int
	lastHead providerexecutor.Receipt
	finalize bool
}

func (l *teardownLedger) FinalizeResourceFreeTeardown(
	_ context.Context,
	command providerexecutor.Command,
	head providerexecutor.Receipt,
) (OperationRecord, bool, error) {
	l.calls++
	l.lastHead = head
	// The real finalizer commits only when the database proves no provider
	// resource can exist, which for an in-flight head it cannot. Modelling that
	// keeps this double from short-circuiting the normal dispatch flow.
	if !l.finalize || head.Status != providerexecutor.StatusFailed {
		return OperationRecord{}, false, nil
	}
	record, err := l.memoryLedger.LoadOperation(context.Background(), command.TenantID, command.OperationID)
	if err != nil {
		return OperationRecord{}, false, err
	}
	return record, true, nil
}

func newTerminalFailedProvision(t *testing.T, ledger Ledger) OperationRecord {
	t.Helper()
	executor := &atMostOnceExecutor{dispatchResults: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
		Reason: &providerexecutor.Reason{
			Code: providerexecutor.ReasonCodeProviderPartialCreate, Retryable: false,
		},
	}}}
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
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
	failed, _, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if failed.Head.Status != providerexecutor.StatusFailed || len(failed.Head.Resources) != 0 {
		t.Fatalf("expected a resource-free terminal failure, got %#v", failed.Head)
	}
	// Return the coordinator's view via a second Advance below; the caller owns it.
	t.Cleanup(func() {})
	return failed
}

// A provision that fails before binding any resource still holds its
// managed-runtime capacity reservation. Capacity is released only by a
// decommission (which needs a provisioned server) or by resource-free teardown,
// so a terminal head must still be offered to the teardown finalizer -- else the
// slot leaks permanently and, after `limit` failures, the owner can never create
// another managed server.
func TestTerminalFailedProvisionIsOfferedToResourceFreeTeardown(t *testing.T) {
	ledger := &teardownLedger{memoryLedger: newMemoryLedger()}
	failed := newTerminalFailedProvision(t, ledger)

	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", &atMostOnceExecutor{}); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	before := ledger.calls
	replay, advanced, err := coordinator.Advance(
		t.Context(), failed.Command.TenantID, failed.Command.OperationID,
	)
	if err != nil {
		t.Fatalf("Advance on terminal head: %v", err)
	}
	if ledger.calls == before {
		t.Fatal("terminal failed provision was never offered to resource-free teardown; its capacity reservation leaks forever")
	}
	if ledger.lastHead.Status != providerexecutor.StatusFailed {
		t.Fatalf("teardown was offered %q, want the terminal failed head", ledger.lastHead.Status)
	}
	// The finalizer declined, so the terminal record is returned unchanged.
	if advanced || replay.Head.ReceiptDigest != failed.Head.ReceiptDigest {
		t.Fatalf("declined teardown must leave the terminal head intact: advanced=%v head=%#v", advanced, replay.Head)
	}
}

// When the finalizer accepts, Advance reports the finalized record so the
// caller can observe that the slot was reclaimed.
func TestAcceptedResourceFreeTeardownAdvancesTerminalProvision(t *testing.T) {
	ledger := &teardownLedger{memoryLedger: newMemoryLedger(), finalize: true}
	failed := newTerminalFailedProvision(t, ledger)

	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", &atMostOnceExecutor{}); err != nil {
		t.Fatalf("RegisterAtMostOnceProvision: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	_, advanced, err := coordinator.Advance(
		t.Context(), failed.Command.TenantID, failed.Command.OperationID,
	)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !advanced {
		t.Fatal("accepted resource-free teardown must be reported as an advance")
	}
}
