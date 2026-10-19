package guardbinding

import "time"

// Denial codes of the execution fence (G04).
const (
	// CodeAuthorityRevoked refuses an effect once this Guard has observed that
	// the control plane revoked its identity.
	CodeAuthorityRevoked = "authority_revoked"
	// CodeRevalidationRequired refuses an effect whose remaining authority this
	// Guard can no longer trust (no in-process receipt, a restart or reboot, or a
	// stepped clock); only a fresh online offer can admit it.
	CodeRevalidationRequired = "online_revalidation_required"
)

// AuthorityWindow is the authority a Guard holds for one command, captured
// when the offer was received online. It lives only in memory: after a restart
// or reboot there is no trusted remaining lifetime, so the command needs a
// fresh offer from the control plane before any effect.
type AuthorityWindow struct {
	// ExpiresAt is the absolute authority expiry from the binding.
	ExpiresAt time.Time
	// ReceivedAt is the wall clock when the offer was received.
	ReceivedAt time.Time
	// ReceivedMono is the process-monotonic reading when the offer was received.
	ReceivedMono time.Duration
}

// Check is the authority half of the pre-effect check. The authority ends at
// the earlier of the absolute expiry and the lifetime the binding had left at
// receipt, measured on the monotonic clock. A wall clock that moved away from
// the monotonic clock by more than ResultClockMargin, or a missing receipt,
// means the remaining lifetime is untrusted and online revalidation is needed.
func (w AuthorityWindow) Check(now time.Time, mono time.Duration) *Denial {
	if w.ExpiresAt.IsZero() || w.ReceivedAt.IsZero() {
		return Denyf(CodeRevalidationRequired, "the command has no authority received by this process")
	}
	elapsed := mono - w.ReceivedMono
	if elapsed < 0 {
		return Denyf(CodeRevalidationRequired, "the monotonic clock does not continue from the receipt")
	}
	if drift := now.Sub(w.ReceivedAt) - elapsed; drift > ResultClockMargin || drift < -ResultClockMargin {
		return Denyf(CodeRevalidationRequired, "the wall clock moved %s against the monotonic clock since receipt", drift.Round(time.Second))
	}
	if !now.Before(w.ExpiresAt) || elapsed >= w.ExpiresAt.Sub(w.ReceivedAt) {
		return Denyf(CodeAuthorityExpired, "command authority expired at %s", w.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// JournaledIntent is the identity a Guard journaled for a command when it was
// first admitted. Journal recovery is an admission input: a recovered intent
// runs only under a current admission that names the same command identity.
type JournaledIntent struct {
	CommandID     string
	TenantID      string
	WorkerID      string
	PayloadDigest string
}

// EffectState is what a Guard knows at the moment it would start an effect.
type EffectState struct {
	// Expect names the running Guard's tenant, worker and server and the
	// highest credential generation it has observed.
	Expect Expectation
	// Revoked is true once a revocation of this Guard has been observed.
	Revoked bool
	Now     time.Time
	Mono    time.Duration
}

// CheckBeforeEffect re-evaluates an admission at the local effect boundary:
// observed revocation, tenant, worker and server, command identity and digest
// against both the offer and the journaled intent, credential generation and
// the still-valid authority. Inventory sequence and heartbeat freshness are
// not inputs: they never authorize an effect.
func (a Admission) CheckBeforeEffect(intent JournaledIntent, state EffectState) *Denial {
	if state.Revoked {
		return Denyf(CodeAuthorityRevoked, "this Guard observed that its identity was revoked")
	}
	b := a.Binding
	expect := state.Expect
	switch {
	case b.CommandID == "" || b.CommandID != intent.CommandID:
		return Denyf(CodeBindingMismatch, "the admission names another command")
	case expect.TenantID != "" && b.TenantID != expect.TenantID, intent.TenantID != b.TenantID:
		return Denyf(CodeBindingMismatch, "command is bound to another tenant")
	case expect.WorkerID != "" && b.WorkerID != expect.WorkerID, intent.WorkerID != b.WorkerID:
		return Denyf(CodeBindingMismatch, "command is bound to another worker")
	case expect.ServerID != "" && b.ServerID != "" && b.ServerID != expect.ServerID:
		return Denyf(CodeBindingMismatch, "command is bound to another server")
	}
	if digest, err := CanonicalDigest(a.Raw); err != nil || digest != b.PayloadDigest || intent.PayloadDigest != b.PayloadDigest {
		return Denyf(CodeDigestMismatch, "command payload does not match its digest")
	}
	if b.WorkerGeneration > b.CurrentGeneration || b.WorkerGeneration < expect.HighestGeneration {
		return Denyf(CodeStaleGeneration, "command generation %d is not the current generation %d", b.WorkerGeneration, expect.HighestGeneration)
	}
	window := a.Window
	if window.ExpiresAt.IsZero() || !window.ExpiresAt.Equal(b.AuthorityExpiresAt) {
		return Denyf(CodeRevalidationRequired, "the command has no authority received by this process")
	}
	return window.Check(state.Now, state.Mono)
}
