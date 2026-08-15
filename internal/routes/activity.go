package routes

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterActivityRoutes exposes the durable audit/activity lane using the
// same canonical tenant/server/service scope keys as runtime logs.
func RegisterActivityRoutes(r *httpx.Router, store controlplane.ActivityStore) {
	if r == nil || store == nil {
		return
	}
	r.GET("/api/v1/activity", func(e *httpx.Event) error {
		ownerID, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenantID := requestTenantID(e, ownerID)
		if tenantID == "" {
			return httpx.RejectUnauthorized(e, "Authenticated tenant required")
		}

		filter, err := activityFilterFromRequest(e.Request)
		if err != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
		}
		events, err := store.ListActivityScoped(e.Request.Context(), tenantID, filter)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to list activity", nil)
		}
		items := make([]map[string]any, 0, len(events))
		for _, event := range events {
			items = append(items, activityResponse(event))
		}
		nextCursor := ""
		if len(events) > 0 {
			last := events[len(events)-1]
			nextCursor = encodeActivityCursor(last.CreatedAt, last.ID)
		}
		return httpx.Success(e, http.StatusOK, map[string]any{
			"items":       items,
			"next_cursor": nextCursor,
		})
	})
}

func activityFilterFromRequest(req *http.Request) (controlplane.ActivityFilter, error) {
	filter := controlplane.ActivityFilter{Limit: 50}
	if req == nil {
		return filter, nil
	}
	query := req.URL.Query()
	filter.StackID = query.Get("stack_id")
	filter.RuntimeScopeKey = query.Get("runtime_scope_key")
	filter.ServerScopeKey = firstNonEmptyRouteString(query.Get("server_scope_key"), query.Get("server_id"))
	filter.ServiceScopeKey = firstNonEmptyRouteString(query.Get("service_scope_key"), query.Get("service_id"))
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return filter, fmt.Errorf("limit must be a positive integer")
		}
		filter.Limit = parsed
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		createdAt, id, err := decodeActivityCursor(raw)
		if err != nil {
			return filter, fmt.Errorf("invalid activity cursor")
		}
		filter.CursorCreatedAt = createdAt
		filter.CursorID = id
	}
	return filter, nil
}

func activityResponse(event controlplane.ActivityEvent) map[string]any {
	scopeStatus := "unscoped"
	if strings.TrimSpace(event.RuntimeScopeKey) != "" {
		scopeStatus = "scoped"
	}
	return map[string]any{
		"id":                event.ID,
		"stack_id":          event.StackID,
		"runtime_scope_key": event.RuntimeScopeKey,
		"server_scope_key":  event.ServerScopeKey,
		"service_scope_key": event.ServiceScopeKey,
		"scope_status":      scopeStatus,
		"correlation_id":    event.CorrelationID,
		"action":            event.Action,
		"category":          event.Category,
		"severity":          event.Severity,
		"message":           event.Message,
		"details":           event.Details,
		"created_at":        event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func encodeActivityCursor(createdAt time.Time, id string) string {
	payload := createdAt.UTC().Format(time.RFC3339Nano) + "\n" + strings.TrimSpace(id)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeActivityCursor(cursor string) (time.Time, string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(payload), "\n", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return time.Time{}, "", fmt.Errorf("cursor identity missing")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return createdAt.UTC(), strings.TrimSpace(parts[1]), nil
}
