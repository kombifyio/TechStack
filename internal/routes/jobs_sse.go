// Package routes provides the Jobs SSE endpoint for real-time progress updates.
//
//nolint:goconst
package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/pocketbase/pocketbase/core"
)

// JobProgress represents a job progress update sent via SSE.
type JobProgress struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	WaitReason        string `json:"wait_reason,omitempty"`
	NextResumeAt      string `json:"next_resume_at,omitempty"`
	ResumeAvailableAt string `json:"resume_available_at,omitempty"`
	ResumeAvailable   bool   `json:"resume_available,omitempty"`
	Progress          int    `json:"progress"`
	Step              string `json:"step,omitempty"`
	CurrentStep       string `json:"current_step"`
	Message           string `json:"message,omitempty"`
	Error             string `json:"error,omitempty"`
	ErrorDetails      string `json:"error_details,omitempty"`
}

// RegisterJobsSSERoutes adds the SSE endpoint for job progress streaming.
func RegisterJobsSSERoutes(r *httpx.Router, app core.App) {
	// GET /api/v1/jobs - List jobs owned by the authenticated user.
	r.GET("/api/v1/jobs", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		query := parseJobListQuery(e.Request)
		stores := currentJobRouteStores()
		if tenantID := tenantIDFromJobRequest(e); stores.Jobs != nil && stores.Stacks != nil && tenantID != "" {
			payload, err := listOwnedJobsFromStore(e, stores, tenantID, ownerID, query)
			if err != nil {
				return err
			}
			return httpx.Success(e, http.StatusOK, payload)
		}

		payload, err := listOwnedJobs(e, app, ownerID, query)
		if err != nil {
			return err
		}
		return httpx.Success(e, http.StatusOK, payload)
	})

	// GET /api/v1/jobs/{id} - Get current job state for authenticated UI polling.
	r.GET("/api/v1/jobs/{id}", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		if details, handled, err := configuredStoreJobDetails(e, ownerID, jobID); handled {
			if err != nil {
				return err
			}
			return httpx.Success(e, http.StatusOK, details)
		}

		job, stack, err := findJobAndAuthorize(e, app, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		_ = stack

		return httpx.Success(e, http.StatusOK, jobRecordDetails(job))
	})

	// GET /api/collections/jobs/records/{id} keeps the legacy PocketBase record
	// polling URL alive while jobs are persisted in the control-plane store.
	r.GET("/api/collections/jobs/records/{id}", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		if details, handled, err := configuredStoreJobDetails(e, ownerID, jobID); handled {
			if err != nil {
				return err
			}
			return writeLegacyJobRecord(e, details)
		}

		job, stack, err := findJobAndAuthorize(e, app, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		_ = stack
		return writeLegacyJobRecord(e, jobRecordDetails(job))
	})

	// GET /api/v1/jobs/{id}/stream - Stream job progress updates via SSE
	r.GET("/api/v1/jobs/{id}/stream", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		stores := currentJobRouteStores()
		if tenantID := tenantIDFromJobRequest(e); stores.Jobs != nil && stores.Stacks != nil && tenantID != "" {
			return streamJobFromStore(e, stores, tenantID, jobID, ownerID)
		}

		job, stack, err := findJobAndAuthorize(e, app, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		_ = stack // stack is already validated; retained to make intent explicit

		// Set SSE headers
		// Note: CORS is handled globally by the CORS middleware in main.go
		// Do NOT set Access-Control-Allow-Origin here to avoid conflicts
		e.Response.Header().Set("Content-Type", "text/event-stream")
		e.Response.Header().Set("Cache-Control", "no-cache")
		e.Response.Header().Set("Connection", "keep-alive")
		e.Response.WriteHeader(http.StatusOK)

		// Get the flusher
		flusher, ok := e.Response.(http.Flusher)
		if !ok {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "SSE not supported", nil)
		}

		// Send initial state
		progress := jobProgressFromRecord(job)

		if err := sendSSEEvent(e.Response, "progress", progress); err != nil {
			return nil
		}
		flusher.Flush()

		// Check if job is already done
		if isTerminalState(progress.State) {
			sendSSEEvent(e.Response, "done", progress)
			flusher.Flush()
			return nil
		}

		// Poll for updates (PocketBase doesn't have native SSE subscription for records)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.After(10 * time.Minute) // Max 10 minute connection

		for {
			select {
			case <-e.Request.Context().Done():
				// Client disconnected
				return nil

			case <-timeout:
				sendSSEEvent(e.Response, "timeout", map[string]string{
					"message": "Connection timeout after 10 minutes",
				})
				flusher.Flush()
				return nil

			case <-ticker.C:
				// Fetch latest job state
				job, stack, err := findJobAndAuthorize(e, app, jobID, ownerID)
				if err != nil || job == nil {
					sendSSEEvent(e.Response, "error", map[string]string{
						"message": "Unauthorized or job not found",
					})
					flusher.Flush()
					return nil
				}
				_ = stack

				current := jobProgressFromRecord(job)

				// Only send update if something changed
				if legacyJobProgressChanged(progress, current) {
					progress = current

					if err := sendSSEEvent(e.Response, "progress", progress); err != nil {
						return nil // Client disconnected
					}
					flusher.Flush()
				}

				// Check if job is done
				if isTerminalState(current.State) {
					sendSSEEvent(e.Response, "done", progress)
					flusher.Flush()
					return nil
				}
			}
		}
	})

	// GET /api/v1/jobs/{id}/logs - Get job logs
	r.GET("/api/v1/jobs/{id}/logs", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		if details, handled, err := configuredStoreJobDetails(e, ownerID, jobID); handled {
			if err != nil {
				return err
			}
			return httpx.Success(e, http.StatusOK, details)
		}

		job, stack, err := findJobAndAuthorize(e, app, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		_ = stack

		return httpx.Success(e, http.StatusOK, jobRecordDetails(job))
	})
}

