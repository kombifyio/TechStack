package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// releaseWithAdvancedCatalog resolves an artifact pin whose release ships the
// stackkit.advanced-operations/v1 catalog beside the pin, as the image does.
func releaseWithAdvancedCatalog(t *testing.T, executable ...[]byte) stackkitrelease.Release {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "stackkit")
	content := []byte("stand-in")
	if len(executable) > 0 {
		content = executable[0]
	}
	if err := os.WriteFile(binary, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	pin, _ := json.Marshal(map[string]any{
		"schemaVersion": stackkitrelease.PinSchemaVersion, "kit": "basement-kit", "version": "v0.47.0",
		"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64),
		"binarySha256": hex.EncodeToString(digest[:]), "binaryPath": binary,
	})
	pinPath := filepath.Join(root, "stackkits-release-pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join("..", "..", "internal", "stackkitrelease", "pinned-advanced-operations.json")) // #nosec G304 G703 -- fixed repository fixture.
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(root, stackkitrelease.AdvancedOperationsFileName), catalog, 0o600); writeErr != nil { // #nosec G703 -- test temp directory.
		t.Fatal(writeErr)
	}
	release, err := (stackkitrelease.Cache{Root: root}).ResolvePin(pinPath)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

type recordingAdvancedIssuer struct {
	staticAdvancedIssuer
	issued []advancedissuer.Request
}

func (issuer *recordingAdvancedIssuer) Issue(ctx context.Context, request advancedissuer.Request) (advancedissuer.Capability, error) {
	issuer.issued = append(issuer.issued, request)
	return issuer.staticAdvancedIssuer.Issue(ctx, request)
}

// advancedResultSender answers every command with one fixed command result.
type advancedResultSender struct {
	commands []*agentpb.StackKitCommand
	result   string
	success  bool
}

func (sender *advancedResultSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	exitCode := int32(0)
	if !sender.success {
		exitCode = 3
	}
	return &agentpb.StackKitResult{
		CommandId: command.CommandId, Success: sender.success, ExitCode: exitCode, Release: command.Release,
		CommandResultJson: []byte(sender.result), CommandResultSchemaVersion: "stackkit.command-result/v1",
	}, nil
}

func advancedRollbackJob(t *testing.T, sender StackKitCommandSender, issuer AdvancedIssuer) (*Job, error) {
	t.Helper()
	release := releaseWithAdvancedCatalog(t)
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender: sender, AdvancedIssuer: issuer,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		Operation: StackKitLifecycleRollback, StackKit: "basement-kit", SpecPath: "stack-spec.yaml",
		RollbackTargetRef: "sha256:" + strings.Repeat("c", 64),
	}
	job := &Job{ID: "job-rollback-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	return job, handler(context.Background(), job, NewQueue(0, nil))
}

// changeSetSender answers a change-set create and its apply like the CLI.
type changeSetSender struct{ commands []*agentpb.StackKitCommand }

func (sender *changeSetSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	id := "sha256:" + strings.Repeat("e", 64)
	result := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, Release: command.Release, CommandResultSchemaVersion: "stackkit.command-result/v1"}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE {
		result.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit advanced change-set create","status":"success","data":{"schemaVersion":"stackkit.advanced-change-set/v2","changeSetId":"` + id + `","path":".stackkit/advanced/change-sets/e.json"}}`)
		result.AdvancedChangeSetSha256 = "sha256:" + strings.Repeat("f", 64)
		return result, nil
	}
	result.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit advanced change-set apply","status":"success","data":{"schemaVersion":"stackkit.advanced-mutation/v1","changeSetId":"` + id + `","changeSetResult":{"schemaVersion":"stackkit.change-set-result/v1","status":"converged"}}}`)
	return result, nil
}

// StackKits binds a change set to the capability that created it, so one
// operator operation carries one capability allowing exactly its steps.
func TestAdvancedOperationCarriesOneCapabilityScopedToExactlyItsSteps(t *testing.T) {
	sender, issuer := &changeSetSender{}, &recordingAdvancedIssuer{}
	release := releaseWithAdvancedCatalog(t)
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender: sender, AdvancedIssuer: issuer,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		Operation: StackKitLifecycleAdvancedChangeSet, StackKit: "basement-kit", SpecPath: "stack-spec.yaml",
		CandidateSpecJSON: []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"basement-kit"}}`),
	}
	job := &Job{ID: "job-change-set-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatalf("advanced_change_set: %v", err)
	}
	if len(issuer.issued) != 1 || strings.Join(issuer.issued[0].Operations, ",") != "terramate.change-set.create,terramate.change-set.apply" ||
		issuer.issued[0].OwnerRef != "owner/local/00112233445566778899aabbccddeeff" || issuer.issued[0].StackID != "cloud-stack" ||
		issuer.issued[0].TTL != advancedissuer.DefaultTTL {
		t.Fatalf("issued capabilities = %+v, want one create+apply capability for the bound stack and Owner", issuer.issued)
	}
	want := `{"allowedOperations":["terramate.change-set.create","terramate.change-set.apply"]}`
	if len(sender.commands) != 2 || string(sender.commands[0].AdvancedCapability) != want || string(sender.commands[1].AdvancedCapability) != want ||
		sender.commands[1].ChangeSetSha256 != "sha256:"+strings.Repeat("f", 64) {
		t.Fatalf("dispatched commands = %+v, want create and apply carrying the same capability and the stored digest", sender.commands)
	}
}

// The agent's command deadline must leave room for the target's own deadline
// and the compensating rollback, or Core can kill recovery before it reports.
func TestAdvancedChangeSetDispatchAllowsTargetRecovery(t *testing.T) {
	sender := &changeSetSender{}
	release := releaseWithAdvancedCatalog(t)
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender: sender, AdvancedIssuer: &recordingAdvancedIssuer{},
		releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		Operation: StackKitLifecycleAdvancedChangeSet, StackKit: "basement-kit", SpecPath: "stack-spec.yaml",
		CandidateSpecJSON: []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"basement-kit"}}`),
	}
	job := &Job{ID: "job-change-set-recovery-budget", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatalf("advanced_change_set: %v", err)
	}
	for _, command := range sender.commands {
		if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY {
			continue
		}
		budget := time.Duration(command.TimeoutSeconds) * time.Second
		if budget <= 25*time.Minute || budget > 30*time.Minute {
			t.Fatalf("Advanced mutation agent budget %s cannot complete bounded target and recovery", budget)
		}
		return
	}
	t.Fatal("Advanced change-set apply was not dispatched")
}

func TestAdvancedDenialFailsTheJobWithTheStackKitsReasonCode(t *testing.T) {
	sender := &advancedResultSender{result: `{"schemaVersion":"stackkit.command-result/v1","command":"stackkit advanced rollback run","status":"denied","data":{"schemaVersion":"stackkit.operation-denial/v1","operation":"rollback.coordinated","mode":"advanced","reasonCode":"advanced_capability_expired","message":"expired"}}`}
	job, err := advancedRollbackJob(t, sender, &recordingAdvancedIssuer{})
	if err == nil {
		t.Fatal("a denied rollback completed")
	}
	queue := NewQueue(0, nil)
	job.MaxAttempts, job.Attempts, job.State = 1, 1, JobStateRunning
	queue.handleJobExecutionError(context.Background(), job, err)
	snapshot := job.Snapshot()
	if snapshot.State != JobStateFailed || snapshot.Result["reason_code"] != "advanced_capability_expired" {
		t.Fatalf("job state %s reason %v, want failed with advanced_capability_expired", snapshot.State, snapshot.Result["reason_code"])
	}
}
