package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
)

type GovernedActionWorkflow interface {
	Execute(context.Context, actions.BeginExecution) (*actions.GovernedCard, error)
}

type GovernedActionRouteConfig struct {
	Authority actions.Authority
	Workflow  GovernedActionWorkflow
	Policy    InventoryPolicy
	Now       func() time.Time
}

type governedActionHandler struct{ config GovernedActionRouteConfig }

func RegisterGovernedActionRoutes(r *httpx.Router, config GovernedActionRouteConfig) {
	if config.Authority == nil || config.Workflow == nil || config.Policy == nil {
		panic("RegisterGovernedActionRoutes: authority, workflow, and inventory policy required")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	h := governedActionHandler{config: config}
	r.POST("/v1/ril/action-cards", h.create)
	r.GET("/v1/ril/action-cards", h.list)
	r.GET("/v1/ril/action-cards/{cardId}", h.get)
	r.POST("/v1/ril/action-cards/{cardId}/approve", h.approve)
	r.POST("/v1/ril/action-cards/{cardId}/deny", h.deny)
	r.POST("/v1/ril/action-cards/{cardId}/execute", h.execute)
}

type createGovernedActionRequest struct {
	ID       string                 `json:"id,omitempty"`
	ServerID string                 `json:"server_id"`
	Title    string                 `json:"title"`
	Severity string                 `json:"severity,omitempty"`
	Action   actions.ActionTemplate `json:"action"`
}

func (h governedActionHandler) create(e *httpx.Event) error {
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	var body createGovernedActionRequest
	if decodeErr := decodeGovernedActionJSON(e, &body); decodeErr != nil {
		return writeGovernedActionError(e, decodeErr)
	}
	card, err := h.config.Authority.Create(e.Request.Context(), actions.CreateGovernedCard{ID: body.ID, TenantID: scope.tenantID, OwnerSubjectID: scope.ownerID, ServerID: body.ServerID, Title: body.Title, Severity: body.Severity, Template: body.Action})
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	return httpx.Success(e, http.StatusCreated, card)
}

func (h governedActionHandler) list(e *httpx.Event) error {
	if len(e.Request.URL.Query()) != 0 {
		return writeGovernedActionError(e, errGovernedActionRequest)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	cards, err := h.config.Authority.List(e.Request.Context(), scope.tenantID, scope.ownerID)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, cards)
}

func (h governedActionHandler) get(e *httpx.Event) error {
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	card, err := h.config.Authority.Get(e.Request.Context(), scope.tenantID, scope.ownerID, e.Request.PathValue("cardId"))
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, card)
}

type actionDecisionRequest struct {
	AuditCorrelationID string `json:"audit_correlation_id"`
}

func (h governedActionHandler) approve(e *httpx.Event) error { return h.decision(e, true) }
func (h governedActionHandler) deny(e *httpx.Event) error    { return h.decision(e, false) }

func (h governedActionHandler) decision(e *httpx.Event, approve bool) error {
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	var body actionDecisionRequest
	if decodeErr := decodeGovernedActionJSON(e, &body); decodeErr != nil {
		return writeGovernedActionError(e, decodeErr)
	}
	var card *actions.GovernedCard
	if approve {
		card, err = h.config.Authority.Approve(e.Request.Context(), scope.tenantID, scope.ownerID, e.Request.PathValue("cardId"), body.AuditCorrelationID, h.config.Now())
	} else {
		card, err = h.config.Authority.Deny(e.Request.Context(), scope.tenantID, scope.ownerID, e.Request.PathValue("cardId"), body.AuditCorrelationID, h.config.Now())
	}
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, card)
}

