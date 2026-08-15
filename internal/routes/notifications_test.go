package routes

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// fakeEngineServer stands in for the notifications engine and records what it saw.
type engineCapture struct {
	method, path, query, auth string
	body                      string
}

func newFakeEngine(t *testing.T, respStatus int, respBody string, cap *engineCapture) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		cap.body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newNotificationsHandler(t *testing.T, engineURL string) notificationsHandler {
	t.Helper()
	t.Setenv("NOTIFICATIONS_ENGINE_URL", engineURL)
	t.Setenv("SERVICE_AUTH_SECRET", "test-service-secret")
	return notificationsHandler{engine: notifications.NewEngineFromEnv()}
}

func authedEvent(method, target, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec, Auth: &httpx.Principal{Id: "auth0|user-1"}}, rec
}

func bearerClaims(t *testing.T, auth string) map[string]any {
	t.Helper()
	tok := strings.TrimPrefix(auth, "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", auth)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNotificationsFeed_ProxiesAuthedUser(t *testing.T) {
	cap := &engineCapture{}
	h := newNotificationsHandler(t, newFakeEngine(t, 200, `{"feed":[{"id":"f1","subject":"hi"}]}`, cap))

	ev, rec := authedEvent(http.MethodGet, "/api/v1/notifications/feed?limit=30", "")
	if err := h.getFeed(ev); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"f1"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/notifications/feed" {
		t.Fatalf("engine path %s", cap.path)
	}
	if !strings.Contains(cap.query, "auth0_user_id=auth0%7Cuser-1") || !strings.Contains(cap.query, "limit=30") {
		t.Fatalf("engine query %s", cap.query)
	}
	claims := bearerClaims(t, cap.auth)
	if claims["iss"] != "kombify-techstack" || claims["scope"] != "notifications:read" {
		t.Fatalf("claims %v", claims)
	}
}

func TestNotificationsFeed_RequiresAuth(t *testing.T) {
	cap := &engineCapture{}
	h := newNotificationsHandler(t, newFakeEngine(t, 200, `{"feed":[]}`, cap))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/feed", nil)
	rec := httptest.NewRecorder()
	ev := &httpx.Event{Request: req, Response: rec} // no Auth

	_ = h.getFeed(ev)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	if cap.path != "" {
		t.Fatal("engine must not be called when unauthenticated")
	}
}

func TestNotificationsMarkRead_PathParamWriteScope(t *testing.T) {
	cap := &engineCapture{}
	h := newNotificationsHandler(t, newFakeEngine(t, 200, `{"item":{"id":"item-9","read_at":"now"}}`, cap))

	ev, rec := authedEvent(http.MethodPost, "/api/v1/notifications/feed/item-9/read", "")
	ev.Request.SetPathValue("id", "item-9")
	if err := h.markRead(ev); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if cap.method != http.MethodPost || cap.path != "/v1/notifications/feed/item-9/read" {
		t.Fatalf("engine %s %s", cap.method, cap.path)
	}
	if bearerClaims(t, cap.auth)["scope"] != "notifications:write" {
		t.Fatal("mutation must use write scope")
	}
}

func TestNotificationsPutPreferences_ForwardsBody(t *testing.T) {
	cap := &engineCapture{}
	h := newNotificationsHandler(t, newFakeEngine(t, 200, `{"topics":[]}`, cap))

	body := `{"topics":[{"topic_key":"product.news","category":"product","channels":{"email":{"enabled":true,"locked":false}}}]}`
	ev, rec := authedEvent(http.MethodPut, "/api/v1/notifications/preferences", body)
	if err := h.putPreferences(ev); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if cap.method != http.MethodPut || cap.path != "/v1/notifications/preferences" {
		t.Fatalf("engine %s %s", cap.method, cap.path)
	}
	if !strings.Contains(cap.body, "product.news") {
		t.Fatalf("body not forwarded: %s", cap.body)
	}
}

func TestNotifications_NotConfigured(t *testing.T) {
	t.Setenv("NOTIFICATIONS_ENGINE_URL", "http://unused")
	t.Setenv("SERVICE_AUTH_SECRET", "")
	h := notificationsHandler{engine: notifications.NewEngineFromEnv()}

	ev, rec := authedEvent(http.MethodGet, "/api/v1/notifications/feed", "")
	if err := h.getFeed(ev); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}
