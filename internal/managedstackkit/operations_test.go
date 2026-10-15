package managedstackkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeexecutor"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
)

type fakeOperationsCustody struct {
	credentials backupstore.Credentials
	evidence    backupstore.CustodyEvidence
	issued      backupstore.NodeBinding
	recorded    bool
}

func (f *fakeOperationsCustody) NodeBinding(context.Context, string, string) (backupstore.NodeBinding, error) {
	return f.issued, nil
}

func (f *fakeOperationsCustody) Get(context.Context, string, string) (backupstore.Credentials, error) {
	return f.credentials, nil
}
func (f *fakeOperationsCustody) Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error) {
	return f.evidence, nil
}
func (f *fakeOperationsCustody) RecordAttestation(_ context.Context, _, _ string, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
	f.recorded = true
	f.evidence = evidence
	return evidence, nil
}

func nodeIssuedBinding() backupstore.NodeBinding {
	return backupstore.NodeBinding{BackupTargetRef: "backup-target://sha256/" + strings.Repeat("1", 64), CustodyAttestationRef: "backup-custody-attestation://sha256/" + strings.Repeat("2", 64)}
}

func TestManagedOperationsReturnsAppliedHealthyOnlyAfterFreshTargetVerification(t *testing.T) {
	evidence := operationsEvidence(t)
	request := operationsExecutionRequest(t, nodeIssuedBinding())
	called := false
	custody := &fakeOperationsCustody{credentials: backupstore.Credentials{TenantID: "tenant-a", StackID: "stack-a"}, evidence: evidence, issued: nodeIssuedBinding()}
	operations, _ := NewOperations(custody)
	operations.validate = func() error { return nil }
	operations.now = func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }
	operations.attest = func(_ context.Context, _ backupstore.Credentials, fresh backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
		called = true
		if len(fresh.AttestationEvidence) != 0 {
			t.Fatal("fresh verification reused durable attestation as target proof")
		}
		fresh.AttestationEvidence = []byte("sha256:" + strings.Repeat("f", 64))
		fresh.ObservedAt = time.Date(2026, 8, 6, 12, 0, 1, 0, time.UTC)
		return fresh, nil
	}
	outcome, err := operations.Execute(context.Background(), OperationsRequest{TenantID: "tenant-a", StackID: "stack-a", RuntimeAgentID: "runtime-a", Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if !called || !custody.recorded || len(outcome.Runtime) != 1 || outcome.Runtime[0].Status != runtimeexecutor.RuntimeStatusApplied || len(outcome.Health) != 1 || outcome.Health[0].Status != runtimeexecutor.HealthStatusHealthy {
		t.Fatalf("outcome = %#v called=%t", outcome, called)
	}
}

// Core cannot derive the node's references, so a binding that is not the one the
// node issued never reaches the target.
func TestManagedOperationsRejectsBindingTheNodeDidNotIssue(t *testing.T) {
	evidence := operationsEvidence(t)
	request := operationsExecutionRequest(t, nodeIssuedBinding())
	request.BackupTargetBindings[0].BackupTargetRef = "backup-target://sha256/" + strings.Repeat("0", 64)
	operations, _ := NewOperations(&fakeOperationsCustody{evidence: evidence, issued: nodeIssuedBinding()})
	operations.validate = func() error { return nil }
	operations.now = func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }
	operations.attest = func(context.Context, backupstore.Credentials, backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
		t.Fatal("mismatched binding reached target")
		return backupstore.CustodyEvidence{}, nil
	}
	if _, err := operations.Execute(context.Background(), OperationsRequest{TenantID: "tenant-a", StackID: "stack-a", RuntimeAgentID: "runtime-a", Request: request}); err == nil {
		t.Fatalf("mismatched binding accepted: %v", err)
	}
}

