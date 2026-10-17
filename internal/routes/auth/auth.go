// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// RouteConfig carries optional auth route dependencies that are only available
// in the composed runtime.
type RouteConfig struct {
	Runtime               RuntimeStore
	PortalSession         PortalSession
	LocalSetupProvisioner LocalSetupProvisioner
	LocalOwnerStore       LocalOwnerLookup
	// WalletSource reports where this deployment keeps credentials; the UI
	// reads it from GET /api/v1/auth/mode. The zero value is the legacy wallet.
	WalletSource kombifywallet.Source
}

// RegisterRoutesWithConfig registers all auth-related API routes with optional
// runtime dependencies.
func RegisterRuntimeRoutesWithConfig(r *httpx.Router, mode config.DeploymentMode, edition config.Edition, cfg RouteConfig) {
	// Auth mode detection - returns current auth mode and related config
	r.GET("/api/v1/auth/mode", withWalletSource(cfg.WalletSource, getRuntimeAuthMode(cfg.Runtime, mode, edition, cfg.LocalOwnerStore)))

	// First-run setup wizard (public, one-shot, blocked after completion)
	r.POST("/api/v1/auth/setup", handleRuntimeSetup(cfg.Runtime, mode, cfg.LocalSetupProvisioner, cfg.LocalOwnerStore))

	// Stack identity routes live with the homelab (internal/routes/stacks):
	// the homelab name is the Stack Identity name.

	// Provider logout for hosted kombify Cloud login.
	r.GET("/api/v1/auth/logout", handleRuntimeOIDCLogout())

	// Browser-facing root paths, formerly owned by the Node SSR layer. They
	// redirect into the v2 auth handlers (see web_redirects.go).
	r.GET("/auth/callback", handleWebAuthCallbackRedirect())
	r.GET("/auth/logout", handleWebAuthLogoutRedirect(cfg.PortalSession.CookieName, mode))
	r.GET("/auth/cloud-logout", handleCloudLogoutRedirect(mode))

	// Cloud-link flow: connect a kombify Cloud profile to the local operator
	// account (PKCE authorization-code flow, used by the cloud-linked owner
	// source in the stack creation wizard).
	r.POST("/api/v1/auth/cloud-link/start", handleRuntimeCloudLinkStart(cfg.Runtime, mode))
	r.GET("/api/v1/auth/cloud-link/callback", handleRuntimeCloudLinkCallback(cfg.Runtime, mode))
	r.GET("/api/v1/auth/cloud-link/status", getRuntimeCloudLinkStatus(cfg.Runtime))
	r.DELETE("/api/v1/auth/cloud-link", deleteRuntimeCloudLink(cfg.Runtime))

	// SSO routes for kombify Cloud Portal integration
	RegisterRuntimeSSORoutes(r, cfg.PortalSession)
}
