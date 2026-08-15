package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRFMiddleware_SafeMethods(t *testing.T) {
	cfg := CSRFConfig{
		Secure:      false,
		IgnorePaths: []string{"/health"},
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	safeMethods := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace}

	for _, method := range safeMethods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/test", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected status 200 for %s, got %d", method, rec.Code)
			}

			// Should set CSRF token cookie
			cookies := rec.Result().Cookies()
			var found bool
			for _, c := range cookies {
				if c.Name == CSRFCookieName {
					found = true
					if len(c.Value) != csrfTokenLength*2 {
						t.Errorf("expected token length %d, got %d", csrfTokenLength*2, len(c.Value))
					}
					if c.SameSite != http.SameSiteStrictMode {
						t.Error("expected SameSite=Strict")
					}
				}
			}
			if !found {
				t.Error("CSRF cookie not set on safe method")
			}

			// Should also set token in header
			headerToken := rec.Header().Get(CSRFHeaderName)
			if headerToken == "" {
				t.Error("CSRF header not set on safe method")
			}
		})
	}
}

func TestCSRFMiddleware_POSTWithoutToken(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.AddCookie(testSessionCookie())
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for POST without token, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "invalid or missing CSRF token") {
		t.Errorf("expected CSRF error message, got: %s", body)
	}
}

func TestCSRFMiddleware_POSTWithValidToken(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))

	// First, do a GET to obtain a token
	getReq := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	// Extract token from cookie
	var token string
	for _, c := range getRec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			token = c.Value
			break
		}
	}
	if token == "" {
		t.Fatal("failed to get CSRF token from GET request")
	}

	// Now do POST with token
	postReq := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	postReq.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	postReq.Header.Set(CSRFHeaderName, token)
	postRec := httptest.NewRecorder()

	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Errorf("expected status 200 for POST with valid token, got %d", postRec.Code)
	}

	if postRec.Body.String() != "success" {
		t.Errorf("expected 'success' body, got: %s", postRec.Body.String())
	}
}

func TestCSRFMiddleware_POSTWithMismatchedToken(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Use different tokens in cookie and header
	postReq := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	postReq.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	postReq.Header.Set(CSRFHeaderName, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	postRec := httptest.NewRecorder()

	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for mismatched tokens, got %d", postRec.Code)
	}
}

func TestCSRFMiddleware_IgnoredPaths(t *testing.T) {
	cfg := CSRFConfig{
		Secure:      false,
		IgnorePaths: []string{"/api/v1/health", "/webhook"},
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	ignoredPaths := []string{"/api/v1/health", "/webhook"}

	for _, path := range ignoredPaths {
		t.Run(path, func(t *testing.T) {
			// POST without token should succeed for ignored paths
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected status 200 for ignored path %s, got %d", path, rec.Code)
			}
		})
	}

	// Non-ignored path should still require token
	t.Run("non-ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
		req.AddCookie(testSessionCookie())
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status 403 for non-ignored path, got %d", rec.Code)
		}
	})
}

func TestCSRFMiddleware_IgnoredPrefixes(t *testing.T) {
	cfg := CSRFConfig{
		Secure:         false,
		IgnorePrefixes: []string{"/api/v1/agent/", "/internal/"},
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		path     string
		expected int
	}{
		{"/api/v1/agent/register", http.StatusOK},
		{"/api/v1/agent/heartbeat", http.StatusOK},
		{"/internal/metrics", http.StatusOK},
		{"/api/v1/stacks", http.StatusForbidden}, // not ignored
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.AddCookie(testSessionCookie())
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.expected {
				t.Errorf("path %s: expected status %d, got %d", tc.path, tc.expected, rec.Code)
			}
		})
	}
}

func TestCSRFMiddleware_AllMutationMethods(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mutationMethods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}

	for _, method := range mutationMethods {
		t.Run(method+"_without_token", func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/test", nil)
			req.AddCookie(testSessionCookie())
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("expected status 403 for %s without token, got %d", method, rec.Code)
			}
		})
	}
}

