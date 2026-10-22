package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// noApplyRemovalEvidence proves, from the durable job history of the stack,
// that no StackKits Apply ever succeeded, was dispatched or is running for it.
// A stack that holds port claims but has no applied workload then has nothing
// on any node to remove, so its destroy releases the claims and retires the
// control-plane state without node teardown. Anything that cannot be proven
// returns an error and the destroy keeps refusing.
func (o *Orchestrator) noApplyRemovalEvidence(ctx context.Context, stack *orchestratorStack, snapshot portinventory.TeardownSnapshot) (jobs.NoApplyRemovalEvidence, error) {
	if o == nil || o.jobStore == nil {
		return jobs.NoApplyRemovalEvidence{}, errors.New("the job history of the stack is not available")
	}
	history, err := o.jobStore.ListJobsByStack(ctx, stack.tenantID, stack.id, jobs.NoApplyJobScanLimit)
	if err != nil {
		return jobs.NoApplyRemovalEvidence{}, fmt.Errorf("read the jobs of stack %s: %w", stack.id, err)
	}
	return jobs.NewNoApplyRemovalEvidence(stack.id, stack.tenantID, stack.ownerID, snapshot, history, o.now())
}
