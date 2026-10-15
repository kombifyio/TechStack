// Package guardposture is the control-plane side of kombify Guard posture: the
// host security evidence a Guard observes through the pinned StackKits CLI, the
// backup and restore evidence next to it, the findings and alerts they raise,
// and the bounded repair a named drifted control may be given.
//
// The rules it keeps are the ones the Guard-ready definition rests on. A
// heartbeat never implies security. Evidence past its expiry is unknown, an
// unreadable, unsupported or incomplete observation is unknown and never
// compliant, and a snapshot's age never proves that a restore works. Nothing
// here trusts a verdict the Guard sent: the overall state is recomputed from
// the controls, and time is judged by the control plane's own clock.
package guardposture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// EvidenceSchema is the StackKits host security evidence document a Guard
// forwards. Any other version is rejected: a verdict this control plane cannot
// read is never treated as a pass.
const EvidenceSchema = "stackkit.host-security-evidence/v1"

// Bounds of an untrusted evidence document.
const (
	MaxEvidenceBytes = 256 << 10
	maxControls      = 32
	maxTextBytes     = 512
	maxNotices       = 16
	// maxClockSkew is how far into the future an observation may claim to be.
	maxClockSkew = 5 * time.Minute
	minFreshness = time.Minute
	maxFreshness = 24 * time.Hour
)

// State is the judgement of one control or of the whole host.
type State string

const (
	StateCompliant State = "compliant"
	StateDrifted   State = "drifted"
	StateUnknown   State = "unknown"
	StateException State = "exception"
)

// Control ids the control plane treats specially.
const (
	ControlFirewall          = "firewall.default_inbound"
	ControlSSHPassword       = "ssh.password_authentication"
	ControlSSHRootLogin      = "ssh.root_login"
	ControlSSHPort           = "ssh.port"
	ControlBruteForce        = "bruteforce.fail2ban"
	ControlUnattended        = "updates.unattended_upgrades"
	ControlPendingSecurity   = "updates.pending_security"
	ControlRebootRequired    = "updates.reboot_required"
	ControlSysctl            = "kernel.sysctl"
	ControlListeners         = "exposure.listeners"
	ControlPublishedPorts    = "exposure.published_ports"
	ControlCertificateExpiry = "tls.certificate_expiry"
	// ControlSSHUserCA and ControlSSHHostCertificate are opt-in: a node reports
	// them only once custody records SSH access, so they are never required.
	ControlSSHUserCA          = "ssh.user_ca_trust"
	ControlSSHHostCertificate = "ssh.host_certificate"
)

// requiredControls are the controls whose absence means the baseline was not
// fully observed. Their absence makes the evidence unknown, never compliant.
var requiredControls = []string{
	ControlFirewall, ControlSSHPassword, ControlSSHRootLogin, ControlSSHPort, ControlBruteForce,
	ControlUnattended, ControlSysctl, ControlPendingSecurity, ControlRebootRequired,
}

// Remediation capabilities.
const (
	RemediationAutomatic = "automatic"
	RemediationManual    = "manual"
	RemediationNone      = "none"
)

// Remediation says who can restore a control.
type Remediation struct {
	Capability string `json:"capability"`
	Action     string `json:"action,omitempty"`
}

// ExceptionInfo names the owner decision behind an approved exception.
type ExceptionInfo struct {
	Owner     string    `json:"owner"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Control is one judged baseline control of the evidence.
type Control struct {
	ID          string         `json:"id"`
	State       State          `json:"state"`
	Expected    string         `json:"expected"`
	Observed    string         `json:"observed"`
	ObservedAt  time.Time      `json:"observed_at"`
	Reason      string         `json:"reason,omitempty"`
	ReasonCode  string         `json:"reason_code,omitempty"`
	Remediation Remediation    `json:"remediation"`
	Exception   *ExceptionInfo `json:"exception,omitempty"`
}

// ExceptionRecord reports a declared exception and whether it applied.
type ExceptionRecord struct {
	Control   string    `json:"control"`
	Owner     string    `json:"owner,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
}

