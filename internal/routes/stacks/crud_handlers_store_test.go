package stacks

import (
	"context"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestDeployStackFailsClosedWithoutOrchestratorAndDoesNotQueueJob(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-no-orchestrator", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1",
		Name: "No orchestrator", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	event.Request.SetPathValue("id", "stack-no-orchestrator")

	if err := (crudRouteHandlers{stackStore: store, jobStore: store}).deployStack(event); err != nil {
		t.Fatalf("deployStack: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	jobs, err := store.ListJobsByStack(ctx, "tenant-1", "stack-no-orchestrator", 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, deploy without an executor must not enqueue fake work", jobs)
	}
}

func TestMarkStackProvisionStartFailedUpdatesControlPlaneStack(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-start-failed",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Start Failed",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	h := crudRouteHandlers{stackStore: store}
	h.markStackProvisionStartFailed(ctx, "stack-start-failed", "tenant-1")

	stack, err := store.GetStack(ctx, "tenant-1", "stack-start-failed")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "failed" {
		t.Fatalf("stack status = %q, want failed", stack.Status)
	}
}

func TestLatestJobAllowsFreshRolloutOnlyForCurrentFailedDeployOrProvision(t *testing.T) {
	tests := []struct {
		name string
		jobs []controlplane.Job
		want bool
	}{
		{name: "failed deploy", jobs: []controlplane.Job{{Type: "deploy", State: "failed"}}, want: true},
		{name: "failed provision", jobs: []controlplane.Job{{Type: "provision", State: "failed"}}, want: true},
		{name: "completed retry supersedes failure", jobs: []controlplane.Job{{Type: "deploy", State: "completed"}, {Type: "deploy", State: "failed"}}},
		{name: "failed destroy is not rollout authority", jobs: []controlplane.Job{{Type: "destroy", State: "failed"}}},
		{name: "no durable job"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := latestJobAllowsFreshRollout(test.jobs); got != test.want {
				t.Fatalf("latestJobAllowsFreshRollout() = %v, want %v", got, test.want)
			}
		})
	}
}
