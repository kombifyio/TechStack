// Package auth provides REST API routes for authentication configuration.
package auth

import (
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

// RouteConfig carries optional auth route dependencies that are only available
// in the composed runtime.
type RouteConfig struct {
	PortalSession         PortalSession
	LocalSetupProvisioner LocalSetupProvisioner
}

// RegisterRoutesWithConfig registers all auth-related API routes with optional
// runtime dependencies.
func RegisterRoutesWithConfig(r *httpx.Router, app core.App, mode config.DeploymentMode, edition config.Edition, cfg RouteConfig) {
	// Auth mode detection - returns current auth mode and related config
	r.GET("/api/v1/auth/mode", getAuthMode(app, mode, edition))

	// First-run setup wizard (public, one-shot, blocked after completion)
	r.POST("/api/v1/auth/setup", handleSetup(app, mode, cfg.LocalSetupProvisioner))

	// Stack identity persistence and retrieval
	r.GET("/api/v1/auth/stack-identity", getStackIdentity(app, mode))
	r.PUT("/api/v1/auth/stack-identity", updateStackIdentity(app, mode))

	// OIDC callback/logout for hosted kombify Cloud login (legacy v1 flow)
	r.GET("/api/v1/auth/callback", handleOIDCCallbackFull(app))
	r.GET("/api/v1/auth/logout", handleOIDCLogout(app))

	// Browser-facing root paths, formerly owned by the Node SSR layer. They
	// redirect into the v2 auth handlers (see web_redirects.go); the legacy v1
	// flow stays reachable via its canonical /api/v1/auth/* paths above.
	r.GET("/auth/callback", handleWebAuthCallbackRedirect())
	r.GET("/auth/logout", handleWebAuthLogoutRedirect(cfg.PortalSession.CookieName, mode))
	r.GET("/auth/cloud-logout", handleCloudLogoutRedirect(mode))

	// Cloud-link flow: connect a kombify Cloud profile to the local operator
	// account (PKCE authorization-code flow, used by the cloud-linked owner
	// source in the stack creation wizard).
	r.POST("/api/v1/auth/cloud-link/start", handleCloudLinkStart(app))
	r.GET("/api/v1/auth/cloud-link/callback", handleCloudLinkCallback(app))
	r.GET("/api/v1/auth/cloud-link/status", getCloudLinkStatus(app))
	r.DELETE("/api/v1/auth/cloud-link", deleteCloudLink(app))

	// SSO routes for kombify Cloud Portal integration
	RegisterSSORoutes(r, app, cfg.PortalSession)
}
