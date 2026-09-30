package stackkitcommand

import (
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ManagedBackupPlan is the credential-free subset needed to attest a fresh
// managed backup target. It is tied to a successful generation inspection,
// never an arbitrary artifact or an operator-supplied plan.
func ManagedBackupPlan(result *agentpb.StackKitResult, instance string) ([]byte, string, error) {
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Output string          `json:"output"`
			Plan   json.RawMessage `json:"managed_backup_plan"`
		} `json:"data"`
	}
	if result == nil || !result.Success || json.Unmarshal(result.CommandResultJson, &envelope) != nil || envelope.SchemaVersion != "stackkit.command-result/v1" || envelope.Command != "stackkit plan" || envelope.Status != "success" {
		return nil, "", fmt.Errorf("managed backup requires a successful typed plan")
	}
	var inspection struct {
		APIVersion      string `json:"apiVersion"`
		Kind            string `json:"kind"`
		VerifiedPhase   string `json:"verifiedPhase"`
		ExecutorInvoked bool   `json:"executorInvoked"`
		Binding         struct {
			PlanHash string `json:"planHash"`
		} `json:"binding"`
		Readiness struct {
			Generation struct {
				Status   string   `json:"status"`
				Blockers []string `json:"blockers"`
			} `json:"generation"`
		} `json:"readiness"`
	}
	if json.Unmarshal([]byte(envelope.Data.Output), &inspection) != nil || inspection.APIVersion != "stackkit.plan-inspection/v1" || inspection.Kind != "PlanInspection" || inspection.VerifiedPhase != "generation" || inspection.ExecutorInvoked || inspection.Readiness.Generation.Status != "ready" || len(inspection.Readiness.Generation.Blockers) != 0 {
		return nil, "", fmt.Errorf("managed backup plan inspection is not ready")
	}
	var plan map[string]json.RawMessage
	if len(envelope.Data.Plan) == 0 || len(envelope.Data.Plan) > 2<<20 || json.Unmarshal(envelope.Data.Plan, &plan) != nil {
		return nil, "", fmt.Errorf("managed backup plan is unavailable")
	}
	value := func(key string) string { var result string; _ = json.Unmarshal(plan[key], &result); return result }
	if value("apiVersion") != "stackkit.resolved-plan/v1" || value("kind") != "ResolvedPlan" || instance == "" || value("stackId") != instance || value("planHash") != inspection.Binding.PlanHash || !specHashPattern.MatchString(value("planHash")) {
		return nil, "", fmt.Errorf("managed backup plan identity does not match the inspected instance")
	}
	projection := map[string]json.RawMessage{}
	for _, key := range []string{"apiVersion", "kind", "stackId", "planHash", "backupTargetRequirements", "externalBackupTargetBindings", "backupPolicy"} {
		if raw, ok := plan[key]; ok {
			projection[key] = raw
		}
	}
	raw, err := json.Marshal(projection)
	return raw, value("planHash"), err
}
