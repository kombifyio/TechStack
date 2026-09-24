package orchestrator

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/jobs"
)

// TestEnqueueBackupRequiresExactIdentity keeps an unattributed backup from
// becoming a durable job. The gate resolved against a tenant and a stack; a
// job that cannot name both is not the job that was admitted.
func TestEnqueueBackupRequiresExactIdentity(t *testing.T) {
	o := &Orchestrator{}
	for _, payload := range []jobs.BackupPayload{
		{StackID: "stack-a"},
		{TenantID: "tenant-a"},
		{},
	} {
		if err := o.EnqueueBackup(context.Background(), payload); err == nil {
			t.Fatalf("incomplete identity must not enqueue: %+v", payload)
		}
	}
}

func TestEnqueueBackupRefusesAnUnconfiguredOrchestrator(t *testing.T) {
	var o *Orchestrator
	if err := o.EnqueueBackup(context.Background(), jobs.BackupPayload{
		TenantID: "tenant-a", StackID: "stack-a",
	}); err == nil {
		t.Fatal("an unconfigured orchestrator must refuse rather than panic or silently drop")
	}
}
