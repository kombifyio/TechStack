package stacks

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func TestRetryStackRolloutRouteUsesExactFailedJobAndNeverRequestsVM(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-waiting", "tenant-1", "auth0|user-1", "error")
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-failed", TenantID: "tenant-1", StackID: "stack-waiting", Type: "deploy", State: "failed",
		Error: "StackKit artifact generation failed", Result: map[string]any{"lease_id": "lease-existing"},
	}); err != nil {
		t.Fatal(err)
	}
	app := newOwnerSpecTestApp(t)
	orch := orchestrator.NewWithApp(app, &orchestrator.Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: routingTestLeaseLister{leases: []vmlease.Lease{
			routingTestManagedLease("lease-existing", "tenant-1", "auth0|user-1", "stack-waiting"),
		}},
	}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{app: app, orch: orch, stackStore: store, jobStore: store}

	blocked, blockedRec := enrollmentResumeEvent("auth0|user-1", `{"source_job_id":"job-failed","lease_id":"lease-existing"}`)
	blocked.Request.URL.Path = "/api/v1/stacks/stack-waiting/retry-rollout"
	if err := h.retryStackRollout(blocked); err != nil {
		t.Fatal(err)
	}
	if blockedRec.Code != http.StatusConflict || !strings.Contains(blockedRec.Body.String(), "rollout_retry_guard_not_connected") {
		t.Fatalf("missing Guard status=%d body=%s", blockedRec.Code, blockedRec.Body.String())
	}
	if stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-waiting", 10); err != nil || len(stored) != 1 {
		t.Fatalf("missing Guard mutated jobs = %#v err=%v", stored, err)
	}
	seedRecoveryGuardRuntime(t, store, "tenant-1", "auth0|user-1", "stack-waiting", "lease-existing", time.Now().UTC())

	first, firstRec := enrollmentResumeEvent("auth0|user-1", `{"source_job_id":"job-failed","lease_id":"lease-existing"}`)
	first.Request.URL.Path = "/api/v1/stacks/stack-waiting/retry-rollout"
	if err := h.retryStackRollout(first); err != nil {
		t.Fatal(err)
	}
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}
	data := enrollmentResumeResponseData(t, firstRec)
	jobID, _ := data["job_id"].(string)
	if jobID == "" || jobID == "job-failed" || data["provider_vm_create_requested"] != false || data["lease_id"] != "lease-existing" {
		t.Fatalf("response = %#v", data)
	}
	second, secondRec := enrollmentResumeEvent("auth0|user-1", `{"source_job_id":"job-failed","lease_id":"lease-existing"}`)
	second.Request.URL.Path = "/api/v1/stacks/stack-waiting/retry-rollout"
	if err := h.retryStackRollout(second); err != nil {
		t.Fatal(err)
	}
	secondData := enrollmentResumeResponseData(t, secondRec)
	if secondData["job_id"] != jobID || secondData["idempotent_replay"] != true {
		t.Fatalf("replay = %#v", secondData)
	}
}

func TestRetryStackRolloutRejectsUnknownJSONFields(t *testing.T) {
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-waiting", "tenant-1", "auth0|user-1", "error")
	e, rec := enrollmentResumeEvent("auth0|user-1", `{"source_job_id":"job-failed","lease_id":"lease-existing","provider":"ionos"}`)
	if err := (crudRouteHandlers{stackStore: store, jobStore: store, orch: &orchestrator.Orchestrator{}}).retryStackRollout(e); err != nil {
		t.Fatalf("router error = %v", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_rollout_retry_body") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRolloutRetryRuntimeEvidenceUnavailableIs503(t *testing.T) {
	e, rec := enrollmentResumeEvent("auth0|owner", `{}`)
	if err := writeRolloutRetryError(e, orchestrator.ErrDeployRuntimeEvidenceUnavailable); err != nil {
		t.Fatalf("router error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "rollout_retry_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
