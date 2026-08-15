package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// authorizeDemoAutomation writes its rejection envelope through httpx.Error,
// which returns the encoder result -- nil whenever the write succeeds. So on a
// refused request it returns ("", nil), and a caller that branches only on the
// error keeps going as if the request had been admitted.
//
// This test pins that contract so the empty tenant id stays the authoritative
// rejection signal, and so nobody "simplifies" a caller back to an error-only
// check.
func TestAuthorizeDemoAutomationSignalsRejectionThroughTheTenantIDNotTheError(t *testing.T) {
	t.Setenv(envDemoResetSecret, "operator-secret")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	for _, test := range []struct {
		name       string
		secret     string
		wantStatus int
	}{
		{"missing secret", "", http.StatusForbidden},
		{"wrong secret", "nope", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/demo/reset", nil)
			if test.secret != "" {
				req.Header.Set(demoResetSecretHeader, test.secret)
			}
			rec := httptest.NewRecorder()
			event := &httpx.Event{Request: req, Response: rec}

			tenantID, err := authorizeDemoAutomation(event, "reset")
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantStatus)
			}
			if tenantID != "" {
				t.Fatalf("tenant id = %q, want empty on rejection", tenantID)
			}
			if err != nil {
				t.Skip("rejection now also returns a non-nil error; the tenant-id check stays correct either way")
			}
		})
	}
}

// The admitted path still returns the tenant, so the guard above cannot be
// satisfied by simply always refusing.
func TestAuthorizeDemoAutomationAdmitsTheOperator(t *testing.T) {
	t.Setenv(envDemoResetSecret, "operator-secret")
	t.Setenv(envDemoResetSecretNext, "")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/demo/reset", nil)
	req.Header.Set(demoResetSecretHeader, "operator-secret")
	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}

	tenantID, err := authorizeDemoAutomation(event, "reset")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tenantID == "" {
		t.Fatal("admitted request must return the demo tenant id")
	}
}
