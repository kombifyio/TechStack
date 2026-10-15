package guardposture

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Finding scopes: each report reconciles the findings of the scope it observed
// and leaves the others alone.
const (
	ScopeSecurity = "security"
	ScopeRecovery = "recovery"
	ScopeGuard    = "guard"
)

// Finding keys outside the per-control ones.
const (
	keyBackupStale       = "recovery:backup"
	keyDrillStale        = "recovery:restore_drill"
	keyUpdateRolledBack  = "guard:update_rolled_back"
	keyRecoveryNeeded    = "guard:recovery_needed"
	keyOutcomeNeedsOpRaw = "guard:outcome_needs_operator"
)

func controlKey(id string) string { return "control:" + id }

// FindingRecord is one entry of the findings ledger. An episode is opened when
// a fact is first observed wrong and closed when it is observed right again:
// one finding, at most one alert, at most one resolution.
type FindingRecord struct {
	ID             string
	TenantID       string
	WorkerID       string
	ServerID       string
	OwnerSubjectID string
	DisplayName    string
	Key            string
	Scope          string
	Severity       string
	State          string
	Expected       string
	Observed       string
	ReasonCode     string
	Capability     string
	OpenedAt       time.Time
	LastSeenAt     time.Time
	// AlertDueAt is when the owner is told; later than OpenedAt for a control
	// that unattended upgrades or a reboot are given time to clear.
	AlertDueAt time.Time
	AlertedAt  *time.Time
	ResolvedAt *time.Time
}

// Desired is a wrong fact observed now.
type Desired struct {
	Key        string
	Severity   string
	State      string
	Expected   string
	Observed   string
	ReasonCode string
	Capability string
	AlertAfter time.Duration
}

// Reconciliation is one scope's observation: the facts that are wrong, and the
// keys that were positively observed right and so may be closed. A key in
// neither set was not observed and stays as it is.
type Reconciliation struct {
	Scope   string
	Desired map[string]Desired
	Clear   map[string]bool
}

func newReconciliation(scope string) Reconciliation {
	return Reconciliation{Scope: scope, Desired: map[string]Desired{}, Clear: map[string]bool{}}
}

// Empty reports whether the observation says nothing either way.
func (r Reconciliation) Empty() bool { return len(r.Desired) == 0 && len(r.Clear) == 0 }

// SecurityReconciliation derives the control findings of fresh evidence. Stale
// evidence decides nothing: an old drift stays open until a fresh observation
// says it is gone, and an old pass cannot close anything.
func (p Policy) SecurityReconciliation(evidence *Evidence, now time.Time) Reconciliation {
	r := newReconciliation(ScopeSecurity)
	if evidence == nil || !now.Before(evidence.ExpiresAt) {
		return r
	}
	for _, control := range evidence.Controls {
		key := controlKey(control.ID)
		switch control.State {
		case StateDrifted:
			after := time.Duration(0)
			switch control.ID {
			case ControlPendingSecurity:
				after = p.PendingSecurityAlertAfter
			case ControlRebootRequired:
				after = p.RebootAlertAfter
			}
			r.Desired[key] = Desired{
				Key: key, Severity: Severity(control.ID, StateDrifted), State: string(StateDrifted),
				Expected: control.Expected, Observed: control.Observed, ReasonCode: control.ReasonCode,
				Capability: control.Remediation.Capability, AlertAfter: after,
			}
		case StateCompliant, StateException:
			r.Clear[key] = true
		}
	}
	return r
}

