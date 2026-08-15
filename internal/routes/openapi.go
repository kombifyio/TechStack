package routes

import (
	"github.com/kombifyio/techstack/pkg/httpx"
	"net/http"

	"github.com/kombifyio/techstack/api/openapi"
)

// RegisterOpenAPIRoutes serves the OpenAPI spec at /api/v1/openapi.yaml.
func RegisterOpenAPIRoutes(r *httpx.Router) {
	r.GET("/api/v1/openapi.yaml", func(e *httpx.Event) error {
		e.Response.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		e.Response.Header().Set("Cache-Control", "public, max-age=3600")
		e.Response.WriteHeader(http.StatusOK)
		_, err := e.Response.Write(openapi.Spec)
		return err
	})
}
