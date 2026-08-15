package auth

import (
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

// CheckAuth verifies that the request is from any authenticated user.
// Returns an error response if not authenticated, nil if authenticated.
func CheckAuth(e *httpx.Event) error {
	if !IsAuthenticated(e) {
		return httpx.Unauthorized(e, "Authentication required")
	}
	return nil
}

// IsAuthenticated returns true if the request has an authenticated user.
func IsAuthenticated(e *httpx.Event) bool {
	_, ok := AuthUserID(e)
	return ok
}

// AuthUserID returns the authenticated principal or signed Edge user ID.
func AuthUserID(e *httpx.Event) (string, bool) {
	if e == nil {
		return "", false
	}
	if e.Auth != nil && e.Auth.Id != "" {
		return e.Auth.Id, true
	}
	if e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil && id.IsAuthenticated() {
			return id.UserID, true
		}
	}
	return "", false
}
