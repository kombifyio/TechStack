package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestListOwnedJobsFromStoreFiltersByTenantAndOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-owned", "user-1")
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-other-owner", "user-2")
	mustCreateJobStoreStack(t, store, "tenant-2", "stack-other-tenant", "user-1")
	mustCreateJob(t, store, "tenant-1", "job-owned", "stack-owned", "provision", "pending")
	mustCreateJob(t, store, "tenant-1", "job-other-owner", "stack-other-owner", "provision", "pending")
	mustCreateJob(t, store, "tenant-2", "job-other-tenant", "stack-other-tenant", "provision", "pending")

	payload, err := listOwnedJobsFromStore(
		jobStoreEvent("user-1", "tenant-1"),
		JobRouteStores{Stacks: store, Jobs: store},
		"tenant-1",
		"user-1",
		jobListQuery{Page: 1, PerPage: 50, Type: "provision"},
	)
	if err != nil {
		t.Fatalf("listOwnedJobsFromStore: %v", err)
	}
	if payload.TotalItems != 1 || len(payload.Items) != 1 {
		t.Fatalf("payload = %#v, want one owned job", payload)
	}
	if payload.Items[0]["id"] != "job-owned" {
		t.Fatalf("job id = %v, want job-owned", payload.Items[0]["id"])
	}
}

func TestFindJobAndAuthorizeFromStoreRejectsOtherOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-other-owner", "user-2")
	mustCreateJob(t, store, "tenant-1", "job-1", "stack-other-owner", "provision", "pending")

	rec := httptest.NewRecorder()
	event := jobStoreEventWithRecorder("user-1", "tenant-1", rec)
	job, err := findJobAndAuthorizeFromStore(event, JobRouteStores{Stacks: store, Jobs: store}, "tenant-1", "job-1", "user-1")
	if err != nil {
		t.Fatalf("findJobAndAuthorizeFromStore returned response error: %v", err)
	}
	if job != nil {
		t.Fatalf("job = %#v, want nil for forbidden owner", job)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestArchivedStackKeepsOwnedDestroyJobReceiptReadable(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-archived", "user-1")
	mustCreateJob(t, store, "tenant-1", "job-destroy", "stack-archived", "destroy", "completed")
	if err := store.SoftDeleteStack(context.Background(), "tenant-1", "stack-archived"); err != nil {
		t.Fatalf("SoftDeleteStack: %v", err)
	}

	rec := httptest.NewRecorder()
	event := jobStoreEventWithRecorder("user-1", "tenant-1", rec)
	job, err := findJobAndAuthorizeFromStore(event, JobRouteStores{Stacks: store, Jobs: store}, "tenant-1", "job-destroy", "user-1")
	if err != nil || job == nil || job.ID != "job-destroy" {
		t.Fatalf("archived stack job receipt = %#v, err=%v", job, err)
	}

	payload, err := listOwnedJobsFromStore(event, JobRouteStores{Stacks: store, Jobs: store}, "tenant-1", "user-1", jobListQuery{Page: 1, PerPage: 50})
	if err != nil || payload.TotalItems != 1 || len(payload.Items) != 1 || payload.Items[0]["id"] != "job-destroy" {
		t.Fatalf("archived stack job list = %#v, err=%v", payload, err)
	}

	otherRecorder := httptest.NewRecorder()
	otherEvent := jobStoreEventWithRecorder("user-2", "tenant-1", otherRecorder)
	otherJob, otherErr := findJobAndAuthorizeFromStore(otherEvent, JobRouteStores{Stacks: store, Jobs: store}, "tenant-1", "job-destroy", "user-2")
	if otherErr != nil {
		t.Fatalf("other-owner authorization returned response error: %v", otherErr)
	}
	if otherJob != nil || otherRecorder.Code != http.StatusForbidden {
		t.Fatalf("other owner received archived receipt: job=%#v status=%d", otherJob, otherRecorder.Code)
	}
}

func TestStreamJobFromStoreSendsSSEForTerminalJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-owned", "user-1")
	mustCreateJob(t, store, "tenant-1", "job-1", "stack-owned", "provision", "completed")
	rec := httptest.NewRecorder()
	event := jobStoreEventWithRecorder("user-1", "tenant-1", rec)

	if err := streamJobFromStore(event, JobRouteStores{Stacks: store, Jobs: store}, "tenant-1", "job-1", "user-1"); err != nil {
		t.Fatalf("streamJobFromStore: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: progress") || !strings.Contains(body, "event: done") {
		t.Fatalf("SSE body missing progress/done events: %q", body)
	}
	if !strings.Contains(body, `"state":"completed"`) {
		t.Fatalf("SSE body missing completed state: %q", body)
	}
}

func TestJobProgressFromStoreIncludesStepMessageAndErrorDetails(t *testing.T) {
	progress := jobProgressFromStore(controlplane.Job{
		ID:           "job-1",
		State:        "running",
		Progress:     74,
		Step:         "docker_ready",
		Message:      "phase=apt_wait status=begin",
		Error:        "context deadline exceeded",
		ErrorDetails: "Runtime diagnostics collected",
	})
	if progress.Step != "docker_ready" || progress.CurrentStep != "docker_ready" {
		t.Fatalf("step fields = %q/%q, want docker_ready", progress.Step, progress.CurrentStep)
	}
	if progress.Message != "phase=apt_wait status=begin" || progress.ErrorDetails != "Runtime diagnostics collected" {
		t.Fatalf("progress payload missing message/details: %#v", progress)
	}
}

func TestJobDetailsFromStoreReconstructsWaitingProjection(t *testing.T) {
	nextResumeAt := time.Now().UTC().Add(15 * time.Second).Format(time.RFC3339Nano)
	job := controlplane.Job{
		ID:      "job-waiting",
		State:   "pending",
		Message: "Managed VM is still enrolling.",
		Result: map[string]any{
			"job_wait": map[string]interface{}{
				"state":          "waiting",
				"reason":         "waiting_enrollment",
				"next_resume_at": nextResumeAt,
			},
		},
	}
	details := jobDetailsFromStore(job)
	if details["state"] != "waiting" || details["wait_reason"] != "waiting_enrollment" || details["next_resume_at"] != nextResumeAt {
		t.Fatalf("waiting API details = %#v", details)
	}
	progress := jobProgressFromStore(job)
	if progress.State != "waiting" || progress.WaitReason != "waiting_enrollment" || progress.NextResumeAt != nextResumeAt {
		t.Fatalf("waiting SSE progress = %#v", progress)
	}
}

func TestJobDetailsFromStoreDoesNotExposeReusableCredentials(t *testing.T) {
	job := controlplane.Job{Message: "Bearer reusable-progress-secret", Result: map[string]any{
		"registration_token": "kpt1.reusable-enrollment-secret",
		"runtime":            map[string]any{"authorization": "Bearer reusable-runtime-secret", "status": "ready"},
	}, Logs: []map[string]any{{"message": "Bearer reusable-log-secret", "credential": "private"}}}
	details := jobDetailsFromStore(job)
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	progress, err := json.Marshal(jobProgressFromStore(job))
	if err != nil {
		t.Fatal(err)
	}
	text += string(progress)
	for _, forbidden := range []string{"reusable-enrollment-secret", "reusable-runtime-secret", "reusable-log-secret", "reusable-progress-secret", "private"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("owner job projection exposed credential material: %s", text)
		}
	}
	if !strings.Contains(text, `"status":"ready"`) {
		t.Fatalf("owner job projection dropped non-sensitive state: %s", text)
	}
}

