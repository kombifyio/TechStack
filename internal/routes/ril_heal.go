// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains the RIL self-heal audit and recipe management endpoints.
package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RILHealEventResponse represents a self-heal event in the API response.
type RILHealEventResponse struct {
	ID            string     `json:"id"`
	EventID       string     `json:"event_id"`
	ServerID      string     `json:"server_id"`
	RecipeName    string     `json:"recipe_name"`
	TriggerReason string     `json:"trigger_reason,omitempty"`
	AutoExecuted  bool       `json:"auto_executed"`
	Success       bool       `json:"success"`
	ActionTaken   string     `json:"action_taken,omitempty"`
	RollbackInfo  string     `json:"rollback_info,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	Severity      string     `json:"severity"`
	OccurredAt    *time.Time `json:"occurred_at,omitempty"`
	Created       time.Time  `json:"created"`
}

// RegisterRILHealRoutes exposes the canonical tenant-scoped self-heal audit.
func RegisterRILHealRoutes(r *httpx.Router, store rilHealStore) {
	h := rilHealHandler{store: store}
	r.GET("/api/v1/ril/heal/audit", h.listAudit)
}

type rilHealStore interface {
	ListHealEvents(ctx context.Context, tenantID, serverID string) ([]controlplane.RILHealEvent, error)
}

type rilHealHandler struct {
	store rilHealStore
}

func (h rilHealHandler) listAudit(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.ril.heal.audit.read")
	if tenantErr != nil {
		return tenantErr
	}
	records, err := h.store.ListHealEvents(e.Request.Context(), tenantID, e.Request.URL.Query().Get("server_id"))
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_audit_failed", "Failed to list heal events", nil)
	}

	events := make([]RILHealEventResponse, 0, len(records))
	for _, record := range records {
		events = append(events, healEventResponse(record))
	}

	pagination := ksapi.ParsePagination(e.Request)
	total := len(events)
	paged := ksapi.Paginate(events, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, paged, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func healEventResponse(event controlplane.RILHealEvent) RILHealEventResponse {
	occurredAt := event.CreatedAt
	if raw, ok := event.Details["occurred_at"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			occurredAt = parsed
		}
	}
	return RILHealEventResponse{
		ID: event.ID, EventID: event.ID, ServerID: event.ServerID,
		RecipeName: stringFromAnyMap(event.Details, "recipe_name"), TriggerReason: event.Cause,
		AutoExecuted: boolFromDetails(event.Details, "auto_executed"), Success: event.Status == "succeeded",
		ActionTaken: stringFromAnyMap(event.Details, "action_taken"), RollbackInfo: stringFromAnyMap(event.Details, "rollback_info"),
		ErrorMessage: stringFromAnyMap(event.Details, "error_message"), Severity: stringFromAnyMap(event.Details, "severity"),
		OccurredAt: &occurredAt, Created: event.CreatedAt,
	}
}

func boolFromDetails(details map[string]any, key string) bool {
	value, _ := boolValueFromAny(details[key])
	return value
}
