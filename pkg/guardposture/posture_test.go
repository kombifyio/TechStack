package guardposture

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func evidenceAt(observed time.Time, states map[string]State) Evidence {
	ev := Evidence{
		SchemaVersion: EvidenceSchema, Mode: "advanced", SiteKind: "home", BaselineVersion: "stackkit.host-security-baseline/1.0.0",
		ObservedAt: observed, ExpiresAt: observed.Add(45 * time.Minute), FreshnessSecs: 2700,
	}
	for _, id := range requiredControls {
		state := StateCompliant
		if s, ok := states[id]; ok {
			state = s
		}
		control := Control{ID: id, State: state, Expected: "expected " + id, Observed: "observed " + id, ObservedAt: observed, Remediation: Remediation{Capability: RemediationAutomatic}}
		if state == StateException {
			control.Exception = &ExceptionInfo{Owner: "owner", Reason: "accepted", ExpiresAt: observed.Add(24 * time.Hour)}
		}
		ev.Controls = append(ev.Controls, control)
	}
	return ev
}

func reportBody(t *testing.T, seq int64, at time.Time, security *SecurityReport, backup *BackupReport) []byte {
	t.Helper()
	raw, err := json.Marshal(Report{SchemaVersion: ReportSchema, SourceEpoch: "epoch-1", Sequence: seq, ObservedAt: at, GuardVersion: "v1", Security: security, Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func observed(t *testing.T, ev Evidence) *SecurityReport {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return &SecurityReport{Status: ObservationObserved, Evidence: raw}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type recordingSink struct {
	mu     sync.Mutex
	events []AlertEvent
}

func (s *recordingSink) EnqueueAlert(_ context.Context, _ *sql.Tx, event AlertEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		suffix := "opened"
		if e.Resolved {
			suffix = "resolved"
		}
		out = append(out, e.EventKey+":"+suffix)
	}
	return out
}

func newTestService(t *testing.T) (*Service, *MemoryStore, *recordingSink, *clock) {
	t.Helper()
	sink := &recordingSink{}
	store := NewMemoryStore(sink)
	c := &clock{now: t0}
	svc, err := NewService(ServiceConfig{Store: store, Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, store, sink, c
}

var ident = Identity{TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1", OwnerSubjectID: "auth0|owner"}

// A server is secure only while fresh evidence says so. Evidence past its expiry,
// a CLI that does not know the command, a document the control plane cannot
// read and a schema it does not speak are all unknown; none is ever compliant,
// and a rejected document does not leave the earlier pass standing.
func TestPostureIsUnknownUnlessFreshEvidenceSaysOtherwise(t *testing.T) {
	svc, store, _, clk := newTestService(t)
	ctx := context.Background()
	ingest := func(seq int64, sec *SecurityReport) {
		t.Helper()
		if _, err := svc.Ingest(ctx, ident, "node", reportBody(t, seq, clk.now, sec, nil)); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	state := func() Security {
		t.Helper()
		posture, _ := store.Get(ctx, ident.TenantID, ident.WorkerID)
		return svc.View(posture).Security
	}

	if got := state(); got.State != SecurityUnknown || got.ReasonCode != ReasonNotObserved {
		t.Fatalf("a Guard that never reported is %+v", got)
	}

	ingest(1, observed(t, evidenceAt(clk.now, nil)))
	if got := state(); got.State != SecurityCompliant {
		t.Fatalf("fresh compliant evidence = %+v", got)
	}

	clk.now = clk.now.Add(46 * time.Minute) // heartbeats continue; the evidence has expired
	if got := state(); got.State != SecurityUnknown || got.ReasonCode != ReasonEvidenceStale || len(got.Findings) != 0 {
		t.Fatalf("expired evidence = %+v", got)
	}

	ingest(2, observed(t, evidenceAt(clk.now, nil)))
	if got := state(); got.State != SecurityCompliant {
		t.Fatalf("renewed evidence = %+v", got)
	}

	future := evidenceAt(clk.now, nil)
	future.SchemaVersion = "stackkit.host-security-evidence/v2"
	ingest(3, observed(t, future))
	if got := state(); got.State != SecurityUnknown || got.ReasonCode != ReasonEvidenceRejected {
		t.Fatalf("a schema this control plane does not speak = %+v (the earlier pass must not stand)", got)
	}

	ingest(4, &SecurityReport{Status: ObservationUnsupported, ReasonCode: ReasonCLIUnsupported})
	if got := state(); got.State != SecurityUnknown || got.ReasonCode != ReasonCLIUnsupported {
		t.Fatalf("an older StackKits CLI = %+v", got)
	}

	// A declared verdict is never trusted: the controls decide.
	lying := evidenceAt(clk.now, map[string]State{ControlFirewall: StateDrifted})
	lying.Overall = StateCompliant
	ingest(5, observed(t, lying))
	if got := state(); got.State != SecurityDrifted || len(got.Findings) != 1 || got.Findings[0].Control != ControlFirewall {
		t.Fatalf("a contradicting overall = %+v", got)
	}

	// A baseline missing a required control was not fully observed.
	partial := evidenceAt(clk.now, nil)
	partial.Controls = partial.Controls[:3]
	ingest(6, observed(t, partial))
	if got := state(); got.State != SecurityUnknown || got.ReasonCode != ReasonIncompleteEvidence {
		t.Fatalf("an incomplete baseline = %+v", got)
	}
}

// Evidence is read against a closed control vocabulary. A node that reports the
// opt-in SSH certificate access controls is read like any other, and a finding
// on them reaches the owner; a control id nobody defined rejects the whole
// document, so the earlier verdict never stands on evidence that cannot be read.
func TestEvidenceIsReadAgainstTheClosedControlVocabulary(t *testing.T) {
	svc, store, _, clk := newTestService(t)
	ctx := context.Background()
	security := func(seq int64, extra ...Control) Security {
		t.Helper()
		ev := evidenceAt(clk.now, nil)
		ev.Controls = append(ev.Controls, extra...)
		if _, err := svc.Ingest(ctx, ident, "node", reportBody(t, seq, clk.now, observed(t, ev), nil)); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		posture, _ := store.Get(ctx, ident.TenantID, ident.WorkerID)
		return svc.View(posture).Security
	}
	control := func(id string, state State) Control {
		return Control{ID: id, State: state, Expected: "expected " + id, Observed: "observed " + id, ObservedAt: clk.now, Remediation: Remediation{Capability: RemediationAutomatic}}
	}

	got := security(1, control(ControlSSHUserCA, StateDrifted), control(ControlSSHHostCertificate, StateCompliant))
	if got.State != SecurityDrifted || len(got.Findings) != 1 || got.Findings[0].Control != ControlSSHUserCA {
		t.Fatalf("evidence with the SSH access controls = %+v", got)
	}

	got = security(2, control("ssh.everything", StateCompliant))
	if got.State != SecurityUnknown || got.ReasonCode != ReasonEvidenceRejected || len(got.Findings) != 0 {
		t.Fatalf("evidence with an undefined control = %+v (the earlier drift must not stand)", got)
	}
}

// One drifted control is one finding, one alert and, once it is observed right
// again, one resolution. Repeated reports, and a control unattended upgrades
// are still given time to clear, raise nothing extra.
func TestOneFindingIsOneAlertAndOneResolution(t *testing.T) {
	svc, store, sink, clk := newTestService(t)
	ctx := context.Background()
	seq := int64(0)
	send := func(states map[string]State) {
		t.Helper()
		seq++
		if _, err := svc.Ingest(ctx, ident, "node", reportBody(t, seq, clk.now, observed(t, evidenceAt(clk.now, states)), nil)); err != nil {
			t.Fatal(err)
		}
		clk.now = clk.now.Add(15 * time.Minute)
	}
	drift := map[string]State{ControlSSHPassword: StateDrifted, ControlPendingSecurity: StateDrifted}
	for i := 0; i < 4; i++ {
		send(drift)
	}
	if got := sink.kinds(); len(got) != 1 || got[0] != "guard.ssh_password_auth_enabled:opened" {
		t.Fatalf("alerts after repeated drift = %v (pending security updates are still inside their grace)", got)
	}
	open, _ := store.OpenFindings(ctx, ident.TenantID, ident.WorkerID)
	if len(open) != 2 {
		t.Fatalf("open findings = %d", len(open))
	}

	clk.now = clk.now.Add(73 * time.Hour)
	send(drift)
	send(nil)
	send(nil)
	want := map[string]int{
		"guard.ssh_password_auth_enabled:opened": 1, "guard.ssh_password_auth_enabled:resolved": 1,
		"guard.security_updates_pending:opened": 1, "guard.security_updates_pending:resolved": 1,
	}
	got := map[string]int{}
	for _, kind := range sink.kinds() {
		got[kind]++
	}
	if len(got) != len(want) {
		t.Fatalf("alerts = %v", got)
	}
	for kind, n := range want {
		if got[kind] != n {
			t.Fatalf("alerts = %v", got)
		}
	}
	if open, _ := store.OpenFindings(ctx, ident.TenantID, ident.WorkerID); len(open) != 0 {
		t.Fatalf("findings still open after compliance: %d", len(open))
	}
}

// A backup that stops being readable is not a pass and not a silence. While
// the repository cannot be read the last receipt keeps aging; it is never read
// as fresh, and once it is older than the bound the backup is stale: one
// finding, one alert, Recoverable withdrawn until a readable repository shows a
// new snapshot, which resolves the finding once.
func TestUnreadableBackupAgesIntoOneStaleFindingAndAlert(t *testing.T) {
	svc, store, sink, clk := newTestService(t)
	ctx := context.Background()
	seq := int64(0)
	send := func(backup *BackupReport) {
		t.Helper()
		seq++
		if _, err := svc.Ingest(ctx, ident, "node", reportBody(t, seq, clk.now, nil, backup)); err != nil {
			t.Fatal(err)
		}
	}
	view := func() Recovery {
		t.Helper()
		posture, _ := store.Get(ctx, ident.TenantID, ident.WorkerID)
		return svc.View(posture).Recovery
	}
	good := func() *BackupReport {
		at := clk.now
		return &BackupReport{Status: ObservationObserved, SnapshotState: "recorded", LastSnapshotAt: &at, RestoreState: "recorded", LastRestoreAt: &at}
	}
	unreadable := &BackupReport{Status: ObservationFailed, ReasonCode: ReasonObservationFailed}

	send(good())
	snapshotAt := clk.now
	clk.now = clk.now.Add(20 * time.Minute)
	send(unreadable)
	if got := view(); got.Backup.State == BackupFresh || got.RestoreDrill.State == DrillVerified || len(sink.kinds()) != 0 {
		t.Fatalf("an unreadable repository inside the bound = %+v, alerts %v (a receipt is not a current pass)", got, sink.kinds())
	}

	clk.now = snapshotAt.Add(DefaultPolicy().BackupMaxAge + time.Hour)
	send(unreadable)
	clk.now = clk.now.Add(15 * time.Minute)
	send(unreadable)
	got := view()
	if got.Backup.State != BackupStale || got.Backup.LastSnapshotAt == nil || !got.Backup.LastSnapshotAt.Equal(snapshotAt) {
		t.Fatalf("an unreadable repository past the bound = %+v", got.Backup)
	}
	if kinds := sink.kinds(); len(kinds) != 1 || kinds[0] != "guard.backup_stale:opened" {
		t.Fatalf("alerts for a repository unreadable past the bound = %v", kinds)
	}

	clk.now = clk.now.Add(15 * time.Minute)
	send(good())
	if got := view(); got.Backup.State != BackupFresh {
		t.Fatalf("a readable repository with a new snapshot = %+v", got.Backup)
	}
	if kinds := sink.kinds(); len(kinds) != 2 || kinds[1] != "guard.backup_stale:resolved" {
		t.Fatalf("alerts after the next snapshot = %v", kinds)
	}
	if open, _ := store.OpenFindings(ctx, ident.TenantID, ident.WorkerID); len(open) != 0 {
		t.Fatalf("findings still open after a new snapshot: %d", len(open))
	}
}

// A repair is admitted only for a control the fresh evidence reports drifted and
// automatically repairable, and only inside its bounds: one at a time, a
// cooldown per control, a daily attempt budget and a circuit breaker on the
// server. Everything else is a structured denial with guidance.
func TestRepairIsAdmittedOnlyInsideItsBounds(t *testing.T) {
	policy := DefaultPolicy()
	posture := func(states map[string]State) *Posture {
		ev := evidenceAt(t0, states)
		return &Posture{Security: SecurityRecord{Status: ObservationObserved, Evidence: &ev}}
	}
	drifted := posture(map[string]State{ControlBruteForce: StateDrifted, ControlSSHPassword: StateDrifted, ControlPendingSecurity: StateDrifted, ControlSysctl: StateException})
	for i := range drifted.Security.Evidence.Controls {
		if drifted.Security.Evidence.Controls[i].ID == ControlPendingSecurity {
			drifted.Security.Evidence.Controls[i].Remediation = Remediation{Capability: RemediationManual, Action: "stackkit host updates plan"}
		}
		if drifted.Security.Evidence.Controls[i].ID == ControlSSHPassword {
			drifted.Security.Evidence.Controls[i].ReasonCode = "no_authorized_key"
		}
	}
	// The SSH certificate access controls are restored from node custody when
	// their drift can be; a host certificate that needs a renewed delivery cannot.
	drifted.Security.Evidence.Controls = append(drifted.Security.Evidence.Controls,
		Control{ID: ControlSSHUserCA, State: StateDrifted, ObservedAt: t0, Remediation: Remediation{Capability: RemediationAutomatic}},
		Control{ID: ControlSSHHostCertificate, State: StateDrifted, ObservedAt: t0, ReasonCode: "ssh_host_certificate_expiring",
			Remediation: Remediation{Capability: RemediationManual, Action: "deliver a renewed host certificate (ssh-host-certificate)"}})
	deny := func(history []Repair, p *Posture, now time.Time, controls ...string) string {
		t.Helper()
		err := policy.AdmitRepair(p, history, controls, now)
		if err == nil {
			return ""
		}
		denial, ok := IsRepairDenial(err)
		if !ok {
			t.Fatalf("not a structured denial: %v", err)
		}
		return denial.Code
	}
	now := t0.Add(time.Minute)
	for _, tc := range []struct {
		name     string
		posture  *Posture
		controls []string
		want     string
	}{
		{"no evidence", nil, []string{ControlBruteForce}, DenyNoEvidence},
		{"drifted and automatic", drifted, []string{ControlBruteForce}, ""},
		{"compliant control", drifted, []string{ControlFirewall}, DenyNotDrifted},
		{"approved exception", drifted, []string{ControlSysctl}, DenyException},
		{"manual control", drifted, []string{ControlPendingSecurity}, DenyNotRepairable},
		{"password login without a key", drifted, []string{ControlSSHPassword}, DenyNeedsAuthorizedKey},
		{"SSH CA trust restorable from custody", drifted, []string{ControlSSHUserCA}, ""},
		{"host certificate needing a renewed delivery", drifted, []string{ControlSSHHostCertificate}, DenyNotRepairable},
		{"not a control", drifted, []string{"firewall.everything"}, DenyControlUnknown},
	} {
		if got := deny(nil, tc.posture, now, tc.controls...); got != tc.want {
			t.Errorf("%s: denial %q, want %q", tc.name, got, tc.want)
		}
	}

	finished := func(at time.Time, status string, controls ...string) Repair {
		return Repair{ID: at.String(), Controls: controls, Status: status, RequestedAt: at.Add(-time.Minute), FinishedAt: &at}
	}
	if got := deny([]Repair{{Controls: []string{ControlBruteForce}, Status: RepairDispatched, RequestedAt: now, ExpiresAt: now.Add(10 * time.Minute)}}, drifted, now.Add(time.Minute), ControlBruteForce); got != DenyInProgress {
		t.Errorf("a second repair while one runs: %q", got)
	}
	done := finished(now.Add(-10*time.Minute), RepairSucceeded, ControlBruteForce)
	if got := deny([]Repair{done}, drifted, now, ControlBruteForce); got != DenyCooldown {
		t.Errorf("inside the cooldown: %q", got)
	}
	var daily []Repair
	for _, hours := range []int{20, 12, 4} {
		daily = append(daily, finished(now.Add(-time.Duration(hours)*time.Hour), RepairSucceeded, ControlBruteForce))
	}
	if got := deny(daily, drifted, now, ControlBruteForce); got != DenyAttemptsExhausted {
		t.Errorf("a control that keeps reverting: %q", got)
	}
	failures := []Repair{
		finished(now.Add(-50*time.Minute), RepairFailed, ControlFirewall),
		finished(now.Add(-40*time.Minute), RepairBlocked, ControlFirewall),
		finished(now.Add(-35*time.Minute), RepairOutcomeUnknown, ControlSysctl),
	}
	if got := deny(failures, drifted, now, ControlBruteForce); got != DenyCircuitOpen {
		t.Errorf("after three failures in a row: %q", got)
	}
	renewed := *drifted
	renewedEvidence := *drifted.Security.Evidence
	renewedEvidence.ExpiresAt = now.Add(9 * time.Hour)
	renewed.Security.Evidence = &renewedEvidence
	if got := deny(failures, &renewed, now.Add(7*time.Hour), ControlBruteForce); got != "" {
		t.Errorf("after the breaker pause: %q", got)
	}
	recovered := append([]Repair{finished(now.Add(-20*time.Minute), RepairSucceeded, ControlSysctl)}, failures...)
	if got := deny(recovered, drifted, now, ControlBruteForce); got != "" {
		t.Errorf("a success closes the breaker: %q", got)
	}

	// Automatic repair is observe-only without a live owner approval, and covers
	// only the controls the approval names.
	approval := &AutoRepairPolicy{Enabled: true, Controls: []string{ControlBruteForce, ControlSSHPassword}, ExpiresAt: ptr(now.Add(time.Hour))}
	if got := AutoRepairCandidates(approval, drifted, now); len(got) != 1 || got[0] != ControlBruteForce {
		t.Errorf("candidates under approval = %v", got)
	}
	if got := AutoRepairCandidates(approval, drifted, now.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("an expired approval repaired %v", got)
	}
	if got := AutoRepairCandidates(nil, drifted, now); len(got) != 0 {
		t.Errorf("no approval repaired %v", got)
	}
	for _, id := range []string{ControlSSHPassword, ControlSSHUserCA, ControlSSHHostCertificate} {
		if err := policy.ValidateAutoRepair([]string{id}, now.Add(time.Hour), now); err == nil {
			t.Errorf("%s may never be repaired automatically", id)
		}
	}
	if err := policy.ValidateAutoRepair([]string{ControlBruteForce}, now.Add(400*24*time.Hour), now); err == nil {
		t.Error("an approval that never expires was accepted")
	}
}

func ptr[T any](v T) *T { return &v }

// Posture is tenant scoped: one tenant never reads another's Guard, evidence or
// repairs, even with the same worker id.
func TestPostureReadsAreTenantScoped(t *testing.T) {
	svc, store, _, clk := newTestService(t)
	ctx := context.Background()
	other := Identity{TenantID: "tenant-2", WorkerID: ident.WorkerID, ServerID: "server-2", OwnerSubjectID: "auth0|other"}
	if _, err := svc.Ingest(ctx, ident, "a", reportBody(t, 1, clk.now, observed(t, evidenceAt(clk.now, map[string]State{ControlFirewall: StateDrifted})), nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, other.TenantID, other.WorkerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read the posture: %v", err)
	}
	views, err := svc.Views(ctx, other.TenantID)
	if err != nil || len(views) != 0 {
		t.Fatalf("another tenant listed %v, %v", views, err)
	}
	if open, _ := store.OpenFindings(ctx, other.TenantID, other.WorkerID); len(open) != 0 {
		t.Fatalf("another tenant read findings %v", open)
	}
}

// "Verify now" is answered by an observation the Guard started after it heard of
// the request, never by one that was already in flight: that observation may
// predate the host change the owner wants checked. Only the report that echoes
// the request's token answers it, and a newer request replaces the token.
func TestVerifyRequestIsAnsweredOnlyByAnObservationStartedAfterIt(t *testing.T) {
	svc, _, _, clk := newTestService(t)
	ctx := context.Background()
	report := func(seq int64, echo string) Directive {
		t.Helper()
		raw, err := json.Marshal(Report{
			SchemaVersion: ReportSchema, SourceEpoch: "epoch-1", Sequence: seq, ObservedAt: clk.now, VerifyRequestID: echo,
			Security: observed(t, evidenceAt(clk.now, nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		directive, err := svc.Ingest(ctx, ident, "node", raw)
		if err != nil {
			t.Fatalf("ingest %d: %v", seq, err)
		}
		return directive
	}
	checkIn := func(seq int64) Directive {
		t.Helper()
		raw, _ := json.Marshal(Report{SchemaVersion: ReportSchema, CheckIn: true, SourceEpoch: "epoch-1", Sequence: seq, ObservedAt: clk.now})
		directive, err := svc.Ingest(ctx, ident, "node", raw)
		if err != nil {
			t.Fatalf("check-in %d: %v", seq, err)
		}
		return directive
	}

	if report(1, "").VerifyRequested {
		t.Fatal("a routine observation with no request asks for another")
	}
	clk.now = clk.now.Add(time.Minute)
	if err := svc.RequestVerify(ctx, ident, "node"); err != nil {
		t.Fatal(err)
	}
	asked := checkIn(2)
	if !asked.VerifyRequested || asked.VerifyRequestID == "" {
		t.Fatalf("the Guard is not asked: %+v", asked)
	}

	// The observation that was in flight when the request arrived reports now.
	clk.now = clk.now.Add(time.Minute)
	if got := report(3, ""); !got.VerifyRequested || got.VerifyRequestID != asked.VerifyRequestID {
		t.Fatalf("an observation started before the request answered it: %+v", got)
	}
	// A request made meanwhile replaces the token; the stale echo does not answer it.
	if err := svc.RequestVerify(ctx, ident, "node"); err != nil {
		t.Fatal(err)
	}
	renewed := checkIn(4)
	if renewed.VerifyRequestID == "" || renewed.VerifyRequestID == asked.VerifyRequestID {
		t.Fatalf("a new request kept the old token: %+v", renewed)
	}
	if got := report(5, asked.VerifyRequestID); !got.VerifyRequested || got.VerifyRequestID != renewed.VerifyRequestID {
		t.Fatalf("an echo of a replaced request answered the new one: %+v", got)
	}
	if got := report(6, renewed.VerifyRequestID); got.VerifyRequested {
		t.Fatalf("the observation that echoes the request did not answer it: %+v", got)
	}
}

// A Guard that predates request tokens never echoes one. Asking it again would
// loop, so its second report without the token is taken as the answer, as it
// was before tokens existed.
func TestVerifyRequestIsNotAskedInALoopOfAGuardThatDoesNotEchoTokens(t *testing.T) {
	svc, _, _, clk := newTestService(t)
	ctx := context.Background()
	if err := svc.RequestVerify(ctx, ident, "node"); err != nil {
		t.Fatal(err)
	}
	var asked []bool
	for seq := int64(1); seq <= 3; seq++ {
		directive, err := svc.Ingest(ctx, ident, "node", reportBody(t, seq, clk.now, observed(t, evidenceAt(clk.now, nil)), nil))
		if err != nil {
			t.Fatal(err)
		}
		asked = append(asked, directive.VerifyRequested)
	}
	if !asked[0] || asked[1] || asked[2] {
		t.Fatalf("asks across three token-less reports = %v, want one more ask and then none", asked)
	}
}
