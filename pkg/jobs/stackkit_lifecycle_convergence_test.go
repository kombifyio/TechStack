package jobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// A lifecycle operation dispatched while a freshly paired Guard still converges
// its runtime is refused before it runs; the job sends it again once the Guard
// is ready instead of failing for good.
func TestLifecycleVerifyWaitsWhenGuardRuntimeIsStillConverging(t *testing.T) {
	previousDelay := typedStackKitRuntimePendingRetryDelay
	typedStackKitRuntimePendingRetryDelay = 0
	t.Cleanup(func() { typedStackKitRuntimePendingRetryDelay = previousDelay })

	sender := &pendingThenReadyLifecycleSender{}
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender:          sender,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &stackkitrelease.Release{}, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", StackKitInstanceID: "home-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleVerify, StackKit: "basement-kit", SpecPath: "stack-spec.v2.json",
	}
	job := &Job{ID: "job-verify-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatalf("verify after runtime readiness: %v", err)
	}
	if len(sender.ids) != 2 || sender.ids[0] == sender.ids[1] {
		t.Fatalf("verify commands = %v, want the refused one and one retry under a new command ID", sender.ids)
	}
}

type pendingThenReadyLifecycleSender struct{ ids []string }

func (sender *pendingThenReadyLifecycleSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.ids = append(sender.ids, command.CommandId)
	if len(sender.ids) == 1 {
		return &agentpb.StackKitResult{
			CommandId: command.CommandId, Success: false, ExitCode: 1,
			Stderr: "runtime convergence is pending; retry after the Guard reports runtime readiness",
		}, nil
	}
	return &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, CommandResultJson: typedStackKitFakeSuccessResult(command), Release: command.Release}, nil
}