type executeGovernedActionRequest struct {
	ExecutionID    string `json:"execution_id"`
	TraceID        string `json:"trace_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h governedActionHandler) execute(e *httpx.Event) error {
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	cardID := strings.TrimSpace(e.Request.PathValue("cardId"))
	card, err := h.config.Authority.Get(e.Request.Context(), scope.tenantID, scope.ownerID, cardID)
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	if err := authorizeInventoryServerOperate(e.Request.Context(), h.config.Policy, scope, card.ServerID); err != nil {
		return writeGovernedActionError(e, err)
	}
	var body executeGovernedActionRequest
	if decodeErr := decodeGovernedActionJSON(e, &body); decodeErr != nil {
		return writeGovernedActionError(e, decodeErr)
	}
	card, err = h.config.Workflow.Execute(e.Request.Context(), actions.BeginExecution{TenantID: scope.tenantID, OwnerSubjectID: scope.ownerID, CardID: cardID, ExecutionID: body.ExecutionID, TraceID: body.TraceID, IdempotencyKey: body.IdempotencyKey, Now: h.config.Now(), ConnectorProjection: connectorProjectionFromEvent(e)})
	if err != nil {
		return writeGovernedActionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, card)
}

var errGovernedActionRequest = errors.New("invalid governed action request")

func decodeGovernedActionJSON(e *httpx.Event, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, rilaction.MaxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errGovernedActionRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errGovernedActionRequest
	}
	return nil
}

func writeGovernedActionError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, actions.ErrCardNotFound):
		return httpx.NotFound(e, "Action card not found")
	case errors.Is(err, actions.ErrGrantRequired), errors.Is(err, actions.ErrConnectorBindingRequired), errors.Is(err, actions.ErrConnectorGrantInsufficient):
		return writeGovernedActionEntitlementDenial(e, err)
	case errors.Is(err, actions.ErrApprovalRequired), errors.Is(err, actions.ErrExecutionAdmission), errors.Is(err, actions.ErrCardConflict), errors.Is(err, actions.ErrExecutionInProgress):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Action cannot transition", map[string]any{inventoryReasonCodeField: governedActionReason(err)})
	case errors.Is(err, errGovernedActionRequest):
		return httpx.BadRequest(e, "Invalid governed action request", map[string]any{inventoryReasonCodeField: "invalid_request"})
	default:
		if _, ok := err.(*inventoryError); ok {
			return writeInventoryHTTPError(e, err)
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Governed action unavailable", map[string]any{inventoryReasonCodeField: "governed_action_unavailable"})
	}
}

func writeGovernedActionEntitlementDenial(e *httpx.Event, err error) error {
	reason := governedActionReason(err)
	env := denial.Envelope{
		ErrorCode:        reason,
		ReasonCode:       reason,
		Capability:       "techstack.ril.execute",
		RequiredFeatures: []string{"techstack.ril.execute"},
		MissingFeatures:  []string{"techstack.ril.grant"},
		Retryable:        false,
		UserGuidance: denial.UserGuidance{
			Title: "This RIL action cannot run yet",
			Body:  "Techstack stopped before starting the durable workflow because the required execution grant is missing, expired, or insufficient.",
			NextSteps: []string{
				"Approve the action card with a live delegated grant.",
				"Retry only after the grant and connector binding are present.",
			},
		},
	}
	if errors.Is(err, actions.ErrConnectorBindingRequired) || errors.Is(err, actions.ErrConnectorGrantInsufficient) {
		env.MissingFeatures = []string{"techstack.ril.connector_binding"}
		env.UserGuidance.Title = "This RIL action needs a connector binding"
		env.UserGuidance.Body = "Techstack stopped before starting the durable workflow because the exact connector binding is missing or cannot mutate."
		env.UserGuidance.NextSteps = []string{
			"Bind the action card to an active connector grant.",
			"Retry the execute request through Gateway so the signed connector projection is present.",
		}
	}
	if validateErr := env.Validate(); validateErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Governed action unavailable", map[string]any{inventoryReasonCodeField: "governed_action_unavailable"})
	}
	raw, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Governed action unavailable", map[string]any{inventoryReasonCodeField: "governed_action_unavailable"})
	}
	var payload map[string]any
	if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Governed action unavailable", map[string]any{inventoryReasonCodeField: "governed_action_unavailable"})
	}
	payload["error"] = map[string]any{"code": env.ErrorCode, "message": env.UserGuidance.Body}
	return e.JSON(http.StatusForbidden, payload)
}

func governedActionReason(err error) string {
	switch {
	case errors.Is(err, actions.ErrApprovalRequired):
		return "approval_required"
	case errors.Is(err, actions.ErrGrantRequired):
		return "grant_required"
	case errors.Is(err, actions.ErrConnectorBindingRequired):
		return "connector_binding_required"
	case errors.Is(err, actions.ErrConnectorGrantInsufficient):
		return "connector_grant_insufficient"
	case errors.Is(err, actions.ErrExecutionInProgress):
		return "execution_in_progress"
	case errors.Is(err, actions.ErrExecutionAdmission):
		return "execution_admission_rejected"
	default:
		return "state_conflict"
	}
}

func connectorProjectionFromEvent(e *httpx.Event) *actions.ConnectorBindingProjection {
	if e == nil || e.Request == nil || !middleware.IsEdgeAuthenticated(e.Request.Context()) {
		return nil
	}
	header := e.Request.Header.Get
	grantID := header("X-Kombify-Connector-Grant-ID")
	bindingID := header("X-Kombify-Connector-Binding-ID")
	if grantID == "" && bindingID == "" {
		return nil
	}
	projection := &actions.ConnectorBindingProjection{
		GrantID:      grantID,
		BindingID:    bindingID,
		BindingHash:  header("X-Kombify-Connector-Binding-Hash"),
		ConnectorID:  header("X-Kombify-Connector-ID"),
		BindingScope: header("X-Kombify-Connector-Binding-Scope"),
		ResourceID:   header("X-Kombify-Connector-Resource-ID"),
		ServerID:     header("X-Kombify-Connector-Server-ID"),
		Status:       header("X-Kombify-Connector-Binding-Status"),
	}
	if rawScopes := strings.TrimSpace(header("X-Kombify-Connector-Scopes")); rawScopes != "" {
		projection.Scopes = strings.Split(rawScopes, ",")
	}
	return projection
}
