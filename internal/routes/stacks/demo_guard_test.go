package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestDestroyStackBlocksOnlyMarkedDemoAnchor(t *testing.T) {
	t.Setenv(demoguard.EnvDemoTenantID, "demo-tenant")
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "demo-tenant", OwnerSubjectID: "visitor",
		Name: "kombify-demo", Status: "running", Config: map[string]any{"demo_anchor": true},
	}); err != nil {
		t.Fatalf("seed demo anchor: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/stack-1/destroy", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "visitor", OrgID: "demo-tenant"}))
	req.SetPathValue("id", "stack-1")
	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}

	h := crudRouteHandlers{stackStore: store}
	if err := h.destroyStack(event); err != nil {
		t.Fatalf("destroyStack returned router error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 for demo tenant", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	apiError, _ := response["error"].(map[string]any)
	details, _ := apiError["details"].(map[string]any)
	if details == nil || details["error_code"] != "demo_account_restricted" {
		t.Fatalf("details = %#v, want demo_account_restricted", details)
	}
	guidance, _ := details["user_guidance"].(map[string]any)
	if guidance == nil || guidance["body"] == "" {
		t.Fatalf("user_guidance = %#v, want user-facing body", details["user_guidance"])
	}
}

func TestDemoTenantFailedStackIsNotProtectedAnchor(t *testing.T) {
	t.Setenv(demoguard.EnvDemoTenantID, "demo-tenant")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/failed-stack/destroy", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "visitor", OrgID: "demo-tenant"}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	stack := &controlplane.Stack{
		ID: "failed-stack", TenantID: "demo-tenant", OwnerSubjectID: "visitor",
		Name: "Demo", Status: "failed", Config: map[string]any{"demo_anchor": false},
	}
	if demoProtectedStoreStackRequest(event, stack) {
		t.Fatal("an unmarked failed visitor stack must remain deletable in the demo tenant")
	}
}

func TestDemoRestrictedStackRequestInertWithoutConfig(t *testing.T) {
	t.Setenv(demoguard.EnvDemoTenantID, "")
	t.Setenv(demoguard.EnvDemoUserIDs, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/stack-1/destroy", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "visitor", OrgID: "any-org"}))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	if demoRestrictedStackRequest(event) {
		t.Fatal("demo guard fired without demo config")
	}
	t.Setenv(demoguard.EnvDemoUserIDs, "visitor")
	if !demoRestrictedStackRequest(event) {
		t.Fatal("demo guard must match the configured demo user id")
	}
}
