package guardposture

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Wire states of the security projection.
const (
	SecurityCompliant     = "compliant"
	SecurityDrifted       = "drifted"
	SecurityUnknown       = "unknown"
	SecurityExceptionOnly = "exception_only"
)

// Finding is one control that is not compliant, as the read model shows it.
type Finding struct {
	Control     string         `json:"control"`
	State       string         `json:"state"`
	Severity    string         `json:"severity"`
	Expected    string         `json:"expected"`
	Observed    string         `json:"observed"`
	Remediation Remediation    `json:"remediation"`
	ReasonCode  string         `json:"reason_code,omitempty"`
	Exception   *ExceptionInfo `json:"exception,omitempty"`
}

// Security is the projected host security posture of one server.
type Security struct {
	State           string     `json:"state"`
	ReasonCode      string     `json:"reason_code,omitempty"`
	BaselineVersion string     `json:"baseline_version,omitempty"`
	ObservedAt      *time.Time `json:"observed_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Findings        []Finding  `json:"findings"`
}

// BackupView and DrillView are the projected recovery evidence.
type BackupView struct {
	State          string     `json:"state"`
	ReasonCode     string     `json:"reason_code,omitempty"`
	LastSnapshotAt *time.Time `json:"last_snapshot_at,omitempty"`
	PolicyMaxAge   string     `json:"policy_max_age"`
}

type DrillView struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// PolicyMaxAge is the age past which a verified receipt is stale.
	PolicyMaxAge string `json:"policy_max_age"`
}

// Recovery is the projected recoverability. A fresh snapshot never proves that
// a restore works: only a verified drill receipt speaks for that.
type Recovery struct {
	Backup       BackupView `json:"backup"`
	RestoreDrill DrillView  `json:"restore_drill"`
}

// GuardUpdate is the Guard's own update posture.
type GuardUpdate struct {
	Version string `json:"version,omitempty"`
	Signed  bool   `json:"signed"`
	State   string `json:"state"`
}

// OSUpdates are the operating system's update facts, read from the security
// evidence. A field the evidence does not state is absent, never guessed.
type OSUpdates struct {
	PendingSecurityUpdates *int       `json:"pending_security_updates,omitempty"`
	RebootRequired         *bool      `json:"reboot_required,omitempty"`
	LastUnattendedRun      *time.Time `json:"last_unattended_run,omitempty"`
}

// Updates is the projected update posture.
type Updates struct {
	Guard GuardUpdate `json:"guard"`
	OS    OSUpdates   `json:"os"`
}

// View is everything the server read model carries about posture.
type View struct {
	Security Security `json:"security"`
	Recovery Recovery `json:"recovery"`
	Updates  Updates  `json:"updates"`
}

// Recovery states.
const (
	BackupFresh     = "fresh"
	BackupStale     = "stale"
	DrillVerified   = "verified"
	DrillStale      = "stale"
	RecoveryUnknown = "unknown"
)

// Reason codes of a recovery fact that is unknown.
const (
	ReasonObservationStale = "observation_stale"
	ReasonNoReceipt        = "no_receipt"
	ReasonClockSkew        = "clock_skew"
)

var severityByControl = map[string]string{
	ControlFirewall: "critical", ControlSSHPassword: "high", ControlSSHRootLogin: "high",
	ControlSSHPort: "medium", ControlBruteForce: "medium", ControlUnattended: "medium",
	ControlPendingSecurity: "high", ControlRebootRequired: "medium", ControlSysctl: "low",
	ControlListeners: "high", ControlPublishedPorts: "high", ControlCertificateExpiry: "high",
	// A user CA that stays trusted after its revocation keeps SSH logins open;
	// an expiring host certificate only costs clients their host verification.
	ControlSSHUserCA: "high", ControlSSHHostCertificate: "medium",
}

// Severity is how serious a drifted control is. An unobservable control is
// low; an approved exception is informational.
func Severity(control string, state State) string {
	switch state {
	case StateException:
		return "info"
	case StateUnknown:
		return "low"
	}
	if severity, ok := severityByControl[control]; ok {
		return severity
	}
	return "medium"
}

// observationMaxAge is how old the newest report may be before a fact it carries
// is no longer current; a silent Guard must not keep a pass alive.
func (p Policy) observationMaxAge() time.Duration { return 4 * p.VerifyInterval }

// Project computes the read model of one Guard at the given time. It never
// depends on the heartbeat: a connected, healthy server with no fresh evidence
// is unknown.
func (p Policy) Project(posture *Posture, now time.Time) View {
	p = p.Normalize()
	view := View{
		Security: p.projectSecurity(posture, now),
		Recovery: p.projectRecovery(posture, now),
		Updates:  p.projectUpdates(posture, now),
	}
	return view
}

func (p Policy) projectSecurity(posture *Posture, now time.Time) Security {
	out := Security{State: SecurityUnknown, ReasonCode: ReasonNotObserved, Findings: []Finding{}}
	if posture == nil {
		return out
	}
	record := posture.Security
	if record.Evidence == nil {
		if record.Status != "" && record.Status != ObservationObserved && record.ReasonCode != "" {
			out.ReasonCode = record.ReasonCode
		} else if record.Status == ObservationObserved {
			out.ReasonCode = ReasonEvidenceRejected
		}
		return out
	}
	evidence := record.Evidence
	observed, expires := evidence.ObservedAt.UTC(), evidence.ExpiresAt.UTC()
	out.BaselineVersion, out.ObservedAt, out.ExpiresAt = evidence.BaselineVersion, &observed, &expires
	verdict, reason := evidence.Judge(now)
	switch verdict {
	case StateCompliant:
		out.State, out.ReasonCode = SecurityCompliant, ""
	case StateDrifted:
		out.State, out.ReasonCode = SecurityDrifted, ""
	case StateException:
		out.State, out.ReasonCode = SecurityExceptionOnly, ""
	default:
		out.State, out.ReasonCode = SecurityUnknown, reason
	}
	if reason == ReasonEvidenceStale {
		return out
	}
	for _, control := range evidence.Controls {
		if control.State == StateCompliant {
			continue
		}
		out.Findings = append(out.Findings, Finding{
			Control: control.ID, State: string(control.State), Severity: Severity(control.ID, control.State),
			Expected: control.Expected, Observed: control.Observed, Remediation: control.Remediation,
			ReasonCode: control.ReasonCode, Exception: control.Exception,
		})
	}
	return out
}

func (p Policy) projectRecovery(posture *Posture, now time.Time) Recovery {
	out := Recovery{
		Backup:       BackupView{State: RecoveryUnknown, ReasonCode: ReasonNotObserved, PolicyMaxAge: isoDuration(p.BackupMaxAge)},
		RestoreDrill: DrillView{State: RecoveryUnknown, ReasonCode: ReasonNotObserved, PolicyMaxAge: isoDuration(p.RestoreDrillMaxAge)},
	}
	if posture == nil || posture.Backup.Status == "" {
		return out
	}
	record := posture.Backup
	if record.Status != ObservationObserved {
		reason := firstNonEmpty(record.ReasonCode, ReasonObservationFailed)
		out.Backup.ReasonCode, out.RestoreDrill.ReasonCode = reason, reason
		if record.Status == ObservationFailed || record.Status == ObservationUnavailable {
			// The receipts the failed observation carried are not read as current,
			// but they keep aging: past the bound they are stale.
			out.Backup.State, out.Backup.LastSnapshotAt = agedReceipt(record.SnapshotState, record.LastSnapshotAt, now, p.BackupMaxAge, BackupStale)
			out.RestoreDrill.State, out.RestoreDrill.VerifiedAt = agedReceipt(record.RestoreState, record.LastRestoreAt, now, p.RestoreDrillMaxAge, DrillStale)
		}
		return out
	}
	if now.Sub(record.ReportedAt) > p.observationMaxAge() {
		out.Backup.ReasonCode, out.RestoreDrill.ReasonCode = ReasonObservationStale, ReasonObservationStale
		return out
	}
	out.Backup.State, out.Backup.ReasonCode, out.Backup.LastSnapshotAt = receiptState(record.SnapshotState, record.LastSnapshotAt, now, p.BackupMaxAge, BackupFresh, BackupStale)
	out.RestoreDrill.State, out.RestoreDrill.ReasonCode, out.RestoreDrill.VerifiedAt = receiptState(record.RestoreState, record.LastRestoreAt, now, p.RestoreDrillMaxAge, DrillVerified, DrillStale)
	return out
}

// receiptState judges one receipt's age. Only a recorded receipt counts, a
// receipt from the future is clock skew, and nothing is ever inferred from its
// absence.
func receiptState(state string, at *time.Time, now time.Time, maxAge time.Duration, ok, stale string) (string, string, *time.Time) {
	if state != "recorded" || at == nil || at.IsZero() {
		reason := ReasonNoReceipt
		if state == "clock-skew" {
			reason = ReasonClockSkew
		}
		return RecoveryUnknown, reason, nil
	}
	utc := at.UTC()
	age := now.Sub(utc)
	switch {
	case age < -maxClockSkew:
		return RecoveryUnknown, ReasonClockSkew, &utc
	case age <= maxAge:
		return ok, "", &utc
	default:
		return stale, "", &utc
	}
}

// agedReceipt judges a receipt carried over a failed observation: stale once it
// is older than the bound, otherwise unknown, since an unread repository cannot
// show it is current.
func agedReceipt(state string, at *time.Time, now time.Time, maxAge time.Duration, stale string) (string, *time.Time) {
	if state != "recorded" || at == nil || at.IsZero() {
		return RecoveryUnknown, nil
	}
	utc := at.UTC()
	if now.Sub(utc) > maxAge {
		return stale, &utc
	}
	return RecoveryUnknown, &utc
}

// guardUpdateStates maps the executor's update states to the read model's.
var guardUpdateStates = map[string]string{
	"": "current", "update_committed": "current", "guard_layout_migrated": "current",
	"update_staged": "staged", "update_trial": "trial", "update_denied": "denied",
	"update_rolled_back": "rolled_back", "guard_layout_rolled_back": "layout_rolled_back",
	"recovery-needed": "recovery_needed",
}

var (
	pendingSecurityPattern = regexp.MustCompile(`^(\d{1,9}) security updates? pending`)
	lastRunPattern         = regexp.MustCompile(`last run (\d{4}-\d{2}-\d{2}T[0-9:.]+(?:Z|[+-]\d{2}:\d{2}))`)
)

func (p Policy) projectUpdates(posture *Posture, now time.Time) Updates {
	out := Updates{Guard: GuardUpdate{State: RecoveryUnknown}}
	if posture == nil {
		return out
	}
	out.Guard.Version, out.Guard.Signed = posture.GuardVersion, posture.GuardSigned
	if !posture.GuardObservedAt.IsZero() {
		mapped, known := guardUpdateStates[posture.GuardUpdateState]
		if !known {
			mapped = posture.GuardUpdateState
		}
		out.Guard.State = mapped
		if posture.GuardMode != "split" {
			// Only the privilege-split layout admits nothing but signed releases.
			out.Guard.Signed = false
		}
	}
	evidence := posture.Security.Evidence
	if evidence == nil || !now.Before(evidence.ExpiresAt) {
		return out
	}
	if control, ok := evidence.Control(ControlPendingSecurity); ok && control.State != StateUnknown {
		if match := pendingSecurityPattern.FindStringSubmatch(control.Observed); match != nil {
			if count, err := strconv.Atoi(match[1]); err == nil {
				out.OS.PendingSecurityUpdates = &count
			}
		}
	}
	if control, ok := evidence.Control(ControlRebootRequired); ok && control.State != StateUnknown {
		required := control.State == StateDrifted
		out.OS.RebootRequired = &required
	}
	if control, ok := evidence.Control(ControlUnattended); ok {
		if match := lastRunPattern.FindStringSubmatch(control.Observed); match != nil {
			if at, err := time.Parse(time.RFC3339, match[1]); err == nil {
				utc := at.UTC()
				out.OS.LastUnattendedRun = &utc
			}
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// isoDuration renders a duration as an ISO 8601 duration (P35D, PT36H).
func isoDuration(d time.Duration) string {
	if d <= 0 {
		return "PT0S"
	}
	seconds := int64(d / time.Second)
	days, seconds := seconds/86400, seconds%86400
	hours, seconds := seconds/3600, seconds%3600
	minutes, seconds := seconds/60, seconds%60
	var b strings.Builder
	b.WriteString("P")
	if days > 0 {
		b.WriteString(strconv.FormatInt(days, 10) + "D")
	}
	if hours > 0 || minutes > 0 || seconds > 0 {
		b.WriteString("T")
		if hours > 0 {
			b.WriteString(strconv.FormatInt(hours, 10) + "H")
		}
		if minutes > 0 {
			b.WriteString(strconv.FormatInt(minutes, 10) + "M")
		}
		if seconds > 0 {
			b.WriteString(strconv.FormatInt(seconds, 10) + "S")
		}
	}
	return b.String()
}
