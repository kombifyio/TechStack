package jobs

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// localRemovalCurrentPlanField holds, beside a removal the node refused with a
// binding mismatch, what a read-only PLAN of the same StackSpec resolves on the
// node now.
const localRemovalCurrentPlanField = "current_plan"

// inspectLocalRemovalBinding runs after StackKits refused a removal with
// "binding_mismatch at resolvedPlan" (uoe2). The removal resolves under the
// Apply authority; PLAN resolves the same StackSpec and Inventory under the
// read-only one and compares them with the persisted plan too. A PLAN that
// also mismatches means the StackSpec or Inventory changed after the rollout's
// last generate (its spec and inventory hashes name which); a PLAN that passes
// and equals the applied plan means only the Apply authority diverges. The
// probe never changes the node, and its own failure is recorded, not raised.
func inspectLocalRemovalBinding(ctx context.Context, sender StackKitCommandSender, removal StackKitLifecycleRequest, release stackkitrelease.Release, commandID, appliedPlanHash string, refused *agentpb.StackKitResult) map[string]interface{} {
	if refused == nil || !strings.Contains(refused.Stderr, "binding_mismatch") {
		return nil
	}
	inspection := map[string]interface{}{"applied_plan_hash": appliedPlanHash, "spec_path": removal.SpecPath}
	plan := removal
	plan.Operation, plan.OwnerApproved, plan.WorkloadRef = StackKitLifecyclePlan, false, ""
	plan, err := NormalizeStackKitLifecycleRequest(plan)
	if err == nil {
		var result *agentpb.StackKitResult
		_, result, err = sendStackKitLifecycleCommand(ctx, sender, commandID+"-plan", plan, release, "")
		if err == nil {
			recordLocalRemovalPlan(inspection, result, appliedPlanHash)
			return inspection
		}
	}
	inspection["error"] = boundedRedactedNodeStderr(err.Error(), localRemovalStderrBriefBound)
	return inspection
}

func recordLocalRemovalPlan(inspection map[string]interface{}, result *agentpb.StackKitResult, appliedPlanHash string) {
	inspection["success"] = result != nil && result.Success
	if result == nil || !result.Success {
		if result != nil {
			inspection["stderr"] = boundedRedactedNodeStderr(result.Stderr, localRemovalStderrBriefBound)
		}
		return
	}
	var receipt struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	var plan struct {
		Binding struct {
			PlanHash        string `json:"planHash"`
			SpecHash        string `json:"specHash"`
			InventoryHash   string `json:"inventoryHash"`
			DefinitionHash  string `json:"definitionHash"`
			CompilerVersion string `json:"compilerVersion"`
		} `json:"binding"`
	}
	if json.Unmarshal(result.CommandResultJson, &receipt) != nil || json.Unmarshal([]byte(receipt.Data.Output), &plan) != nil {
		inspection["error"] = "PLAN receipt is not a plan inspection"
		return
	}
	binding := plan.Binding
	inspection["plan_hash"], inspection["spec_hash"] = localRemovalPlanHash(binding.PlanHash), localRemovalPlanHash(binding.SpecHash)
	inspection["inventory_hash"], inspection["definition_hash"] = localRemovalPlanHash(binding.InventoryHash), localRemovalPlanHash(binding.DefinitionHash)
	inspection["compiler_version"] = boundedRedactedNodeStderr(binding.CompilerVersion, 64)
	inspection["matches_applied_plan"] = localRemovalPlanHash(binding.PlanHash) != "" && binding.PlanHash == appliedPlanHash
}

// localRemovalPlanHash keeps only a well-formed node-reported hash.
func localRemovalPlanHash(value string) string {
	if stackkitcommand.NativeSpecHashValid(value) {
		return value
	}
	return ""
}
