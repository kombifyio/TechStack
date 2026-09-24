package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handleRecommendations(e *httpx.Event) error {
	ownerID, err := requireUnifierAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.unifier.recommendations")
	if tenantErr != nil {
		return tenantErr
	}
	body, tooLarge, readErr := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if readErr != nil {
		return httpx.BadRequest(e, "failed to read recommendation request")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return httpx.BadRequest(e, "recommendation request cannot be empty")
	}

	var envelope struct {
		kscore.WizardRecommendationRequest
		SmartHomeContext  specv2.SmartHomeContext `json:"smart_home_context,omitempty"`
		SmartHomeSettings map[string]any          `json:"smart_home_settings,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&envelope); decodeErr != nil {
		return httpx.BadRequest(e, "invalid recommendation request: "+decodeErr.Error())
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); decodeErr != io.EOF {
		if decodeErr == nil {
			return httpx.BadRequest(e, "recommendation request must contain one JSON object")
		}
		return httpx.BadRequest(e, "invalid recommendation request: "+decodeErr.Error())
	}
	request := envelope.WizardRecommendationRequest
	if validationErr := validateWizardRecommendationRequest(request); validationErr != "" {
		return httpx.BadRequest(e, validationErr)
	}

	inventory, inventoryErr := api.fetchRecommendationInventory(e.Request.Context(), tenantID, ownerID)
	if inventoryErr != nil {
		// The recommendation remains a useful answer when registry evidence is
		// unavailable, but the authority must expose the degraded state instead
		// of silently treating an empty list as a healthy inventory.
		inventory.State = "unavailable"
	}
	authority := api.recommendations
	if authority == nil {
		var catalog unifier.StackKitCatalog
		if api.engine != nil {
			catalog = api.engine
		}
		authority = unifier.NewWizardRecommendationAuthority(catalog)
	}
	response := authority.Recommend(unifier.WizardRecommendationEvaluation{
		Request:   request,
		TenantID:  tenantID,
		OwnerID:   ownerID,
		Inventory: inventory,
	})
	var smartHome *specv2.SmartHomeRecommendation
	for _, goal := range request.Goals {
		if goal == "smart-home" {
			value := specv2.RecommendSmartHome(envelope.SmartHomeContext, envelope.SmartHomeSettings)
			smartHome = &value
			break
		}
	}
	return httpx.Success(e, http.StatusOK, struct {
		kscore.WizardRecommendationResponse
		SmartHome *specv2.SmartHomeRecommendation `json:"smart_home_recommendation,omitempty"`
	}{response, smartHome})
}

func validateWizardRecommendationRequest(request kscore.WizardRecommendationRequest) string {
	if lane := strings.ToLower(strings.TrimSpace(request.DeploymentLane)); lane != "saas" && lane != "self-hosted" {
		return "deployment_lane must be saas or self-hosted"
	}
	if surface := strings.ToLower(strings.TrimSpace(request.Surface)); surface != "easy" && surface != "techie" {
		return "surface must be easy or techie"
	}
	return ""
}
