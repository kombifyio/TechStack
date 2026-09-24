package providercontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestProvisionResolutionDerivesZeroOneManyWithoutOperatorHandles(t *testing.T) {
	for _, test := range []struct {
		name       string
		candidates int
		want       ProvisionResolutionOutcome
	}{
		{name: "zero remains blocked", candidates: 0, want: ProvisionResolutionNoCandidateObserved},
		{name: "one exact graph is adopted", candidates: 1, want: ProvisionResolutionAdoptedExactCandidate},
		{name: "many are quarantined", candidates: 2, want: ProvisionResolutionMultipleCandidatesQuarantined},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, binding := guardedProvisionResolutionRecord(t)
			request := provisionDiscoveryRequest(record, binding, test.candidates)
			observation, err := sealProvisionDiscoveryObservation(
				t.Context(), record, binding, request, allowProvisionResolutionVerifier{},
			)
			if err != nil {
				t.Fatalf("sealProvisionDiscoveryObservation: %v", err)
			}
			if len(observation.Candidates) != test.candidates || observation.SnapshotDigest == "" || observation.RequestDigest == "" {
				t.Fatalf("observation = %+v", observation)
			}
			decisionRequest, err := sealProvisionResolutionRequest(ProvisionResolutionRequest{
				TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
				ObservationID: observation.ObservationID, ObservationSnapshotDigest: observation.SnapshotDigest,
				ExpectedHeadSequence: record.Head.Sequence, ExpectedHeadReceiptDigest: record.Head.ReceiptDigest,
				ExpectedResolutionRevision: binding.ResolutionRevision,
				IdempotencyKey:             "decision-1", OperatorSubjectID: "operator-1",
				OperatorAttestationRef:    "provider-attestation://techstack/operators/decision-1",
				OperatorAttestationDigest: digest("operator-decision-1"),
			})
			if err != nil {
				t.Fatalf("sealProvisionResolutionRequest: %v", err)
			}
			decision, err := deriveProvisionResolutionDecision(
				record,
				decisionRequest,
				observation,
				request.CollectedAt.Add(time.Second),
			)
			if err != nil {
				t.Fatalf("deriveProvisionResolutionDecision: %v", err)
			}
			if decision.Outcome != test.want {
				t.Fatalf("outcome = %q, want %q", decision.Outcome, test.want)
			}
			if test.candidates == 1 && decision.SelectedCandidateDigest != observation.Candidates[0].GraphDigest {
				t.Fatalf("selected digest = %q, want sole candidate", decision.SelectedCandidateDigest)
			}
			if test.candidates != 1 && decision.SelectedCandidateDigest != "" {
				t.Fatalf("non-adoption selected candidate %q", decision.SelectedCandidateDigest)
			}
		})
	}
}

func TestProvisionDiscoveryRejectsUnverifiedOrPreGuardCandidateEvidence(t *testing.T) {
	record, binding := guardedProvisionResolutionRecord(t)
	request := provisionDiscoveryRequest(record, binding, 1)
	request.Candidates[0].Resources[0].Evidence[0].Source = providerexecutor.EvidenceSourceAdapter
	if _, err := sealProvisionDiscoveryObservation(
		t.Context(), record, binding, request, allowProvisionResolutionVerifier{},
	); !errors.Is(err, ErrProvisionResolutionEvidence) {
		t.Fatalf("adapter evidence error = %v, want ErrProvisionResolutionEvidence", err)
	}

	request = provisionDiscoveryRequest(record, binding, 1)
	request.Candidates[0].Resources[0].Evidence[0].CollectedAt = binding.GuardedAt.Add(-time.Second)
	if _, err := sealProvisionDiscoveryObservation(
		t.Context(), record, binding, request, allowProvisionResolutionVerifier{},
	); err == nil {
		t.Fatal("pre-guard candidate evidence was accepted")
	}
}

