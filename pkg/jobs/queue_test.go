// Package jobs provides unit tests for queue operations.
package jobs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// TestNewQueue verifies queue creation.
func TestNewQueue(t *testing.T) {
	q := NewQueue(4, nil)
	if q == nil {
		t.Fatal("expected non-nil queue")
	}
	if q.workers != 4 {
		t.Errorf("expected 4 workers, got %d", q.workers)
	}
	if q.maxCompletedJobs != 1000 {
		t.Errorf("expected maxCompletedJobs 1000, got %d", q.maxCompletedJobs)
	}
	if q.completedJobTTL != 24*time.Hour {
		t.Errorf("expected completedJobTTL 24h, got %v", q.completedJobTTL)
	}
}

// TestQueue_List tests the List method with various filters.
func TestQueue_List(t *testing.T) {
	q := NewQueue(1, nil)

	// Add jobs with different states
	q.jobs["job1"] = &Job{ID: "job1", State: JobStatePending}
	q.jobs["job2"] = &Job{ID: "job2", State: JobStateRunning}
	q.jobs["job3"] = &Job{ID: "job3", State: JobStateCompleted}
	q.jobs["job4"] = &Job{ID: "job4", State: JobStateFailed}
	q.jobs["job5"] = &Job{ID: "job5", State: JobStateCancelled}
	q.jobs["job6"] = &Job{ID: "job6", State: JobStatePending}

	tests := []struct {
		name      string
		filter    JobState
		wantCount int
	}{
		{"all jobs", "", 6},
		{"pending jobs", JobStatePending, 2},
		{"running jobs", JobStateRunning, 1},
		{"completed jobs", JobStateCompleted, 1},
		{"failed jobs", JobStateFailed, 1},
		{"canceled jobs", JobStateCancelled, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := q.List(tt.filter)
			if len(jobs) != tt.wantCount {
				t.Errorf("List(%q) returned %d jobs, want %d", tt.filter, len(jobs), tt.wantCount)
			}
		})
	}
}

