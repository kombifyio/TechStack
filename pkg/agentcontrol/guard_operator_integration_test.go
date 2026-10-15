package agentcontrol

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// escalatedOutcome drives one mutating command to an escalated outcome_unknown
// (revision 2) and returns its latest digest.
func escalatedOutcome(t *testing.T, hub *Hub, id string) (latestDigest string) {
	t.Helper()
	command := mutatingCommand(id)
	if offer := pollBound(t, hub, 1); offer == nil || offer.Command.CommandId != id {
		t.Fatalf("offer = %+v", offer)
	}
	unknown := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: "interrupted_before_result"}
	unknown.Digest, _ = unknown.ComputeDigest(id, nil)
	if _, err := submit(hub, 1, command, unknown, nil); err != nil {
		t.Fatal(err)
	}
	escalated := guardbinding.ResultReport{Revision: 2, State: guardbinding.ReportResolved, PriorDigest: unknown.Digest, Resolution: guardbinding.ResolutionEscalated, Reason: "no_reconciliation_oracle_for_action"}
	escalated.Digest, _ = escalated.ComputeDigest(id, nil)
	if _, err := submit(hub, 1, command, escalated, nil); err != nil {
		t.Fatal(err)
	}
	return escalated.Digest
}

// An escalated outcome keeps fencing until an operator releases it with an
// explicit disposition. The clock only changes how it is shown, a Guard cannot
// commit an operator disposition itself, and a stale or foreign request never
// releases the fence.
func TestOperatorReleaseOfEscalatedOutcome(t *testing.T) {
	hub, database := openGuardHub(t)
	first, second := mutatingCommand("cmd-op-1"), mutatingCommand("cmd-op-2")
	queueCommand(t, database, first)
	queueCommand(t, database, second)
	digest := escalatedOutcome(t, hub, first.CommandId)
	hub.SetNeedsOperatorAfter(time.Hour)

	outcomes, err := hub.ListUnresolvedOutcomes(t.Context(), integrationTenant, integrationAgent)
	if err != nil || len(outcomes) != 1 || outcomes[0].State != OutcomeReconciling || !outcomes[0].Escalated || outcomes[0].Revision != 2 {
		t.Fatalf("unresolved = %+v err = %v", outcomes, err)
	}
	// Past the policy time the outcome needs an operator; it is still fenced.
	hub.SetNeedsOperatorAfter(time.Nanosecond)
	if outcomes, err = hub.ListUnresolvedOutcomes(t.Context(), integrationTenant, integrationAgent); err != nil || len(outcomes) != 1 || outcomes[0].State != OutcomeNeedsOperator {
		t.Fatalf("expired outcome = %+v err = %v", outcomes, err)
	}
	if offer := pollBound(t, hub, 1, first.CommandId); offer != nil {
		t.Fatalf("the expiry policy released the fence: %+v", offer.Command.CommandId)
	}

	// A Guard cannot commit an operator disposition.
	forged := guardbinding.ResultReport{Revision: 3, State: guardbinding.ReportResolved, PriorDigest: digest, Resolution: guardbinding.DispositionVerifiedNotApplied, Reason: "self-release"}
	forged.Digest, _ = forged.ComputeDigest(first.CommandId, nil)
	if _, err := submit(hub, 1, first, forged, nil); denialCode(err) != guardbinding.CodeResultRejected {
		t.Fatalf("a Guard committed an operator disposition: %v", err)
	}

	resolve := func(mutate func(*OperatorResolution)) error {
		request := OperatorResolution{
			TenantID: integrationTenant, AgentID: integrationAgent, CommandID: first.CommandId, ActorID: "operator-1",
			Disposition: guardbinding.DispositionVerifiedNotApplied, Note: "checked the unit on the host", ExpectedRevision: 2,
		}
		mutate(&request)
		_, resolveErr := hub.ResolveOutcomeByOperator(t.Context(), request)
		return resolveErr
	}
	if err := resolve(func(r *OperatorResolution) { r.ExpectedRevision = 1 }); denialCode(err) != guardbinding.CodeOutcomeRevisionStale {
		t.Fatalf("stale revision err = %v", err)
	}
	if err := resolve(func(r *OperatorResolution) { r.Disposition = guardbinding.ResolutionNotApplied }); denialCode(err) != guardbinding.CodeOperatorNotAllowed {
		t.Fatalf("guard disposition by an operator err = %v", err)
	}
	if err := resolve(func(r *OperatorResolution) { r.Note = "" }); denialCode(err) != guardbinding.CodeOperatorNotAllowed {
		t.Fatalf("missing note err = %v", err)
	}
	if err := resolve(func(r *OperatorResolution) { r.TenantID = "tenant-other" }); err == nil {
		t.Fatal("another tenant resolved the outcome")
	}
	if state := commandState(t, database, first.CommandId); state != "outcome_unknown" {
		t.Fatalf("a refused resolution changed the state to %s", state)
	}

	// The Guard still holds the command as unsettled: it is told first, before
	// any new command is offered.
	notices, err := hub.OperatorResolutionsForTenant(t.Context(), integrationTenant, integrationAgent, []string{first.CommandId})
	if err != nil || len(notices) != 0 {
		t.Fatalf("notices before resolution = %+v err = %v", notices, err)
	}
	if err := resolve(func(*OperatorResolution) {}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := resolve(func(*OperatorResolution) {}); denialCode(err) != guardbinding.CodeOutcomeNotUnresolved {
		t.Fatalf("a second resolution err = %v", err)
	}
	notices, err = hub.OperatorResolutionsForTenant(t.Context(), integrationTenant, integrationAgent, []string{first.CommandId})
	if err != nil || len(notices) != 1 || notices[0].Revision != 3 || notices[0].PriorDigest != digest || notices[0].ActorID != "operator-1" {
		t.Fatalf("notices = %+v err = %v", notices, err)
	}
	if recomputed := (guardbinding.ResultReport{Revision: 3, State: guardbinding.ReportResolved, PriorDigest: digest, Resolution: notices[0].Resolution, Reason: notices[0].Reason}); func() string { d, _ := recomputed.ComputeDigest(first.CommandId, nil); return d }() != notices[0].Digest {
		t.Fatal("the notice digest is not the digest of its content")
	}

	// The fence is released: the waiting mutation is offered.
	if offer := pollBound(t, hub, 1, first.CommandId); offer == nil || offer.Command.CommandId != second.CommandId {
		t.Fatalf("the operator resolution did not release the fence: %+v", offer)
	}
}
