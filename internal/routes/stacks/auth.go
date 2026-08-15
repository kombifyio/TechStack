package stacks

import (
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func authenticatedStackUserID(e *httpx.Event) (string, bool) {
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

func requireStackAuth(e *httpx.Event) (string, error) {
	if userID, ok := authenticatedStackUserID(e); ok {
		return userID, nil
	}
	return "", httpx.RejectUnauthorized(e, "")
}
