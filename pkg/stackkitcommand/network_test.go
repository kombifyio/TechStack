package stackkitcommand

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Re-binding changes the node's DNS record and re-applies its workspace, so it
// is refused without Owner approval or the stack instance it acts on; a status
// read must not pretend to carry an approval.
func TestNetworkCommandsRequireApprovalForRebindOnly(t *testing.T) {
	command := func(operation agentpb.StackKitOperation, approved bool, instance string) *agentpb.StackKitCommand {
		return &agentpb.StackKitCommand{
			CommandId: "cmd-1", Operation: operation, WorkingDirectory: "/opt/stackkit",
			Release: &agentpb.StackKitReleasePin{
				Version: MinNetworkRelease, PlatformOs: "linux", PlatformArch: "amd64",
				ArchiveSha256:      "0000000000000000000000000000000000000000000000000000000000000000",
				ReleaseIndexSha256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
			OwnerApproved: approved, StackkitInstanceId: instance,
		}
	}
	rebind := agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_REBIND
	status := agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_STATUS

	if err := ValidateCommand(command(rebind, true, "inst-1")); err != nil {
		t.Fatalf("approved rebind rejected: %v", err)
	}
	if ValidateCommand(command(rebind, false, "inst-1")) == nil {
		t.Fatal("rebind without Owner approval must be refused")
	}
	if ValidateCommand(command(rebind, true, "")) == nil {
		t.Fatal("rebind without a stack instance must be refused")
	}
	if err := ValidateCommand(command(status, false, "")); err != nil {
		t.Fatalf("status read rejected: %v", err)
	}
	if ValidateCommand(command(status, true, "")) == nil {
		t.Fatal("status must not carry Owner approval")
	}
	if IsMutatingOperation(status) || !IsMutatingOperation(rebind) {
		t.Fatal("rebind mutates the node, status only observes it")
	}
}
