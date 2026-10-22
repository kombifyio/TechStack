package guardposture

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// LocalRepairGrant is the unsigned grant of the owner's current repair
// approval for one Guard: the registered policy version, its safe controls,
// the StackKits release, the persisted bounds and the server's repairs of the
// last day. It is nil while no approval is active (missing, withdrawn or
// expired), or when no release is admitted: the Guard then only observes. The
// caller signs it with the worker's binding key; an unsigned grant is never
// admitted by a Guard. generation is the credential generation that
// authenticated the request.
func (s *Service) LocalRepairGrant(ctx context.Context, identity Identity, generation int64) (*guardbinding.LocalRepairGrant, error) {
	if s.localRelease == nil || generation < 1 {
		return nil, nil
	}
	now := s.now()
	policy, err := s.store.GetAutoRepair(ctx, identity.TenantID, identity.WorkerID)
	if err != nil || !policy.Active(now) || policy.PolicyID == "" || policy.Version < 1 {
		return nil, err
	}
	controls := make([]string, 0, len(policy.Controls))
	for _, id := range policy.Controls {
		if slices.Contains(AutoRepairSafeControls, id) {
			controls = append(controls, id)
		}
	}
	if len(controls) == 0 {
		return nil, nil
	}
	sort.Strings(controls)
	release, err := s.localRelease()
	if err != nil || release == nil {
		// Without an admitted release no repair can run: observe-only.
		return nil, nil
	}
	repairs, err := s.store.Repairs(ctx, identity.TenantID, identity.WorkerID, guardbinding.MaxLocalRepairHistory)
	if err != nil {
		return nil, err
	}
	history := make([]guardbinding.LocalRepairRecord, 0, len(repairs))
	for _, repair := range repairs {
		// The newest repairs, as the store holds them: the bounds look at the
		// last day and the breaker at the most recent finished repairs.
		record := guardbinding.LocalRepairRecord{Controls: repair.Controls, RequestedAt: repair.RequestedAt.UTC(), FinishedAt: repair.FinishedAt, Failed: repair.failure()}
		if repair.Local != nil {
			record.OperationID = repair.Local.OperationID
		}
		history = append(history, record)
	}
	return &guardbinding.LocalRepairGrant{
		ContractVersion: guardbinding.LocalRepairContractVersion,
		PolicyID:        policy.PolicyID, PolicyVersion: policy.Version,
		TenantID: identity.TenantID, WorkerID: identity.WorkerID, ServerID: identity.ServerID, Generation: generation,
		Action: guardbinding.LocalRepairAction, Controls: controls, Release: *release,
		PolicyExpiresAt: policy.ExpiresAt.UTC(), IssuedAt: now.UTC(), Bounds: s.policy.LocalBounds(), History: history,
	}, nil
}

// LocalBounds is the policy's repair bounds as a grant carries them.
func (p Policy) LocalBounds() guardbinding.LocalRepairBounds {
	p = p.Normalize()
	return guardbinding.LocalRepairBounds{
		CooldownSeconds: int64(p.Repair.ControlCooldown / time.Second), MaxAttemptsPerDay: p.Repair.MaxAttempts,
		BreakerFailures: p.Repair.BreakerFailures, BreakerOpenSeconds: int64(p.Repair.BreakerOpenFor / time.Second),
	}
}

// CheckLocalBudget is the Guard's own check of the persisted repair bounds for
// a repair under grant: the server's circuit breaker, then a cooldown and a
// daily attempt budget per control, over the repairs the grant carries and the
// Guard's own local repairs (local, from its durable ledger, so a restart
// cannot reset them). A local repair already in the grant's history is
// counted once. It applies the same rule the control plane applies when it
// accepts the evidence.
func CheckLocalBudget(grant guardbinding.LocalRepairGrant, local []Repair, controls []string, now time.Time) error {
	b := grant.Bounds
	policy := Policy{Repair: RepairPolicy{
		ControlCooldown: time.Duration(b.CooldownSeconds) * time.Second, MaxAttempts: b.MaxAttemptsPerDay,
		BreakerFailures: b.BreakerFailures, BreakerOpenFor: time.Duration(b.BreakerOpenSeconds) * time.Second,
	}}.Normalize()
	known := map[string]bool{}
	history := make([]Repair, 0, len(grant.History)+len(local))
	for _, repair := range local {
		if repair.Local != nil {
			known[repair.Local.OperationID] = true
		}
		history = append(history, repair)
	}
	for _, record := range grant.History {
		if record.OperationID != "" && known[record.OperationID] {
			continue
		}
		status := RepairSucceeded
		if record.Failed {
			status = RepairFailed
		}
		history = append(history, Repair{Controls: record.Controls, RequestedAt: record.RequestedAt, FinishedAt: record.FinishedAt, Status: status})
	}
	return policy.checkBudget(history, sortedControls(controls), now)
}

// LocalCandidates are the controls a grant may repair according to fresh
// evidence: drifted, automatically repairable, not blocked on an authorized
// key and not under an exception. It is the Guard-side counterpart of
// AutoRepairCandidates.
func LocalCandidates(grant guardbinding.LocalRepairGrant, evidence *Evidence, now time.Time) []string {
	expires := grant.PolicyExpiresAt
	policy := &AutoRepairPolicy{Enabled: true, Controls: grant.Controls, ExpiresAt: &expires}
	return AutoRepairCandidates(policy, &Posture{Security: SecurityRecord{Evidence: evidence}}, now)
}
