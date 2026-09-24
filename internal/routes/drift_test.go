package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestDriftStatusUsesCanonicalTenantOwnerScope(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Homelab", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkedAt := time.Now().UTC()
	if _, err := store.UpdateStackRuntime(t.Context(), "tenant-1", stack.ID, controlplane.RuntimeUpdate{Status: "running", DriftStatus: "drifted", DriftCheckedAt: &checkedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDriftResult(t.Context(), controlplane.DriftResult{
		ID: "drift-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: stack.ID,
		Status: "drifted", AffectedResources: []map[string]any{{"address": "server.web"}},
	}); err != nil {
		t.Fatal(err)
	}
	handler := driftRouteHandlers{stores: DriftRouteStores{Stacks: store, Drift: store}}

	event, recorder := canonicalDriftRouteEvent(http.MethodGet, "/api/v1/stacks/stack-1/drift/status", stack.ID, "tenant-1", "owner-1", ``)
	if err := handler.status(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "drift-1") || !strings.Contains(recorder.Body.String(), "drifted") {
		t.Fatalf("canonical status response = %d %s", recorder.Code, recorder.Body.String())
	}

	foreign, foreignRecorder := canonicalDriftRouteEvent(http.MethodGet, "/api/v1/stacks/stack-1/drift/status", stack.ID, "tenant-1", "owner-2", ``)
	if err := handler.status(foreign); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatal(err)
	}
	if foreignRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign owner status = %d %s, want 404", foreignRecorder.Code, foreignRecorder.Body.String())
	}
}

func TestDriftCheckRejectsIneligibleCanonicalStackBeforeDispatch(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Homelab", Status: "stopped",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := driftRouteHandlers{stores: DriftRouteStores{Stacks: store, Drift: store}}
	event, recorder := canonicalDriftRouteEvent(http.MethodPost, "/api/v1/stacks/stack-1/drift/check", stack.ID, "tenant-1", "owner-1", `{}`)
	if err := handler.check(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
}

func TestDriftStatusUnauthenticatedStopsBeforeStoreLookup(t *testing.T) {
	handler := driftRouteHandlers{}
	event, recorder := canonicalDriftRouteEvent(http.MethodGet, "/api/v1/stacks/missing/drift/status", "missing", "", "", ``)
	if err := handler.status(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
	}
}

func canonicalDriftRouteEvent(method, target, resourceID, tenantID, ownerID, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", resourceID)
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	}
	recorder := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: recorder}, recorder
}
