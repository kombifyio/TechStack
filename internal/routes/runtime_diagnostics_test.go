package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func diagnosticsRequest(secret string) (*httpx.Router, *httptest.ResponseRecorder, *http.Request) {
	router := httpx.NewRouter()
	RegisterRuntimeDiagnosticsRoutes(router)
	req := httptest.NewRequest(http.MethodGet, diagnosticsRoute, nil)
	if secret != "" {
		req.Header.Set(demoResetSecretHeader, secret)
	}
	return router, httptest.NewRecorder(), req
}

// Without the shared secret the dump must not be reachable. A goroutine dump
// names internal call paths, so it is operator-only by construction.
func TestGoroutineDumpRejectsAnUnauthenticatedCaller(t *testing.T) {
	t.Setenv(envDemoResetSecret, "operator-secret")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	router, rec, req := diagnosticsRequest("")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "goroutine ") {
		t.Fatalf("rejected request leaked a stack dump: %s", rec.Body.String()[:min(400, rec.Body.Len())])
	}
}

// A wrong secret is refused just like a missing one.
func TestGoroutineDumpRejectsAWrongSecret(t *testing.T) {
	t.Setenv(envDemoResetSecret, "operator-secret")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	router, rec, req := diagnosticsRequest("not-the-secret")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// With the secret the operator gets a real dump, which is the only signal that
// says where a silently wedged job actually is.
func TestGoroutineDumpServesTheOperator(t *testing.T) {
	t.Setenv(envDemoResetSecret, "operator-secret")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	router, rec, req := diagnosticsRequest("operator-secret")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "goroutine ") {
		t.Fatalf("body does not look like a stack dump: %s", rec.Body.String()[:min(200, rec.Body.Len())])
	}
}

// Fail closed when the gate is not configured at all.
func TestGoroutineDumpIsUnavailableWithoutConfiguredSecret(t *testing.T) {
	t.Setenv(envDemoResetSecret, "")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	router, rec, req := diagnosticsRequest("anything")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