type jobListQuery struct {
	Page    int
	PerPage int
	StackID string
	State   string
	Type    string
	Search  string
}

type jobListPayload struct {
	Items      []map[string]any `json:"items"`
	Page       int              `json:"page"`
	PerPage    int              `json:"per_page"`
	TotalItems int              `json:"total_items"`
	TotalPages int              `json:"total_pages"`
}

func tenantIDFromJobRequest(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	id := identity.FromContext(e.Request.Context())
	if id == nil {
		return ""
	}
	return strings.TrimSpace(id.OrgID)
}

func jobRecordDetails(job *core.Record) map[string]any {
	// Defense in depth: findJobAndAuthorize writes a 404 response and returns
	// (nil, nil, nil) when the job is missing; callers must also nil-check
	// the record but a panic here would still take down the request handler
	// (and Sentry recorded one such crash from a smoke-test poll on a bogus
	// job_id - see KOMBIFY-TECHSTACK-3).
	if job == nil {
		return nil
	}
	message := job.GetString("message")
	if message == "" {
		message = job.GetString("current_step")
	}
	result := job.Get("result")
	state, waitReason, nextResumeAt := apiJobWaitProjection(job.GetString("state"), result)
	resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
	details := map[string]any{
		"id":            job.Id,
		"type":          job.GetString("type"),
		"state":         state,
		"progress":      job.GetFloat("progress"),
		"step":          job.GetString("step"),
		"current_step":  job.GetString("current_step"),
		"message":       message,
		"error":         job.GetString("error"),
		"error_details": job.GetString("error_details"),
		"error_message": job.GetString("error_message"),
		"stack_id":      job.GetString("stack_id"),
		"result":        result,
		"logs":          job.Get("logs"),
		"created":       job.GetDateTime("created"),
		"updated":       job.GetDateTime("updated"),
		"created_at":    job.GetDateTime("created"),
		"updated_at":    job.GetDateTime("updated"),
	}
	if waitReason != "" {
		details["wait_reason"] = waitReason
	}
	if nextResumeAt != "" {
		details["next_resume_at"] = nextResumeAt
	}
	if resumeAvailableAt != "" {
		details["resume_available_at"] = resumeAvailableAt
		details["resume_available"] = resumeAvailable
	}
	return details
}

func jobDetailsFromStore(job controlplane.Job) map[string]any {
	message := job.Message
	if message == "" {
		message = job.Step
	}
	state, waitReason, nextResumeAt := apiJobWaitProjection(job.State, job.Result)
	resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
	details := map[string]any{
		"id":            job.ID,
		"type":          job.Type,
		"state":         state,
		"progress":      job.Progress,
		"step":          job.Step,
		"current_step":  job.Step,
		"message":       message,
		"error":         job.Error,
		"error_details": job.ErrorDetails,
		"error_message": job.Error,
		"stack_id":      job.StackID,
		"result":        job.Result,
		"logs":          job.Logs,
		"created":       job.CreatedAt,
		"updated":       job.UpdatedAt,
		"created_at":    job.CreatedAt,
		"updated_at":    job.UpdatedAt,
	}
	if waitReason != "" {
		details["wait_reason"] = waitReason
	}
	if nextResumeAt != "" {
		details["next_resume_at"] = nextResumeAt
	}
	if resumeAvailableAt != "" {
		details["resume_available_at"] = resumeAvailableAt
		details["resume_available"] = resumeAvailable
	}
	return details
}

