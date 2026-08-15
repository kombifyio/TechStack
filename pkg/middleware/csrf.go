// Package middleware provides HTTP middleware components for kombifyTechstack.
package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	// CSRFCookieName is the name of the cookie storing the CSRF token.
	CSRFCookieName = "csrf_token"

	// CSRFHeaderName is the HTTP header name for the CSRF token.
	CSRFHeaderName = "X-CSRF-Token"

	// csrfTokenLength is the byte length of generated tokens (64 hex chars).
	csrfTokenLength = 32
)

// CSRFMiddleware implements Double Submit Cookie CSRF protection.
// It generates a token stored in a cookie and requires the same token
// to be sent in a header for state-changing requests (POST, PUT, DELETE, PATCH).
type CSRFMiddleware struct {
	ignorePaths    []string // paths that don't need CSRF (e.g., /api/v1/health)
	ignorePrefixes []string // path prefixes that don't need CSRF (e.g., /api/v1/agent/)
	secure         bool     // use Secure cookie flag (should be true in production)
	cookiePath     string   // cookie path, defaults to "/"
	maxAge         int      // cookie max age in seconds, defaults to 12 hours
}

// CSRFConfig holds configuration for CSRF middleware.
type CSRFConfig struct {
	// Secure enables the Secure flag on the cookie (HTTPS only).
	// Should be true in production.
	Secure bool

	// IgnorePaths is a list of exact paths that bypass CSRF validation.
	// Useful for health checks, webhooks with their own auth, etc.
	IgnorePaths []string

	// IgnorePrefixes is a list of path prefixes that bypass CSRF validation.
	// Useful for agent gRPC endpoints or external integrations.
	IgnorePrefixes []string

	// CookiePath sets the path for the CSRF cookie.
	// Defaults to "/".
	CookiePath string

	// MaxAge is the cookie max age in seconds.
	// Defaults to 43200 (12 hours).
	MaxAge int
}

// DefaultCSRFConfig returns sensible defaults for CSRF protection.
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		Secure:         true,
		IgnorePaths:    []string{"/api/v1/health", "/api/health"},
		IgnorePrefixes: []string{},
		CookiePath:     "/",
		MaxAge:         43200, // 12 hours
	}
}

// NewCSRFMiddleware creates a new CSRF middleware with the given config.
func NewCSRFMiddleware(cfg CSRFConfig) *CSRFMiddleware {
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 43200
	}

	return &CSRFMiddleware{
		ignorePaths:    cfg.IgnorePaths,
		ignorePrefixes: cfg.IgnorePrefixes,
		secure:         cfg.Secure,
		cookiePath:     cfg.CookiePath,
		maxAge:         cfg.MaxAge,
	}
}

// Middleware returns the HTTP middleware handler.
func (c *CSRFMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.handle(w, r, func() { next.ServeHTTP(w, r) })
	})
}

// HTTPXMiddleware returns the httpx router middleware equivalent of
// Middleware. It enforces CSRF only for unsafe cookie-auth browser requests and
// lets bearer/service-auth API calls keep their own authentication boundary.
func (c *CSRFMiddleware) HTTPXMiddleware() func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		nextCalled := false
		c.handle(e.Response, e.Request, func() { nextCalled = true })
		if !nextCalled {
			return nil
		}
		return e.Next()
	}
}

func (c *CSRFMiddleware) handle(w http.ResponseWriter, r *http.Request, next func()) {
	// Skip for safe methods (GET, HEAD, OPTIONS, TRACE)
	if c.isSafeMethod(r.Method) {
		if err := c.ensureToken(w, r); err != nil {
			c.writeError(w, "failed to generate CSRF token", http.StatusInternalServerError)
			return
		}
		next()
		return
	}

	// Skip ignored paths
	if c.shouldIgnore(r.URL.Path) {
		next()
		return
	}

	if c.shouldBypassNonCookieAuth(r) {
		next()
		return
	}

	// Validate token for state-changing requests
	if !c.validateToken(r) {
		c.writeError(w, "invalid or missing CSRF token", http.StatusForbidden)
		return
	}

	next()
}

