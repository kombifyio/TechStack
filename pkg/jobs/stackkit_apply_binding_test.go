package jobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestLifecycleApplyPlansAndBindsTheExactReturnedHash(t *testing.T) {
	sender := &recordingStackKitCommandSender{}
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleApply, OwnerApproved: true, StackKit: "cloud-kit",
	}
	planHash, err := planStackKitLifecycleApply(context.Background(), sender, "job-1", req, stackkitrelease.Release{})
	if err != nil {
		t.Fatal(err)
	}
	if planHash != testResolvedPlanHash {
		t.Fatalf("plan hash = %q, want %q", planHash, testResolvedPlanHash)
	}
	if len(sender.commands) != 1 || sender.commands[0].Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		t.Fatalf("commands = %#v, want one typed PLAN", sender.commands)
	}
}

func TestLifecycleApplyHandlerDispatchesPlanThenHashBoundApply(t *testing.T) {
	sender := &recordingStackKitCommandSender{applyStatus: "applied"}
	release := &stackkitrelease.Release{}
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender:          sender,
		releaseResolver: func() (*stackkitrelease.Release, error) { return release, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleApply, OwnerApproved: true, StackKit: "cloud-kit",
	}
	job := &Job{ID: "job-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatal(err)
	}
	if len(sender.commands) != 2 {
		t.Fatalf("commands = %d, want PLAN then APPLY", len(sender.commands))
	}
	if sender.commands[0].Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN ||
		sender.commands[1].Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		t.Fatalf("operations = %s, %s", sender.commands[0].Operation, sender.commands[1].Operation)
	}
	if sender.commands[1].ExpectedPlanHash != testResolvedPlanHash {
		t.Fatalf("apply expected_plan_hash = %q, want %q", sender.commands[1].ExpectedPlanHash, testResolvedPlanHash)
	}
}
