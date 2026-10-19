package guardbinding

import (
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardcompat"
)

// AdmitMutatingDispatch is the control-plane side of mixed-version admission:
// a mutating command is offered only to a Guard that advertises the binding
// contract this control plane speaks. Any other Guard (an older release, or a
// newer one that dropped this version) stays observe-only and the command is
// refused with upgrade guidance; there is no unbound mutation path.
func AdmitMutatingDispatch(capabilities []string) *Denial {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), Capability) {
			return nil
		}
	}
	return Denyf(CodeObserveOnlyContract, "the Guard does not advertise %s", Capability)
}

// Check validates a report on its own, before any stored state is read: a
// known state, a revision within the contract bound, a digest that matches its
// content and, for a resolution, a known resolution.
func (r ResultReport) Check(commandID string, result *agentpb.StackKitResult) *Denial {
	switch r.State {
	case ReportTerminal, ReportOutcomeUnknown, ReportRejected, ReportResolved:
	default:
		return Denyf(CodeResultRejected, "unknown report state %q", r.State)
	}
	if r.Revision < 1 || r.Revision > guardcompat.MaxResultRevision {
		return Denyf(CodeRevisionOutOfOrder, "invalid revision %d", r.Revision)
	}
	if r.State != ReportTerminal {
		result = nil
	}
	expected, err := r.ComputeDigest(commandID, result)
	if err != nil {
		return Denyf(CodeResultRejected, "terminal report without a result")
	}
	if expected != r.Digest {
		return Denyf(CodeResultDigestMismatch, "report digest does not match its content")
	}
	if r.State == ReportResolved {
		switch r.Resolution {
		case ResolutionObservedConverged, ResolutionNotApplied, ResolutionEscalated:
		default:
			return Denyf(CodeResultRejected, "unknown resolution %q", r.Resolution)
		}
	}
	return nil
}

// Committed is what the control plane holds for one bound command when a
// report for it arrives.
type Committed struct {
	// State is the stored command state ("dispatched", "outcome_unknown", or a
	// settled state).
	State string
	// Revision and Digest are the last committed revision and its digest.
	Revision int64
	Digest   string
	// SameRevisionDigest is the digest already committed at the report's
	// revision, empty when that revision is not committed yet.
	SameRevisionDigest string
	// DispatchGeneration is the credential generation the command was
	// dispatched under; zero when unknown.
	DispatchGeneration int64
	// AuthorityExpiresAt is the command's authority expiry; zero when unknown.
	AuthorityExpiresAt time.Time
}

// Submission is one report as received, with the generation that
// authenticated it and, for a terminal report, when the effect started.
type Submission struct {
	Report     ResultReport
	Generation int64
	StartedAt  time.Time
}

// AcceptRevision decides whether a checked report may be committed against
// the stored state. duplicate is true for an identical repeat of a committed
// revision, which is acknowledged again without a new commit. A refusal for a
// command that cannot take this revision in its state carries
// CodeCommandNotPending.
func AcceptRevision(stored Committed, in Submission) (duplicate bool, denial *Denial) {
	report := in.Report
	if stored.SameRevisionDigest != "" {
		if stored.SameRevisionDigest == report.Digest {
			return true, nil
		}
		return false, Denyf(CodeRevisionConflict, "revision %d of this command is already committed with different content", report.Revision)
	}
	if report.Revision != stored.Revision+1 {
		return false, Denyf(CodeRevisionOutOfOrder, "revision %d follows committed revision %d", report.Revision, stored.Revision)
	}
	if report.Revision == 1 {
		if stored.State != "dispatched" {
			return false, Denyf(CodeCommandNotPending, "command is %s, not awaiting a first outcome", stored.State)
		}
		if report.State == ReportResolved {
			return false, Denyf(CodeRevisionConflict, "a first revision cannot resolve an outcome")
		}
	} else {
		if stored.State != "outcome_unknown" {
			return false, Denyf(CodeCommandNotPending, "command is %s, not awaiting a resolution", stored.State)
		}
		if report.State == ReportRejected || report.PriorDigest == "" || report.PriorDigest != stored.Digest {
			return false, Denyf(CodeRevisionConflict, "a resolution must reference the evidence it supersedes")
		}
	}
	if report.State == ReportRejected {
		return false, nil
	}
	if stored.DispatchGeneration > 0 && in.Generation < stored.DispatchGeneration {
		return false, Denyf(CodeStaleGeneration, "report generation %d is older than dispatch generation %d", in.Generation, stored.DispatchGeneration)
	}
	if report.State == ReportTerminal && !stored.AuthorityExpiresAt.IsZero() && !in.StartedAt.IsZero() &&
		in.StartedAt.After(stored.AuthorityExpiresAt.Add(ResultClockMargin)) {
		return false, Denyf(CodeAuthorityExpired, "the effect started after the command authority expired")
	}
	return false, nil
}