func TestProvisionResolutionVerifierErrorsAreSanitizedAndContextIsPreserved(t *testing.T) {
	secret := errors.New("provider response included sensitive diagnostics")
	if err := provisionResolutionVerificationError(context.Background(), secret); !errors.Is(err, ErrProvisionResolutionEvidence) || strings.Contains(err.Error(), secret.Error()) {
		t.Fatalf("verification error = %q, want sanitized evidence error", err)
	}

	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		wrapped := fmt.Errorf("sensitive verifier detail: %w", contextErr)
		if err := provisionResolutionVerificationError(context.Background(), wrapped); !errors.Is(err, contextErr) || strings.Contains(err.Error(), "sensitive verifier detail") {
			t.Fatalf("context error = %v, want %v", err, contextErr)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := provisionResolutionVerificationError(ctx, secret); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verifier error = %v, want context.Canceled", err)
	}
}

func TestProvisionResolutionRequestDigestRejectsChangedReplay(t *testing.T) {
	record, binding := guardedProvisionResolutionRecord(t)
	observation, err := sealProvisionDiscoveryObservation(
		t.Context(), record, binding, provisionDiscoveryRequest(record, binding, 0), allowProvisionResolutionVerifier{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := sealProvisionResolutionRequest(ProvisionResolutionRequest{
		TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
		ObservationID: observation.ObservationID, ObservationSnapshotDigest: observation.SnapshotDigest,
		ExpectedHeadSequence: record.Head.Sequence, ExpectedHeadReceiptDigest: record.Head.ReceiptDigest,
		ExpectedResolutionRevision: binding.ResolutionRevision,
		IdempotencyKey:             "decision-1", OperatorSubjectID: "operator-1",
		OperatorAttestationRef:    "provider-attestation://techstack/operators/decision-1",
		OperatorAttestationDigest: digest("operator-decision-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	request.OperatorSubjectID = "different-operator"
	if _, err := sealProvisionResolutionRequest(request); !errors.Is(err, ErrProvisionResolutionConflict) {
		t.Fatalf("changed request error = %v, want ErrProvisionResolutionConflict", err)
	}
}

func TestProvisionResolutionRejectsExhaustedCounterRange(t *testing.T) {
	record, binding := guardedProvisionResolutionRecord(t)
	binding.ResolutionRevision = providerexecutor.MaxJSONSafeInteger
	request := provisionDiscoveryRequest(record, binding, 0)
	if _, err := sealProvisionDiscoveryObservation(
		t.Context(), record, binding, request, allowProvisionResolutionVerifier{},
	); !errors.Is(err, ErrProvisionResolutionConflict) {
		t.Fatalf("exhausted discovery revision error = %v, want ErrProvisionResolutionConflict", err)
	}

	_, err := sealProvisionResolutionRequest(ProvisionResolutionRequest{
		TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
		ObservationID: binding.ObservationID, ObservationSnapshotDigest: digest("snapshot"),
		ExpectedHeadSequence: record.Head.Sequence, ExpectedHeadReceiptDigest: record.Head.ReceiptDigest,
		ExpectedResolutionRevision: providerexecutor.MaxJSONSafeInteger,
		IdempotencyKey:             "decision-exhausted", OperatorSubjectID: "operator-1",
		OperatorAttestationRef:    "provider-attestation://techstack/operators/decision-exhausted",
		OperatorAttestationDigest: digest("operator-decision-exhausted"),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("exhausted decision revision error = %v, want ErrInvalidRequest", err)
	}
}

func guardedProvisionResolutionRecord(t *testing.T) (OperationRecord, provisionDiscoveryBinding) {
	t.Helper()
	ledger := newMemoryLedger()
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-amo", &atMostOnceExecutor{}); err != nil {
		t.Fatal(err)
	}
	profile := testProfile("ionos-amo")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: staticProfileResolver{profile: profile}, Ledger: ledger,
		ClaimOwner: "operator-resolution-test", ClaimTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := coordinator.Start(t.Context(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedBindingForCommandHead(t, record.Command, record.Head)
	grant, err := ledger.AcquireProvisionDispatchClaim(
		t.Context(), record.Command, record.Head, prepared, "dispatch-worker", "dispatch-token", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.ReleaseExecutionClaim(t.Context(), grant.Claim); err != nil {
		t.Fatal(err)
	}
	record, err = ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return record, provisionDiscoveryBinding{
		ObservationID: "11111111-1111-4111-8111-111111111111",
		LeaseID:       record.Command.LeaseID, LeaseRevision: record.Command.LeaseRevision,
		RuntimeServerID: record.Command.RuntimeServerID, ResourceGenerationID: record.Command.ResourceGenerationID,
		HeadSequence: record.Head.Sequence, HeadReceiptDigest: record.Head.ReceiptDigest,
		AdapterManifestHash: record.ExecutionProfile.AdapterManifestHash,
		PreparedBinding:     prepared, GuardedAt: record.Head.IssuedAt.Add(time.Second),
	}
}

func provisionDiscoveryRequest(
	record OperationRecord,
	binding provisionDiscoveryBinding,
	candidateCount int,
) RecordProvisionDiscoveryRequest {
	collectedAt := binding.GuardedAt.Add(time.Microsecond)
	request := RecordProvisionDiscoveryRequest{
		TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
		ExpectedHeadSequence: record.Head.Sequence, ExpectedHeadReceiptDigest: record.Head.ReceiptDigest,
		ExpectedResolutionRevision: binding.ResolutionRevision,
		RequestedBySubjectID:       "discovery-worker", IdempotencyKey: "discovery-1",
		ObservationRef:    "provider-evidence://ionos/discovery/observation-1",
		ObservationDigest: digest("discovery-observation-1"),
		AttestationRef:    "provider-attestation://ionos/discovery/observation-1",
		AttestationDigest: digest("discovery-attestation-1"), CollectedAt: collectedAt,
	}
	for index := range candidateCount {
		bindingID := "server-" + string(rune('a'+index))
		nativeRef := "provider-server-" + string(rune('a'+index))
		target := providerexecutor.ResourceTarget{
			BindingID: bindingID, Kind: "compute", NativeRef: nativeRef,
			OwnershipHash: digest("ownership-" + bindingID), Disposition: providerexecutor.DispositionDelete,
		}
		evidence := providerexecutor.Evidence{
			Ref: "provider-evidence://ionos/discovery/" + bindingID, Digest: digest("evidence-" + bindingID),
			Source: providerexecutor.EvidenceSourceProviderAPI, OperationID: record.Command.OperationID,
			LeaseRevision: record.Command.LeaseRevision, RuntimeServerID: record.Command.RuntimeServerID,
			ProviderID: record.Command.ProviderID, CapabilitySnapshotHash: record.Command.CapabilitySnapshotHash,
			BindingID: bindingID, NativeRefHash: providerexecutor.ComputeNativeRefHash(nativeRef),
			ConnectionHash: record.Command.ConnectionHash, ExecutionProfileHash: record.Command.ExecutionProfileHash,
			ResourceGraphHash: record.Command.ResourceGraphHash,
			SubjectHash:       providerexecutor.ComputeEvidenceSubjectHash(record.Command, target, providerexecutor.ObservationPresent),
			Observation:       providerexecutor.ObservationPresent, Definitive: true,
			AttestationRef:    "provider-attestation://ionos/discovery/" + bindingID,
			AttestationDigest: digest("attestation-" + bindingID), CollectedAt: collectedAt,
		}
		request.Candidates = append(request.Candidates, ProvisionCandidateGraph{Resources: []providerexecutor.ResourceBinding{{
			BindingID: bindingID, Kind: "compute", NativeRef: nativeRef,
			OwnershipHash: target.OwnershipHash, Disposition: providerexecutor.DispositionDelete,
			Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupPending,
			Evidence: []providerexecutor.Evidence{evidence},
		}}})
	}
	return request
}

type allowProvisionResolutionVerifier struct{}

func (allowProvisionResolutionVerifier) VerifyProvisionDiscovery(
	context.Context, OperationRecord, ProvisionDiscoveryObservation,
) error {
	return nil
}

func (allowProvisionResolutionVerifier) VerifyProvisionDecision(
	context.Context, OperationRecord, ProvisionDiscoveryObservation, ProvisionResolutionRequest,
) error {
	return nil
}
