// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains drift detection API endpoints for Sprint 6.
package routes

import (
	"encoding/json"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/pocketbase/pocketbase/core"
)

// DriftStatusResponse represents the drift status for a stack.
type DriftStatusResponse struct {
	StackID       string    `json:"stack_id"`
	DriftStatus   string    `json:"drift_status"`
	CheckedAt     time.Time `json:"checked_at,omitempty"`
	AffectedCount int       `json:"affected_count,omitempty"`
	LastJobID     string    `json:"last_job_id,omitempty"`
	LastResultID  string    `json:"last_result_id,omitempty"`
}

// DriftDetailsResponse represents detailed drift information.
type DriftDetailsResponse struct {
	StackID           string                   `json:"stack_id"`
	Status            string                   `json:"status"`
	CheckedAt         time.Time                `json:"checked_at"`
	AffectedResources []ResourceChangeResponse `json:"affected_resources,omitempty"`
	Summary           DriftSummaryResponse     `json:"summary"`
	DurationMs        int64                    `json:"duration_ms,omitempty"`
	ErrorMessage      string                   `json:"error_message,omitempty"`
}

// ResourceChangeResponse represents a single resource change in API response.
type ResourceChangeResponse struct {
	Address      string                 `json:"address"`
	ResourceType string                 `json:"resource_type"`
	Name         string                 `json:"name,omitempty"`
	Action       string                 `json:"action"`
	Changes      map[string]interface{} `json:"changes,omitempty"`
}

// DriftSummaryResponse contains a summary of drift changes.
type DriftSummaryResponse struct {
	ToCreate int `json:"to_create"`
	ToUpdate int `json:"to_update"`
	ToDelete int `json:"to_delete"`
}

// DriftTriggerRequest represents a request to trigger a drift check.
type DriftTriggerRequest struct {
	Force bool `json:"force,omitempty"` // Force check even if recently checked
}

// DriftTriggerResponse represents the response when triggering a drift check.
type DriftTriggerResponse struct {
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

// DriftResultListResponse represents a paginated list of drift results.
type DriftResultListResponse struct {
	Items      []DriftResultItem `json:"items"`
	TotalItems int               `json:"total_items"`
	TotalPages int               `json:"total_pages"`
	Page       int               `json:"page"`
	PerPage    int               `json:"per_page"`
}

type driftResultPayload struct {
	AffectedResources []ResourceChangeResponse `json:"affected_resources"`
	Summary           DriftSummaryResponse     `json:"plan_summary"`
}

// DriftResultItem represents a single drift result in the list.
type DriftResultItem struct {
	ID            string    `json:"id"`
	StackID       string    `json:"stack_id"`
	StackName     string    `json:"stack_name,omitempty"`
	Status        string    `json:"status"`
	AffectedCount int       `json:"affected_count"`
	TriggerType   string    `json:"trigger_type"`
	CheckedAt     time.Time `json:"checked_at"`
	DurationMs    int64     `json:"duration_ms"`
}

type driftRouteHandlers struct {
	app  core.App
	orch *orchestrator.Orchestrator
}

// RegisterDriftRoutes adds drift detection API endpoints.
// F5/F6: Drift Detection Backend and UI Support
func RegisterDriftRoutes(r *httpx.Router, app core.App, orch *orchestrator.Orchestrator) {
	handlers := driftRouteHandlers{app: app, orch: orch}

	r.POST("/api/v1/stacks/{id}/drift/check", handlers.check)
	r.GET("/api/v1/stacks/{id}/drift/status", handlers.status)
	r.GET("/api/v1/stacks/{id}/drift/details", handlers.details)
	r.POST("/api/v1/stacks/{id}/drift/resolve", handlers.resolve)
	r.GET("/api/v1/drift/results", handlers.listResults)
	r.GET("/api/v1/drift/results/{id}", handlers.getResult)
	r.DELETE("/api/v1/drift/results/{id}", handlers.deleteResult)
}

func (h driftRouteHandlers) check(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	stack, stackID, ok, err := h.ownedStack(e, ownerID)
	if err != nil || !ok {
		return err
	}
	if !driftCheckAllowedForStack(stack) {
		return httpx.BadRequest(e, "Drift check only available for running or provisioning stacks", nil)
	}

	req := h.decodeTriggerRequest(e)
	limited, err := h.recentCheckRateLimit(e, stack, req.Force)
	if err != nil || limited {
		return err
	}

	stack.Set("drift_status", "checking")
	if saveErr := h.app.Save(stack); saveErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to update stack status", nil)
	}

	jobID, err := h.orch.TriggerDriftCheck(stackID, "manual")
	if err != nil {
		stack.Set("drift_status", "unknown")
		if saveErr := h.app.Save(stack); saveErr != nil {
			h.app.Logger().Warn("failed to persist drift stack update", "error", saveErr)
		}

		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to start drift check", err)
	}

	return httpx.Success(e, http.StatusAccepted, DriftTriggerResponse{
		JobID:   jobID,
		Message: "Drift check started",
	})
}

