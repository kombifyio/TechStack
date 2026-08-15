// Package stacks provides CRUD handlers for stack management.
package stacks

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func legacyJobTypeForPersistence(jobType string) string {
	switch jobType {
	case "provision", "destroy", "update", "restart":
		return jobType
	case "deploy", "drift_check":
		return "update"
	case "drift_resolve":
		return "restart"
	default:
		return "update"
	}
}

func legacyJobRecordFields(jobType, stackID, currentStep string) map[string]any {
	return map[string]any{
		"type":         legacyJobTypeForPersistence(jobType),
		"state":        "pending",
		"progress":     0,
		"stack_id":     stackID,
		"current_step": currentStep,
	}
}

// createLegacyJob creates a job record without orchestrator execution (legacy mode).
func createLegacyJob(app core.App, jobType, stackID, currentStep, stackStatus string) (string, error) {
	jobsCollection, err := app.FindCollectionByNameOrId("jobs")
	if err != nil {
		return "", err
	}

	job := core.NewRecord(jobsCollection)
	for key, value := range legacyJobRecordFields(jobType, stackID, currentStep) {
		job.Set(key, value)
	}
	if stack, lookupErr := app.FindRecordById("stacks", stackID); lookupErr == nil {
		if tenantID := strings.TrimSpace(stack.GetString("tenant_id")); tenantID != "" {
			job.Set("tenant_id", tenantID)
		}
	}

	if saveErr := app.Save(job); saveErr != nil {
		return "", saveErr
	}

	// Update stack status
	stack, err := app.FindRecordById("stacks", stackID)
	if err != nil {
		return job.Id, nil // Job created but stack update failed - still return job ID
	}
	stack.Set("status", stackStatus)
	app.Save(stack) // Best effort

	return job.Id, nil
}

func RegisterCRUDRoutesWithModeAndFeatures(r *httpx.Router, app core.App, orch *orchestrator.Orchestrator, mode config.DeploymentMode, featureChecker managedRuntimeFeatureChecker, managedLeases jobs.ManagedLeaseManager, notificationOutbox productnotifications.ProductEventEnqueuer) {
	if !mode.IsValid() {
		mode = config.ModeSelfHosted
	}
	stores := currentControlPlaneStores()
	h := crudRouteHandlers{
		app:                app,
		orch:               orch,
		deploymentMode:     mode,
		stackStore:         stores.Stacks,
		homelabStore:       stores.Homelabs,
		jobStore:           stores.Jobs,
		walletStore:        stores.Wallet,
		serverStore:        stores.Servers,
		routingStore:       stores.Routing,
		runtimeFeatures:    featureChecker,
		managedLeases:      managedLeases,
		notificationOutbox: notificationOutbox,
	}

	// GET /api/v1/stacks - List stacks owned by the authenticated user.
	r.GET("/api/v1/stacks", h.listStacks)

	// GET /api/v1/homelab - Resolve the caller's homelab umbrella (ADR-0036)
	// with its kit deployments.
	r.GET("/api/v1/homelab", h.getHomelab)

	// PATCH /api/v1/homelab - Rename the caller's homelab. The generated
	// starting name is a placeholder, not a product name.
	r.PATCH("/api/v1/homelab", h.renameHomelab)

	// GET /api/v1/stacks/{id} - Read one stack owned by the authenticated user.
	r.GET("/api/v1/stacks/{id}", h.getStack)

	// POST /api/v1/stacks - Create a stack and immediately start the Unifier/provision job.
	// IMPORTANT: This endpoint stores the raw user config even if it is invalid.
	// Validation and mapping happen asynchronously in the job (shown on /stacks/creating).
	r.POST("/api/v1/stacks", h.createStack)

	// GET /api/v1/stacks/{id}/owner-spec - StackKit bootstrap-only owner/recovery handoff.
	r.GET("/api/v1/stacks/{id}/owner-spec", h.ownerSpec)

	// POST /api/v1/stacks/{id}/provision - Start provisioning a stack
	r.POST("/api/v1/stacks/{id}/provision", h.provisionStack)

	// POST /api/v1/stacks/{id}/deploy - Rollout Homelab (final unify + IaC + tofu apply)
	r.POST("/api/v1/stacks/{id}/deploy", h.deployStack)

	// POST /api/v1/stacks/{id}/stackkit/operations - Dispatch one closed,
	// typed StackKits lifecycle operation to an enrolled mTLS agent.
	r.POST("/api/v1/stacks/{id}/stackkit/operations", h.startStackKitLifecycle)

	// POST /api/v1/stacks/{id}/resume-enrollment - Resume one overdue
	// waiting_enrollment rollout on its already-created exact managed VM.
	r.POST("/api/v1/stacks/{id}/resume-enrollment", h.resumeStackEnrollment)

	// POST /api/v1/stacks/{id}/retry-rollout - Retry only the failed rollout
	// on its exact already-active managed runtime target.
	r.POST("/api/v1/stacks/{id}/retry-rollout", h.retryStackRollout)

	// POST /api/v1/stacks/{id}/jobs/{jobId}/abandon - Retire one rollout that
	// is still marked running but has stopped reporting, so the stack becomes
	// retryable again instead of holding its execution claim forever.
	r.POST("/api/v1/stacks/{id}/jobs/{jobId}/abandon", h.abandonStaleStackJob)

	// Revisioned desired routing for an exact stack/server target. Managed
	// targets additionally bind the exact provider lease.
	r.GET("/api/v1/stacks/{id}/routing", h.getStackRouting)
	r.PUT("/api/v1/stacks/{id}/routing", h.putStackRouting)
	r.GET("/api/v1/stacks/{id}/routing/targets", h.listStackRoutingTargets)

	// POST /api/v1/stacks/{id}/destroy - Destroy a stack
	r.POST("/api/v1/stacks/{id}/destroy", h.destroyStack)

	// POST /api/internal/stacks/domain-attach - Attach a managed/purchased domain to the
	// caller's stack (kombify-Cloud handover). Service-JWT authenticated (NOT user auth);
	// delegates an exact stack/server/lease tuple to revisioned routing desired state.
	r.POST("/api/internal/stacks/domain-attach", h.attachDomainToStack)

	// GET /api/internal/stacks/{id}/ingress - main-node IP for the managed-domain
	// lifecycle reconciler (service-JWT auth; 404 ingress_pending while provisioning).
	r.GET("/api/internal/stacks/{id}/ingress", h.stackIngress)

	// GET /api/v1/jobs/stats - Get job queue statistics
	r.GET("/api/v1/jobs/stats", h.jobStats)
}
