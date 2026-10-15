package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func architectureV2TestWorkspace(t *testing.T) (specPath, tofuDir, planHash string) {
	t.Helper()
	workDir := t.TempDir()
	specPath = filepath.Join(workDir, "stack-spec.yaml")
	spec := "apiVersion: stackkit/v2alpha1\nkind: StackSpec\nkit:\n  slug: cloud-kit\nmetadata:\n  name: demo\ngeneration:\n  outputRoot: deploy\n"
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	tofuDir = filepath.Join(workDir, "deploy")
	if err := os.MkdirAll(filepath.Join(tofuDir, ".stackkit"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planHash = "sha256:" + strings.Repeat("a", 64)
	plan := `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"demo","planHash":"` + planHash + `","kit":{"slug":"cloud-kit"}}`
	if err := os.WriteFile(filepath.Join(tofuDir, ".stackkit", "resolved-plan.json"), []byte(plan), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return specPath, tofuDir, planHash
}

// typedLifecycleCommander answers a whole rollout like the pinned CLI on the
// enrolled node: init, trust import, generate, plan, apply, verify, backup
// configure, backup run and the Advanced restore.drill. order records the
// mutating steps as rollout, verify and restore.
type typedLifecycleCommander struct {
	planHash string
	commands []*agentpb.StackKitCommand
	order    *[]string
	// applyErr fails the apply dispatch; verifyStatus overrides the status
	// verify reports.
	applyErr     error
	verifyStatus string
}

// applies counts the dispatched apply commands.
func (sender *typedLifecycleCommander) applies() int {
	count := 0
	for _, command := range sender.commands {
		if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
			count++
		}
	}
	return count
}

func (sender *typedLifecycleCommander) record(step string) {
	if sender.order != nil {
		*sender.order = append(*sender.order, step)
	}
}

func (sender *typedLifecycleCommander) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		return &agentpb.StackKitResult{Success: true, CommandResultJson: typedPlanCommandResult(sender.planHash), Release: command.Release}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT, agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND, agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY, agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY:
		commandResult := typedStackKitFakeSuccessResult(command)
		switch command.Operation {
		case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
			sender.record("rollout")
			if sender.applyErr != nil {
				return nil, sender.applyErr
			}
		case agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY:
			sender.record("verify")
			if sender.verifyStatus != "" {
				commandResult, _ = json.Marshal(map[string]any{
					"schemaVersion": stackkitcommand.CommandResultVersion, "command": "stackkit verify",
					"status": "success", "data": map[string]any{"status": sender.verifyStatus},
				})
			}
		}
		return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
	}
	data := map[string]any{}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		data = map[string]any{"apiVersion": "stackkit.local-backup-configuration/v1", "ownerRef": "owner-local"}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		data = map[string]any{
			"apiVersion": "stackkit.local-backup-snapshot-anchor/v1",
			"id":         "sha256:" + strings.Repeat("b", 64), "ownerRef": "owner-local",
			"operationId": command.CommandId,
			"lineage":     map[string]any{"binding": map[string]any{"planHash": sender.planHash}},
			"signature":   map[string]any{"ownerRef": "owner-local", "keyId": "owner-key-1", "value": strings.Repeat("A", 86)},
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		sender.record("restore")
		data = map[string]any{
			"schemaVersion": "stackkit.restore-drill-report/v1", "status": "succeeded", "drillId": command.CommandId,
			"activated": false, "stagingRemoved": true, "anchorId": sender.planHash, "restoreResultId": sender.planHash,
			"capabilityId": "capability-1",
		}
	default:
		return nil, fmt.Errorf("unexpected operation %s", command.Operation)
	}
	envelope, _ := json.Marshal(map[string]any{
		"schemaVersion": stackkitcommand.CommandResultVersion,
		"command":       stackkitcommand.ResultCommandName(command.Operation),
		"status":        "success", "data": data,
	})
	result := &agentpb.StackKitResult{
		CommandId: command.CommandId, Success: true, CommandResultJson: envelope,
		CommandResultSchemaVersion: stackkitcommand.CommandResultVersion,
		EventsSchemaVersion:        stackkitcommand.RolloutEventVersion, Release: command.Release,
	}
	digest := sha256.Sum256(envelope)
	result.PinnedCliEvidenceVerified = true
	result.PinnedCliEvidenceSha256 = fmt.Sprintf("sha256:%x", digest)
	return result, nil
}

