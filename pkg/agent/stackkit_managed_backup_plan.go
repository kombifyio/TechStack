package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The signed historical CLI proves the old generation; retain its complete
// public provenance separately while the existing parser binds its Plan hash.
func attachSourceGenerationInspection(result *agentpb.StackKitResult) error {
	var envelope map[string]json.RawMessage
	if result == nil || json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return fmt.Errorf("source generation receipt is invalid")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &data) != nil {
		return fmt.Errorf("source generation receipt data is invalid")
	}
	var output string
	if json.Unmarshal(data["output"], &output) != nil {
		return fmt.Errorf("source generation receipt output is invalid")
	}
	if len(output) == 0 || len(output) > 2<<20 {
		return fmt.Errorf("source generation inspection is not bounded")
	}
	var source struct {
		APIVersion string          `json:"apiVersion"`
		Inspection json.RawMessage `json:"inspection"`
	}
	if json.Unmarshal([]byte(output), &source) != nil {
		return fmt.Errorf("source generation inspection is invalid")
	}
	if source.APIVersion == "stackkit.plan-inspection/v1" {
		return nil // Same release: ordinary current CUE inspection.
	}
	if (source.APIVersion != "stackkit.source-generation-inspection/v1" && source.APIVersion != "stackkit.source-generation-inspection/v3") || len(source.Inspection) == 0 {
		return fmt.Errorf("source generation inspection version is invalid")
	}
	data["source_generation_authority"] = json.RawMessage(output)
	data["output"], _ = json.Marshal(string(source.Inspection))
	envelope["data"], _ = json.Marshal(data)
	var err error
	result.CommandResultJson, err = json.Marshal(envelope)
	return err
}

// attachManagedBackupPlan reads only the canonical artifact inspected by PLAN.
// OpenRoot confines all traversal; symlink components are additionally refused.
func attachManagedBackupPlan(workspace, instance string, result *agentpb.StackKitResult) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	const artifact = "deploy/.stackkit/resolved-plan.json"
	for _, path := range []string{"deploy", "deploy/.stackkit", artifact} {
		info, err := root.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed backup plan artifact is unavailable")
		}
	}
	file, err := root.Open(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return fmt.Errorf("managed backup plan artifact is not bounded")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 {
		return fmt.Errorf("managed backup plan artifact is not bounded")
	}
	var plan map[string]json.RawMessage
	if json.Unmarshal(raw, &plan) != nil {
		return fmt.Errorf("managed backup plan artifact is invalid")
	}
	projection := map[string]json.RawMessage{}
	for _, key := range []string{"apiVersion", "kind", "stackId", "planHash", "backupTargetRequirements", "externalBackupTargetBindings", "backupPolicy"} {
		if value, ok := plan[key]; ok {
			projection[key] = value
		}
	}
	if err := appendPlanExecutionProjection(plan, projection); err != nil {
		return err
	}
	projected, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return fmt.Errorf("managed backup plan receipt is invalid")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &data) != nil {
		return fmt.Errorf("managed backup plan receipt data is invalid")
	}
	data["managed_backup_plan"] = projected
	envelope["data"], _ = json.Marshal(data)
	result.CommandResultJson, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, _, err = stackkitcommand.ManagedBackupPlan(result, instance)
	return err
}
