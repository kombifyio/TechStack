package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

const managedInventoryRefreshResultField = "managed_inventory_refresh"

// Refresh the admitted process pin through the same producer used by rollout.
// Runtime convergence can replace Operations independently of the old Inventory;
// only a new canonical generation may adopt that executable into its plan.
func prepareManagedGenerate(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, req StackKitLifecycleRequest, release stackkitrelease.Release) (StackKitLifecycleRequest, map[string]interface{}, error) {
	if req.Operation != StackKitLifecycleGenerate || req.StackKit != "cloud-kit" || req.ManagedRuntimeLeaseID == "" {
		return req, nil, nil
	}
	if cfg.ManagedStackKitInventory == nil || cfg.ManagedAddressAuthority == nil || req.StackKitInstanceID == "" || len(req.InventoryJSON) != 0 {
		return req, nil, fmt.Errorf("managed generation requires existing runtime and Inventory authority")
	}
	current, err := cfg.ManagedAddressAuthority(ctx, req)
	if err != nil {
		return req, nil, err
	}
	if current.LeaseID != req.ManagedRuntimeLeaseID || current.ServerID == "" || current.ServerID != req.ManagedRuntimeServerID || current.Generation <= 0 || current.Rollout.StackKit != req.StackKit || current.Rollout.SpecPath != req.SpecPath {
		return req, nil, fmt.Errorf("managed generation differs from the current runtime binding")
	}
	var cachePreparation map[string]interface{}
	if stackkitcommand.ReleaseAtLeast(release.Receipt().Version, stackkitcommand.MinSourceGenerationCachePreparationRelease) {
		if req.DryRun || req.Offline || req.TargetRelease != "" || len(req.CandidateSpecJSON) != 0 {
			return req, nil, fmt.Errorf("source cache preparation cannot run with dry-run, offline, or replacement intent")
		}
		cacheReq := req
		cacheReq.Operation, cacheReq.OwnerApproved = stackKitStepPrepareSourceGeneration, false
		command, result, err := sendStackKitLifecycleCommand(ctx, cfg.Sender, job.ID+"-source-cache", cacheReq, release, "")
		if err != nil {
			return req, nil, err
		}
		if result == nil || result.CommandId != command.CommandId || !proto.Equal(result.Release, command.Release) {
			return req, nil, fmt.Errorf("source cache preparation has a different command or release")
		}
		normalized, err := normalizeStackKitLifecycleResult(cacheReq, result)
		if err != nil {
			return req, nil, err
		}
		job.mutateResult(func(result map[string]interface{}) { result["managed_source_cache_preparation"] = normalized })
		cachePreparation = normalized
		if !result.Success {
			// An unsuccessful preparation may have materialized a verified cache.
			// Preserve its result; never retry it or continue to PLAN / renewal.
			return req, nil, &typedStackKitOperationError{
				Operation: stackKitStepPrepareSourceGeneration, CommandID: command.CommandId,
				Err:           fmt.Errorf("StackKits source cache preparation failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr)),
				AgentTimedOut: stackKitResultTimedOut(result), Failure: typedStackKitFailureFromResult(result),
			}
		}
	}
	planReq := req
	planReq.Operation, planReq.OwnerApproved = StackKitLifecyclePlan, false
	// Older admitted CLIs retain strict current-authority preparation. Only a
	// supporting release may inspect historical custody; never retry a denial
	// through another mode.
	sourceInspection := stackkitcommand.ReleaseAtLeast(release.Receipt().Version, stackkitcommand.MinSourceGenerationInspectionRelease)
	planReq.sourceGenerationInspection = sourceInspection
	command, result, err := sendStackKitLifecycleCommand(ctx, cfg.Sender, job.ID+"-inventory-plan", planReq, release, "")
	if err != nil {
		return req, nil, err
	}
	if result == nil || result.CommandId != command.CommandId || !proto.Equal(result.Release, command.Release) {
		return req, nil, fmt.Errorf("managed generation plan has a different command or release")
	}
	if !result.Success {
		// Keep the admitted prerequisite denial after Guard acknowledges and
		// compacts its journal. No Inventory or backup renewal follows a failure.
		normalized, err := normalizeStackKitLifecycleResult(planReq, result)
		if err != nil {
			return req, nil, err
		}
		job.mutateResult(func(result map[string]interface{}) { result["managed_inventory_plan"] = normalized })
		return req, nil, &typedStackKitOperationError{
			Operation: StackKitLifecyclePlan, CommandID: command.CommandId,
			Err:           fmt.Errorf("StackKits plan failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr)),
			AgentTimedOut: stackKitResultTimedOut(result), Failure: typedStackKitFailureFromResult(result),
		}
	}
	plan, planHash, err := stackkitcommand.ManagedSourceGenerationBackupPlan(result, req.StackKitInstanceID)
	if err != nil {
		return req, nil, err
	}
	normalized, err := normalizeStackKitLifecycleResult(planReq, result)
	if err != nil {
		return req, nil, err
	}
	job.mutateResult(func(result map[string]interface{}) { result["managed_inventory_plan"] = normalized })
	receipt := release.Receipt()
	if required, requiredErr := managedBackupTargetInventoryRequired(plan); requiredErr != nil {
		return req, nil, fmt.Errorf("inspect managed backup target requirements: %w", requiredErr)
	} else if required {
		// The node issued its own backup binding and it expires. Re-deliver the
		// same sealed credentials so the node renews it over identical custody;
		// it refuses different credentials, and the references it returns must be
		// the ones recorded when it first issued them.
		delivery := managedBackupDeliveryOf(cfg.ManagedStackKitInventory)
		if delivery == nil {
			return req, nil, fmt.Errorf("managed backup delivery authority is unavailable")
		}
		renewal := req
		renewal.sourceGenerationInspection = sourceInspection
		if _, err := deliverManagedBackupTarget(ctx, cfg.Sender, job.ID+"-backup-renewal", renewal, release, delivery, StackKitLifecycleBackupTargetRebind); err != nil {
			return req, nil, err
		}
	}
	inventory, err := cfg.ManagedStackKitInventory.Build(ctx, ManagedStackKitInventoryRequest{
		TenantID: req.TenantID, StackID: req.StackID, ResolvedPlan: plan,
	})
	if err != nil {
		return req, nil, fmt.Errorf("refresh managed generation Inventory: %w", err)
	}
	latest, err := cfg.ManagedAddressAuthority(ctx, req)
	if err != nil || latest != current {
		return req, nil, fmt.Errorf("managed runtime changed during Inventory preparation")
	}
	if len(inventory) == 0 {
		return req, nil, fmt.Errorf("managed generation Inventory is unavailable")
	}
	req.InventoryJSON = inventory
	proof := map[string]interface{}{
		"source_plan_hash": planHash, "inventory_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256(inventory)),
		"server_id": current.ServerID, "lease_id": current.LeaseID, "generation": current.Generation,
		"release": receipt.Version,
	}
	if cachePreparation != nil {
		proof["source_cache_preparation"] = cachePreparation
	}
	return req, proof, nil
}
