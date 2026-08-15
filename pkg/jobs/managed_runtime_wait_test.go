package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

type enrollmentWindowTestResolver struct {
	job              *Job
	now              *time.Time
	advance          time.Duration
	markerDuringCall string
	deadlineBudget   time.Duration
	target           *ManagedRuntimeTarget
	err              error
}

func (r *enrollmentWindowTestResolver) ResolveManagedRuntimeTarget(ctx context.Context, _ ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	if r.job != nil {
		r.markerDuringCall = stringFromMap(r.job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField)
	}
	if deadline, ok := ctx.Deadline(); ok {
		r.deadlineBudget = time.Until(deadline)
	}
	if r.now != nil {
		*r.now = r.now.Add(r.advance)
	}
	return r.target, r.err
}

func TestManagedRuntimeEnrollmentWaitStartsBeforeFirstResolverAttempt(t *testing.T) {
	now := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	job := enrollmentWindowTestJob("job-first-attempt", "lease-first-attempt")
	resolver := &enrollmentWindowTestResolver{
		job: job,
		now: &now,
		err: errors.New("enrollment pending"),
	}
	cfg := enrollmentWindowTestConfig(resolver, time.Minute, 10*time.Second)

	target, err := resolveManagedRuntimeTargetWithWaitClock(
		context.Background(), cfg, job, &core.KombinationSpec{}, nil, func() time.Time { return now },
	)
	if target != nil {
		t.Fatalf("target = %#v, want nil while enrollment is pending", target)
	}
	var pending *ManagedRuntimeEnrollmentPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("error = %T %v, want ManagedRuntimeEnrollmentPendingError", err, err)
	}
	wantStartedAt := now.Format(time.RFC3339Nano)
	if resolver.markerDuringCall != wantStartedAt {
		t.Fatalf("marker visible to first resolver = %q, want %q", resolver.markerDuringCall, wantStartedAt)
	}
	if got := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); got != wantStartedAt {
		t.Fatalf("persisted wait start = %q, want %q", got, wantStartedAt)
	}
}

func TestManagedRuntimeEnrollmentWaitClampsAttemptAndResumeToRemainingWindow(t *testing.T) {
	remaining := 5 * time.Second
	if got := managedRuntimeEnrollmentBoundedDelay(30*time.Second, remaining); got != remaining {
		t.Fatalf("bounded attempt timeout = %s, want %s", got, remaining)
	}
	if got := managedRuntimeEnrollmentBoundedDelay(10*time.Second, remaining); got != remaining {
		t.Fatalf("bounded resume delay = %s, want %s", got, remaining)
	}

	startedAt := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	now := startedAt.Add(55 * time.Second)
	job := enrollmentWindowTestJob("job-bounded-resume", "lease-bounded-resume")
	job.Result[managedRuntimeEnrollmentWaitStartedAtField] = startedAt.Format(time.RFC3339Nano)
	resolver := &enrollmentWindowTestResolver{
		job: job,
		now: &now,
		err: errors.New("enrollment pending"),
	}
	cfg := enrollmentWindowTestConfig(resolver, time.Minute, 10*time.Second)

	_, err := resolveManagedRuntimeTargetWithWaitClock(
		context.Background(), cfg, job, &core.KombinationSpec{}, nil, func() time.Time { return now },
	)
	var waitErr *JobWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error = %T %v, want JobWaitError", err, err)
	}
	if waitErr.ResumeAfter != remaining {
		t.Fatalf("resume after = %s, want remaining window %s", waitErr.ResumeAfter, remaining)
	}
	if resolver.deadlineBudget <= 0 || resolver.deadlineBudget > remaining {
		t.Fatalf("resolver attempt budget = %s, want a positive duration capped at %s", resolver.deadlineBudget, remaining)
	}
}

func TestManagedRuntimeEnrollmentWaitClearsMarkerWhenAttemptExhaustsDeadline(t *testing.T) {
	startedAt := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	now := startedAt.Add(55 * time.Second)
	job := enrollmentWindowTestJob("job-exhausted-during-attempt", "lease-exhausted-during-attempt")
	job.Result[managedRuntimeEnrollmentWaitStartedAtField] = startedAt.Format(time.RFC3339Nano)
	resolver := &enrollmentWindowTestResolver{
		job:     job,
		now:     &now,
		advance: 6 * time.Second,
		err:     errors.New("enrollment pending"),
	}
	cfg := enrollmentWindowTestConfig(resolver, time.Minute, 10*time.Second)

	target, err := resolveManagedRuntimeTargetWithWaitClock(
		context.Background(), cfg, job, &core.KombinationSpec{}, nil, func() time.Time { return now },
	)
	if target != nil {
		t.Fatalf("target = %#v, want nil after deadline", target)
	}
	if !errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
		t.Fatalf("error = %T %v, want ErrManagedRuntimeEnrollmentFailed", err, err)
	}
	var pending *ManagedRuntimeEnrollmentPendingError
	if errors.As(err, &pending) {
		t.Fatalf("deadline exhaustion returned a non-terminal wait: %#v", pending)
	}
	if got := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); got != "" {
		t.Fatalf("terminal deadline retained wait start %q", got)
	}
}

func TestManagedRuntimeEnrollmentWaitClearsMarkerOnTerminalReturns(t *testing.T) {
	startedAt := time.Date(2026, time.July, 19, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		ctx    context.Context
		target *ManagedRuntimeTarget
		err    error
	}{
		{
			name: "success",
			target: &ManagedRuntimeTarget{
				Host:          "203.0.113.70",
				SSHUser:       "ubuntu",
				SSHPrivateKey: "test-private-key",
			},
		},
		{name: "terminal enrollment failure", err: ErrManagedRuntimeEnrollmentFailed},
		{name: "non-waitable configuration failure", err: errors.New("required runtime configuration missing")},
		{name: "caller cancellation", ctx: canceledEnrollmentWindowContext(), err: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := startedAt.Add(time.Second)
			job := enrollmentWindowTestJob("job-terminal", "lease-terminal")
			job.Result[managedRuntimeEnrollmentWaitStartedAtField] = startedAt.Format(time.RFC3339Nano)
			resolver := &enrollmentWindowTestResolver{job: job, now: &now, target: tt.target, err: tt.err}
			cfg := enrollmentWindowTestConfig(resolver, time.Minute, 10*time.Second)
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}

			_, _ = resolveManagedRuntimeTargetWithWaitClock(
				ctx, cfg, job, &core.KombinationSpec{}, nil, func() time.Time { return now },
			)
			if got := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); got != "" {
				t.Fatalf("terminal return retained wait start %q", got)
			}
		})
	}
}

func enrollmentWindowTestJob(jobID, leaseID string) *Job {
	return &Job{
		ID:       jobID,
		Type:     JobTypeDeploy,
		TargetID: "stack-enrollment-window",
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "tenant-1",
		},
		Result: map[string]interface{}{
			leaseIDField: leaseID,
		},
	}
}

func enrollmentWindowTestConfig(resolver ManagedRuntimeTargetResolver, timeout, interval time.Duration) *ProvisionConfig {
	return &ProvisionConfig{
		ManagedRuntimeTargetWaitTimeout:  timeout,
		ManagedRuntimeTargetPollInterval: interval,
		RuntimeActions: RuntimeActions{
			RuntimeTargetResolver: resolver,
		},
	}
}

func canceledEnrollmentWindowContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
