package jobs

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

// The defer interval must grow and then stop growing. Unbounded 1/s retries
// were what turned a stack held by an unreclaimable job into a permanent hot
// loop against the durable store.
func TestExecutionDeferBackoffIsBounded(t *testing.T) {
	if got := executionDeferBackoff(1); got != ExecutionDeferBaseInterval {
		t.Fatalf("first defer = %s, want %s", got, ExecutionDeferBaseInterval)
	}
	if got := executionDeferBackoff(2); got != 2*ExecutionDeferBaseInterval {
		t.Fatalf("second defer = %s, want %s", got, 2*ExecutionDeferBaseInterval)
	}
	previous := time.Duration(0)
	for attempt := 1; attempt <= 100; attempt++ {
		got := executionDeferBackoff(attempt)
		if got < previous {
			t.Fatalf("backoff went backwards at attempt %d: %s after %s", attempt, got, previous)
		}
		if got > ExecutionDeferMaxInterval {
			t.Fatalf("backoff at attempt %d = %s, exceeds the %s cap", attempt, got, ExecutionDeferMaxInterval)
		}
		previous = got
	}
	if previous != ExecutionDeferMaxInterval {
		t.Fatalf("backoff saturated at %s, want the %s cap", previous, ExecutionDeferMaxInterval)
	}
}

// A short wait is ordinary contention and must stay quiet; a wait that outlives
// the threshold is the signal that was missing when four stacks sat blocked for
// days with nothing above info level to show for it.
func TestExecutionDeferReportsOnlyAfterTheAlertThreshold(t *testing.T) {
	q := NewQueue(1, logger.New("error", ""))
	start := time.Now().UTC()

	if _, report := q.noteExecutionDefer("job-1", WaitReasonStackExecution, start); report != nil {
		t.Fatalf("first defer reported an alert: %#v", report)
	}
	if _, report := q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(ExecutionDeferAlertAfter-time.Second)); report != nil {
		t.Fatalf("defer below the threshold reported an alert: %#v", report)
	}

	_, report := q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(ExecutionDeferAlertAfter))
	if report == nil {
		t.Fatal("a wait past the alert threshold produced no report")
	}
	if report.JobID != "job-1" || report.WaitReason != WaitReasonStackExecution {
		t.Fatalf("report identity = %#v", report)
	}
	if report.WaitingFor < ExecutionDeferAlertAfter {
		t.Fatalf("report waiting_for = %s, want at least %s", report.WaitingFor, ExecutionDeferAlertAfter)
	}
	if report.Attempts != 3 {
		t.Fatalf("report attempts = %d, want the full streak", report.Attempts)
	}

	// The repeat cadence keeps a long legitimate wait from flooding the log.
	if _, repeat := q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(ExecutionDeferAlertAfter+time.Minute)); repeat != nil {
		t.Fatalf("alert repeated inside its cadence: %#v", repeat)
	}
	if _, repeat := q.noteExecutionDefer("job-1", WaitReasonStackExecution,
		start.Add(ExecutionDeferAlertAfter+ExecutionDeferAlertEvery)); repeat == nil {
		t.Fatal("alert never repeated after its cadence elapsed")
	}
}

// Winning the claim ends the streak, so the next block starts from the base
// interval and cannot inherit a stale alert clock.
func TestExecutionDeferStreakResetsWhenTheJobProceeds(t *testing.T) {
	q := NewQueue(1, logger.New("error", ""))
	start := time.Now().UTC()

	for i := 0; i < 5; i++ {
		q.noteExecutionDefer("job-1", WaitReasonStackExecution, start)
	}
	q.clearExecutionDefer("job-1")

	backoff, report := q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(time.Hour))
	if backoff != ExecutionDeferBaseInterval {
		t.Fatalf("backoff after reset = %s, want %s", backoff, ExecutionDeferBaseInterval)
	}
	if report != nil {
		t.Fatalf("a fresh streak reported an alert immediately: %#v", report)
	}
}

// A different wait reason is a different block. Reporting a busy-stack wait as
// claim-store unavailability would send an operator to the wrong system.
func TestExecutionDeferStreakRestartsOnANewWaitReason(t *testing.T) {
	q := NewQueue(1, logger.New("error", ""))
	start := time.Now().UTC()

	q.noteExecutionDefer("job-1", WaitReasonStackExecution, start)
	q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(time.Second))

	backoff, report := q.noteExecutionDefer("job-1", WaitReasonExecutionClaim, start.Add(2*time.Second))
	if backoff != ExecutionDeferBaseInterval {
		t.Fatalf("backoff for a new reason = %s, want %s", backoff, ExecutionDeferBaseInterval)
	}
	if report != nil {
		t.Fatalf("a new reason inherited the old streak's alert: %#v", report)
	}
}

// The observer is how cmd wiring turns the signal into a metric without
// pkg/jobs depending on the monitoring stack.
func TestExecutionDeferObserverIsInstallable(t *testing.T) {
	q := NewQueue(1, logger.New("error", ""))
	if q.executionDeferObserver() != nil {
		t.Fatal("a fresh queue already has an observer")
	}
	var seen []ExecutionDeferReport
	q.SetExecutionDeferObserver(func(report ExecutionDeferReport) { seen = append(seen, report) })
	observer := q.executionDeferObserver()
	if observer == nil {
		t.Fatal("observer was not installed")
	}
	observer(ExecutionDeferReport{JobID: "job-1"})
	if len(seen) != 1 || seen[0].JobID != "job-1" {
		t.Fatalf("observer did not receive the report: %#v", seen)
	}
	q.SetExecutionDeferObserver(nil)
	if q.executionDeferObserver() != nil {
		t.Fatal("observer was not cleared")
	}
}
