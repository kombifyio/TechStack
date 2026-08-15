// Package routes provides custom HTTP routes for kombifyTechstack API.
package routes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// PreCheckRequest represents a request to run pre-checks on a worker.
type PreCheckRequest struct {
	WorkerID string                      `json:"worker_id"`
	StackID  string                      `json:"stack_id,omitempty"`
	Checks   []kscore.PreCheckDefinition `json:"checks"`
	Timeout  int                         `json:"timeout_seconds,omitempty"`
}

// PreCheckResultResponse represents a pre-check result in API responses.
type PreCheckResultResponse struct {
	ID          string         `json:"id"`
	WorkerID    string         `json:"worker_id"`
	StackID     string         `json:"stack_id,omitempty"`
	CheckType   string         `json:"check_type"`
	Description string         `json:"description,omitempty"`
	Blocking    bool           `json:"blocking"`
	Status      string         `json:"status"`
	Message     string         `json:"message,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
	Error       string         `json:"error,omitempty"`
	ExecutedAt  string         `json:"executed_at,omitempty"`
	DurationMS  int            `json:"duration_ms,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
}

const (
	preCheckResultsCollection = "precheck_results"
	preCheckWorkerIDField     = "worker_id"
	preCheckStackIDField      = "stack_id"
	preCheckTypeField         = "check_type"
	preCheckDescriptionField  = "description"
	preCheckBlockingField     = "blocking"
	preCheckStatusField       = "status"
	routeMessageField         = "message"
	preCheckDetailsField      = "details"
	routeErrorField           = "error"
	routeSuccessField         = "success"
	preCheckExecutedAtField   = "executed_at"
	preCheckDurationMSField   = "duration_ms"
	preCheckRequestIDField    = "request_id"
	preCheckOwnerIDField      = "owner_id"
	preCheckStatusPending     = "pending"
	preCheckStatusFailed      = "failed"
	preCheckWorkerIDParam     = "workerId"
	preCheckOwnerIDParam      = "ownerId"
	preCheckRequestIDParam    = "requestId"
	preCheckResultsKey        = "results"
)

// RegisterPreCheckRoutes adds pre-check API endpoints.
// H6a/H6b: Pre-Check Execution and Results Collection
func RegisterPreCheckRoutes(r *httpx.Router, app core.App, grpcServer *grpcserver.Server) {
	handlers := preCheckRouteHandlers{app: app}
	r.POST("/api/v1/workers/{id}/prechecks", handlers.queueWorkerPreChecks)
	r.GET("/api/v1/workers/{id}/prechecks", handlers.listWorkerPreChecks)
	r.GET("/api/v1/prechecks/request/{id}", handlers.getRequestPreChecks)
	r.POST("/api/v1/prechecks/{id}/result", handlers.updatePreCheckResult)
}

type preCheckRouteHandlers struct {
	app core.App
}

func (h preCheckRouteHandlers) queueWorkerPreChecks(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	workerID, ok, err := h.requireOwnedWorkerID(e, ownerID)
	if !ok || err != nil {
		return err
	}

	var req PreCheckRequest
	if decodeErr := json.NewDecoder(e.Request.Body).Decode(&req); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}

	collection, err := h.app.FindCollectionByNameOrId(preCheckResultsCollection)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "precheck_results collection not found", nil)
	}

	requestID := generateRequestID()
	results := h.createPendingPreCheckResults(collection, ownerID, workerID, req.StackID, requestID, preCheckRequestChecks(req))
	return httpx.SuccessWithMeta(e, http.StatusAccepted, map[string]any{
		preCheckRequestIDField: requestID,
		preCheckWorkerIDField:  workerID,
		preCheckResultsKey:     results,
		routeMessageField:      "Pre-checks queued for execution",
	}, nil)
}

func (h preCheckRouteHandlers) listWorkerPreChecks(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	workerID, ok, err := h.requireOwnedWorkerID(e, ownerID)
	if !ok || err != nil {
		return err
	}

	records, err := h.app.FindRecordsByFilter(
		preCheckResultsCollection,
		"worker_id = {:workerId} && owner_id = {:ownerId}",
		"-created",
		100, 0,
		map[string]any{preCheckWorkerIDParam: workerID, preCheckOwnerIDParam: ownerID},
	)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch pre-check results", nil)
	}

	results := preCheckResultResponses(records)
	return httpx.SuccessWithMeta(e, http.StatusOK, results, &ksapi.ResponseMeta{Total: len(results)})
}

func (h preCheckRouteHandlers) getRequestPreChecks(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}

	requestID := e.Request.PathValue("id")
	if requestID == "" {
		return httpx.BadRequest(e, "Request ID is required", nil)
	}

	records, err := h.app.FindRecordsByFilter(
		preCheckResultsCollection,
		"request_id = {:requestId} && owner_id = {:ownerId}",
		"created",
		100, 0,
		map[string]any{preCheckRequestIDParam: requestID, preCheckOwnerIDParam: ownerID},
	)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch pre-check results", nil)
	}
	if len(records) == 0 {
		return httpx.NotFound(e, "Pre-check request not found")
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		preCheckRequestIDField: requestID,
		preCheckResultsKey:     preCheckResultResponses(records),
		"all_blocking_passed":  allBlockingPreChecksPassed(records),
	})
}

func (h preCheckRouteHandlers) updatePreCheckResult(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}

	resultID := e.Request.PathValue("id")
	if resultID == "" {
		return httpx.BadRequest(e, "Result ID is required", nil)
	}

	record, err := h.app.FindRecordById(preCheckResultsCollection, resultID)
	if err != nil {
		return httpx.NotFound(e, "Pre-check result not found")
	}
	if record.GetString(preCheckOwnerIDField) != ownerID {
		return httpx.Forbidden(e, "Not allowed")
	}

	var update preCheckResultUpdate
	if err := json.NewDecoder(e.Request.Body).Decode(&update); err != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	applyPreCheckResultUpdate(record, update)

	if err := h.app.Save(record); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update pre-check result", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"id":                record.Id,
		preCheckStatusField: update.Status,
		routeMessageField:   "Pre-check result updated",
	})
}

