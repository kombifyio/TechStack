package agent

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestBackupRunArgsCarryTheCommandIdAsTheOperationId(t *testing.T) {
	args, err := stackKitBackupRunArgs(&agentpb.StackKitCommand{CommandId: "job-abc-123"})
	if err != nil {
		t.Fatalf("backup run args: %v", err)
	}
	got := strings.Join(args, " ")
	if got != "backup run --json --operation-id job-abc-123" {
		t.Fatalf("argv = %q", got)
	}
}

func TestPinnedCLIEvidenceAttestationBindsExactNativeBackupResult(t *testing.T) {
	command := &agentpb.StackKitCommand{
		CommandId: "backup-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
	}
	stdout := []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup run","status":"success","data":{"apiVersion":"stackkit.local-backup-snapshot-anchor/v1"}}`)
	result := finishStackKitResult(&agentpb.StackKitResult{}, command, stdout, nil, nil, time.Now())
	digest := sha256.Sum256(stdout)
	if !result.PinnedCliEvidenceVerified || result.PinnedCliEvidenceSha256 != fmt.Sprintf("sha256:%x", digest) {
		t.Fatalf("native evidence attestation = %t %q", result.PinnedCliEvidenceVerified, result.PinnedCliEvidenceSha256)
	}

	tampered := append([]byte(nil), result.CommandResultJson...)
	tampered[len(tampered)-2] ^= 1
	result.CommandResultJson = tampered
	if result.PinnedCliEvidenceSha256 == fmt.Sprintf("sha256:%x", sha256.Sum256(result.CommandResultJson)) {
		t.Fatal("attestation digest unexpectedly followed mutated result bytes")
	}

	wrongSchema := []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup run","status":"success","data":{"apiVersion":"other/v1"}}`)
	invalid := finishStackKitResult(&agentpb.StackKitResult{}, command, wrongSchema, nil, nil, time.Now())
	if invalid.PinnedCliEvidenceVerified || invalid.PinnedCliEvidenceSha256 != "" {
		t.Fatal("agent attested output that was not native backup evidence")
	}
}

// TestBackupRunArgsRequireAnIdempotencyKey covers the transport reality: the
// Guard long poll is at-least-once, so a redelivered command without an
// operation id would cost the customer another snapshot generation every retry.
func TestBackupRunArgsRequireAnIdempotencyKey(t *testing.T) {
	if _, err := stackKitBackupRunArgs(&agentpb.StackKitCommand{}); err == nil {
		t.Fatal("a backup run without a command id must be refused")
	}
	if _, err := stackKitBackupRunArgs(nil); err == nil {
		t.Fatal("a nil command must be refused")
	}
}

// TestBackupRunArgsRefuseArgvEscape keeps the closed command set closed: the
// operation id is the only caller-supplied token in the argv.
func TestBackupRunArgsRefuseArgvEscape(t *testing.T) {
	for _, hostile := range []string{"--force", "-x", "a b", "a/b", `a"b`, "a'b", "a\tb", "a\nb"} {
		if _, err := stackKitBackupRunArgs(&agentpb.StackKitCommand{CommandId: hostile}); err == nil {
			t.Fatalf("operation id %q must be refused", hostile)
		}
	}
}

func TestBackupOperationsMapToFixedArgv(t *testing.T) {
	status, err := stackKitOperationArgs(&agentpb.StackKitCommand{
		Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS,
	})
	if err != nil {
		t.Fatalf("backup status: %v", err)
	}
	if strings.Join(status, " ") != "backup status --json" {
		t.Fatalf("status argv = %v", status)
	}

	run, err := stackKitOperationArgs(&agentpb.StackKitCommand{
		Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		CommandId: "job-1",
	})
	if err != nil {
		t.Fatalf("backup run: %v", err)
	}
	if strings.Join(run, " ") != "backup run --json --operation-id job-1" {
		t.Fatalf("run argv = %v", run)
	}

	restore, err := stackKitOperationArgs(&agentpb.StackKitCommand{
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE,
		CommandId:        "job-1-restore",
		OwnerApproved:    true,
		SnapshotAnchorId: "sha256:" + strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("backup restore: %v", err)
	}
	wantRestore := "backup restore sha256:" + strings.Repeat("a", 64) + " --owner-approve --json --operation-id job-1-restore"
	if strings.Join(restore, " ") != wantRestore {
		t.Fatalf("restore argv = %v", restore)
	}
}

func TestRestoreActivationArgsPreserveOriginalJournalAndAttestReceipt(t *testing.T) {
	command := &agentpb.StackKitCommand{CommandId: "job-activate-2", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE, OwnerApproved: true, RestoreResultId: "sha256:" + strings.Repeat("a", 64), ActivationOperationId: "restore-job-original"}
	command.ExpectedPlanHash = "sha256:" + strings.Repeat("b", 64)
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "backup restore activate "+command.RestoreResultId+" --owner-approve --json --operation-id restore-job-original --expected-plan-hash "+command.ExpectedPlanHash {
		t.Fatalf("activation argv: %v", args)
	}
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER
	args, err = stackKitOperationArgs(command)
	if err != nil || strings.Join(args, " ") != "backup restore recover restore-job-original --rollback --owner-approve --json --expected-plan-hash "+command.ExpectedPlanHash {
		t.Fatalf("recovery argv: %v %v", args, err)
	}
	stdout := []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup restore recover","status":"success","data":{"apiVersion":"stackkit.restore-activation-result/v1"}}`)
	result := finishStackKitResult(&agentpb.StackKitResult{}, command, stdout, nil, nil, time.Now())
	if !result.PinnedCliEvidenceVerified || result.PinnedCliEvidenceSha256 != fmt.Sprintf("sha256:%x", sha256.Sum256(stdout)) {
		t.Fatal("terminal node receipt not attested")
	}
	command.OwnerApproved = false
	if _, err = stackKitOperationArgs(command); err == nil {
		t.Fatal("recovery admitted without approval")
	}
	command.OwnerApproved = true
	command.ActivationOperationId = "--replace"
	if _, err = stackKitOperationArgs(command); err == nil {
		t.Fatal("journal ID escaped closed argv")
	}
}
