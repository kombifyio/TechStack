package agent

import (
	"fmt"
	"regexp"
	"strings"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

var stackKitBackupDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

var stackKitRestoreActivationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{7,127}$`)

func stackKitRestoreActivationArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if command == nil || !command.OwnerApproved || !stackKitBackupDigestPattern.MatchString(command.RestoreResultId) ||
		!stackKitBackupDigestPattern.MatchString(command.ExpectedPlanHash) ||
		!stackKitRestoreActivationIDPattern.MatchString(command.ActivationOperationId) {
		return nil, fmt.Errorf("restore activation requires approval, exact staged result and plan, and original activation identity")
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE:
		return []string{"backup", "restore", "activate", command.RestoreResultId, "--owner-approve", "--json", "--operation-id", command.ActivationOperationId, "--expected-plan-hash", command.ExpectedPlanHash}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER:
		args := []string{"backup", "restore", "recover", command.ActivationOperationId, "--rollback", "--owner-approve", "--json", "--expected-plan-hash", command.ExpectedPlanHash}
		if original := command.OriginalRecoveryRelease; original != nil {
			if err := stackkitcommand.ValidateCommand(command); err != nil {
				return nil, err
			}
			args = append(args, "--original-release", original.Version, "--original-archive-sha256", original.ArchiveSha256,
				"--original-index-sha256", original.ReleaseIndexSha256)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported restore activation operation")
	}
}

// stackKitBackupRunArgs builds the fixed argv for one out-of-band snapshot.
//
// The command id becomes --operation-id so a redelivered command cannot create
// a second snapshot. The Guard transport is an at-least-once outbound long
// poll, so a retry after a lost result is normal rather than exceptional, and
// without this every retry would cost the customer another generation.
func stackKitBackupRunArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if command == nil {
		return nil, fmt.Errorf("StackKit backup run requires a command")
	}
	operationID := strings.TrimSpace(command.CommandId)
	if operationID == "" {
		return nil, fmt.Errorf("StackKit backup run requires a command id to stay idempotent")
	}
	// The id reaches the CLI as an argument, so it must not be able to open a
	// second flag or escape the fixed argv this closed command set exists to
	// guarantee.
	if strings.HasPrefix(operationID, "-") || strings.ContainsAny(operationID, ` /\"'`+"\t\n") {
		return nil, fmt.Errorf("StackKit backup run operation id must be a bare token")
	}
	return []string{"backup", "run", "--json", "--operation-id", operationID}, nil
}

// stackKitBackupRestoreArgs stages one signed snapshot through the released
// native v2 lifecycle. StackKits derives the repository object and isolated
// staging path from the anchor, then revalidates local Owner, CUE, generation,
// and Apply authority before doing any work.
func stackKitBackupRestoreArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if command == nil {
		return nil, fmt.Errorf("StackKit backup restore requires a command")
	}
	if !command.OwnerApproved {
		return nil, fmt.Errorf("StackKit backup restore requires explicit Owner approval")
	}
	operationID := strings.TrimSpace(command.CommandId)
	if operationID == "" || strings.HasPrefix(operationID, "-") || strings.ContainsAny(operationID, ` /\"'`+"\t\n") {
		return nil, fmt.Errorf("StackKit backup restore operation id must be a bare token")
	}
	snapshotAnchorID := strings.TrimSpace(command.SnapshotAnchorId)
	if !stackKitBackupDigestPattern.MatchString(snapshotAnchorID) {
		return nil, fmt.Errorf("StackKit backup restore requires a sha256 snapshot-anchor id")
	}
	return []string{
		"backup", "restore", snapshotAnchorID,
		"--owner-approve", "--json", "--operation-id", operationID,
	}, nil
}

var stackKitDeliveryTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// stackKitBackupTargetRecipientArgs proves the node's recipient key for the
// control plane's challenge. The challenge is a bare token so it cannot open a
// second flag.
func stackKitBackupTargetRecipientArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if command == nil || !command.OwnerApproved {
		return nil, fmt.Errorf("StackKit backup target recipient requires explicit Owner approval")
	}
	if !stackKitDeliveryTokenPattern.MatchString(command.RecipientChallenge) {
		return nil, fmt.Errorf("StackKit backup target recipient requires a bare challenge")
	}
	args := []string{"backup", "target", "recipient", "--challenge", command.RecipientChallenge, "--owner-approve"}
	if command.SourceGenerationInspection {
		args = append(args, "--source-generation")
	}
	return args, nil
}

// stackKitBackupTargetImportArgs opens one sealed delivery into the node's own
// custody. The envelope is ciphertext and travels on stdin, never in argv or
// the environment; the release digest is the pinned archive the node issues its
// binding for.
func stackKitBackupTargetImportArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if command == nil || !command.OwnerApproved {
		return nil, fmt.Errorf("StackKit backup target import requires explicit Owner approval")
	}
	if !stackKitDeliveryTokenPattern.MatchString(command.DeliveryTenantRef) {
		return nil, fmt.Errorf("StackKit backup target import requires a bare tenant ref")
	}
	if command.Release == nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(command.Release.ArchiveSha256) {
		return nil, fmt.Errorf("StackKit backup target import requires the pinned release archive digest")
	}
	args := []string{"backup", "target", "import", "--sealed", "--tenant-ref", command.DeliveryTenantRef, "--owner-approve", "--candidate-digest", "sha256:" + command.Release.ArchiveSha256}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_REBIND {
		args = append(args, "--rebind")
	}
	return args, nil
}
