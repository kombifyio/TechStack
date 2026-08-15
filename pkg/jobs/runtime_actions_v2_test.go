package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/logger"
)

func architectureV2TestWorkspace(t *testing.T) (specPath, tofuDir, planHash string) {
	t.Helper()
	workDir := t.TempDir()
	specPath = filepath.Join(workDir, "stack-spec.yaml")
	spec := "apiVersion: stackkit/v2alpha1\nkind: StackSpec\nmetadata:\n  name: demo\ngeneration:\n  outputRoot: deploy\n"
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

func TestArchitectureV2ExecutionPayloadBindsPlanIdentity(t *testing.T) {
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
}

func TestArchitectureV2ExecutionPayloadCarriesWorkspaceInventory(t *testing.T) {
	specPath, tofuDir, _ := architectureV2TestWorkspace(t)
	workDir := filepath.Dir(specPath)
	if err := os.MkdirAll(filepath.Join(workDir, ".stackkit"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inventory := "schemaVersion: stackkit.inventory/v1\nnodes: {}\n"
	if err := os.WriteFile(filepath.Join(workDir, ".stackkit", "inventory.yaml"), []byte(inventory), 0o600); err != nil {
		t.Fatalf("write inventory: %v", err)
	}
	payload, err := architectureV2ExecutionPayload(string(runtimeaction.ActionVerifyRollout), RuntimeActionRequest{
		StackSpecPath: specPath,
		StackKit:      "cloud-kit",
		TofuDir:       tofuDir,
	})
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
}

type retiredRestoreDrillRunner struct{}

func (retiredRestoreDrillRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	_, err := retiredRestoreDrillRunner{}.RunWithResult(ctx, req)
	return err
}

func (retiredRestoreDrillRunner) RunWithResult(context.Context, RuntimeActionRequest) (map[string]interface{}, error) {
	return nil, errors.New(`runtime action restore_drill returned 410: {"error":{"error_code":"legacy_runtime_action_retired"}}`)
}

func TestRestoreDrillRetiredSurfaceKeepsRolloutDeployedNotVerified(t *testing.T) {
	log := logger.New("error", "")
	queue := NewQueue(1, log)
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-restore-retired", Result: map[string]interface{}{}}
	queue.jobs[job.ID] = job
	rollout := &deployRollout{
		cfg:            &ProvisionConfig{RuntimeActions: RuntimeActions{RestoreDrill: retiredRestoreDrillRunner{}}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		runtimeProof:   map[string]interface{}{},
		e2eProof:       map[string]any{},
		actionReq:      RuntimeActionRequest{StackID: "stack-1"},
	}
	if err := rollout.runRestoreDrill(context.Background()); err != nil {
		t.Fatalf("a retired drill surface must not fail the rollout: %v", err)
	}
	proof, ok := rollout.runtimeProof["restore"].(map[string]interface{})
	if !ok || proof["status"] != "not_applicable" ||
		proof["reason_code"] != "restore_drill_retired_pending_native_v2_contract" {
		t.Fatalf("expected an honest not_applicable receipt, got %#v", rollout.runtimeProof["restore"])
	}
	if rollout.finalRuntimePhase == RuntimePhaseVerified {
		t.Fatal("a retired drill must never promote the runtime phase to verified")
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
