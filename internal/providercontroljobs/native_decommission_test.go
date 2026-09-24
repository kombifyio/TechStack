package providercontroljobs

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"

	"github.com/kombifyio/techstack/internal/providercontrol"
)

type unavailableProvisionResolution struct{}

func (unavailableProvisionResolution) ResolveProvisionOperation(
	context.Context, providercontrol.ProvisionResolutionWorkflowRequest,
) (providercontrol.ProvisionResolutionWorkflowResult, error) {
	return providercontrol.ProvisionResolutionWorkflowResult{}, errors.New("unexpected provision resolution")
}

type recordingProvisionResolution struct {
	requests []providercontrol.ProvisionResolutionWorkflowRequest
}

func (r *recordingProvisionResolution) ResolveProvisionOperation(
	_ context.Context, request providercontrol.ProvisionResolutionWorkflowRequest,
) (providercontrol.ProvisionResolutionWorkflowResult, error) {
	r.requests = append(r.requests, request)
	return providercontrol.ProvisionResolutionWorkflowResult{}, nil
}

type resourceFreeFinalizerSequence struct {
	calls    int
	terminal providercontrol.OperationRecord
}

func (f *resourceFreeFinalizerSequence) FinalizeResourceFreeTeardown(
	context.Context, providerexecutor.Command, providerexecutor.Receipt,
) (providercontrol.OperationRecord, bool, error) {
	f.calls++
	return f.terminal, f.calls == 2, nil
}

func TestNativeDecommissionIdempotencyKeyPinsLeaseGeneration(t *testing.T) {
	candidate := nativeDecommissionCandidate{
		LeaseID:            "lease-1",
		ResourceGeneration: "11111111-1111-4111-8111-111111111111",
		ServerRevision:     7,
	}
	first := nativeDecommissionIdempotencyKey(candidate)
	candidate.ServerRevision = 8
	if replay := nativeDecommissionIdempotencyKey(candidate); replay != first {
		t.Fatalf("idempotency key changed with mutable server revision: got %q want %q", replay, first)
	}
	candidate.ResourceGeneration = "22222222-2222-4222-8222-222222222222"
	if replacement := nativeDecommissionIdempotencyKey(candidate); replacement == first {
		t.Fatal("replacement resource generation reused the prior decommission idempotency key")
	}
}

func TestNativeDecommissionTerminalRequiresDefinitiveAbsence(t *testing.T) {
	base := providercontrol.OperationRecord{
		Command: providerexecutor.Command{Operation: providerexecutor.OperationDecommission},
		Head: providerexecutor.Receipt{
			Status: providerexecutor.StatusSucceeded,
			Phase:  providerexecutor.PhaseAbsent,
		},
	}
	if !nativeDecommissionTerminal(base) {
		t.Fatal("definitive provider absence was not terminal")
	}
	cases := []providercontrol.OperationRecord{
		{
			Command: providerexecutor.Command{Operation: providerexecutor.OperationProvision},
			Head:    base.Head,
		},
		{
			Command: base.Command,
			Head: providerexecutor.Receipt{
				Status: providerexecutor.StatusPending,
				Phase:  providerexecutor.PhaseAbsencePending,
			},
		},
		{
			Command: base.Command,
			Head: providerexecutor.Receipt{
				Status: providerexecutor.StatusPending,
				Phase:  providerexecutor.PhaseDeleteAccepted,
			},
		},
	}
	for _, record := range cases {
		if nativeDecommissionTerminal(record) {
			t.Fatalf("non-terminal provider state accepted: operation=%s status=%s phase=%s",
				record.Command.Operation, record.Head.Status, record.Head.Phase)
		}
	}
}

func TestNewNativeDecommissionerFailsClosedWithoutSharedAuthorities(t *testing.T) {
	if _, err := NewNativeDecommissioner(NativeDecommissionConfig{}); err == nil {
		t.Fatal("constructor accepted missing database/application/ledger authorities")
	}
}

func TestFailedResourceFreeProvisionResolvesInsteadOfEmptyDecommission(t *testing.T) {
	provision := providercontrol.OperationRecord{
		Command: providerexecutor.Command{
			TenantID:    "tenant",
			OperationID: "op-failed-provision",
			Operation:   providerexecutor.OperationProvision,
		},
		Head: providerexecutor.Receipt{
			Status: providerexecutor.StatusFailed,
			Phase:  providerexecutor.PhaseFailed,
		},
	}
	if !resourceFreeProvisionCanFinalize(provision) {
		t.Fatal("failed resource-free provision must stay on provision resolution, not empty-target decommission")
	}
	resolution := &recordingProvisionResolution{}
	finalizer := &resourceFreeFinalizerSequence{terminal: provision}
	_, handled, err := convergeResourceFreeProvision(
		t.Context(), provision, finalizer, resolution, "test:provider-control-worker",
	)
	if err != nil || !handled {
		t.Fatalf("failed resource-free provision must resolve then finalize: handled=%v err=%v", handled, err)
	}
	if len(resolution.requests) != 1 ||
		resolution.requests[0].Confirmation != "adopt-provision:op-failed-provision" {
		t.Fatalf("resolution=%+v", resolution.requests)
	}
}

func TestResourceFreeConvergenceResolvesCertifiedAbsenceBeforeFinalizing(t *testing.T) {
	source := providercontrol.OperationRecord{Command: providerexecutor.Command{
		TenantID: "tenant-1", OperationID: "operation-1",
	}}
	finalizer := &resourceFreeFinalizerSequence{terminal: source}
	resolution := &recordingProvisionResolution{}
	terminal, handled, err := convergeResourceFreeProvision(
		t.Context(), source, finalizer, resolution, "system:provider-control-worker",
	)
	if err != nil || !handled || terminal.Command.OperationID != source.Command.OperationID {
		t.Fatalf("terminal=%+v handled=%v error=%v", terminal, handled, err)
	}
	if finalizer.calls != 2 || len(resolution.requests) != 1 ||
		resolution.requests[0].Confirmation != "adopt-provision:operation-1" {
		t.Fatalf("finalizer calls=%d resolution=%+v", finalizer.calls, resolution.requests)
	}
}
