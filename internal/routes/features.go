// Package routes provides custom HTTP routes for kombifyTechstack API.
package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// featureResponseTypeKey is the shared JSON key for type fields in route
// responses (also used by registry and managed-runtime projections).
const featureResponseTypeKey = "type"

// FeatureFlagsResponse represents the feature flags API response
type FeatureFlagsResponse struct {
	Security []features.FlagState `json:"security"`
	Beta     []features.FlagState `json:"beta"`
	UX       []features.FlagState `json:"ux"`
}

// RegisterFeatureFlagRoutes adds the read-only feature flag API endpoint.
// Flag management is central (Cloudflare Edge/OpenFeature entitlements); the local API only lists
// effective flags for the authenticated user.
func RegisterFeatureFlagRoutes(r *httpx.Router, svc *features.Service) {
	if svc == nil {
		return
	}

	handlers := featureRouteHandlers{svc: svc}
	r.GET("/api/v1/features", handlers.listFeatures)
}

type featureRouteHandlers struct {
	svc *features.Service
}

func (h featureRouteHandlers) listFeatures(e *httpx.Event) error {
	userID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "")
	}

	flagsByCategory, err := h.svc.GetFlagsByCategory(e.Request.Context(), userID, isAdmin)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load features", nil)
	}
	return httpx.Success(e, http.StatusOK, featureFlagsResponse(flagsByCategory))
}

func featureFlagsResponse(flagsByCategory map[features.Category][]features.FlagState) FeatureFlagsResponse {
	return FeatureFlagsResponse{
		Security: flagsByCategory[features.CategorySecurity],
		Beta:     flagsByCategory[features.CategoryBeta],
		UX:       flagsByCategory[features.CategoryUX],
	}
}
