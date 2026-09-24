package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const providerResolutionBodyLimit = 2048

type providerResolutionApplication interface {
	LoadProvisionResolutionOperation(context.Context, string, string) (providercontrol.OperationRecord, error)
	DiscoverProvisionOperation(context.Context, providercontrol.ProvisionDiscoveryWorkflowRequest) (providercontrol.ProvisionDiscoveryWorkflowResult, error)
	CommitProvisionOperation(context.Context, providercontrol.ProvisionResolutionWorkflowRequest) (providercontrol.ProvisionResolutionWorkflowResult, error)
}

// ProviderResolutionRouteConfig composes the native provider-control runtime
// with the same signed-entitlement plus FGA policy used by server operations.
type ProviderResolutionRouteConfig struct {
	Application providerResolutionApplication
	Policy      InventoryPolicy
}

type providerResolutionHandlers struct {
	application providerResolutionApplication
	policy      InventoryPolicy
}

type providerResolutionConfirmRequest struct {
	Confirmation string `json:"confirmation"`
}

// RegisterProviderResolutionRoutes exposes the two-step hosted Operator
// Reconcile application. Neither request accepts provider handles, candidate
// counts, outcomes, revisions, or replay identities.
func RegisterProviderResolutionRoutes(r *httpx.Router, cfg ProviderResolutionRouteConfig) {
	if r == nil {
		return
	}
	h := providerResolutionHandlers{application: cfg.Application, policy: cfg.Policy}
	r.POST("/api/v1/provider-operations/{operationId}/provision-discovery", h.discover)
	r.POST("/api/v1/provider-operations/{operationId}/provision-resolution", h.resolve)
}

func (h providerResolutionHandlers) discover(e *httpx.Event) error {
	operatorID, tenantID, operationID, handled, err := h.authorize(e, &struct{}{}, "techstack.provider-operations.discover")
	if handled || err != nil {
		return err
	}
	result, err := h.application.DiscoverProvisionOperation(e.Request.Context(), providercontrol.ProvisionDiscoveryWorkflowRequest{
		TenantID: tenantID, OperationID: operationID, OperatorSubjectID: operatorID,
	})
	if err != nil {
		return writeProviderResolutionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"operation_id": operationID, "observation_id": result.Observation.ObservationID,
		"observation_digest":    result.Observation.SnapshotDigest,
		"resolution_revision":   result.Observation.ObservedResolutionRevision,
		"candidate_count":       len(result.Observation.Candidates),
		"expected_outcome":      providerResolutionOutcomeForCount(len(result.Observation.Candidates)),
		"required_confirmation": "adopt-provision:" + operationID,
		"created":               result.Created,
	})
}

func (h providerResolutionHandlers) resolve(e *httpx.Event) error {
	var request providerResolutionConfirmRequest
	operatorID, tenantID, operationID, handled, err := h.authorize(e, &request, "techstack.provider-operations.resolve")
	if handled || err != nil {
		return err
	}
	result, err := h.application.CommitProvisionOperation(e.Request.Context(), providercontrol.ProvisionResolutionWorkflowRequest{
		TenantID: tenantID, OperationID: operationID, OperatorSubjectID: operatorID,
		Confirmation: strings.TrimSpace(request.Confirmation),
	})
	if err != nil {
		return writeProviderResolutionError(e, err)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"operation_id": operationID, "outcome": result.Decision.Outcome,
		"operation_phase": result.Operation.Head.Phase,
		"resource_count":  len(result.Operation.Head.Resources),
		"decision_digest": result.Decision.DecisionDigest,
		"created":         result.DecisionCreated,
	})
}

func (h providerResolutionHandlers) authorize(e *httpx.Event, body any, capability string) (string, string, string, bool, error) {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return "", "", "", true, err
	}
	operatorID, err := requireAuth(e)
	if err != nil {
		return "", "", "", true, err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), "", capability)
	if tenantErr != nil {
		return "", "", "", true, tenantErr
	}
	operationID := strings.TrimSpace(e.Request.PathValue("operationId"))
	if tenantID == "" || !validProviderResolutionOperationID(operationID) {
		return "", "", "", true, httpx.BadRequest(e, "A canonical tenant and operation ID are required", nil)
	}
	if err := decodeProviderResolutionBody(e, body); err != nil {
		return "", "", "", true, httpx.BadRequest(e, "Invalid operator-resolution request", nil)
	}
	if h.application == nil || h.policy == nil {
		return "", "", "", true, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Provider operator resolution is unavailable", map[string]any{"reason_code": "provider_resolution_unavailable"})
	}
	record, err := h.application.LoadProvisionResolutionOperation(e.Request.Context(), tenantID, operationID)
	if err != nil {
		return "", "", "", true, writeProviderResolutionError(e, err)
	}
	_, err = h.policy.AuthorizeInventory(e.Request.Context(), InventoryAuthorization{
		TenantID: tenantID, SubjectID: operatorID,
		ResourceType: controlplane.InventoryReadTargetServer,
		ResourceID:   record.Command.RuntimeServerID,
		Action:       InventoryActionOperate,
	})
	if errors.Is(err, ErrInventoryAccessDenied) {
		return "", "", "", true, httpx.RejectForbidden(e, "Provider operator resolution access denied")
	}
	if err != nil {
		return "", "", "", true, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Provider operator authorization is unavailable", map[string]any{"reason_code": "provider_resolution_authorization_unavailable"})
	}
	return operatorID, tenantID, operationID, false, nil
}

func decodeProviderResolutionBody(e *httpx.Event, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, providerResolutionBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON object")
	}
	return nil
}

func validProviderResolutionOperationID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func providerResolutionOutcomeForCount(count int) providercontrol.ProvisionResolutionOutcome {
	switch count {
	case 0:
		return providercontrol.ProvisionResolutionNoCandidateObserved
	case 1:
		return providercontrol.ProvisionResolutionAdoptedExactCandidate
	default:
		return providercontrol.ProvisionResolutionMultipleCandidatesQuarantined
	}
}

func writeProviderResolutionError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, providercontrol.ErrOperationNotFound):
		return httpx.NotFound(e, "Provider operation not found")
	case errors.Is(err, providercontrol.ErrProvisionResolutionConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Provider resolution no longer matches the current operation", map[string]any{"reason_code": "provider_resolution_conflict"})
	case errors.Is(err, providercontrol.ErrProvisionResolutionEvidence):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Provider discovery evidence could not be verified", map[string]any{"reason_code": "provider_resolution_evidence_stale"})
	case errors.Is(err, providercontrol.ErrInvalidRequest):
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Provider resolution request is not admissible", map[string]any{"reason_code": "provider_resolution_invalid"})
	default:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Provider operator resolution could not be completed", map[string]any{"reason_code": "provider_resolution_unavailable"})
	}
}
