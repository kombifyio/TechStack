package main

import (
	"context"
	"testing"
	"time"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/jobs"
)

type stackKitLifecycleNotificationOutboxFake struct {
	events []productnotifications.ProductEvent
}

func (f *stackKitLifecycleNotificationOutboxFake) Enqueue(_ context.Context, event productnotifications.ProductEvent) error {
	f.events = append(f.events, event)
	return nil
}

// Stable cross-product invariant: only a durably terminal StackKit install or
// update becomes one owner-scoped activity event per supported native channel.
func TestStackKitLifecycleNotifierProjectsTerminalOwnerEvent(t *testing.T) {
	completedAt := time.Date(2026, 8, 31, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name         string
		job          jobs.JobSnapshot
		wantEventKey string
		wantState    string
	}{
		{
			name: "install completed",
			job: jobs.JobSnapshot{
				ID: "job-install", Type: jobs.JobTypeDeploy, State: jobs.JobStateCompleted,
				TargetID: "deployment-1", TargetName: "Media",
				Payload: map[string]any{
					"tenant_id": "tenant-1", "owner_id": "auth0|owner-1",
					"stackkit_id": "media-kit", "node_id": "node-1",
				},
				CompletedAt: &completedAt,
			},
			wantEventKey: "stackkit.install.completed",
			wantState:    "completed",
		},
		{
			name: "update failed",
			job: jobs.JobSnapshot{
				ID: "job-update", Type: jobs.JobTypeStackKitLifecycle, State: jobs.JobStateFailed,
				TargetID: "deployment-1", TargetName: "Media",
				Payload: map[string]any{
					"tenant_id": "tenant-1", "owner_id": "auth0|owner-1",
					"stackkit_id": "media-kit", "node_id": "node-1", "operation": jobs.StackKitLifecycleUpgrade,
				},
				CompletedAt: &completedAt,
			},
			wantEventKey: "stackkit.update.failed",
			wantState:    "failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outbox := &stackKitLifecycleNotificationOutboxFake{}
			notifier := stackKitLifecycleNotifier{outbox: outbox}
			if err := notifier.ObserveTerminalJob(context.Background(), tc.job); err != nil {
				t.Fatalf("ObserveTerminalJob: %v", err)
			}
			if len(outbox.events) != 2 {
				t.Fatalf("native channel events = %d, want in_app and push", len(outbox.events))
			}
			seenChannels := map[string]bool{}
			for _, event := range outbox.events {
				seenChannels[event.Channel] = true
				if event.Auth0UserID != "auth0|owner-1" || event.OrganizationID != "tenant-1" {
					t.Fatalf("owner scope changed: %#v", event)
				}
				if event.SourceApp != "techstack" || event.EventKey != tc.wantEventKey {
					t.Fatalf("activity identity = %q/%q", event.SourceApp, event.EventKey)
				}
				if event.SubjectRef != "stackkit:media-kit" || event.DeepLink != "/stacks/deployment-1" {
					t.Fatalf("subject/deep-link = %q/%q", event.SubjectRef, event.DeepLink)
				}
				if event.Payload["stackkit_id"] != "media-kit" ||
					event.Payload["kit_deployment_id"] != "deployment-1" ||
					event.Payload["techstack_id"] != "deployment-1" ||
					event.Payload["node_id"] != "node-1" || event.Payload["state"] != tc.wantState {
					t.Fatalf("lifecycle context = %#v", event.Payload)
				}
			}
			if !seenChannels["in_app"] || !seenChannels["push"] || outbox.events[0].IdempotencyKey == outbox.events[1].IdempotencyKey {
				t.Fatalf("channel/idempotency projection = %#v", outbox.events)
			}
		})
	}

	outbox := &stackKitLifecycleNotificationOutboxFake{}
	notifier := stackKitLifecycleNotifier{outbox: outbox}
	for _, state := range []jobs.JobState{jobs.JobStatePending, jobs.JobStateRunning, jobs.JobStateWaiting} {
		if err := notifier.ObserveTerminalJob(context.Background(), jobs.JobSnapshot{
			ID: "job-not-terminal", Type: jobs.JobTypeDeploy, State: state,
			TargetID: "deployment-1", Payload: map[string]any{
				"tenant_id": "tenant-1", "owner_id": "auth0|owner-1", "stackkit_id": "media-kit", "node_id": "node-1",
			},
		}); err != nil {
			t.Fatalf("ObserveTerminalJob(%s): %v", state, err)
		}
	}
	if len(outbox.events) != 0 {
		t.Fatalf("non-terminal rollout emitted events: %#v", outbox.events)
	}
}
