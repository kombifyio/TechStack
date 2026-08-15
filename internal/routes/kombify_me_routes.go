package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

// RegisterKombifyMeRoutes wires read-only kombify.me registry diagnostics.
//
// StackKits own kombify.me registration and service exposure during
// stack-spec.yaml generation/apply. TechStack only lists the registry state for
// diagnostics and cleanup verification.
func RegisterKombifyMeRoutes(r *httpx.Router) {
	client := kombifyme.NewClient(kombifyme.DefaultConfigFromEnv())

	r.GET("/api/v1/tunnel/subdomains", func(e *httpx.Event) error {
		return forwardKombifyMe(e, client, http.MethodGet, "subdomains/", nil)
	})

	r.GET("/api/v1/tunnel/subdomains/{baseId}/services", func(e *httpx.Event) error {
		return forwardKombifyMe(
			e,
			client,
			http.MethodGet,
			fmt.Sprintf("subdomains/%s/services", url.PathEscape(e.Request.PathValue("baseId"))),
			nil,
		)
	})
}

func forwardKombifyMe(e *httpx.Event, client *kombifyme.Client, method, upstreamPath string, body any) error {
	ctx, cancel := context.WithTimeout(e.Request.Context(), 20*time.Second)
	defer cancel()

	resp, err := client.Forward(ctx, method, upstreamPath, body, e.Request.Header.Get("Authorization"))
	if err != nil {
		var upstreamErr *kombifyme.UpstreamError
		if errors.As(err, &upstreamErr) {
			return httpx.Error(e, upstreamErr.Status, errorCodeForStatus(upstreamErr.Status), "kombify.me registry request failed", upstreamErr.Body)
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, err.Error(), nil)
	}

	return httpx.Success(e, resp.Status, resp.Body)
}

func errorCodeForStatus(status int) ksapi.ErrorCode {
	switch status {
	case http.StatusUnauthorized:
		return ksapi.ErrCodeUnauthorized
	case http.StatusForbidden:
		return ksapi.ErrCodeForbidden
	case http.StatusNotFound:
		return ksapi.ErrCodeNotFound
	case http.StatusConflict:
		return ksapi.ErrCodeConflict
	case http.StatusTooManyRequests:
		return ksapi.ErrCodeRateLimited
	case http.StatusBadRequest:
		return ksapi.ErrCodeValidation
	default:
		if status >= 500 {
			return ksapi.ErrCodeUnavailable
		}
		return ksapi.ErrCodeBadRequest
	}
}
