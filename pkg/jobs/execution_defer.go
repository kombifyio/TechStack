package jobs

import "time"

const (
	// ExecutionDeferBaseInterval is the first retry delay after a durable
	// execution claim reports its target busy.
	ExecutionDeferBaseInterval = time.Second
	// ExecutionDeferMaxInterval caps the backoff. A stack rollout can legitimately
	// run for many minutes, so a queued follow-up must keep waiting; what it must
	// not do is re-attempt the durable claim once per second for hours.
	ExecutionDeferMaxInterval = 30 * time.Second
	// ExecutionDeferAlertAfter is how long one job may keep deferring before the
	// wait stops being ordinary contention and becomes an operational signal.
	// The lease reclaimer terminalizes an orphaned holder within roughly
	// JobExecutionLeaseTTL + one sweep interval, so anything past this threshold
	// means the block is not an orphan the reclaimer can see.
	ExecutionDeferAlertAfter = 5 * time.Minute
	// ExecutionDeferAlertEvery bounds how often a single job repeats that
	// warning, so a long legitimate wait cannot flood the log.
	ExecutionDeferAlertEvery = 5 * time.Minute
)

// ExecutionDeferReport is the observable record of one job that has been
// waiting on a durable execution claim longer than ExecutionDeferAlertAfter.
type ExecutionDeferReport struct {
	JobID      string
	JobType    JobType
	TargetType string
	TargetID   string
	WaitReason string
	// Attempts counts consecutive defers since the job last made progress.
	Attempts int
	// WaitingFor is how long this job has been deferring without interruption.
	WaitingFor time.Duration
	// NextRetryIn is the backoff the job just scheduled.
	NextRetryIn time.Duration
}

// ExecutionDeferObserver receives one report per alert-worthy defer. It exists
// so cmd wiring can turn the signal into a metric without pkg/jobs depending on
// the monitoring stack.
type ExecutionDeferObserver func(ExecutionDeferReport)

// executionDeferState tracks one job's consecutive defers so the retry can back
// off and the wait can be reported once it stops looking ordinary.
type executionDeferState struct {
	firstDeferredAt time.Time
	lastAlertedAt   time.Time
	attempts        int
	reason          string
}

// SetExecutionDeferObserver installs the alert sink. Passing nil clears it.
func (q *Queue) SetExecutionDeferObserver(observer ExecutionDeferObserver) {
	q.deferMu.Lock()
	defer q.deferMu.Unlock()
	q.deferObserver = observer
}

func (q *Queue) executionDeferObserver() ExecutionDeferObserver {
	q.deferMu.Lock()
	defer q.deferMu.Unlock()
	return q.deferObserver
}

// noteExecutionDefer records one defer and returns the backoff to use plus, if
// the wait has crossed the alert threshold and the per-job cadence allows it,
// the report to publish.
func (q *Queue) noteExecutionDefer(jobID, reason string, now time.Time) (time.Duration, *ExecutionDeferReport) {
	q.deferMu.Lock()
	defer q.deferMu.Unlock()

	if q.defers == nil {
		q.defers = make(map[string]*executionDeferState)
	}
	state, ok := q.defers[jobID]
	if !ok || state.reason != reason {
		// A different wait reason is a different block; restart the streak so a
		// busy-stack wait is never reported as claim-store unavailability.
		state = &executionDeferState{firstDeferredAt: now, reason: reason}
		q.defers[jobID] = state
	}
	state.attempts++

	backoff := executionDeferBackoff(state.attempts)
	waitingFor := now.Sub(state.firstDeferredAt)
	if waitingFor < ExecutionDeferAlertAfter {
		return backoff, nil
	}
	if !state.lastAlertedAt.IsZero() && now.Sub(state.lastAlertedAt) < ExecutionDeferAlertEvery {
		return backoff, nil
	}
	state.lastAlertedAt = now
	return backoff, &ExecutionDeferReport{
		JobID:       jobID,
		WaitReason:  reason,
		Attempts:    state.attempts,
		WaitingFor:  waitingFor.Round(time.Second),
		NextRetryIn: backoff,
	}
}

// clearExecutionDefer forgets a job's defer streak. Called whenever the job
// stops deferring, so the next block starts from the base interval.
func (q *Queue) clearExecutionDefer(jobID string) {
	q.deferMu.Lock()
	defer q.deferMu.Unlock()
	delete(q.defers, jobID)
}

// executionDeferBackoff doubles from the base interval and saturates at the
// cap. Bounding the interval is what turns an unbounded 1/s hot loop into a
// wait that costs the durable store a constant, small amount of work.
func executionDeferBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := ExecutionDeferBaseInterval
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= ExecutionDeferMaxInterval {
			return ExecutionDeferMaxInterval
		}
	}
	return delay
}