// TestQueue_Cancel tests job cancellation.
func TestQueue_Cancel(t *testing.T) {
	q := NewQueue(1, nil)

	t.Run("cancel pending job", func(t *testing.T) {
		job := &Job{ID: "pending-job", State: JobStatePending}
		q.jobsMu.Lock()
		q.jobs[job.ID] = job
		q.jobsMu.Unlock()

		err := q.Cancel(job.ID)
		if err != nil {
			t.Fatalf("failed to cancel pending job: %v", err)
		}

		if job.State != JobStateCancelled {
			t.Errorf("expected state %s, got %s", JobStateCancelled, job.State)
		}
		if job.CompletedAt == nil {
			t.Error("expected CompletedAt to be set")
		}
	})

	t.Run("cancel running job with cancel func", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		job := &Job{
			ID:         "running-job",
			State:      JobStateRunning,
			cancelFunc: cancel,
		}
		q.jobsMu.Lock()
		q.jobs[job.ID] = job
		q.jobsMu.Unlock()

		err := q.Cancel(job.ID)
		if err != nil {
			t.Fatalf("failed to cancel running job: %v", err)
		}

		// Check context was canceled
		select {
		case <-ctx.Done():
			// Expected
		default:
			t.Error("expected context to be canceled")
		}
		job.mu.RLock()
		state, completedAt := job.State, job.CompletedAt
		cancellationRequested := job.cancellationRequested
		job.mu.RUnlock()
		if state != JobStateRunning || completedAt != nil || !cancellationRequested {
			t.Fatalf(
				"running cancel state=%q completed_at=%v requested=%v, want an active claim until the handler unwinds",
				state,
				completedAt,
				cancellationRequested,
			)
		}
	})

	t.Run("cancel non-existent job", func(t *testing.T) {
		err := q.Cancel("non-existent-job")
		if err == nil {
			t.Error("expected error for non-existent job")
		}
		if !contains(err.Error(), "job not found") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("cancel completed job", func(t *testing.T) {
		job := &Job{ID: "completed-job", State: JobStateCompleted}
		q.jobsMu.Lock()
		q.jobs[job.ID] = job
		q.jobsMu.Unlock()

		err := q.Cancel(job.ID)
		if err == nil {
			t.Error("expected error when canceling completed job")
		}
		if !contains(err.Error(), "cannot cancel job in state") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("cancel failed job", func(t *testing.T) {
		job := &Job{ID: "failed-job", State: JobStateFailed}
		q.jobsMu.Lock()
		q.jobs[job.ID] = job
		q.jobsMu.Unlock()

		err := q.Cancel(job.ID)
		if err == nil {
			t.Error("expected error when canceling failed job")
		}
	})
}

// TestQueue_cancelJobInternal tests internal job cancellation.
func TestQueue_cancelJobInternal(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{ID: "test-job", State: JobStateRunning}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.cancelJobInternal(job, "test cancellation reason")

	if job.State != JobStateCancelled {
		t.Errorf("expected state %s, got %s", JobStateCancelled, job.State)
	}
	if job.Error != "test cancellation reason" {
		t.Errorf("expected error message 'test cancellation reason', got '%s'", job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if len(job.Logs) == 0 {
		t.Error("expected log entry to be added")
	}
}

// TestQueue_failJob tests job failure handling.
func TestQueue_failJob(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{ID: "test-job", State: JobStateRunning}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.failJob(job, "test error message")

	if job.State != JobStateFailed {
		t.Errorf("expected state %s, got %s", JobStateFailed, job.State)
	}
	if job.Error != "test error message" {
		t.Errorf("expected error 'test error message', got '%s'", job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if len(job.Logs) == 0 {
		t.Error("expected log entry to be added")
	}
}

// TestQueue_failJobWithDetails tests detailed job failure handling.
func TestQueue_failJobWithDetails(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{ID: "test-job", State: JobStateRunning}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.failJobWithDetails(job, "error message", "detailed error information")

	if job.State != JobStateFailed {
		t.Errorf("expected state %s, got %s", JobStateFailed, job.State)
	}
	if job.Error != "error message" {
		t.Errorf("expected error 'error message', got '%s'", job.Error)
	}
	if job.ErrorDetails != "detailed error information" {
		t.Errorf("expected ErrorDetails 'detailed error information', got '%s'", job.ErrorDetails)
	}
	if job.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

// TestQueue_cleanupOldJobs tests cleanup of old completed jobs.
func TestQueue_cleanupOldJobs(t *testing.T) {
	q := NewQueue(1, nil)
	q.maxCompletedJobs = 3
	q.completedJobTTL = 1 * time.Millisecond // Very short TTL for testing

	// Add old completed jobs
	oldTime := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 5; i++ {
		job := &Job{
			ID:          genTestJobID(i),
			State:       JobStateCompleted,
			CreatedAt:   oldTime,
			CompletedAt: &oldTime,
		}
		q.jobs[job.ID] = job
	}

	// Add a pending job (should not be cleaned up)
	pendingJob := &Job{ID: "pending-job", State: JobStatePending, CreatedAt: time.Now()}
	q.jobs[pendingJob.ID] = pendingJob

	// Wait for TTL to pass
	time.Sleep(5 * time.Millisecond)

	// Run cleanup
	q.cleanupOldJobs()

	// Verify pending job still exists
	if _, ok := q.jobs[pendingJob.ID]; !ok {
		t.Error("pending job should not be cleaned up")
	}

	// Count remaining completed jobs
	completedCount := 0
	for _, job := range q.jobs {
		if job.State == JobStateCompleted {
			completedCount++
		}
	}

	// Should have removed old jobs (TTL passed)
	if completedCount > q.maxCompletedJobs {
		t.Errorf("expected at most %d completed jobs, got %d", q.maxCompletedJobs, completedCount)
	}
}

// TestQueue_cleanupOldJobs_MaxLimit tests that cleanup respects max completed jobs limit.
func TestQueue_cleanupOldJobs_MaxLimit(t *testing.T) {
	q := NewQueue(1, nil)
	q.maxCompletedJobs = 2
	q.completedJobTTL = 24 * time.Hour // Long TTL

	// Add more completed jobs than the limit
	now := time.Now()
	for i := 0; i < 5; i++ {
		completedTime := now.Add(time.Duration(i) * time.Minute) // Different ages
		job := &Job{
			ID:          genTestJobID(i),
			State:       JobStateCompleted,
			CreatedAt:   completedTime,
			CompletedAt: &completedTime,
		}
		q.jobs[job.ID] = job
	}

	// Run cleanup
	q.cleanupOldJobs()

	// Count remaining completed jobs
	completedCount := 0
	for _, job := range q.jobs {
		if job.State == JobStateCompleted {
			completedCount++
		}
	}

	if completedCount != q.maxCompletedJobs {
		t.Errorf("expected exactly %d completed jobs after cleanup, got %d", q.maxCompletedJobs, completedCount)
	}
}

// TestQueue_cleanupOldJobs_MultipleStates tests cleanup handles multiple terminal states.
func TestQueue_cleanupOldJobs_MultipleStates(t *testing.T) {
	q := NewQueue(1, nil)
	q.maxCompletedJobs = 3
	q.completedJobTTL = 1 * time.Millisecond

	oldTime := time.Now().Add(-1 * time.Hour)

	// Add completed job
	completedJob := &Job{ID: "completed", State: JobStateCompleted, CreatedAt: oldTime, CompletedAt: &oldTime}
	q.jobs[completedJob.ID] = completedJob

	// Add failed job
	failedJob := &Job{ID: "failed", State: JobStateFailed, CreatedAt: oldTime, CompletedAt: &oldTime}
	q.jobs[failedJob.ID] = failedJob

	// Add canceled job
	cancelledJob := &Job{ID: "canceled", State: JobStateCancelled, CreatedAt: oldTime, CompletedAt: &oldTime}
	q.jobs[cancelledJob.ID] = cancelledJob

	// Wait for TTL
	time.Sleep(5 * time.Millisecond)

	q.cleanupOldJobs()

	// All old terminal jobs should be cleaned up (they're past TTL)
	if len(q.jobs) != 0 {
		t.Errorf("expected 0 jobs after cleanup (all past TTL), got %d", len(q.jobs))
	}
}

// TestQueue_processJob_NoHandler tests processJob when no handler is registered.
func TestQueue_processJob_NoHandler(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	job := &Job{
		ID:          "test-job",
		Type:        "unknown-type",
		State:       JobStatePending,
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if job.State != JobStateFailed {
		t.Errorf("expected state %s, got %s", JobStateFailed, job.State)
	}
	if !contains(job.Error, "no handler") {
		t.Errorf("expected error about no handler, got: %s", job.Error)
	}
}

// TestQueue_processJob_Success tests successful job processing.
func TestQueue_processJob_Success(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	handlerCalled := false
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		handlerCalled = true
		return nil
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeCommand,
		State:       JobStatePending,
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if !handlerCalled {
		t.Error("handler was not called")
	}
	if job.State != JobStateCompleted {
		t.Errorf("expected state %s, got %s", JobStateCompleted, job.State)
	}
	if job.Progress != 100 {
		t.Errorf("expected progress 100, got %d", job.Progress)
	}
	if job.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

func TestQueue_processJob_RestoresEdgeFlagsFromPayload(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		flags, ok := edgeauth.FlagsFromContext(ctx)
		if !ok {
			t.Fatal("expected edge flags in job context")
		}
		if !flags.Bool("sim.monthly.runtime.standard", false) {
			t.Fatal("expected monthly runtime edge flag to be true")
		}
		return nil
	})

	job := &Job{
		ID:    "test-job-edge-flags",
		Type:  JobTypeCommand,
		State: JobStatePending,
		Payload: map[string]interface{}{
			payloadKeyEdgeFlags: map[string]interface{}{
				"sim.monthly.runtime.standard": true,
			},
		},
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if job.State != JobStateCompleted {
		t.Errorf("expected state %s, got %s", JobStateCompleted, job.State)
	}
}

func TestQueue_processJob_RestoresOnlyCapturedCommercialAuthority(t *testing.T) {
	q := NewQueue(1, nil)
	budget := json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3}}`)
	requestCtx := verifiedJobAuthorityContext(t, budget)
	requestCtx = middleware.WithSignedEntitlements(
		requestCtx,
		"techstack.managed.runtime",
		"techstack.managed.runtime.cloudkit",
		"techstack.managed.runtime.ionos",
	)
	called := false
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, _ *Job, _ *Queue) error {
		called = true
		entitlements, ok := middleware.SignedEntitlementsFromContext(ctx)
		if !ok || !entitlements.Has("techstack.managed.runtime.ionos") {
			t.Fatalf("commercial entitlements=%#v, want verified snapshot", entitlements.Values())
		}
		decisions, ok := edgeauth.FlagsFromContext(ctx)
		if !ok || string(decisions.Budgets["cloud.runtime.credits"]) != `{"managed_servers":{"mode":"limited","limit":3}}` {
			t.Fatalf("budget=%s, want verified snapshot", decisions.Budgets["cloud.runtime.credits"])
		}
		credits, binding, err := decisions.VerifiedCloudRuntimeCredits()
		if err != nil {
			t.Fatalf("VerifiedCloudRuntimeCredits: %v", err)
		}
		if credits.ManagedServers.Limit != 3 || binding.SubjectID != "owner-1" ||
			binding.TenantID != "tenant-1" || binding.Audience != "techstack" {
			t.Fatalf("verified decision=%+v binding=%+v, want bound managed-server limit", credits, binding)
		}
		return nil
	})
	job := &Job{
		ID: "captured-commercial-authority", Type: JobTypeCommand, State: JobStatePending,
		Payload: map[string]interface{}{"tenant_id": "tenant-1", "owner_id": "owner-1"}, MaxAttempts: 1,
	}
	requestDecisions, ok := edgeauth.FlagsFromContext(requestCtx)
	if !ok {
		t.Fatal("verified request decision is missing before capture")
	}
	if _, _, err := requestDecisions.VerifiedCloudRuntimeCredits(); err != nil {
		t.Fatalf("verified request decision before capture: %v", err)
	}
	requestCtx, cancelRequest := context.WithCancel(requestCtx)
	CaptureRequestAuthority(requestCtx, job, "tenant-1", "owner-1")
	cancelRequest()
	budget[0] = ' '
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()
	q.processJob(context.Background(), job)
	if !called || job.State != JobStateCompleted {
		t.Fatalf("called=%v state=%s, want captured authority execution", called, job.State)
	}
}

func verifiedJobAuthorityContext(t *testing.T, budget json.RawMessage) context.Context {
	t.Helper()
	const (
		edgeSecret  = "edge-secret"
		flagsSecret = "flags-secret"
		signedPath  = "/api/v1/stacks/stack-1/provision"
	)
	now := time.Now().UTC().Truncate(time.Second)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequest(http.MethodPost, "https://techstack.internal"+signedPath, nil)
	if err != nil {
		t.Fatalf("new authority request: %v", err)
	}
	req.Header.Set(edgeauth.HeaderEdgeAuth, edgeauth.EdgeAuthValueJWT)
	req.Header.Set(edgeauth.HeaderEdgeService, "techstack")
	req.Header.Set(edgeauth.HeaderPublicPrefix, "/v1/techstack")
	req.Header.Set(edgeauth.HeaderUserID, "owner-1")
	req.Header.Set(edgeauth.HeaderOrgID, "tenant-1")
	req.Header.Set(edgeauth.HeaderRequestID, "request-1")
	req.Header.Set(edgeauth.HeaderEdgeKeyID, "edge-primary")
	req.Header.Set(edgeauth.HeaderEdgeTimestamp, timestamp)
	req.Header.Set(edgeauth.HeaderEdgeNonce, "nonce-1")
	req.Header.Set(edgeauth.HeaderEdgeSignedPath, signedPath)
	edgePayload := strings.Join([]string{
		"v2", "edge-primary", http.MethodPost, signedPath,
		edgeauth.EdgeAuthValueJWT, "techstack", "/v1/techstack",
		"owner-1", "tenant-1", "", "", "", "", "", "",
		timestamp, "nonce-1",
	}, "\n")
	edgeMAC := hmac.New(sha256.New, []byte(edgeSecret))
	_, _ = edgeMAC.Write([]byte(edgePayload))
	edgeSignature := "v2=" + base64.RawURLEncoding.EncodeToString(edgeMAC.Sum(nil))
	req.Header.Set(edgeauth.HeaderEdgeSignature, edgeSignature)

	decisionHeaders, err := edgeauth.SignDecisionHeaders(edgeauth.DecisionSignInput{
		Secret: flagsSecret, KeyID: "flags-primary",
		Method: req.Method, SignedPath: signedPath,
		Audience: "techstack", PublicPrefix: "/v1/techstack",
		SubjectID: "owner-1", TenantID: "tenant-1", RequestID: "request-1",
		EdgeKeyID: "edge-primary", EdgeTimestamp: timestamp,
		EdgeNonce: "nonce-1", EdgeSignature: edgeSignature,
		Flags:   map[string]bool{"techstack.managed.runtime.ionos": true},
		Budgets: map[string]any{"cloud.runtime.credits": budget},
	})
	if err != nil {
		t.Fatalf("sign authority decision: %v", err)
	}
	for header, values := range decisionHeaders {
		for _, value := range values {
			req.Header.Set(header, value)
		}
	}
	decisions, err := edgeauth.VerifyDecisionHeaders(req, edgeauth.DecisionVerifyConfig{
		PrimarySecret: flagsSecret, PrimaryKeyID: "flags-primary",
		ExpectedAudience: "techstack", ExpectedPublicPrefix: "/v1/techstack",
		SignatureWindow: 5 * time.Minute, Now: func() time.Time { return now },
		IdentityConfig: edgeauth.Config{
			EdgeAuthSecret: edgeSecret, EdgeAuthKeyID: "edge-primary",
			SignatureWindow: 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("verify authority decision: %v", err)
	}
	ctx := identity.NewContext(t.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"})
	return edgeauth.FlagsToContext(ctx, decisions)
}

func TestQueue_processJob_RawPayloadCannotForgeCommercialAuthority(t *testing.T) {
	q := NewQueue(1, nil)
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, _ *Job, _ *Queue) error {
		if entitlements, ok := middleware.SignedEntitlementsFromContext(ctx); ok || len(entitlements.Values()) != 0 {
			t.Fatalf("raw payload forged commercial entitlements: %#v", entitlements.Values())
		}
		if decisions, ok := edgeauth.FlagsFromContext(ctx); ok && len(decisions.Budgets) != 0 {
			t.Fatalf("raw payload forged signed budgets: %#v", decisions.Budgets)
		}
		return nil
	})
	job := &Job{
		ID: "raw-commercial-authority", Type: JobTypeCommand, State: JobStatePending,
		Payload: map[string]interface{}{
			"tenant_id": "tenant-1", "owner_id": "owner-1",
			"edge_entitlements": []interface{}{"techstack.managed.runtime.ionos"},
			"edge_budgets":      map[string]interface{}{"cloud.runtime.credits": map[string]interface{}{"managed_servers": nil}},
		},
		MaxAttempts: 1,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()
	q.processJob(context.Background(), job)
	if job.State != JobStateCompleted {
		t.Fatalf("state=%s, want handler to observe no forged authority", job.State)
	}
}

func TestQueue_processJob_RejectsCapturedAuthorityTenantOwnerTransplant(t *testing.T) {
	q := NewQueue(1, nil)
	requestCtx := identity.NewContext(t.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"})
	requestCtx = middleware.WithSignedEntitlements(requestCtx, "techstack.managed.runtime")
	requestCtx = edgeauth.FlagsToContext(requestCtx, edgeauth.FlagSet{Budgets: map[string]json.RawMessage{
		"cloud.runtime.credits": json.RawMessage(`{"managed_servers":{"mode":"limited","limit":3}}`),
	}})
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, _ *Job, _ *Queue) error {
		if _, ok := middleware.SignedEntitlementsFromContext(ctx); ok {
			t.Fatal("transplanted authority must not enter handler context")
		}
		return nil
	})
	job := &Job{
		ID: "transplanted-commercial-authority", Type: JobTypeCommand, State: JobStatePending,
		Payload: map[string]interface{}{"tenant_id": "tenant-1", "owner_id": "owner-1"}, MaxAttempts: 1,
	}
	CaptureRequestAuthority(requestCtx, job, "tenant-1", "owner-1")
	job.Payload["owner_id"] = "owner-2"
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()
	q.processJob(context.Background(), job)
	if job.State != JobStateCompleted {
		t.Fatalf("state=%s, want fail-closed authority removal", job.State)
	}
}

// TestQueue_processJob_ProvisionError tests handling of ProvisionError.
func TestQueue_processJob_ProvisionError(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	q.RegisterHandler(JobTypeProvision, func(ctx context.Context, job *Job, q *Queue) error {
		return &ProvisionError{
			Step:    "test-step",
			Message: "provision failed",
			Details: "detailed error",
		}
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeProvision,
		State:       JobStatePending,
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if job.State != JobStateFailed {
		t.Errorf("expected state %s, got %s", JobStateFailed, job.State)
	}
	if job.Step != "test-step" {
		t.Errorf("expected step 'test-step', got '%s'", job.Step)
	}
	if job.Error != "provision failed" {
		t.Errorf("expected error 'provision failed', got '%s'", job.Error)
	}
	if job.ErrorDetails != "detailed error" {
		t.Errorf("expected ErrorDetails 'detailed error', got '%s'", job.ErrorDetails)
	}
}

func TestQueue_processJob_WaitDoesNotConsumeAttempt(t *testing.T) {
	q := NewQueue(1, nil)
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		return &JobWaitError{
			Reason:      WaitReasonManagedRuntimeEnrollment,
			Message:     "Enrollment is still pending.",
			ResumeAfter: time.Hour,
		}
	})
	job := &Job{
		ID:          "wait-attempt-job",
		Type:        JobTypeDeploy,
		State:       JobStatePending,
		Attempts:    2,
		MaxAttempts: 3,
	}
	q.jobs[job.ID] = job

	q.processJob(context.Background(), job)

	if job.State != JobStateWaiting || job.Attempts != 2 {
		t.Fatalf("job state=%q attempts=%d, want waiting and unchanged attempts", job.State, job.Attempts)
	}
	if job.CompletedAt != nil || job.WaitReason != WaitReasonManagedRuntimeEnrollment || job.NextResumeAt == nil {
		t.Fatalf("wait metadata = completed:%v reason:%q resume:%v", job.CompletedAt, job.WaitReason, job.NextResumeAt)
	}
	q.resumeMu.Lock()
	resumeCount := len(q.resumes)
	q.resumeMu.Unlock()
	if resumeCount != 1 {
		t.Fatalf("resume schedules = %d, want exactly one", resumeCount)
	}
	q.Stop()
}

func TestQueue_WaitResumesSameJobExactlyOnce(t *testing.T) {
	q := NewQueue(1, nil)
	var calls atomic.Int32
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		if calls.Add(1) == 1 {
			return &JobWaitError{
				Reason:      WaitReasonManagedRuntimeEnrollment,
				Message:     "Enrollment is still pending.",
				ResumeAfter: 20 * time.Millisecond,
			}
		}
		return nil
	})
	job := &Job{ID: "resume-once-job", Type: JobTypeDeploy, MaxAttempts: 1}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	defer q.Stop()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job.mu.RLock()
		completed := job.State == JobStateCompleted
		job.mu.RUnlock()
		if completed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	job.mu.RLock()
	state, attempts := job.State, job.Attempts
	job.mu.RUnlock()
	if state != JobStateCompleted || calls.Load() != 2 || attempts != 1 {
		t.Fatalf("state=%q calls=%d attempts=%d, want completed/2/1", state, calls.Load(), attempts)
	}
	q.resumeMu.Lock()
	resumeCount := len(q.resumes)
	q.resumeMu.Unlock()
	if resumeCount != 0 {
		t.Fatalf("resume schedules = %d after completion, want zero", resumeCount)
	}
}

func TestQueue_CancelWaitingJobCancelsResume(t *testing.T) {
	q := NewQueue(1, nil)
	var calls atomic.Int32
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		calls.Add(1)
		return &JobWaitError{
			Reason:      WaitReasonManagedRuntimeEnrollment,
			ResumeAfter: 200 * time.Millisecond,
		}
	})
	job := &Job{ID: "cancel-wait-job", Type: JobTypeDeploy}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	defer q.Stop()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job.mu.RLock()
		waiting := job.State == JobStateWaiting
		job.mu.RUnlock()
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := q.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d after cancel, want one", calls.Load())
	}
	q.resumeMu.Lock()
	resumeCount := len(q.resumes)
	q.resumeMu.Unlock()
	if resumeCount != 0 {
		t.Fatalf("resume schedules = %d after cancel, want zero", resumeCount)
	}
}

func TestQueue_CancelCannotBeOverwrittenByHandlerReturn(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr error
	}{
		{name: "success", handlerErr: nil},
		{name: "wait", handlerErr: &JobWaitError{
			Reason:      WaitReasonManagedRuntimeEnrollment,
			ResumeAfter: time.Hour,
		}},
		{name: "failure", handlerErr: NewPermanentError(errors.New("handler failed after cancel"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewQueue(1, nil)
			started := make(chan struct{})
			release := make(chan struct{})
			q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
				close(started)
				<-release // Deliberately ignore context to exercise terminal-state CAS.
				return tt.handlerErr
			})

			job := &Job{ID: "cancel-return-" + tt.name, Type: JobTypeDeploy}
			if err := q.Enqueue(job); err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			<-started

			if err := q.Cancel(job.ID); err != nil {
				t.Fatal(err)
			}
			close(release)
			q.Stop()

			job.mu.RLock()
			state, completedAt := job.State, job.CompletedAt
			job.mu.RUnlock()
			if state != JobStateCancelled || completedAt == nil {
				t.Fatalf("state=%q completed_at=%v, want cancellation to win", state, completedAt)
			}
			q.resumeMu.Lock()
			resumeCount := len(q.resumes)
			q.resumeMu.Unlock()
			if resumeCount != 0 {
				t.Fatalf("resume schedules=%d after cancellation, want zero", resumeCount)
			}
		})
	}
}

// TestQueue_processJob_ContextCancelled tests handling of canceled context.
func TestQueue_processJob_ContextCancelled(t *testing.T) {
	q := NewQueue(1, nil)

	cancelledCtx, cancel := context.WithCancel(context.Background())

	// Handler that triggers cancellation
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		cancel() // Cancel the job context
		return ctx.Err()
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeCommand,
		State:       JobStatePending,
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(cancelledCtx, job)

	if job.State != JobStateCancelled {
		t.Errorf("expected state %s, got %s", JobStateCancelled, job.State)
	}
}

// TestQueue_processJob_RetryableError tests retry behavior for transient errors.
func TestQueue_processJob_RetryableError(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	attemptCount := 0
	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		attemptCount++
		return NewTransientError(errors.New("temporary failure"))
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeCommand,
		State:       JobStatePending,
		MaxAttempts: 2,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	// Retry backoff is an honest non-terminal wait. Its durable projection is
	// acknowledged as pending before the timer can enqueue another attempt.
	if job.State != JobStateWaiting || job.WaitReason != WaitReasonRetryBackoff {
		t.Errorf("expected retry wait, got state=%s reason=%s", job.State, job.WaitReason)
	}

	if attemptCount != 1 {
		t.Errorf("expected 1 attempt, got %d", attemptCount)
	}
}

// TestQueue_processJob_MaxAttemptsExceeded tests failure after max attempts.
func TestQueue_processJob_MaxAttemptsExceeded(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		return NewTransientError(errors.New("temporary failure"))
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeCommand,
		State:       JobStatePending,
		Attempts:    2, // Already at max-1
		MaxAttempts: 2,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if job.State != JobStateFailed {
		t.Errorf("expected state %s after max attempts, got %s", JobStateFailed, job.State)
	}
	if !contains(job.Error, "max attempts") {
		t.Errorf("expected error about max attempts, got: %s", job.Error)
	}
}

// TestQueue_processJob_NonRetryableError tests failure on permanent error.
func TestQueue_processJob_NonRetryableError(t *testing.T) {
	q := NewQueue(1, nil)
	ctx := context.Background()

	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		return NewPermanentError(errors.New("permanent failure"))
	})

	job := &Job{
		ID:          "test-job",
		Type:        JobTypeCommand,
		State:       JobStatePending,
		MaxAttempts: 3,
	}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.processJob(ctx, job)

	if job.State != JobStateFailed {
		t.Errorf("expected state %s for permanent error, got %s", JobStateFailed, job.State)
	}
	if !contains(job.Error, "non-retryable") {
		t.Errorf("expected error about non-retryable, got: %s", job.Error)
	}
}

// TestQueue_Enqueue_QueueFull tests enqueue when queue is full.
func TestQueue_Enqueue_QueueFull(t *testing.T) {
	// Create queue with small buffer using NewQueue to ensure log is initialized
	q := NewQueue(1, nil)
	// Replace pending channel with small buffer for testing
	q.pending = make(chan *Job, 1)

	// Fill the queue
	job1 := &Job{ID: "job1", Type: JobTypeCommand}
	if err := q.Enqueue(job1); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Second enqueue should fail
	job2 := &Job{ID: "job2", Type: JobTypeCommand}
	err := q.Enqueue(job2)
	if err == nil {
		t.Error("expected error when queue is full")
	}
	if !contains(err.Error(), "queue full") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, exists := q.Get(job2.ID); exists {
		t.Fatal("queue-full job remained registered as process-local work")
	}
}

// TestQueue_Enqueue_DefaultValues tests that Enqueue sets default values.
func TestQueue_Enqueue_DefaultValues(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{
		Type: JobTypeCommand,
		// ID and MaxAttempts not set
	}

	if err := q.Enqueue(job); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	if job.ID == "" {
		t.Error("expected ID to be auto-generated")
	}
	if job.MaxAttempts != 3 {
		t.Errorf("expected MaxAttempts 3, got %d", job.MaxAttempts)
	}
	if job.State != JobStatePending {
		t.Errorf("expected state %s, got %s", JobStatePending, job.State)
	}
	if job.Progress != 0 {
		t.Errorf("expected progress 0, got %d", job.Progress)
	}
	if job.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

// TestQueue_Get tests job retrieval.
func TestQueue_Get(t *testing.T) {
	q := NewQueue(1, nil)

	// Add a job
	expectedJob := &Job{ID: "test-job", State: JobStatePending}
	q.jobsMu.Lock()
	q.jobs[expectedJob.ID] = expectedJob
	q.jobsMu.Unlock()

	// Get existing job
	job, ok := q.Get("test-job")
	if !ok {
		t.Error("expected to find job")
	}
	if job.ID != expectedJob.ID {
		t.Errorf("expected job ID %s, got %s", expectedJob.ID, job.ID)
	}

	// Get non-existent job
	_, ok = q.Get("non-existent")
	if ok {
		t.Error("expected not to find job")
	}
}

// TestQueue_UpdateProgress tests progress updates.
func TestQueue_UpdateProgress(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{ID: "test-job", State: JobStateRunning}
	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.UpdateProgress(job.ID, 50, "halfway there")

	if job.Progress != 50 {
		t.Errorf("expected progress 50, got %d", job.Progress)
	}
	if job.Message != "halfway there" {
		t.Errorf("expected message 'halfway there', got '%s'", job.Message)
	}
	if len(job.Logs) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(job.Logs))
	}
	if job.Logs[0].Message != "halfway there" {
		t.Errorf("expected message 'halfway there', got '%s'", job.Logs[0].Message)
	}

	// Test update with empty message (no log entry added)
	q.UpdateProgress(job.ID, 75, "")
	if job.Progress != 75 {
		t.Errorf("expected progress 75, got %d", job.Progress)
	}
	if job.Message != "halfway there" {
		t.Errorf("expected empty progress update to preserve message, got '%s'", job.Message)
	}
	if len(job.Logs) != 1 {
		t.Errorf("expected still 1 log entry, got %d", len(job.Logs))
	}

	// Test update for non-existent job (should not panic)
	q.UpdateProgress("non-existent", 100, "test")
}

// TestQueue_Stats_AllStates tests stats counting for all states.
func TestQueue_Stats_AllStates(t *testing.T) {
	q := NewQueue(1, nil)

	// Add jobs in various states
	q.jobs["p1"] = &Job{ID: "p1", State: JobStatePending}
	q.jobs["p2"] = &Job{ID: "p2", State: JobStatePending}
	q.jobs["r1"] = &Job{ID: "r1", State: JobStateRunning}
	q.jobs["c1"] = &Job{ID: "c1", State: JobStateCompleted}
	q.jobs["c2"] = &Job{ID: "c2", State: JobStateCompleted}
	q.jobs["c3"] = &Job{ID: "c3", State: JobStateCompleted}
	q.jobs["f1"] = &Job{ID: "f1", State: JobStateFailed}
	q.jobs["x1"] = &Job{ID: "x1", State: JobStateCancelled}

	stats := q.Stats()

	expected := map[string]int{
		"total":     8,
		"pending":   2,
		"running":   1,
		"completed": 3,
		"failed":    1,
		"canceled":  1,
	}

	for key, want := range expected {
		if got := stats[key]; got != want {
			t.Errorf("stats[%q] = %d, want %d", key, got, want)
		}
	}
}

// TestQueue_StartStop tests queue lifecycle.
func TestQueue_StartStop(t *testing.T) {
	q := NewQueue(2, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// Queue should be running
	if !q.running.Load() {
		t.Error("expected queue to be running after Start")
	}

	q.Stop()

	// Queue should be stopped
	if q.running.Load() {
		t.Error("expected queue to be stopped after Stop")
	}
}

// TestQueue_Concurrency tests thread-safety of queue operations.
func TestQueue_Concurrency(t *testing.T) {
	q := NewQueue(4, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processedCount int32
	var mu sync.Mutex

	q.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		mu.Lock()
		processedCount++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	q.Start(ctx)
	defer q.Stop()

	// Enqueue multiple jobs concurrently
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := &Job{
				ID:   genTestJobID(i),
				Type: JobTypeCommand,
			}
			_ = q.Enqueue(job)
		}(i)
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond) // Wait for processing

	mu.Lock()
	count := processedCount
	mu.Unlock()

	if count == 0 {
		t.Error("expected some jobs to be processed")
	}
	t.Logf("processed %d jobs concurrently", count)
}

// TestQueue_addLog tests log entry addition.
func TestQueue_addLog(t *testing.T) {
	q := NewQueue(1, nil)

	job := &Job{ID: "test-job"}

	q.addLog(job, "info", "first message")
	q.addLog(job, "warn", "warning message")
	q.addLog(job, "error", "error message")

	if len(job.Logs) != 3 {
		t.Errorf("expected 3 log entries, got %d", len(job.Logs))
	}

	if job.Logs[0].Level != "info" {
		t.Errorf("expected level 'info', got '%s'", job.Logs[0].Level)
	}
	if job.Logs[0].Message != "first message" {
		t.Errorf("expected message 'first message', got '%s'", job.Logs[0].Message)
	}

	if job.Logs[1].Level != "warn" {
		t.Errorf("expected level 'warn', got '%s'", job.Logs[1].Level)
	}

	if job.Logs[2].Level != "error" {
		t.Errorf("expected level 'error', got '%s'", job.Logs[2].Level)
	}
}

func TestQueueSupersedeWaitingEnrollmentClaimsExactJobAndAddsReceipt(t *testing.T) {
	q := NewQueue(1, nil)
	nextResumeAt := time.Now().Add(-time.Minute)
	job := &Job{
		ID: "waiting-job", Type: JobTypeDeploy, State: JobStateWaiting,
		WaitReason: WaitReasonManagedRuntimeEnrollment, NextResumeAt: &nextResumeAt,
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	q.jobs["waiting-job"] = job

	persisted := false
	result, err := q.SupersedeWaitingEnrollment("waiting-job", map[string]any{
		"enrollment_resume_source_job_id": "waiting-job",
	}, func() error { persisted = true; return nil })
	if err != nil || result != WaitingHandoverClaimed || !persisted {
		t.Fatalf("SupersedeWaitingEnrollment() result=%q persisted=%v err=%v", result, persisted, err)
	}
	snapshot := job.Snapshot()
	if snapshot.State != JobStateCancelled || snapshot.WaitReason != "" || snapshot.NextResumeAt != nil {
		t.Fatalf("snapshot after resume = %#v", snapshot)
	}
	if snapshot.Result["enrollment_resume_source_job_id"] != "waiting-job" || snapshot.Result["lease_id"] != "lease-1" {
		t.Fatalf("result = %#v", snapshot.Result)
	}
}

func TestQueueSupersedeWaitingJobClaimsProviderProvisionWait(t *testing.T) {
	q := NewQueue(1, nil)
	nextResumeAt := time.Now().Add(-time.Minute)
	job := &Job{
		ID: "provider-wait", Type: JobTypeProvision, State: JobStateWaiting,
		WaitReason: WaitReasonManagedRuntimeProvider, NextResumeAt: &nextResumeAt,
		Result: map[string]interface{}{"lease_id": "lease-existing"},
	}
	q.jobs[job.ID] = job

	persisted := false
	result, err := q.SupersedeWaitingJob(
		job.ID,
		JobTypeProvision,
		WaitReasonManagedRuntimeProvider,
		map[string]any{"recovery_kind": WaitReasonManagedRuntimeProvider},
		func() error { persisted = true; return nil },
	)
	if err != nil || result != WaitingHandoverClaimed || !persisted {
		t.Fatalf("SupersedeWaitingJob() result=%q persisted=%v err=%v", result, persisted, err)
	}
	snapshot := job.Snapshot()
	if snapshot.State != JobStateCancelled || snapshot.WaitReason != "" || snapshot.NextResumeAt != nil {
		t.Fatalf("provider wait after handover = %#v", snapshot)
	}
	if snapshot.Result["lease_id"] != "lease-existing" || snapshot.Result["recovery_kind"] != WaitReasonManagedRuntimeProvider {
		t.Fatalf("provider wait result = %#v", snapshot.Result)
	}
}

func TestQueueSupersedeWaitingEnrollmentDistinguishesAbsentAndTerminal(t *testing.T) {
	q := NewQueue(1, nil)

	result, err := q.SupersedeWaitingEnrollment("missing-job", map[string]any{
		"receipt": "must-not-be-used",
	}, func() error { return nil })
	if err != nil || result != WaitingHandoverAbsent {
		t.Fatalf("SupersedeWaitingEnrollment(missing) result=%q err=%v", result, err)
	}

	for _, state := range []JobState{JobStateCompleted, JobStateFailed, JobStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			job := &Job{
				ID:         "terminal-" + string(state),
				Type:       JobTypeDeploy,
				State:      state,
				WaitReason: WaitReasonManagedRuntimeEnrollment,
				Result:     map[string]interface{}{"lease_id": "lease-1"},
			}
			q.jobs[job.ID] = job

			result, err := q.SupersedeWaitingEnrollment(job.ID, map[string]any{
				"receipt": "must-not-be-merged",
			}, func() error { return nil })
			if err != nil || result != WaitingHandoverTerminal {
				t.Fatalf("SupersedeWaitingEnrollment(%s) result=%q err=%v", state, result, err)
			}
			snapshot := job.Snapshot()
			if _, ok := snapshot.Result["receipt"]; ok {
				t.Fatalf("terminal job result was mutated: %#v", snapshot.Result)
			}
			if snapshot.State != state {
				t.Fatalf("terminal job state=%q, want %q", snapshot.State, state)
			}
		})
	}
}

func TestQueueSupersedeWaitingEnrollmentCancellationWinsBeforeRecovery(t *testing.T) {
	q := NewQueue(1, nil)
	job := &Job{
		ID:         "cancelled-waiting-job",
		Type:       JobTypeDeploy,
		State:      JobStateWaiting,
		WaitReason: WaitReasonManagedRuntimeEnrollment,
		Result:     map[string]interface{}{"lease_id": "lease-1"},
	}
	q.jobs[job.ID] = job

	if err := q.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	result, err := q.SupersedeWaitingEnrollment(job.ID, map[string]any{
		"receipt": "must-not-be-merged",
	}, func() error { return nil })
	if err != nil || result != WaitingHandoverTerminal {
		t.Fatalf("SupersedeWaitingEnrollment() result=%q err=%v", result, err)
	}
	snapshot := job.Snapshot()
	if snapshot.State != JobStateCancelled {
		t.Fatalf("state=%q, want %q", snapshot.State, JobStateCancelled)
	}
	if _, ok := snapshot.Result["receipt"]; ok {
		t.Fatalf("cancelled job result was mutated: %#v", snapshot.Result)
	}
	select {
	case queued := <-q.pending:
		t.Fatalf("cancelled job was queued: %s", queued.ID)
	default:
	}
}

func TestQueueSupersedeWaitingEnrollmentRejectsWrongJobType(t *testing.T) {
	q := NewQueue(1, nil)
	nextResumeAt := time.Now().Add(-time.Minute)
	job := &Job{
		ID: "wrong-type-job", Type: JobTypeProvision, State: JobStateWaiting,
		WaitReason: WaitReasonManagedRuntimeEnrollment, NextResumeAt: &nextResumeAt,
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	q.jobs[job.ID] = job

	result, err := q.SupersedeWaitingEnrollment(job.ID, map[string]any{
		"receipt": "must-not-be-merged",
	}, func() error { return nil })
	if err == nil || result != "" {
		t.Fatalf("SupersedeWaitingEnrollment() result=%q err=%v, want typed zero result and error", result, err)
	}
	snapshot := job.Snapshot()
	if snapshot.State != JobStateWaiting || snapshot.WaitReason != WaitReasonManagedRuntimeEnrollment || snapshot.NextResumeAt == nil {
		t.Fatalf("wrong-type job was mutated: %#v", snapshot)
	}
	if _, ok := snapshot.Result["receipt"]; ok {
		t.Fatalf("wrong-type job result was mutated: %#v", snapshot.Result)
	}
	select {
	case queued := <-q.pending:
		t.Fatalf("wrong-type job was queued: %s", queued.ID)
	default:
	}
}

func TestQueueSupersedeWaitingEnrollmentRollsBackReceiptWhenDurableClaimFails(t *testing.T) {
	q := NewQueue(1, nil)
	nextResumeAt := time.Now().Add(-time.Minute)
	job := &Job{
		ID: "claim-fails", Type: JobTypeDeploy, State: JobStateWaiting,
		WaitReason: WaitReasonManagedRuntimeEnrollment, NextResumeAt: &nextResumeAt,
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	q.jobs[job.ID] = job

	result, err := q.SupersedeWaitingEnrollment(job.ID, map[string]any{"receipt": "must-rollback"}, func() error {
		return errors.New("compare-and-set lost")
	})
	if err == nil || result != "" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	snapshot := job.Snapshot()
	if snapshot.State != JobStateWaiting || snapshot.Result["lease_id"] != "lease-1" {
		t.Fatalf("waiting job changed after failed claim: %#v", snapshot)
	}
	if _, exists := snapshot.Result["receipt"]; exists {
		t.Fatalf("failed claim leaked receipt into memory: %#v", snapshot.Result)
	}
}

func TestQueueDurableExecutionClaimHappensBeforeHandler(t *testing.T) {
	q := NewQueue(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claimed := atomic.Bool{}
	handled := make(chan struct{}, 1)
	q.SetExecutionClaimer(func(_ context.Context, claim ExecutionClaim) error {
		if claim.JobID != "claimed-job" || claim.TenantID != "tenant-1" || claim.TargetID != "stack-1" {
			t.Fatalf("claim = %#v", claim)
		}
		claimed.Store(true)
		return nil
	})
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		if !claimed.Load() {
			t.Fatal("handler ran before durable execution claim")
		}
		handled <- struct{}{}
		return nil
	})
	q.Start(ctx)
	defer q.Stop()
	if err := q.Enqueue(&Job{
		ID: "claimed-job", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1",
		Payload: map[string]interface{}{"tenant_id": "tenant-1"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler did not run")
	}
}

func TestQueueFailsClosedWhenDurableClaimHasNoTenantIdentity(t *testing.T) {
	q := NewQueue(1, nil)
	var claimCalls atomic.Int32
	q.SetExecutionClaimer(func(context.Context, ExecutionClaim) error {
		claimCalls.Add(1)
		return nil
	})
	var handlerCalls atomic.Int32
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		handlerCalls.Add(1)
		return nil
	})
	q.Start(context.Background())
	defer q.Stop()

	job := &Job{ID: "missing-tenant", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1"}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !job.Snapshot().PersistenceSuppressed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !job.Snapshot().PersistenceSuppressed {
		t.Fatalf("job was not fenced: %#v", job.Snapshot())
	}
	if claimCalls.Load() != 0 || handlerCalls.Load() != 0 {
		t.Fatalf("missing tenant reached durable claim or handler: claims=%d handlers=%d", claimCalls.Load(), handlerCalls.Load())
	}
}

func TestQueueStopCancelsBlockedDurableClaimWithoutJobMutexDeadlock(t *testing.T) {
	q := NewQueue(1, nil)
	claimStarted := make(chan struct{})
	q.SetExecutionClaimer(func(ctx context.Context, _ ExecutionClaim) error {
		close(claimStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	handled := atomic.Bool{}
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		handled.Store(true)
		return nil
	})
	q.Start(context.Background())
	if err := q.Enqueue(&Job{
		ID: "blocked-claim", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1",
		Payload: map[string]interface{}{"tenant_id": "tenant-1"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("durable claim did not start")
	}
	stopped := make(chan struct{})
	go func() {
		q.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind the durable claim")
	}
	if handled.Load() {
		t.Fatal("handler ran without a durable claim")
	}
}

func TestQueueDetachedPendingJobCannotStart(t *testing.T) {
	q := NewQueue(1, nil)
	var handled atomic.Int32
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		handled.Add(1)
		return nil
	})
	job := &Job{ID: "detached", Type: JobTypeDeploy, State: JobStatePending, suppressPersistence: true}
	q.processJob(context.Background(), job)
	if handled.Load() != 0 || job.State != JobStatePending {
		t.Fatalf("detached job executed: handled=%d state=%s", handled.Load(), job.State)
	}
}

func TestQueueDefersBusyDurableExecutionWithoutRunningHandler(t *testing.T) {
	q := NewQueue(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var claims atomic.Int32
	handled := make(chan struct{}, 1)
	q.SetExecutionClaimer(func(context.Context, ExecutionClaim) error {
		if claims.Add(1) == 1 {
			return ErrExecutionTargetBusy
		}
		return nil
	})
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		handled <- struct{}{}
		return nil
	})
	q.Start(ctx)
	defer q.Stop()

	previousStartedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	job := &Job{
		ID: "busy-claim", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1",
		Payload: map[string]interface{}{"tenant_id": "tenant-1"}, StartedAt: &previousStartedAt,
	}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	waitingObserved := false
	for time.Now().Before(deadline) {
		snapshot := job.Snapshot()
		if snapshot.State == JobStateWaiting && snapshot.WaitReason == WaitReasonStackExecution {
			if snapshot.Attempts != 0 {
				t.Fatalf("busy durable claim consumed attempt: %d", snapshot.Attempts)
			}
			if snapshot.StartedAt == nil || !snapshot.StartedAt.Equal(previousStartedAt) {
				t.Fatalf("execution generation changed while busy: got %v want %v", snapshot.StartedAt, previousStartedAt)
			}
			waitingObserved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waitingObserved {
		t.Fatalf("busy claim did not enter waiting state: %#v", job.Snapshot())
	}
	select {
	case <-handled:
		t.Fatal("handler ran while durable stack execution was busy")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not run after the durable claim became available")
	}
	if got := claims.Load(); got < 2 {
		t.Fatalf("durable claim attempts = %d, want at least two", got)
	}
}

func TestQueueRetriesUnavailableDurableExecutionClaimWithoutRunningHandler(t *testing.T) {
	q := NewQueue(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var claims atomic.Int32
	handled := make(chan struct{}, 1)
	q.SetExecutionClaimer(func(context.Context, ExecutionClaim) error {
		if claims.Add(1) == 1 {
			return errors.New("database temporarily unavailable")
		}
		return nil
	})
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		handled <- struct{}{}
		return nil
	})
	q.Start(ctx)
	defer q.Stop()

	job := &Job{
		ID: "unavailable-claim", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1",
		Payload: map[string]interface{}{"tenant_id": "tenant-1"},
	}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	waitingObserved := false
	for time.Now().Before(deadline) {
		snapshot := job.Snapshot()
		if snapshot.State == JobStateWaiting && snapshot.WaitReason == WaitReasonExecutionClaim {
			if snapshot.Attempts != 0 {
				t.Fatalf("unavailable durable claim consumed attempt: %d", snapshot.Attempts)
			}
			waitingObserved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waitingObserved {
		t.Fatalf("unavailable claim did not enter waiting state: %#v", job.Snapshot())
	}
	select {
	case <-handled:
		t.Fatal("handler ran while durable execution coordination was unavailable")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not run after durable execution coordination recovered")
	}
	if got := claims.Load(); got < 2 {
		t.Fatalf("durable claim attempts = %d, want at least two", got)
	}
}

func TestQueueDoesNotResumeWaitingJobBeforeDurableAcknowledgement(t *testing.T) {
	q := NewQueue(1, nil)
	var durableAvailable atomic.Bool
	var syncCalls atomic.Int32
	q.SetExecutionSnapshotSyncer(func(context.Context, JobSnapshot) error {
		syncCalls.Add(1)
		if !durableAvailable.Load() {
			return errors.New("database temporarily unavailable")
		}
		return nil
	})
	var handlerCalls atomic.Int32
	secondStarted := make(chan struct{})
	q.RegisterHandler(JobTypeDeploy, func(context.Context, *Job, *Queue) error {
		if handlerCalls.Add(1) == 1 {
			return &JobWaitError{Reason: WaitReasonManagedRuntimeEnrollment, ResumeAfter: 20 * time.Millisecond}
		}
		close(secondStarted)
		return nil
	})
	q.Start(context.Background())
	defer q.Stop()
	if err := q.Enqueue(&Job{
		ID: "durable-ack", Type: JobTypeDeploy,
		Payload: map[string]interface{}{"tenant_id": "tenant-1"}, MaxAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for syncCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if syncCalls.Load() == 0 {
		t.Fatal("durable waiting acknowledgement was not attempted")
	}
	select {
	case <-secondStarted:
		t.Fatal("job resumed before its durable release transition was acknowledged")
	case <-time.After(100 * time.Millisecond):
	}
	durableAvailable.Store(true)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("job did not resume after durable acknowledgement recovered")
	}
}

func TestQueueSerializesDestroyBehindCanceledRunningDeploy(t *testing.T) {
	q := NewQueue(2, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deployStarted := make(chan struct{})
	deployCanceled := make(chan struct{})
	releaseDeploy := make(chan struct{})
	destroyStarted := make(chan struct{})
	q.RegisterHandler(JobTypeDeploy, func(ctx context.Context, _ *Job, _ *Queue) error {
		close(deployStarted)
		<-ctx.Done()
		close(deployCanceled)
		<-releaseDeploy
		return ctx.Err()
	})
	q.RegisterHandler(JobTypeDestroy, func(context.Context, *Job, *Queue) error {
		close(destroyStarted)
		return nil
	})
	q.Start(ctx)
	defer q.Stop()
	if err := q.Enqueue(&Job{ID: "deploy", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deployStarted:
	case <-time.After(time.Second):
		t.Fatal("deploy did not start")
	}
	if canceled := q.CancelStackRollouts("stack-1"); len(canceled) != 1 || canceled[0] != "deploy" {
		t.Fatalf("canceled = %#v", canceled)
	}
	select {
	case <-deployCanceled:
	case <-time.After(time.Second):
		t.Fatal("deploy did not observe cancellation")
	}
	if err := q.Enqueue(&Job{ID: "destroy", Type: JobTypeDestroy, TargetType: "stack", TargetID: "stack-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-destroyStarted:
		t.Fatal("destroy started before canceled deploy handler exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseDeploy)
	select {
	case <-destroyStarted:
	case <-time.After(time.Second):
		t.Fatal("destroy did not start after deploy exited")
	}
}

// genTestJobID generates a test job ID.
func genTestJobID(i int) string {
	return "test-job-" + string(rune('a'+i%26)) + string(rune('0'+i%10))
}
