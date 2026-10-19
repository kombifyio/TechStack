package guardposture

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// LocalActionHostSecurityRepair is the only action a local repair policy
// admits: the existing typed StackKits host-security repair of named controls.
// There is no shell and no other executor behind a local policy.
const LocalActionHostSecurityRepair = "stackkit.host-security-repair"

// Denial codes of local-policy operation evidence, in addition to the shared
// guardbinding codes and the repair bound codes (cooldown, attempts, breaker).
const (
	// DenyLocalPolicyUnregistered refuses evidence that names no registered
	// policy version of this Guard, or a withdrawn one: without authority the
	// Guard is observe-only.
	DenyLocalPolicyUnregistered = "local_policy_unregistered"
	// DenyLocalPolicyScope refuses evidence for an action or a control the
	// policy version does not cover.
	DenyLocalPolicyScope = "local_policy_scope"
	// DenyLocalEvidenceInvalid refuses evidence that is malformed.
	DenyLocalEvidenceInvalid = "local_evidence_invalid"
)

const (
	maxOperationIDBytes = 128
	maxLocalSteps       = stackkitcommand.MaxHostSecurityRepairControls
)

var (
	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	outcomeToken       = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// LocalBinding is what the control plane recorded for one local-policy
// operation: its Guard-generated identity, the policy version and credential
// generation it ran under, and the last acknowledged revision.
type LocalBinding struct {
	OperationID   string `json:"operation_id"`
	PolicyID      string `json:"policy_id"`
	PolicyVersion int64  `json:"policy_version"`
	Generation    int64  `json:"generation"`
	Revision      int64  `json:"revision"`
	Digest        string `json:"digest"`
}

// PolicyVersion is one registered version of an owner's repair approval. It is
// immutable except for SupersededAt, which the next version sets.
type PolicyVersion struct {
	PolicyID     string
	Version      int64
	Enabled      bool
	Controls     []string
	ExpiresAt    *time.Time
	ApprovedAt   *time.Time
	SupersededAt *time.Time
}

// window is the interval in which an operation under this version may start:
// from its approval until the earlier of its expiry and its supersession.
func (v PolicyVersion) window() (from, until time.Time, ok bool) {
	if !v.Enabled || v.ExpiresAt == nil || v.ApprovedAt == nil {
		return time.Time{}, time.Time{}, false
	}
	until = *v.ExpiresAt
	if v.SupersededAt != nil && v.SupersededAt.Before(until) {
		until = *v.SupersededAt
	}
	return *v.ApprovedAt, until, true
}

// LocalOperationEvidence is what a Guard reports about one repair it admitted
// locally under a registered policy version. Each revision is immutable; the
// report digest covers every other field, so the binding to policy, tenant,
// worker and generation cannot change between revisions of one digest.
type LocalOperationEvidence struct {
	OperationID     string    `json:"operation_id"`
	TenantID        string    `json:"tenant_id"`
	WorkerID        string    `json:"worker_id"`
	Generation      int64     `json:"generation"`
	PolicyID        string    `json:"policy_id"`
	PolicyVersion   int64     `json:"policy_version"`
	PolicyExpiresAt time.Time `json:"policy_expires_at"`
	Action          string    `json:"action"`
	Controls        []string  `json:"controls"`
	// StartedAt is when the Guard started the effect.
	StartedAt time.Time `json:"started_at"`
	// Report carries revision, state and digest under the same rules as a
	// typed command result: terminal, outcome_unknown, or (revision 2 and
	// later) resolved.
	Report guardbinding.ResultReport `json:"report"`
	// Outcome and Steps are the stackkit.host-security-repair/v1 result of a
	// terminal report.
	Outcome string       `json:"outcome,omitempty"`
	Steps   []RepairStep `json:"steps,omitempty"`
}

// ComputeDigest is the digest the report of this evidence must carry.
func (e LocalOperationEvidence) ComputeDigest() (string, error) {
	controls := append([]string(nil), e.Controls...)
	sort.Strings(controls)
	steps := e.Steps
	if steps == nil {
		steps = []RepairStep{}
	}
	return guardbinding.DigestOf(map[string]any{
		"operation_id": e.OperationID, "tenant_id": e.TenantID, "worker_id": e.WorkerID, "generation": e.Generation,
		"policy_id": e.PolicyID, "policy_version": e.PolicyVersion, "policy_expires_at": e.PolicyExpiresAt.UTC().Format(time.RFC3339Nano),
		"action": e.Action, "controls": controls, "started_at": e.StartedAt.UTC().Format(time.RFC3339Nano),
		"revision": e.Report.Revision, "state": e.Report.State, "reason": e.Report.Reason, "resolution": e.Report.Resolution,
		"prior_digest": e.Report.PriorDigest, "truncated": e.Report.Truncated, "outcome": e.Outcome, "steps": steps,
	})
}

// LocalSubmission is one evidence revision as received, with the identity and
// credential generation that authenticated it.
type LocalSubmission struct {
	Identity   Identity
	Generation int64
	Evidence   LocalOperationEvidence
}

// LocalState is what the store holds when evidence arrives: the operation's
// record if it was reported before, the policy version it names, and the
// server's recent repairs.
type LocalState struct {
	Existing *Repair
	Version  *PolicyVersion
	History  []Repair
}

func localDeny(code, format string, args ...any) *guardbinding.Denial {
	denial := guardbinding.Denyf(code, format, args...)
	if denial.Guidance == "" {
		switch code {
		case DenyLocalPolicyUnregistered:
			denial.Guidance = "kombify Guard holds no current repair authority for this server, so it only observes. The owner approves automatic repair again to register a new policy version."
		case DenyLocalPolicyScope:
			denial.Guidance = "The local policy does not cover this repair; it stays with a person. Repair it from the server's security view."
		}
	}
	return denial
}

func budgetDenial(err error) *guardbinding.Denial {
	if denial, ok := IsRepairDenial(err); ok {
		return &guardbinding.Denial{Code: denial.Code, Message: denial.Message, Guidance: denial.Guidance}
	}
	return localDeny(DenyLocalEvidenceInvalid, "%v", err)
}

// AcceptLocalOperation decides whether one revision of local-policy operation
// evidence may be committed. It returns the record to store, or duplicate for an
// identical repeat of the committed revision (acknowledged again without a
// write). Evidence is accepted only when it names this tenant and worker, a
// credential generation no newer than the one that authenticated it, and a
// registered, enabled policy version whose expiry it repeats exactly, started
// inside that version's window, for controls it covers, and within the
// persisted cooldown, attempt budget and circuit breaker as they stood when the
// operation started.
func (p Policy) AcceptLocalOperation(in LocalSubmission, state LocalState, now time.Time) (write *Repair, duplicate bool, denial *guardbinding.Denial) {
	p = p.Normalize()
	e := in.Evidence
	if denial := checkEvidenceShape(e); denial != nil {
		return nil, false, denial
	}
	switch {
	case e.TenantID != in.Identity.TenantID:
		return nil, false, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "evidence names another tenant")
	case e.WorkerID != in.Identity.WorkerID:
		return nil, false, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "evidence names another worker")
	case e.Generation < 1 || e.Generation > in.Generation:
		return nil, false, guardbinding.Denyf(guardbinding.CodeStaleGeneration, "evidence generation %d is not admissible under credential generation %d", e.Generation, in.Generation)
	}
	stored := guardbinding.Committed{State: "dispatched"}
	if existing := state.Existing; existing != nil {
		b := existing.Local
		if b == nil || existing.Mode != RepairLocal {
			return nil, false, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "operation id belongs to another record")
		}
		if b.PolicyID != e.PolicyID || b.PolicyVersion != e.PolicyVersion || b.Generation != e.Generation ||
			!existing.RequestedAt.Equal(storedTime(e.StartedAt)) || !slices.Equal(existing.Controls, sortedControls(e.Controls)) {
			return nil, false, guardbinding.Denyf(guardbinding.CodeRevisionConflict, "a later revision must keep the operation's binding")
		}
		stored = guardbinding.Committed{Revision: b.Revision, Digest: b.Digest, State: "settled"}
		if existing.Status == RepairOutcomeUnknown {
			stored.State = "outcome_unknown"
		}
		if e.Report.Revision == b.Revision {
			stored.SameRevisionDigest = b.Digest
		}
	}
	dup, revisionDenial := guardbinding.AcceptRevision(stored, guardbinding.Submission{Report: e.Report, Generation: e.Generation})
	if dup {
		return nil, true, nil
	}
	if revisionDenial != nil {
		return nil, false, revisionDenial
	}

	version := state.Version
	if version == nil || version.PolicyID != e.PolicyID || version.Version != e.PolicyVersion {
		return nil, false, localDeny(DenyLocalPolicyUnregistered, "policy %s version %d is not registered for this Guard", e.PolicyID, e.PolicyVersion)
	}
	from, until, ok := version.window()
	switch {
	case !ok:
		return nil, false, localDeny(DenyLocalPolicyUnregistered, "policy version %d withdrew repair authority", e.PolicyVersion)
	case !storedTime(e.PolicyExpiresAt).Equal(storedTime(*version.ExpiresAt)):
		return nil, false, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "evidence names another expiry than the registered policy version")
	case e.StartedAt.Before(from.Add(-ResultClockMarginLocal)) || e.StartedAt.After(now.Add(ResultClockMarginLocal)):
		return nil, false, localDeny(DenyLocalEvidenceInvalid, "the operation start lies outside the policy version's lifetime")
	case !e.StartedAt.Before(until.Add(ResultClockMarginLocal)):
		return nil, false, guardbinding.Denyf(guardbinding.CodeAuthorityExpired, "the operation started after the policy version ended at %s", until.UTC().Format(time.RFC3339))
	}
	for _, id := range e.Controls {
		if !slices.Contains(version.Controls, id) || !slices.Contains(AutoRepairSafeControls, id) {
			return nil, false, localDeny(DenyLocalPolicyScope, "control %q is not covered by policy version %d", id, version.Version)
		}
	}

	controls := sortedControls(e.Controls)
	if state.Existing == nil {
		// The bounds as they stood when the operation started: what the
		// server's persisted history held before it.
		var before []Repair
		for _, repair := range state.History {
			if repair.RequestedAt.Before(e.StartedAt) {
				if repair.FinishedAt != nil && !repair.FinishedAt.Before(e.StartedAt) {
					repair.FinishedAt = nil
				}
				before = append(before, repair)
			}
		}
		if err := p.checkBudget(before, controls, e.StartedAt); err != nil {
			return nil, false, budgetDenial(err)
		}
	}

	started, done := storedTime(e.StartedAt), storedTime(now)
	record := &Repair{
		Mode: RepairLocal, ServerID: in.Identity.ServerID, Controls: controls,
		RequestedBy: fmt.Sprintf("guard-local-policy:%s@v%d", e.PolicyID, e.PolicyVersion),
		RequestedAt: started, ExpiresAt: version.ExpiresAt.UTC(), DispatchedAt: &started, FinishedAt: &done,
		Local: &LocalBinding{OperationID: e.OperationID, PolicyID: e.PolicyID, PolicyVersion: e.PolicyVersion,
			Generation: e.Generation, Revision: e.Report.Revision, Digest: e.Report.Digest},
	}
	if existing := state.Existing; existing != nil {
		record.ID, record.CommandID, record.ServerID, record.Steps = existing.ID, existing.CommandID, existing.ServerID, existing.Steps
	}
	switch e.Report.State {
	case guardbinding.ReportTerminal:
		record.Status, record.Steps = RepairOutcomeStatus(e.Outcome), e.Steps
		for _, step := range e.Steps {
			if step.ReasonCode != "" {
				record.ReasonCode = step.ReasonCode
				break
			}
		}
	case guardbinding.ReportOutcomeUnknown:
		record.Status, record.ReasonCode = RepairOutcomeUnknown, "local_outcome_unknown"
	default: // resolved
		switch e.Report.Resolution {
		case guardbinding.ResolutionObservedConverged:
			record.Status, record.ReasonCode = RepairSucceeded, "observed_converged"
		case guardbinding.ResolutionNotApplied:
			record.Status, record.ReasonCode = RepairFailed, "not_applied"
		default:
			record.Status, record.ReasonCode = RepairOutcomeUnknown, "operator_escalation"
		}
	}
	return record, false, nil
}

