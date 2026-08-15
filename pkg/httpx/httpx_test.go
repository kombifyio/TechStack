package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/go-common/identity"
	"github.com/kombifyio/techstack/pkg/api"
)

func TestRouterPathValueAndSuccessEnvelope(t *testing.T) {
	r := NewRouter()
	r.GET("/api/v1/agents/{id}", func(e *Event) error {
		return Success(e, http.StatusOK, map[string]string{"id": e.Request.PathValue("id")})
	})
	mux := r.BuildMux()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/abc123", nil)
	req.Header.Set("X-Request-ID", "req-42")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Meta api.ResponseMeta `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%q", err, rec.Body.String())
	}
	if got.Data.ID != "abc123" {
		t.Errorf("PathValue id: got %q want abc123", got.Data.ID)
	}
	if got.Meta.RequestID != "req-42" {
		t.Errorf("meta request_id: got %q want req-42", got.Meta.RequestID)
	}
	if got.Meta.Timestamp == "" {
		t.Error("meta timestamp not injected")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q", ct)
	}
}

func TestMiddlewareChainNextOrderAndShortCircuit(t *testing.T) {
	var order []string
	r := NewRouter()
	r.BindFunc(func(e *Event) error {
		order = append(order, "mw1-before")
		err := e.Next()
		order = append(order, "mw1-after")
		return err
	})
	g := r.Group("/api")
	g.BindFunc(func(e *Event) error {
		order = append(order, "grp-before")
		return e.Next()
	})
	g.GET("/x", func(e *Event) error {
		order = append(order, "handler")
		return Success(e, http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	want := "mw1-before,grp-before,handler,mw1-after"
	if strings.Join(order, ",") != want {
		t.Fatalf("chain order: got %q want %q", strings.Join(order, ","), want)
	}

	// Short-circuit: middleware that does NOT call Next() must skip the handler.
	order = nil
	handlerRan := false
	r2 := NewRouter()
	r2.BindFunc(func(e *Event) error {
		return Forbidden(e, "blocked")
	})
	r2.GET("/y", func(e *Event) error {
		handlerRan = true
		return Success(e, http.StatusOK, "ok")
	})
	rec2 := httptest.NewRecorder()
	r2.BuildMux().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/y", nil))
	if handlerRan {
		t.Error("handler ran despite short-circuit middleware")
	}
	if rec2.Code != http.StatusForbidden {
		t.Errorf("short-circuit status: got %d want 403", rec2.Code)
	}
}

func TestErrorEnvelopeHelpers(t *testing.T) {
	cases := []struct {
		name     string
		handler  HandlerFunc
		wantCode int
		wantErr  api.ErrorCode
	}{
		{"badrequest", func(e *Event) error { return BadRequest(e, "bad") }, 400, api.ErrCodeBadRequest},
		{"unauthorized", func(e *Event) error { return Unauthorized(e, "") }, 401, api.ErrCodeUnauthorized},
		{"forbidden", func(e *Event) error { return Forbidden(e, "") }, 403, api.ErrCodeForbidden},
		{"notfound", func(e *Event) error { return NotFound(e, "") }, 404, api.ErrCodeNotFound},
		{"methodnotallowed", func(e *Event) error { return MethodNotAllowed(e, "POST") }, 405, api.ErrCodeMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter()
			r.GET("/e", tc.handler)
			rec := httptest.NewRecorder()
			r.BuildMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/e", nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("status: got %d want %d", rec.Code, tc.wantCode)
			}
			var got api.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Error.Code != tc.wantErr {
				t.Errorf("error code: got %q want %q", got.Error.Code, tc.wantErr)
			}
		})
	}
}

func TestReturnedAPIErrorRenders(t *testing.T) {
	r := NewRouter()
	r.GET("/boom", func(e *Event) error {
		return NewNotFoundError("missing widget", nil)
	})
	r.GET("/panic", func(e *Event) error {
		return context.DeadlineExceeded // plain error -> 500
	})

	rec := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != 404 {
		t.Fatalf("APIError status: got %d want 404", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec2.Code != 500 {
		t.Fatalf("plain error status: got %d want 500", rec2.Code)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	r := NewRouter()
	r.GET("/only", func(e *Event) error { return Success(e, 200, "ok") })
	mux := r.BuildMux()

	// Unregistered path -> catch-all 404 envelope.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("unknown path: got %d want 404 body=%q", rec.Code, rec.Body.String())
	}

	// Wrong method on a known path -> ServeMux 405.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/only", nil))
	if rec2.Code != 405 {
		t.Fatalf("wrong method: got %d want 405", rec2.Code)
	}
}

func TestBindBodyJSON(t *testing.T) {
	r := NewRouter()
	type payload struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	var captured payload
	r.POST("/b", func(e *Event) error {
		if err := e.BindBody(&captured); err != nil {
			return BadRequest(e, "bind failed")
		}
		return Success(e, 200, captured)
	})

	body := bytes.NewBufferString(`{"name":"alpha","n":7}`)
	req := httptest.NewRequest(http.MethodPost, "/b", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d want 200 body=%q", rec.Code, rec.Body.String())
	}
	if captured.Name != "alpha" || captured.N != 7 {
		t.Errorf("bind: got %+v want {alpha 7}", captured)
	}
}

func TestBindBodyForm(t *testing.T) {
	r := NewRouter()
	type form struct {
		Title string `form:"title"`
		Count int    `form:"count"`
		On    bool   `form:"on"`
	}
	var captured form
	r.POST("/f", func(e *Event) error {
		if err := e.BindBody(&captured); err != nil {
			return BadRequest(e, "bind: "+err.Error())
		}
		return Success(e, 200, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/f", strings.NewReader("title=hello&count=3&on=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d want 200 body=%q", rec.Code, rec.Body.String())
	}
	if captured.Title != "hello" || captured.Count != 3 || !captured.On {
		t.Errorf("form bind: got %+v", captured)
	}
}

func TestEventResponseDirectAccessAndPrincipal(t *testing.T) {
	r := NewRouter()
	r.GET("/whoami", func(e *Event) error {
		// Direct ResponseWriter access (header set + http.Error path parity).
		e.Response.Header().Set("X-Custom", "1")
		if e.Auth == nil {
			return Unauthorized(e, "no principal")
		}
		return Success(e, 200, map[string]string{
			"id":   e.Auth.Id,
			"role": e.Auth.GetString("role"),
			"org":  e.Auth.GetString("org_id"),
		})
	})

	// Inject identity into context the way edge-identity middleware does.
	id := &identity.Identity{UserID: "user-9", OrgID: "org-1", Roles: []string{"admin"}}
	ctx := identity.NewContext(context.Background(), id)
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.BuildMux().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d want 200 body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Custom") != "1" {
		t.Error("direct Response.Header().Set lost")
	}
	var got struct {
		Data map[string]string `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Data["id"] != "user-9" || got.Data["role"] != "admin" || got.Data["org"] != "org-1" {
		t.Errorf("principal: got %+v", got.Data)
	}

	// Superuser derivation from admin role.
	if p := NewPrincipal(id); p == nil || !p.IsSuperuser() {
		t.Error("admin role should yield superuser principal")
	}
	if NewPrincipal(nil) != nil {
		t.Error("nil identity should yield nil principal")
	}
}