func apiJobWaitProjection(storedState string, result any) (state, reason, nextResumeAt string) {
	resultMap, hasResult := mapFromJSONAny(result)
	if storedState == "cancelled" {
		if hasResult {
			if recoveryReason, recoveryAt, ok := claimedManagedRolloutRecoveryWait(resultMap); ok {
				return "waiting", recoveryReason, recoveryAt
			}
		}
		// PostgreSQL uses British spelling in its legacy state constraint while
		// the public v1 API has always exposed the canonical `canceled` value.
		return "canceled", "", ""
	}
	state = storedState
	if storedState != "pending" {
		return state, "", ""
	}
	if !hasResult {
		return state, "", ""
	}
	wait, ok := mapFromJSONAny(resultMap["job_wait"])
	if !ok || jobWaitString(wait, "state") != "waiting" {
		return state, "", ""
	}
	return "waiting", jobWaitString(wait, "reason"), jobWaitString(wait, "next_resume_at")
}

// claimedManagedRolloutRecoveryWait keeps the exact recovery affordance visible
// after the source compare-and-set has been persisted but before its
// deterministic replacement deploy was admitted. A repeated request is safe:
// the recovery receipt rehydrates the same replacement job and never re-enters
// provider creation.
func claimedManagedRolloutRecoveryWait(result map[string]interface{}) (reason, nextResumeAt string, ok bool) {
	wait, waitOK := mapFromJSONAny(result["job_wait"])
	if !waitOK || jobWaitString(wait, "state") != string(jobs.JobStateWaiting) {
		return "", "", false
	}
	reason = jobWaitString(wait, "reason")
	if reason != jobs.WaitReasonManagedRuntimeEnrollment && reason != jobs.WaitReasonManagedRuntimeProvider {
		return "", "", false
	}
	nextResumeAt = jobWaitString(wait, "next_resume_at")
	if nextResumeAt == "" || jobWaitString(result, "enrollment_resume_kind") != reason {
		return "", "", false
	}
	for _, field := range []string{
		"enrollment_resume_key",
		"enrollment_resume_source_job_id",
		"enrollment_resume_lease_id",
		"enrollment_resume_server_id",
		"enrollment_resume_scheduled_at",
	} {
		if jobWaitString(result, field) == "" {
			return "", "", false
		}
	}
	return reason, nextResumeAt, true
}

func apiJobResumeAvailability(reason, nextResumeAt string, now time.Time) (string, bool) {
	reason = strings.TrimSpace(reason)
	if reason != jobs.WaitReasonManagedRuntimeEnrollment && reason != jobs.WaitReasonManagedRuntimeProvider {
		return "", false
	}
	scheduledAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(nextResumeAt))
	if err != nil {
		return "", false
	}
	availableAt := scheduledAt.UTC().Add(jobs.ManagedRuntimeEnrollmentRecoveryGrace)
	return availableAt.Format(time.RFC3339Nano), !now.Before(availableAt)
}

func jobWaitString(wait map[string]interface{}, key string) string {
	value, _ := wait[key].(string)
	return strings.TrimSpace(value)
}

func configuredStoreJobDetails(e *httpx.Event, ownerID, jobID string) (map[string]any, bool, error) {
	stores := currentJobRouteStores()
	tenantID := tenantIDFromJobRequest(e)
	if stores.Jobs == nil || stores.Stacks == nil || tenantID == "" {
		return nil, false, nil
	}
	job, err := stores.Jobs.GetJob(e.Request.Context(), tenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch job", nil)
	}
	if strings.TrimSpace(job.StackID) == "" {
		return nil, true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Job missing stack_id", nil)
	}
	stack, err := stackForJobAuthorization(e.Request.Context(), stores.Stacks, tenantID, job.StackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return nil, true, httpx.Forbidden(e, "Not allowed")
	}
	return jobDetailsFromStore(*job), true, nil
}

func writeLegacyJobRecord(e *httpx.Event, details map[string]any) error {
	if details == nil {
		return httpx.NotFound(e, "Job not found")
	}
	e.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
	e.Response.Header().Set("Cache-Control", "no-store")
	e.Response.WriteHeader(http.StatusOK)
	return json.NewEncoder(e.Response).Encode(details)
}