func TestAPIJobWaitProjectionDecodesPersistedJSONRepresentations(t *testing.T) {
	const raw = `{"job_wait":{"state":"waiting","reason":"waiting_enrollment","next_resume_at":"2026-07-19T08:15:00Z"}}`
	tests := []struct {
		name   string
		result any
	}{
		{name: "raw message", result: json.RawMessage(raw)},
		{name: "bytes", result: []byte(raw)},
		{name: "string", result: raw},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, reason, nextResumeAt := apiJobWaitProjection("pending", tt.result)
			if state != "waiting" || reason != "waiting_enrollment" || nextResumeAt != "2026-07-19T08:15:00Z" {
				t.Fatalf("projection = %q/%q/%q", state, reason, nextResumeAt)
			}
		})
	}
}

func TestAPIJobWaitProjectionNormalizesControlPlaneCancellation(t *testing.T) {
	state, reason, nextResumeAt := apiJobWaitProjection("cancelled", nil)
	if state != "canceled" || reason != "" || nextResumeAt != "" {
		t.Fatalf("cancelled projection = %q/%q/%q", state, reason, nextResumeAt)
	}
}

func TestAPIJobWaitProjectionKeepsClaimedRecoveryVisibleUntilReplacementAdmission(t *testing.T) {
	nextResumeAt := "2026-07-19T08:15:00Z"
	result := map[string]any{
		"job_wait": map[string]any{
			"state": string(jobs.JobStateWaiting), "reason": jobs.WaitReasonManagedRuntimeProvider,
			"next_resume_at": nextResumeAt,
		},
		"enrollment_resume_kind":          jobs.WaitReasonManagedRuntimeProvider,
		"enrollment_resume_key":           "resume-provider",
		"enrollment_resume_source_job_id": "job-provider",
		"enrollment_resume_lease_id":      "lease-provider",
		"enrollment_resume_server_id":     "server-provider",
		"enrollment_resume_scheduled_at":  nextResumeAt,
	}
	state, reason, gotNextResumeAt := apiJobWaitProjection("cancelled", result)
	if state != "waiting" || reason != jobs.WaitReasonManagedRuntimeProvider || gotNextResumeAt != nextResumeAt {
		t.Fatalf("claimed recovery projection = %q/%q/%q", state, reason, gotNextResumeAt)
	}

	delete(result, "enrollment_resume_key")
	state, reason, gotNextResumeAt = apiJobWaitProjection("cancelled", result)
	if state != "canceled" || reason != "" || gotNextResumeAt != "" {
		t.Fatalf("incomplete recovery receipt projection = %q/%q/%q", state, reason, gotNextResumeAt)
	}
}

