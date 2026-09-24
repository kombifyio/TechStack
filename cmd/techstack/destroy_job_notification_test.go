package main

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestDestroyJobFailureNotifierEnqueuesFailedDestroy(t *testing.T) {
	outbox := &stackKitLifecycleNotificationOutboxFake{}
	notifier := destroyJobFailureNotifier{outbox: outbox}
	err := notifier.ObserveTerminalJob(context.Background(), jobs.JobSnapshot{
		ID:       "job-destroy-1",
		Type:     jobs.JobTypeDestroy,
		State:    jobs.JobStateFailed,
		TargetID: "stack-1",
		Error:    "managed runtime cleanup failed",
		Payload:  map[string]any{"owner_id": "auth0|owner", "tenant_id": "tenant-1"},
	})
	if err != nil {
		t.Fatalf("ObserveTerminalJob: %v", err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("events = %d, want in-app and push", len(outbox.events))
	}
	got := outbox.events[0]
	if got.EventKey != "runtime.destroy.failed" || got.Auth0UserID != "auth0|owner" {
		t.Fatalf("event = %#v", got)
	}
}

func TestDestroyJobFailureNotifierIgnoresSuccessfulDestroy(t *testing.T) {
	outbox := &stackKitLifecycleNotificationOutboxFake{}
	notifier := destroyJobFailureNotifier{outbox: outbox}
	err := notifier.ObserveTerminalJob(context.Background(), jobs.JobSnapshot{
		ID:      "job-destroy-ok",
		Type:    jobs.JobTypeDestroy,
		State:   jobs.JobStateCompleted,
		Payload: map[string]any{"owner_id": "auth0|owner", "tenant_id": "tenant-1"},
	})
	if err != nil {
		t.Fatalf("ObserveTerminalJob: %v", err)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("successful destroy notified: %#v", outbox.events)
	}
}
