// Package stacks provides the main entry point for stack-related API routes.
// It aggregates all stack handlers including CRUD operations and import/export.
package stacks

import (
	"github.com/pocketbase/pocketbase/core"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func RegisterRoutesWithModeAndFeatures(r *httpx.Router, app core.App, orch *orchestrator.Orchestrator, mode config.DeploymentMode, featureChecker managedRuntimeFeatureChecker, managedLeases jobs.ManagedLeaseManager, notificationOutbox productnotifications.ProductEventEnqueuer) {
	// Register CRUD routes for stack management
	// Handles: POST /api/v1/stacks, POST /api/v1/stacks/{id}/provision, etc.
	RegisterCRUDRoutesWithModeAndFeatures(r, app, orch, mode, featureChecker, managedLeases, notificationOutbox)

	// Register import/export routes for configuration management
	// Handles: GET /api/v1/stacks/{id}/export, POST /api/v1/stacks/import
	RegisterImportExportRoutesWithModeAndFeatures(r, app, orch, mode, featureChecker)

	// Register spec access routes for persisted specs
	// Handles: GET /api/v1/stacks/{id}/requirements, GET /api/v1/stacks/{id}/unified, etc.
	RegisterSpecRoutes(r, app)
}
