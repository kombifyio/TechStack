package routes

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/instance"
)

// RegisterInstanceRoutes exposes the local instance identity at GET
// /api/v1/instance.
//
// The endpoint is intentionally unauthenticated: it returns only the
// instance's stable ID, creation timestamp, hostname, and product version. It
// is used by the operator UI and by tooling to confirm which TechStack
// deployment unit is being talked to. No tenant, owner, or workspace data is
// exposed here.
//
// Pillar 1 (Unifier) anchor; architecture detail in docs/ARCHITECTURE.md
// and docs/pillars/01-unifier.md.
func RegisterInstanceRoutes(r *httpx.Router, identity instance.Identity, version string) {
	r.GET("/api/v1/instance", func(e *httpx.Event) error {
		return httpx.Success(e, http.StatusOK, map[string]any{
			"id":         identity.ID,
			"created_at": identity.CreatedAt,
			"hostname":   identity.Hostname,
			"version":    version,
		})
	})
}