// Evidence is the stackkit.host-security-evidence/v1 document.
type Evidence struct {
	SchemaVersion   string            `json:"schema_version"`
	Kit             string            `json:"kit,omitempty"`
	Mode            string            `json:"mode"`
	SiteKind        string            `json:"site_kind"`
	BaselineVersion string            `json:"baseline_version"`
	NodeRef         string            `json:"node_ref,omitempty"`
	PlanHash        string            `json:"plan_hash,omitempty"`
	ObservedAt      time.Time         `json:"observed_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
	FreshnessSecs   int               `json:"freshness_budget_seconds"`
	Overall         State             `json:"overall"`
	Controls        []Control         `json:"controls"`
	Exceptions      []ExceptionRecord `json:"exceptions,omitempty"`
	Notices         []string          `json:"notices,omitempty"`
}

// ErrEvidenceInvalid marks a document the control plane refuses to read.
var ErrEvidenceInvalid = errors.New("guardposture: host security evidence is not acceptable")

func invalidEvidence(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEvidenceInvalid, fmt.Sprintf(format, args...))
}

// ParseEvidence reads an untrusted evidence document. The schema version must be
// the exact one, every state and control id must be known, and the freshness
// window must be sane. The declared overall state is recomputed from the
// controls, so a Guard cannot vouch for a host its own controls contradict.
func ParseEvidence(raw []byte, now time.Time) (*Evidence, error) {
	if len(raw) == 0 || len(raw) > MaxEvidenceBytes {
		return nil, invalidEvidence("the document is empty or exceeds %d bytes", MaxEvidenceBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, invalidEvidence("it does not decode: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, invalidEvidence("it holds more than one document")
	}
	if evidence.SchemaVersion != EvidenceSchema {
		return nil, invalidEvidence("schema %q is not %s", evidence.SchemaVersion, EvidenceSchema)
	}
	switch evidence.Mode {
	case "standard", "advanced":
	default:
		return nil, invalidEvidence("mode %q is unknown", evidence.Mode)
	}
	switch evidence.SiteKind {
	case "home", "cloud", "unknown":
	default:
		return nil, invalidEvidence("site kind %q is unknown", evidence.SiteKind)
	}
	if !strings.HasPrefix(evidence.BaselineVersion, "stackkit.host-security-baseline/") || len(evidence.BaselineVersion) > 64 {
		return nil, invalidEvidence("baseline version %q is not a host security baseline", evidence.BaselineVersion)
	}
	if evidence.ObservedAt.IsZero() || evidence.ExpiresAt.IsZero() || evidence.ObservedAt.After(now.Add(maxClockSkew)) {
		return nil, invalidEvidence("the observation time is missing or in the future")
	}
	window := evidence.ExpiresAt.Sub(evidence.ObservedAt)
	if window < minFreshness || window > maxFreshness {
		return nil, invalidEvidence("the freshness window %s is outside %s to %s", window, minFreshness, maxFreshness)
	}
	if n := len(evidence.Controls); n == 0 || n > maxControls {
		return nil, invalidEvidence("it carries %d controls", n)
	}
	seen := map[string]bool{}
	for i := range evidence.Controls {
		control := &evidence.Controls[i]
		if !stackkitcommand.ValidHostSecurityControl(control.ID) || seen[control.ID] {
			return nil, invalidEvidence("control %q is unknown or repeated", control.ID)
		}
		seen[control.ID] = true
		if !validState(control.State) {
			return nil, invalidEvidence("control %s has unknown state %q", control.ID, control.State)
		}
		if control.State == StateException && (control.Exception == nil || control.Exception.Owner == "" || control.Exception.ExpiresAt.IsZero()) {
			return nil, invalidEvidence("control %s is an exception without an owner and an expiry", control.ID)
		}
		switch control.Remediation.Capability {
		case RemediationAutomatic, RemediationManual, RemediationNone:
		default:
			return nil, invalidEvidence("control %s has unknown remediation %q", control.ID, control.Remediation.Capability)
		}
		control.Expected, control.Observed = clip(control.Expected), clip(control.Observed)
		control.Reason, control.ReasonCode = clip(control.Reason), clip(control.ReasonCode)
		control.Remediation.Action = clip(control.Remediation.Action)
		if control.Exception != nil {
			control.Exception.Owner, control.Exception.Reason = clip(control.Exception.Owner), clip(control.Exception.Reason)
		}
	}
	for i := range evidence.Exceptions {
		exception := &evidence.Exceptions[i]
		exception.Owner, exception.Reason, exception.Detail = clip(exception.Owner), clip(exception.Reason), clip(exception.Detail)
	}
	if len(evidence.Exceptions) > maxControls {
		evidence.Exceptions = evidence.Exceptions[:maxControls]
	}
	if len(evidence.Notices) > maxNotices {
		evidence.Notices = evidence.Notices[:maxNotices]
	}
	for i := range evidence.Notices {
		evidence.Notices[i] = clip(evidence.Notices[i])
	}
	evidence.Kit, evidence.NodeRef, evidence.PlanHash = clip(evidence.Kit), clip(evidence.NodeRef), clip(evidence.PlanHash)
	evidence.Overall, _ = judgeControls(evidence.Controls)
	return &evidence, nil
}

func validState(state State) bool {
	switch state {
	case StateCompliant, StateDrifted, StateUnknown, StateException:
		return true
	}
	return false
}

func clip(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxTextBytes {
		value = strings.ToValidUTF8(value[:maxTextBytes], "")
	}
	return value
}

// Reason codes of a verdict the control plane cannot call compliant.
const (
	ReasonEvidenceStale      = "evidence_stale"
	ReasonNotObserved        = "not_observed"
	ReasonIncompleteEvidence = "incomplete_evidence"
	ReasonCLIUnsupported     = "stackkit_cli_unsupported"
	ReasonRuntimeUnavailable = "stackkit_runtime_unavailable"
	ReasonObservationFailed  = "observation_failed"
	ReasonEvidenceRejected   = "evidence_rejected"
)

// judgeControls computes the overall state from the controls: drifted when any
// control is drifted, else unknown when any is unknown or a required control is
// missing, else exception when any is an approved exception, else compliant.
// The second result is the reason code when the verdict is unknown.
func judgeControls(controls []Control) (State, string) {
	var drifted, unknown, exception bool
	present := map[string]bool{}
	for _, control := range controls {
		present[control.ID] = true
		switch control.State {
		case StateDrifted:
			drifted = true
		case StateUnknown:
			unknown = true
		case StateException:
			exception = true
		}
	}
	if drifted {
		return StateDrifted, ""
	}
	if unknown {
		return StateUnknown, ReasonNotObserved
	}
	for _, id := range requiredControls {
		if !present[id] {
			return StateUnknown, ReasonIncompleteEvidence
		}
	}
	if exception {
		return StateException, ""
	}
	return StateCompliant, ""
}

// Judge is the verdict of the evidence at the given time. Evidence past its
// expiry is unknown whatever it once said.
func (e *Evidence) Judge(now time.Time) (State, string) {
	if e == nil {
		return StateUnknown, ReasonNotObserved
	}
	if !now.Before(e.ExpiresAt) {
		return StateUnknown, ReasonEvidenceStale
	}
	return judgeControls(e.Controls)
}

// Control returns the named control, if the evidence holds it.
func (e *Evidence) Control(id string) (Control, bool) {
	if e != nil {
		for _, control := range e.Controls {
			if control.ID == id {
				return control, true
			}
		}
	}
	return Control{}, false
}
