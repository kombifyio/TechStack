package stacks

import (
	"context"
	"errors"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// WizardExistingServers adapts the existing canonical server assignment
// authority without creating another inventory or deployment executor.
type WizardExistingServers interface {
	Resolve(context.Context, WizardExistingServerRequest) error
	Assign(context.Context, WizardExistingServerRequest) error
}

type WizardExistingServerRequest struct {
	TenantID string
	OwnerID  string
	StackID  string
	ServerID string
}

func (h wizardRunHandlers) runWizardExistingServer(e *httpx.Event, run *wizardRunState, projection *specv2.Projection, normalized normalizedCreateStackRequest) error {
	if run.key == "" {
		return httpx.BadRequest(e, "Installing on an existing server requires an Idempotency-Key", nil)
	}
	if run.request.Managed != nil || run.request.Remote != nil || run.request.Substrate != nil {
		return httpx.BadRequest(e, "An existing enrolled server cannot request provisioning or reconnection", nil)
	}
	// Owner-bootstrap options are a generic map on the older create contract.
	// Refuse provider fields there too, so this route cannot admit a provider
	// request hidden outside the explicit managed section.
	policy := runtimePolicyConfigFromRequest(normalized)
	if hasManagedRuntimeFields(policy, runtimeFieldsFromConfig(policy)) {
		return httpx.BadRequest(e, "An existing enrolled server cannot request a managed runtime", nil)
	}
	if h.cfg.ExistingServers == nil || h.crud.orch == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Existing-server installation authority is unavailable", nil)
	}
	target := WizardExistingServerRequest{
		TenantID: run.tenantID, OwnerID: run.ownerID,
		StackID:  deterministicStackID(run.tenantID, run.ownerID, run.key),
		ServerID: strings.TrimSpace(run.request.Intent.Server.ExistingServerID),
	}
	if err := h.cfg.ExistingServers.Resolve(e.Request.Context(), target); err != nil {
		return writeWizardExistingServerError(e, err)
	}
	if err := h.crud.requireWalletCreateRight(e, run.ownerID); err != nil {
		return err
	}
	if err := h.crud.requireWalletForCreate(e, run.ownerID, normalized); err != nil {
		return err
	}
	if handled := h.ensureWizardHomelab(e, run, run.request.Intent.Name); handled {
		return nil
	}
	normalized.HomelabID = run.homelab.ID
	normalized.StackSpecV2 = projection.Spec
	normalized.UserConfig["stackkit"] = run.request.Intent.KitAssignment.KitSlug
	stack, err := h.crud.persistStackWithRequestHash(e, run.ownerID, run.tenantID, normalized, run.requestHash)
	if err != nil || stack == nil {
		return err
	}
	if err := h.persistWizardHomelabIntent(e.Request.Context(), run, stack.Id, projection); err != nil {
		return h.failWizardIntentProjection(e, run, stack.Id, projection, err)
	}
	if err := h.crud.applyCreateOwnerBootstrap(e, run.ownerID, run.tenantID, stack, normalized); err != nil {
		return err
	}
	access, err := h.crud.issueCreateOwnerSpecAccess(e, stack, run.ownerID, run.tenantID, normalized)
	if err != nil {
		return err
	}
	// Re-read immediately before CAS: another installation may have claimed
	// this host while the pinned CLI validated the requested configuration.
	if err := h.cfg.ExistingServers.Assign(e.Request.Context(), target); err != nil {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "existing_server_assignment_failed")
		return writeWizardExistingServerError(e, err)
	}
	jobSpec := createStackJobSpec(normalized)
	jobSpec[stackConfigKeySpecV2] = projection.Spec
	jobID, err := h.crud.orch.ProvisionStackWithOptions(stack.Id, jobSpec, orchestrator.ProvisionStackOptions{
		RequestContext: e.Request.Context(), TenantID: run.tenantID, OwnerID: run.ownerID,
		StackName: stack.Name, IdempotencyKey: "wizard-existing-server:" + run.key,
		ExistingServerID: target.ServerID, AutoDeploy: true, OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(access),
	})
	if err != nil {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "existing_server_deploy_failed")
		return writeWizardExistingServerError(e, err)
	}
	return h.finishWizardFound(e, run, wizardFoundOutcome{
		stack: stack, name: stack.Name, serverID: target.ServerID, jobID: jobID,
		autoDeploy: true, projection: projection, ownerSpecAccess: access,
	})
}

func writeWizardExistingServerError(e *httpx.Event, err error) error {
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Existing server not found")
	}
	if errors.Is(err, controlplane.ErrConflict) || errors.Is(err, orchestrator.ErrNoAssignedWorkers) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The selected server must be owned, unassigned, approved and currently connected through Guard", wizardRunDetails(
				"wizard_existing_server_not_admitted", true, "Server not ready", "Refresh the server's Guard connection and select an available server."))
	}
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		"Existing-server installation could not be admitted; retry with the same Idempotency-Key", wizardRunDetails(
			"wizard_existing_server_unavailable", true, "Installation unavailable", "The server was retained. Retry this installation with the same attempt key."))
}
