package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/outcome"
)

type rateLimitedApplySender struct{ recordingStackKitCommandSender }

func (sender *rateLimitedApplySender) SendStackKitCommand(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		return sender.recordingStackKitCommandSender.SendStackKitCommand(ctx, agentID, command)
	}
	// The exact machine envelope StackKits emits for a rate-limited public
	// certificate (kombify-StackKits-m89y).
	return &agentpb.StackKitResult{
		Success: false, ExitCode: 1, Release: command.Release,
		Stderr: `declared HTTPS route "cloud-hub-public" has no issued certificate: urn:ietf:params:acme:error:rateLimited`,
		CommandResultJson: []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit apply","status":"failed","data":{` +
			`"schemaVersion":"stackkit.actionable-error/v1","code":"stackkit_command_failed","reasonCode":"acme_rate_limited",` +
			`"message":"certificate authority rate limit","userGuidance":["Retry after the reported retry-after time."],` +
			`"retryable":true,"retryAfter":"2026-09-08T23:59:50Z"}}`),
	}, nil
}

// Managed Cloud Kit attempts 2026-09-07 failed with an opaque HTTP 526 and a
// generic, non-retryable provision outcome although the certificate authority
// had named when issuance would be possible again. The job outcome must carry
// that reason and time so neither the owner nor a retry guesses.
func TestManagedRolloutCarriesCertificateAuthorityRetryAfterIntoTheJobOutcome(t *testing.T) {
	_, applyErr := runTypedStackKitApplySequence(context.Background(), &rateLimitedApplySender{}, "job-1", typedStackKitApplyRequest(), stackkitrelease.Release{})
	if applyErr == nil {
		t.Fatal("rate-limited Apply reported success")
	}

	q := NewQueue(1, nil)
	q.RegisterHandler(JobTypeProvision, func(context.Context, *Job, *Queue) error {
		return wrapProvisionCause(StepRolloutRunner, fmt.Errorf("StackKits rollout failed: %w", applyErr), "StackKits could not apply the selected StackKit rollout.")
	})
	job := &Job{ID: "job-1", Type: JobTypeProvision, State: JobStatePending, MaxAttempts: 3}
	registerAndProcessJob(q, context.Background(), job)

	want := time.Date(2026, 9, 8, 23, 59, 50, 0, time.UTC)
	decision, ok := job.Result["last_outcome"].(outcome.Decision)
	if job.State != JobStateFailed || !ok || decision.ReasonCode != "stackkit_acme_rate_limited" || !decision.Retryable ||
		decision.RetryAfter == nil || !decision.RetryAfter.Equal(want) {
		t.Fatalf("state=%s outcome=%#v", job.State, job.Result["last_outcome"])
	}
	if retryAfter, found := JobResultRetryAfter(job.Result); !found || !retryAfter.Equal(want) {
		t.Fatalf("persisted job result retry_after = %v found=%v", retryAfter, found)
	}
}
