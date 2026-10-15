package guardposture

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Repair modes.
const (
	RepairOperator = "operator"
	RepairAuto     = "auto"
)

// Repair statuses. queued and dispatched are live; every other one is final.
const (
	RepairQueued         = "queued"
	RepairDispatched     = "dispatched"
	RepairSucceeded      = "succeeded"
	RepairManualRequired = "manual_required"
	RepairBlocked        = "blocked"
	RepairFailed         = "failed"
	RepairExpired        = "expired"
	RepairOutcomeUnknown = "outcome_unknown"
)

// RepairStep is the bounded record of one control's repair.
type RepairStep struct {
	Control    string `json:"control"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code,omitempty"`
}

// Repair is one admitted request to restore named drifted controls.
type Repair struct {
	ID           string       `json:"id"`
	TenantID     string       `json:"-"`
	WorkerID     string       `json:"-"`
	ServerID     string       `json:"server_id"`
	Mode         string       `json:"mode"`
	RequestedBy  string       `json:"requested_by"`
	Controls     []string     `json:"controls"`
	Status       string       `json:"status"`
	ReasonCode   string       `json:"reason_code,omitempty"`
	CommandID    string       `json:"command_id"`
	Steps        []RepairStep `json:"steps,omitempty"`
	RequestedAt  time.Time    `json:"requested_at"`
	ExpiresAt    time.Time    `json:"expires_at"`
	DispatchedAt *time.Time   `json:"dispatched_at,omitempty"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
}

// Live reports whether the repair still holds its server.
func (r Repair) Live(now time.Time) bool {
	return (r.Status == RepairQueued || r.Status == RepairDispatched) && now.Before(r.ExpiresAt.Add(dispatchGrace))
}

// dispatchGrace is how long past its expiry a dispatched repair may still
// report; past it the outcome is unknown.
const dispatchGrace = 25 * time.Minute

func (r Repair) failure() bool {
	switch r.Status {
	case RepairFailed, RepairBlocked, RepairExpired, RepairOutcomeUnknown:
		return true
	}
	return false
}

// Stable denial codes of a repair request.
const (
	DenyNoEvidence          = "no_fresh_evidence"
	DenyControlUnknown      = "control_unknown"
	DenyNotDrifted          = "control_not_drifted"
	DenyNotRepairable       = "control_not_repairable"
	DenyException           = "control_has_exception"
	DenyNeedsAuthorizedKey  = "needs_authorized_key"
	DenyInProgress          = "repair_in_progress"
	DenyCooldown            = "repair_cooldown"
	DenyAttemptsExhausted   = "repair_attempts_exhausted"
	DenyCircuitOpen         = "repair_circuit_open"
	DenyAutoRepairNotActive = "auto_repair_not_active"
)

// RepairDenial is a structured refusal with guidance the caller can act on.
type RepairDenial struct {
	Code       string
	Message    string
	Guidance   string
	Control    string
	RetryAfter *time.Time
}

func (d *RepairDenial) Error() string { return d.Code + ": " + d.Message }

// IsRepairDenial reports whether err is a repair denial and returns it.
func IsRepairDenial(err error) (*RepairDenial, bool) {
	var denial *RepairDenial
	ok := errors.As(err, &denial)
	return denial, ok
}

// AdmitRepair decides whether the named controls may be repaired now. It rests
// on fresh evidence only, refuses a control that is not drifted, not
// automatically repairable or under an approved exception, and applies the
// bounds: one repair at a time, a cooldown per control, a daily attempt budget
// per control and a circuit breaker on the server. history is the server's
// recent repairs.
func (p Policy) AdmitRepair(posture *Posture, history []Repair, controls []string, now time.Time) error {
	p = p.Normalize()
	if posture == nil || posture.Security.Evidence == nil || !now.Before(posture.Security.Evidence.ExpiresAt) {
		return &RepairDenial{Code: DenyNoEvidence, Message: "No fresh host security evidence exists for this server.",
			Guidance: "Verify the server, then request the repair for a control the fresh evidence reports as drifted."}
	}
	evidence := posture.Security.Evidence
	if len(controls) == 0 || len(controls) > stackkitcommand.MaxHostSecurityRepairControls {
		return &RepairDenial{Code: DenyControlUnknown, Message: fmt.Sprintf("Name between one and %d controls.", stackkitcommand.MaxHostSecurityRepairControls)}
	}
	seen := map[string]bool{}
	for _, id := range controls {
		if !stackkitcommand.ValidHostSecurityControl(id) || seen[id] {
			return &RepairDenial{Code: DenyControlUnknown, Control: id, Message: "The control is unknown or named twice."}
		}
		seen[id] = true
		control, ok := evidence.Control(id)
		switch {
		case !ok || control.State == StateUnknown:
			return &RepairDenial{Code: DenyControlUnknown, Control: id, Message: "The control was not observed, so it cannot be repaired.", Guidance: "Verify the server again."}
		case control.State == StateException:
			return &RepairDenial{Code: DenyException, Control: id, Message: "An approved exception covers this control.", Guidance: "Remove or let the exception expire before repairing the control."}
		case control.State != StateDrifted:
			return &RepairDenial{Code: DenyNotDrifted, Control: id, Message: "The control is compliant."}
		case control.ReasonCode == "no_authorized_key":
			return &RepairDenial{Code: DenyNeedsAuthorizedKey, Control: id, Message: "Disabling password logins would lock the owner out: no account holds an authorized SSH key.", Guidance: control.Remediation.Action}
		case control.Remediation.Capability != RemediationAutomatic:
			return &RepairDenial{Code: DenyNotRepairable, Control: id, Message: "This control is not restored automatically.", Guidance: control.Remediation.Action}
		}
	}
	for _, repair := range history {
		if repair.Live(now) {
			return &RepairDenial{Code: DenyInProgress, Message: "Another repair of this server is still running."}
		}
	}
	if until, open := p.breakerOpenUntil(history, now); open {
		return &RepairDenial{Code: DenyCircuitOpen, RetryAfter: &until,
			Message:  fmt.Sprintf("The last %d repairs of this server failed, so automatic repair is paused.", p.Repair.BreakerFailures),
			Guidance: "Inspect the server, fix the cause and repair again once the pause ends."}
	}
	for _, id := range controls {
		attempts, last := 0, time.Time{}
		var oldest time.Time
		for _, repair := range history {
			if !slices.Contains(repair.Controls, id) {
				continue
			}
			if repair.RequestedAt.After(last) {
				last = repair.RequestedAt
			}
			if now.Sub(repair.RequestedAt) < 24*time.Hour {
				attempts++
				if oldest.IsZero() || repair.RequestedAt.Before(oldest) {
					oldest = repair.RequestedAt
				}
			}
		}
		if !last.IsZero() && now.Sub(last) < p.Repair.ControlCooldown {
			after := last.Add(p.Repair.ControlCooldown)
			return &RepairDenial{Code: DenyCooldown, Control: id, RetryAfter: &after, Message: "This control was repaired a moment ago.", Guidance: "Wait for the cooldown and verify the server before repairing again."}
		}
		if attempts >= p.Repair.MaxAttempts {
			after := oldest.Add(24 * time.Hour)
			return &RepairDenial{Code: DenyAttemptsExhausted, Control: id, RetryAfter: &after,
				Message:  fmt.Sprintf("This control was repaired %d times in a day without staying compliant.", attempts),
				Guidance: "Investigate what reverts the control instead of repairing it again."}
		}
	}
	return nil
}

// breakerOpenUntil reports whether the server's repair circuit breaker is open:
// the most recent BreakerFailures finished repairs all failed and the pause has
// not passed. A success closes it.
func (p Policy) breakerOpenUntil(history []Repair, now time.Time) (time.Time, bool) {
	finished := make([]Repair, 0, len(history))
	for _, repair := range history {
		if repair.FinishedAt != nil {
			finished = append(finished, repair)
		}
	}
	if len(finished) < p.Repair.BreakerFailures {
		return time.Time{}, false
	}
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].FinishedAt.After(*finished[j].FinishedAt) })
	for _, repair := range finished[:p.Repair.BreakerFailures] {
		if !repair.failure() {
			return time.Time{}, false
		}
	}
	until := finished[0].FinishedAt.Add(p.Repair.BreakerOpenFor)
	return until, now.Before(until)
}

// RepairCommandID is the typed command identity of one repair.
func RepairCommandID(repairID string) string { return "guard-sec-repair:" + repairID }

// AutoRepairSafeControls are the only controls an owner may approve for
// automatic repair: restoring them cannot cut the management path or lose data.
// The ssh controls (ssh.user_ca_trust and ssh.host_certificate included: they
// rewrite sshd's trust) and every manual control stay with a person.
var AutoRepairSafeControls = []string{ControlBruteForce, ControlFirewall, ControlSysctl, ControlUnattended}

// AutoRepairPolicy is an owner's approval to repair a small safe set of controls
// without asking each time. It is off by default, expires and never renews
// itself.
type AutoRepairPolicy struct {
	TenantID   string     `json:"-"`
	WorkerID   string     `json:"-"`
	Enabled    bool       `json:"enabled"`
	Controls   []string   `json:"controls"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Active reports whether the approval is in force. A missing or expired policy
// is observe-only.
func (a *AutoRepairPolicy) Active(now time.Time) bool {
	return a != nil && a.Enabled && a.ExpiresAt != nil && now.Before(*a.ExpiresAt) && len(a.Controls) > 0
}

// ErrAutoRepairInvalid marks an approval request the control plane refuses.
var ErrAutoRepairInvalid = errors.New("guardposture: invalid automatic repair approval")

// ValidateAutoRepair checks an owner's request to enable automatic repair.
func (p Policy) ValidateAutoRepair(controls []string, expiresAt, now time.Time) error {
	p = p.Normalize()
	if len(controls) == 0 {
		return fmt.Errorf("%w: name at least one control", ErrAutoRepairInvalid)
	}
	seen := map[string]bool{}
	for _, id := range controls {
		if !slices.Contains(AutoRepairSafeControls, id) || seen[id] {
			return fmt.Errorf("%w: control %q cannot be repaired automatically; allowed: %s", ErrAutoRepairInvalid, id, strings.Join(AutoRepairSafeControls, ", "))
		}
		seen[id] = true
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(p.Repair.AutoRepairMaxTTL)) {
		return fmt.Errorf("%w: the approval must expire within %s", ErrAutoRepairInvalid, p.Repair.AutoRepairMaxTTL)
	}
	return nil
}

// AutoRepairCandidates are the controls an active approval may repair now: the
// fresh evidence reports them drifted, automatically repairable, not blocked on
// an authorized key and not under an exception.
func AutoRepairCandidates(policy *AutoRepairPolicy, posture *Posture, now time.Time) []string {
	if !policy.Active(now) || posture == nil || posture.Security.Evidence == nil || !now.Before(posture.Security.Evidence.ExpiresAt) {
		return nil
	}
	var out []string
	for _, id := range policy.Controls {
		control, ok := posture.Security.Evidence.Control(id)
		if ok && control.State == StateDrifted && control.Remediation.Capability == RemediationAutomatic && control.ReasonCode != "no_authorized_key" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// RepairOutcome maps the repair document's outcome to a repair status.
func RepairOutcomeStatus(outcome string) string {
	switch outcome {
	case "applied", "nothing_to_do":
		return RepairSucceeded
	case "manual_required":
		return RepairManualRequired
	case "blocked":
		return RepairBlocked
	default:
		return RepairFailed
	}
}