// configureTypedLifecycle gives a deploy test the production rollout seam:
// the pinned release with its Advanced catalog, the Advanced issuer and a
// typed commander that runs the whole lifecycle.
func configureTypedLifecycle(t *testing.T, cfg *ProvisionConfig, order *[]string) {
	t.Helper()
	configureTestStackKitRelease(t)
	t.Setenv("TECHSTACK_STACKKIT_PREP_DISABLED", "1")
	root := writeCanonicalTemplate(t, "cloud-kit")
	for _, kit := range []string{DefaultBasementKitRef, "modern-homelab"} {
		other := writeCanonicalTemplate(t, kit)
		if err := os.Rename(filepath.Join(other, kit), filepath.Join(root, kit)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(stackKitSpecTemplateEnv, root)
	cfg.StackKitCommander = &typedLifecycleCommander{planHash: testResolvedPlanHash, order: order}
	cfg.ManagedStackKitInventory = &recordingManagedStackKitInventoryBuilder{}
	cfg.AdvancedIssuer = staticAdvancedIssuer{}
}

func configureTestStackKitRelease(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "stackkit")
	content := []byte("test stackkit release")
	if err := os.WriteFile(binary, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	pin, err := json.Marshal(stackkitrelease.Pin{
		SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "cloud-kit", Version: "v0.37.1",
		Platform:      stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		ArchiveSHA256: digest, IndexSHA256: digest, BinarySHA256: digest, BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(dir, "pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestAdvancedCatalog(t, dir)
	t.Setenv(stackKitReleasePinEnv, pinPath)
	t.Setenv(stackKitReleaseCacheEnv, dir)
}

// writeTestAdvancedCatalog places the pinned release's Advanced operations
// catalog beside a test release pin, as the image build does.
func writeTestAdvancedCatalog(t *testing.T, dir string) {
	t.Helper()
	catalog, err := os.ReadFile(filepath.Join("..", "..", "internal", "stackkitrelease", "testdata", "advanced-operations-v0.50.0.json")) // #nosec G304 -- fixed repository fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stackkitrelease.AdvancedOperationsFileName), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
}

func managedNativeRestoreRollout(t *testing.T, sender *typedLifecycleCommander) *deployRollout {
	t.Helper()
	configureTestStackKitRelease(t)
	specPath, _, _ := architectureV2TestWorkspace(t)
	queue := NewQueue(1, logger.New("error", ""))
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-native-restore", TargetID: "stack-techstack-1", Result: map[string]interface{}{}}
	queue.jobs[job.ID] = job
	return &deployRollout{
		cfg: &ProvisionConfig{
			StackKitCommander: sender,
			AdvancedIssuer:    staticAdvancedIssuer{},
		}, job: job, q: queue,
		managedRuntime: true, unifiedSpec: &core.UnifiedSpec{StackKit: "cloud-kit"},
		runtimeProof: map[string]interface{}{}, e2eProof: map[string]any{"phases_completed": []string{}},
		finalRuntimePhase: RuntimePhaseDeployed,
		actionReq: RuntimeActionRequest{
			StackID: job.TargetID, StackName: "demo", StackKit: "cloud-kit",
			TenantID: "tenant-1", OwnerID: "owner-1", StackSpecPath: specPath,
			TechStackEnrollment: &TechStackEnrollment{RuntimeAgentID: "agent-1"},
		},
	}
}

// Managed and self-hosted rollouts both promote only through the pinned
// release's Advanced restore.drill; the native staged restore never runs.
func TestRolloutRestoreDrillRunsTheAdvancedDrill(t *testing.T) {
	planHash := "sha256:" + strings.Repeat("a", 64)
	for _, managed := range []bool{true, false} {
		sender := &typedLifecycleCommander{planHash: planHash}
		rollout := managedNativeRestoreRollout(t, sender)
		rollout.managedRuntime = managed
		if err := rollout.runRestoreDrill(context.Background()); err != nil {
			t.Fatalf("managed=%v runRestoreDrill: %v", managed, err)
		}
		proof := mapFromInterface(rollout.runtimeProof["restore"])
		if rollout.finalRuntimePhase != RuntimePhaseVerified || proof["mode"] != "advanced-restore-drill" || proof["plan_hash"] != planHash {
			t.Fatalf("managed=%v phase %q proof %#v, want a verified Advanced drill for the node plan", managed, rollout.finalRuntimePhase, proof)
		}
		for _, command := range sender.commands {
			if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN || command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE {
				t.Fatalf("managed=%v dispatched the native staged restore %s", managed, command.Operation)
			}
		}
	}
}

func TestRestoreDrillRequiresTheTypedCommander(t *testing.T) {
	queue := NewQueue(1, logger.New("error", ""))
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-native-required", TargetID: "stack-1", Result: map[string]interface{}{}}
	queue.jobs[job.ID] = job
	for _, managed := range []bool{true, false} {
		rollout := &deployRollout{
			cfg: &ProvisionConfig{AdvancedIssuer: staticAdvancedIssuer{}}, job: job, q: queue, managedRuntime: managed,
			runtimeProof: map[string]interface{}{}, e2eProof: map[string]any{"phases_completed": []string{}},
			finalRuntimePhase: RuntimePhaseDeployed,
		}
		if err := rollout.runRestoreDrill(context.Background()); err == nil || rollout.finalRuntimePhase == RuntimePhaseVerified {
			t.Fatalf("managed=%v restore drill without a typed commander: err %v, phase %q", managed, err, rollout.finalRuntimePhase)
		}
	}
}
