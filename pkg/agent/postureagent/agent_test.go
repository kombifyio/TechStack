package postureagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardexec"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

func envelope(command, status string, data string) []byte {
	return []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"` + command + `","status":"` + status + `","data":` + data + `}`)
}

// The Guard forwards only a successful StackKits result of the command it asked
// for. A release without the command is an observation gap, a failed or foreign
// document is a failure, and neither carries evidence the control plane could
// read as a pass.
func TestGuardForwardsOnlyTheSuccessDocumentOfTheCommandItRan(t *testing.T) {
	good := envelope(hostSecurityCommand, "success", `{"overall":"compliant"}`)
	for name, tc := range map[string]struct {
		observation guardexec.Observation
		status      string
		reason      string
	}{
		"success":             {guardexec.Observation{Supported: true, Stdout: good}, guardposture.ObservationObserved, ""},
		"older StackKits":     {guardexec.Observation{Reason: guardexec.ReasonCLIUnsupported}, guardposture.ObservationUnsupported, guardposture.ReasonCLIUnsupported},
		"no runtime":          {guardexec.Observation{Reason: guardexec.ReasonRuntimeUnavailable}, guardposture.ObservationUnavailable, guardexec.ReasonRuntimeUnavailable},
		"no executor":         {guardexec.Observation{Reason: "executor_unavailable"}, guardposture.ObservationUnavailable, "executor_unavailable"},
		"command failed":      {guardexec.Observation{Supported: true, ExitCode: 1, Stdout: good}, guardposture.ObservationFailed, guardposture.ReasonObservationFailed},
		"denied envelope":     {guardexec.Observation{Supported: true, Stdout: envelope(hostSecurityCommand, "denied", `{}`)}, guardposture.ObservationFailed, guardposture.ReasonObservationFailed},
		"another command":     {guardexec.Observation{Supported: true, Stdout: envelope("stackkit host security repair", "success", `{}`)}, guardposture.ObservationFailed, guardposture.ReasonObservationFailed},
		"not a StackKits doc": {guardexec.Observation{Supported: true, Stdout: []byte(`{"overall":"compliant"}`)}, guardposture.ObservationFailed, guardposture.ReasonObservationFailed},
	} {
		got := SecurityReport(tc.observation)
		if got.Status != tc.status || got.ReasonCode != tc.reason || (tc.status != guardposture.ObservationObserved && len(got.Evidence) != 0) {
			t.Errorf("%s: %+v, want %s/%s", name, got, tc.status, tc.reason)
		}
	}
	at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	backup := BackupReport(guardexec.Observation{Supported: true, Stdout: envelope(backupStatusCommand, "success",
		`{"repositoryId":"secret-repo","ready":true,"history":{"snapshot":{"state":"recorded","recordedAt":"2026-09-30T08:00:00Z","ageSeconds":10},"stagedRestore":{"state":"unverified"},"availability":{"state":"unverified"}}}`)})
	if backup.Status != guardposture.ObservationObserved || !backup.Ready || backup.SnapshotState != "recorded" || backup.LastSnapshotAt == nil || !backup.LastSnapshotAt.Equal(at) || backup.RestoreState != "unverified" || backup.LastRestoreAt != nil {
		t.Fatalf("backup report = %+v", backup)
	}
	if raw, _ := json.Marshal(backup); strings.Contains(string(raw), "secret-repo") {
		t.Fatalf("the backup report carries repository identity: %s", raw)
	}
	if got := BackupReport(guardexec.Observation{Supported: true, ExitCode: 1, Stderr: "no backup configured"}); got.Status != guardposture.ObservationFailed {
		t.Fatalf("a backup status that failed reports %+v", got)
	}
}

type fakeObserver struct {
	mu     sync.Mutex
	verify int
	err    error
}

func (o *fakeObserver) HostSecurityVerify(context.Context) (guardexec.Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.verify++
	if o.err != nil {
		return guardexec.Observation{}, o.err
	}
	return guardexec.Observation{Supported: true, Stdout: envelope(hostSecurityCommand, "success", `{"overall":"compliant"}`)}, nil
}
func (o *fakeObserver) BackupStatus(context.Context) (guardexec.Observation, error) {
	return guardexec.Observation{Reason: guardexec.ReasonWorkspaceUnavailable}, nil
}

// An operator's request for a fresh observation is answered at the next
// check-in, not at the next routine cycle, by an observation that echoes the
// request's token (a routine one carries none), and an executor that cannot be
// reached is reported as a failed observation instead of silence.
func TestGuardAnswersARequestedVerificationWithoutWaitingForTheRoutineCycle(t *testing.T) {
	var mu sync.Mutex
	var bodies []guardposture.Report
	requestVerify := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var report guardposture.Report
		_ = json.Unmarshal(raw, &report)
		mu.Lock()
		bodies = append(bodies, report)
		ask := requestVerify && report.CheckIn
		if ask {
			requestVerify = false
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"posture": guardposture.Directive{VerifyRequested: ask, VerifyRequestID: map[bool]string{true: "req-1"}[ask], VerifyIntervalSeconds: 3600}}})
	}))
	defer server.Close()
	observer := &fakeObserver{}
	agent, err := New(Config{URL: server.URL, HTTPClient: server.Client(), Observer: observer, Version: "v1", CheckIn: 10 * time.Millisecond, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go agent.Run(ctx)
	waitFor(t, func() bool { observer.mu.Lock(); defer observer.mu.Unlock(); return observer.verify == 1 })
	mu.Lock()
	requestVerify = true
	mu.Unlock()
	waitFor(t, func() bool { observer.mu.Lock(); defer observer.mu.Unlock(); return observer.verify == 2 })

	mu.Lock()
	defer mu.Unlock()
	full := 0
	var echoed []string
	var last int64
	for _, report := range bodies {
		if report.Sequence <= last {
			t.Fatalf("sequence did not advance: %d after %d", report.Sequence, last)
		}
		last = report.Sequence
		if !report.CheckIn {
			full++
			echoed = append(echoed, report.VerifyRequestID)
			if report.Security == nil || report.Security.Status != guardposture.ObservationObserved || report.SourceEpoch == "" {
				t.Fatalf("full report = %+v", report)
			}
		}
	}
	if full != 2 {
		t.Fatalf("full reports = %d, want the first observation and the requested one", full)
	}
	if len(echoed) != 2 || echoed[0] != "" || echoed[1] != "req-1" {
		t.Fatalf("request tokens echoed by the full reports = %q, want none on the routine observation and the request's on the one it started", echoed)
	}
	if got := failedSecurity(&guardexec.Error{Code: guardexec.CodeUnavailable, Message: "down"}); got.Status != guardposture.ObservationFailed || got.ReasonCode != "executor_unavailable" {
		t.Fatalf("an unreachable executor reports %+v", got)
	}
	if got := failedSecurity(errors.New("boom")); got.ReasonCode != guardposture.ReasonObservationFailed {
		t.Fatalf("a failed run reports %+v", got)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