func parseJobListQuery(r *http.Request) jobListQuery {
	values := r.URL.Query()
	page := parseBoundedPositiveInt(values.Get("page"), 1, 10_000, 1)
	perPage := parseBoundedPositiveInt(values.Get("per_page"), 1, 200, 50)
	if values.Get("limit") != "" {
		perPage = parseBoundedPositiveInt(values.Get("limit"), 1, 200, perPage)
	}
	return jobListQuery{
		Page:    page,
		PerPage: perPage,
		StackID: strings.TrimSpace(values.Get("stack_id")),
		State:   strings.TrimSpace(values.Get("state")),
		Type:    strings.TrimSpace(firstNonEmptyString(values.Get("type"), values.Get("job_type"))),
		Search:  strings.ToLower(strings.TrimSpace(values.Get("search"))),
	}
}

func parseBoundedPositiveInt(raw string, min, max, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func listOwnedJobs(e *httpx.Event, app core.App, ownerID string, query jobListQuery) (jobListPayload, error) {
	ownedStackIDs, err := ownerStackIDs(app, ownerID)
	if err != nil {
		return jobListPayload{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stacks", nil)
	}
	if query.StackID != "" && !ownedStackIDs[query.StackID] {
		return jobListPayload{}, httpx.Forbidden(e, "Not allowed")
	}

	records, err := app.FindRecordsByFilter("jobs", "", "-created", 1000, 0)
	if err != nil {
		return jobListPayload{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch jobs", nil)
	}

	filtered := filterJobRecords(records, ownedStackIDs, query)
	return jobListResponse(filtered, query), nil
}

func listOwnedJobsFromStore(e *httpx.Event, stores JobRouteStores, tenantID, ownerID string, query jobListQuery) (jobListPayload, error) {
	var jobs []controlplane.Job
	var err error
	if query.StackID != "" {
		stack, stackErr := stackForJobAuthorization(e.Request.Context(), stores.Stacks, tenantID, query.StackID)
		if errors.Is(stackErr, controlplane.ErrNotFound) {
			return jobListPayload{}, httpx.NotFound(e, "Stack not found")
		}
		if stackErr != nil {
			return jobListPayload{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
		}
		if stack.OwnerSubjectID != ownerID {
			return jobListPayload{}, httpx.Forbidden(e, "Not allowed")
		}
		jobs, err = stores.Jobs.ListJobsByStack(e.Request.Context(), tenantID, query.StackID, 1000)
	} else {
		jobs, err = stores.Jobs.ListJobsByTenant(e.Request.Context(), tenantID, 1000)
	}
	if err != nil {
		return jobListPayload{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch jobs", nil)
	}

	filtered := make([]controlplane.Job, 0, len(jobs))
	ownedStacks := map[string]bool{}
	for _, job := range jobs {
		if query.StackID != "" && job.StackID != query.StackID {
			continue
		}
		projectedState, _, _ := apiJobWaitProjection(job.State, job.Result)
		if query.State != "" && projectedState != query.State {
			continue
		}
		if query.Type != "" && job.Type != query.Type {
			continue
		}
		if query.Search != "" && !jobMatchesSearchFromStore(job, query.Search) {
			continue
		}
		if !ownedStacks[job.StackID] {
			stack, stackErr := stackForJobAuthorization(e.Request.Context(), stores.Stacks, tenantID, job.StackID)
			if stackErr != nil || stack.OwnerSubjectID != ownerID {
				continue
			}
			ownedStacks[job.StackID] = true
		}
		filtered = append(filtered, job)
	}
	return jobListResponseFromStore(filtered, query), nil
}

func ownerStackIDs(app core.App, ownerID string) (map[string]bool, error) {
	stacks, err := app.FindRecordsByFilter(
		"stacks",
		"owner_id = {:ownerId}",
		"",
		500,
		0,
		map[string]any{"ownerId": ownerID},
	)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(stacks))
	for _, stack := range stacks {
		ids[stack.Id] = true
	}
	return ids, nil
}

func filterJobRecords(records []*core.Record, ownedStackIDs map[string]bool, query jobListQuery) []*core.Record {
	filtered := make([]*core.Record, 0, len(records))
	for _, job := range records {
		stackID := job.GetString("stack_id")
		if !ownedStackIDs[stackID] {
			continue
		}
		if query.StackID != "" && stackID != query.StackID {
			continue
		}
		projectedState, _, _ := apiJobWaitProjection(job.GetString("state"), job.Get("result"))
		if query.State != "" && projectedState != query.State {
			continue
		}
		if query.Type != "" && job.GetString("type") != query.Type {
			continue
		}
		if query.Search != "" && !jobMatchesSearch(job, query.Search) {
			continue
		}
		filtered = append(filtered, job)
	}
	return filtered
}

func jobMatchesSearch(job *core.Record, search string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		job.GetString("type"),
		job.GetString("state"),
		job.GetString("step"),
		job.GetString("current_step"),
		job.GetString("message"),
		job.GetString("error"),
		job.GetString("error_message"),
	}, " "))
	return strings.Contains(haystack, search)
}

func jobMatchesSearchFromStore(job controlplane.Job, search string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		job.Type,
		job.State,
		job.Step,
		job.Message,
		job.Error,
	}, " "))
	return strings.Contains(haystack, search)
}

