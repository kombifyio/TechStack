package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

type countingVMLeaseHandler struct {
	calls int
}

func (h *countingVMLeaseHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls++
	w.WriteHeader(http.StatusNoContent)
}

func TestRegisterVMLeaseRoutesExposesInventoryOnly(t *testing.T) {
	router := httpx.NewRouter()
	handler := &countingVMLeaseHandler{}
	RegisterVMLeaseRoutes(router, handler)

	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/internal/vm-leases/lease-1"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s %s status = %d, want %d", request.method, request.path, recorder.Code, http.StatusNoContent)
		}
	}

	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/internal/vm-leases"},
		{method: http.MethodPatch, path: "/api/v1/internal/vm-leases/lease-1"},
		{method: http.MethodPost, path: "/api/v1/internal/vm-leases/lease-1/validate"},
		{method: http.MethodGet, path: "/api/v1/internal/vm-leases/lease-1/desired-spec"},
		{method: http.MethodPost, path: "/api/v1/internal/vm-leases/lease-1/executor-status"},
		{method: http.MethodPost, path: "/api/v1/internal/vm-leases/lease-1/executor-commands/provision"},
	} {
		callsBefore := handler.calls
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		if handler.calls != callsBefore {
			t.Fatalf("retired route %s %s reached VM-lease handler", request.method, request.path)
		}
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("retired route %s %s status = %d, want 404 or 405", request.method, request.path, recorder.Code)
		}
	}
}
