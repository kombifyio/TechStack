// Package guardbinding is the wire contract shared by the control plane and
// kombify Guard for admitting one typed command and for reporting its outcome.
//
// Every command offered to a Guard is bound to its command ID, tenant, worker
// identity, credential generation, executor action, canonical payload digest,
// authority expiry and contract version. Both sides evaluate the same binding:
// the control plane when it dispatches and when it accepts a result, and Guard
// before it persists intent and before any effect. The package carries no I/O
// and no policy beyond those checks.
package guardbinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardcompat"
)

const (
	// ContractVersion names the binding and outcome-report contract.
	ContractVersion = guardcompat.BindingContractVersion
	// LegacyContractVersion is stored for a command dispatched to a Guard that
	// does not advertise Capability. Such a command keeps the pre-binding
	// at-most-once semantics and is flagged so it is never mistaken for a
	// bound command; it is never a mutating command (see
	// AdmitMutatingDispatch).
	LegacyContractVersion = guardcompat.LegacyContractVersion
	// Capability is advertised by a Guard that validates bindings, journals
	// intent and result, and reports revisions.
	Capability = guardcompat.BindingCapability
	// ResultClockMargin is the skew tolerated between the Guard clock reported
	// in a result and the control-plane authority expiry.
	ResultClockMargin = guardcompat.ResultClockMargin
	// MaxKnownCommands bounds the in-flight command IDs a Guard lists in a poll.
	MaxKnownCommands = guardcompat.MaxKnownCommands
	// MaxReasonBytes bounds every free-text reason carried on the wire.
	MaxReasonBytes = guardcompat.MaxReasonBytes
)

// Denial codes are stable, machine-readable and safe to show to an operator.
const (
	CodeBindingMismatch      = "binding_mismatch"
	CodeDigestMismatch       = "payload_digest_mismatch"
	CodeContractUnsupported  = "contract_unsupported"
	CodeStaleGeneration      = "stale_generation"
	CodeAuthorityExpired     = "authority_expired"
	CodeJournalUnavailable   = "journal_unavailable"
	CodeResourceFenced       = "resource_fenced"
	CodeResultDigestMismatch = "result_digest_mismatch"
	CodeRevisionConflict     = "result_revision_conflict"
	CodeRevisionOutOfOrder   = "result_revision_out_of_order"
	CodeCommandNotPending    = "command_not_pending"
	CodeResultRejected       = "result_rejected"
	CodeOutcomeNotUnresolved = "outcome_not_unresolved"
	CodeOutcomeRevisionStale = "outcome_revision_stale"
	CodeOperatorNotAllowed   = "operator_resolution_not_allowed"
	// CodeObserveOnlyContract refuses a mutating command to a Guard that does
	// not speak the binding contract: it stays observe-only.
	CodeObserveOnlyContract = "observe_only_contract"
)

// Denial is a structured refusal with recovery guidance.
type Denial struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Guidance string `json:"guidance,omitempty"`
}

func (d *Denial) Error() string {
	if d == nil {
		return ""
	}
	return d.Code + ": " + d.Message
}

// Denyf builds a Denial with the standard guidance for its code.
func Denyf(code, format string, args ...any) *Denial {
	return &Denial{Code: code, Message: fmt.Sprintf(format, args...), Guidance: guidanceFor(code)}
}

func guidanceFor(code string) string {
	switch code {
	case CodeBindingMismatch, CodeDigestMismatch:
		return "The command was altered or addressed to a different worker; it was not run. Issue it again from the control plane."
	case CodeContractUnsupported:
		return "Update kombify Guard to a release that supports the command binding contract."
	case CodeObserveOnlyContract:
		return "This kombify Guard does not support the command binding contract, so it stays observe-only: read-only checks still run, but no change is sent to it. Update kombify Guard to a release that supports the binding contract, then issue the change again."
	case CodeStaleGeneration:
		return "The command was issued for an older worker credential generation; it was not run. Issue it again."
	case CodeAuthorityExpired:
		return "The authority to run this command expired before it started; it was not run. Issue it again."
	case CodeJournalUnavailable:
		return "kombify Guard cannot record operations durably on this host (corrupt, incompatible or full journal). Read-only diagnosis still works; free disk space or repair the journal, then retry."
	case CodeResourceFenced:
		return "An earlier operation on this server has no acknowledged outcome yet; new changes wait until it is reconciled."
	case CodeAuthorityRevoked:
		return "This kombify Guard's identity was revoked, so it starts no further changes. Pair the server again to continue."
	case CodeRevalidationRequired:
		return "kombify Guard cannot trust how much authority is left for this command (it restarted, the host rebooted or the clock changed); it was not run. It runs once the control plane offers it again online."
	case CodeLocalAuthorityMissing:
		return "kombify Guard holds no current repair authority for this server, so it only observes. Automatic repair resumes once the owner's approval is active and the Guard has reached the control plane again."
	case CodeOutcomeNotUnresolved:
		return "This operation has no unresolved outcome to release. Refresh the list of unresolved operations."
	case CodeOutcomeRevisionStale:
		return "The operation's outcome changed since you read it. Refresh it, verify the server again and resolve with the current revision."
	case CodeOperatorNotAllowed:
		return "Choose verified_applied, verified_not_applied or accept_unknown, and describe what you verified."
	default:
		return ""
	}
}

