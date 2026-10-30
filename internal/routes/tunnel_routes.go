// Package routes provides REST API routes for kombifyTechstack.
// This file contains tunnel management routes for Sprint 14 (Seamless Networking).
package routes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/routes/trust"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/tunnel"
)

// RegisterTunnelRoutes registers tunnel management routes.
// This should be called from main.go during server setup.
// pairing mints the enrollment token of an install command that names none.
func RegisterTunnelRoutes(r *httpx.Router, resolver *tunnel.RegistryURLResolver, pairing trust.RouteStores) {
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

		response := map[string]any{
			"url":  url,
			"mode": string(resolver.GetCurrentMode()),
		}
		if pin := resolver.LocalBridgePin(url); pin != "" {
			response["lan_pin"] = pin
		}
		return httpx.Success(e, http.StatusOK, response)
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

		response := map[string]any{
			"url":  url,
			"mode": string(resolver.GetCurrentMode()),
		}
		if pin := resolver.LocalBridgePin(url); pin != "" {
			response["lan_pin"] = pin
		}
		return httpx.Success(e, http.StatusOK, response)
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
		return tunnelInstallCommand(e, resolver, pairing)
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

// tunnelInstallCommand answers the install commands for a new server. A
// caller that names no pairing token gets a fresh one minted for it, so an
// agent never handles a token it did not receive from Techstack.
func tunnelInstallCommand(e *httpx.Event, resolver *tunnel.RegistryURLResolver, pairing trust.RouteStores) error {
	if e.Request.Method != http.MethodPost {
		return httpx.MethodNotAllowed(e, e.Request.Method)
	}
	userID, err := requireAuth(e)
	if err != nil {
		return err
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := e.BindBody(&req); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "invalid request body", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 90*time.Second)
	defer cancel()

	var minted *trust.MintedStackPairing
	if req.Token == "" {
		// No token is minted for a command that cannot be generated.
		if _, err := resolver.GetRegistryURL(ctx); err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
		}
		if pairing.Workers == nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Pairing token authority is temporarily unavailable", map[string]any{
				"reason_code": "pairing_token_authority_unavailable", "retryable": true,
			})
		}
		tenantID, err := tenantguard.TenantScope(requestExplicitTenantID(e), userID, "techstack.trust.pairing-tokens.create")
		if err != nil {
			return err
		}
		token, err := trust.MintStackPairingTokenOnly(ctx, pairing, tenantID, userID, nil, trust.PairingTokenParams{Name: "Install command"})
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, token.FailureMessage(), nil)
		}
		minted, req.Token = &token, token.Token
	}

	linuxCmd, err := resolver.GenerateInstallCommand(ctx, req.Token)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}

	windowsCmd, err := resolver.GenerateInstallCommandPS(ctx, req.Token)
	if errors.Is(err, tunnel.ErrLANBridgeLinuxOnly) {
		windowsCmd, err = "", nil
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}

	registryURL, _ := resolver.GetRegistryURL(ctx)

	response := map[string]any{
		"linux":        linuxCmd,
		"registry_url": registryURL,
	}
	// The LAN bridge has no Windows command; the key is then absent.
	if windowsCmd != "" {
		response["windows"] = windowsCmd
	}
	if minted != nil {
		response["pairing_token_id"] = minted.TokenID
		response["expires_at"] = minted.ExpiresAt
	}
	return httpx.Success(e, http.StatusOK, response)
}
