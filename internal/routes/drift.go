// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains drift detection API endpoints for Sprint 6.
package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// DriftStatusResponse represents the drift status for a StackKit deployment.
type DriftStatusResponse struct {
	KitDeploymentID string    `json:"kit_deployment_id"`
	DriftStatus     string    `json:"drift_status"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	AffectedCount   int       `json:"affected_count,omitempty"`
	LastJobID       string    `json:"last_job_id,omitempty"`
	LastResultID    string    `json:"last_result_id,omitempty"`
}

// DriftDetailsResponse represents detailed drift information.
type DriftDetailsResponse struct {
	KitDeploymentID   string                   `json:"kit_deployment_id"`
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

// DriftResultItem represents a single drift result in the list.
type DriftResultItem struct {
	ID              string    `json:"id"`
	KitDeploymentID string    `json:"kit_deployment_id"`
	StackName       string    `json:"stack_name,omitempty"`
	Status          string    `json:"status"`
	AffectedCount   int       `json:"affected_count"`
	TriggerType     string    `json:"trigger_type"`
	CheckedAt       time.Time `json:"checked_at"`
	DurationMs      int64     `json:"duration_ms"`
}

type driftRouteHandlers struct {
	orch   *orchestrator.Orchestrator
	stores DriftRouteStores
}

type DriftRouteStores struct {
	Stacks controlplane.StackStore
	Drift  controlplane.DriftResultStore
}

// RegisterDriftRoutes adds drift detection API endpoints.
// F5/F6: Drift Detection Backend and UI Support
func RegisterDriftRoutesWithStores(r *httpx.Router, orch *orchestrator.Orchestrator, stores DriftRouteStores) {
	handlers := driftRouteHandlers{orch: orch, stores: stores}

	r.POST("/api/v1/stacks/{id}/drift/check", handlers.check)
	r.GET("/api/v1/stacks/{id}/drift/status", handlers.status)
	r.GET("/api/v1/stacks/{id}/drift/details", handlers.details)
	r.POST("/api/v1/stacks/{id}/drift/resolve", handlers.resolve)
	r.GET("/api/v1/drift/results", handlers.listResults)
	r.GET("/api/v1/drift/results/{id}", handlers.getResult)
	r.DELETE("/api/v1/drift/results/{id}", handlers.deleteResult)
}

func (h driftRouteHandlers) check(e *httpx.Event) error {
	stack, tenantID, ownerID, ok, err := h.canonicalStack(e, "techstack.drift.check")
	if err != nil || !ok {
		return err
	}
	if !driftCheckAllowedForStack(stack) {
		return httpx.BadRequest(e, "Drift check only available for running or provisioning stacks", nil)
	}

	req := h.decodeTriggerRequest(e)
	limited, err := recentCanonicalDriftCheckRateLimit(e, stack, req.Force)
	if err != nil || limited {
		return err
	}

	jobID, err := h.orch.TriggerDriftCheck(orchestrator.DriftLifecycleRequest{
		RequestContext: e.Request.Context(), TenantID: tenantID, OwnerID: ownerID, StackID: stack.ID, TriggerType: "manual",
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to start drift check", err)
	}

	return httpx.Success(e, http.StatusAccepted, DriftTriggerResponse{
		JobID:   jobID,
		Message: "Drift check started",
	})
}

func (h driftRouteHandlers) status(e *httpx.Event) error {
	stack, tenantID, ownerID, ok, err := h.canonicalStack(e, "techstack.drift.read")
	if err != nil || !ok {
		return err
	}
	results, _, err := h.stores.Drift.ListDriftResults(e.Request.Context(), tenantID, ownerID, stack.ID, 1, 0)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to query drift status", nil)
	}
	return httpx.Success(e, http.StatusOK, canonicalDriftStatusResponse(stack, results))
}

func (h driftRouteHandlers) details(e *httpx.Event) error {
	stack, tenantID, ownerID, ok, err := h.canonicalStack(e, "techstack.drift.read")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	results, _, err := h.stores.Drift.ListDriftResults(e.Request.Context(), tenantID, ownerID, stack.ID, 1, 0)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to query drift details", nil)
	}
	if len(results) == 0 {
		return httpx.Success(e, http.StatusOK, DriftDetailsResponse{KitDeploymentID: stack.ID, Status: "never_checked", Summary: DriftSummaryResponse{}})
	}
	return httpx.Success(e, http.StatusOK, canonicalDriftDetailsResponse(results[0]))
}

func (h driftRouteHandlers) resolve(e *httpx.Event) error {
	stack, tenantID, ownerID, ok, err := h.canonicalStack(e, "techstack.drift.resolve")
	if err != nil || !ok {
		return err
	}
	if stack.DriftStatus != "drifted" {
		return httpx.BadRequest(e, "No drift detected to resolve", nil)
	}

	jobID, err := h.orch.TriggerDriftResolve(orchestrator.DriftLifecycleRequest{
		RequestContext: e.Request.Context(), TenantID: tenantID, OwnerID: ownerID, StackID: stack.ID,
	})
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.drift.read")
	if tenantErr != nil {
		return tenantErr
	}
	pagination := ksapi.ParsePagination(e.Request)
	if !h.canonicalReadStoresReady() || tenantID == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Drift store unavailable", nil)
	}
	results, total, err := h.stores.Drift.ListDriftResults(e.Request.Context(), tenantID, ownerID, "", pagination.PerPage, pagination.Offset)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to query drift results", nil)
	}
	totalPages := 0
	if pagination.PerPage > 0 {
		totalPages = (total + pagination.PerPage - 1) / pagination.PerPage
	}
	response := DriftResultListResponse{
		Items: canonicalDriftResultItems(results), TotalItems: total, TotalPages: totalPages,
		Page: pagination.Page, PerPage: pagination.PerPage,
	}
	return httpx.Success(e, http.StatusOK, response)
}

func (h driftRouteHandlers) getResult(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.drift.read")
	if tenantErr != nil {
		return tenantErr
	}
	resultID := strings.TrimSpace(e.Request.PathValue("id"))
	if resultID == "" {
		return httpx.BadRequest(e, "Result ID is required", nil)
	}
	if !h.canonicalReadStoresReady() || tenantID == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Drift store unavailable", nil)
	}
	result, err := h.stores.Drift.GetDriftResult(e.Request.Context(), tenantID, ownerID, resultID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Drift result not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to query drift result", nil)
	}
	return httpx.Success(e, http.StatusOK, canonicalDriftDetailsResponse(*result))
}

func (h driftRouteHandlers) deleteResult(e *httpx.Event) error {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.drift.delete")
	if tenantErr != nil {
		return tenantErr
	}
	resultID := strings.TrimSpace(e.Request.PathValue("id"))
	if resultID == "" {
		return httpx.BadRequest(e, "Result ID is required", nil)
	}
	if !h.canonicalReadStoresReady() || tenantID == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Drift store unavailable", nil)
	}
	if err := h.stores.Drift.DeleteDriftResult(e.Request.Context(), tenantID, ownerID, resultID); errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Drift result not found")
	} else if err != nil {
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

func (h driftRouteHandlers) canonicalReadStoresReady() bool {
	return h.stores.Stacks != nil && h.stores.Drift != nil
}

func (h driftRouteHandlers) canonicalStack(e *httpx.Event, capability string) (*controlplane.Stack, string, string, bool, error) {
	ownerID, ok, err := h.authenticatedOwner(e)
	if err != nil || !ok {
		return nil, "", ownerID, false, err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if tenantErr != nil {
		return nil, "", ownerID, false, tenantErr
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return nil, "", ownerID, false, httpx.BadRequest(e, "Stack ID is required", nil)
	}
	if !h.canonicalReadStoresReady() || tenantID == "" {
		return nil, tenantID, ownerID, false, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Drift store unavailable", nil)
	}
	stack, err := h.stores.Stacks.GetStack(e.Request.Context(), tenantID, stackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && stack.OwnerSubjectID != ownerID) {
		return nil, tenantID, ownerID, false, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return nil, tenantID, ownerID, false, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to query stack", nil)
	}
	return stack, tenantID, ownerID, true, nil
}

func canonicalDriftStatusResponse(stack *controlplane.Stack, results []controlplane.DriftResult) DriftStatusResponse {
	response := DriftStatusResponse{KitDeploymentID: stack.ID, DriftStatus: stack.DriftStatus}
	if stack.DriftCheckedAt != nil {
		response.CheckedAt = *stack.DriftCheckedAt
	}
	if len(results) == 0 {
		return response
	}
	latest := results[0]
	response.LastResultID = latest.ID
	response.LastJobID = latest.JobID
	response.AffectedCount = len(latest.AffectedResources)
	if response.CheckedAt.IsZero() {
		response.CheckedAt = latest.CreatedAt
	}
	return response
}

func canonicalDriftResultItems(results []controlplane.DriftResult) []DriftResultItem {
	items := make([]DriftResultItem, 0, len(results))
	for _, result := range results {
		items = append(items, DriftResultItem{
			ID: result.ID, KitDeploymentID: result.StackID, StackName: result.StackName, Status: result.Status,
			AffectedCount: len(result.AffectedResources), TriggerType: driftDetailString(result.Details, "trigger_type"),
			CheckedAt: driftResultCheckedAt(result), DurationMs: driftDetailInt64(result.Details, "duration_ms"),
		})
	}
	return items
}

func canonicalDriftDetailsResponse(result controlplane.DriftResult) DriftDetailsResponse {
	response := DriftDetailsResponse{
		KitDeploymentID: result.StackID, Status: result.Status, CheckedAt: driftResultCheckedAt(result),
		DurationMs: driftDetailInt64(result.Details, "duration_ms"), ErrorMessage: driftDetailString(result.Details, "error_message"),
	}
	payload, err := json.Marshal(result.AffectedResources)
	if err == nil {
		_ = json.Unmarshal(payload, &response.AffectedResources)
	}
	payload, err = json.Marshal(result.PlanSummary)
	if err == nil {
		_ = json.Unmarshal(payload, &response.Summary)
	}
	return response
}

func driftResultCheckedAt(result controlplane.DriftResult) time.Time {
	if value := driftDetailString(result.Details, "checked_at"); value != "" {
		if checkedAt, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return checkedAt
		}
	}
	return result.CreatedAt
}

func driftDetailString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return strings.TrimSpace(value)
}

func driftDetailInt64(details map[string]any, key string) int64 {
	switch value := details[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}

func (h driftRouteHandlers) decodeTriggerRequest(e *httpx.Event) DriftTriggerRequest {
	var req DriftTriggerRequest
	if e.Request.ContentLength > 0 {
		_ = json.NewDecoder(e.Request.Body).Decode(&req)
	}
	return req
}

func recentCanonicalDriftCheckRateLimit(e *httpx.Event, stack *controlplane.Stack, force bool) (bool, error) {
	if force {
		return false, nil
	}
	if stack.DriftCheckedAt == nil || time.Since(*stack.DriftCheckedAt) >= 5*time.Minute {
		return false, nil
	}
	return true, httpx.Error(e, http.StatusTooManyRequests, "RATE_LIMITED",
		"Drift check recently run. Wait or use force=true.", nil)
}

func driftCheckAllowedForStack(stack *controlplane.Stack) bool {
	return stack.Status == "running" || stack.Status == "provisioning"
}