func jobListResponse(records []*core.Record, query jobListQuery) jobListPayload {
	totalItems := len(records)
	totalPages := 0
	if totalItems > 0 {
		totalPages = (totalItems + query.PerPage - 1) / query.PerPage
	}
	start := (query.Page - 1) * query.PerPage
	if start > totalItems {
		start = totalItems
	}
	end := start + query.PerPage
	if end > totalItems {
		end = totalItems
	}

	items := make([]map[string]any, 0, end-start)
	for _, job := range records[start:end] {
		items = append(items, jobRecordDetails(job))
	}
	return jobListPayload{
		Items:      items,
		Page:       query.Page,
		PerPage:    query.PerPage,
		TotalItems: totalItems,
		TotalPages: totalPages,
	}
}

func jobListResponseFromStore(jobs []controlplane.Job, query jobListQuery) jobListPayload {
	totalItems := len(jobs)
	totalPages := 0
	if totalItems > 0 {
		totalPages = (totalItems + query.PerPage - 1) / query.PerPage
	}
	start := (query.Page - 1) * query.PerPage
	if start > totalItems {
		start = totalItems
	}
	end := start + query.PerPage
	if end > totalItems {
		end = totalItems
	}

	items := make([]map[string]any, 0, end-start)
	for _, job := range jobs[start:end] {
		items = append(items, jobDetailsFromStore(job))
	}
	return jobListPayload{
		Items:      items,
		Page:       query.Page,
		PerPage:    query.PerPage,
		TotalItems: totalItems,
		TotalPages: totalPages,
	}
}

func findJobAndAuthorize(e *httpx.Event, app core.App, jobID, userID string) (*core.Record, *core.Record, error) {
	job, err := app.FindRecordById("jobs", jobID)
	if err != nil {
		return nil, nil, httpx.NotFound(e, "Job not found")
	}

	stackID := job.GetString("stack_id")
	if stackID == "" {
		return nil, nil, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Job missing stack_id", nil)
	}

	stack, err := app.FindRecordById("stacks", stackID)
	if err != nil {
		return nil, nil, httpx.NotFound(e, "Stack not found")
	}
	if stack.GetString("owner_id") != userID {
		return nil, nil, httpx.Forbidden(e, "Not allowed")
	}

	return job, stack, nil
}

