package routes

import (
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/specv2"
	"net/http"
	"testing"
)

// Advisory discovery evidence must not grant management of an existing instance.
func TestWizardPreviewExistingSmartHomeRemainsObserved(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{Features: fakeWizardFeatures{enabled: true}, Seeds: fakeSeedSource{seed: wizardTestSeed()}, Projector: fakeWizardProjector(), Validator: fakeValidator{}})
	body := wizardPreviewBody(specv2.KitAssignmentFound)
	body["intent"].(map[string]any)["goals"] = []string{"smart-home"}
	body["smart_home_context"] = map[string]any{"existing": true, "proxmox_available": true, "lan_reachable": true, "cpu": 8, "memory_mib": 16384, "disk_gib": 64}
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", body)
	if err := h.preview(event); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			Recommendation specv2.SmartHomeRecommendation `json:"smart_home_recommendation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || envelope.Data.Recommendation.ManagementScope != "observed" || envelope.Data.Recommendation.OperatingForm != "" {
		t.Fatalf("existing instance not preserved: %s", rec.Body.String())
	}
}
