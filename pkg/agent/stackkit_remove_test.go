package agent

import (
	"slices"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitRemoveRequiresOwnerApprovalAndTerminalEvidence(t *testing.T) {
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE, OwnerApproved: true, WorkloadRef: "photos", LocalSiteRef: "home", LocalNodeRef: "node-a", LocalExecutionChannelRef: "channel-a"}
	args, err := stackKitOperationArgs(command)
	if err != nil || !slices.Equal(args, []string{"remove", "--auto-approve", "--terminal-evidence-json", "--workload", "photos", "--local-site", "home", "--local-node", "node-a", "--local-execution-channel", "channel-a"}) {
		t.Fatalf("stackKitOperationArgs() = %v, %v", args, err)
	}
	command.OwnerApproved = false
	if _, err := stackKitOperationArgs(command); err == nil {
		t.Fatal("stackKitOperationArgs() accepted remove without Owner approval")
	}
	command.OwnerApproved, command.LocalNodeRef = true, ""
	if _, err := stackKitOperationArgs(command); err == nil {
		t.Fatal("stackKitOperationArgs() accepted remove without exact local placement")
	}
}
