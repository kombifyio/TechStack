package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestResumeStackEnrollmentRouteUsesRealExactRecoveryService(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-waiting", "tenant-1", "auth0|user-1", "provisioning")
	nextResumeAt := time.Now().UTC().Add(-3 * time.Minute)
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-waiting", TenantID: "tenant-1", StackID: "stack-waiting", Type: "deploy", State: "pending",
		Progress: 82, Step: "resolve_managed_runtime", Message: "Managed VM enrollment is pending",
		Result: map[string]any{
			"lease_id": "lease-waiting",
			"job_wait": map[string]any{
				"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeEnrollment,
				"next_resume_at": nextResumeAt.Format(time.RFC3339Nano),
			},
		},
		ScheduledFor: nextResumeAt,
	}); err != nil {
		t.Fatal(err)
	}
	app := newOwnerSpecTestApp(t)
	orch := orchestrator.NewWithApp(app, &orchestrator.Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: routingTestLeaseLister{leases: []vmlease.Lease{
			routingTestManagedLease("lease-waiting", "tenant-1", "auth0|user-1", "stack-waiting"),
		}},
	}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{app: app, orch: orch, stackStore: store, jobStore: store}

	blocked, blockedRec := enrollmentResumeEvent("auth0|user-1", `{"job_id":"job-waiting","lease_id":"lease-waiting"}`)
	if err := h.resumeStackEnrollment(blocked); err != nil {
		t.Fatal(err)
	}
	if blockedRec.Code != http.StatusConflict || !strings.Contains(blockedRec.Body.String(), "enrollment_resume_guard_not_connected") {
		t.Fatalf("missing Guard status=%d body=%s", blockedRec.Code, blockedRec.Body.String())
	}
	if stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-waiting", 10); err != nil || len(stored) != 1 {
		t.Fatalf("missing Guard mutated jobs = %#v err=%v", stored, err)
	}
	seedRecoveryGuardRuntime(t, store, "tenant-1", "auth0|user-1", "stack-waiting", "lease-waiting", time.Now().UTC())

	first, firstRec := enrollmentResumeEvent("auth0|user-1", `{"job_id":"job-waiting","lease_id":"lease-waiting"}`)
	if err := h.resumeStackEnrollment(first); err != nil {
		t.Fatal(err)
	}
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}
	firstData := enrollmentResumeResponseData(t, firstRec)
	jobID, _ := firstData["job_id"].(string)
	if jobID == "" || jobID == "job-waiting" || firstData["lease_id"] != "lease-waiting" || firstData["provider_vm_create_requested"] != false {
		t.Fatalf("first response = %#v", firstData)
	}
	stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-waiting", 10)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored jobs = %#v err=%v", stored, err)
	}

	second, secondRec := enrollmentResumeEvent("auth0|user-1", `{"job_id":"job-waiting","lease_id":"lease-waiting"}`)
	if err := h.resumeStackEnrollment(second); err != nil {
		t.Fatal(err)
	}
	secondData := enrollmentResumeResponseData(t, secondRec)
	if secondData["job_id"] != jobID || secondData["idempotent_replay"] != true {
		t.Fatalf("replay response = %#v", secondData)
	}
	stored, _ = store.ListJobsByStack(ctx, "tenant-1", "stack-waiting", 10)
	if len(stored) != 2 {
		t.Fatalf("replay created another job: %#v", stored)
	}
}

func seedRecoveryGuardRuntime(
	t *testing.T,
	store *controlplane.MemoryStore,
	tenantID, ownerID, stackID, leaseID string,
	heartbeatAt time.Time,
) {
	t.Helper()
	_, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: runtimeidentity.LeaseServerID(leaseID), TenantID: tenantID, StackID: stackID,
		OwnerSubjectID: ownerID, WorkerID: "guard-" + leaseID, LeaseID: leaseID,
		LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
		LastHeartbeatAt: &heartbeatAt,
	})
	if err != nil {
		t.Fatalf("seed recovery Guard runtime: %v", err)
	}
}

func TestResumeStackEnrollmentRouteRejectsAnotherOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-waiting", "tenant-1", "auth0|owner", "provisioning")
	e, _ := enrollmentResumeEvent("auth0|other", `{"job_id":"job-1","lease_id":"lease-1"}`)
	err := (crudRouteHandlers{stackStore: store, jobStore: store}).resumeStackEnrollment(e)
	apiErr, ok := err.(*httpx.APIError)
	if !ok || apiErr.Status != http.StatusForbidden {
		t.Fatalf("error=%#v, want forbidden API error", err)
	}
}

func TestResumeStackEnrollmentRejectsUnknownJSONFields(t *testing.T) {
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-waiting", "tenant-1", "auth0|owner", "provisioning")
	e, rec := enrollmentResumeEvent("auth0|owner", `{"job_id":"job-1","lease_id":"lease-1","provider":"ionos"}`)
	if err := (crudRouteHandlers{stackStore: store, jobStore: store, orch: &orchestrator.Orchestrator{}}).resumeStackEnrollment(e); err != nil {
		t.Fatalf("router error = %v", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_enrollment_resume_body") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEnrollmentResumeUnknownErrorIsGeneric500(t *testing.T) {
	e, rec := enrollmentResumeEvent("auth0|owner", `{}`)
	if err := writeEnrollmentResumeError(e, errors.New(`database failed at C:\\secret\\tenant.db`)); err != nil {
		t.Fatalf("router error = %v", err)
	}
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "tenant.db") ||
		!strings.Contains(rec.Body.String(), "enrollment_resume_internal") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEnrollmentResumeRuntimeEvidenceUnavailableIs503(t *testing.T) {
	e, rec := enrollmentResumeEvent("auth0|owner", `{}`)
	if err := writeEnrollmentResumeError(e, orchestrator.ErrDeployRuntimeEvidenceUnavailable); err != nil {
		t.Fatalf("router error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "enrollment_resume_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func enrollmentResumeEvent(userID, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/stack-waiting/resume-enrollment", strings.NewReader(body))
	req.SetPathValue("id", "stack-waiting")
	req.Header.Set("content-type", "application/json")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: userID, OrgID: "tenant-1"}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func enrollmentResumeResponseData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope ksapi.SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("response data = %T", envelope.Data)
	}
	return data
}
