package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"gopkg.in/yaml.v3"
)

// architectureV2OperationForAction maps the runner's action name onto the
// governed Architecture v2 operation. Only rollout and verify exist on the v2
// surface; backup execution awaits its own native v2 contract.
func architectureV2OperationForAction(action string) (runtimeaction.ArchitectureV2Operation, bool) {
	switch runtimeaction.NormalizeAction(action) {
	case runtimeaction.ActionStackKitRollout:
		return runtimeaction.ArchitectureV2OperationRollout, true
	case runtimeaction.ActionVerifyRollout:
		return runtimeaction.ArchitectureV2OperationVerify, true
	default:
		return "", false
	}
}

// architectureV2ExecutionPayload builds the v2 execution envelope from the
// prepared rollout request. The admitted identity comes from the governed
// ResolvedPlan the pinned CLI persisted beside the artifacts: the server
// re-resolves the same StackSpec and Inventory and admits execution only when
// its planHash and stackId match this binding exactly.
func architectureV2ExecutionPayload(action string, req RuntimeActionRequest) (*runtimeaction.ArchitectureV2ExecutionRequest, error) {
	operation, ok := architectureV2OperationForAction(action)
	if !ok {
		return nil, fmt.Errorf("runtime action %q has no Architecture v2 operation", action)
	}
	specPath := strings.TrimSpace(req.StackSpecPath)
	if specPath == "" {
		return nil, errors.New("architecture v2 execution requires the persisted StackSpec")
	}
	canonical, err := canonicalStackSpecFor(specPath, req.StackKit, req.StackName)
	if err != nil {
		return nil, err
	}
	specJSON, err := yamlObjectFileToJSON(canonical.Path)
	if err != nil {
		return nil, fmt.Errorf("encode canonical StackSpec for Architecture v2: %w", err)
	}
	inventoryJSON, err := workspaceInventoryJSON(filepath.Dir(canonical.Path))
	if err != nil {
		return nil, err
	}
	tofuDir := strings.TrimSpace(req.TofuDir)
	if tofuDir == "" {
		return nil, errors.New("architecture v2 execution requires the generated workspace (tofu_dir)")
	}
	planStackID, planHash, err := readArchitectureV2PlanBinding(filepath.Join(tofuDir, ".stackkit", "resolved-plan.json"))
	if err != nil {
		return nil, err
	}
	payload := &runtimeaction.ArchitectureV2ExecutionRequest{
		ArchitectureV2Request: runtimeaction.ArchitectureV2Request{
			APIVersion:       runtimeaction.RuntimeActionAPIVersionV2Alpha1,
			Action:           operation,
			StackID:          planStackID,
			TenantID:         strings.TrimSpace(req.TenantID),
			OwnerID:          strings.TrimSpace(req.OwnerID),
			StackSpec:        specJSON,
			Inventory:        inventoryJSON,
			ExpectedPlanHash: planHash,
		},
		TofuDir:       tofuDir,
		RuntimeTarget: normalizeRuntimeActionTarget(req.RuntimeTarget),
		PlatformNodes: normalizePlatformNodes(req.PlatformNodes),
	}
	if err := runtimeaction.ValidateArchitectureV2ExecutionRequest(*payload); err != nil {
		return nil, fmt.Errorf("architecture v2 execution envelope is invalid: %w", err)
	}
	return payload, nil
}

// readArchitectureV2PlanBinding reads the admitted identity from the governed
// ResolvedPlan: the stackId the authority resolved and the canonical planHash.
func readArchitectureV2PlanBinding(path string) (stackID, planHash string, err error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed governed metadata location inside the generated workspace.
	if err != nil {
		return "", "", fmt.Errorf("read governed ResolvedPlan for Architecture v2 execution: %w", err)
	}
	var identity struct {
		StackID  string `json:"stackId"`
		PlanHash string `json:"planHash"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", "", fmt.Errorf("decode governed ResolvedPlan identity: %w", err)
	}
	if strings.TrimSpace(identity.StackID) == "" || !isSHA256Digest(identity.PlanHash) {
		return "", "", errors.New("governed ResolvedPlan carries no complete Architecture v2 identity")
	}
	return identity.StackID, identity.PlanHash, nil
}

// workspaceInventoryJSON mirrors the pinned CLI's inventory discovery: exactly
// one of .stackkit/inventory.{yaml,json} or inventory.{yaml,json} in the
// workspace, or none at all. Absence returns nil so the authority resolves
// against its canonical empty Inventory — sending "{}" instead would resolve a
// different document and fail the plan-hash gate.
func workspaceInventoryJSON(workDir string) (json.RawMessage, error) {
	candidates := []string{
		filepath.Join(workDir, ".stackkit", "inventory.yaml"),
		filepath.Join(workDir, ".stackkit", "inventory.json"),
		filepath.Join(workDir, "inventory.yaml"),
		filepath.Join(workDir, "inventory.json"),
	}
	selected := make([]string, 0, 1)
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			selected = append(selected, candidate)
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect Architecture v2 Inventory candidate %s: %w", candidate, statErr)
		}
	}
	if len(selected) > 1 {
		return nil, fmt.Errorf("architecture v2 Inventory is ambiguous: %s", strings.Join(selected, ", "))
	}
	if len(selected) == 0 {
		return nil, nil
	}
	inventory, err := yamlObjectFileToJSON(selected[0])
	if err != nil {
		return nil, fmt.Errorf("encode Architecture v2 Inventory: %w", err)
	}
	return inventory, nil
}

// yamlObjectFileToJSON reads a YAML (or JSON) document and re-encodes it as a
// JSON object for the strict v2 wire contract.
func yamlObjectFileToJSON(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- callers pass validated workspace paths.
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if unmarshalErr := yaml.Unmarshal(data, &document); unmarshalErr != nil {
		return nil, fmt.Errorf("decode %s as a YAML object: %w", filepath.Base(path), unmarshalErr)
	}
	if document == nil {
		return nil, fmt.Errorf("%s is empty", filepath.Base(path))
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode %s as JSON: %w", filepath.Base(path), err)
	}
	return encoded, nil
}
