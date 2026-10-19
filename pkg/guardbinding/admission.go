package guardbinding

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Admission is what a Guard verified about one offered command: the exact JSON
// the control plane sent and the binding it was admitted under. It travels with
// the execution context so the privileged executor can re-check the same
// evidence at the side-effect boundary instead of trusting the daemon's word.
type Admission struct {
	Binding Binding
	// Raw is the exact command JSON whose canonical digest the binding names.
	Raw []byte
	// Window is the authority this process received with the offer; the Guard
	// checks it again immediately before the effect (see CheckBeforeEffect).
	Window AuthorityWindow
}

type admissionKey struct{}

// WithAdmission attaches a verified admission to ctx.
func WithAdmission(ctx context.Context, admission Admission) context.Context {
	return context.WithValue(ctx, admissionKey{}, admission)
}

// AdmissionFrom returns the admission attached to ctx, if any.
func AdmissionFrom(ctx context.Context) (Admission, bool) {
	admission, ok := ctx.Value(admissionKey{}).(Admission)
	return admission, ok && len(admission.Raw) > 0
}

// UnknownOutcomeMarker prefixes the stderr of a synthetic result that an
// executor returns when it lost the privileged side after the effect may have
// started. The Guard records it as outcome_unknown, never as a failure.
const UnknownOutcomeMarker = "kombify-guard-outcome-unknown:"

// UnknownOutcomeResult builds the synthetic result for an effect whose outcome
// can no longer be observed.
func UnknownOutcomeResult(commandID, reason string) *agentpb.StackKitResult {
	return &agentpb.StackKitResult{CommandId: commandID, Success: false, ExitCode: -1, Stderr: UnknownOutcomeMarker + reason}
}

// IsUnknownOutcome reports whether result is a synthetic unknown outcome.
func IsUnknownOutcome(result *agentpb.StackKitResult) (reason string, ok bool) {
	if result == nil {
		return "", false
	}
	reason, ok = strings.CutPrefix(result.Stderr, UnknownOutcomeMarker)
	return strings.TrimSpace(reason), ok
}

// Guard privilege modes reported in the heartbeat.
const (
	// PostureSplit is the hardened layout: an unprivileged daemon with a
	// privileged executor behind a typed boundary.
	PostureSplit = "split"
	// PostureLegacyRoot is a Guard that still runs entirely as root; it is never
	// presented as least-privilege isolation.
	PostureLegacyRoot = "legacy_root"
	// PostureUnavailable is an unprivileged Guard without an executor.
	PostureUnavailable = "unavailable"
)

// Posture is the additive heartbeat projection of how this Guard is isolated and
// of its latest update. It carries no secret.
type Posture struct {
	Mode          string `json:"mode"`
	UpdateState   string `json:"update_state,omitempty"`
	UpdateVersion string `json:"update_version,omitempty"`
	UpdateReason  string `json:"update_reason,omitempty"`
	// DenyMutations is true when a failed update left no admissible fallback.
	DenyMutations bool `json:"deny_mutations,omitempty"`
}

// Normalize bounds an untrusted posture before the control plane stores it.
func (p Posture) Normalize() Posture {
	switch p.Mode {
	case PostureSplit, PostureLegacyRoot, PostureUnavailable:
	default:
		p.Mode = PostureLegacyRoot
	}
	clip := func(value string, limit int) string {
		if len(value) > limit {
			return strings.ToValidUTF8(value[:limit], "")
		}
		return value
	}
	p.UpdateState, p.UpdateVersion, p.UpdateReason = clip(p.UpdateState, 64), clip(p.UpdateVersion, 64), clip(p.UpdateReason, MaxReasonBytes)
	return p
}
