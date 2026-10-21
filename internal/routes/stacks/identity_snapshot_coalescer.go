package stacks

import (
	"context"
	"errors"
	"sync"
	"time"
)

// The dashboard reads GET /api/v1/stacks/{id}/identity for every stack about
// every 15 s. Each read sends two StackKits commands to the stack's Guard and
// polls their durable outcome until the Guard answers, which took 15 to 45 s
// on 2026-10-20. Overlapping reads therefore stacked up per stack, queued more
// work on the Guards and kept polling the control-plane pool until it starved.
// identitySnapshotCoalescer runs at most one snapshot per owner and stack,
// shares it with every waiting read and keeps the answer briefly.
const (
	// identitySnapshotFreshFor is how long an answered snapshot is reused.
	identitySnapshotFreshFor = 20 * time.Second
	// identitySnapshotFailedFor is how long a failed snapshot is reused, so an
	// offline Guard is not asked again on every read.
	identitySnapshotFailedFor = 10 * time.Second
	// identitySnapshotWait is how long one read waits for a running snapshot
	// before it answers as not reporting yet; a later read gets the result.
	identitySnapshotWait = 8 * time.Second
	// identitySnapshotBudget bounds one snapshot: the Guard runs each command
	// within 30 s, plus time to pick it up.
	identitySnapshotBudget = 45 * time.Second
	// identitySnapshotRetention drops entries nobody has read for a while.
	identitySnapshotRetention = 5 * time.Minute
)

var errIdentitySnapshotPending = errors.New("identity snapshot is still running")

type identitySnapshotCoalescer struct {
	mu      sync.Mutex
	entries map[string]*identitySnapshotEntry
}

type identitySnapshotEntry struct {
	done     chan struct{}
	snapshot WizardIdentitySnapshot
	err      error
	settled  time.Time
	// stale marks a snapshot that was running when the identity changed; it is
	// guarded by the coalescer's mutex.
	stale bool
}

func newIdentitySnapshotCoalescer() *identitySnapshotCoalescer {
	return &identitySnapshotCoalescer{entries: make(map[string]*identitySnapshotEntry)}
}

func identitySnapshotKey(request WizardIdentityRuntimeRequest) string {
	return request.TenantID + "\x00" + request.OwnerID + "\x00" + request.DeploymentID
}

// Snapshot returns a fresh snapshot, joins the one already running, or starts
// one. The snapshot runs detached from the request, so a reader that gives up
// neither cancels it for the others nor makes the next read start over.
func (c *identitySnapshotCoalescer) Snapshot(ctx context.Context, runtime WizardIdentityRuntime, request WizardIdentityRuntimeRequest) (WizardIdentitySnapshot, error) {
	if c == nil {
		return runtime.Snapshot(ctx, request)
	}
	key := identitySnapshotKey(request)
	now := time.Now()
	c.mu.Lock()
	entry := c.entries[key]
	if entry == nil || entry.expired(now) || entry.stale {
		// A stale snapshot still running keeps the Guard busy: its replacement
		// starts once it ends, so one stack never has two snapshots at once.
		var after <-chan struct{}
		if entry != nil && entry.stale && !entry.isSettled() {
			after = entry.done
		}
		entry = &identitySnapshotEntry{done: make(chan struct{})}
		c.entries[key] = entry
		c.pruneLocked(now)
		go entry.run(context.WithoutCancel(ctx), after, runtime, request)
	}
	c.mu.Unlock()

	timer := time.NewTimer(identitySnapshotWait)
	defer timer.Stop()
	select {
	case <-entry.done:
		return entry.snapshot, entry.err
	case <-timer.C:
		return WizardIdentitySnapshot{}, errIdentitySnapshotPending
	case <-ctx.Done():
		return WizardIdentitySnapshot{}, ctx.Err()
	}
}

// Forget drops the snapshot after a change, so the next read asks the Guard
// again. A snapshot still running may have read the state before the change:
// the readers already waiting get it, but no later read does.
func (c *identitySnapshotCoalescer) Forget(request WizardIdentityRuntimeRequest) {
	if c == nil {
		return
	}
	key := identitySnapshotKey(request)
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		if entry.isSettled() {
			delete(c.entries, key)
		} else {
			entry.stale = true
		}
	}
	c.mu.Unlock()
}

func (c *identitySnapshotCoalescer) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if entry.isSettled() && now.Sub(entry.settled) > identitySnapshotRetention {
			delete(c.entries, key)
		}
	}
}

func (e *identitySnapshotEntry) run(ctx context.Context, after <-chan struct{}, runtime WizardIdentityRuntime, request WizardIdentityRuntimeRequest) {
	if after != nil {
		<-after
	}
	ctx, cancel := context.WithTimeout(ctx, identitySnapshotBudget)
	defer cancel()
	snapshot, err := runtime.Snapshot(ctx, request)
	// Only the status is kept; an activation link is never held in memory.
	snapshot.Owner.ActivationURL = ""
	e.snapshot, e.err, e.settled = snapshot, err, time.Now()
	close(e.done)
}

func (e *identitySnapshotEntry) isSettled() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// expired is true for a settled snapshot past its reuse window; a running one
// never expires, it is joined.
func (e *identitySnapshotEntry) expired(now time.Time) bool {
	if !e.isSettled() {
		return false
	}
	keep := identitySnapshotFreshFor
	if e.err != nil {
		keep = identitySnapshotFailedFor
	}
	return now.Sub(e.settled) > keep
}