// ResultClockMarginLocal is the skew tolerated between a Guard's operation
// start and the control-plane clock and policy window.
const ResultClockMarginLocal = guardbinding.ResultClockMargin

// storedTime is t at the precision the store keeps (PostgreSQL timestamptz
// holds microseconds), so a value read back compares equal to the one sent.
func storedTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func sortedControls(controls []string) []string {
	out := append([]string(nil), controls...)
	sort.Strings(out)
	return out
}

func checkEvidenceShape(e LocalOperationEvidence) *guardbinding.Denial {
	switch {
	case e.OperationID == "" || len(e.OperationID) > maxOperationIDBytes || !operationIDPattern.MatchString(e.OperationID):
		return localDeny(DenyLocalEvidenceInvalid, "operation id is missing or malformed")
	case strings.TrimSpace(e.PolicyID) == "" || e.PolicyVersion < 1 || e.PolicyExpiresAt.IsZero() || e.StartedAt.IsZero():
		return localDeny(DenyLocalEvidenceInvalid, "evidence names no complete policy binding")
	case e.Action != LocalActionHostSecurityRepair:
		return localDeny(DenyLocalPolicyScope, "action %q cannot run under a local policy", e.Action)
	case len(e.Controls) == 0 || len(e.Controls) > stackkitcommand.MaxHostSecurityRepairControls || len(e.Steps) > maxLocalSteps:
		return localDeny(DenyLocalEvidenceInvalid, "evidence names no controls or too many")
	case e.Report.State == guardbinding.ReportRejected:
		return localDeny(DenyLocalEvidenceInvalid, "a locally refused operation is not reported as evidence")
	}
	seen := map[string]bool{}
	for _, id := range e.Controls {
		if !stackkitcommand.ValidHostSecurityControl(id) || seen[id] {
			return localDeny(DenyLocalEvidenceInvalid, "control %q is unknown or named twice", id)
		}
		seen[id] = true
	}
	if e.Report.State == guardbinding.ReportTerminal && !outcomeToken.MatchString(e.Outcome) {
		return localDeny(DenyLocalEvidenceInvalid, "a terminal report needs a repair outcome")
	}
	for _, step := range e.Steps {
		if !seen[step.Control] || !outcomeToken.MatchString(step.Status) || (step.ReasonCode != "" && !outcomeToken.MatchString(step.ReasonCode)) {
			return localDeny(DenyLocalEvidenceInvalid, "a repair step is malformed")
		}
	}
	// The report's own checks: known state and resolution, bounded revision.
	// The digest is the evidence digest, not a result digest.
	switch e.Report.State {
	case guardbinding.ReportTerminal, guardbinding.ReportOutcomeUnknown:
	case guardbinding.ReportResolved:
		switch e.Report.Resolution {
		case guardbinding.ResolutionObservedConverged, guardbinding.ResolutionNotApplied, guardbinding.ResolutionEscalated:
		default:
			return localDeny(DenyLocalEvidenceInvalid, "unknown resolution %q", e.Report.Resolution)
		}
	default:
		return localDeny(DenyLocalEvidenceInvalid, "unknown report state %q", e.Report.State)
	}
	if len(e.Report.Reason) > guardbinding.MaxReasonBytes {
		return localDeny(DenyLocalEvidenceInvalid, "reason is too long")
	}
	digest, err := e.ComputeDigest()
	if err != nil || digest != e.Report.Digest {
		return guardbinding.Denyf(guardbinding.CodeResultDigestMismatch, "evidence digest does not match its content")
	}
	return nil
}
