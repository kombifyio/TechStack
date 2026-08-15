package managedstackkit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
)

type fakeRolloutCustody struct {
	evidence backupstore.CustodyEvidence
	order    *[]string
}

func (fake *fakeRolloutCustody) Ensure(context.Context, string, string) (backupstore.CustodyEvidence, error) {
	*fake.order = append(*fake.order, "ensure")
	return fake.evidence, nil
}

func (fake *fakeRolloutCustody) Get(context.Context, string, string) (backupstore.Credentials, error) {
	*fake.order = append(*fake.order, "get")
	return backupstore.Credentials{TenantID: "tenant-a", StackID: "stack-a"}, nil
}

func (fake *fakeRolloutCustody) RecordAttestation(_ context.Context, _, _ string, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
	*fake.order = append(*fake.order, "record")
	fake.evidence = evidence
	return evidence, nil
}

func TestRolloutInventoryAttestsBeforeBuildingOpaqueInventory(t *testing.T) {
	order := []string{}
	custody := &fakeRolloutCustody{order: &order, evidence: backupstore.CustodyEvidence{
		BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"),
		AttestationEvidence: []byte("old"), ObservedAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}}
	builder, err := NewRolloutInventory(custody, custody, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	builder.attest = func(_ context.Context, _ backupstore.Credentials, evidence backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
		order = append(order, "attest")
		if len(evidence.AttestationEvidence) != 0 {
			t.Fatal("stale attestation reached fresh verifier")
		}
		evidence.AttestationEvidence = []byte("sha256:" + strings.Repeat("c", 64))
		evidence.ObservedAt = time.Date(2026, 8, 6, 10, 1, 0, 0, time.UTC)
		return evidence, nil
	}
	inventory, err := builder.Build(context.Background(), RolloutInventoryRequest{
		TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: testResolvedPlan(t),
		StackKitsVersion: "v0.5.2", CandidateDigest: "sha256:" + strings.Repeat("a", 64), ValidFor: 30 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "ensure,get,attest,record" {
		t.Fatalf("operation order = %v", order)
	}
	encoded := string(inventory)
	for _, forbidden := range []string{"secret", "password", "binding\"", "target\""} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("Inventory exposes custody input %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, cloudOperationsExecutable) || !strings.Contains(encoded, "sha256:"+strings.Repeat("b", 64)) {
		t.Fatalf("Inventory does not bind exact Operations process: %s", encoded)
	}
}

func TestRolloutInventoryRejectsPlanBeforeCustodySideEffects(t *testing.T) {
	order := []string{}
	custody := &fakeRolloutCustody{order: &order}
	builder, err := NewRolloutInventory(custody, custody, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.Build(context.Background(), RolloutInventoryRequest{
		TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: []byte(`{"kind":"not-a-resolved-plan"}`),
		StackKitsVersion: "v0.5.2", CandidateDigest: "sha256:" + strings.Repeat("a", 64), ValidFor: 30 * time.Minute,
	})
	if err == nil {
		t.Fatal("invalid plan was accepted")
	}
	if len(order) != 0 {
		t.Fatalf("invalid plan caused custody side effects: %v", order)
	}
}

func TestRolloutInventoryBuildsChannelOnlyWithoutBackupCustody(t *testing.T) {
	builder, err := NewRolloutInventory(nil, nil, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := builder.Build(context.Background(), RolloutInventoryRequest{
		TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: testResolvedPlanWithoutBackup(t),
		StackKitsVersion: "v0.5.2", CandidateDigest: "sha256:" + strings.Repeat("a", 64), ValidFor: 30 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory), "executionChannels") || strings.Contains(string(inventory), "externalBackupTargetBindings") {
		t.Fatalf("channel-only Inventory = %s", inventory)
	}
}