// RecoveryReconciliation derives the stale-backup and stale-drill findings. A
// missing or unverified receipt is unknown, not stale: a server that never
// configured backups is not alerted for not having one. A failed observation
// carries the last receipts (MergeBackup), so a repository that stays unreadable
// turns stale when its last receipt passes the bound, measured from that
// receipt; it never clears a finding.
func (p Policy) RecoveryReconciliation(record BackupRecord, now time.Time) Reconciliation {
	r := newReconciliation(ScopeRecovery)
	observed := record.Status == ObservationObserved
	if !observed && record.Status != ObservationFailed && record.Status != ObservationUnavailable {
		return r
	}
	for _, fact := range []struct {
		key, state, expectedPrefix string
		at                         *time.Time
		max                        time.Duration
		ok, stale, severity        string
	}{
		{keyBackupStale, record.SnapshotState, "snapshot receipt newer than ", record.LastSnapshotAt, p.BackupMaxAge, BackupFresh, BackupStale, "medium"},
		{keyDrillStale, record.RestoreState, "verified restore receipt newer than ", record.LastRestoreAt, p.RestoreDrillMaxAge, DrillVerified, DrillStale, "low"},
	} {
		state, _, at := receiptState(fact.state, fact.at, now, fact.max, fact.ok, fact.stale)
		switch state {
		case fact.ok:
			// Only a repository that was just read proves the receipt current; a
			// failed observation carries an old one that can only age out.
			if observed {
				r.Clear[fact.key] = true
			}
		case fact.stale:
			r.Desired[fact.key] = Desired{
				Key: fact.key, Severity: fact.severity, State: fact.stale, Expected: fact.expectedPrefix + isoDuration(fact.max),
				Observed: "last receipt " + at.UTC().Format(time.RFC3339), Capability: RemediationManual,
			}
		}
	}
	return r
}

// GuardObservation is what the heartbeat reports about the Guard itself.
type GuardObservation struct {
	Mode          string
	UpdateState   string
	UpdateVersion string
	UpdateReason  string
	DenyMutations bool
	// NeedsOperator is true when an unresolved outcome has waited past the
	// reconciliation policy and only an operator can release it.
	NeedsOperator bool
}

// GuardReconciliation derives the findings about the Guard's own update and
// operation state.
func GuardReconciliation(obs GuardObservation) Reconciliation {
	r := newReconciliation(ScopeGuard)
	flag := func(key string, wrong bool, d Desired) {
		if wrong {
			d.Key = key
			r.Desired[key] = d
		} else {
			r.Clear[key] = true
		}
	}
	flag(keyUpdateRolledBack, obs.UpdateState == "update_rolled_back", Desired{
		Severity: "high", State: "rolled_back", Expected: "the admitted Guard release runs",
		Observed: "update to " + firstNonEmpty(obs.UpdateVersion, "a new release") + " was rolled back", ReasonCode: boundedToken(obs.UpdateReason), Capability: RemediationManual,
	})
	flag(keyRecoveryNeeded, obs.UpdateState == "recovery-needed" || obs.DenyMutations, Desired{
		Severity: "critical", State: "recovery_needed", Expected: "an admissible Guard release runs",
		Observed: "no admissible Guard remains; host changes are refused", ReasonCode: boundedToken(obs.UpdateReason), Capability: RemediationManual,
	})
	flag(keyOutcomeNeedsOpRaw, obs.NeedsOperator, Desired{
		Severity: "high", State: "needs_operator", Expected: "every host operation has a known outcome",
		Observed: "an operation's outcome is unknown and needs an operator", Capability: RemediationManual,
	})
	return r
}

func boundedToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if reasonToken.MatchString(value) {
		return value
	}
	return ""
}

// plan is the ledger change a reconciliation implies.
type plan struct {
	open   []FindingRecord
	update []FindingRecord
	close  []FindingRecord
	alerts []AlertEvent
}

