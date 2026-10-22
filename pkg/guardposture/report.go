package guardposture

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// ReportSchema is the Guard-to-control-plane posture report.
const ReportSchema = "kombify.guard-posture/v1"

// MaxReportBytes bounds one report: the evidence plus a small envelope.
const MaxReportBytes = MaxEvidenceBytes + 32<<10

// Statuses of one observation a Guard makes.
const (
	// ObservationObserved carries a document the Guard read from StackKits.
	ObservationObserved = "observed"
	// ObservationUnsupported means the pinned StackKits release lacks the
	// command. It is an observation gap, never an error and never secure.
	ObservationUnsupported = "unsupported"
	// ObservationUnavailable means the observation could not run here (no
	// runtime, no workspace).
	ObservationUnavailable = "unavailable"
	// ObservationFailed means it ran and did not produce a document.
	ObservationFailed = "failed"
)

var reasonToken = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// requestToken bounds the opaque token that pairs a verify request with the
// observation that answers it.
var requestToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Report is what a Guard sends. A check-in carries no observation: it only asks
// whether the control plane wants a fresh one.
type Report struct {
	SchemaVersion string    `json:"schema_version"`
	CheckIn       bool      `json:"check_in,omitempty"`
	SourceEpoch   string    `json:"source_epoch"`
	Sequence      int64     `json:"source_sequence"`
	ObservedAt    time.Time `json:"observed_at"`
	GuardVersion  string    `json:"guard_version,omitempty"`
	// GuardSigned is true when this Guard's update path admits only signed
	// releases (the split layout without an unsigned-build opt-in).
	GuardSigned bool `json:"guard_signed"`
	// VerifyRequestID echoes the token of a directive's verify request. A Guard
	// sets it only on a report whose observation it started after it received
	// that directive; a routine observation carries none.
	VerifyRequestID string          `json:"verify_request_id,omitempty"`
	Security        *SecurityReport `json:"security,omitempty"`
	Backup          *BackupReport   `json:"backup,omitempty"`
}

// SecurityReport is one host security observation.
type SecurityReport struct {
	Status     string          `json:"status"`
	ReasonCode string          `json:"reason_code,omitempty"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
}

// BackupReport is the minimal, parsed backup and restore-drill evidence of one
// observation: timestamps and states, never paths or repository identity.
type BackupReport struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code,omitempty"`
	Ready      bool   `json:"ready,omitempty"`
	// SnapshotState and RestoreState are StackKits' own receipt states:
	// recorded, unverified or clock-skew.
	SnapshotState  string     `json:"snapshot_state,omitempty"`
	LastSnapshotAt *time.Time `json:"last_snapshot_at,omitempty"`
	RestoreState   string     `json:"restore_state,omitempty"`
	LastRestoreAt  *time.Time `json:"last_restore_at,omitempty"`
}

// ErrReportInvalid marks a report the control plane refuses.
var ErrReportInvalid = errors.New("guardposture: the posture report is not acceptable")

func invalidReport(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrReportInvalid, fmt.Sprintf(format, args...))
}

// Validate checks the envelope. It does not read the evidence: the service
// does, so a rejected document is recorded as such.
func (r *Report) Validate(now time.Time) error {
	if r.SchemaVersion != ReportSchema {
		return invalidReport("schema %q is not %s", r.SchemaVersion, ReportSchema)
	}
	r.SourceEpoch = strings.TrimSpace(r.SourceEpoch)
	if r.SourceEpoch == "" || len(r.SourceEpoch) > 64 || r.Sequence < 0 {
		return invalidReport("the source epoch or sequence is missing")
	}
	r.GuardVersion = strings.TrimSpace(r.GuardVersion)
	if len(r.GuardVersion) > 64 {
		r.GuardVersion = r.GuardVersion[:64]
	}
	if r.VerifyRequestID != "" && !requestToken.MatchString(r.VerifyRequestID) {
		return invalidReport("the verify request id is not a bounded token")
	}
	if r.ObservedAt.IsZero() || r.ObservedAt.After(now.Add(maxClockSkew)) {
		return invalidReport("the observation time is missing or in the future")
	}
	if r.CheckIn {
		if r.Security != nil || r.Backup != nil {
			return invalidReport("a check-in carries no observation")
		}
		return nil
	}
	if r.Security == nil && r.Backup == nil {
		return invalidReport("the report observes nothing")
	}
	if r.Security != nil {
		if err := validateObservation(r.Security.Status, r.Security.ReasonCode, len(r.Security.Evidence) > 0); err != nil {
			return err
		}
	}
	if r.Backup != nil {
		if err := validateObservation(r.Backup.Status, r.Backup.ReasonCode, false); err != nil {
			return err
		}
		for _, state := range []string{r.Backup.SnapshotState, r.Backup.RestoreState} {
			switch state {
			case "", "recorded", "unverified", "clock-skew":
			default:
				return invalidReport("receipt state %q is unknown", state)
			}
		}
	}
	return nil
}

func validateObservation(status, reason string, hasDocument bool) error {
	switch status {
	case ObservationObserved:
		if reason != "" {
			return invalidReport("an observed report names no failure reason")
		}
	case ObservationUnsupported, ObservationUnavailable, ObservationFailed:
		if hasDocument {
			return invalidReport("a %s observation carries no document", status)
		}
		if !reasonToken.MatchString(reason) {
			return invalidReport("the reason code is not a bounded token")
		}
	default:
		return invalidReport("status %q is unknown", status)
	}
	return nil
}

// Directive is the control plane's answer to a report or a check-in.
type Directive struct {
	// VerifyRequested asks the Guard to observe the host now.
	VerifyRequested bool `json:"verify_requested"`
	// VerifyRequestID is the token the Guard echoes in the report of the fresh
	// observation it starts for this request. A Guard that does not know it
	// ignores it.
	VerifyRequestID string `json:"verify_request_id,omitempty"`
	// VerifyIntervalSeconds is the cadence of routine observations.
	VerifyIntervalSeconds int `json:"verify_interval_seconds"`
	// LocalPolicy is the signed grant of the owner's current repair approval,
	// present only while it is active and the control plane can sign it. A
	// Guard holds it in memory and may repair under it on its own for a short
	// while when the control plane is unreachable; without it the Guard only
	// observes.
	LocalPolicy *guardbinding.LocalRepairGrant `json:"local_policy,omitempty"`
}
