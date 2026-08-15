// Package routes provides REST API routes for kombifyTechstack.
// Auth routes have been refactored into the auth/ subpackage.
package routes

import (
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/internal/routes/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type LocalOwnerSetup = auth.LocalOwnerSetup
type LocalSetupProvisioner = auth.LocalSetupProvisioner

type AuthRouteConfig struct {
	PortalSession         auth.PortalSession
	LocalSetupProvisioner LocalSetupProvisioner
}

// RegisterAuthRoutesWithConfig registers auth routes with runtime-only
// dependencies such as the V2 local setup provisioner.
func RegisterAuthRoutesWithConfig(r *httpx.Router, app core.App, mode config.DeploymentMode, edition config.Edition, cfg AuthRouteConfig) {
	auth.RegisterRoutesWithConfig(r, app, mode, edition, auth.RouteConfig{
		PortalSession:         cfg.PortalSession,
		LocalSetupProvisioner: cfg.LocalSetupProvisioner,
	})
}

// RegisterInternalRoutes registers internal API routes for trusted service-to-service
// communication (Edge → Stack). Only active in SaaS deployment mode.
//
// Routes:
//   - POST /api/internal/sso/exchange - Exchange Edge identity for PB token
//   - POST /api/internal/feature-flags/apply - Push feature flag overrides
func RegisterInternalRoutes(r *httpx.Router, app core.App, mode config.DeploymentMode) {
	auth.RegisterInternalRoutes(r, app, mode)
}
