package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func TestDeployStackReplaysOneKeyedJobThroughTheHTTPBoundary(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-keyed-deploy", "tenant-1", "auth0|user-1", "pending")
	seedApprovedWorker(t, store, "worker-1", "tenant-1", "auth0|user-1", "stack-keyed-deploy", "192.0.2.10")
	now := time.Now().UTC()
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "server-worker-1", TenantID: "tenant-1", StackID: "stack-keyed-deploy",
		OwnerSubjectID: "auth0|user-1", WorkerID: "worker-1", LifecycleState: "active",
		ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(&orchestrator.Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: routingTestLeaseLister{},
	}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{orch: orch, stackStore: store, jobStore: store}

	request := func(key string) (*httpx.Event, *httptest.ResponseRecorder) {
		event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
		event.Request.SetPathValue("id", "stack-keyed-deploy")
		event.Request.Header.Set("Idempotency-Key", key)
		return event, recorder
	}
	acceptedJob := func(key string) (string, int, string) {
		event, recorder := request(key)
		if err := h.deployStack(event); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data struct {
				JobID string `json:"job_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return envelope.Data.JobID, recorder.Code, recorder.Body.String()
	}

	firstID, firstStatus, firstBody := acceptedJob("deploy-attempt-1")
	replayID, replayStatus, replayBody := acceptedJob("deploy-attempt-1")
	jobs, _ := store.ListJobsByStack(ctx, "tenant-1", "stack-keyed-deploy", 10)
	if firstStatus != http.StatusAccepted || replayStatus != http.StatusAccepted || replayID != firstID || len(jobs) != 1 {
		t.Fatalf("first=%d/%q %s replay=%d/%q %s jobs=%d", firstStatus, firstID, firstBody, replayStatus, replayID, replayBody, len(jobs))
	}
	_, malformedStatus, _ := acceptedJob("short")
	jobs, _ = store.ListJobsByStack(ctx, "tenant-1", "stack-keyed-deploy", 10)
	if malformedStatus != http.StatusUnprocessableEntity || len(jobs) != 1 {
		t.Fatalf("malformed status=%d jobs=%d", malformedStatus, len(jobs))
	}
}

func TestDeployStackFailsClosedWithoutOrchestratorAndDoesNotQueueJob(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-no-orchestrator", TenantID: "auth0|user-1", OwnerSubjectID: "auth0|user-1",
		Name: "No orchestrator", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "")
	event.Request.SetPathValue("id", "stack-no-orchestrator")

	if err := (crudRouteHandlers{stackStore: store, jobStore: store}).deployStack(event); err != nil {
		t.Fatalf("deployStack: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	jobs, err := store.ListJobsByStack(ctx, "auth0|user-1", "stack-no-orchestrator", 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, deploy without an executor must not enqueue fake work", jobs)
	}
}

func TestDeployStackFailsClosedWithoutCanonicalStore(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (crudRouteHandlers{}).deployStack(event)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestProvisionStackQueuesCanonicalOwnerTenantJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "owner-1", OwnerSubjectID: "owner-1", Status: "pending", Config: map[string]any{},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	event, recorder := stackStoreRequestEvent("owner-1", "")
	event.Request.SetPathValue("id", "stack-1")
	if err := (crudRouteHandlers{stackStore: store, jobStore: store}).provisionStack(event); err != nil {
		t.Fatalf("provisionStack: %v", err)
	}
	jobs, _ := store.ListJobsByStack(t.Context(), "owner-1", "stack-1", 10)
	if recorder.Code != http.StatusAccepted || len(jobs) != 1 || jobs[0].Type != "provision" {
		t.Fatalf("status=%d jobs=%+v, want accepted canonical provision job", recorder.Code, jobs)
	}
}

func TestProvisionStackFailsClosedWithoutCanonicalStores(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (crudRouteHandlers{}).provisionStack(event)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
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
