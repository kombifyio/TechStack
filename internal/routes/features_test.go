package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestFeatureFlagsRouteReturnsRoleAwareCategories(t *testing.T) {
	service, err := features.NewService(nil, features.ServiceConfig{})
	if err != nil {
		t.Fatalf("create feature service: %v", err)
	}
	t.Cleanup(service.Shutdown)

	router := httpx.NewRouter()
	RegisterFeatureFlagRoutes(router, service)
	list := func(roles ...string) FeatureFlagsResponse {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
		request = request.WithContext(identity.NewContext(t.Context(), &identity.Identity{UserID: "operator", Roles: roles}))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("feature listing status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Data FeatureFlagsResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode feature listing: %v", err)
		}
		return payload.Data
	}
	find := func(states []features.FlagState, key string) features.FlagState {
		t.Helper()
		for _, state := range states {
			if state.Key == key {
				return state
			}
		}
		t.Fatalf("feature %q missing from its public category: %+v", key, states)
		return features.FlagState{}
	}

	operator := list()
	for key, states := range map[string][]features.FlagState{
		"network_discovery": operator.Security,
		"monthly_runtime":   operator.Beta,
		"dark_mode":         operator.UX,
	} {
		find(states, key)
	}
	for _, state := range operator.Security {
		if state.Enabled || !state.RequiresConsent {
			t.Fatalf("security feature must remain disabled pending consent: %+v", state)
		}
	}
	for _, states := range [][]features.FlagState{operator.Security, operator.Beta, operator.UX} {
		for _, state := range states {
			if state.Locked && state.RiskLevel == features.RiskLevelHigh && !state.RequiresConsent {
				t.Fatalf("admin-only high-risk feature must require consent: %+v", state)
			}
		}
	}
	if !find(operator.Security, "raw_commands").Locked {
		t.Fatal("operator listing must lock admin-only raw commands")
	}
	if find(list("admin").Security, "raw_commands").Locked {
		t.Fatal("admin listing must unlock raw commands")
	}
}
