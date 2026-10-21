package stacks

import (
	"context"
	"time"

	"github.com/kombifyio/techstack/internal/readcoalesce"
)

// The dashboard reads GET /api/v1/stacks/{id}/identity for every stack every
// 5 to 15 s. Each read sends two StackKits commands to the stack's Guard and
// polls their durable outcome until the Guard answers, which took 15 to 45 s
// on 2026-10-20, so overlapping reads stacked up per stack and kept polling the
// control-plane pool. identitySnapshotCoalescer runs at most one snapshot per
// owner and stack, shares it with every waiting read, keeps the answer, and
// answers from the last good snapshot while the next one runs.
const (
	// identitySnapshotFreshFor is how long an answered snapshot is reused.
	identitySnapshotFreshFor = 20 * time.Second
	// identitySnapshotFailedFor is how long a failed snapshot is reused, so an
	// offline Guard is not asked again on every read.
	identitySnapshotFailedFor = 10 * time.Second
	// identitySnapshotServeStaleFor lets a read take the last good snapshot,
	// at most this much past its reuse window, while the next one runs.
	identitySnapshotServeStaleFor = 2 * time.Minute
	// identitySnapshotWait is how long a read with no good snapshot waits for
	// a running one before it answers as not reporting yet; a later read gets
	// the result.
	identitySnapshotWait = 4 * time.Second
	// identitySnapshotBudget bounds one snapshot: the Guard runs each command
	// within 30 s, plus time to pick it up.
	identitySnapshotBudget = 45 * time.Second
)

type identitySnapshotCoalescer struct {
	group *readcoalesce.Group[WizardIdentitySnapshot]
}

func newIdentitySnapshotCoalescer() *identitySnapshotCoalescer {
	return &identitySnapshotCoalescer{group: readcoalesce.New[WizardIdentitySnapshot](readcoalesce.Options{
		FreshFor: identitySnapshotFreshFor, FailedFor: identitySnapshotFailedFor,
		ServeStaleFor: identitySnapshotServeStaleFor, Wait: identitySnapshotWait,
		Budget: identitySnapshotBudget,
	})}
}

func identitySnapshotKey(request WizardIdentityRuntimeRequest) string {
	return request.TenantID + "\x00" + request.OwnerID + "\x00" + request.DeploymentID
}

// Snapshot returns a fresh or recent snapshot, joins the one already running,
// or starts one.
func (c *identitySnapshotCoalescer) Snapshot(ctx context.Context, runtime WizardIdentityRuntime, request WizardIdentityRuntimeRequest) (WizardIdentitySnapshot, error) {
	if c == nil {
		return runtime.Snapshot(ctx, request)
	}
	return c.group.Do(ctx, identitySnapshotKey(request), func(ctx context.Context) (WizardIdentitySnapshot, error) {
		snapshot, err := runtime.Snapshot(ctx, request)
		// Only the status is kept; an activation link is never held in memory.
		snapshot.Owner.ActivationURL = ""
		return snapshot, err
	})
}

// Forget drops the snapshot after a change, so the next read asks the Guard
// again.
func (c *identitySnapshotCoalescer) Forget(request WizardIdentityRuntimeRequest) {
	if c == nil {
		return
	}
	c.group.Forget(identitySnapshotKey(request))
}
