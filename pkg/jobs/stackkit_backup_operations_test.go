package jobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestLifecycleBackupRunDispatchesPlanBoundBackupRunWithOperationID(t *testing.T) {
	sender := &nativeV2RestoreCommander{planHash: testResolvedPlanHash, configured: true}
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender:          sender,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &stackkitrelease.Release{}, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", StackKitInstanceID: "home-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleBackupRun, OwnerApproved: true, StackKit: "basement-kit", SpecPath: "stack-spec.v2.json",
	}
	job := &Job{ID: "job-backup-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatal(err)
	}
	run := sender.commands[len(sender.commands)-1]
	if run.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN || run.CommandId != job.ID ||
		run.ExpectedPlanHash != testResolvedPlanHash || run.StackkitInstanceId != "home-stack" || run.SpecPath != "stack-spec.v2.json" {
		t.Fatalf("backup run command = %+v", run)
	}
	backup, _ := job.Snapshot().Result["backup"].(map[string]interface{})
	if backup["operation_id"] != job.ID || backup["snapshot_anchor_id"] == "" {
		t.Fatalf("backup evidence = %+v", backup)
	}
}
