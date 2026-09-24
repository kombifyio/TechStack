package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/internal/onboarding"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

const (
	onboardingTestTenant  = "tenant-onboarding"
	onboardingTestOwner   = "auth0|onboarding-user"
	onboardingTestJourney = "techstack.platform"
)

type onboardingTestFeatures struct{ enabled bool }

func (f onboardingTestFeatures) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, nil
}

func newOnboardingTestHandlers(store *controlplane.MemoryStore, mode config.DeploymentMode) onboardingHandlers {
	return onboardingHandlers{
		service: onboarding.Service{
			Store:          store,
			Signals:        onboarding.SignalSources{Homelabs: store, KitDeployments: store, Servers: store, Services: store},
			DeploymentMode: mode,
			Features:       onboardingTestFeatures{enabled: true},
		},
		defaultTenantID: onboardingTestTenant,
		available:       true,
	}
}

func onboardingTestEvent(t *testing.T, method, path string, payload any, ifMatch string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, body)
	req.SetPathValue("journeyId", onboardingTestJourney)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: onboardingTestOwner,
		OrgID:  onboardingTestTenant,
	}))
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func decodeOnboardingSuccess(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Data
}

func decodeOnboardingError(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Error.Details
}

func onboardingRow(t *testing.T, store *controlplane.MemoryStore) *controlplane.OnboardingState {
	t.Helper()
	row, err := store.GetOnboardingState(context.Background(), onboardingTestTenant, onboardingTestOwner, "techstack", onboardingTestJourney)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("read row: %v", err)
	}
	return row
}

// The client resolves the journey itself, so GET must hand it the descriptor
// and the state, not a rendered checklist.
func TestGetOnboardingReturnsDescriptorStateAndAvailability(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSelfHosted)
	e, rec := onboardingTestEvent(t, http.MethodGet, "/api/v1/onboarding/"+onboardingTestJourney, nil, "")

	if err := h.getJourney(e); err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data := decodeOnboardingSuccess(t, rec)
	journey, _ := data["journey"].(map[string]any)
	if journey["journey_id"] != onboardingTestJourney {
		t.Fatalf("descriptor missing from the payload: %v", data["journey"])
	}
	if _, ok := data["availability"]; !ok {
		t.Fatal("availability must always ride along")
	}
	if onboardingRow(t, store) != nil {
		t.Fatal("a read must not found a row")
	}
}

func TestSkipPersistsAndBumpsTheRevision(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSelfHosted)
	e, rec := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "skip", "step_id": "techstack.deploy_service"}, "")

	if err := h.applyAction(e); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if revision, _ := decodeOnboardingSuccess(t, rec)["revision"].(float64); revision != 1 {
		t.Fatalf("revision = %v, want 1", revision)
	}
	row := onboardingRow(t, store)
	if row == nil || row.Revision != 1 {
		t.Fatalf("skip was not persisted: %+v", row)
	}
}

// The entitlement boundary: a client may not report itself through a step that
// spends money, and the refusal must leave nothing behind.
func TestReportedCompletionOfACostBearingStepIsForbidden(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSaaS)
	e, rec := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "complete", "step_id": "techstack.deploy_stackkit", "source": "reported"}, "")

	if err := h.applyAction(e); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body = %s)", rec.Code, rec.Body.String())
	}
	details := decodeOnboardingError(t, rec)
	if details["reason_code"] != onboardingErrorDerivedOnly {
		t.Fatalf("reason_code = %v", details["reason_code"])
	}
	if details["user_guidance"] == nil {
		t.Fatal("a refusal must explain itself")
	}
	if onboardingRow(t, store) != nil {
		t.Fatal("a refused action left a row behind")
	}
}

// Two tabs must not silently overwrite each other.
func TestStaleIfMatchIsAPreconditionFailure(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSelfHosted)

	first, _ := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "skip", "step_id": "techstack.deploy_service"}, "")
	if err := h.applyAction(first); err != nil {
		t.Fatalf("first action: %v", err)
	}

	stale, rec := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "dismiss"}, `"0"`)
	if err := h.applyAction(stale); err != nil {
		t.Fatalf("second action: %v", err)
	}
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412 (body = %s)", rec.Code, rec.Body.String())
	}
	details := decodeOnboardingError(t, rec)
	if details["retryable"] != true {
		t.Fatalf("a conflict is retryable: %v", details)
	}
}

// An action endpoint that accepted arbitrary fields would be a state-blob
// upload with extra steps.
func TestUnknownFieldsAreRejected(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSelfHosted)
	e, rec := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "skip", "step_id": "techstack.deploy_service", "completed": []any{}}, "")

	if err := h.applyAction(e); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body = %s)", rec.Code, rec.Body.String())
	}
}

func TestResetClearsCompletions(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newOnboardingTestHandlers(store, config.ModeSelfHosted)

	skip, _ := onboardingTestEvent(t, http.MethodPut, "/api/v1/onboarding/"+onboardingTestJourney,
		map[string]any{"action": "skip", "step_id": "techstack.deploy_service"}, "")
	if err := h.applyAction(skip); err != nil {
		t.Fatalf("skip: %v", err)
	}

	e, rec := onboardingTestEvent(t, http.MethodPost, "/api/v1/onboarding/"+onboardingTestJourney+"/reset", nil, "")
	if err := h.resetJourney(e); err != nil {
		t.Fatalf("reset: %v", err)
	}
	state, _ := decodeOnboardingSuccess(t, rec)["state"].(map[string]any)
	completed, _ := state["completed"].([]any)
	if len(completed) != 0 {
		t.Fatalf("reset left completions behind: %v", completed)
	}
}

// Without a control-plane store there is nowhere to persist a journey; saying
// so beats serving an empty checklist that silently forgets every tick.
func TestOnboardingFailsClosedWithoutTheControlPlane(t *testing.T) {
	h := newOnboardingTestHandlers(controlplane.NewMemoryStore(), config.ModeSelfHosted)
	h.available = false
	e, rec := onboardingTestEvent(t, http.MethodGet, "/api/v1/onboarding/"+onboardingTestJourney, nil, "")

	_ = h.getJourney(e)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body = %s)", rec.Code, rec.Body.String())
	}
	if decodeOnboardingError(t, rec)["reason_code"] != onboardingErrorNoControlPlane {
		t.Fatalf("wrong reason: %s", rec.Body.String())
	}
}
