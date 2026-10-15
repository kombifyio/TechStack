package stackkitcommand

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	// NetworkCapability marks an agent whose pinned StackKits release ships
	// `stackkit network status|rebind` (home network move).
	NetworkCapability = "stackkit.network.v1"
	// MinNetworkRelease is the first StackKits release that ships the
	// `stackkit network` command group.
	MinNetworkRelease = "v0.51.7"
)

// IsNetworkOperation reports the home network move operations. They act on the
// workspace of the stack bound to the node, like service operations.
func IsNetworkOperation(operation agentpb.StackKitOperation) bool {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_REBIND,
		agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_STATUS:
		return true
	default:
		return false
	}
}

// validateNetworkCommand admits the two network operations. They carry no
// caller input beyond the workspace binding: the address, gateway and DNS record
// are re-detected on the node by the pinned CLI.
func validateNetworkCommand(command *agentpb.StackKitCommand) error {
	if !ReleaseAtLeast(command.Release.Version, MinNetworkRelease) {
		return fmt.Errorf("network operations require StackKits %s or newer; upgrade the configured release pin", MinNetworkRelease)
	}
	if len(command.CandidateSpecJson) != 0 || len(command.AdvancedCapability) != 0 || len(command.InventoryJson) != 0 {
		return fmt.Errorf("StackKit network operations take no candidate, Inventory or capability")
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_NETWORK_STATUS:
		if command.OwnerApproved {
			return fmt.Errorf("StackKit network status is a read and must not carry Owner approval")
		}
		return nil
	default:
		if !command.OwnerApproved || strings.TrimSpace(command.StackkitInstanceId) == "" {
			return fmt.Errorf("StackKit network rebind requires Owner approval and stackkit_instance_id")
		}
		return nil
	}
}
