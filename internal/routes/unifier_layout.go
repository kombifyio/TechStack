package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/layoutadvice"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// LayoutRecommendationRouteConfig wires the layout recommendation routes.
type LayoutRecommendationRouteConfig struct {
	Service *layoutadvice.Service
}

// NewLayoutWorkerInventory exposes the Unifier's owner-scoped worker
// inventory (the same facts the wizard recommendation reads) to the layout
// advice service.
func NewLayoutWorkerInventory(workers controlplane.WorkerStore) (layoutadvice.WorkerInventory, error) {
	api, err := NewUnifierAPI(workers)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, tenantID, ownerID string) ([]kscore.Worker, error) {
		inventory, err := api.fetchRecommendationInventory(ctx, tenantID, ownerID)
		return inventory.Workers, err
	}, nil
}

// RegisterLayoutRecommendationRoutes adds the Recommendation/v1 surface for
// layouts. Acceptance happens inside the wizard run that carries it out, so
// the advice and its execution share one revalidation (owner decision D2).
func RegisterLayoutRecommendationRoutes(r *httpx.Router, cfg LayoutRecommendationRouteConfig) {
	h := layoutRecommendationHandlers{service: cfg.Service}
	r.POST("/api/v1/unifier/layout/recommendations", h.preview)
	r.GET("/api/v1/unifier/layout/recommendations", h.list)
	r.GET("/api/v1/unifier/layout/recommendations/{id}", h.get)
	r.POST("/api/v1/unifier/layout/recommendations/{id}/reject", h.reject)
	r.POST("/api/v1/unifier/layout/recommendations/{id}/snooze", h.snooze)
}

type layoutRecommendationHandlers struct {
	service *layoutadvice.Service
}

func (h layoutRecommendationHandlers) scope(e *httpx.Event) (layoutadvice.Scope, error) {
	ownerID, err := requireUnifierAuth(e)
	if err != nil {
		return layoutadvice.Scope{}, err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.unifier.layout")
	if tenantErr != nil {
		return layoutadvice.Scope{}, tenantErr
	}
	if h.service == nil {
		return layoutadvice.Scope{}, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Layout recommendations are unavailable", map[string]any{"reason_code": "layout_recommendations_unavailable", "retryable": true})
	}
	return layoutadvice.Scope{TenantID: tenantID, OwnerID: ownerID}, nil
}

func (h layoutRecommendationHandlers) preview(e *httpx.Event) error {
	scope, err := h.scope(e)
	if err != nil {
		return err
	}
	var selection layoutadvice.Selection
	if err := decodeClosedJSON(e, &selection); err != nil {
		return err
	}
	if selection.Flow != layoutadvice.FlowAddServer && selection.Flow != layoutadvice.FlowAddUseCase {
		return httpx.BadRequest(e, "flow must be add-server or add-use-case")
	}
	if len(selection.Goals) == 0 {
		return httpx.BadRequest(e, "goals are required")
	}
	view, err := h.service.Preview(e.Request.Context(), scope, selection)
	if err != nil {
		return layoutAdviceError(e, err)
	}
	return httpx.Success(e, http.StatusOK, view)
}

func (h layoutRecommendationHandlers) list(e *httpx.Event) error {
	scope, err := h.scope(e)
	if err != nil {
		return err
	}
	stackID := strings.TrimSpace(e.Request.URL.Query().Get("stack_id"))
	if stackID == "" {
		return httpx.BadRequest(e, "stack_id is required")
	}
	views, err := h.service.List(e.Request.Context(), scope, stackID)
	if err != nil {
		return layoutAdviceError(e, err)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"recommendations": views})
}

func (h layoutRecommendationHandlers) get(e *httpx.Event) error {
	scope, err := h.scope(e)
	if err != nil {
		return err
	}
	view, err := h.service.Get(e.Request.Context(), scope, e.Request.PathValue("id"))
	if err != nil {
		return layoutAdviceError(e, err)
	}
	return httpx.Success(e, http.StatusOK, view)
}

func (h layoutRecommendationHandlers) reject(e *httpx.Event) error {
	scope, err := h.scope(e)
	if err != nil {
		return err
	}
	if h.service.Records == nil {
		return layoutAdviceError(e, layoutadvice.ErrUnavailable)
	}
	if _, err := h.service.Records.Reject(e.Request.Context(), scope.TenantID, scope.OwnerID, e.Request.PathValue("id"), "", time.Now().UTC()); err != nil {
		return layoutAdviceError(e, err)
	}
	return h.get(e)
}

func (h layoutRecommendationHandlers) snooze(e *httpx.Event) error {
	scope, err := h.scope(e)
	if err != nil {
		return err
	}
	var body struct {
		Until time.Time `json:"until"`
	}
	if err := decodeClosedJSON(e, &body); err != nil {
		return err
	}
	if h.service.Records == nil {
		return layoutAdviceError(e, layoutadvice.ErrUnavailable)
	}
	if _, err := h.service.Records.Snooze(e.Request.Context(), scope.TenantID, scope.OwnerID, e.Request.PathValue("id"), body.Until, "", time.Now().UTC()); err != nil {
		return layoutAdviceError(e, err)
	}
	return h.get(e)
}

func decodeClosedJSON(e *httpx.Event, target any) error {
	body, tooLarge, err := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if err != nil {
		return httpx.BadRequest(e, "failed to read request")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return httpx.BadRequest(e, "invalid request: "+err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return httpx.BadRequest(e, "request must contain one JSON object")
	}
	return nil
}

func layoutAdviceError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, layoutadvice.ErrStackNotFound), errors.Is(err, actions.ErrRecommendationNotFound):
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Not found", nil)
	case errors.Is(err, layoutadvice.ErrNoSpec):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This homelab has no canonical setup yet", map[string]any{"reason_code": "layout_spec_unavailable", "retryable": false})
	case errors.Is(err, actions.ErrRecommendationState), errors.Is(err, actions.ErrRecommendationDigest):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The recommendation changed; reload it", map[string]any{"reason_code": "layout_recommendation_changed", "retryable": true})
	case errors.Is(err, layoutadvice.ErrUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Layout recommendations are unavailable", map[string]any{"reason_code": "layout_recommendations_unavailable", "retryable": true})
	case errors.Is(err, layoutadvice.ErrSelectionMismatch):
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation, "The selection cannot be distributed", map[string]any{"reason_code": "layout_selection_invalid", "retryable": false})
	default:
		httpx.RecordDependencyFailure(e, "unifier.layout", err)
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation, "The selection cannot be distributed right now", map[string]any{"reason_code": "layout_selection_invalid", "retryable": true})
	}
}
