package routes

import (
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

func normalizeRuntimeConvergence(snapshot **runtimeconvergence.Snapshot) error {
	if snapshot == nil || *snapshot == nil {
		return nil
	}
	normalized, err := runtimeconvergence.Normalize(**snapshot)
	if err != nil {
		return fmt.Errorf("%w: runtime convergence", controlplane.ErrConflict)
	}
	*snapshot = &normalized
	return nil
}

func runtimeConvergenceMetadata(worker controlplane.Worker) map[string]any {
	for _, source := range []map[string]any{worker.Resources, worker.Capabilities} {
		if source == nil {
			continue
		}
		candidate, ok := source["runtime_convergence"].(map[string]any)
		if !ok {
			continue
		}
		var snapshot runtimeconvergence.Snapshot
		if err := decodeRuntimeConvergenceMap(candidate, &snapshot); err != nil {
			continue
		}
		return runtimeconvergence.Map(snapshot)
	}
	return nil
}

func decodeRuntimeConvergenceMap(value map[string]any, target *runtimeconvergence.Snapshot) error {
	if target == nil {
		return fmt.Errorf("runtime convergence target is required")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return err
	}
	normalized, err := runtimeconvergence.Normalize(*target)
	if err != nil {
		return err
	}
	*target = normalized
	return nil
}
