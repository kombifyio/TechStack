package routes

import (
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/pkg/demoguard"
)

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
