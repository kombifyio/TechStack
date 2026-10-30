package stacks

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/layoutadvice"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const (
	wizardRunKindExtend              = specv2.KitAssignmentExtend
	wizardRunStateAccepted           = "accepted"
	wizardRunLayoutRecommendationKey = "layout_recommendation_id"
)

// layoutAcceptanceHook accepts the shown layout recommendation after the
// run's own projection and before the pinned CLI validates it: the record is
// revalidated against a fresh snapshot (owner decision D2) and only then are
// the accepted nodeRefs written into the proposal. A material change stops the
// run with the replacement recommendation so the owner is asked again.
func (h wizardRunHandlers) layoutAcceptanceHook(e *httpx.Event, run *wizardRunState, stackID string) func(*specv2.Projection) bool {
	acceptance := run.request.Intent.LayoutRecommendation
	if acceptance == nil {
		return nil
	}
	return func(projection *specv2.Projection) bool {
		if h.cfg.LayoutAdvice == nil {
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"The recommended distribution cannot be checked right now", wizardRunDetails(
					"layout_recommendations_unavailable", true, "Try again",
					"The recommendation could not be revalidated, so nothing was changed. Retry in a moment."))
			return true
		}
		scope := layoutadvice.Scope{TenantID: run.tenantID, OwnerID: run.ownerID}
		flow := layoutadvice.FlowAddServer
		if run.request.Intent.KitAssignment.Mode == wizardRunKindExtend {
			flow = layoutadvice.FlowAddUseCase
		}
		result, err := h.cfg.LayoutAdvice.AcceptForRun(e.Request.Context(), scope, *acceptance, stackID, flow, projection, run.runID)
		switch {
		case errors.Is(err, actions.ErrRecommendationNotFound), errors.Is(err, layoutadvice.ErrSelectionMismatch),
			errors.Is(err, actions.ErrRecommendationDigest), errors.Is(err, actions.ErrRecommendationState):
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
				"The recommended distribution is no longer current", wizardRunDetails(
					"layout_recommendation_stale", false, "Review the distribution again",
					"The distribution you accepted no longer matches this homelab. Nothing was changed."))
			return true
		case errors.Is(err, layoutadvice.ErrUnverifiable):
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
				"The recommended distribution cannot be checked against your servers right now", wizardRunDetails(
					"layout_recommendation_unverifiable", true, "Review the distribution again",
					"Your servers changed or cannot be checked right now, so the distribution was not applied. Nothing was changed."))
			return true
		case err != nil:
			logger.Default().Warn("layout_recommendation_revalidation_failed", "error", err, "recommendation_id", acceptance.ID)
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Recommendation revalidation failed", wizardRunDetails(
				"layout_recommendation_unavailable", true, "Try again",
				"The recommended distribution could not be revalidated, so nothing was changed."))
			return true
		case result.Replacement != nil:
			details := wizardRunDetails("layout_recommendation_superseded", false, "Review the new distribution",
				"Your homelab changed since the distribution was shown. Review the updated recommendation and confirm again; nothing was changed.")
			details["layout_recommendation"] = result.Replacement
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The recommended distribution changed", details)
			return true
		}
		if err := specv2.ApplyLayoutNodeRefs(projection.Spec, result.Assignments); err != nil {
			logger.Default().Warn("layout_recommendation_inapplicable", "error", err, "recommendation_id", acceptance.ID)
			_ = httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation, "The accepted distribution does not fit this run",
				wizardRunDetails("layout_recommendation_inapplicable", false, "Review the distribution again",
					"The accepted distribution does not match this run; nothing was changed."))
			return true
		}
		if result.Record != nil {
			run.layoutRecommendationID = result.Record.ID
		}
		return false
	}
}

// linkLayoutExecution binds the accepted record to the job carrying it out.
func (h wizardRunHandlers) linkLayoutExecution(e *httpx.Event, run *wizardRunState, jobID string) {
	if run.layoutRecommendationID == "" || strings.TrimSpace(jobID) == "" || h.cfg.LayoutAdvice == nil {
		return
	}
	scope := layoutadvice.Scope{TenantID: run.tenantID, OwnerID: run.ownerID}
	if err := h.cfg.LayoutAdvice.MarkExecuting(e.Request.Context(), scope, run.layoutRecommendationID, jobID); err != nil {
		logger.Default().Warn("layout_recommendation_execution_link_failed", "error", err, "recommendation_id", run.layoutRecommendationID, "job_id", jobID)
	}
}

