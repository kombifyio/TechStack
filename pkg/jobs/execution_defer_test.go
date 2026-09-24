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

// Winning the claim or entering a different wait reason starts a fresh block;
// neither may inherit a stale backoff or alert clock.
func TestExecutionDeferStreakRestartsAcrossBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		priorAttempts int
		clear         bool
		nextReason    string
	}{
		{name: "job proceeds", priorAttempts: 5, clear: true, nextReason: WaitReasonStackExecution},
		{name: "wait reason changes", priorAttempts: 2, nextReason: WaitReasonExecutionClaim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewQueue(1, logger.New("error", ""))
			start := time.Now().UTC()
			for attempt := 0; attempt < tt.priorAttempts; attempt++ {
				q.noteExecutionDefer("job-1", WaitReasonStackExecution, start.Add(time.Duration(attempt)*time.Second))
			}
			if tt.clear {
				q.clearExecutionDefer("job-1")
			}

			backoff, report := q.noteExecutionDefer("job-1", tt.nextReason, start.Add(time.Hour))
			if backoff != ExecutionDeferBaseInterval || report != nil {
				t.Fatalf("fresh streak backoff=%s report=%#v", backoff, report)
			}
		})
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
