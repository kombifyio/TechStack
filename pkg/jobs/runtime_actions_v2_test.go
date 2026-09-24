package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
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

func TestArchitectureV2ExecutionPayloadBindsWorkspaceAuthority(t *testing.T) {
	specPath, tofuDir, planHash := architectureV2TestWorkspace(t)
	req := RuntimeActionRequest{
		StackID:       "4020ce46-4f77-479f-8790-36392e63a4a7",
		StackName:     "demo",
		StackKit:      "cloud-kit",
		TenantID:      "tenant-1",
		OwnerID:       "owner-1",
		StackSpecPath: specPath,
		TofuDir:       tofuDir,
		RuntimeTarget: &RuntimeActionTarget{Host: "203.0.113.10", User: "root", Port: 22, PrivateKey: "key"},
	}
	payload, err := architectureV2ExecutionPayload(string(runtimeaction.ActionStackKitRollout), req)
	if err != nil {
		t.Fatalf("architectureV2ExecutionPayload: %v", err)
	}
	// The admitted identity is the governed plan's, not Techstack's stack UUID:
	// the server compares stack_id against the stackId of its own resolution.
	if payload.StackID != "demo" {
		t.Fatalf("stack_id = %q, want the governed plan identity", payload.StackID)
	}
	if payload.ExpectedPlanHash != planHash {
		t.Fatalf("expected_plan_hash = %q, want %q", payload.ExpectedPlanHash, planHash)
	}
	if payload.TofuDir != tofuDir || payload.RuntimeTarget == nil || payload.RuntimeTarget.Host != "203.0.113.10" {
		t.Fatalf("execution contract incomplete: %#v", payload)
	}
	var decodedSpec map[string]any
	if err := json.Unmarshal(payload.StackSpec, &decodedSpec); err != nil {
		t.Fatalf("stack_spec is not JSON: %v", err)
	}
	if decodedSpec["apiVersion"] != "stackkit/v2alpha1" {
		t.Fatalf("stack_spec lost its canonical identity: %v", decodedSpec)
	}
	// Without a workspace inventory file the envelope omits the document so
	// the authority resolves against its canonical empty Inventory.
	if len(payload.Inventory) != 0 {
		t.Fatalf("inventory should be omitted, got %s", payload.Inventory)
	}
	if err := runtimeaction.ValidateArchitectureV2ExecutionRequest(*payload); err != nil {
		t.Fatalf("envelope does not validate: %v", err)
	}

	workDir := filepath.Dir(specPath)
	if err := os.MkdirAll(filepath.Join(workDir, ".stackkit"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inventory := "schemaVersion: stackkit.inventory/v1\nnodes:\n  main:\n    siteAddress: 192.0.2.10\n"
	if err := os.WriteFile(filepath.Join(workDir, ".stackkit", "inventory.yaml"), []byte(inventory), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	payload, err = architectureV2ExecutionPayload(string(runtimeaction.ActionVerifyRollout), req)
	if err != nil {
		t.Fatalf("architectureV2ExecutionPayload: %v", err)
	}
	if payload.Action != runtimeaction.ArchitectureV2OperationVerify {
		t.Fatalf("action = %q, want verify operation", payload.Action)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload.Inventory, &decoded); err != nil {
		t.Fatalf("inventory is not JSON: %v", err)
	}
	if decoded["schemaVersion"] != "stackkit.inventory/v1" {
		t.Fatalf("inventory lost its identity: %v", decoded)
	}
	nodes, _ := decoded["nodes"].(map[string]any)
	main, _ := nodes["main"].(map[string]any)
	if main["siteAddress"] != "192.0.2.10" {
		t.Fatal("execution lost the observed address used by the admitted DNS listener plan")
	}
}

type nativeV2RestoreCommander struct {
	planHash              string
	mismatchRestore       bool
	missingAttestation    bool
	mismatchedAttestation bool
	configured            bool
	commands              []*agentpb.StackKitCommand
	order                 *[]string
}

type restoreOnlyNativeV2Commander struct{ *nativeV2RestoreCommander }

func (restoreOnlyNativeV2Commander) StackKitRestoreOnly() bool { return true }

func (sender *nativeV2RestoreCommander) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		return &agentpb.StackKitResult{Success: true, CommandResultJson: typedPlanCommandResult(sender.planHash), Release: command.Release}, nil
	}
	data := map[string]any{}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		sender.configured = true
		data = map[string]any{"apiVersion": "stackkit.local-backup-configuration/v1", "ownerRef": "owner-local"}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		// Like StackKits, a snapshot needs the configured owner-bound
		// repository; Apply alone does not configure it.
		if !sender.configured {
			envelope, _ := json.Marshal(map[string]any{
				"schemaVersion": stackkitcommand.CommandResultVersion,
				"command":       stackkitcommand.ResultCommandName(command.Operation),
				"status":        "failed",
			})
			return &agentpb.StackKitResult{
				CommandId: command.CommandId, ExitCode: 1, CommandResultJson: envelope,
				CommandResultSchemaVersion: stackkitcommand.CommandResultVersion,
				EventsSchemaVersion:        stackkitcommand.RolloutEventVersion, Release: command.Release,
				Stderr: "Error: backuplifecycle: backup configuration not found; run stackkit backup configure first",
			}, nil
		}
		data = map[string]any{
			"apiVersion": "stackkit.local-backup-snapshot-anchor/v1",
			"id":         "sha256:" + strings.Repeat("b", 64), "ownerRef": "owner-local",
			"operationId": command.CommandId,
			"lineage":     map[string]any{"binding": map[string]any{"planHash": sender.planHash}},
			"signature":   map[string]any{"ownerRef": "owner-local", "keyId": "owner-key-1", "value": strings.Repeat("A", 86)},
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		if sender.order != nil {
			*sender.order = append(*sender.order, "restore")
		}
		planHash := sender.planHash
		if sender.mismatchRestore {
			planHash = "sha256:" + strings.Repeat("c", 64)
		}
		data = map[string]any{
			"apiVersion": "stackkit.local-backup-restore-result/v1",
			"id":         "sha256:" + strings.Repeat("d", 64), "ownerRef": "owner-local",
			"snapshotAnchorId": command.SnapshotAnchorId, "operationId": command.CommandId,
			"authorizationLineage": map[string]any{"binding": map[string]any{"planHash": planHash}},
			"snapshotLineage":      map[string]any{"binding": map[string]any{"planHash": sender.planHash}},
			"recoveryAnchor":       map[string]any{"snapshotAnchorId": command.SnapshotAnchorId, "operationId": command.CommandId},
			"request":              map[string]any{"snapshotAnchorId": command.SnapshotAnchorId, "operationId": command.CommandId},
			"receipt":              map[string]any{"operationId": command.CommandId, "repositoryContentVerified": true},
			"verification":         map[string]any{"planHash": planHash, "servicesVerified": true, "verifiedAt": time.Now().UTC()},
			"signature":            map[string]any{"ownerRef": "owner-local", "keyId": "owner-key-1", "value": strings.Repeat("A", 86)},
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
	if !sender.missingAttestation {
		digest := sha256.Sum256(envelope)
		result.PinnedCliEvidenceVerified = true
		result.PinnedCliEvidenceSha256 = fmt.Sprintf("sha256:%x", digest)
		if sender.mismatchedAttestation {
			result.PinnedCliEvidenceSha256 = "sha256:" + strings.Repeat("f", 64)
		}
	}
	return result, nil
}

func configureRestoreOnlyCommander(t *testing.T, cfg *ProvisionConfig, order *[]string) {
	t.Helper()
	configureTestStackKitRelease(t)
	t.Setenv("TECHSTACK_STACKKIT_PREP_DISABLED", "1")
	writeCanonicalTemplate(t, "cloud-kit")
	cfg.StackKitCommander = restoreOnlyNativeV2Commander{&nativeV2RestoreCommander{
		planHash: testResolvedPlanHash, order: order,
	}}
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
	t.Setenv(stackKitReleasePinEnv, pinPath)
	t.Setenv(stackKitReleaseCacheEnv, dir)
}

func managedNativeRestoreRollout(t *testing.T, sender *nativeV2RestoreCommander) *deployRollout {
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
			RuntimeActions: RuntimeActions{RestoreDrill: &fakeRuntimeRunner{
				name: "retired-v1", err: errors.New("retired v1 restore drill must not run"),
			}},
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

func TestManagedRestoreDrillPromotesOnlyMatchingNativeV2Evidence(t *testing.T) {
	planHash := "sha256:" + strings.Repeat("a", 64)
	t.Run("matching locally verified identities promote", func(t *testing.T) {
		sender := &nativeV2RestoreCommander{planHash: planHash}
		rollout := managedNativeRestoreRollout(t, sender)
		if err := rollout.runRestoreDrill(context.Background()); err != nil {
			t.Fatalf("runRestoreDrill: %v", err)
		}
		if rollout.finalRuntimePhase != RuntimePhaseVerified {
			t.Fatalf("runtime phase = %q, want verified", rollout.finalRuntimePhase)
		}
		proof := mapFromInterface(rollout.runtimeProof["restore"])
		if proof["status"] != "verified" || proof["mode"] != "native-v2-staged" || proof["plan_hash"] != planHash || proof["stackkit_instance_id"] != "demo" {
			t.Fatalf("restore proof = %#v", proof)
		}
		want := []agentpb.StackKitOperation{
			agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
			agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE,
			agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
			agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE,
		}
		if len(sender.commands) != len(want) {
			t.Fatalf("commands = %d, want %d", len(sender.commands), len(want))
		}
		for index, operation := range want {
			if sender.commands[index].Operation != operation {
				t.Fatalf("command %d = %s, want %s", index, sender.commands[index].Operation, operation)
			}
		}
	})

	t.Run("mismatched plan evidence denies promotion", func(t *testing.T) {
		sender := &nativeV2RestoreCommander{planHash: planHash, mismatchRestore: true}
		rollout := managedNativeRestoreRollout(t, sender)
		if err := rollout.runRestoreDrill(context.Background()); err == nil {
			t.Fatal("mismatched restore evidence must fail closed")
		}
		if rollout.finalRuntimePhase == RuntimePhaseVerified {
			t.Fatal("mismatched restore evidence promoted the runtime")
		}
	})

	t.Run("shape-only Owner signature without pinned CLI attestation denies promotion", func(t *testing.T) {
		sender := &nativeV2RestoreCommander{planHash: planHash, missingAttestation: true}
		rollout := managedNativeRestoreRollout(t, sender)
		if err := rollout.runRestoreDrill(context.Background()); err == nil {
			t.Fatal("unattested restore evidence must fail closed")
		}
		if rollout.finalRuntimePhase == RuntimePhaseVerified {
			t.Fatal("unattested restore evidence promoted the runtime")
		}
	})

	t.Run("attestation for other bytes denies promotion", func(t *testing.T) {
		sender := &nativeV2RestoreCommander{planHash: planHash, mismatchedAttestation: true}
		rollout := managedNativeRestoreRollout(t, sender)
		if err := rollout.runRestoreDrill(context.Background()); err == nil {
			t.Fatal("mismatched pinned CLI evidence digest must fail closed")
		}
		if rollout.finalRuntimePhase == RuntimePhaseVerified {
			t.Fatal("mismatched pinned CLI evidence digest promoted the runtime")
		}
	})
}

func TestManagedRestoreDrillRequiresNativeCommander(t *testing.T) {
	legacyCalls := []string{}
	legacy := &fakeRuntimeRunner{name: "retired-v1", order: &legacyCalls, result: map[string]interface{}{"status": "verified"}}
	queue := NewQueue(1, logger.New("error", ""))
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-native-required", TargetID: "stack-1", Result: map[string]interface{}{}}
	queue.jobs[job.ID] = job
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{RestoreDrill: legacy}},
		job: job, q: queue, managedRuntime: true,
		runtimeProof: map[string]interface{}{}, e2eProof: map[string]any{"phases_completed": []string{}},
		finalRuntimePhase: RuntimePhaseDeployed,
	}
	if err := rollout.runRestoreDrill(context.Background()); err == nil {
		t.Fatal("managed restore accepted the legacy restore runner without a native commander")
	}
	if len(legacyCalls) != 0 || rollout.finalRuntimePhase == RuntimePhaseVerified {
		t.Fatalf("legacy calls = %v, runtime phase = %q", legacyCalls, rollout.finalRuntimePhase)
	}
}

func TestManagedRestoreDrillReusesInstancePlanOperationIdentity(t *testing.T) {
	planHash := "sha256:" + strings.Repeat("a", 64)
	sender := &nativeV2RestoreCommander{planHash: planHash}
	first := managedNativeRestoreRollout(t, sender)
	if err := first.runRestoreDrill(context.Background()); err != nil {
		t.Fatalf("first restore drill: %v", err)
	}
	second := managedNativeRestoreRollout(t, sender)
	second.job.ID = "job-native-restore-repeat"
	if err := second.runRestoreDrill(context.Background()); err != nil {
		t.Fatalf("repeated restore drill: %v", err)
	}
	if len(sender.commands) != 8 {
		t.Fatalf("commands = %d, want two plan/configure/backup/restore sequences", len(sender.commands))
	}
	if sender.commands[2].CommandId != sender.commands[6].CommandId || sender.commands[3].CommandId != sender.commands[7].CommandId {
		t.Fatalf("repeat allocated new backup/restore identities: %q/%q then %q/%q",
			sender.commands[2].CommandId, sender.commands[3].CommandId,
			sender.commands[6].CommandId, sender.commands[7].CommandId)
	}
	if nativeRestoreDrillOperationID("restore", "other-instance", planHash) == sender.commands[3].CommandId ||
		nativeRestoreDrillOperationID("restore", "demo", "sha256:"+strings.Repeat("e", 64)) == sender.commands[3].CommandId {
		t.Fatal("restore operation identity did not separate another instance or admitted plan")
	}
	proof := mapFromInterface(second.runtimeProof["restore"])
	if proof["retention_mode"] != "idempotent-instance-plan-operation" {
		t.Fatalf("retention proof = %#v", proof)
	}
}

func TestArchitectureV2ExecutionPayloadRefusesIncompleteWorkspaces(t *testing.T) {
	specPath, tofuDir, _ := architectureV2TestWorkspace(t)

	if _, err := architectureV2ExecutionPayload("restore_drill", RuntimeActionRequest{StackSpecPath: specPath, TofuDir: tofuDir}); err == nil {
		t.Fatal("restore_drill must not map onto the v2 execution surface")
	}
	if _, err := architectureV2ExecutionPayload(string(runtimeaction.ActionStackKitRollout), RuntimeActionRequest{TofuDir: tofuDir}); err == nil {
		t.Fatal("a payload without the persisted StackSpec must be refused")
	}
	if _, err := architectureV2ExecutionPayload(string(runtimeaction.ActionStackKitRollout), RuntimeActionRequest{StackSpecPath: specPath, StackKit: "cloud-kit"}); err == nil {
		t.Fatal("a payload without the generated workspace must be refused")
	}
	if err := os.Remove(filepath.Join(tofuDir, ".stackkit", "resolved-plan.json")); err != nil {
		t.Fatalf("remove plan: %v", err)
	}
	if _, err := architectureV2ExecutionPayload(string(runtimeaction.ActionStackKitRollout), RuntimeActionRequest{StackSpecPath: specPath, StackKit: "cloud-kit", TofuDir: tofuDir}); err == nil {
		t.Fatal("a workspace without its governed ResolvedPlan must be refused")
	}
}

// Regression: a self-hosted rollout with the typed commander but without a
// StackKits restore-drill action dispatched the unset action and crashed the
// installed Windows client's runtime (journey B2 run 35905142629).
func TestSelfHostedRolloutWithoutRestoreDrillActionSkipsTheDrill(t *testing.T) {
	rollout := managedNativeRestoreRollout(t, &nativeV2RestoreCommander{planHash: "sha256:" + strings.Repeat("a", 64)})
	rollout.managedRuntime = false
	rollout.cfg.RuntimeActions.RestoreDrill = nil
	if err := rollout.runRestoreDrill(context.Background()); err != nil {
		t.Fatalf("self-hosted rollout without a restore-drill action failed: %v", err)
	}
}
