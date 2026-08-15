// Package routes provides REST API routes for kombifyTechstack.
// This file contains tunnel management routes for Sprint 14 (Seamless Networking).
package routes

import (
	"context"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/tunnel"
)

// RegisterTunnelRoutes registers tunnel management routes.
// This should be called from main.go during server setup.
func RegisterTunnelRoutes(r *httpx.Router, resolver *tunnel.RegistryURLResolver) {
	if resolver == nil {
		return
	}

	r.GET("/api/v1/tunnel/registry-url", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(e.Request.Context(), 90*time.Second)
		defer cancel()

		url, err := resolver.GetRegistryURL(ctx)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
		}

		return httpx.Success(e, http.StatusOK, map[string]any{
			"url":  url,
			"mode": string(resolver.GetCurrentMode()),
		})
	})

	r.GET("/api/v1/tunnel/status", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		status := resolver.Status()

		return httpx.Success(e, http.StatusOK, map[string]any{
			"mode":         string(status.Mode),
			"url":          status.URL,
			"valid_until":  status.ValidUntil,
			"tunnel_state": status.TunnelStatus.State.String(),
			"tunnel_error": status.TunnelStatus.Error,
			"local_port":   status.TunnelStatus.LocalPort,
			"tunnel_url":   status.TunnelStatus.URL,
		})
	})

	r.POST("/api/v1/tunnel/start", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(e.Request.Context(), 90*time.Second)
		defer cancel()

		url, err := resolver.GetRegistryURL(ctx)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
		}

		return httpx.Success(e, http.StatusOK, map[string]any{
			"url":  url,
			"mode": string(resolver.GetCurrentMode()),
		})
	})

	r.POST("/api/v1/tunnel/stop", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		err := resolver.Stop()
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
		}

		return httpx.Success(e, http.StatusOK, map[string]any{
			"message": "tunnel stopped",
		})
	})

	// Generate install commands for workers.
	// Tokens are accepted only in the request body so they don't leak through
	// URL logs, browser history, referrers, or reverse-proxy request paths.
	r.POST("/api/v1/tunnel/install-command", func(e *httpx.Event) error {
		return tunnelInstallCommand(e, resolver)
	})

	// Validate a given URL (used by UI to pre-check custom URLs)
	// POST /api/v1/tunnel/validate-url
	r.POST("/api/v1/tunnel/validate-url", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		var req struct {
			URL string `json:"url"`
		}

		if err := e.BindBody(&req); err != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "invalid request body", nil)
		}

		if req.URL == "" {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "url is required", nil)
		}

		err := tunnel.ValidateURL(req.URL)
		if err != nil {
			return httpx.Success(e, http.StatusOK, map[string]any{
				"valid":   false,
				"error":   err.Error(),
				"message": "This URL cannot be used for worker registration",
			})
		}

		return httpx.Success(e, http.StatusOK, map[string]any{
			"valid":   true,
			"message": "URL is valid for worker registration",
		})
	})

	// Network topology detection endpoint (Sprint 15.2)
	r.GET("/api/v1/tunnel/topology", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(e.Request.Context(), 10*time.Second)
		defer cancel()

		detector := tunnel.NewTopologyDetector(resolver.Log())
		topology, err := detector.Detect(ctx)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Topology detection failed", nil)
		}

		return httpx.Success(e, http.StatusOK, topology)
	})
}

func tunnelInstallCommand(e *httpx.Event, resolver *tunnel.RegistryURLResolver) error {
	if e.Request.Method != http.MethodPost {
		return httpx.MethodNotAllowed(e, e.Request.Method)
	}
	if _, err := requireAuth(e); err != nil {
		return err
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := e.BindBody(&req); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "invalid request body", nil)
	}
	if req.Token == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "token is required", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 90*time.Second)
	defer cancel()

	linuxCmd, err := resolver.GenerateInstallCommand(ctx, req.Token)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}

	windowsCmd, err := resolver.GenerateInstallCommandPS(ctx, req.Token)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}

	registryURL, _ := resolver.GetRegistryURL(ctx)

	return httpx.Success(e, http.StatusOK, map[string]any{
		"linux":        linuxCmd,
		"windows":      windowsCmd,
		"registry_url": registryURL,
	})
}
