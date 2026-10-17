package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
)

func TestPreviewHTTPCannotSubmitStableWorkOrAdmitAnotherSubject(t *testing.T) {
	t.Setenv(config.EnvReleaseChannel, "preview")
	t.Setenv("TEST_RIL_PRO_USER_ID", "auth0|fixture")
	for _, tc := range []struct {
		subject, method, path string
		want                  int
	}{
		{"auth0|fixture", http.MethodGet, "/api/v1/stacks", http.StatusOK},
		{"auth0|fixture", http.MethodPost, "/api/v1/stacks", http.StatusConflict},
		{"auth0|customer", http.MethodGet, "/api/v1/stacks", http.StatusForbidden},
		{"", http.MethodGet, "/api/v1/stacks", http.StatusUnauthorized},
		{"auth0|fixture", http.MethodPost, "/internal/v1/datasubject/requests", http.StatusConflict},
		{"auth0|fixture", http.MethodGet, "/_internal/jobs/reclaim", http.StatusForbidden},
		{"auth0|fixture", http.MethodGet, "/api/v1/internal/runtime/summary", http.StatusForbidden},
		{"auth0|fixture", http.MethodGet, "/api/v1/agent/commands", http.StatusForbidden},
		{"auth0|fixture", http.MethodPost, "/webhook", http.StatusConflict},
		{"auth0|fixture", http.MethodGet, "/dashboard/reclaim", http.StatusForbidden},
		{"auth0|customer", http.MethodGet, "/api/v2/auth/login", http.StatusOK},
		{"auth0|customer", http.MethodGet, "/api/v2/auth/logout", http.StatusOK},
	} {
		router := httpx.NewRouter()
		execution := config.NewPreviewExecution()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, previewExecution: execution, log: logger.Default()})
		reached := false
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			router.Route(method, tc.path, func(e *httpx.Event) error { reached = true; return e.NoContent(http.StatusOK) })
		}
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.subject != "" {
			req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: tc.subject}))
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
			t.Fatalf("%s %s: status=%d reached=%v", tc.subject, tc.method, rec.Code, reached)
		}
	}
	for _, headers := range []http.Header{{"Upgrade": {"websocket"}}, {"Accept": {"text/event-stream"}}} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, log: logger.Default()})
		router.GET("/api/v1/stacks", func(e *httpx.Event) error {
			t.Error("preview entered a stream/upgrade handler")
			return e.NoContent(200)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
		req.Header = headers
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("stream/upgrade status=%d", rec.Code)
		}
	}
	for _, rawPath := range []string{"/api/v1%2Fhealth", "/api/v1%2fstacks"} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, log: logger.Default()})
		router.GET("/api/{rest...}", func(e *httpx.Event) error {
			t.Error("encoded slash reached a different catch-all handler")
			return e.NoContent(200)
		})
		req := httptest.NewRequest(http.MethodGet, rawPath, nil)
		req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "auth0|fixture"}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("encoded path %s admitted: %d", rawPath, rec.Code)
		}
	}
	// ServeMux may canonicalize malformed paths before dispatch; none may
	// execute a mutation or an internal handler through an alternate spelling.
	for _, rawPath := range []string{"//api/v1/stacks", "/api/v1/../v1/stacks", "/%5finternal/jobs/reclaim"} {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, routeDeps{startup: &startupContext{cfg: config.DefaultConfig()}, log: logger.Default()})
		router.POST("/api/v1/stacks", func(e *httpx.Event) error { t.Error("canonicalized request reached mutation"); return e.NoContent(200) })
		router.POST("/_internal/jobs/reclaim", func(e *httpx.Event) error {
			t.Error("canonicalized request reached internal handler")
			return e.NoContent(200)
		})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, rawPath, nil))
		if rec.Code < 300 {
			t.Fatalf("alternate path %s admitted: %d", rawPath, rec.Code)
		}
	}
	booted := false
	stableBootComponent(func() bool { booted = true; return true })
	if booted {
		t.Fatal("preview ran a stable-only startup constructor")
	}
	execution := config.NewPreviewExecution()
	background := startRuntimeLifecycle(context.Background(), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, logger.Default(), execution).background
	if scope := execution.Snapshot(); scope["queues"] != "unqualified" {
		t.Fatalf("background alone qualified startup: %v", scope)
	}
	background.run(func(context.Context) { t.Error("preview started a background consumer") })
	background.stopAndWait()
	for _, status := range []string{"", "ACTIVE", " active ", "revoked"} {
		membership := &controlplane.Membership{TenantID: " org_fixture ", UserID: " auth0|fixture ", Status: status}
		id := &identity.Identity{UserID: "auth0|fixture", OrgID: "org_fixture"}
		projected, err := resolveIdentityTenantProjection(nil, &v2Boot{saasMode: true}, id, membership, true)
		if status == "revoked" {
			if err == nil || projected != nil {
				t.Fatal("preview admitted a revoked membership")
			}
		} else if err != nil || projected != membership {
			t.Fatalf("established active membership %q denied: %v", status, err)
		}
	}
}