// runWizardExtend adds use cases to an existing kit deployment without a new
// node. The accepted distribution places the new workloads on the
// deployment's own nodes; the run persists the CLI-validated proposal as the
// desired StackSpec and starts the rollout where the deployment is not
// already running. A running deployment applies a changed desired state
// through the governed Day-2 path, never through a second apply here.
func (h wizardRunHandlers) runWizardExtend(e *httpx.Event, run *wizardRunState) error {
	stack, findErr := h.crud.findOwnedStoreStack(e, run.request.Intent.KitAssignment.KitDeploymentID)
	if findErr != nil {
		return findErr
	}
	base, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok || specv2.RequireCanonicalV2(base) != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"This homelab has no canonical setup to extend yet", wizardRunDetails(
				"wizard_extend_requires_v2", false, "Finish the first rollout",
				"Use cases can be added once the homelab has its first validated setup; nothing was changed."))
	}
	if run.key == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Adding use cases requires an Idempotency-Key", wizardRunDetails(
				"wizard_projection_identity_requires_key", false, "Retry this attempt",
				"Submit the attempt with its Idempotency-Key; nothing was changed."))
	}
	if strings.TrimSpace(stack.Status) == "running" || (run.priorFailed != nil && run.priorFailed.Result[wizardExtendCheckpointKey] != nil) {
		return h.runWizardRunningExtend(e, run, stack, base)
	}
	if status := strings.TrimSpace(stack.Status); status == "provisioning" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The homelab is still being set up", wizardRunDetails(
				"wizard_extend_deployment_busy", true, "Try again when the setup finished",
				"Use cases can be added once the running setup finished; nothing was changed."))
	}
	// Rights and owner access come first, exactly as a deploy checks them, so
	// a refusal never leaves a changed desired setup behind.
	deploy := strings.TrimSpace(stack.Status) != "running" && h.crud.orch != nil
	var access ownerSpecBootstrapAccess
	if deploy {
		if walletErr := h.crud.requireWalletCreateRight(e, run.ownerID); walletErr != nil {
			return walletErr
		}
		var accessErr error
		if access, accessErr = h.crud.ownerSpecBootstrapAccessForStoreDeploy(e.Request.Context(), stack); accessErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
		}
	}
	intent := run.request.Intent
	if metadata, ok := base["metadata"].(map[string]any); ok {
		if name, ok := metadata["name"].(string); ok && strings.TrimSpace(name) != "" {
			intent.Name = name // extending never renames the deployment
		}
	}
	projection, handled := h.projectAndValidate(e, base, intent, run.homelabID, h.layoutAcceptanceHook(e, run, stack.ID))
	if handled {
		return nil
	}
	run.captureGoalActivations(projection)
	if homelabHandled := h.ensureWizardHomelab(e, run, stack.Name); homelabHandled {
		return nil
	}
	newConfig := map[string]any{}
	for key, value := range stack.Config {
		newConfig[key] = value
	}
	newConfig[stackConfigKeySpecV2] = projection.Spec
	updated, err := h.crud.stackStore.CompareAndSwapStackConfig(e.Request.Context(), controlplane.StackConfigCAS{
		TenantID: run.tenantID, StackID: stack.ID, ExpectedUpdatedAt: stack.UpdatedAt, Config: newConfig,
	})
	if errors.Is(err, controlplane.ErrConflict) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The homelab changed while the use cases were being added", wizardRunDetails(
				"wizard_extend_config_changed", true, "Deployment changed", "Reload and add the use cases again; nothing was changed."))
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to save the extended setup", nil)
	}
	if intentErr := h.persistWizardHomelabIntent(e.Request.Context(), run, stack.ID, projection); intentErr != nil {
		return h.failWizardIntentProjection(e, run, stack.ID, projection, intentErr)
	}
	state, jobID := wizardRunStateAccepted, ""
	if deploy {
		jobID, err = h.crud.orch.DeployStackWithOptions(stack.ID, orchestrator.ProvisionStackOptions{
			RequestContext: e.Request.Context(), TenantID: updated.TenantID, OwnerID: updated.OwnerSubjectID,
			StackName: updated.Name, IdempotencyKey: run.key, OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(access),
		})
		if err != nil {
			return h.crud.deployStartError(e, stack.ID, err)
		}
		state = wizardRunStateProvisioning
		h.linkLayoutExecution(e, run, jobID)
	}
	data := map[string]any{
		"run_kind":            run.effectiveKind,
		"requested_run_kind":  run.request.Intent.RunKind,
		"coerced":             run.coerced(),
		"homelab_id":          run.homelab.ID,
		"kit_assignment_mode": wizardRunKindExtend,
		"kit_slug":            wizardRunKitSlug(run.request, projection),
		creationStackIDField:  stack.ID,
		"node_id":             projection.NodeID,
		creationNameField:     stack.Name,
		wizardRunStateField:   state,
		"accepted_at":         time.Now().UTC(),
	}
	if jobID != "" {
		data[creationJobIDField] = jobID
	}
	if run.layoutRecommendationID != "" {
		data[wizardRunLayoutRecommendationKey] = run.layoutRecommendationID
	}
	addWizardProjectionFields(data, projection, h.cfg.ReleaseVersion)
	if err := h.recordWizardRun(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.ID, NodeID: projection.NodeID, JobID: jobID}, data); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard outcome could not be recorded; retry with the same Idempotency-Key", nil)
	}
	response := map[string]any{"run_id": run.runID}
	for key, value := range data {
		response[key] = value
	}
	return httpx.Success(e, http.StatusAccepted, response)
}

func firstNonEmptyWizardJob(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
