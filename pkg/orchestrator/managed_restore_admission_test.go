package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

// A paid managed snapshot must never be queued on caller-selected identity,
// missing entitlement, stale usage or an alternate kit spelling.
func TestManagedRestoreDrillRequiresCurrentOwnerBackupAdmission(t *testing.T) {
	for _, scenario := range []string{"missing", "denied", "stale", "tenant", "owner", "agent", "kit", "unapproved", "direct-backup", "allowed"} {
		t.Run(scenario, func(t *testing.T) {
			orch, store := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: jobs.StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "stack-spec.bound.json"}.Map()})
			req := jobs.StackKitLifecycleRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: jobs.StackKitLifecycleRestoreDrill, OwnerApproved: true}
			if scenario != "missing" {
				orch.ConfigureManagedRestoreAdmission(func(_ context.Context, request jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
					if request.TenantID != "tenant-1" || request.StackID != "stack-1" || request.UserID != "owner-1" {
						t.Fatal("gate received unowned identity")
					}
					measured := time.Now()
					if scenario == "stale" {
						measured = measured.Add(-time.Hour)
					}
					return jobs.BackupAdmissionDecision{Denied: scenario == "denied", QuotaBytes: 100, UsedBytes: 20, MeasuredAt: measured}, nil
				})
			}
			switch scenario {
			case "tenant":
				req.TenantID = "other"
			case "owner":
				req.OwnerID = "other"
			case "agent":
				req.AgentID = "other"
			case "kit":
				req.StackKit = "basement-kit"
			case "unapproved":
				req.OwnerApproved = false
			case "direct-backup":
				req.Operation = jobs.StackKitLifecycleBackupRun
			}
			id, err := orch.EnqueueStackKitLifecycle(context.Background(), req)
			if scenario != "allowed" {
				if err == nil || id != "" {
					t.Fatal("unadmitted managed snapshot was queued")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			job, ok := orch.Queue().Get(id)
			if !ok {
				t.Fatal("admitted drill was not queued")
			}
			if _, err := store.GetJob(context.Background(), "tenant-1", id); err != nil {
				t.Fatalf("drill lacks durable custody: %v", err)
			}
			if job.Payload["spec_path"] != "stack-spec.bound.json" || job.Payload["stackkit_instance_id"] != "home" || job.Payload[jobs.ManagedBackupAdmissionField] == nil {
				t.Fatal("drill lost its exact runtime/admission binding")
			}
		})
	}
}