func (h driftRouteHandlers) status(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	stack, stackID, ok, err := h.ownedStack(e, ownerID)
	if err != nil || !ok {
		return err
	}
	return httpx.Success(e, http.StatusOK, h.stackDriftStatusResponse(stack, stackID))
}

func (h driftRouteHandlers) details(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	stack, _, ok, err := h.ownedStack(e, ownerID)
	if err != nil || !ok {
		return err
	}

	response, ok, err := h.stackDriftDetailsResponse(stack)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound(e, "Drift result not found")
	}
	return httpx.Success(e, http.StatusOK, response)
}

func (h driftRouteHandlers) resolve(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	stack, stackID, ok, err := h.ownedStack(e, ownerID)
	if err != nil || !ok {
		return err
	}
	if stack.GetString("drift_status") != "drifted" {
		return httpx.BadRequest(e, "No drift detected to resolve", nil)
	}

	jobID, err := h.orch.TriggerDriftResolve(stackID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to start drift resolution", err)
	}

	return httpx.Success(e, http.StatusAccepted, DriftTriggerResponse{
		JobID:   jobID,
		Message: "Drift resolution started",
	})
}

func (h driftRouteHandlers) listResults(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	pagination := ksapi.ParsePagination(e.Request)
	response, err := h.driftResultListResponse(ownerID, pagination.Page, pagination.PerPage, pagination.Offset)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to query drift results", nil)
	}
	return httpx.Success(e, http.StatusOK, response)
}

func (h driftRouteHandlers) getResult(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	result, ok, err := h.ownedResult(e, ownerID)
	if err != nil || !ok {
		return err
	}
	return httpx.Success(e, http.StatusOK, driftDetailsResponseFromResult(result))
}

func (h driftRouteHandlers) deleteResult(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	result, ok, err := h.ownedResult(e, ownerID)
	if err != nil || !ok {
		return err
	}
	if err := h.app.Delete(result); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to delete drift result", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]string{
		routeMessageField: "Drift result deleted",
	})
}

func (h driftRouteHandlers) authenticatedOwner(e *httpx.Event) (string, bool, error) {
	ownerID, err := requireAuth(e)
	if err != nil || ownerID == "" {
		return ownerID, false, err
	}
	return ownerID, true, nil
}

func (h driftRouteHandlers) ownedStack(e *httpx.Event, ownerID string) (*core.Record, string, bool, error) {
	stackID := e.Request.PathValue("id")
	if stackID == "" {
		return nil, "", false, httpx.BadRequest(e, "Stack ID is required", nil)
	}

	stack, err := h.app.FindRecordById("stacks", stackID)
	if err != nil {
		return nil, stackID, false, httpx.NotFound(e, "Stack not found")
	}
	if stack.GetString("owner_id") != ownerID {
		return nil, stackID, false, httpx.Forbidden(e, "Not allowed")
	}
	return stack, stackID, true, nil
}

func (h driftRouteHandlers) ownedResult(e *httpx.Event, ownerID string) (*core.Record, bool, error) {
	resultID := e.Request.PathValue("id")
	if resultID == "" {
		return nil, false, httpx.BadRequest(e, "Result ID is required", nil)
	}

	result, err := h.app.FindRecordById("drift_results", resultID)
	if err != nil {
		return nil, false, httpx.NotFound(e, "Drift result not found")
	}
	if result.GetString("owner_id") != ownerID {
		return nil, false, httpx.Forbidden(e, "Not allowed")
	}
	return result, true, nil
}

func (h driftRouteHandlers) decodeTriggerRequest(e *httpx.Event) DriftTriggerRequest {
	var req DriftTriggerRequest
	if e.Request.ContentLength > 0 {
		_ = json.NewDecoder(e.Request.Body).Decode(&req)
	}
	return req
}

func (h driftRouteHandlers) recentCheckRateLimit(e *httpx.Event, stack *core.Record, force bool) (bool, error) {
	if force {
		return false, nil
	}
	lastCheck := stack.GetDateTime("drift_checked_at")
	if lastCheck.IsZero() || time.Since(lastCheck.Time()) >= 5*time.Minute {
		return false, nil
	}
	return true, httpx.Error(e, http.StatusTooManyRequests, "RATE_LIMITED",
		"Drift check recently run. Wait or use force=true.", nil)
}