func operationsEvidence(t *testing.T) backupstore.CustodyEvidence {
	t.Helper()
	return backupstore.CustodyEvidence{BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"), AttestationEvidence: []byte("sha256:" + strings.Repeat("a", 64)), ObservedAt: time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)}
}

func TestOperationRejectionDetailsRemainStable(t *testing.T) {
	details := RejectionDetails(rejectOperation(
		"backup_binding_stale", true, "Regenerate the StackKits Inventory.", errors.New("internal detail"),
	))
	if details.ReasonCode != "backup_binding_stale" || !details.Retryable || details.UserGuidance == "" {
		t.Fatalf("details = %#v", details)
	}
	fallback := RejectionDetails(errors.New("untyped"))
	if fallback.ReasonCode != "managed_stackkit_operation_rejected" || fallback.UserGuidance == "" {
		t.Fatalf("fallback = %#v", fallback)
	}
}

func operationsExecutionRequest(t *testing.T, issued backupstore.NodeBinding) runtimeexecutor.ExecutionRequest {
	t.Helper()
	bindingRef := "backup-target-binding://sha256/" + strings.Repeat("3", 64)
	targetRef, attestationRef := issued.BackupTargetRef, issued.CustodyAttestationRef
	binding := runtimeexecutor.BackupTargetBinding{ID: "binding-a", Kind: "backup-target", RuntimeRequirementID: "requirement-a", StackID: "stack-a", SiteRef: managedOperationsSite,
		CapabilityRef: backupbinding.Capability, ContractOwnerRef: cloudBackupProviderRef, TargetNodeRefs: []string{managedOperationsNode}, BindingRef: bindingRef,
		BindingHash: testDigest("b"), BackupTargetRef: targetRef, CustodyAttestationRef: attestationRef, ValidUntil: "2026-08-07T00:00:00Z"}
	document := map[string]any{"apiVersion": "stackkit.executor-contract-bundle/v1", "kind": "ExecutorContractBundle", "module": map[string]any{"id": cloudBackupModuleRef},
		"planInputs": map[string]any{"stackId": "stack-a", "cloudOffsiteBackup": map[string]any{"bindings": map[string]any{managedOperationsSite: map[string]any{backupbinding.Capability: map[string]any{
			"bindingRef": binding.BindingRef, "bindingHash": binding.BindingHash, "backupTargetRef": binding.BackupTargetRef, "custodyAttestationRef": binding.CustodyAttestationRef,
		}}}}}}
	content, _ := json.Marshal(document)
	target := runtimeexecutor.RuntimeTarget{RequirementID: "requirement-a", OwnerKind: "module", OwnerRef: cloudBackupModuleRef, ProviderRef: cloudBackupProviderRef,
		ModuleRef: cloudBackupModuleRef, UnitRef: cloudBackupUnitRef, RuntimeKind: "host", RuntimeDelivery: "stackkit", InstanceRef: "instance-a",
		ExecutionChannelRef: managedOperationsChannel, SiteRefs: []string{managedOperationsSite}, NodeRefs: []string{managedOperationsNode},
		BackupTargetBindingRefs: []string{binding.ID}, ArtifactRefs: []string{"artifact-a"}}
	return runtimeexecutor.ExecutionRequest{RequestDigest: testDigest("r"), RuntimeTargets: []runtimeexecutor.RuntimeTarget{target},
		HealthTargets:        []runtimeexecutor.HealthTarget{{RequirementID: "health-a", RuntimeRequirementID: target.RequirementID, TargetRef: cloudBackupModuleRef, SiteRefs: target.SiteRefs, NodeRefs: target.NodeRefs}},
		BackupTargetBindings: []runtimeexecutor.BackupTargetBinding{binding}, Artifacts: []runtimeexecutor.Artifact{{ID: "artifact-a", Format: "json", OwnerRef: target.InstanceRef,
			ProviderRef: cloudBackupProviderRef, ModuleRef: cloudBackupModuleRef, UnitRef: cloudBackupUnitRef, SiteRefs: target.SiteRefs, NodeRefs: target.NodeRefs, Content: content}}}
}
