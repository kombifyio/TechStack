package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV2AuthForceLoginPromptAddsAuthorizePrompt(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.kombify.io/authorize?client_id=abc&state=1", http.StatusFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/login?prompt=login&return_to=%2Fdashboard", nil)
	recorder := httptest.NewRecorder()
	v2AuthForceLoginPrompt(inner).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "https://login.kombify.io/authorize") ||
		!strings.Contains(location, "prompt=login") ||
		!strings.Contains(location, "max_age=0") {
		t.Fatalf("location = %q, want Auth0 authorize with prompt=login", location)
	}
}

func TestV2AuthForceLoginPromptLeavesSilentLoginAlone(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.kombify.io/authorize?client_id=abc", http.StatusFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v2/auth/login?return_to=%2Fdashboard", nil)
	recorder := httptest.NewRecorder()
	v2AuthForceLoginPrompt(inner).ServeHTTP(recorder, req)

	location := recorder.Header().Get("Location")
	if location != "https://login.kombify.io/authorize?client_id=abc" {
		t.Fatalf("location = %q, want untouched authorize URL", location)
	}
}