// planReconciliation applies one observation to the open findings of a scope.
// idFor supplies the identity of a new episode. It is pure: stores persist what
// it returns and emit its alerts in the same transaction.
func planReconciliation(rec Reconciliation, open []FindingRecord, ident Identity, name string, now time.Time, idFor func() string) plan {
	var out plan
	openByKey := map[string]FindingRecord{}
	for _, record := range open {
		if record.ResolvedAt == nil {
			openByKey[record.Key] = record
		}
	}
	for key, want := range rec.Desired {
		current, exists := openByKey[key]
		if !exists {
			current = FindingRecord{
				ID: idFor(), TenantID: ident.TenantID, WorkerID: ident.WorkerID, Key: key, Scope: rec.Scope, OpenedAt: now,
			}
			current.AlertDueAt = now.Add(want.AlertAfter)
		}
		current.ServerID, current.OwnerSubjectID, current.DisplayName = ident.ServerID, ident.OwnerSubjectID, name
		current.Severity, current.State, current.Expected, current.Observed = want.Severity, want.State, want.Expected, want.Observed
		current.ReasonCode, current.Capability, current.LastSeenAt = want.ReasonCode, want.Capability, now
		if current.AlertedAt == nil && !now.Before(current.AlertDueAt) {
			at := now
			current.AlertedAt = &at
			out.alerts = append(out.alerts, alertFor(current, false, now))
		}
		if exists {
			out.update = append(out.update, current)
		} else {
			out.open = append(out.open, current)
		}
		delete(openByKey, key)
	}
	for key, current := range openByKey {
		if !rec.Clear[key] {
			continue
		}
		at := now
		current.ResolvedAt = &at
		out.close = append(out.close, current)
		if current.AlertedAt != nil {
			out.alerts = append(out.alerts, alertFor(current, true, now))
		}
	}
	return out
}

// AlertEvent is one notification the ledger asks for: the opening of a finding
// or, once, its resolution.
type AlertEvent struct {
	Resolved       bool
	FindingID      string
	TenantID       string
	WorkerID       string
	ServerID       string
	OwnerSubjectID string
	DisplayName    string
	Key            string
	EventKey       string
	Severity       string
	ReasonCode     string
	Subject        string
	Body           string
	OccurredAt     time.Time
}

// AlertSink records an alert inside the transaction that changed the ledger, so
// the finding and its notification commit or fail together. A nil transaction
// is the in-memory store.
type AlertSink interface {
	EnqueueAlert(ctx context.Context, tx *sql.Tx, event AlertEvent) error
}

func alertFor(record FindingRecord, resolved bool, now time.Time) AlertEvent {
	eventKey, subject, body := alertCopy(record)
	if resolved {
		subject = "Resolved: " + subject
		body = "This finding is no longer observed. " + body
	}
	return AlertEvent{
		Resolved: resolved, FindingID: record.ID, TenantID: record.TenantID, WorkerID: record.WorkerID,
		ServerID: record.ServerID, OwnerSubjectID: record.OwnerSubjectID, DisplayName: record.DisplayName,
		Key: record.Key, EventKey: eventKey, Severity: record.Severity, ReasonCode: record.ReasonCode,
		Subject: subject, Body: body, OccurredAt: now.UTC(),
	}
}

// alertCopy names the event and describes it in English; the stable event key,
// control and reason code let a client localize it.
func alertCopy(record FindingRecord) (eventKey, subject, body string) {
	where := firstNonEmpty(record.DisplayName, "your server")
	detail := fmt.Sprintf("Expected %s; observed %s.", record.Expected, record.Observed)
	switch record.Key {
	case controlKey(ControlSSHPassword):
		return "guard.ssh_password_auth_enabled", "SSH password login is enabled on " + where, detail
	case controlKey(ControlFirewall):
		return "guard.firewall_not_enforced", "The firewall on " + where + " is disabled or permissive", detail
	case controlKey(ControlPendingSecurity):
		return "guard.security_updates_pending", "Security updates are overdue on " + where, detail
	case controlKey(ControlRebootRequired):
		return "guard.reboot_pending", where + " has needed a reboot for too long", detail
	case controlKey(ControlCertificateExpiry):
		return "guard.certificate_expiring", "A certificate on " + where + " is expiring", detail
	case keyBackupStale:
		return "guard.backup_stale", "The newest backup of " + where + " is out of date", detail
	case keyDrillStale:
		return "guard.restore_drill_stale", "The last verified restore drill of " + where + " is out of date", detail
	case keyUpdateRolledBack:
		return "guard.update_rolled_back", "A kombify Guard update was rolled back on " + where, detail
	case keyRecoveryNeeded:
		return "guard.recovery_needed", "kombify Guard on " + where + " needs recovery", detail
	case keyOutcomeNeedsOpRaw:
		return "guard.outcome_needs_operator", "An operation on " + where + " needs an operator", detail
	}
	control := strings.TrimPrefix(record.Key, "control:")
	return "guard.security_drift", "The security baseline drifted on " + where + ": " + control, detail
}
