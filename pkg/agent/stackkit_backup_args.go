package agent

import (
	"fmt"
	"regexp"
	"strings"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
)

var stackKitBackupDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

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
