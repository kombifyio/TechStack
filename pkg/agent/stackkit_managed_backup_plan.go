package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

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
