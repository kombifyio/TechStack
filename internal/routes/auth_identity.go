package routes

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func authenticatedUserID(e *httpx.Event) (string, bool) {
	userID, _, ok := authenticatedUser(e)
	return userID, ok
}

func authenticatedUser(e *httpx.Event) (string, bool, bool) {
	if e == nil {
		return "", false, false
	}
	if e.Auth != nil && e.Auth.Id != "" {
		return e.Auth.Id, principalIsAdmin(e.Auth), true
	}
	if e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil && id.IsAuthenticated() {
			return id.UserID, identityHasAdminRole(id), true
		}
	}
	return "", false, false
}

func principalIsAdmin(p *httpx.Principal) bool {
	if p == nil {
		return false
	}
	if p.IsSuperuser() {
		return true
	}
	role := strings.TrimSpace(p.GetString("role"))
	return isAdminRole(role)
}

func identityHasAdminRole(id *identity.Identity) bool {
	if id == nil {
		return false
	}
	return id.HasRole("admin") || id.HasRole("super_admin") || id.HasRole("global_admin")
}

func isAdminRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "super_admin", "global_admin":
		return true
	default:
		return false
	}
}

func signedEdgeAdmin(e *httpx.Event) bool {
	if e == nil || e.Request == nil {
		return false
	}
	return identityHasAdminRole(identity.FromContext(e.Request.Context()))
}
