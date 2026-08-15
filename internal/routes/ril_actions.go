// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains the RIL Action-Card endpoints for proactive intelligence.
package routes

import (
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// RILActionCardResponse represents an action card in the API response.
type RILActionCardResponse struct {
	ID            string               `json:"id"`
	CardID        string               `json:"card_id"`
	ServerID      string               `json:"server_id"`
	Type          string               `json:"type"`
	Severity      string               `json:"severity"`
	Title         string               `json:"title"`
	Description   string               `json:"description,omitempty"`
	Engine        string               `json:"engine,omitempty"`
	Status        string               `json:"status"`
	SuggestedPlan *actions.ActionPlan  `json:"suggested_plan,omitempty"`
	Evidence      any                  `json:"evidence,omitempty"`
	AuditTrail    []actions.AuditEntry `json:"audit_trail,omitempty"`
	DetectedAt    *time.Time           `json:"detected_at,omitempty"`
	ApprovedAt    *time.Time           `json:"approved_at,omitempty"`
	CompletedAt   *time.Time           `json:"completed_at,omitempty"`
	Created       time.Time            `json:"created"`
}

// RegisterRILActionRoutes adds RIL action card API endpoints.
func RegisterRILActionRoutes(r *httpx.Router, store *actions.Store) {
	h := rilActionHandler{store: store}
	r.GET("/api/v1/ril/actions", h.listActions)
	r.GET("/api/v1/ril/actions/{cardId}", h.getAction)
	r.POST("/api/v1/ril/actions/{cardId}/approve", h.approveAction)
	r.POST("/api/v1/ril/actions/{cardId}/dismiss", h.dismissAction)
	r.GET("/api/v1/ril/servers/{serverId}/actions", h.listServerActions)
}

type rilActionHandler struct {
	store *actions.Store
}

func (h rilActionHandler) listActions(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	filters := actions.ListFilters{
		Status:   e.Request.URL.Query().Get("status"),
		Severity: e.Request.URL.Query().Get("severity"),
		ServerID: e.Request.URL.Query().Get("server_id"),
	}

	cards, err := h.store.ListByOwner(ownerID, filters)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_actions_failed", "Failed to list action cards", nil)
	}

	responses := make([]RILActionCardResponse, 0, len(cards))
	for _, c := range cards {
		responses = append(responses, actionCardToResponse(c))
	}

	pagination := ksapi.ParsePagination(e.Request)
	total := len(responses)
	paged := ksapi.Paginate(responses, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, paged, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func (h rilActionHandler) getAction(e *httpx.Event) error {
	if _, err := requireAuth(e); err != nil {
		return err
	}

	cardID := e.Request.PathValue("cardId")
	if cardID == "" {
		return httpx.BadRequest(e, "Card ID is required", nil)
	}

	card, err := h.store.GetByID(cardID)
	if err != nil {
		return httpx.NotFound(e, "Action card not found")
	}

	return httpx.Success(e, http.StatusOK, actionCardToResponse(card))
}

func (h rilActionHandler) approveAction(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	cardID := e.Request.PathValue("cardId")
	if err := h.store.Approve(cardID, ownerID); err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	card, err := h.store.GetByID(cardID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_approve_fetch_failed", "Approved but failed to fetch card", nil)
	}

	return httpx.Success(e, http.StatusOK, actionCardToResponse(card))
}

func (h rilActionHandler) dismissAction(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	cardID := e.Request.PathValue("cardId")
	if err := h.store.Dismiss(cardID, ownerID); err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	card, err := h.store.GetByID(cardID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_dismiss_fetch_failed", "Dismissed but failed to fetch card", nil)
	}

	return httpx.Success(e, http.StatusOK, actionCardToResponse(card))
}

func (h rilActionHandler) listServerActions(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	serverID := e.Request.PathValue("serverId")
	if serverID == "" {
		return httpx.BadRequest(e, "Server ID is required", nil)
	}

	cards, err := h.store.ListByServer(serverID, ownerID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_server_actions_failed", "Failed to list server actions", nil)
	}

	responses := make([]RILActionCardResponse, 0, len(cards))
	for _, c := range cards {
		responses = append(responses, actionCardToResponse(c))
	}

	pagination := ksapi.ParsePagination(e.Request)
	total := len(responses)
	paged := ksapi.Paginate(responses, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, paged, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func actionCardToResponse(c *actions.ActionCard) RILActionCardResponse {
	return RILActionCardResponse{
		ID:            c.ID,
		CardID:        c.CardID,
		ServerID:      c.ServerID,
		Type:          string(c.Type),
		Severity:      string(c.Severity),
		Title:         c.Title,
		Description:   c.Description,
		Engine:        c.Engine,
		Status:        string(c.Status),
		SuggestedPlan: c.SuggestedPlan,
		Evidence:      c.Evidence,
		AuditTrail:    c.AuditTrail,
		DetectedAt:    c.DetectedAt,
		ApprovedAt:    c.ApprovedAt,
		CompletedAt:   c.CompletedAt,
		Created:       c.Created,
	}
}