func findJobAndAuthorizeFromStore(e *httpx.Event, stores JobRouteStores, tenantID, jobID, userID string) (*controlplane.Job, error) {
	job, err := stores.Jobs.GetJob(e.Request.Context(), tenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, httpx.NotFound(e, "Job not found")
	}
	if err != nil {
		return nil, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch job", nil)
	}
	if strings.TrimSpace(job.StackID) == "" {
		return nil, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Job missing stack_id", nil)
	}
	stack, err := stackForJobAuthorization(e.Request.Context(), stores.Stacks, tenantID, job.StackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return nil, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != userID {
		return nil, httpx.Forbidden(e, "Not allowed")
	}
	return job, nil
}

type archivedStackReceiptReader interface {
	GetStackIncludingDeleted(context.Context, string, string) (*controlplane.Stack, error)
}

func stackForJobAuthorization(ctx context.Context, stacks controlplane.StackStore, tenantID, stackID string) (*controlplane.Stack, error) {
	stack, err := stacks.GetStack(ctx, tenantID, stackID)
	if !errors.Is(err, controlplane.ErrNotFound) {
		return stack, err
	}
	reader, ok := stacks.(archivedStackReceiptReader)
	if !ok {
		return nil, err
	}
	return reader.GetStackIncludingDeleted(ctx, tenantID, stackID)
}

func streamJobFromStore(e *httpx.Event, stores JobRouteStores, tenantID, jobID, ownerID string) error {
	job, err := findJobAndAuthorizeFromStore(e, stores, tenantID, jobID, ownerID)
	if err != nil || job == nil {
		return err
	}

	e.Response.Header().Set("Content-Type", "text/event-stream")
	e.Response.Header().Set("Cache-Control", "no-cache")
	e.Response.Header().Set("Connection", "keep-alive")
	e.Response.WriteHeader(http.StatusOK)

	flusher, ok := e.Response.(http.Flusher)
	if !ok {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "SSE not supported", nil)
	}

	progress := jobProgressFromStore(*job)
	if err := sendSSEEvent(e.Response, "progress", progress); err != nil {
		return nil
	}
	flusher.Flush()

	if isTerminalState(progress.State) {
		sendSSEEvent(e.Response, "done", progress)
		flusher.Flush()
		return nil
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-e.Request.Context().Done():
			return nil
		case <-timeout:
			sendSSEEvent(e.Response, "timeout", map[string]string{
				"message": "Connection timeout after 10 minutes",
			})
			flusher.Flush()
			return nil
		case <-ticker.C:
			job, err := findJobAndAuthorizeFromStore(e, stores, tenantID, jobID, ownerID)
			if err != nil || job == nil {
				sendSSEEvent(e.Response, "error", map[string]string{
					"message": "Unauthorized or job not found",
				})
				flusher.Flush()
				return nil
			}
			current := jobProgressFromStore(*job)
			if legacyJobProgressChanged(progress, current) {
				progress = current
				if err := sendSSEEvent(e.Response, "progress", current); err != nil {
					return nil
				}
				flusher.Flush()
			}
			if isTerminalState(current.State) {
				sendSSEEvent(e.Response, "done", current)
				flusher.Flush()
				return nil
			}
		}
	}
}

func jobProgressFromStore(job controlplane.Job) JobProgress {
	state, waitReason, nextResumeAt := apiJobWaitProjection(job.State, job.Result)
	resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
	return JobProgress{
		ID:                job.ID,
		State:             state,
		WaitReason:        waitReason,
		NextResumeAt:      nextResumeAt,
		ResumeAvailableAt: resumeAvailableAt,
		ResumeAvailable:   resumeAvailable,
		Progress:          job.Progress,
		Step:              job.Step,
		CurrentStep:       job.Step,
		Message:           job.Message,
		Error:             job.Error,
		ErrorDetails:      job.ErrorDetails,
	}
}

func jobProgressFromRecord(job *core.Record) JobProgress { // pocketbase-migration-compat: legacy SSE projection only
	state, waitReason, nextResumeAt := apiJobWaitProjection(job.GetString("state"), job.Get("result"))
	resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
	message := job.GetString("message")
	if message == "" {
		message = job.GetString("current_step")
	}
	return JobProgress{
		ID:                job.Id,
		State:             state,
		WaitReason:        waitReason,
		NextResumeAt:      nextResumeAt,
		ResumeAvailableAt: resumeAvailableAt,
		ResumeAvailable:   resumeAvailable,
		Progress:          int(job.GetFloat("progress")),
		Step:              job.GetString("step"),
		CurrentStep:       job.GetString("current_step"),
		Message:           message,
		Error:             job.GetString("error"),
		ErrorDetails:      job.GetString("error_details"),
	}
}

func legacyJobProgressChanged(previous, current JobProgress) bool {
	return current.Progress != previous.Progress ||
		current.State != previous.State ||
		current.WaitReason != previous.WaitReason ||
		current.NextResumeAt != previous.NextResumeAt ||
		current.ResumeAvailableAt != previous.ResumeAvailableAt ||
		current.ResumeAvailable != previous.ResumeAvailable ||
		current.Step != previous.Step ||
		current.CurrentStep != previous.CurrentStep ||
		current.Message != previous.Message ||
		current.Error != previous.Error ||
		current.ErrorDetails != previous.ErrorDetails
}

// sendSSEEvent sends a single SSE event.
func sendSSEEvent(w http.ResponseWriter, event string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
	return err
}

// isTerminalState checks if a job state is terminal (completed, failed, canceled).
func isTerminalState(state string) bool {
	return state == "completed" || state == "failed" || state == "canceled"
}
