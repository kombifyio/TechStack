package jobs

import (
	"context"
	"errors"
	"testing"
)

type fakeRemoteEnrollmentExecutor struct {
	calls  int
	result RemoteEnrollmentOutcome
	err    error
}

func (f *fakeRemoteEnrollmentExecutor) ExecuteRemoteEnrollment(
	_ context.Context,
	_ RemoteEnrollmentRequest,
	progress func(step, message string, percent int),
) (RemoteEnrollmentOutcome, error) {
	f.calls++
	if progress != nil {
		progress("remote_ssh_connect", "Connecting to your Node over SSH…", 10)
	}
	return f.result, f.err
}

func remoteEnrollmentTestJob() *Job {
	return &Job{
		ID: "job-enroll", Type: JobTypeRemoteEnrollment, TargetType: "stack", TargetID: "stack-1",
		Payload: map[string]interface{}{"tenant_id": "tenant-1", "owner_id": "owner-1"},
		Result:  map[string]interface{}{},
	}
}

func TestRemoteEnrollmentHandlerFailsClosedWithoutExecutor(t *testing.T) {
	err := RemoteEnrollmentHandler(&ProvisionConfig{})(context.Background(), remoteEnrollmentTestJob(), nil)
	if !errors.Is(err, ErrRemoteEnrollmentUnavailable) {
		t.Fatalf("handler error = %v, want ErrRemoteEnrollmentUnavailable", err)
	}
}

func TestRemoteEnrollmentHandlerProjectsSuccess(t *testing.T) {
	executor := &fakeRemoteEnrollmentExecutor{result: RemoteEnrollmentOutcome{ServerID: "server-1", Message: "Guard enrolled over SSH"}}
	job := remoteEnrollmentTestJob()
	if err := RemoteEnrollmentHandler(&ProvisionConfig{RemoteEnrollment: executor})(context.Background(), job, nil); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if job.Step != "remote_ssh_enrolled" || job.Progress != 100 {
		t.Fatalf("job step/progress = %q/%d", job.Step, job.Progress)
	}
	if job.Result["server_id"] != "server-1" {
		t.Fatalf("job result = %#v", job.Result)
	}
	if job.Result["retryable"] != false {
		t.Fatalf("completed enrollment must clear retryable: %#v", job.Result)
	}
}

func TestRemoteEnrollmentHandlerWaitsThenFailsClosed(t *testing.T) {
	retryable := &RemoteEnrollmentRetryableError{
		Reason:  "remote_enrollment_ssh_not_ready",
		Message: "Could not reach the server over SSH. Check host, port, firewall, and that SSH is running.",
		Cause:   errors.New("ssh dial timeout"),
	}
	executor := &fakeRemoteEnrollmentExecutor{err: retryable}
	handler := RemoteEnrollmentHandler(&ProvisionConfig{RemoteEnrollment: executor})
	job := remoteEnrollmentTestJob()

	for attempt := 1; attempt <= remoteEnrollmentMaxWaitAttempts; attempt++ {
		err := handler(context.Background(), job, nil)
		var wait *JobWaitError
		if !errors.As(err, &wait) {
			t.Fatalf("attempt %d error = %v, want JobWaitError", attempt, err)
		}
		if got := job.Result[remoteEnrollmentWaitAttemptsField]; got != attempt {
			t.Fatalf("attempt %d wait counter = %#v, want %d", attempt, got, attempt)
		}
	}

	err := handler(context.Background(), job, nil)
	var wait *JobWaitError
	if errors.As(err, &wait) {
		t.Fatal("enrollment retried past the bounded wait budget")
	}
	if !errors.Is(err, retryable.Cause) {
		t.Fatalf("exhausted error = %v, want the underlying cause", err)
	}
	if job.Result["retryable"] != true {
		t.Fatalf("exhausted enrollment must stay retryable for the user: %#v", job.Result)
	}
}

func TestRegisterDefaultHandlersRegistersRemoteEnrollment(t *testing.T) {
	queue := NewQueue(1, nil)
	RegisterDefaultHandlers(queue, &ProvisionConfig{})
	if !queue.HasHandler(JobTypeRemoteEnrollment) {
		t.Fatal("RegisterDefaultHandlers must register the remote_enrollment handler")
	}
}

func TestRemoteEnrollmentHandlerRecordsTerminalFailureReason(t *testing.T) {
	executor := &fakeRemoteEnrollmentExecutor{err: &RemoteEnrollmentFailure{
		Reason:  "remote_enrollment_ssh_auth",
		Message: "SSH authentication failed. Check the username, password, or SSH key.",
		Cause:   errors.New("permission denied"),
	}}
	job := remoteEnrollmentTestJob()
	err := RemoteEnrollmentHandler(&ProvisionConfig{RemoteEnrollment: executor})(context.Background(), job, nil)
	if err == nil {
		t.Fatal("terminal enrollment failure must return an error")
	}
	if job.Result["reason_code"] != "remote_enrollment_ssh_auth" {
		t.Fatalf("reason_code = %#v", job.Result["reason_code"])
	}
	if job.Result["retryable"] != false {
		t.Fatalf("terminal failure must record retryable=false: %#v", job.Result)
	}
}
