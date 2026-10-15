package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// stalledApplySender answers like a node that ended its apply because it went
// silent, with the stall in its error text and no deadline anywhere in it.
type stalledApplySender struct{ stall stackkitcommand.ApplyStall }

func (sender stalledApplySender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		return &agentpb.StackKitResult{
			Success: false, ExitCode: 1,
			Stderr: "applying runtime owners\n" + sender.stall.Message(),
		}, nil
	}
	return &agentpb.StackKitResult{Success: true, CommandResultJson: typedStackKitFakeSuccessResult(command), Release: command.Release}, nil
}

// A silent apply is neither a slow one nor an ordinary failure: the controller
// names the owner it stopped at and tells the customer the setup stopped
// responding, where an exhausted budget keeps its own wording.
func TestTypedStackKitApplyReportsAStalledNodeApplyAsStalledNotTimedOut(t *testing.T) {
	sender := stalledApplySender{stall: stackkitcommand.ApplyStall{Owner: "runtime/tofu/main", Position: 3, Total: 10, Idle: 3 * time.Minute}}
	_, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", typedStackKitApplyRequest(), releaseWithAdvancedCatalog(t))

	var operationErr *typedStackKitOperationError
	if !errors.As(err, &operationErr) || operationErr.Operation != StackKitLifecycleApply {
		t.Fatalf("error = %v, want a typed apply failure", err)
	}
	if !operationErr.Stalled() || operationErr.TimedOut() {
		t.Fatalf("stalled = %v, timed out = %v; a silent apply is stalled, not timed out", operationErr.Stalled(), operationErr.TimedOut())
	}
	if stall := operationErr.AgentStall; stall.Owner != "runtime/tofu/main" || stall.Position != 3 || stall.Total != 10 {
		t.Fatalf("stall = %+v, want the owner and position the node reported", stall)
	}
	if details := rolloutFailureDetails(err); details != rolloutApplyStalledDetails {
		t.Fatalf("customer details = %q, want the stalled wording", details)
	}
}
