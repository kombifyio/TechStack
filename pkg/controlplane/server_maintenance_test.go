package controlplane

import (
	"errors"
	"testing"
	"time"
)

// A claimed reboot holds its node: a stack job for the same agent waits until
// it settles. A reboot that is only queued never blocks the job; it is the
// one that waits.
func TestStackJobWaitsWhileAClaimedRebootHoldsItsAgent(t *testing.T) {
	store := NewMemoryStore()
	ctx, now := t.Context(), time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	// The hold compares its deadline with the store clock; pin it to the test's.
	store.SetNow(func() time.Time { return now })
	reboot := func(id string) {
		t.Helper()
		if _, err := store.CreateServerMaintenanceJob(ctx, ServerMaintenanceJob{
			ID: id, TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1",
			Action: ServerMaintenanceActionReboot, RequestDigest: "digest-" + id, State: ServerMaintenanceStateQueued,
		}); err != nil {
			t.Fatal(err)
		}
	}
	stackJob := func(id string) {
		t.Helper()
		if _, err := store.CreateJob(ctx, UpsertJobRequest{
			ID: id, TenantID: "tenant-1", Type: "restart", State: "pending", Payload: map[string]any{"agent_id": "agent-1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	reboot("reboot-1")
	if _, err := store.ClaimServerMaintenanceJob(ctx, "tenant-1", "reboot-1", "cmd-reboot-1", now.Add(5*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	stackJob("job-1")
	if _, err := store.StartJob(ctx, "tenant-1", "job-1", now); !errors.Is(err, ErrNodeUnderMaintenance) {
		t.Fatalf("start under a claimed reboot err = %v, want ErrNodeUnderMaintenance", err)
	}
	if _, err := store.UpdateServerMaintenanceJob(ctx, "tenant-1", "reboot-1", ServerMaintenanceUpdate{
		ExpectedState: ServerMaintenanceStateRunning, State: ServerMaintenanceStateCompleted, At: now,
	}); err != nil {
		t.Fatal(err)
	}
	reboot("reboot-2")
	if _, err := store.StartJob(ctx, "tenant-1", "job-1", now); err != nil {
		t.Fatalf("start next to a queued reboot: %v", err)
	}
}