func TestCSRFMiddleware_BypassesNonCookieAuth(t *testing.T) {
	csrf := NewCSRFMiddleware(CSRFConfig{Secure: false})

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		header func(*http.Request)
	}{
		{
			name:   "no cookies",
			header: func(*http.Request) {},
		},
		{
			name: "bearer auth",
			header: func(r *http.Request) {
				r.AddCookie(testSessionCookie())
				r.Header.Set("Authorization", "Bearer api-token")
			},
		},
		{
			name: "service auth",
			header: func(r *http.Request) {
				r.AddCookie(testSessionCookie())
				r.Header.Set("X-Kombify-Service-Auth", "service.jwt")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
			tt.header(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestCSRFMiddleware_BypassesVerifiedEdgeContext(t *testing.T) {
	csrf := NewCSRFMiddleware(CSRFConfig{Secure: false})

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{}`))
	req = req.WithContext(markEdgeAuthenticatedContext(context.Background()))
	req.AddCookie(testSessionCookie())
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCSRFMiddleware_FormField(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First, get a token
	getReq := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	var token string
	for _, c := range getRec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			token = c.Value
			break
		}
	}

	// POST with form field instead of header
	formBody := strings.NewReader("_csrf=" + token + "&data=test")
	postReq := httptest.NewRequest(http.MethodPost, "/api/test", formBody)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: token})
	postRec := httptest.NewRecorder()

	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Errorf("expected status 200 for POST with form field token, got %d", postRec.Code)
	}
}

func TestCSRFMiddleware_SecureCookie(t *testing.T) {
	cfg := CSRFConfig{
		Secure: true,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			if !c.Secure {
				t.Error("expected Secure flag to be set on cookie")
			}
		}
	}
}

func TestCSRFMiddleware_ExistingTokenNotReplaced(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create a valid existing token
	existingToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: existingToken})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Token should be echoed in header
	headerToken := rec.Header().Get(CSRFHeaderName)
	if headerToken != existingToken {
		t.Errorf("expected existing token %s in header, got %s", existingToken, headerToken)
	}
}

func TestGenerateCSRFToken(t *testing.T) {
	token1, err1 := generateCSRFToken()
	if err1 != nil {
		t.Fatalf("unexpected error: %v", err1)
	}
	token2, err2 := generateCSRFToken()
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}

	// Check length
	if len(token1) != csrfTokenLength*2 {
		t.Errorf("expected token length %d, got %d", csrfTokenLength*2, len(token1))
	}

	// Check uniqueness
	if token1 == token2 {
		t.Error("generated tokens should be unique")
	}

	// Check it's valid hex
	for _, c := range token1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("token contains invalid character: %c", c)
		}
	}
}

func TestGetCSRFToken(t *testing.T) {
	expectedToken := "testtoken123456789012345678901234567890123456789012345678901234"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: expectedToken})

	token := GetCSRFToken(req)
	if token != expectedToken {
		t.Errorf("expected token %s, got %s", expectedToken, token)
	}

	// Test with no cookie
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	token2 := GetCSRFToken(req2)
	if token2 != "" {
		t.Errorf("expected empty token, got %s", token2)
	}
}

func TestCSRFTokenEndpoint(t *testing.T) {
	handler := CSRFTokenEndpoint(false)

	// First request - should generate new token
	req := httptest.NewRequest(http.MethodGet, "/api/csrf", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Check response contains token
	body := rec.Body.String()
	if !strings.Contains(body, `"token"`) {
		t.Errorf("expected token in response body, got: %s", body)
	}

	// Check header
	headerToken := rec.Header().Get(CSRFHeaderName)
	if headerToken == "" {
		t.Error("expected CSRF token in header")
	}

	// Check cookie
	var cookieToken string
	for _, c := range rec.Result().Cookies() {
		if c.Name == CSRFCookieName {
			cookieToken = c.Value
		}
	}
	if cookieToken == "" {
		t.Error("expected CSRF cookie to be set")
	}

	// Header and cookie should match
	if headerToken != cookieToken {
		t.Error("header and cookie tokens should match")
	}
}

func testSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     "techstack_session",
		Value:    "session",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

func TestDefaultCSRFConfig(t *testing.T) {
	cfg := DefaultCSRFConfig()

	if !cfg.Secure {
		t.Error("default config should have Secure=true")
	}

	if cfg.MaxAge != 43200 {
		t.Errorf("expected MaxAge 43200, got %d", cfg.MaxAge)
	}

	if cfg.CookiePath != "/" {
		t.Errorf("expected CookiePath '/', got %s", cfg.CookiePath)
	}

	if len(cfg.IgnorePaths) == 0 {
		t.Error("expected default ignore paths")
	}
}

func TestCSRFMiddleware_JSONResponse(t *testing.T) {
	cfg := CSRFConfig{
		Secure: false,
	}
	csrf := NewCSRFMiddleware(cfg)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.AddCookie(testSessionCookie())
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"error"`) {
		t.Errorf("expected JSON error response, got: %s", body)
	}
}