func (h preCheckRouteHandlers) requireOwnedWorkerID(e *httpx.Event, ownerID string) (string, bool, error) {
	workerID := e.Request.PathValue("id")
	if workerID == "" {
		return "", false, httpx.BadRequest(e, "Worker ID is required", nil)
	}

	worker, err := h.app.FindRecordById("workers", workerID)
	if err != nil {
		return "", false, httpx.NotFound(e, "Worker not found")
	}
	if worker.GetString(preCheckOwnerIDField) != ownerID {
		return "", false, httpx.Forbidden(e, "Not allowed")
	}
	return workerID, true, nil
}

func (h preCheckRouteHandlers) createPendingPreCheckResults(collection *core.Collection, ownerID, workerID, stackID, requestID string, checks []kscore.PreCheckDefinition) []PreCheckResultResponse {
	results := make([]PreCheckResultResponse, 0, len(checks))
	for _, check := range checks {
		record := core.NewRecord(collection)
		applyPendingPreCheckRecord(record, ownerID, workerID, stackID, requestID, check)
		if err := h.app.Save(record); err != nil {
			continue
		}
		results = append(results, preCheckResultResponseFromRecord(record))
	}
	return results
}

type preCheckResultUpdate struct {
	Status     string         `json:"status"`
	Message    string         `json:"message,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	Error      string         `json:"error,omitempty"`
	DurationMS int            `json:"duration_ms,omitempty"`
}

func preCheckRequestChecks(req PreCheckRequest) []kscore.PreCheckDefinition {
	if len(req.Checks) > 0 {
		return req.Checks
	}
	return getDefaultPreChecks()
}

func applyPendingPreCheckRecord(record *core.Record, ownerID, workerID, stackID, requestID string, check kscore.PreCheckDefinition) {
	record.Set(preCheckWorkerIDField, workerID)
	record.Set(preCheckStackIDField, stackID)
	record.Set(preCheckTypeField, check.Type)
	record.Set(preCheckDescriptionField, check.Description)
	record.Set(preCheckBlockingField, check.Blocking)
	record.Set(preCheckStatusField, preCheckStatusPending)
	record.Set(preCheckRequestIDField, requestID)
	record.Set(preCheckOwnerIDField, ownerID)
}

func applyPreCheckResultUpdate(record *core.Record, update preCheckResultUpdate) {
	record.Set(preCheckStatusField, update.Status)
	record.Set(routeMessageField, update.Message)
	record.Set(preCheckDetailsField, update.Details)
	record.Set(routeErrorField, update.Error)
	record.Set(preCheckDurationMSField, update.DurationMS)
	record.Set(preCheckExecutedAtField, time.Now())
}

func preCheckResultResponses(records []*core.Record) []PreCheckResultResponse {
	results := make([]PreCheckResultResponse, 0, len(records))
	for _, record := range records {
		results = append(results, preCheckResultResponseFromRecord(record))
	}
	return results
}

func preCheckResultResponseFromRecord(record *core.Record) PreCheckResultResponse {
	return PreCheckResultResponse{
		ID:          record.Id,
		WorkerID:    record.GetString(preCheckWorkerIDField),
		StackID:     record.GetString(preCheckStackIDField),
		CheckType:   record.GetString(preCheckTypeField),
		Description: record.GetString(preCheckDescriptionField),
		Blocking:    record.GetBool(preCheckBlockingField),
		Status:      record.GetString(preCheckStatusField),
		Message:     record.GetString(routeMessageField),
		Details:     preCheckDetails(record),
		Error:       record.GetString(routeErrorField),
		ExecutedAt:  record.GetDateTime(preCheckExecutedAtField).String(),
		DurationMS:  int(record.GetFloat(preCheckDurationMSField)),
		RequestID:   record.GetString(preCheckRequestIDField),
	}
}

func preCheckDetails(record *core.Record) map[string]any {
	switch details := record.Get(preCheckDetailsField).(type) {
	case map[string]any:
		return details
	case types.JSONRaw:
		var parsed map[string]any
		if err := json.Unmarshal(details, &parsed); err == nil {
			return parsed
		}
	}
	return nil
}

func allBlockingPreChecksPassed(records []*core.Record) bool {
	for _, record := range records {
		if record.GetBool(preCheckBlockingField) && record.GetString(preCheckStatusField) == preCheckStatusFailed {
			return false
		}
	}
	return true
}

// getDefaultPreChecks returns the default pre-checks for a worker.
func getDefaultPreChecks() []kscore.PreCheckDefinition {
	return []kscore.PreCheckDefinition{
		{
			Type:        "docker_version",
			Description: "Check Docker is installed and meets minimum version",
			MinVersion:  "20.10.0",
			Blocking:    true,
		},
		{
			Type:        "disk_space",
			Description: "Check sufficient disk space is available",
			Blocking:    true,
		},
		{
			Type:        "ram_available",
			Description: "Check sufficient RAM is available",
			Blocking:    true,
		},
		{
			Type:        "port_available",
			Description: "Check required ports are available",
			Blocking:    false,
		},
	}
}

// generateRequestID creates a unique request ID for pre-check batches.
func generateRequestID() string {
	return "precheck-" + time.Now().Format("20060102-150405") + "-" + randomHex(4)
}

// randomHex generates a random hex string of the specified byte length.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
