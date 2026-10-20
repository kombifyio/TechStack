package specv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FeasibilityAPIVersion is the StackKits report contract Techstack reads.
const FeasibilityAPIVersion = "stackkit.kombify.io/feasibility/v1"

// ErrFeasibilityUnsupported means the pinned StackKits release has no
// feasibility operation; callers must treat the proposal as unverified.
var ErrFeasibilityUnsupported = errors.New("specv2: the pinned StackKits release has no feasibility operation")

// FeasibilityChecker validates a layout proposal against exact inventory
// through the pinned StackKits CLI. Techstack reads only this structured
// evidence and never interprets a ResolvedPlan (boundary §15.2).
type FeasibilityChecker interface {
	Feasibility(ctx context.Context, spec map[string]any, inventory InventoryDocument) (FeasibilityReport, error)
}

// InventoryDocument is a stackkit.inventory/v1 document: exact node facts
// keyed by StackSpec node id.
type InventoryDocument struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Nodes         map[string]InventoryNodeFact `json:"nodes"`
}

// InventoryNodeFact carries only facts Techstack observed; absent facts stay
// absent so StackKits reports them as unverified rather than satisfied.
type InventoryNodeFact struct {
	Arch                        string                 `json:"arch,omitempty"`
	AMD64MicroarchitectureLevel int                    `json:"amd64MicroarchitectureLevel,omitempty"`
	CPUCores                    int                    `json:"cpuCores,omitempty"`
	RAMGB                       int                    `json:"ramGB,omitempty"`
	StorageGB                   int                    `json:"storageGB,omitempty"`
	Accelerators                []InventoryAccelerator `json:"accelerators,omitempty"`
}

// InventoryAccelerator is one observed GPU.
type InventoryAccelerator struct {
	Vendor string `json:"vendor"`
}

// FeasibilityReport is the StackKits feasibility evidence.
type FeasibilityReport struct {
	APIVersion      string                `json:"apiVersion"`
	Kind            string                `json:"kind"`
	CLIVersion      string                `json:"cliVersion"`
	SpecDigest      string                `json:"specDigest"`
	InventoryDigest string                `json:"inventoryDigest"`
	Outcome         string                `json:"outcome"`
	Workloads       []FeasibilityWorkload `json:"workloads"`
	Nodes           []FeasibilityNode     `json:"nodes,omitempty"`
	Errors          []FeasibilityError    `json:"errors,omitempty"`
}

// FeasibilityWorkload is the verdict for one workload.
type FeasibilityWorkload struct {
	Workload          string              `json:"workload"`
	Modules           []string            `json:"modules,omitempty"`
	RequestedNodeRefs []string            `json:"requestedNodeRefs,omitempty"`
	ResolvedNodeRefs  []string            `json:"resolvedNodeRefs,omitempty"`
	Verdict           string              `json:"verdict"`
	Reasons           []FeasibilityReason `json:"reasons,omitempty"`
}

// FeasibilityReason is one StackKits reason.
type FeasibilityReason struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	NodeRef string `json:"nodeRef,omitempty"`
	Axis    string `json:"axis,omitempty"`
}

// FeasibilityNode is the capacity verdict of one node.
type FeasibilityNode struct {
	NodeRef     string   `json:"nodeRef"`
	Verdict     string   `json:"verdict"`
	ReasonCodes []string `json:"reasonCodes,omitempty"`
}

// FeasibilityError is a compile error not attributable to one workload.
type FeasibilityError struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

type commandResultEnvelope struct {
	SchemaVersion string          `json:"schemaVersion"`
	Command       string          `json:"command"`
	Status        string          `json:"status"`
	Data          json.RawMessage `json:"data"`
}

