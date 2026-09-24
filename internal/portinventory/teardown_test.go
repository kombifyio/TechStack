package portinventory

import (
	"errors"
	"strings"
	"testing"
)

func TestTeardownSnapshotIsDeterministicAndTamperEvident(t *testing.T) {
	assertTeardownSnapshotContract(t)
}

func assertTeardownSnapshotContract(t *testing.T) {
	t.Helper()
	request := TeardownSnapshotRequest{TenantID: "tenant-a", OwnerSubjectID: "owner-a", TechstackID: "stack-a"}
	generations := []TeardownGeneration{
		{GenerationRef: GenerationRef{ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-b", ServerGeneration: 2}, StackID: "stack-a", ResolvedPlanHash: "plan-b"}, ClaimSetDigest: "sha256:" + strings.Repeat("b", 64), NodeRefs: []string{"node-b"}},
		{GenerationRef: GenerationRef{ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 1}, StackID: "stack-a", ResolvedPlanHash: "plan-a"}, ClaimSetDigest: "sha256:" + strings.Repeat("a", 64), NodeRefs: []string{"node-a"}},
	}
	first, err := sealTeardownSnapshot(request, generations)
	if err != nil {
		t.Fatalf("seal teardown snapshot: %v", err)
	}
	second, err := sealTeardownSnapshot(request, []TeardownGeneration{generations[1], generations[0]})
	if err != nil || first.SnapshotDigest != second.SnapshotDigest || first.Generations[0].ServerID != "server-a" {
		t.Fatalf("deterministic snapshots differ: first=%+v second=%+v err=%v", first, second, err)
	}
	decoded, err := DecodeTeardownSnapshot(first)
	if err != nil || decoded.SnapshotDigest != first.SnapshotDigest {
		t.Fatalf("decode durable snapshot: decoded=%+v err=%v", decoded, err)
	}
	decoded.Generations[0].ClaimSetDigest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidateTeardownSnapshot(decoded); !errors.Is(err, ErrTeardownSnapshotMismatch) {
		t.Fatalf("tampered snapshot error = %v, want ErrTeardownSnapshotMismatch", err)
	}
}
