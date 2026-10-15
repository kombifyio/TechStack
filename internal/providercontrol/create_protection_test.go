package providercontrol

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// Protect the provider-control invariant: replay and invalid input never
// inflate a pause, a recovery failure reopens it, and uncertainty outlives time.
func TestCreateProtectionRecoveryAndCustodyFence(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	failed := func(id string, at time.Time, code string) createOutcome {
		return createOutcome{OperationID: id, RequestedAt: at, Attempted: true, Resolved: true, Failure: &CreateFailure{code, at}, Head: providerexecutor.Receipt{Phase: providerexecutor.PhaseFailed, Status: providerexecutor.StatusFailed}}
	}
	outcomes := []createOutcome{
		failed("one", now.Add(-3*time.Minute), providerexecutor.ReasonCodeProviderQuota),
		failed("two", now.Add(-2*time.Minute), providerexecutor.ReasonCodeProviderPermission),
		failed("invalid", now.Add(-90*time.Second), providerexecutor.ReasonCodeProviderInvalidSpec),
		failed("three", now.Add(-time.Minute), providerexecutor.ReasonCodeProviderAuth),
	}
	p := projectCreateProtection("ionos", "de/fra", now, outcomes)
	if p.State != "paused" || p.RetryAt == nil || !p.RetryAt.Equal(now.Add(14*time.Minute)) {
		t.Fatalf("fresh creates were not paused: %+v", p)
	}
	duplicate := append(append([]createOutcome{}, outcomes...), outcomes[3])
	if replay := projectCreateProtection("ionos", "de/fra", now, duplicate); !replay.RetryAt.Equal(*p.RetryAt) || replay.ConsecutiveFailures != p.ConsecutiveFailures {
		t.Fatalf("receipt replay changed protection: %+v", replay)
	}
	lateSuccess := append(append([]createOutcome{}, outcomes...), createOutcome{OperationID: "old-success", RequestedAt: now.Add(-time.Hour), Resolved: true, Head: providerexecutor.Receipt{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent}})
	if old := projectCreateProtection("ionos", "de/fra", now, lateSuccess); old.State != "paused" {
		t.Fatalf("older completion cleared newer failure epoch: %+v", old)
	}
	recoveryAt := now.Add(16 * time.Minute)
	if ready := projectCreateProtection("ionos", "de/fra", recoveryAt, outcomes); ready.State != "ready" {
		t.Fatalf("cooldown did not admit recovery: %+v", ready)
	}
	outcomes = append(outcomes, failed("recovery", recoveryAt, providerexecutor.ReasonCodeProviderQuota))
	p = projectCreateProtection("ionos", "de/fra", recoveryAt, outcomes)
	if p.State != "paused" || !p.RetryAt.Equal(recoveryAt.Add(createPauseDuration)) {
		t.Fatalf("failed recovery did not reopen pause: %+v", p)
	}
	uncertain := createOutcome{OperationID: "uncertain", RequestedAt: recoveryAt.Add(16 * time.Minute), Attempted: true, Head: providerexecutor.Receipt{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted}}
	uncertain.classifyCustody(false, false, false)
	outcomes = append(outcomes, uncertain)
	p = projectCreateProtection("ionos", "de/fra", recoveryAt.Add(time.Hour), outcomes)
	if p.State != "outcome_unknown" || p.RetryAt != nil || p.Recovery != "reconcile_existing_create" {
		t.Fatalf("time reopened uncertain create: %+v", p)
	}
	outcomes[len(outcomes)-1].Resolved = true
	outcomes = append(outcomes, createOutcome{OperationID: "success", RequestedAt: recoveryAt.Add(17 * time.Minute), Resolved: true, Head: providerexecutor.Receipt{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent}})
	p = projectCreateProtection("ionos", "de/fra", recoveryAt.Add(time.Hour), outcomes)
	if p.State != "ready" || p.LastFailure != nil || p.RetryAt != nil {
		t.Fatalf("successful recovery did not clear pause: %+v", p)
	}
	quarantined := createOutcome{OperationID: "historical-quarantine", RequestedAt: now, DispatchGuarded: true, Head: providerexecutor.Receipt{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAccepted}, Failure: &CreateFailure{providerexecutor.ReasonCodeProviderTimeout, now}}
	quarantined.classifyCustody(false, false, false)
	p = projectCreateProtection("ionos", "de/fra", now.Add(time.Hour), []createOutcome{quarantined})
	if p.State != "outcome_unknown" || p.Recovery != "reconcile_existing_create" || p.ConsecutiveFailures != 0 {
		t.Fatalf("historical quarantine lost custody or counted a fresh failure: %+v", p)
	}
	throttled := failed("definitive-throttle", now, providerexecutor.ReasonCodeProviderRateLimit)
	throttled.Head.Reason = &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderRateLimit}
	throttled.classifyCustody(false, false, false)
	p = projectCreateProtection("ionos", "de/fra", now, []createOutcome{throttled})
	if p.State != "ready" || p.LastFailure == nil || p.LastFailure.Code != providerexecutor.ReasonCodeProviderRateLimit {
		t.Fatalf("definitive throttle prevented policy recovery: %+v", p)
	}
	unknownThrottle := failed("unknown-throttle", now, providerexecutor.ReasonCodeProviderPartialCreate)
	unknownThrottle.Head.Reason = &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderPartialCreate}
	unknownThrottle.classifyCustody(false, false, false)
	p = projectCreateProtection("ionos", "de/fra", now.Add(time.Hour), []createOutcome{unknownThrottle})
	if p.State != "outcome_unknown" {
		t.Fatalf("ambiguous throttle lost its custody fence: %+v", p)
	}
}