// isSafeMethod returns true for HTTP methods that don't require CSRF protection.
func (c *CSRFMiddleware) isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// shouldIgnore returns true if the path should bypass CSRF validation.
func (c *CSRFMiddleware) shouldIgnore(path string) bool {
	// Check exact path matches
	for _, ignorePath := range c.ignorePaths {
		if path == ignorePath {
			return true
		}
	}

	// Check prefix matches
	for _, prefix := range c.ignorePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

func (c *CSRFMiddleware) shouldBypassNonCookieAuth(r *http.Request) bool {
	if isEdgeAuthenticatedContext(r.Context()) {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ") {
		return true
	}
	if strings.TrimSpace(r.Header.Get("X-Kombify-Service-Auth")) != "" {
		return true
	}
	return r.Header.Get("Cookie") == ""
}

// ensureToken ensures a CSRF token exists in the response.
// If no valid token cookie exists, it generates a new one.
// Returns error if token generation fails (indicates system entropy issue).
func (c *CSRFMiddleware) ensureToken(w http.ResponseWriter, r *http.Request) error {
	// Check if token already exists
	cookie, err := r.Cookie(CSRFCookieName)
	if err == nil && len(cookie.Value) == csrfTokenLength*2 {
		// Valid token exists, also set it in response header for frontend convenience
		w.Header().Set(CSRFHeaderName, cookie.Value)
		return nil
	}

	// Generate new token
	token, err := generateCSRFToken()
	if err != nil {
		return err
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     c.cookiePath,
		MaxAge:   c.maxAge,
		HttpOnly: false, // Frontend needs to read this to send in header
		Secure:   c.secure,
		SameSite: http.SameSiteStrictMode,
	})

	// Also set in response header for immediate use
	w.Header().Set(CSRFHeaderName, token)
	return nil
}

// validateToken validates that the CSRF token from the header matches the cookie.
func (c *CSRFMiddleware) validateToken(r *http.Request) bool {
	// Get token from cookie
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	// Get token from header
	headerToken := r.Header.Get(CSRFHeaderName)
	if headerToken == "" {
		// Also check form field as fallback for traditional form submissions
		headerToken = r.FormValue("_csrf")
	}

	if headerToken == "" {
		return false
	}

	// Constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) == 1
}

// writeError writes a JSON error response.
func (c *CSRFMiddleware) writeError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}

// generateCSRFToken generates a cryptographically secure random token.
// Returns an error if crypto/rand fails (indicates serious system issue).
func generateCSRFToken() (string, error) {
	b := make([]byte, csrfTokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("csrf: failed to generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GetCSRFToken extracts the current CSRF token from a request's cookies.
// This is useful for handlers that need to include the token in rendered HTML.
func GetCSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// SetCSRFCookie is a helper to manually set a CSRF cookie with a specific token.
// Useful for testing or special initialization scenarios.
func SetCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   43200,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// CSRFTokenEndpoint returns an HTTP handler that generates and returns a CSRF token.
// This can be used as a dedicated endpoint for SPAs to obtain a token before mutations.
func CSRFTokenEndpoint(secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check for existing token
		cookie, err := r.Cookie(CSRFCookieName)
		var token string
		if err == nil && len(cookie.Value) == csrfTokenLength*2 {
			token = cookie.Value
		} else {
			token, err = generateCSRFToken()
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{
					"error": "failed to generate CSRF token",
				})
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     CSRFCookieName,
				Value:    token,
				Path:     "/",
				MaxAge:   43200,
				HttpOnly: false,
				Secure:   secure,
				SameSite: http.SameSiteStrictMode,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(CSRFHeaderName, token)
		json.NewEncoder(w).Encode(map[string]string{
			"token": token,
		})
	}
}

// TokenRefreshMiddleware is a middleware that refreshes the CSRF token cookie
// on every request to extend its lifetime. Use this if you want tokens to stay
// valid as long as the user is active.
func (c *CSRFMiddleware) TokenRefreshMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CSRFCookieName)
		if err == nil && len(cookie.Value) == csrfTokenLength*2 {
			// Refresh the cookie with the same token
			http.SetCookie(w, &http.Cookie{
				Name:     CSRFCookieName,
				Value:    cookie.Value,
				Path:     c.cookiePath,
				MaxAge:   c.maxAge,
				HttpOnly: false,
				Secure:   c.secure,
				SameSite: http.SameSiteStrictMode,
				Expires:  time.Now().Add(time.Duration(c.maxAge) * time.Second),
			})
		}
		next.ServeHTTP(w, r)
	})
}
