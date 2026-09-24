package portinventory

import "time"

// EvaluateHostBaseline joins the authenticated current-Node observation with
// compiler requirements. It does not allocate, infer application ownership, or
// treat network exposure as occupancy. StackKits rechecks exact local ownership
// at its own mutation boundary, including bindings invisible to a socket scan.
func EvaluateHostBaseline(inventory Inventory, requirements []Requirement, kitDeploymentID string, now time.Time) error {
	if len(requirements) == 0 {
		return nil
	}
	if inventory.ObservedAt == nil || inventory.ExpiresAt == nil || inventory.ObservedAt.After(now) || !inventory.ExpiresAt.After(now) || !inventory.ListenersComplete {
		return baselineConflict(requirements[0], "host_baseline_unverified", true,
			"Refresh the Node inventory before starting the rollout. Listener evidence is missing, incomplete, or stale; existing services are preserved.")
	}
	for _, raw := range requirements {
		requirement, err := NormalizeRequirement(raw)
		if err != nil {
			return err
		}
		activeOwner := false
		for _, allocation := range inventory.Allocations {
			address, err := normalizeBindAddress(allocation.BindAddress)
			if err == nil && allocation.Desired && allocation.StackID == kitDeploymentID && allocation.ClaimState == ClaimStateActive && allocation.Transport == requirement.Transport && allocation.Port == requirement.Port && addressesOverlap(address, requirement.BindAddress) {
				activeOwner = true
			}
		}
		for _, allocation := range inventory.Allocations {
			address, err := normalizeBindAddress(allocation.BindAddress)
			if err != nil {
				return err
			}
			if allocation.Transport != requirement.Transport || allocation.Port != requirement.Port || !addressesOverlap(address, requirement.BindAddress) {
				continue
			}
			if allocation.ObservedState == EvidenceMissing {
				continue
			}
			if allocation.ObservedState == EvidenceUnknown || allocation.ObservedState == EvidenceStale {
				return baselineConflict(requirement, "host_baseline_unverified", true, "Refresh the affected listener evidence before changing this Node.")
			}
			if allocation.Desired && allocation.StackID == kitDeploymentID && activeOwner {
				continue // Current lifecycle claim; local StackKits must still prove the actual owner.
			}
			return baselineConflict(requirement, "host_listener_occupied", false,
				"An existing service uses this binding. Preserve it and choose a supported alternative binding or another Node, then regenerate and review the plan. Reuse requires verified ownership; changing another owner requires an explicit migration.")
		}
	}
	return nil
}

func baselineConflict(requirement Requirement, reason string, retryable bool, body string) *ConflictError {
	return &ConflictError{ErrorCode: ErrorCodeAllocationConflict, ReasonCode: reason, Retryable: retryable,
		Transport: requirement.Transport, BindAddress: requirement.BindAddress, Port: requirement.Port,
		UserGuidance: UserGuidance{Title: "Review the existing host before rollout", Body: body,
			NextSteps: []string{"Review the Node's port inventory and observation time.", "Keep existing services and data. Review configuration changes before regenerating the StackKit plan."}},
	}
}