func TestAPIJobResumeAvailabilityUsesServerClockAndRejectsInvalidSchedule(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 17, 0, 0, time.UTC)
	availableAt, available := apiJobResumeAvailability("waiting_enrollment", "2026-07-19T08:15:00Z", now)
	if availableAt != "2026-07-19T08:17:00Z" || !available {
		t.Fatalf("availability = %q/%v", availableAt, available)
	}
	if at, ok := apiJobResumeAvailability("waiting_enrollment", "invalid", now); at != "" || ok {
		t.Fatalf("invalid schedule availability = %q/%v", at, ok)
	}
	providerAvailableAt, providerAvailable := apiJobResumeAvailability(
		jobs.WaitReasonManagedRuntimeProvider,
		"2026-07-19T08:15:00Z",
		now,
	)
	if providerAvailableAt != "2026-07-19T08:17:00Z" || !providerAvailable {
		t.Fatalf("provider wait availability = %q/%v", providerAvailableAt, providerAvailable)
	}
}

func TestControlPlaneSSEDetectsErrorDetailsOnlyUpdate(t *testing.T) {
	previous := jobProgressFromStore(controlplane.Job{
		ID:           "job-error-details",
		State:        "running",
		Progress:     50,
		Step:         "prepare_rollout",
		Message:      "Collecting provider diagnostics",
		Error:        "provider request failed",
		ErrorDetails: "",
	})
	current := jobProgressFromStore(controlplane.Job{
		ID:           "job-error-details",
		State:        "running",
		Progress:     50,
		Step:         "prepare_rollout",
		Message:      "Collecting provider diagnostics",
		Error:        "provider request failed",
		ErrorDetails: "IONOS request id req-123 returned 503",
	})

	if !jobProgressChanged(previous, current) {
		t.Fatal("control-plane SSE suppressed an error_details-only update")
	}
}

func TestJobDetailRouteReadsCanonicalStore(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-owned", "user-1")
	mustCreateJob(t, store, "tenant-1", "job-1", "stack-owned", "provision", "running")

	router := httpx.NewRouter()
	RegisterJobsSSERoutes(router, JobRouteStores{Stacks: store, Jobs: store})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "user-1",
		OrgID:  "tenant-1",
	}))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "job-1") || !strings.Contains(body, "running") {
		t.Fatalf("canonical job response missing job state: %s", body)
	}
}

func mustCreateJobStoreStack(t *testing.T, store *controlplane.MemoryStore, tenantID, stackID, ownerID string) {
	t.Helper()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             stackID,
		TenantID:       tenantID,
		OwnerSubjectID: ownerID,
		Name:           stackID,
	}); err != nil {
		t.Fatalf("CreateStack(%s): %v", stackID, err)
	}
}

func mustCreateJob(t *testing.T, store *controlplane.MemoryStore, tenantID, jobID, stackID, jobType, state string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID:       jobID,
		TenantID: tenantID,
		StackID:  stackID,
		Type:     jobType,
		State:    "pending",
	}); err != nil {
		t.Fatalf("CreateJob(%s): %v", jobID, err)
	}
	if state == "pending" {
		return
	}
	now := time.Now().UTC()
	if _, err := store.StartJob(ctx, tenantID, jobID, now); err != nil {
		t.Fatalf("StartJob(%s): %v", jobID, err)
	}
	switch state {
	case "running":
		return
	case "completed":
		if _, err := store.CompleteJob(ctx, tenantID, jobID, nil, now.Add(time.Second)); err != nil {
			t.Fatalf("CompleteJob(%s): %v", jobID, err)
		}
	case "failed":
		if _, err := store.FailJob(ctx, tenantID, jobID, "test failure", "", now.Add(time.Second)); err != nil {
			t.Fatalf("FailJob(%s): %v", jobID, err)
		}
	default:
		t.Fatalf("unsupported job state %q", state)
	}
}

func jobStoreEvent(userID, orgID string) *httpx.Event {
	return jobStoreEventWithRecorder(userID, orgID, httptest.NewRecorder())
}

func jobStoreEventWithRecorder(userID, orgID string, rec *httptest.ResponseRecorder) *httpx.Event {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: userID,
		OrgID:  orgID,
	}))
	return &httpx.Event{
		Request:  req,
		Response: rec,
	}
}
