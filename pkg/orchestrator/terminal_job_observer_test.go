package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

type terminalJobObserverFunc func(context.Context, jobs.JobSnapshot) error

func (f terminalJobObserverFunc) ObserveTerminalJob(ctx context.Context, snapshot jobs.JobSnapshot) error {
	return f(ctx, snapshot)
}

// Stable lifecycle invariant: product observers run after the same durable
// terminal transition exposed by the control-plane job API, never from a
// handler's process-local return value.
func TestTerminalJobObserverRunsAfterDurableCompletion(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "deployment-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner-1", Name: "Media", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	orch := New(&Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	type observation struct {
		snapshot jobs.JobSnapshot
		err      error
	}
	observed := make(chan observation, 1)
	orch.AddTerminalJobObserver(terminalJobObserverFunc(func(ctx context.Context, snapshot jobs.JobSnapshot) error {
		durable, err := store.GetJob(ctx, "tenant-1", snapshot.ID)
		if err != nil {
			observed <- observation{err: err}
			return nil
		}
		if durable.State != string(jobs.JobStateCompleted) || durable.CompletedAt == nil {
			observed <- observation{err: fmt.Errorf("observer saw nonterminal durable job: %#v", durable)}
			return nil
		}
		observed <- observation{snapshot: snapshot}
		return nil
	}))
	orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error { return nil })
	if err := orch.enqueueWithSync(&jobs.Job{
		ID: "job-install", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "deployment-1",
		Payload: map[string]any{"tenant_id": "tenant-1", "owner_id": "auth0|owner-1"}, MaxAttempts: 1,
	}, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Start()
	defer orch.Stop()

	select {
	case result := <-observed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.snapshot.State != jobs.JobStateCompleted || result.snapshot.CompletedAt == nil {
			t.Fatalf("terminal snapshot = %#v", result.snapshot)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal observer was not called")
	}
}