// Feasibility runs `stackkit --no-log --chdir <dir> feasibility --spec
// stack-spec.yaml --inventory inventory.json --json` in a scratch workspace.
// The operation is read-only on the StackKits side.
func (v *CLIValidator) Feasibility(ctx context.Context, spec map[string]any, inventory InventoryDocument) (FeasibilityReport, error) {
	if err := RequireCanonicalV2(spec); err != nil {
		return FeasibilityReport{}, err
	}
	if v == nil || strings.TrimSpace(v.Binary) == "" {
		return FeasibilityReport{}, fmt.Errorf("specv2: validator not configured")
	}
	workDir, err := os.MkdirTemp("", "specv2-feasibility-")
	if err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: create feasibility workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	handoff := deepCopyValue(spec).(map[string]any)
	DropBindingRejectedFields(handoff)
	document, err := json.Marshal(handoff)
	if err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: encode proposal: %w", err)
	}
	if inventory.SchemaVersion == "" {
		inventory.SchemaVersion = "stackkit.inventory/v1"
	}
	facts, err := json.Marshal(inventory)
	if err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: encode inventory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "stack-spec.yaml"), document, 0o600); err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: write proposal: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "inventory.json"), facts, 0o600); err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: write inventory: %w", err)
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"--no-log", "--chdir", workDir, "feasibility", "--spec", "stack-spec.yaml", "--inventory", "inventory.json", "--json"}
	stdout, stderr, runErr := v.runCLIStdout(runCtx, args, workDir)
	if runCtx.Err() != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: StackKits feasibility timed out after %s: %w", timeout, runCtx.Err())
	}
	report, parseErr := parseFeasibility(stdout)
	if parseErr == nil {
		return report, nil
	}
	combined := strings.ToLower(string(stdout) + string(stderr))
	if runErr != nil && (strings.Contains(combined, "unknown command") || strings.Contains(combined, "unknown flag")) {
		return FeasibilityReport{}, ErrFeasibilityUnsupported
	}
	if runErr != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: StackKits feasibility failed: %w: %s", runErr, tailText(append(stdout, stderr...), validateOutputTail))
	}
	return FeasibilityReport{}, parseErr
}

func parseFeasibility(stdout []byte) (FeasibilityReport, error) {
	var envelope commandResultEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &envelope); err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: feasibility output is not a command result: %w", err)
	}
	if envelope.SchemaVersion != "stackkit.command-result/v1" || envelope.Status != "success" || len(envelope.Data) == 0 {
		return FeasibilityReport{}, fmt.Errorf("specv2: feasibility returned status %q", envelope.Status)
	}
	var report FeasibilityReport
	if err := json.Unmarshal(envelope.Data, &report); err != nil {
		return FeasibilityReport{}, fmt.Errorf("specv2: decode feasibility report: %w", err)
	}
	if report.APIVersion != FeasibilityAPIVersion {
		return FeasibilityReport{}, fmt.Errorf("specv2: feasibility report version %q is not %s", report.APIVersion, FeasibilityAPIVersion)
	}
	switch report.Outcome {
	case "feasible", "infeasible", "unknown":
	default:
		return FeasibilityReport{}, fmt.Errorf("specv2: feasibility outcome %q is not part of the contract", report.Outcome)
	}
	return report, nil
}

func (v *CLIValidator) runCLIStdout(ctx context.Context, args []string, workDir string) ([]byte, []byte, error) {
	if v.commandRunner != nil {
		output, err := v.commandRunner(ctx, v.Binary, append([]string(nil), args...), workDir)
		return output, nil, err
	}
	cmd := exec.CommandContext(ctx, v.Binary, args...) // #nosec G204 -- binary comes from pinned-release admission or the operator env; arguments are closed.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// ApplyLayoutNodeRefs binds each planned workload of an accepted layout to
// exactly one StackSpec node: workloads.<id>.placement.nodeRefs = [node].
// It never touches a workload the layout does not name, and it refuses a
// node the spec does not declare.
func ApplyLayoutNodeRefs(spec map[string]any, assignments map[string]string) error {
	if len(assignments) == 0 {
		return nil
	}
	nodes, err := specNodes(spec)
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, raw := range nodes {
		node, _ := raw.(map[string]any)
		if id, _ := node["id"].(string); id != "" {
			declared[id] = true
		}
	}
	workloads, _ := spec["workloads"].(map[string]any)
	for workload, nodeRef := range assignments {
		if !declared[nodeRef] {
			return fmt.Errorf("specv2: layout names node %q that the spec does not declare", nodeRef)
		}
		entry, ok := workloads[workload].(map[string]any)
		if !ok {
			return fmt.Errorf("specv2: layout names workload %q that the proposal does not select", workload)
		}
		placement, _ := entry["placement"].(map[string]any)
		if placement == nil {
			placement = map[string]any{}
		}
		placement["nodeRefs"] = []any{nodeRef}
		entry["placement"] = placement
	}
	return nil
}