func driftCheckAllowedForStack(stack *core.Record) bool {
	status := stack.GetString("status")
	return status == "running" || status == "provisioning" //nolint:goconst // Stack statuses mirror PocketBase wire values.
}

func (h driftRouteHandlers) stackDriftStatusResponse(stack *core.Record, stackID string) DriftStatusResponse {
	response := DriftStatusResponse{
		StackID:     stackID,
		DriftStatus: stack.GetString("drift_status"),
	}

	checkedAt := stack.GetDateTime("drift_checked_at")
	if !checkedAt.IsZero() {
		response.CheckedAt = checkedAt.Time()
	}
	lastResultID := stack.GetString("last_drift_result_id")
	if lastResultID != "" {
		if result, err := h.app.FindRecordById("drift_results", lastResultID); err == nil && driftResultBelongsToStack(result, stack) {
			response.LastResultID = lastResultID
			response.AffectedCount = int(result.GetFloat("affected_count"))
			response.LastJobID = result.GetString("job_id")
		}
	}
	return response
}

func (h driftRouteHandlers) stackDriftDetailsResponse(stack *core.Record) (DriftDetailsResponse, bool, error) {
	stackID := stack.Id
	lastResultID := stack.GetString("last_drift_result_id")
	if lastResultID == "" {
		return DriftDetailsResponse{
			StackID: stackID,
			Status:  "never_checked",
			Summary: DriftSummaryResponse{},
		}, true, nil
	}

	result, err := h.app.FindRecordById("drift_results", lastResultID)
	if err != nil {
		return DriftDetailsResponse{}, false, nil
	}
	if !driftResultBelongsToStack(result, stack) {
		return DriftDetailsResponse{}, false, nil
	}
	return driftDetailsResponseFromResult(result), true, nil
}

func driftResultBelongsToStack(result, stack *core.Record) bool {
	return result.GetString("owner_id") == stack.GetString("owner_id") && result.GetString("stack_id") == stack.Id
}

func (h driftRouteHandlers) driftResultListResponse(ownerID string, page, perPage, offset int) (DriftResultListResponse, error) {
	records, err := h.app.FindRecordsByFilter(
		"drift_results",
		"owner_id = {:ownerId}",
		"-checked_at",
		perPage,
		offset,
		map[string]any{"ownerId": ownerID},
	)
	if err != nil {
		return DriftResultListResponse{}, err
	}

	allRecords, _ := h.app.FindRecordsByFilter(
		"drift_results",
		"owner_id = {:ownerId}",
		"-checked_at",
		1000,
		0,
		map[string]any{"ownerId": ownerID},
	)

	totalItems := len(allRecords)
	totalPages := (totalItems + perPage - 1) / perPage
	return DriftResultListResponse{
		Items:      h.driftResultItems(records),
		TotalItems: totalItems,
		TotalPages: totalPages,
		Page:       page,
		PerPage:    perPage,
	}, nil
}

func (h driftRouteHandlers) driftResultItems(records []*core.Record) []DriftResultItem {
	items := make([]DriftResultItem, 0, len(records))
	for _, record := range records {
		item := DriftResultItem{
			ID:            record.Id,
			StackID:       record.GetString("stack_id"),
			Status:        record.GetString("status"),
			AffectedCount: int(record.GetFloat("affected_count")),
			TriggerType:   record.GetString("trigger_type"),
			CheckedAt:     record.GetDateTime("checked_at").Time(),
			DurationMs:    int64(record.GetFloat("duration_ms")),
		}
		if item.StackID != "" {
			if stack, err := h.app.FindRecordById("stacks", item.StackID); err == nil {
				item.StackName = stack.GetString("name")
			}
		}
		items = append(items, item)
	}
	return items
}

func driftDetailsResponseFromResult(result *core.Record) DriftDetailsResponse {
	response := DriftDetailsResponse{
		StackID:      result.GetString("stack_id"),
		Status:       result.GetString("status"),
		CheckedAt:    result.GetDateTime("checked_at").Time(),
		DurationMs:   int64(result.GetFloat("duration_ms")),
		ErrorMessage: result.GetString("error_message"),
	}
	applyDriftResultPayload(result, &response)
	return response
}

func applyDriftResultPayload(result *core.Record, response *DriftDetailsResponse) {
	if result == nil || response == nil {
		return
	}

	var payload driftResultPayload
	if err := result.UnmarshalJSONField("affected_resources", &payload.AffectedResources); err == nil {
		response.AffectedResources = payload.AffectedResources
	}
	if err := result.UnmarshalJSONField("plan_summary", &payload.Summary); err == nil {
		response.Summary = payload.Summary
	}
}
