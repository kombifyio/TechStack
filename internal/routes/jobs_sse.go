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

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/secrets"
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

type JobRouteStores struct {
	Stacks controlplane.StackStore
	Jobs   controlplane.JobStore
}

// RegisterJobsSSERoutes adds the SSE endpoint for job progress streaming.
func RegisterJobsSSERoutes(r *httpx.Router, stores JobRouteStores) {
	if stores.Jobs == nil || stores.Stacks == nil {
		panic("RegisterJobsSSERoutes: canonical stack and job stores are required")
	}
	// GET /api/v1/jobs - List jobs owned by the authenticated user.
	r.GET("/api/v1/jobs", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.jobs.list")
		if err != nil {
			return err
		}
		payload, err := listOwnedJobsFromStore(e, stores, tenantID, ownerID, parseJobListQuery(e.Request))
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
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.jobs.read")
		if err != nil {
			return err
		}
		job, err := findJobAndAuthorizeFromStore(e, stores, tenantID, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		return httpx.Success(e, http.StatusOK, jobDetailsFromStore(*job))
	})

	// GET /api/v1/jobs/{id}/stream - Stream job progress updates via SSE
	r.GET("/api/v1/jobs/{id}/stream", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.jobs.stream")
		if err != nil {
			return err
		}
		return streamJobFromStore(e, stores, tenantID, jobID, ownerID)
	})

	// GET /api/v1/jobs/{id}/logs - Get job logs
	r.GET("/api/v1/jobs/{id}/logs", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}

		jobID := e.Request.PathValue("id")
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.jobs.logs.read")
		if err != nil {
			return err
		}
		job, err := findJobAndAuthorizeFromStore(e, stores, tenantID, jobID, ownerID)
		if err != nil || job == nil {
			return err
		}
		return httpx.Success(e, http.StatusOK, jobDetailsFromStore(*job))
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

func requireJobRouteTenant(e *httpx.Event, ownerID, capability string) (string, error) {
	return tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
}

func jobDetailsFromStore(job controlplane.Job) map[string]any {
	message := secrets.Redact(job.Message)
	if message == "" {
		message = secrets.Redact(job.Step)
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
		"error":         secrets.Redact(job.Error),
		"error_details": secrets.Redact(job.ErrorDetails),
		"error_message": secrets.Redact(job.Error),
		"stack_id":      job.StackID,
		"result":        publicJobMap(job.Result, 0),
		"logs":          publicJobLogs(job.Logs),
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

const publicJobMaxDepth = 12

func publicJobLogs(logs []map[string]any) []map[string]any {
	if logs == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(logs))
	for _, entry := range logs {
		out = append(out, publicJobMap(entry, 0))
	}
	return out
}

func publicJobMap(input map[string]any, depth int) map[string]any {
	if input == nil || depth > publicJobMaxDepth {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if secrets.SensitiveKey(key) {
			continue
		}
		if sanitized, ok := publicJobValue(value, depth+1); ok {
			out[key] = sanitized
		}
	}
	return out
}

func publicJobValue(value any, depth int) (any, bool) {
	if depth > publicJobMaxDepth {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return secrets.Redact(typed), true
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return publicJobValue(decoded, depth+1)
		}
		return secrets.Redact(string(typed)), true
	case []byte:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return publicJobValue(decoded, depth+1)
		}
		return secrets.Redact(string(typed)), true
	case map[string]any:
		return publicJobMap(typed, depth), true
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, publicJobMap(item, depth+1))
		}
		return out, true
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if sanitized, ok := publicJobValue(item, depth+1); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		return value, true
	}
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

func stackForJobAuthorization(ctx context.Context, stacks controlplane.StackStore, tenantID, stackID string) (*controlplane.Stack, error) {
	stack, err := stacks.GetStack(ctx, tenantID, stackID)
	if !errors.Is(err, controlplane.ErrNotFound) {
		return stack, err
	}
	reader, ok := stacks.(controlplane.ArchivedStackReceiptReader)
	if !ok {
		return nil, err
	}
	return reader.GetStackIncludingDeleted(ctx, tenantID, stackID)
}

// streamJobWriteBudget bounds the write deadline extension for one SSE
// progress stream; the handler closes the stream after 10 minutes regardless.
const jobStreamWriteBudget = 2 * time.Minute

// jobStreamKeepaliveInterval is the idle-cadence for SSE keepalive comments.
const jobStreamKeepaliveInterval = 25 * time.Second

func streamJobFromStore(e *httpx.Event, stores JobRouteStores, tenantID, jobID, ownerID string) error {
	// The event stream lives up to its own 10-minute budget, well past the
	// server-wide write timeout, so every write extends the deadline first.
	_ = httpx.ExtendWriteDeadline(e.Response, jobStreamWriteBudget)
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
	lastKeepalive := time.Now()

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
			// The server-wide write timeout is far shorter than this stream's
			// budget; keep the deadline ahead of every write so a slow-
			// changing job does not silently lose its stream.
			_ = httpx.ExtendWriteDeadline(e.Response, jobStreamWriteBudget)
			// A periodic comment keeps intermediaries from closing an idle
			// stream between real progress events.
			if time.Since(lastKeepalive) >= jobStreamKeepaliveInterval {
				if _, err := e.Response.Write([]byte(": keepalive\n\n")); err != nil {
					return nil
				}
				flusher.Flush()
				lastKeepalive = time.Now()
			}
			job, err := findJobAndAuthorizeFromStore(e, stores, tenantID, jobID, ownerID)
			if err != nil || job == nil {
				sendSSEEvent(e.Response, "error", map[string]string{
					"message": "Unauthorized or job not found",
				})
				flusher.Flush()
				return nil
			}
			current := jobProgressFromStore(*job)
			if jobProgressChanged(progress, current) {
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
		Message:           secrets.Redact(job.Message),
		Error:             secrets.Redact(job.Error),
		ErrorDetails:      secrets.Redact(job.ErrorDetails),
	}
}

func jobProgressChanged(previous, current JobProgress) bool {
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
