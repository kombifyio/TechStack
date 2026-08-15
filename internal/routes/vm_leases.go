package routes

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func RegisterVMLeaseRoutes(r *httpx.Router, handler http.Handler) {
	if r == nil || handler == nil {
		return
	}
	adapter := func(e *httpx.Event) error {
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	}

	r.GET("/api/v1/internal/vm-leases/{id}", adapter)
}
