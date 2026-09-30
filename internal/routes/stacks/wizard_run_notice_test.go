package stacks

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// Regression: a stuck Cloudreve rollout kept the dashboard notice up forever
// because /wizard/runs/active reported any non-terminal job. A silent or
// superseded run must read as no run; a fresh one names its target server.
func TestActiveWizardRunNoticeEndsWhenTheRolloutIsOver(t *testing.T) {
	ctx := context.Background()
	base := time.Now().UTC()

	activeRun := func(t *testing.T, store *controlplane.MemoryStore) map[string]any {
		t.Helper()
		h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
		e, rec := wizardRunActiveEvent(t)
		if err := h.getActiveWizardRun(e); err != nil {
			t.Fatalf("getActiveWizardRun: %v", err)
		}
		run, _ := decodeWizardRunSuccess(t, rec)["run"].(map[string]any)
		return run
	}
	seed := func(t *testing.T, jobAt time.Time) *controlplane.MemoryStore {
		t.Helper()
		store := controlplane.NewMemoryStore()
		store.SetNow(func() time.Time { return jobAt })
		if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
			ID: "job-cloudreve", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy",
			State: "pending", Step: "rollout", Result: map[string]any{"server_id": "server-7"},
		}); err != nil {
			t.Fatalf("seed job: %v", err)
		}
		store.SetNow(func() time.Time { return base })
		if _, err := store.UpsertWizardRun(ctx, controlplane.WizardRun{
			ID: "run-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
			RequestSHA256: "h", RunKind: "expansion", RequestedRunKind: "expansion",
			StackID: "stack-1", JobID: "job-cloudreve", Status: "completed",
		}); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		return store
	}

	t.Run("fresh rollout renders at its server", func(t *testing.T) {
		run := activeRun(t, seed(t, base.Add(-time.Minute)))
		if run == nil || run["target_server_id"] != "server-7" {
			t.Fatalf("fresh run must be active and target its server: %#v", run)
		}
	})
	t.Run("silent rollout is over", func(t *testing.T) {
		if run := activeRun(t, seed(t, base.Add(-2*time.Hour))); run != nil {
			t.Fatalf("a job silent for two hours must not keep a notice: %#v", run)
		}
	})
	t.Run("later completed rollout supersedes it", func(t *testing.T) {
		store := seed(t, base.Add(-5*time.Minute))
		if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
			ID: "job-later", TenantID: "tenant-1", StackID: "stack-1", Type: "deploy", State: "completed",
		}); err != nil {
			t.Fatalf("seed later job: %v", err)
		}
		if run := activeRun(t, store); run != nil {
			t.Fatalf("a superseded run must not keep a notice: %#v", run)
		}
	})
	t.Run("dismissed run stays hidden", func(t *testing.T) {
		store := seed(t, base.Add(-time.Minute))
		if err := store.DismissWizardRun(ctx, "tenant-1", "auth0|user-1", "run-1", base); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
		if run := activeRun(t, store); run != nil {
			t.Fatalf("a dismissed run must not keep a notice: %#v", run)
		}
	})
}
