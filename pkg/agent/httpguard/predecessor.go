package httpguard

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

const (
	// predecessorAskEvery bounds how often a Guard asks for a recovery
	// statement while it holds outcomes of an earlier worker binding.
	predecessorAskEvery = time.Minute
	// predecessorCommandAskEvery bounds how often one held command is listed,
	// so commands the control plane does not cover (settled or resolved there)
	// cannot starve newer recoverable ones.
	predecessorCommandAskEvery = time.Hour
)

// heldPredecessorCommands lists the held outcomes of an earlier worker binding
// of this Guard's tenant, when the Guard holds no current recovery statement
// and has not asked within predecessorAskEvery. Outcomes of another tenant are
// never listed: they stay in the journal for a receipted handoff.
func (d *durableOps) heldPredecessorCommands() []guardbinding.PredecessorCommand {
	store := d.readable()
	if store == nil {
		return nil
	}
	now := d.opts.Now()
	d.mu.Lock()
	current := d.predecessor != nil && now.Before(d.predecessor.ExpiresAt)
	recent := !d.lastPredecessor.IsZero() && now.Sub(d.lastPredecessor) < predecessorAskEvery
	d.mu.Unlock()
	if current || recent {
		return nil
	}
	c := d.c
	d.mu.Lock()
	asked := maps.Clone(d.predecessorAsked)
	d.mu.Unlock()
	entries := store.Entries(func(e journal.Entry) bool {
		_, unacked := e.NextUnacked()
		last, seen := asked[e.Intent.CommandID]
		return unacked && e.Intent.TenantID == c.cfg.TenantID && e.Intent.WorkerID != "" &&
			e.Intent.WorkerID != c.cfg.RuntimeAgentID && e.Intent.ContractVersion == guardbinding.ContractVersion &&
			(!seen || now.Sub(last) >= predecessorCommandAskEvery)
	})
	if len(entries) == 0 {
		return nil
	}
	held := make([]guardbinding.PredecessorCommand, 0, min(len(entries), guardbinding.MaxPredecessorCommands))
	for _, e := range entries {
		if len(held) == guardbinding.MaxPredecessorCommands {
			break
		}
		held = append(held, guardbinding.PredecessorCommand{CommandID: e.Intent.CommandID, WorkerID: e.Intent.WorkerID})
	}
	d.mu.Lock()
	d.lastPredecessor = now
	if d.predecessorAsked == nil {
		d.predecessorAsked = map[string]time.Time{}
	}
	maps.DeleteFunc(d.predecessorAsked, func(_ string, at time.Time) bool { return now.Sub(at) >= predecessorCommandAskEvery })
	for _, c := range held {
		d.predecessorAsked[c.CommandID] = now
	}
	d.mu.Unlock()
	return held
}

// acceptPredecessorRecovery holds a recovery statement only when it verifies
// under the pinned control-plane key for this Guard's own tenant, worker and
// server. A statement that does not verify changes nothing.
func (d *durableOps) acceptPredecessorRecovery(recovery *guardbinding.PredecessorRecovery) {
	if recovery == nil {
		return
	}
	c := d.c
	expect := guardbinding.Expectation{TenantID: c.cfg.TenantID, WorkerID: c.cfg.RuntimeAgentID, ServerID: c.cfg.ServerID}
	if denial := recovery.Verify(c.cfg.BindingSigningKeys, expect, d.opts.Now()); denial != nil {
		c.log.Warn("guard_predecessor_recovery_ignored", "code", denial.Code, "reason", denial.Message)
		return
	}
	held := *recovery
	held.Commands = slices.Clone(recovery.Commands)
	d.mu.Lock()
	d.predecessor = &held
	d.mu.Unlock()
	c.log.Info("guard_predecessor_recovery_held", "commands", len(held.Commands), "expires_at", held.ExpiresAt.Format(time.RFC3339))
	d.kick()
}

// predecessorCovers reports whether the held statement admits delivering this
// journaled outcome now.
func (d *durableOps) predecessorCovers(intent journal.Intent) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.predecessor
	return r != nil && d.opts.Now().Before(r.ExpiresAt) && intent.TenantID == r.TenantID && r.Covers(intent.CommandID, intent.WorkerID)
}

// deliverPredecessor posts one held outcome of an earlier worker binding under
// the recovery statement. Only an acknowledgement settles it locally. A refusal
// never marks the evidence permanent: it stays held, still fences conflicting
// work, and is dropped from the statement so it waits for a new one.
func (d *durableOps) deliverPredecessor(ctx context.Context, store *journal.Store, e journal.Entry, report guardbinding.ResultReport, result *agentpb.StackKitResult) deliveryOutcome {
	id := e.Intent.CommandID
	d.mu.Lock()
	var recovery guardbinding.PredecessorRecovery
	covered := d.predecessor != nil && d.opts.Now().Before(d.predecessor.ExpiresAt) && d.predecessor.Covers(id, e.Intent.WorkerID)
	if covered {
		recovery = *d.predecessor
		recovery.Commands = slices.Clone(d.predecessor.Commands)
	}
	d.mu.Unlock()
	if !covered {
		return deliveryRetry
	}
	request := durableRequest{
		controlRequest: d.c.durableIdentity(), CommandID: id,
		PredecessorWorkerID: e.Intent.WorkerID, PredecessorRecovery: &recovery,
	}
	switch d.c.postReportRequest(ctx, request, report, result, false) {
	case deliveryAcked:
		if err := store.Ack(id, report.Revision, false); err != nil {
			d.c.log.Warn("guard_ack_not_recorded", "command_id", id, "error", err.Error())
		}
		d.c.log.Info("guard_predecessor_outcome_delivered", "command_id", id, "predecessor_worker_id", e.Intent.WorkerID, "revision", report.Revision)
		return deliveryAcked
	case deliveryPermanent:
		d.mu.Lock()
		if d.predecessor != nil {
			d.predecessor.Commands = slices.DeleteFunc(d.predecessor.Commands, func(c guardbinding.PredecessorCommand) bool { return c.CommandID == id })
		}
		d.mu.Unlock()
		d.c.log.Warn("guard_predecessor_outcome_held", "command_id", id, "predecessor_worker_id", e.Intent.WorkerID,
			"guidance", "the control plane refused this earlier binding's outcome; it stays in the journal and keeps fencing conflicting work")
	}
	return deliveryRetry
}
