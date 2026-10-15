package routes

import (
	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Sensitive boundary: unauthenticated and tenant-less SaaS reads must fail
// before an unavailable monitor can return a healthy-looking empty report.
func TestDomainMonitoringRequiresTenantIdentity(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	router := httpx.NewRouter()
	RegisterDomainMonitoringRoutes(router, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/domains", nil)
	response := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous domain read admitted: %d", response.Code)
	}
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "auth0|domain-test"}))
	response = httptest.NewRecorder()
	router.BuildMux().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("tenant-less domain read admitted: %d", response.Code)
	}
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "auth0|domain-test", OrgID: "test-tenant"}))
	response = httptest.NewRecorder()
	router.BuildMux().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable store reported domain health: %d", response.Code)
	}
}
