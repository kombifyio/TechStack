package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/pkg/specv2"
)

type fakeWizardFeatures struct {
	enabled bool
	err     error
}

func (f fakeWizardFeatures) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, f.err
}

type fakeSeedSource struct {
	seed map[string]any
	err  error
}

func (f fakeSeedSource) Seed(string) (map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.seed, nil
}

type fakeValidator struct {
	err error
}

func (f fakeValidator) ValidateSpec(context.Context, map[string]any) error {
	return f.err
}

func wizardTestSeed() map[string]any {
	return map[string]any{
		"apiVersion": "stackkit/v2alpha1",
		"kind":       "StackSpec",
		"kit":        map[string]any{"slug": "basement-kit"},
		"metadata":   map[string]any{"name": "seed"},
		"sites": []any{
			map[string]any{"id": "home", "kind": "home"},
		},
		"nodes": []any{
			map[string]any{"id": "main", "siteRef": "home", "roles": []any{"controller", "worker"}},
		},
	}
}

func wizardPreviewBody(mode string) map[string]any {
	intent := map[string]any{
		"schema":   specv2.WizardIntentSchema,
		"run_kind": specv2.RunKindFirstRun,
		"name":     "My Homelab",
		"goals":    []string{"photos", "smart-home"},
		"kit_assignment": map[string]any{
			"mode":     mode,
			"kit_slug": "basement-kit",
		},
	}
	if mode == specv2.KitAssignmentJoin {
		intent["run_kind"] = specv2.RunKindExpansion
		intent["kit_assignment"] = map[string]any{
			"mode":              mode,
			"kit_deployment_id": "stack-1",
		}
	}
	return map[string]any{"intent": intent}
}

func wizardPreviewHandlers(cfg WizardRouteConfig) wizardRouteHandlers {
	return wizardRouteHandlers{cfg: cfg}
}

func TestWizardPreviewRequiresAuth(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{Features: fakeWizardFeatures{enabled: true}})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "", "", wizardPreviewBody(specv2.KitAssignmentFound))
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestWizardPreviewFailsClosedWhenFlagDisabled(t *testing.T) {
	cases := map[string]WizardRouteConfig{
		"flag off":     {Features: fakeWizardFeatures{enabled: false}},
		"checker nil":  {},
		"checker errs": {Features: fakeWizardFeatures{enabled: true, err: errors.New("store down")}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			h := wizardPreviewHandlers(cfg)
			event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", wizardPreviewBody(specv2.KitAssignmentFound))
			if err := h.preview(event); err != nil {
				t.Fatalf("preview: %v", err)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			details := envelope.Error.Details
			if details["reason_code"] != "feature_not_enabled" {
				t.Fatalf("details = %#v, want reason_code feature_not_enabled", details)
			}
			if _, ok := details["user_guidance"]; !ok {
				t.Fatalf("details = %#v, want user_guidance", details)
			}
		})
	}
}

func TestWizardPreviewFoundProjectsAndValidates(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features:       fakeWizardFeatures{enabled: true},
		Seeds:          fakeSeedSource{seed: wizardTestSeed()},
		Validator:      fakeValidator{},
		ReleaseVersion: "v0.9.0",
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", wizardPreviewBody(specv2.KitAssignmentFound))
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := envelope.Data
	if data["valid"] != true || data["kit_slug"] != "basement-kit" || data["node_id"] != "main" {
		t.Fatalf("data = %#v", data)
	}
	if data["release_version"] != "v0.9.0" {
		t.Fatalf("release_version = %v", data["release_version"])
	}
	spec := data["spec"].(map[string]any)
	if spec["metadata"].(map[string]any)["name"] != "my-homelab" {
		t.Fatalf("projected metadata = %#v, want contract-id name my-homelab", spec["metadata"])
	}
	unmapped := data["unmapped_goals"].([]any)
	if len(unmapped) != 1 || unmapped[0] != "smart-home" {
		t.Fatalf("unmapped_goals = %#v", unmapped)
	}
}

func TestWizardPreviewReturnsStructuredInvalidResult(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features:  fakeWizardFeatures{enabled: true},
		Seeds:     fakeSeedSource{seed: wizardTestSeed()},
		Validator: fakeValidator{err: errors.New("CUE #KitSpecBinding rejected definition/spec")},
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", wizardPreviewBody(specv2.KitAssignmentFound))
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (invalid is data, not HTTP failure)", rec.Code)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Data["valid"] != false {
		t.Fatalf("valid = %v, want false", envelope.Data["valid"])
	}
	if envelope.Data["validate_error"] == "" {
		t.Fatalf("validate_error missing: %#v", envelope.Data)
	}
}

func TestWizardPreviewFailsClosedWithoutValidator(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features: fakeWizardFeatures{enabled: true},
		Seeds:    fakeSeedSource{seed: wizardTestSeed()},
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", wizardPreviewBody(specv2.KitAssignmentFound))
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestWizardPreviewJoinRequiresBaseSpec(t *testing.T) {
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features:  fakeWizardFeatures{enabled: true},
		Validator: fakeValidator{},
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", wizardPreviewBody(specv2.KitAssignmentJoin))
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestWizardPreviewJoinAppendsNodeToBaseSpec(t *testing.T) {
	body := wizardPreviewBody(specv2.KitAssignmentJoin)
	body["base_spec"] = wizardTestSeed()
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features:  fakeWizardFeatures{enabled: true},
		Validator: fakeValidator{},
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", body)
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Data["node_id"] != "worker-1" {
		t.Fatalf("node_id = %v, want worker-1", envelope.Data["node_id"])
	}
}

func TestWizardPreviewRejectsInvalidIntent(t *testing.T) {
	body := wizardPreviewBody(specv2.KitAssignmentFound)
	body["intent"].(map[string]any)["kit_assignment"].(map[string]any)["kit_slug"] = "ha-kit"
	h := wizardPreviewHandlers(WizardRouteConfig{
		Features:  fakeWizardFeatures{enabled: true},
		Seeds:     fakeSeedSource{seed: wizardTestSeed()},
		Validator: fakeValidator{},
	})
	event, rec := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/wizard/preview", "auth0|user-1", "tenant-1", body)
	if err := h.preview(event); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
