package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	// MaxSealedDeliveryBytes bounds one sealed host delivery envelope.
	MaxSealedDeliveryBytes = 64 << 10
	// MaxRecipientChallengeBytes bounds the freshness value a node echoes.
	MaxRecipientChallengeBytes = 128
	// MaxDeliveryTenantRefBytes bounds the tenant a delivery is addressed to.
	MaxDeliveryTenantRefBytes = 128
)

// DeliveryTenantRef is the bare reference a sealed delivery is addressed to.
// Tenant IDs carry Auth0 subjects ("usr:google-oauth2|…"), which the agent
// refuses in argv. The node only compares this reference with the one Core
// sealed, so a stable digest keeps both sides equal without putting the
// subject on the node's command line.
func DeliveryTenantRef(tenantID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tenantID)))
	return "tenant-" + hex.EncodeToString(sum[:16])
}

// IsBackupTargetDeliveryOperation reports the operations that move managed
// off-site backup custody to the node. They carry ciphertext and public values
// only: the node, not the control plane, holds the custody and issues its own
// backup-target binding.
func IsBackupTargetDeliveryOperation(operation agentpb.StackKitOperation) bool {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND:
		return true
	default:
		return false
	}
}

func isSealedImport(operation agentpb.StackKitOperation) bool {
	return operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT ||
		operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND
}

// validateBackupTargetDeliveryFields keeps the delivery inputs on exactly the
// operations that consume them.
func validateBackupTargetDeliveryFields(command *agentpb.StackKitCommand) error {
	walletImport := command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT
	if !isSealedImport(command.Operation) && !walletImport && len(command.SealedDelivery) != 0 {
		return fmt.Errorf("StackKit sealed_delivery is only valid for a sealed backup target or Wallet item import")
	}
	if !isSealedImport(command.Operation) && !walletImport && command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP && strings.TrimSpace(command.DeliveryTenantRef) != "" {
		return fmt.Errorf("StackKit delivery_tenant_ref is only valid for a sealed import or AI provider setup")
	}
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT && command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT && strings.TrimSpace(command.RecipientChallenge) != "" {
		return fmt.Errorf("StackKit recipient_challenge is only valid for a recipient statement")
	}
	return nil
}

func validateBackupTargetDeliveryCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit backup target delivery requires Owner approval")
	}
	if len(command.InventoryJson) != 0 || len(command.CandidateSpecJson) != 0 || len(command.AdvancedCapability) != 0 {
		return fmt.Errorf("StackKit backup target delivery takes no Inventory, candidate or capability: the node issues its own binding")
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT {
		challenge := strings.TrimSpace(command.RecipientChallenge)
		if challenge == "" || challenge != command.RecipientChallenge || len(challenge) > MaxRecipientChallengeBytes {
			return fmt.Errorf("StackKit backup target recipient requires a bare challenge within %d bytes", MaxRecipientChallengeBytes)
		}
		return nil
	}
	tenant := strings.TrimSpace(command.DeliveryTenantRef)
	if len(command.SealedDelivery) == 0 || len(command.SealedDelivery) > MaxSealedDeliveryBytes || tenant == "" || tenant != command.DeliveryTenantRef || len(tenant) > MaxDeliveryTenantRefBytes {
		return fmt.Errorf("StackKit backup target import requires one sealed delivery within %d bytes and its tenant", MaxSealedDeliveryBytes)
	}
	return nil
}

// BackupTargetRecipientEvidence is the node's recipient statement, still
// unverified: the caller checks its owner signature against the owner key it
// pinned at enrollment before it seals anything to the key.
type BackupTargetRecipientEvidence struct {
	Statement json.RawMessage
}

// ParseBackupTargetRecipientEvidence admits a successful recipient result for
// the dispatched command and returns the statement it carries.
func ParseBackupTargetRecipientEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (BackupTargetRecipientEvidence, error) {
	if command == nil || result == nil || !result.Success || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_RECIPIENT {
		return BackupTargetRecipientEvidence{}, fmt.Errorf("successful StackKit backup target recipient evidence is required")
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return BackupTargetRecipientEvidence{}, fmt.Errorf("decode StackKit backup target recipient evidence: %w", err)
	}
	var data struct {
		Challenge string `json:"challenge"`
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != commandResultStatusSuccess ||
		json.Unmarshal(envelope.Data, &data) != nil || data.Challenge != command.RecipientChallenge {
		return BackupTargetRecipientEvidence{}, fmt.Errorf("StackKit backup target recipient evidence does not answer the dispatched challenge")
	}
	return BackupTargetRecipientEvidence{Statement: envelope.Data}, nil
}

// BackupTargetImportEvidence is what the node issued when it opened a delivery:
// opaque references only.
type BackupTargetImportEvidence struct {
	BackupTargetRef       string
	CustodyAttestationRef string
}

// ParseBackupTargetImportEvidence admits a successful sealed import or rebind:
// the node custodied the target and names only opaque references.
func ParseBackupTargetImportEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (BackupTargetImportEvidence, error) {
	if command == nil || result == nil || !result.Success || !isSealedImport(command.Operation) {
		return BackupTargetImportEvidence{}, fmt.Errorf("successful StackKit backup target import evidence is required")
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			State                 string `json:"state"`
			BackupTargetRef       string `json:"backupTargetRef"`
			CustodyAttestationRef string `json:"custodyAttestationRef"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return BackupTargetImportEvidence{}, fmt.Errorf("decode StackKit backup target import evidence: %w", err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != commandResultStatusSuccess ||
		envelope.Data.State != "custodied" || !strings.HasPrefix(envelope.Data.BackupTargetRef, "backup-target://sha256/") ||
		!strings.HasPrefix(envelope.Data.CustodyAttestationRef, "backup-custody-attestation://sha256/") {
		return BackupTargetImportEvidence{}, fmt.Errorf("StackKit backup target import evidence identity is invalid")
	}
	return BackupTargetImportEvidence{BackupTargetRef: envelope.Data.BackupTargetRef, CustodyAttestationRef: envelope.Data.CustodyAttestationRef}, nil
}
