package routes

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/pkg/demoguard"
)

func TestDispatchCommandBlockedForDemoTenant(t *testing.T) {
	t.Setenv(demoguard.EnvDemoTenantID, "demo-tenant")
	h := rilCommandHandler{}
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/ril/servers/srv-1/cmd", `{"command":"id"}`, "visitor", "demo-tenant")
	event.Request.SetPathValue("serverId", "srv-1")
	if err := h.dispatchCommand(event); err != nil {
		t.Fatalf("dispatchCommand returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 for demo tenant", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	apiError, _ := response["error"].(map[string]any)
	details, _ := apiError["details"].(map[string]any)
	if details == nil || details["error_code"] != "demo_account_restricted" {
		t.Fatalf("details = %#v, want demo_account_restricted", details)
	}
}

func TestDispatchCommandBlockedForDemoUserID(t *testing.T) {
	t.Setenv(demoguard.EnvDemoUserIDs, "auth0|demo-sub, other-sub")
	h := rilCommandHandler{}
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/ril/servers/srv-1/cmd", `{"command":"id"}`, "auth0|demo-sub", "some-org")
	event.Request.SetPathValue("serverId", "srv-1")
	if err := h.dispatchCommand(event); err != nil {
		t.Fatalf("dispatchCommand returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for demo user id", recorder.Code)
	}
}

func TestAuthorizeDemoAutomationAcceptsNextSecretDuringRotation(t *testing.T) {
	t.Setenv(envDemoResetSecret, "current-demo-reset-secret")
	t.Setenv(envDemoResetSecretNext, "next-demo-reset-secret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)

	event, _ := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "next-demo-reset-secret")

	tenantID, err := authorizeDemoAutomation(event, "reset")
	if err != nil {
		t.Fatalf("authorizeDemoAutomation: %v", err)
	}
	if tenantID != demoResetTestTenant {
		t.Fatalf("tenantID = %q, want %q", tenantID, demoResetTestTenant)
	}
}