// Binding is the admission record the control plane attaches to an offered
// command. It is additive: a Guard that does not know it ignores it.
type Binding struct {
	CommandID          string    `json:"command_id"`
	TenantID           string    `json:"tenant_id"`
	WorkerID           string    `json:"worker_id"`
	ServerID           string    `json:"server_id,omitempty"`
	Operation          string    `json:"operation"`
	PayloadDigest      string    `json:"payload_digest"`
	WorkerGeneration   int64     `json:"worker_generation"`
	CurrentGeneration  int64     `json:"current_generation"`
	AuthorityExpiresAt time.Time `json:"authority_expires_at"`
	ContractVersion    string    `json:"contract_version"`
	// Reoffer marks a command that was already dispatched to this worker and
	// that the Guard did not list as known: no effect can have started.
	Reoffer bool `json:"reoffer,omitempty"`
	// KeyID and Signature are the control plane's Ed25519 signature over the
	// other fields (see Signer). A Guard that pins a key refuses a binding
	// without them.
	KeyID     string `json:"key_id,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// Expectation is what a Guard knows independently of the control plane.
type Expectation struct {
	TenantID string
	WorkerID string
	// ServerID, when both sides know it, must match the server the binding names.
	ServerID string
	// HighestGeneration is the highest credential generation this Guard has
	// already seen offered; an older dispatch is stale.
	HighestGeneration int64
	// Known is true when the command is already in the local journal; its
	// generation was validated when it was admitted.
	Known bool
}

// Validate is the Guard-side admission check. rawCommand is the exact JSON
// object the control plane sent; the digest is taken over its canonical form so
// it is independent of field order and of the local protobuf revision.
func (b Binding) Validate(rawCommand []byte, operation string, expect Expectation, now time.Time) *Denial {
	if b.ContractVersion != ContractVersion {
		return Denyf(CodeContractUnsupported, "binding contract %q is not supported", b.ContractVersion)
	}
	if strings.TrimSpace(b.CommandID) == "" || strings.TrimSpace(b.PayloadDigest) == "" {
		return Denyf(CodeBindingMismatch, "binding is incomplete")
	}
	if expect.TenantID != "" && b.TenantID != expect.TenantID {
		return Denyf(CodeBindingMismatch, "command is bound to another tenant")
	}
	if expect.WorkerID != "" && b.WorkerID != expect.WorkerID {
		return Denyf(CodeBindingMismatch, "command is bound to another worker")
	}
	if expect.ServerID != "" && b.ServerID != "" && b.ServerID != expect.ServerID {
		return Denyf(CodeBindingMismatch, "command is bound to another server")
	}
	if b.Operation != operation {
		return Denyf(CodeBindingMismatch, "command action differs from its binding")
	}
	digest, err := CanonicalDigest(rawCommand)
	if err != nil || digest != b.PayloadDigest {
		return Denyf(CodeDigestMismatch, "command payload does not match its digest")
	}
	if b.WorkerGeneration > b.CurrentGeneration {
		return Denyf(CodeStaleGeneration, "binding generation is newer than the credential generation")
	}
	if expect.Known {
		return nil
	}
	if b.WorkerGeneration < expect.HighestGeneration {
		return Denyf(CodeStaleGeneration, "command generation %d is older than generation %d already seen", b.WorkerGeneration, expect.HighestGeneration)
	}
	if b.AuthorityExpiresAt.IsZero() || !now.Before(b.AuthorityExpiresAt) {
		return Denyf(CodeAuthorityExpired, "command authority expired at %s", b.AuthorityExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// CanonicalDigest returns the lowercase SHA-256 of the canonical JSON form of
// raw: object keys sorted, insignificant whitespace removed, numbers preserved.
func CanonicalDigest(raw []byte) (string, error) {
	canonical, err := Canonicalize(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Canonicalize re-encodes one JSON document deterministically.
func Canonicalize(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("canonicalize JSON: trailing data")
	}
	return json.Marshal(value)
}

// DigestOf canonicalizes the JSON encoding of v.
func DigestOf(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return CanonicalDigest(raw)
}

// Report states.
const (
	// ReportTerminal carries a complete StackKits result.
	ReportTerminal = "terminal"
	// ReportOutcomeUnknown says the effect may or may not have happened. It is
	// non-terminal: a later revision resolves it.
	ReportOutcomeUnknown = "outcome_unknown"
	// ReportRejected says the Guard refused the command before any effect.
	ReportRejected = "rejected"
	// ReportResolved settles an outcome_unknown command (revision 2 and later)
	// from reconciliation evidence; Resolution says how.
	ReportResolved = "resolved"
)

// Resolutions of an outcome_unknown command (revision 2 and later).
const (
	ResolutionObservedConverged = "observed_converged"
	ResolutionNotApplied        = "not_applied"
	ResolutionEscalated         = "operator_escalation"
)

// Dispositions an authenticated operator may commit for an escalated
// outcome_unknown. They are carried as the Resolution of a resolved revision
// whose actor is the operator; a Guard can never report them.
const (
	// DispositionVerifiedApplied says the operator verified the effect is in place.
	DispositionVerifiedApplied = "verified_applied"
	// DispositionVerifiedNotApplied says the operator verified the effect did not happen.
	DispositionVerifiedNotApplied = "verified_not_applied"
	// DispositionAcceptUnknown says the operator accepts the uncertainty and
	// releases the fence without a verified outcome.
	DispositionAcceptUnknown = "accept_unknown"
)

// Revision actors.
const (
	ActorGuard    = "guard"
	ActorOperator = "operator"
)

// OperatorDisposition reports whether resolution is one an operator may commit.
func OperatorDisposition(resolution string) bool {
	switch resolution {
	case DispositionVerifiedApplied, DispositionVerifiedNotApplied, DispositionAcceptUnknown:
		return true
	}
	return false
}

// OperatorResolutionNotice is an operator resolution revision the control plane
// hands to a Guard that still holds the command as unsettled in its journal, so
// the Guard-side fence is released by the same committed evidence.
type OperatorResolutionNotice struct {
	CommandID   string `json:"command_id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
	PriorDigest string `json:"prior_digest"`
	Resolution  string `json:"resolution"`
	Reason      string `json:"reason,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
}

// ResultReport is the immutable outcome record a Guard delivers. Revision 1 is
// the first report; a resolution of an outcome_unknown command is a later
// revision whose PriorDigest names the evidence it resolves.
type ResultReport struct {
	Revision    int64  `json:"revision"`
	State       string `json:"state"`
	Digest      string `json:"digest"`
	PriorDigest string `json:"prior_digest,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// ResultDigest is the digest of one StackKits result as transported. It
// excludes SensitiveResultJson: that transient bearer material is delivered
// once, on the synchronous path, and is never persisted, so a redelivery from
// the journal must carry the same digest as the original.
func ResultDigest(result *agentpb.StackKitResult) (string, error) {
	if result == nil {
		return "", fmt.Errorf("result is required")
	}
	if len(result.SensitiveResultJson) != 0 {
		stripped := proto.Clone(result).(*agentpb.StackKitResult)
		stripped.SensitiveResultJson = nil
		result = stripped
	}
	return DigestOf(result)
}

// ComputeDigest returns the digest a report must carry. A terminal report is
// addressed by its result; any other report by its own immutable fields.
func (r ResultReport) ComputeDigest(commandID string, result *agentpb.StackKitResult) (string, error) {
	if r.State == ReportTerminal {
		if result == nil {
			return "", fmt.Errorf("terminal report requires a result")
		}
		return ResultDigest(result)
	}
	return DigestOf(map[string]any{
		"command_id": commandID, "revision": r.Revision, "state": r.State, "reason": r.Reason,
		"resolution": r.Resolution, "prior_digest": r.PriorDigest, "truncated": r.Truncated,
	})
}

// Acknowledgement is the control-plane answer to an accepted report. It is sent
// only after the authoritative transaction committed.
type Acknowledgement struct {
	CommandID    string `json:"command_id"`
	Revision     int64  `json:"revision"`
	Digest       string `json:"digest"`
	Accepted     bool   `json:"accepted"`
	Acknowledged bool   `json:"acknowledged"`
	// Duplicate is true when an identical report had already been committed.
	Duplicate bool `json:"duplicate,omitempty"`
}

// JournalHealth is the additive heartbeat projection of a Guard's durable
// operation journal.
type JournalHealth struct {
	Contract                 string `json:"contract"`
	State                    string `json:"state"`
	Reason                   string `json:"reason,omitempty"`
	UnresolvedOutcomeUnknown int    `json:"unresolved_outcome_unknown"`
	UnackedResults           int    `json:"unacked_results"`
	InFlight                 int    `json:"in_flight"`
	SchemaVersion            int    `json:"schema_version"`
	Bytes                    int64  `json:"bytes"`
	BudgetBytes              int64  `json:"budget_bytes"`
}

// Journal states reported in heartbeat.
const (
	JournalOK     = "ok"
	JournalDenied = "denied"
)

// Normalize bounds an untrusted health report before the control plane stores
// it.
func (h JournalHealth) Normalize() JournalHealth {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 1_000_000 {
			return 1_000_000
		}
		return v
	}
	h.Contract = ContractVersion
	if h.State != JournalOK {
		h.State = JournalDenied
	}
	if len(h.Reason) > MaxReasonBytes {
		h.Reason = h.Reason[:MaxReasonBytes]
	}
	h.UnresolvedOutcomeUnknown = clamp(h.UnresolvedOutcomeUnknown)
	h.UnackedResults = clamp(h.UnackedResults)
	h.InFlight = clamp(h.InFlight)
	if h.SchemaVersion < 0 {
		h.SchemaVersion = 0
	}
	if h.Bytes < 0 {
		h.Bytes = 0
	}
	if h.BudgetBytes < 0 {
		h.BudgetBytes = 0
	}
	return h
}
