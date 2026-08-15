package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func TestDocsRedirectRoutes(t *testing.T) {
	t.Parallel()
	router := httpx.NewRouter()
	RegisterDocsRedirectRoutes(router)

	for _, path := range []string{"/docs", "/docs/getting-started", "/docs/a/b/c"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusPermanentRedirect {
			t.Fatalf("%s status = %d, want 308", path, recorder.Code)
		}
		if got := recorder.Header().Get("Location"); got != docsRedirectTarget {
			t.Fatalf("%s location = %q, want %q", path, got, docsRedirectTarget)
		}
	}
}
