package auth

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Losing the native auth store must not revive a local database or admit
// self-hosted setup. Exercise the actual route without any store/DB fixture.
func TestAuthRuntimeRequiresCanonicalPersistence(t *testing.T) {
	router := httpx.NewRouter()
	RegisterRuntimeRoutesWithConfig(router, config.ModeSelfHosted, config.EditionSelfHostOSS, RouteConfig{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/auth/mode", nil))
	if response.Code < 500 {
		t.Fatal("unconfigured canonical auth authority admitted a mode projection")
	}
	saas := httpx.NewRouter()
	RegisterRuntimeRoutesWithConfig(saas, config.ModeSaaS, config.EditionSaaSStandalone, RouteConfig{})
	cloud := httptest.NewRecorder()
	saas.ServeHTTP(cloud, httptest.NewRequest("GET", "/api/v1/auth/mode", nil))
	var body struct {
		Data AuthModeResponse `json:"data"`
	}
	if err := json.Unmarshal(cloud.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if cloud.Code != 200 || body.Data.Mode != "cloud" || body.Data.AllowLocalLogin || body.Data.IsFirstRun {
		t.Fatal("SaaS projection abandoned the platform auth authority")
	}
}
