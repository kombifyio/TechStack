package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

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
	want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io https://preview.kombify.io"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}

func TestApplyRequestSecurityHeaders_SaaSAllowsCloudPreviewReferer(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "")

	// Render suffixes a preview slug that is already taken.
	for _, preview := range []string{
		"https://kombify-cloud-native-pr-434.onrender.com",
		"https://kombify-cloud-native-pr-1146-lft5.onrender.com",
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/auth/sso?embedded=true", nil)
		req.Host = "techstack.kombify.io"
		req.Header.Set("Referer", preview+"/dashboard/tools/stack")

		ApplyRequestSecurityHeaders(rr.Header(), req, config.ModeSaaS)

		csp := rr.Header().Get("Content-Security-Policy")
		want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io https://preview.kombify.io " + preview
		if csp != want {
			t.Fatalf("referer %q: expected CSP %q, got %q", preview, want, csp)
		}
	}
}

func TestApplyRequestSecurityHeaders_SaaSIgnoresLookalikeReferers(t *testing.T) {
	t.Setenv("ALLOWED_FRAME_ORIGINS", "")

	for _, referer := range []string{
		"https://evil-kombify-cloud-native-pr-434.onrender.com/",
		"https://kombify-cloud-native-pr-434.onrender.com.evil.io/",
		"https://other-app-pr-434.onrender.com/",
		"http://kombify-cloud-native-pr-434.onrender.com/",
		"https://kombify-cloud-native-pr-434xonrender.com/",
		"https://kombify-cloud-native-pr-434.onrenderxcom/",
		"https://kombify-cloud-native-pr-434-attacker.onrender.com/",
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://techstack.kombify.io/", nil)
		req.Host = "techstack.kombify.io"
		req.Header.Set("Referer", referer)

		ApplyRequestSecurityHeaders(rr.Header(), req, config.ModeSaaS)

		csp := rr.Header().Get("Content-Security-Policy")
		want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io https://preview.kombify.io"
		if csp != want {
			t.Fatalf("referer %q: expected CSP %q, got %q", referer, want, csp)
		}
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
	want := "frame-ancestors 'self' https://kombify.io https://app.kombify.io https://preview.kombify.io"
	if csp != want {
		t.Fatalf("expected CSP %q, got %q", want, csp)
	}
}
