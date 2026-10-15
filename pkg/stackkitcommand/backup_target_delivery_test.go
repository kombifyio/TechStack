package stackkitcommand

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func deliveryCommand() *agentpb.StackKitCommand {
	command := validationCommand()
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT
	command.StackkitInstanceId = ""
	command.ServiceKey = ""
	command.SealedDelivery = []byte(`{"sealed":"ciphertext"}`)
	command.DeliveryTenantRef = "tenant-1"
	return command
}

// The node issues its own backup-target binding, so a delivery command can
// neither carry the control plane's Inventory nor a sealed value on another
// operation, and the recipient proof answers exactly the challenge it was given.
func TestBackupTargetDeliveryLeavesTheBindingToTheNode(t *testing.T) {
	if err := ValidateCommand(deliveryCommand()); err != nil {
		t.Fatalf("sealed import was refused: %v", err)
	}
	withInventory := deliveryCommand()
	withInventory.InventoryJson = []byte(`{"schemaVersion":"stackkit.inventory/v1"}`)
	if ValidateCommand(withInventory) == nil {
		t.Fatal("a delivery command carried the control plane's Inventory")
	}
	elsewhere := validationCommand()
	elsewhere.SealedDelivery = []byte("ciphertext")
	if ValidateCommand(elsewhere) == nil {
		t.Fatal("a sealed delivery rode on an unrelated operation")
	}
	recipient := validationCommand()
	recipient.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT
	recipient.StackkitInstanceId, recipient.ServiceKey = "", ""
	recipient.RecipientChallenge = "challenge-1"
	if err := ValidateCommand(recipient); err != nil {
		t.Fatalf("recipient proof was refused: %v", err)
	}
	result := validationResult(recipient)
	result.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup target recipient","status":"success","data":{"challenge":"another-challenge"}}`)
	if ValidateResult(result, recipient) == nil {
		t.Fatal("a recipient statement for another challenge was admitted")
	}
	result.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup target recipient","status":"success","data":{"challenge":"challenge-1"}}`)
	if err := ValidateResult(result, recipient); err != nil {
		t.Fatalf("the answered challenge was refused: %v", err)
	}
}
