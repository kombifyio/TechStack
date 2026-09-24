package providercontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestProvisionResolutionWorkflowOwnsDiscoveryResumeAndDecisionOrder(t *testing.T) {
	for _, test := range []struct {
		name          string
		stored        bool
		revision      uint64
		wantDiscover  int
		wantReverify  int
		wantStoreLoad int
	}{
		{name: "records fresh discovery", wantDiscover: 1, wantStoreLoad: 1},
		{name: "reverifies stored discovery", stored: true, wantReverify: 1, wantStoreLoad: 1},
		{name: "records successor discovery after zero", revision: 1, wantDiscover: 1, wantStoreLoad: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, _ := guardedProvisionResolutionRecord(t)
			observation := ProvisionDiscoveryObservation{
				TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
				ObservationID: "observation-1", SnapshotDigest: digest("snapshot-1"),
				HeadSequence: record.Head.Sequence, HeadReceiptDigest: record.Head.ReceiptDigest,
			}
			provider := &workflowProviderFake{observation: observation}
			store := &workflowStoreFake{observation: observation, stored: test.stored, record: record, revision: test.revision}
			workflow, err := NewProvisionResolutionWorkflow(
				workflowOperationLoaderFake{record: record}, store, provider,
			)
			if err != nil {
				t.Fatalf("NewProvisionResolutionWorkflow: %v", err)
			}
			result, err := workflow.Resolve(t.Context(), ProvisionResolutionWorkflowRequest{
				TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
				OperatorSubjectID: "operator-1", Confirmation: "adopt-provision:" + record.Command.OperationID,
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if result.Decision.Outcome != ProvisionResolutionAdoptedExactCandidate || result.Operation.Command.OperationID != record.Command.OperationID {
				t.Fatalf("result = %+v", result)
			}
			if provider.discoverCalls != test.wantDiscover || provider.reverifyCalls != test.wantReverify || store.loadCalls != test.wantStoreLoad {
				t.Fatalf("calls discover=%d reverify=%d load=%d", provider.discoverCalls, provider.reverifyCalls, store.loadCalls)
			}
			if store.recordCalls != test.wantDiscover || store.commitCalls != 1 {
				t.Fatalf("calls record=%d commit=%d", store.recordCalls, store.commitCalls)
			}
			if test.wantDiscover != 0 && provider.discoveryRevision != test.revision {
				t.Fatalf("fresh discovery revision = %d, want workflow-owned %d", provider.discoveryRevision, test.revision)
			}
			revisionKey := fmt.Sprintf(":%d", test.revision)
			if store.loadKey != "provision-discovery:"+record.Command.OperationID+revisionKey || store.decisionRequest.IdempotencyKey != "provision-resolution:"+record.Command.OperationID+revisionKey {
				t.Fatalf("keys discovery=%q decision=%q", store.loadKey, store.decisionRequest.IdempotencyKey)
			}
		})
	}
}

func TestBuildProvisionResolutionRequestRequiresProviderNeutralConfirmation(t *testing.T) {
	observation := ProvisionDiscoveryObservation{
		TenantID: "tenant-1", OperationID: "operation-1",
		ObservationID: "observation-1", SnapshotDigest: digest("snapshot-1"),
		HeadSequence: 2, HeadReceiptDigest: digest("head-2"),
	}
	record := OperationRecord{Head: providerexecutor.Receipt{Sequence: 2, ReceiptDigest: digest("head-2")}}
	if _, err := BuildProvisionResolutionRequest(
		record, observation, "operator-1", "decision-1", "adopt-provision:other",
	); err == nil {
		t.Fatal("mismatched generic operation confirmation was accepted")
	}
	request, err := BuildProvisionResolutionRequest(
		record, observation, "operator-1", "decision-1", "adopt-provision:operation-1",
	)
	if err != nil {
		t.Fatalf("BuildProvisionResolutionRequest: %v", err)
	}
	if request.OperatorAttestationRef != "provider-attestation://techstack/operator-resolution/operation-1" ||
		request.OperatorAttestationDigest == "" {
		t.Fatalf("operator attestation = %+v", request)
	}
	if err := verifyProvisionResolutionOperatorAttestation(observation, request); err != nil {
		t.Fatalf("verify operator attestation: %v", err)
	}
	request.OperatorAttestationDigest = digest("tampered-attestation")
	if err := verifyProvisionResolutionOperatorAttestation(observation, request); !errors.Is(err, ErrProvisionResolutionEvidence) {
		t.Fatalf("tampered attestation error = %v", err)
	}
}

type workflowOperationLoaderFake struct{ record OperationRecord }

func (f workflowOperationLoaderFake) LoadOperation(context.Context, string, string) (OperationRecord, error) {
	return f.record, nil
}

type workflowStoreFake struct {
	observation            ProvisionDiscoveryObservation
	stored                 bool
	record                 OperationRecord
	loadCalls, recordCalls int
	commitCalls            int
	loadKey                string
	decisionRequest        ProvisionResolutionRequest
	revision               uint64
}

func (f *workflowStoreFake) LoadProvisionResolutionRevision(context.Context, string, string) (uint64, error) {
	return f.revision, nil
}

func (f *workflowStoreFake) LoadProvisionDiscovery(_ context.Context, _, _, key string) (ProvisionDiscoveryObservation, bool, error) {
	f.loadCalls++
	f.loadKey = key
	return f.observation, f.stored, nil
}

func (f *workflowStoreFake) RecordProvisionDiscovery(_ context.Context, _ RecordProvisionDiscoveryRequest) (ProvisionDiscoveryObservation, bool, error) {
	f.recordCalls++
	f.observation.ObservedResolutionRevision = f.revision
	return f.observation, true, nil
}

func TestProvisionResolutionCommitRequiresPersistedDiscovery(t *testing.T) {
	record, _ := guardedProvisionResolutionRecord(t)
	provider := &workflowProviderFake{}
	store := &workflowStoreFake{record: record}
	workflow, err := NewProvisionResolutionWorkflow(workflowOperationLoaderFake{record: record}, store, provider)
	if err != nil {
		t.Fatalf("NewProvisionResolutionWorkflow: %v", err)
	}
	_, err = workflow.Commit(t.Context(), ProvisionResolutionWorkflowRequest{
		TenantID: record.Command.TenantID, OperationID: record.Command.OperationID,
		OperatorSubjectID: "operator-1", Confirmation: "adopt-provision:" + record.Command.OperationID,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Commit before discovery error = %v, want ErrInvalidRequest", err)
	}
	if provider.discoverCalls != 0 || provider.reverifyCalls != 0 || store.commitCalls != 0 {
		t.Fatalf("commit-before-discovery crossed provider/CAS boundary: provider=%+v commit=%d", provider, store.commitCalls)
	}
}

func (f *workflowStoreFake) CommitProvisionResolution(_ context.Context, request ProvisionResolutionRequest) (ProvisionResolutionDecision, OperationRecord, bool, error) {
	f.commitCalls++
	f.decisionRequest = request
	return ProvisionResolutionDecision{Outcome: ProvisionResolutionAdoptedExactCandidate}, f.record, true, nil
}

type workflowProviderFake struct {
	observation       ProvisionDiscoveryObservation
	discoverCalls     int
	reverifyCalls     int
	discoveryRevision uint64
	discoveryKey      string
}

func (f *workflowProviderFake) BuildProvisionDiscoveryRequest(
	_ context.Context, record OperationRecord, revision uint64, _ string, key string,
) (RecordProvisionDiscoveryRequest, error) {
	f.discoverCalls++
	f.discoveryRevision = revision
	f.discoveryKey = key
	return RecordProvisionDiscoveryRequest{TenantID: record.Command.TenantID, OperationID: record.Command.OperationID}, nil
}

func (f *workflowProviderFake) VerifyProvisionDiscovery(context.Context, OperationRecord, ProvisionDiscoveryObservation) error {
	f.reverifyCalls++
	return nil
}

func (*workflowProviderFake) VerifyProvisionDecision(context.Context, OperationRecord, ProvisionDiscoveryObservation, ProvisionResolutionRequest) error {
	return nil
}
