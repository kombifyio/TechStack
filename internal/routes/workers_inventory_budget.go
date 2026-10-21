package routes

import (
	"context"
	"time"
)

// Guard inventory projections are the heaviest worker writes: one report runs
// a single control-plane transaction with a locking read and an update for
// every reported service, and on 2026-10-20 those transactions stayed open for
// 10 to 14 s each. Unbounded, a fleet's reports took every pooled connection,
// and heartbeats, command polls, dashboards and the readiness ping waited
// behind them until Render restarted the only instance.
const (
	// inventoryProjectionBudget is how many projections may hold a pooled
	// connection at once; the rest of the pool stays with every other request.
	inventoryProjectionBudget = 12
	// inventoryProjectionWait is how long a report waits for a slot. A
	// projection can take most of the Guard's 15 s request budget, so the wait
	// stays short: an admitted report must still have time to commit, or its
	// rolled-back transaction and the Guard's retries only add load.
	inventoryProjectionWait = 500 * time.Millisecond
)

// inventoryProjectionGate bounds concurrent Guard inventory projections.
type inventoryProjectionGate struct {
	slots chan struct{}
}

func newInventoryProjectionGate(budget int) *inventoryProjectionGate {
	return &inventoryProjectionGate{slots: make(chan struct{}, budget)}
}

// acquire waits up to inventoryProjectionWait for a slot. A report that gets
// none is deferred, not failed: every report is a complete snapshot, fenced
// by its source position, so the Guard's next report supersedes it.
func (g *inventoryProjectionGate) acquire(ctx context.Context) (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	timer := time.NewTimer(inventoryProjectionWait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}