func TestServerGracefulShutdown(t *testing.T) {
	r := NewRouter()
	r.GET("/ping", func(e *Event) error { return Success(e, 200, "pong") })
	srv := NewServer(r, ServerConfig{Addr: "127.0.0.1:0"})

	// Drive ServeHTTP directly to confirm wiring without binding a port.
	rec := httptest.NewRecorder()
	srv.HTTPServer().Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if rec.Code != 200 {
		t.Fatalf("server handler: got %d want 200", rec.Code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestBuildMuxOverlappingWildcardRoutesDoNotPanic guards the 405-fallback
// registration against ServeMux pattern conflicts. Two distinct method-scoped
// routes can have overlapping wildcard paths whose method-less forms conflict
// (regression: the kombify.me backups routes panicked the control-plane at
// startup). BuildMux must not panic, and both routes must still dispatch for
// their correct methods.
func TestBuildMuxOverlappingWildcardRoutesDoNotPanic(t *testing.T) {
	r := NewRouter()
	r.POST("/api/v1/backups/{name}/upload-s3", func(e *Event) error {
		return e.String(http.StatusOK, "upload")
	})
	r.DELETE("/api/v1/backups/s3/{name}", func(e *Event) error {
		return e.String(http.StatusOK, "delete-s3")
	})

	var mux *http.ServeMux
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("BuildMux panicked on overlapping wildcard routes: %v", rec)
			}
		}()
		mux = r.BuildMux()
	}()

	cases := []struct {
		method, path, want string
	}{
		{http.MethodPost, "/api/v1/backups/report.tar.gz/upload-s3", "upload"},
		{http.MethodDelete, "/api/v1/backups/s3/report.tar.gz", "delete-s3"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("%s %s: got code=%d body=%q, want 200 containing %q", c.method, c.path, rec.Code, rec.Body.String(), c.want)
		}
	}
}
