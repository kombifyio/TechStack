package orchestrator

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestTriggerDriftCheckRequiresCanonicalTenantOwnerAndReservesDurableJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Homelab", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	orch := New(&Config{
		Workers: 1, WorkDir: t.TempDir(), StackStore: store, JobStore: store,
		DriftStore: store, ActivityStore: store,
	}, nil)

	defer orch.Stop()

	if _, err := orch.TriggerDriftCheck(DriftLifecycleRequest{
		RequestContext: t.Context(), TenantID: "tenant-1", OwnerID: "owner-2", StackID: stack.ID,
	}); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("foreign owner error = %v, want ErrNotFound", err)
	}
	jobID, err := orch.TriggerDriftCheck(DriftLifecycleRequest{
		RequestContext: t.Context(), TenantID: "tenant-1", OwnerID: "owner-1", StackID: stack.ID, TriggerType: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.GetJob(t.Context(), "tenant-1", jobID)
	if err != nil || job.StackID != stack.ID || job.Type != string(jobs.JobTypeDriftCheck) {
		t.Fatalf("durable drift job = %#v, %v", job, err)
	}
	updated, err := store.GetStack(t.Context(), "tenant-1", stack.ID)
	if err != nil || updated.DriftStatus != "checking" {
		t.Fatalf("canonical stack = %#v, %v", updated, err)
	}
}
