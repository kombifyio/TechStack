package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/enrollment"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterEnrollmentRoutes exposes the local enrollment record at GET
// /api/v1/enrollment.
//
// The endpoint reports the instance's enrollment mode (standalone or
// cloud_linked), the cloud tenant ID when linked, and the timestamp of the
// last update. It is unauthenticated -- it returns only public status fields,
// no secrets or session material -- and is used by the operator UI and the
// configuration wizard to drive the next-step decision.
//
// Pillar 1 (Unifier) anchor; architecture detail in docs/ARCHITECTURE.md
// and docs/pillars/01-unifier.md.
//
// Cloud-linking write operations (POST /api/v1/enrollment/cloud-link, revoke,
// rotate) are intentionally NOT registered in this slice; they require an
// authenticated operator session and a real handshake with kombify Cloud,
// which lands in a later Phase 1 slice.
func RegisterEnrollmentRoutes(r *httpx.Router, resolver *enrollment.Resolver) {
	r.GET("/api/v1/enrollment", func(e *httpx.Event) error {
		current, err := resolver.Load()
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load enrollment record", err.Error())
		}
		payload := map[string]any{
			"instance_id": current.InstanceID,
			"mode":        string(current.Mode),
			"updated_at":  current.UpdatedAt,
		}
		if current.CloudTenantID != "" {
			payload["cloud_tenant_id"] = current.CloudTenantID
		}
		if current.LinkedAt != nil {
			payload["linked_at"] = current.LinkedAt
		}
		return httpx.Success(e, http.StatusOK, payload)
	})
}
