package orchestrator

import (
	"context"
	"fmt"
	"strings"
)

// resolveBackupAgent finds the approved agent that currently owns a stack's
// runtime. It resolves at dispatch rather than at scan time, because a stack
// can be re-enrolled between the two and a stale agent id would send the
// backup to a worker that no longer owns the node.
//
// Only an approved worker is admissible: dispatching to an unapproved
// enrollment would run a customer's backup against a machine the owner has not
// accepted.
func (o *Orchestrator) resolveBackupAgent(ctx context.Context, tenantID, stackID string) (string, error) {
	if o == nil || o.cfg.WorkerStore == nil {
		return "", fmt.Errorf("worker authority is not configured")
	}
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return "", fmt.Errorf("resolving a backup agent requires exact tenant and stack identity")
	}
	workers, err := o.cfg.WorkerStore.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("list workers: %w", err)
	}
	var candidates []string
	for _, worker := range workers {
		if strings.TrimSpace(worker.StackID) != stackID || !worker.Approved {
			continue
		}
		candidates = append(candidates, strings.TrimSpace(worker.ID))
	}
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0], nil
	default:
		// Two approved workers claiming one stack is a control-plane
		// inconsistency. Picking one would make the backup land on an
		// arbitrary machine, so this refuses and leaves the ambiguity visible.
		return "", fmt.Errorf("stack %s has %d approved agents; refusing to guess which one owns the runtime", stackID, len(candidates))
	}
}
