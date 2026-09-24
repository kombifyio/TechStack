package providerevidence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// acceptingVerifier isolates the envelope shape from the store. The question
// this file answers is whether the evidence the adapter now mints satisfies the
// wire contract at all; whether a stored observation backs it is the Verifier's
// job and is covered separately.
type acceptingVerifier struct{ calls int }

func (v *acceptingVerifier) VerifyEvidence(
	context.Context, providerexecutor.Command, providerexecutor.ResourceTarget, providerexecutor.Evidence,
) error {
	v.calls++
	return nil
}

// refusingVerifier stands in for the FailClosedEvidenceVerifier this work
// replaces.
type refusingVerifier struct{}

var errRefused = errors.New("refused")

func (refusingVerifier) VerifyEvidence(
	context.Context, providerexecutor.Command, providerexecutor.ResourceTarget, providerexecutor.Evidence,
) error {
	return errRefused
}

// sealedDecommission builds a real sealed command whose single target is the
// datacenter, plus the delete-accepted head an absence receipt must follow.
func sealedDecommission(t *testing.T) (providerexecutor.Command, providerexecutor.Receipt) {
	t.Helper()
	target := datacenterTarget()
	command, err := providerexecutor.SealCommand(providerexecutor.Command{
		Operation:              providerexecutor.OperationDecommission,
		TenantID:               "auth0|6a4957c8a4a480ce95c4290e",
		LeaseID:                "lease-53ce833757725c03e4fb93029c3073d4",
		LeaseRevision:          1,
		RuntimeServerID:        "server_395c714cbef340a18c96ca61",
		ResourceGenerationID:   "601dc403-e959-4802-b0b9-b68ea46bf67a",
		IdempotencyKey:         "native-decommission:lease-53ce8337:601dc403:retry-1",
		ProviderID:             "ionos",
		AdapterID:              "ionos-cloudapi-v6",
		CustodyRef:             "custody://render/ionos/provider-bundle",
		ConnectionRef:          "provider-connection://ionos/dcd/de-fra",
		CapabilitySnapshotHash: providerexecutor.ComputeNativeRefHash("capability"),
		ExecutionProfileHash:   providerexecutor.ComputeNativeRefHash("profile"),
		CustodyHash:            providerexecutor.ComputeNativeRefHash("custody"),
		ConnectionHash:         providerexecutor.ComputeNativeRefHash("connection"),
		LedgerRevision:         3,
		Targets:                []providerexecutor.ResourceTarget{target},
		RequestedAt:            time.Date(2026, 7, 27, 13, 9, 40, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SealCommand: %v", err)
	}
	issuedAt := command.RequestedAt.Add(time.Second)
	head, err := providerexecutor.InitialReceipt(command, issuedAt)
	if err != nil {
		t.Fatalf("InitialReceipt: %v", err)
	}
	// The contract deliberately forbids skipping absence_pending during a
	// decommission (providerexecutor CanTransition), so the head an absence
	// receipt may follow is absence_pending, never accepted or delete_accepted.
	// Both adapters used to return absent straight from those earlier phases,
	// which failed with "invalid decommission transition".
	for _, step := range []providerexecutor.ExecutionResult{
		{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted},
		{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted,
			Resources: heldBindings(target),
		},
		{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending,
			Resources: heldBindings(target),
		},
	} {
		issuedAt = issuedAt.Add(time.Second)
		next, assembleErr := providerexecutor.AssembleReceipt(
			context.Background(),
			providerexecutor.ExecutionRequest{Command: command, Previous: head},
			step, issuedAt, &acceptingVerifier{},
		)
		if assembleErr != nil {
			t.Fatalf("AssembleReceipt(%s): %v", step.Phase, assembleErr)
		}
		head = next
	}
	return command, head
}

func heldBindings(target providerexecutor.ResourceTarget) []providerexecutor.ResourceBinding {
	return []providerexecutor.ResourceBinding{{
		BindingID:       target.BindingID,
		Kind:            target.Kind,
		NativeRef:       target.NativeRef,
		ParentBindingID: target.ParentBindingID,
		OwnershipHash:   target.OwnershipHash,
		Disposition:     target.Disposition,
		Observation:     providerexecutor.ObservationPresent,
		Cleanup:         providerexecutor.CleanupRequired,
	}}
}

// absentBindings collects the observation at collectedAt, which the contract
// requires to fall between the command's request time and the receipt's issue
// time. The adapter satisfies this naturally: it reads the provider before the
// coordinator stamps the receipt.
func absentBindings(
	t *testing.T,
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
	collectedAt time.Time,
) []providerexecutor.ResourceBinding {
	t.Helper()
	observed := notFoundObservation()
	observed.CollectedAt = collectedAt
	evidence, _, err := buildAbsenceEvidence(command, target, observed)
	if err != nil {
		t.Fatalf("buildAbsenceEvidence: %v", err)
	}
	bindings := heldBindings(target)
	bindings[0].Observation = providerexecutor.ObservationAbsent
	bindings[0].Cleanup = providerexecutor.CleanupComplete
	bindings[0].Evidence = []providerexecutor.Evidence{evidence}
	return bindings
}

// This is the whole point of the change, run against the real contract rather
// than a description of it: an absent receipt carrying this evidence seals.
//
// The adapter previously returned the same receipt with an empty Evidence
// slice, which AssembleReceipt rejects with ErrAbsenceProofRequired. That is why
// no decommission ever converged and why 0 of 115 managed-runtime capacity
// reservations had ever been released as of 2026-07-27.
func TestAbsentReceiptWithRecordedEvidenceSeals(t *testing.T) {
	command, head := sealedDecommission(t)
	verifier := &acceptingVerifier{}

	receipt, err := providerexecutor.AssembleReceipt(
		context.Background(),
		providerexecutor.ExecutionRequest{Command: command, Previous: head},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent,
			Resources: absentBindings(t, command, datacenterTarget(), head.IssuedAt),
		},
		head.IssuedAt.Add(time.Second), verifier,
	)
	if err != nil {
		t.Fatalf("an absence receipt with recorded provider evidence was rejected: %v", err)
	}
	if receipt.Phase != providerexecutor.PhaseAbsent || receipt.Status != providerexecutor.StatusSucceeded {
		t.Fatalf("receipt = %s/%s, want succeeded/absent", receipt.Status, receipt.Phase)
	}
	if verifier.calls == 0 {
		t.Fatal("the evidence verifier was never consulted; the proof is decorative")
	}
}

// The regression this replaces, pinned so it cannot come back: no evidence, no
// seal.
func TestAbsentReceiptWithoutEvidenceIsStillRejected(t *testing.T) {
	command, head := sealedDecommission(t)
	bare := heldBindings(datacenterTarget())
	bare[0].Observation = providerexecutor.ObservationAbsent
	bare[0].Cleanup = providerexecutor.CleanupComplete

	_, err := providerexecutor.AssembleReceipt(
		context.Background(),
		providerexecutor.ExecutionRequest{Command: command, Previous: head},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent,
			Resources: bare,
		},
		head.IssuedAt.Add(time.Second), &acceptingVerifier{},
	)
	if !errors.Is(err, providerexecutor.ErrAbsenceProofRequired) {
		t.Fatalf("error = %v, want ErrAbsenceProofRequired", err)
	}
}

