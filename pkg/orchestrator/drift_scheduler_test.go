package orchestrator

import (
	"slices"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// The schedule walks every tenant of the canonical directory and starts a
// drift check only for rolled-out stacks that recorded Advanced Mode and were
// not checked within the interval.
func TestDriftScheduleChecksOnlyDueAdvancedStacksAcrossTenants(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	advanced := map[string]any{jobs.StackKitModeField: jobs.StackKitModeAdvanced}
	for _, fixture := range []struct {
		tenant, stack string
		summary       map[string]any
		checkedAt     *time.Time
	}{
		{tenant: "tenant-a", stack: "due-a", summary: advanced},
		{tenant: "tenant-a", stack: "standard-a", summary: map[string]any{}},
		{tenant: "tenant-b", stack: "recent-b", summary: advanced, checkedAt: &recent},
		{tenant: "tenant-b", stack: "due-b", summary: advanced},
	} {
		if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
			ID: fixture.stack, TenantID: fixture.tenant, OwnerSubjectID: "owner-" + fixture.tenant, Name: fixture.stack, Status: "running",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateStackRuntime(t.Context(), fixture.tenant, fixture.stack, controlplane.RuntimeUpdate{
			RuntimeSummary: fixture.summary, DriftStatus: "in_sync", DriftCheckedAt: fixture.checkedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	orch := New(&Config{
		Workers: 1, WorkDir: t.TempDir(), StackStore: store, JobStore: store,
		DriftStore: store, ActivityStore: store,
	}, nil)
	defer orch.Stop()
	scheduler, err := orch.NewDriftScheduler(DriftSchedulerConfig{
		Directory: store, Interval: 6 * time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := scheduler.ScheduleOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	var checked []string
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		records, err := store.ListJobsByTenant(t.Context(), tenant, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Type == string(jobs.JobTypeDriftCheck) {
				checked = append(checked, record.StackID)
			}
		}
	}
	slices.Sort(checked)
	if !slices.Equal(checked, []string{"due-a", "due-b"}) {
		t.Fatalf("scheduled drift checks = %v, want the due Advanced stacks of both tenants", checked)
	}
}
