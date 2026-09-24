package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

// RegisterKombifyMeRoutes wires read-only kombify.me registry diagnostics.
//
// StackKits own kombify.me registration and service exposure during
// stack-spec.yaml generation/apply. TechStack only lists the registry state for
// diagnostics and cleanup verification. Both reads require an authenticated
// caller. With the shared platform identity configured (SaaS), kombify.me holds
// every tenant's managed addresses under one account, so the reads are scoped
// to the caller's own owner binding and never reveal another tenant's routes.
func RegisterKombifyMeRoutes(r *httpx.Router) {
	cfg := kombifyme.DefaultConfigFromEnv()
	client := kombifyme.NewClient(cfg)
	platformIdentity := strings.TrimSpace(cfg.APIKey) != ""

	r.GET("/api/v1/tunnel/subdomains", func(e *httpx.Event) error {
		ownerRef, err := kombifyMeCallerScope(e, platformIdentity)
		if err != nil {
			return err
		}
		path := "subdomains/"
		if ownerRef != "" {
			path = "subdomains/?binding_owner_ref=" + url.QueryEscape(ownerRef)
		}
		return forwardKombifyMe(e, client, http.MethodGet, path, nil)
	})

	r.GET("/api/v1/tunnel/subdomains/{baseId}/services", func(e *httpx.Event) error {
		ownerRef, err := kombifyMeCallerScope(e, platformIdentity)
		if err != nil {
			return err
		}
		baseID := url.PathEscape(e.Request.PathValue("baseId"))
		if ownerRef != "" {
			ctx, cancel := context.WithTimeout(e.Request.Context(), 20*time.Second)
			defer cancel()
			base, lookupErr := client.Forward(ctx, http.MethodGet, "subdomains/"+baseID, nil, "")
			var boundOwner any
			if lookupErr == nil && base != nil {
				if body, ok := base.Body.(map[string]any); ok {
					boundOwner = body["binding_owner_ref"]
				}
			}
			if boundOwner != ownerRef {
				return httpx.NotFound(e, "kombify.me address not found")
			}
		}
		return forwardKombifyMe(e, client, http.MethodGet, fmt.Sprintf("subdomains/%s/services", baseID), nil)
	})
}

// kombifyMeCallerScope authenticates the caller and, when the shared platform
// identity answers the registry, returns the caller's owner binding reference.
func kombifyMeCallerScope(e *httpx.Event, platformIdentity bool) (string, error) {
	userID, ok := authenticatedUserID(e)
	if !ok {
		return "", httpx.Unauthorized(e, "Authentication required")
	}
	if !platformIdentity {
		return "", nil
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), userID, "techstack.kombify-me.subdomains.read")
	if tenantErr != nil {
		return "", tenantErr
	}
	ownerRef, err := kombifyme.OwnerRef(tenantID, userID)
	if err != nil {
		return "", httpx.Forbidden(e, "kombify.me addresses require an owner and tenant")
	}
	return ownerRef, nil
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
