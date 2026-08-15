package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestActivityRouteFiltersCanonicalScopesAndReturnsCursor(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for _, event := range []controlplane.ActivityEvent{
		{ID: "a", TenantID: "user-1", StackID: "stack-1", ServerScopeKey: "server-1", ServiceScopeKey: "service-1", CreatedAt: now, Action: "update"},
		{ID: "foreign-service", TenantID: "user-1", StackID: "stack-1", ServerScopeKey: "server-1", ServiceScopeKey: "service-2", CreatedAt: now.Add(time.Second), Action: "update"},
		{ID: "foreign-tenant", TenantID: "tenant-2", ServerScopeKey: "server-1", ServiceScopeKey: "service-1", CreatedAt: now.Add(2 * time.Second), Action: "update"},
	} {
		if _, err := store.AppendActivity(t.Context(), event); err != nil {
			t.Fatalf("append activity: %v", err)
		}
	}

	router := httpx.NewRouter()
	RegisterActivityRoutes(router, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/activity?server_id=server-1&service_id=service-1", nil)
	request = request.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "user-1", OrgID: "user-1"}))
	recorder := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(recorder, request)

	var payload struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0]["id"] != "a" || payload.Data.NextCursor == "" {
		t.Fatalf("unexpected scoped activity response: %#v", payload.Data)
	}
	if payload.Data.Items[0]["scope_status"] != "scoped" {
		t.Fatalf("expected explicit scoped status, got %#v", payload.Data.Items[0])
	}
	if _, _, err := decodeActivityCursor(payload.Data.NextCursor); err != nil {
		t.Fatalf("next cursor is invalid: %v", err)
	}
}

func TestActivityResponseKeepsUnresolvedEntriesVisibleAsUnscoped(t *testing.T) {
	response := activityResponse(controlplane.ActivityEvent{
		ID:        "unresolved",
		TenantID:  "tenant-1",
		Action:    "observe",
		CreatedAt: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
	})

	if response["scope_status"] != "unscoped" {
		t.Fatalf("expected unresolved activity to remain visible as unscoped, got %#v", response)
	}
}