// A refusing verifier must still block the seal. Minting evidence does not by
// itself make an absence provable; the verifier remains the gate, which is what
// makes replacing the fail-closed placeholder a real decision rather than a
// bypass.
func TestARefusingVerifierStillBlocksTheSeal(t *testing.T) {
	command, head := sealedDecommission(t)

	_, err := providerexecutor.AssembleReceipt(
		context.Background(),
		providerexecutor.ExecutionRequest{Command: command, Previous: head},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent,
			Resources: absentBindings(t, command, datacenterTarget(), head.IssuedAt),
		},
		head.IssuedAt.Add(time.Second), refusingVerifier{},
	)
	if err == nil {
		t.Fatal("a refused envelope sealed anyway")
	}
}

// Evidence minted for a different command must not seal this one, even though
// it is otherwise well formed. This is the replay case the subject hash binds.
func TestEvidenceFromAnotherCommandDoesNotSeal(t *testing.T) {
	command, head := sealedDecommission(t)
	foreign := decommissionCommand()
	foreign.OperationID = "op_0000000000000000000000000000000f"

	bindings := absentBindings(t, foreign, datacenterTarget(), head.IssuedAt)
	_, err := providerexecutor.AssembleReceipt(
		context.Background(),
		providerexecutor.ExecutionRequest{Command: command, Previous: head},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent,
			Resources: bindings,
		},
		head.IssuedAt.Add(time.Second), &acceptingVerifier{},
	)
	if err == nil {
		t.Fatal("evidence bound to a different command sealed this one")
	}
}

// The transition rule the adapters now respect, pinned directly: absence may
// only be declared from absence_pending. Without this step the recorded
// evidence is irrelevant, because the receipt is refused before the proof is
// even read.
func TestAbsenceMayOnlyBeDeclaredFromAbsencePending(t *testing.T) {
	for _, from := range []providerexecutor.Phase{
		providerexecutor.PhaseAccepted,
		providerexecutor.PhaseDeleteAccepted,
	} {
		if providerexecutor.CanTransition(
			providerexecutor.OperationDecommission, from, providerexecutor.PhaseAbsent) {
			t.Fatalf("%s -> absent is allowed; the adapters may skip the absence step", from)
		}
	}
	if !providerexecutor.CanTransition(providerexecutor.OperationDecommission,
		providerexecutor.PhaseAbsencePending, providerexecutor.PhaseAbsent) {
		t.Fatal("absence_pending -> absent is refused; no decommission could ever converge")
	}
}
