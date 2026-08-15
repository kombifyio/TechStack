package routes

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterNotificationsRoutes mounts the notification-center proxy. The browser
// calls these same-origin; the SvelteKit [...path] proxy forwards them here, and
// this backend signs the engine call on the authenticated user's behalf.
func RegisterNotificationsRoutes(r *httpx.Router) {
	h := notificationsHandler{engine: notifications.NewEngineFromEnv()}
	r.GET("/api/v1/notifications/feed", h.getFeed)
	r.POST("/api/v1/notifications/feed/{id}/read", h.markRead)
	r.POST("/api/v1/notifications/feed/{id}/dismiss", h.dismiss)
	r.POST("/api/v1/notifications/feed/read-all", h.markAllRead)
	r.GET("/api/v1/notifications/preferences", h.getPreferences)
	r.PUT("/api/v1/notifications/preferences", h.putPreferences)
}

type notificationsHandler struct {
	engine *notifications.Engine
}

// passthrough writes the engine's status + JSON body verbatim. The engine's
// shapes ({feed}/{item}/{updated}/{topics}/{error}) are exactly what the shared
// UI component reads, so no re-wrapping is needed.
func (notificationsHandler) passthrough(e *httpx.Event, res *notifications.Result) error {
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(res.Status)
	_, _ = e.Response.Write(res.Body)
	return nil
}

// transportError maps a failed engine call (not an engine HTTP error, which is
// passed through) to an error shape the UI component understands ({"error":...}).
func (notificationsHandler) transportError(e *httpx.Event, err error) error {
	if errors.Is(err, notifications.ErrNotConfigured) {
		return e.JSON(http.StatusServiceUnavailable, map[string]string{"error": "notifications_unavailable"})
	}
	return e.JSON(http.StatusBadGateway, map[string]string{"error": "notifications_engine_unreachable"})
}

func (h notificationsHandler) getFeed(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	limit := 20
	if raw := e.Request.URL.Query().Get("limit"); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil {
			limit = n
		}
	}
	res, err := h.engine.Feed(e.Request.Context(), userID, limit)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}

func (h notificationsHandler) markRead(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	itemID := e.Request.PathValue("id")
	if itemID == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "item_id_required"})
	}
	res, err := h.engine.MarkRead(e.Request.Context(), userID, itemID)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}

func (h notificationsHandler) dismiss(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	itemID := e.Request.PathValue("id")
	if itemID == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "item_id_required"})
	}
	res, err := h.engine.Dismiss(e.Request.Context(), userID, itemID)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}

func (h notificationsHandler) markAllRead(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	res, err := h.engine.MarkAllRead(e.Request.Context(), userID)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}

func (h notificationsHandler) getPreferences(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	res, err := h.engine.GetPreferences(e.Request.Context(), userID)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}

func (h notificationsHandler) putPreferences(e *httpx.Event) error {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, 1<<20))
	if err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_body"})
	}
	res, err := h.engine.PutPreferences(e.Request.Context(), userID, body)
	if err != nil {
		return h.transportError(e, err)
	}
	return h.passthrough(e, res)
}
