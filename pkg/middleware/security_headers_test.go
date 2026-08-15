package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

func TestSecurityHeadersMiddlewareSetsHeaders(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "")

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := SecurityHeadersMiddleware(base)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got == "" {
		t.Fatalf("expected Referrer-Policy to be set")
	}
	if got := rr.Header().Get("Permissions-Policy"); got == "" {
		t.Fatalf("expected Permissions-Policy to be set")
	}
}

func TestApplySecurityHeaders(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "")

	rr := httptest.NewRecorder()
	ApplySecurityHeaders(rr.Header())

	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got == "" {
		t.Fatalf("expected Referrer-Policy to be set")
	}
	if got := rr.Header().Get("Permissions-Policy"); got == "" {
		t.Fatalf("expected Permissions-Policy to be set")
	}
}

func TestApplySecurityHeaders_AllowedFrameOrigins(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "http://localhost:5290 https://kombify.io")

	rr := httptest.NewRecorder()
	ApplySecurityHeaders(rr.Header())

	// X-Frame-Options should NOT be set when frame-ancestors is used
	if got := rr.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options should be absent when ALLOWED_FRAME_ORIGINS is set, got %q", got)
	}

	csp := rr.Header().Get("Content-Security-Policy")
	want := "frame-ancestors 'self' http://localhost:5290 https://kombify.io"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}

func TestApplyRequestSecurityHeaders_SaaSAddsDevPortalOrigins(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "https://kombify.space")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.dev/", nil)
	req.Host = "techstack.kombify.dev"

	ApplyRequestSecurityHeaders(rr.Header(), req, config.ModeSaaS)

	csp := rr.Header().Get("Content-Security-Policy")
	want := "frame-ancestors 'self' https://kombify.space https://kombify.dev https://app.kombify.dev"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}

func TestApplyRequestSecurityHeaders_SaaSKeepsConfiguredOriginsAndAllowsCanonicalPortal(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "https://kombify.io")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/stacks/creating", nil)
	req.Host = "techstack.kombify.io"

	ApplyRequestSecurityHeaders(rr.Header(), req, config.ModeSaaS)

	if got := rr.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options should be absent when frame-ancestors is set, got %q", got)
	}

	csp := rr.Header().Get("Content-Security-Policy")
	want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}

func TestApplyRequestSecurityHeaders_SaaSAllowsCanonicalPortalWithoutEnvAllowlist(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/stacks/creating", nil)
	req.Host = "techstack.kombify.io"

	ApplyRequestSecurityHeaders(rr.Header(), req, config.ModeSaaS)

	if got := rr.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options should be absent when frame-ancestors is set, got %q", got)
	}

	csp := rr.Header().Get("Content-Security-Policy")
	want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}
