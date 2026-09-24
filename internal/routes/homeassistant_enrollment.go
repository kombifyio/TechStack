package routes

import (
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/pkg/httpx"
	"io"
	"net/http"
)

func RegisterHomeAssistantEnrollmentRoutes(r *httpx.Router, store *homeassistant.EnrollmentStore) {
	r.GET("/api/v1/home-assistant/owners", func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant.bind")
		if err != nil {
			return err
		}
		if store == nil {
			return httpx.NewAPIError(503, "unavailable", "Local owner enrollment is unavailable", nil)
		}
		result, err := store.List(e.Request.Context(), tenant, owner, e.Request.URL.Query().Get("stack_id"))
		if err != nil {
			return httpx.NewAPIError(409, "enrollment_unavailable", "Owned enrollments could not be read", nil)
		}
		return httpx.Success(e, 200, result)
	})
	r.POST("/api/v1/home-assistant/owners/attach", func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant.bind")
		if err != nil {
			return err
		}
		if store == nil {
			return httpx.NewAPIError(503, "unavailable", "Local owner enrollment is unavailable", nil)
		}
		var req homeassistant.AttachOwner
		decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 16384))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&req); err != nil {
			return httpx.BadRequest(e, "Invalid owner attachment", nil)
		}
		var trailing any
		if !errors.Is(decoder.Decode(&trailing), io.EOF) {
			return httpx.BadRequest(e, "Invalid owner attachment", nil)
		}
		if err = store.Attach(e.Request.Context(), tenant, owner, req); err != nil {
			return httpx.NewAPIError(409, "attachment_rejected", "An owned pending enrollment, current runtime and authenticated native instance are required", nil)
		}
		return httpx.Success(e, 200, map[string]any{"binding_ref": req.BindingRef, "attached": true})
	})
}
