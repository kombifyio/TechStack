package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestWizardRecommendationAuthorityUsesCanonicalResolver(t *testing.T) {
	recorder := wizardRecommendationRequest(t, controlplane.NewMemoryStore(), `{"goals":["photos"],"services":[],"deployment_lane":"saas","provider_id":"centron","surface":"easy"}`)
	response := wizardRecommendationData(t, recorder)

	if response.Status != core.WizardRecommendationReady {
		t.Fatalf("status = %q, want ready", response.Status)
	}
	if response.DecisionContextHash == "" || response.CatalogSource == "" {
		t.Fatalf("response missing decision provenance: %#v", response)
	}
	if recommendation := wizardRecommendationByID(response, "stackkit:cloud-kit"); recommendation == nil || !recommendation.Recommended {
		t.Fatalf("canonical resolver did not recommend cloud-kit: %#v", response.Recommendations)
	}
}

func TestWizardRecommendationMissingInputsIsIncomplete(t *testing.T) {
	recorder := wizardRecommendationRequest(t, controlplane.NewMemoryStore(), `{"goals":[],"services":[],"deployment_lane":"self-hosted","surface":"easy"}`)
	response := wizardRecommendationData(t, recorder)

	if response.Status != core.WizardRecommendationIncomplete {
		t.Fatalf("status = %q, want incomplete", response.Status)
	}
	if !containsRecommendationValue(response.MissingInputs, "goals") || !containsRecommendationValue(response.MissingInputs, "services") {
		t.Fatalf("missing inputs = %#v, want goals and services", response.MissingInputs)
	}
	for _, recommendation := range response.Recommendations {
		t.Fatalf("incomplete request unexpectedly produced recommendation: %#v", recommendation)
	}
}

func TestWizardRecommendationStaleInventoryIsExplicit(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stale := time.Now().UTC().Add(-time.Hour)
	if _, err := store.UpsertWorkerHeartbeat(context.Background(), controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Provider: "local", Status: "offline", LastSeenAt: &stale,
	}); err != nil {
		t.Fatal(err)
	}
	recorder := wizardRecommendationRequest(t, store, `{"goals":["photos"],"services":[],"deployment_lane":"self-hosted","surface":"easy"}`)
	response := wizardRecommendationData(t, recorder)

	if response.Status != core.WizardRecommendationStale {
		t.Fatalf("status = %q, want stale", response.Status)
	}
	if !containsRecommendationValue(response.StaleInputs, "worker_inventory") {
		t.Fatalf("stale inputs = %#v, want worker_inventory", response.StaleInputs)
	}
	if wizardRecommendationByID(response, "stackkit:basement-kit") == nil {
		t.Fatalf("stale evaluation should retain an explainable recommendation: %#v", response.Recommendations)
	}
}

func TestWizardRecommendationsUseExactTenantAndOwnerInventoryScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	for _, worker := range []controlplane.Worker{
		{ID: "mine", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Provider: "local", Status: "online", LastSeenAt: &now},
		{ID: "other-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-2", Provider: "centron", Status: "online", LastSeenAt: &now},
		{ID: "other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1", Provider: "centron", Status: "online", LastSeenAt: &now},
	} {
		if _, err := store.UpsertWorkerHeartbeat(context.Background(), worker); err != nil {
			t.Fatal(err)
		}
	}
	response := wizardRecommendationData(t, wizardRecommendationRequest(t, store, `{"goals":["photos"],"services":[],"deployment_lane":"self-hosted","surface":"easy"}`))
	if response.DecisionContext == nil || response.DecisionContext.Environment == nil {
		t.Fatalf("response omitted server-built environment context: %#v", response)
	}
	seen := map[string]bool{}
	for _, node := range response.DecisionContext.Environment.ComputeNodes {
		seen[node.ID] = true
	}
	if !seen["mine"] || seen["other-owner"] || seen["other-tenant"] {
		t.Fatalf("recommendation leaked inventory across tenant/owner scope: %#v", seen)
	}
}

func TestWizardRecommendationsRejectCallerSuppliedPolicyContext(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(context.Background(), controlplane.Worker{
		ID: "mine", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "online",
	}); err != nil {
		t.Fatal(err)
	}
	recorder := wizardRecommendationRequest(t, store, `{"goals":["photos"],"deployment_lane":"self-hosted","surface":"easy","kit":"cloud-kit","tenant":"attacker","owner":"attacker","decision_context":{}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want closed-request rejection", recorder.Code, recorder.Body.String())
	}
}

func wizardRecommendationRequest(t *testing.T, store controlplane.WorkerStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	if err := RegisterUnifierRoutes(router, store); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/unifier/recommendations", bytes.NewBufferString(body))
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(recorder, request)
	return recorder
}

func wizardRecommendationData(t *testing.T, recorder *httptest.ResponseRecorder) core.WizardRecommendationResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data core.WizardRecommendationResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func wizardRecommendationByID(response core.WizardRecommendationResponse, id string) *core.WizardRecommendation {
	for index := range response.Recommendations {
		if response.Recommendations[index].ID == id {
			return &response.Recommendations[index]
		}
	}
	return nil
}

func containsRecommendationValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
