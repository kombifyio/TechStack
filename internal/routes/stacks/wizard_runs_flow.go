// Wizard-run flow lanes (found / join), shared dispatch, pairing mint, and
// ledger recording. Split from wizard_runs.go to keep each lane readable.
//
// Resume model: every side-effectful partial failure records a failed ledger
// entry, and a same-key retry converges on the earlier outcome — the found
// lane through the deterministic keyed stack id (skip re-dispatch, re-mint
// pairing), the join lane through the recorded node id (skip re-append).
package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/routes/trust"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// runWizardFound creates a new kit deployment: seed -> project -> validate
// (fail closed, nothing persisted on rejection) -> create-stack core ->
// dispatch -> pairing (self-host transports) -> ledger.
func (h wizardRunHandlers) runWizardFound(e *httpx.Event, run *wizardRunState) error {
	if handled := h.resolveWizardStackName(e, run); handled {
		return nil
	}
	projection, handled := h.projectFoundSpec(e, run)
	if handled {
		return nil
	}
	normalized, handled := h.normalizeFoundRequest(e, run)
	if handled {
		return nil
	}
	// Top-level stackkit keeps the provision payload on the StackKits
	// handoff shape. The Architecture v2 projection in StackSpecV2 is the
	// document the pinned CLI executes.
	normalized.UserConfig["stackkit"] = run.request.Intent.KitAssignment.KitSlug
	if strings.TrimSpace(run.request.Intent.Server.ExistingServerID) != "" {
		return h.runWizardExistingServer(e, run, projection, normalized)
	}
	if homelabHandled := h.ensureWizardHomelab(e, run, run.request.Intent.Name); homelabHandled {
		return nil
	}
	normalized.HomelabID = run.homelab.ID
	normalized.StackSpecV2 = projection.Spec

	if walletErr := h.crud.requireWalletForCreate(e, run.ownerID, normalized); walletErr != nil {
		return walletErr
	}

	// The wire-request hash keeps a byte-identical retry replayable: the
	// normalized request is mutated by owner-bootstrap resolution (fresh
	// generated recovery material) and would never hash stably.
	stack, err := h.crud.persistStackWithRequestHash(e, run.ownerID, run.tenantID, normalized, run.requestHash)
	if err != nil || stack == nil {
		return err
	}
	if stack.Name != "" {
		// resolveWizardStackName pre-resolved collisions, so a persist-level
		// auto-rename only fires on a create/create race; adopt it for the
		// row-facing fields and accept the spec's contract id divergence for
		// that race (documented in the bead).
		normalized.Name = stack.Name
	}
	if intentErr := h.persistWizardHomelabIntent(e.Request.Context(), run, stack.Id, projection); intentErr != nil {
		return h.failWizardIntentProjection(e, run, stack.Id, projection, intentErr)
	}
	serverID, preparedLease, admissionHandled, admissionErr := h.crud.admitManagedCreate(e, run.ownerID, run.tenantID, stack, normalized)
	if admissionHandled || admissionErr != nil {
		h.crud.markStackProvisionStartFailed(e.Request.Context(), stack.Id, run.tenantID)
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "managed_admission_failed")
		return admissionErr
	}
	var serverErr error
	if serverID == "" && wizardRunTransport(run.request) != specv2.TransportHypervisor {
		serverID, serverErr = h.crud.persistCreateServerIntent(e, run.ownerID, run.tenantID, stack, normalized)
	}
	if serverErr != nil {
		h.crud.markStackProvisionStartFailed(e.Request.Context(), stack.Id, run.tenantID)
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "server_intent_failed")
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to persist server intent", map[string]any{
				creationStackIDField: stack.Id, creationOperationsURLField: homelabDashboardURL(),
			})
	}
	if bootstrapErr := h.crud.applyCreateOwnerBootstrap(e, run.ownerID, run.tenantID, stack, normalized); bootstrapErr != nil {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "owner_bootstrap_failed")
		return bootstrapErr
	}
	ownerSpecAccess, accessErr := h.crud.issueCreateOwnerSpecAccess(e, stack, run.ownerID, run.tenantID, normalized)
	if accessErr != nil {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "owner_spec_access_failed")
		return accessErr
	}

	jobSpec := createStackJobSpec(normalized)
	jobSpec[stackConfigKeySpecV2] = projection.Spec
	jobID, autoDeploy := "", false
	if wizardRunTransport(run.request) != specv2.TransportHypervisor {
		jobID, autoDeploy, handled = h.dispatchWizardProvision(e, run, stack, jobSpec, ownerSpecAccess, preparedLease)
		if handled {
			return nil
		}
	}
	return h.finishWizardFound(e, run, wizardFoundOutcome{
		stack:           stack,
		name:            normalized.Name,
		serverID:        serverID,
		jobID:           jobID,
		autoDeploy:      autoDeploy,
		projection:      projection,
		ownerSpecAccess: ownerSpecAccess,
	})
}

// resolveWizardStackName pre-resolves duplicate-name collisions BEFORE the
// projection so the spec's contract id and the stack row share one name (a
// persist-level auto-rename would otherwise diverge them). Resume runs pin
// the name to the already-persisted keyed stack instead.
func (h wizardRunHandlers) resolveWizardStackName(e *httpx.Event, run *wizardRunState) bool {
	if run.resumeStack != nil {
		run.request.Intent.Name = run.resumeStack.Name
		return false
	}
	ctx := e.Request.Context()
	base := strings.TrimSpace(run.request.Intent.Name)
	for attempt := 0; attempt < maxStackNameAutoFixAttempts; attempt++ {
		candidate := stackNameCandidate(base, attempt)
		_, err := h.crud.stackStore.GetActiveStackByName(ctx, run.tenantID, run.ownerID, candidate)
		if err == nil {
			continue
		}
		if !errorsIsNotFound(err) {
			_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to resolve the deployment name", nil)
			return true
		}
		run.request.Intent.Name = candidate
		return false
	}
	run.request.Intent.Name = uniqueStackNameFallback(base)
	return false
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, controlplane.ErrNotFound)
}

// wizardFoundOutcome bundles the persisted/dispatched facts of a found run so
// finishWizardFound stays below the package's 4-argument threshold.
type wizardFoundOutcome struct {
	stack           *persistedStack
	name            string
	serverID        string
	jobID           string
	autoDeploy      bool
	projection      *specv2.Projection
	ownerSpecAccess ownerSpecBootstrapAccess
}

// finishWizardFound mints the pairing handoff (self-host transports),
// persists the homelab intent, records the ledger entry, and writes the 202.
func (h wizardRunHandlers) finishWizardFound(e *httpx.Event, run *wizardRunState, outcome wizardFoundOutcome) error {
	pairingJobID := ""
	operationID := ""
	var appliance *jobs.CustomerGuestBinding
	if wizardRunTransport(run.request) != specv2.TransportKombifyCloud && strings.TrimSpace(run.request.Intent.Server.ExistingServerID) == "" {
		if wizardRunTransport(run.request) == specv2.TransportConnectRemote {
			if err := h.persistWizardRemoteConnectionBinding(e.Request.Context(), run, outcome.stack.Id); err != nil {
				h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
					StackID: outcome.stack.Id, NodeID: outcome.projection.NodeID, JobID: outcome.jobID,
				}, "remote_connection_persist_failed")
				return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
					"Could not persist the remote SSH connection", wizardRunDetails(
						"wizard_remote_connection_persist_failed", true,
						"Connection not saved",
						err.Error(),
					))
			}
		}
		minted, pairingHandled := h.mintWizardPairing(e, run, &controlplane.Stack{ID: outcome.stack.Id, TenantID: run.tenantID}, outcome.stack.Name, outcome.projection)
		if pairingHandled {
			h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
				StackID: outcome.stack.Id, NodeID: outcome.projection.NodeID, JobID: outcome.jobID,
			}, "pairing_mint_failed")
			return nil
		}
		pairingJobID = minted.JobID
		if wizardRunTransport(run.request) == specv2.TransportConnectRemote {
			h.startWizardRemoteEnrollment(e, run, outcome.stack.Id, minted, outcome.serverID)
		}
		if wizardRunTransport(run.request) == specv2.TransportHypervisor {
			guest, failed := h.provisionWizardGuest(e, run, outcome.projection, outcome.stack.Id, minted, outcome.ownerSpecAccess)
			if failed {
				return nil
			}
			outcome.jobID = guest.JobID
			outcome.serverID = guest.ServerID
			operationID = guest.OperationID
			appliance = guest.Appliance
		}
	}

	data := map[string]any{
		"run_kind":                 run.effectiveKind,
		"requested_run_kind":       run.request.Intent.RunKind,
		"coerced":                  run.coerced(),
		"homelab_id":               run.homelab.ID,
		"kit_assignment_mode":      wizardRunKindFound,
		"kit_slug":                 run.request.Intent.KitAssignment.KitSlug,
		creationStackIDField:       outcome.stack.Id,
		"server_id":                outcome.serverID,
		"node_id":                  outcome.projection.NodeID,
		creationNameField:          outcome.name,
		creationJobIDField:         outcome.jobID,
		wizardRunStateField:        wizardRunStateProvisioning,
		"auto_deploy":              outcome.autoDeploy,
		creationOperationsURLField: homelabDashboardURL(),
	}
	if pairingJobID != "" {
		data["pairing_job_id"] = pairingJobID
	}
	if wizardRunTransport(run.request) == specv2.TransportConnectRemote {
		if plannedID := strings.TrimSpace(outcome.serverID); plannedID != "" {
			data[wizardRunPlannedServerID] = plannedID
		}
	}
	if operationID != "" {
		data["operation_id"] = operationID
	}
	if appliance != nil {
		data["appliance"] = appliance
	}
	if outcome.stack.IdempotentReplay {
		data["idempotent_replay"] = true
	}
	addWizardProjectionFields(data, outcome.projection, h.cfg.ReleaseVersion)

	if err := h.recordWizardRun(e.Request.Context(), run, controlplane.WizardRun{
		StackID: outcome.stack.Id, NodeID: outcome.projection.NodeID, JobID: outcome.jobID, PairingJobID: pairingJobID,
	}, data); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard outcome could not be recorded; retry with the same Idempotency-Key", nil)
	}

	response := map[string]any{"run_id": run.runID}
	for key, value := range data {
		response[key] = value
	}
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(response, outcome.ownerSpecAccess))
}

// projectFoundSpec seeds, projects, and CLI-validates the new deployment's
// spec. Validation runs BEFORE any persistence: a rejected projection is a
// 422 and leaves no state behind (inverse of the preview contract).
func (h wizardRunHandlers) projectFoundSpec(e *httpx.Event, run *wizardRunState) (*specv2.Projection, bool) {
	if h.cfg.Seeds == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Kit seed templates are not configured", wizardRunDetails(
				"wizard_seed_templates_unavailable", true,
				"Server missing seed templates",
				"The server has no TECHSTACK_STACKKIT_SPEC_TEMPLATES configured; wizard runs cannot project a kit spec.",
			))
		return nil, true
	}
	seed, seedErr := h.cfg.Seeds.Seed(run.request.Intent.KitAssignment.KitSlug)
	if seedErr != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Kit seed unavailable", wizardRunDetails(
				"wizard_seed_unavailable", true,
				"Kit seed unavailable",
				"The requested kit's seed template could not be read: "+seedErr.Error(),
			))
		return nil, true
	}
	projection, handled := h.projectAndValidate(e, seed, run.request.Intent, run.homelabID)
	if !handled {
		run.captureGoalActivations(projection)
	}
	return projection, handled
}

// projectAndValidate runs the shared projection + pinned-CLI validation gate
// for both lanes.
func (h wizardRunHandlers) projectAndValidate(e *httpx.Event, seed map[string]any, intent specv2.WizardIntent, homelabID string, beforeValidate ...func(*specv2.Projection) bool) (*specv2.Projection, bool) {
	if h.cfg.Projector == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"StackKits goal authoring is not configured", wizardRunDetails(
				"wizard_projector_unavailable", true,
				"Projector unavailable",
				"The pinned StackKits goal author is not admitted on this server; wizard runs fail closed without it.",
			))
		return nil, true
	}
	projection, projErr := h.cfg.Projector.Project(e.Request.Context(), seed, intent, homelabID)
	if projErr != nil {
		details := wizardRunDetails(
			"wizard_projection_rejected", false,
			"This Node could not be added",
			"The wizard intent cannot be projected onto the kit spec.",
		)
		details["projection_error"] = projErr.Error()
		addWizardRejectedIntent(details, intent, nil, h.cfg.ReleaseVersion)
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Wizard projection rejected: "+projErr.Error(), details)
		return nil, true
	}
	for _, hook := range beforeValidate {
		if hook != nil && hook(projection) {
			return nil, true
		}
	}
	if h.cfg.Validator == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"StackKits validator is not configured", wizardRunDetails(
				"wizard_validator_unavailable", true,
				"Validator unavailable",
				"The pinned StackKits CLI is not admitted on this server; wizard runs fail closed without it.",
			))
		return nil, true
	}
	if validateErr := h.cfg.Validator.ValidateSpec(e.Request.Context(), projection.Spec); validateErr != nil {
		details := wizardRunDetails(
			"wizard_spec_rejected", false,
			"Spec rejected by StackKits",
			"The selected configuration was rejected by the pinned StackKits release. Your choices are retained; review them and submit again. Nothing was persisted or applied.",
		)
		details["validate_error"] = validateErr.Error()
		addWizardRejectedIntent(details, intent, projection.UnmappedGoals, h.cfg.ReleaseVersion)
		_ = httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Projected spec failed StackKits validation", details)
		return nil, true
	}
	return projection, false
}

// addWizardRejectedIntent returns the desired, secret-free intent for review.
// Unfulfilled goals describe the rejected creation as a whole, not a guess at
// which individual workload caused the CLI error. Release-declared recorded
// goals remain separate; a validation failure never turns an installation
// request into a recorded preference or consumes its idempotency key.
func addWizardRejectedIntent(details map[string]any, intent specv2.WizardIntent, recorded []string, releaseVersion string) {
	unfulfilled := make([]string, 0, len(intent.Goals))
	for _, goal := range intent.Goals {
		if !slices.Contains(recorded, goal) {
			unfulfilled = append(unfulfilled, goal)
		}
	}
	details["requested_intent"] = intent
	details["unfulfilled_goals"] = unfulfilled
	details["recorded_goals"] = recorded
	details["release_version"] = releaseVersion
	details["creation_started"] = false
	details["remediation"] = "review_configuration_and_resubmit"
}

// normalizeFoundRequest synthesizes the create-stack request from the wizard
// run and passes it through the exact create-core gates: normalize,
// deployment lane, managed entitlement (fail closed), owner bootstrap.
func (h wizardRunHandlers) normalizeFoundRequest(e *httpx.Event, run *wizardRunState) (normalizedCreateStackRequest, bool) {
	req := h.synthesizeCreateRequest(run)
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		_ = httpx.BadRequest(e, msg)
		return normalized, true
	}
	if laneMsg := validateDeploymentLane(normalized, h.crud.deploymentMode); laneMsg != "" {
		_ = httpx.BadRequest(e, laneMsg)
		return normalized, true
	}
	if rejected, entitlementErr := h.crud.rejectUnauthorizedManagedRuntime(e, run.ownerID, normalized); rejected || entitlementErr != nil {
		return normalized, true
	}
	normalized, denial := resolveCreateOwnerBootstrap(normalized, h.crud.ownerBootstrapContextForCreate(e, run.ownerID, normalized))
	if denial != nil {
		_ = denial.write(e)
		return normalized, true
	}
	canonicalConfig, providerErr := canonicalizeFreshProvisionSpec(runtimePolicyConfigFromRequest(normalized))
	if providerErr != nil {
		_ = httpx.BadRequest(e, "Invalid provider selection: "+providerErr.Error(), nil)
		return normalized, true
	}
	if providerID := fieldString(canonicalConfig, "provider_id"); providerID != "" {
		normalized.ProviderID = providerID
		applyCanonicalProviderID(normalized.UserConfig, providerID)
	}
	return normalized, false
}

// synthesizeCreateRequest maps the wizard run onto the legacy create-stack
// wire shape so the create core (runtime fields, entitlement detection, kit
// normalization) applies unchanged.
func (h wizardRunHandlers) synthesizeCreateRequest(run *wizardRunState) createStackRequest {
	request := run.request
	options := map[string]interface{}{}
	for key, value := range request.Owner {
		options[key] = value
	}
	options[creationServerProvisionModeField] = wizardRunTransport(request)
	options["stackkit"] = request.Intent.KitAssignment.KitSlug
	if managed := request.Managed; managed != nil {
		if managed.RuntimeOfferingID != "" {
			options["runtime_offering_id"] = managed.RuntimeOfferingID
		}
		if managed.ProviderRegion != "" {
			options["provider_region"] = managed.ProviderRegion
		}
		if managed.IONOSDatacenter != "" {
			options["ionos_datacenter"] = managed.IONOSDatacenter
		}
	}
	if remote := request.Remote; remote != nil {
		if remote.Host != "" {
			options["server_remote_host"] = remote.Host
		}
		if remote.Port != nil && *remote.Port > 0 {
			options["server_remote_port"] = *remote.Port
		}
		if remote.User != "" {
			options["server_remote_user"] = remote.User
		}
		if remote.AuthMethod != "" {
			options["server_remote_auth_method"] = remote.AuthMethod
		}
		if remote.SSHKeyLabel != "" {
			options["server_remote_ssh_key_label"] = remote.SSHKeyLabel
		}
		if remote.UseSudo {
			options["server_remote_use_sudo"] = true
		}
	}
	providerID := ""
	if request.Managed != nil {
		providerID = request.Managed.ProviderID
	}
	return createStackRequest{
		Name:       request.Intent.Name,
		Mode:       stackModeEasy,
		ProviderID: providerID,
		Services:   request.Services,
		Options:    options,
	}
}

// wizardRunTransport resolves the run's provisioning lane; empty defaults to
// the self-host agent one-liner.
func wizardRunTransport(request wizardRunRequest) string {
	transport := strings.TrimSpace(request.Intent.Server.Transport)
	if transport == "" {
		return specv2.TransportInstallCommand
	}
	return transport
}

// dispatchWizardProvision starts the provision job (orchestrator when wired,
// legacy queued job otherwise) and returns its id. A same-key resume whose
// first delivery already dispatched reuses the existing provision job instead
// of double-dispatching. handled == true means an error response was written.
func (h wizardRunHandlers) dispatchWizardProvision(e *httpx.Event, run *wizardRunState, stack *persistedStack, jobSpec map[string]interface{}, ownerSpecAccess ownerSpecBootstrapAccess, preparedLease *jobs.ManagedLeaseRequest) (string, bool, bool) {
	autoDeploy := shouldStartRolloutAfterCreate(jobSpec)
	if stack.IdempotentReplay {
		if jobID := h.existingWizardProvisionJob(e.Request.Context(), run, stack.Id); jobID != "" {
			return jobID, autoDeploy, false
		}
	}
	if h.crud.orch == nil {
		jobID, err := h.crud.createQueuedJob(e, queuedJobParams{
			tenantID:    run.tenantID,
			jobType:     wizardRunProvisionJobType,
			stackID:     stack.Id,
			currentStep: wizardRunQueuedNoOrchStep,
			stackStatus: wizardRunStateProvisioning,
		})
		if err != nil {
			h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "provision_dispatch_failed")
			_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
			return "", false, true
		}
		return jobID, false, false
	}
	startCtx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	jobID, err := h.crud.orch.ProvisionStackWithOptions(stack.Id, jobSpec, orchestrator.ProvisionStackOptions{
		AutoDeploy:           autoDeploy,
		OwnerSpecBootstrap:   ownerSpecRuntimeBootstrap(ownerSpecAccess),
		RequestContext:       startCtx,
		OwnerID:              run.ownerID,
		StackName:            stack.Name,
		TenantID:             run.tenantID,
		PreparedManagedLease: preparedLease,
	})
	if err != nil {
		if refusal := tenantguard.ExecutionRefusal(err); refusal != nil {
			_ = httpx.Error(e, refusal.Status, refusal.Code, refusal.Message, refusal.Details)
			return "", false, true
		}
		h.crud.markStackProvisionStartFailed(e.Request.Context(), stack.Id, run.tenantID)
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.Id}, "provision_dispatch_failed")
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
			ownerSpecAuditReasonKey: err.Error(),
		})
		return "", false, true
	}
	return jobID, autoDeploy, false
}

// existingWizardProvisionJob returns the newest provision job for the stack,
// or "" when none exists (dispatch is still needed).
func (h wizardRunHandlers) existingWizardProvisionJob(ctx context.Context, run *wizardRunState, stackID string) string {
	if h.crud.jobStore == nil {
		return ""
	}
	jobs, err := h.crud.jobStore.ListJobsByStack(ctx, run.tenantID, stackID, 50)
	if err != nil {
		return ""
	}
	newestID := ""
	var newestAt time.Time
	for _, job := range jobs {
		if job.Type != wizardRunProvisionJobType && job.Type != wizardRunDeployJobType {
			continue
		}
		if newestID == "" || job.CreatedAt.After(newestAt) {
			newestID = job.ID
			newestAt = job.CreatedAt
		}
	}
	return newestID
}

// mintWizardPairing mints the store pairing token + registration job for the
// run's server. The wizard UI polls the pairing job for the registration
// token, so a mint failure is a hard error (the stack itself already exists;
// retrying with the same Idempotency-Key resumes and mints a fresh token).
func (h wizardRunHandlers) mintWizardPairing(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack, stackName string, projection *specv2.Projection) (trust.MintedStackPairing, bool) {
	if h.cfg.Trust.Workers == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Pairing store unavailable", wizardRunDetails(
				"wizard_pairing_unavailable", true,
				"Pairing unavailable",
				"The worker pairing store is not configured; the server registration token cannot be minted.",
			))
		return trust.MintedStackPairing{}, true
	}
	nodeRole := ""
	if roles := run.request.Intent.Server.Roles; len(roles) > 0 {
		nodeRole = specv2.NormalizeRole(roles[0])
	}
	params := trust.PairingTokenParams{
		Name:                   strings.TrimSpace(stackName + " " + projection.NodeID),
		StackID:                stack.ID,
		SpecNodeID:             projection.NodeID,
		ServerProvisioningMode: wizardRunTransport(run.request),
		EnvironmentClass:       run.request.Intent.Server.EnvironmentClass,
		NodeRole:               nodeRole,
		StackKit:               wizardRunKitSlug(run.request, projection),
		Services:               run.request.Services,
	}
	if remote := run.request.Remote; remote != nil {
		params.ServerRemoteHost = remote.Host
		params.ServerRemotePort = remote.Port
		params.ServerRemoteUser = remote.User
		params.ServerRemoteAuthMethod = remote.AuthMethod
		params.ServerRemoteSSHKeyLabel = remote.SSHKeyLabel
		params.ServerRemoteUseSudo = remote.UseSudo
	}
	var minted trust.MintedStackPairing
	var err error
	if run.key != "" {
		minted, err = h.prepareKeyedWizardPairing(e.Request.Context(), run, stack, params)
	} else {
		minted, err = trust.MintStackPairingToken(e.Request.Context(), h.cfg.Trust, run.tenantID, run.ownerID, stack, params)
	}
	if err != nil || minted.JobID == "" {
		// The answer stays generic; the log and the 5xx report name the cause.
		cause := err
		if cause == nil {
			cause = errors.New("pairing preparation returned no job")
		}
		httpx.RecordDependencyFailure(e, "wizard_runs.pairing", cause)
		details := wizardRunDetails(
			"wizard_pairing_failed", true,
			"Server registration not prepared",
			"The run's persisted state is kept. Retry with the same Idempotency-Key to mint a fresh registration token.",
		)
		details[creationStackIDField] = stack.ID
		details[creationOperationsURLField] = homelabDashboardURL()
		_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to prepare server registration", details)
		return minted, true
	}
	return minted, false
}

// wizardRunKitSlug resolves the kit slug for pairing metadata: the found kit
// on found runs, the joined spec's kit on joins.
func wizardRunKitSlug(request wizardRunRequest, projection *specv2.Projection) string {
	if slug := strings.TrimSpace(request.Intent.KitAssignment.KitSlug); slug != "" {
		return slug
	}
	if kit, ok := projection.Spec["kit"].(map[string]any); ok {
		if slug, ok := kit["slug"].(string); ok {
			return slug
		}
	}
	return ""
}

// runWizardJoin appends the run's server to an existing kit deployment: load +
// owner-check the stack, snapshot the server baseline, take the stored
// Architecture v2 spec or materialize the kit seed when the stored document
// is missing or not canonical, project, CLI-validate, and persist through
// one stack-config CAS. User-owned Nodes then mint pairing;
// managed Nodes dispatch through the canonical managed-runtime authority. A
// same-key retry whose earlier attempt already persisted its node reuses that
// node instead of appending a phantom sibling.
func (h wizardRunHandlers) runWizardJoin(e *httpx.Event, run *wizardRunState) error {
	stack, findErr := h.crud.findOwnedStoreStack(e, run.request.Intent.KitAssignment.KitDeploymentID)
	if findErr != nil {
		return findErr
	}
	managedJoin := wizardRunTransport(run.request) == specv2.TransportKombifyCloud
	if managedJoin && h.cfg.ManagedExpansion == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime expansion is unavailable", wizardRunDetails(
				"wizard_managed_expansion_unavailable", true,
				"Managed expansion unavailable",
				"Retry when the native managed-runtime authority is available; no Node was added.",
			))
	}
	if managedJoin && (run.request.Managed == nil || strings.TrimSpace(run.request.Managed.ProviderID) == "") {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Managed provider selection is required", wizardRunDetails(
				"wizard_managed_provider_required", false,
				"Select a managed provider",
				"Choose Centron or IONOS before adding this managed Node.",
			))
	}
	baseline, handled := h.captureWizardJoinServerBaseline(e, run, stack.ID)
	if handled {
		return nil
	}
	run.existingServerIDs = baseline
	base, migrated, handled := h.resolveWizardJoinBase(e, run, stack)
	if handled {
		return nil
	}

	projection, persisted, handled := h.resolveJoinProjection(e, run, stack, base)
	if handled {
		return nil
	}
	if homelabHandled := h.ensureWizardHomelab(e, run, stack.Name); homelabHandled {
		return nil
	}
	if !persisted {
		if run.key == "" {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
				"Joining a Node requires an Idempotency-Key", wizardRunDetails(
					"wizard_projection_identity_requires_key", false, "Retry this creation attempt",
					"Submit the attempt with its Idempotency-Key; no Node was added."))
		}
		store, ok := h.crud.stackStore.(controlplane.AtomicWizardProjectionStore)
		if !ok {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Atomic Wizard projection persistence is unavailable", nil)
		}
		pending := map[string]any{}
		addWizardProjectionFields(pending, projection, h.cfg.ReleaseVersion)
		pending["unmapped_goals"] = append([]string{}, projection.UnmappedGoals...)
		entry := h.wizardRunLedgerEntry(run, controlplane.WizardRun{
			StackID: stack.ID, NodeID: projection.NodeID, Status: "failed", ErrorReason: "projection_pending",
		}, pending)
		newConfig := map[string]any{}
		for key, value := range stack.Config {
			newConfig[key] = value
		}
		newConfig[stackConfigKeySpecV2] = projection.Spec
		if migrated {
			newConfig[stackConfigKeySpecV1State] = stackConfigSpecV1Archived
		}
		// CAS and the run-to-node identity commit together. Neither a cancelled
		// later request nor a concurrent CAS loser can claim another run's Node.
		_, storedRun, updateErr := store.CommitWizardJoinProjection(e.Request.Context(), controlplane.StackConfigCAS{
			TenantID: run.tenantID, StackID: stack.ID,
			ExpectedUpdatedAt: stack.UpdatedAt, Config: newConfig,
		}, entry)
		if errors.Is(updateErr, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
				"The StackKit deployment changed while this Node was being added", wizardRunDetails(
					"wizard_join_config_changed", true,
					"Deployment changed",
					"Reload the deployment and retry adding the Node; no pairing token was created.",
				))
		}
		if updateErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to persist the joined node", nil)
		}
		run.runID = storedRun.ID
		run.priorFailed = storedRun
	}
	// Persist goal reconciliation before pairing. A pairing failure records a
	// resumable run after the projected spec already became authoritative; the
	// goal state and its idempotent outbox event must cross that same boundary.
	if intentErr := h.persistWizardHomelabIntent(e.Request.Context(), run, stack.ID, projection); intentErr != nil {
		return h.failWizardIntentProjection(e, run, stack.ID, projection, intentErr)
	}
	if stack.HomelabID == "" {
		// Heal the homelab link on legacy stacks; the composite FK guards
		// cross-tenant writes, so a failure here is logged, not fatal.
		if _, linkErr := h.crud.stackStore.SetStackHomelab(e.Request.Context(), run.tenantID, stack.ID, run.homelab.ID); linkErr != nil {
			logger.Default().Warn("wizard_join_homelab_link_failed", "error", linkErr, "stack_id", stack.ID)
		}
	}
	if managedJoin {
		return h.runWizardManagedJoin(e, run, stack, projection, persisted)
	}

	plannedServerID, err := h.crud.persistJoinServerIntent(
		e.Request.Context(),
		run.ownerID,
		run.tenantID,
		stack,
		projection,
		wizardRunTransport(run.request),
		wizardJoinRemoteHost(run, stack),
	)
	if err != nil {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
			StackID: stack.ID, NodeID: projection.NodeID,
		}, "join_server_intent_failed")
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to reserve the joined Node in inventory", wizardRunDetails(
				"wizard_join_server_intent_failed", true,
				"Node inventory unavailable",
				"Retry with the same Idempotency-Key; no pairing token was created.",
			))
	}

	if wizardRunTransport(run.request) == specv2.TransportConnectRemote {
		if err := h.persistWizardRemoteConnectionBinding(e.Request.Context(), run, stack.ID); err != nil {
			h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
				StackID: stack.ID, NodeID: projection.NodeID,
			}, "remote_connection_persist_failed")
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
				"Could not persist the remote SSH connection", wizardRunDetails(
					"wizard_remote_connection_persist_failed", true,
					"Connection not saved",
					err.Error(),
				))
		}
	}

	minted, handled := h.mintWizardPairing(e, run, stack, stack.Name, projection)
	if handled {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
			StackID: stack.ID, NodeID: projection.NodeID,
		}, "pairing_mint_failed")
		return nil
	}
	if wizardRunTransport(run.request) == specv2.TransportConnectRemote {
		h.startWizardRemoteEnrollment(e, run, stack.ID, minted, plannedServerID)
	}
	var guest *WizardGuestProvisionResult
	if wizardRunTransport(run.request) == specv2.TransportHypervisor {
		var failed bool
		guest, failed = h.provisionWizardGuest(e, run, projection, stack.ID, minted, ownerSpecBootstrapAccess{})
		if failed {
			return nil
		}
	}
	data := map[string]any{
		"run_kind":                 run.effectiveKind,
		"requested_run_kind":       run.request.Intent.RunKind,
		"coerced":                  run.coerced(),
		"homelab_id":               run.homelab.ID,
		"kit_assignment_mode":      wizardRunKindJoin,
		"kit_slug":                 wizardRunKitSlug(run.request, projection),
		creationStackIDField:       stack.ID,
		"node_id":                  projection.NodeID,
		creationNameField:          stack.Name,
		wizardRunStateField:        wizardRunStateAwaitingPairing,
		"pairing_job_id":           minted.JobID,
		wizardRunExistingServerIDs: append([]string(nil), run.existingServerIDs...),
		creationOperationsURLField: homelabDashboardURL(),
	}
	if plannedServerID = strings.TrimSpace(plannedServerID); plannedServerID != "" {
		data[wizardRunPlannedServerID] = plannedServerID
	}
	guestJobID := ""
	if guest != nil {
		guestJobID = guest.JobID
		data[creationJobIDField] = guest.JobID
		data["server_id"] = guest.ServerID
		data["operation_id"] = guest.OperationID
		if guest.Appliance != nil {
			data["appliance"] = guest.Appliance
		}
		data[wizardRunStateField] = wizardRunStateProvisioning
	}
	if persisted {
		data["idempotent_replay"] = true
	}
	if run.layoutRecommendationID != "" {
		data[wizardRunLayoutRecommendationKey] = run.layoutRecommendationID
		h.linkLayoutExecution(e, run, firstNonEmptyWizardJob(guestJobID, minted.JobID))
	}
	addWizardProjectionFields(data, projection, h.cfg.ReleaseVersion)

	if err := h.recordWizardRun(e.Request.Context(), run, controlplane.WizardRun{
		StackID: stack.ID, NodeID: projection.NodeID, PairingJobID: minted.JobID, JobID: guestJobID,
	}, data); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard outcome could not be recorded; retry with the same Idempotency-Key", nil)
	}

	response := map[string]any{"run_id": run.runID}
	for key, value := range data {
		response[key] = value
	}
	return httpx.Success(e, http.StatusAccepted, response)
}

// resolveWizardJoinBase returns the stored native-v2 spec when it is
// canonical. Otherwise it materializes the kit's Architecture v2 seed so
// Additional Node can proceed without sending a v1 document (or v2 fields
// such as useCases on v1) through the StackKits migrate adapter.
func (h wizardRunHandlers) resolveWizardJoinBase(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack) (map[string]any, bool, bool) {
	if base, ok := stack.Config[stackConfigKeySpecV2].(map[string]any); ok && len(base) > 0 {
		if err := specv2.RequireCanonicalV2(base); err == nil {
			return base, false, false
		}
	}
	return h.seedWizardJoinBase(e, run, stack)
}

func (h wizardRunHandlers) seedWizardJoinBase(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack) (map[string]any, bool, bool) {
	stored, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
	legacy := stackSpecMapFromValueOrEmpty(stack.Config["user_config"])
	targetKit := wizardLegacyMigrationTarget(run.request.Intent.KitAssignment.KitSlug, stack.Config, stored)
	if targetKit == "" {
		targetKit = wizardLegacyMigrationTarget(run.request.Intent.KitAssignment.KitSlug, stack.Config, legacy)
	}
	if targetKit == "" {
		targetKit = specv2.KitSlugBasement
	}
	if h.cfg.Seeds == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Kit seed templates are not configured", wizardRunDetails(
				"wizard_seed_templates_unavailable", true,
				"Server missing seed templates",
				"The server has no TECHSTACK_STACKKIT_SPEC_TEMPLATES configured; wizard runs cannot project a kit spec.",
			))
		return nil, false, true
	}
	seed, seedErr := h.cfg.Seeds.Seed(targetKit)
	if seedErr != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Kit seed unavailable", wizardRunDetails(
				"wizard_seed_unavailable", true,
				"Kit seed unavailable",
				"The requested kit's seed template could not be read: "+seedErr.Error(),
			))
		return nil, false, true
	}
	base := cloneMapForMutation(seed)
	if specv2.RequireCanonicalV2(base) != nil {
		writeWizardJoinRequiresV2(e, targetKit)
		return nil, false, true
	}
	metadata := map[string]any{}
	if existing, ok := base["metadata"].(map[string]any); ok {
		metadata = cloneMapForMutation(existing)
	}
	if name := strings.TrimSpace(stack.Name); name != "" {
		metadata["name"] = name
	}
	base["metadata"] = metadata
	return base, true, false
}

func stackSpecMapFromValueOrEmpty(value any) map[string]any {
	if spec, ok := stackSpecMapFromValue(value); ok {
		return spec
	}
	return map[string]any{}
}

func wizardLegacyMigrationTarget(explicit string, config, legacy map[string]any) string {
	for _, candidate := range []string{
		explicit,
		fieldString(config, "stackkit_catalog_ref"),
		fieldString(legacy, "stackkit"),
		fieldString(legacy, "kit"),
	} {
		candidate = normalizeRuntimeStackKitRef(candidate, "")
		if specv2.IsKnownKitSlug(candidate) {
			return candidate
		}
	}
	return ""
}

func writeWizardJoinRequiresV2(e *httpx.Event, targetKit string) {
	details := wizardRunDetails(
		"wizard_join_requires_v2", false,
		"This homelab is not Architecture v2",
		"Add-server only joins a native Architecture v2 kit deployment. Found a new kit for this server instead of migrating a v1 stack-spec.",
	)
	if targetKit != "" {
		details["target_kit"] = targetKit
	}
	_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
		"Join requires an Architecture v2 StackSpec", details)
}

func (h wizardRunHandlers) runWizardManagedJoin(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack, projection *specv2.Projection, persisted bool) error {
	managed := run.request.Managed
	result, expansionErr := h.cfg.ManagedExpansion.Execute(e, WizardManagedRuntimeExpansionRequest{
		StackID:           stack.ID,
		IdempotencyKey:    run.key,
		RuntimeSlotKey:    projection.NodeID,
		NodeRole:          wizardManagedNodeRole(run.request),
		RuntimeOfferingID: managed.RuntimeOfferingID,
		ProviderID:        managed.ProviderID,
		ProviderRegion:    managed.ProviderRegion,
		IONOSDatacenter:   managed.IONOSDatacenter,
		StackKit:          wizardRunKitSlug(run.request, projection),
		Services:          append([]string(nil), run.request.Services...),
	})
	if expansionErr != nil || result == nil || strings.TrimSpace(result.JobID) == "" {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{
			StackID: stack.ID, NodeID: projection.NodeID,
		}, "managed_expansion_failed")
		if expansionErr != nil {
			return expansionErr
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime expansion returned no durable job", wizardRunDetails(
				"wizard_managed_expansion_unavailable", true,
				"Managed expansion unavailable",
				"Retry with the same Idempotency-Key; the persisted Node projection will be reused.",
			))
	}

	data := map[string]any{
		"run_kind":                 run.effectiveKind,
		"requested_run_kind":       run.request.Intent.RunKind,
		"coerced":                  run.coerced(),
		"homelab_id":               run.homelab.ID,
		"kit_assignment_mode":      wizardRunKindJoin,
		"kit_slug":                 wizardRunKitSlug(run.request, projection),
		creationStackIDField:       stack.ID,
		"node_id":                  projection.NodeID,
		creationNameField:          stack.Name,
		wizardRunStateField:        wizardRunStateProvisioning,
		creationJobIDField:         result.JobID,
		"runtime_slot_key":         result.RuntimeSlotKey,
		"runtime_slot_id":          result.RuntimeSlotID,
		"lease_id":                 result.LeaseID,
		"runtime_server_id":        result.RuntimeServerID,
		"resource_generation_id":   result.ResourceGenerationID,
		"operation_id":             result.OperationID,
		"provider_id":              result.ProviderID,
		"provider_region":          result.ProviderRegion,
		"ionos_datacenter":         result.IONOSDatacenter,
		"runtime_offering_id":      result.RuntimeOfferingID,
		"enrollment_status":        result.EnrollmentStatus,
		"runtime_phase":            result.RuntimePhase,
		creationMessageField:       result.Message,
		wizardRunExistingServerIDs: append([]string(nil), run.existingServerIDs...),
		creationOperationsURLField: homelabDashboardURL(),
	}
	if persisted || result.IdempotentReplay {
		data["idempotent_replay"] = true
	}
	if len(result.Warnings) > 0 {
		data["warnings"] = append([]string(nil), result.Warnings...)
	}
	addWizardProjectionFields(data, projection, h.cfg.ReleaseVersion)

	if err := h.recordWizardRun(e.Request.Context(), run, controlplane.WizardRun{
		StackID: stack.ID, NodeID: projection.NodeID, JobID: result.JobID,
	}, data); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard outcome could not be recorded; retry with the same Idempotency-Key", nil)
	}

	response := map[string]any{"run_id": run.runID}
	for key, value := range data {
		response[key] = value
	}
	return httpx.Success(e, http.StatusAccepted, response)
}

func wizardManagedNodeRole(request wizardRunRequest) string {
	for _, role := range request.Intent.Server.Roles {
		if role = strings.TrimSpace(role); role != "" {
			return role
		}
	}
	return "worker"
}

// captureWizardJoinServerBaseline snapshots the canonical runtime identities
// before the join can mutate stack config or mint pairing. A missing baseline
// must fail closed: otherwise a resumed UI could accept an already connected
// server as evidence for the new Node.
func (h wizardRunHandlers) captureWizardJoinServerBaseline(e *httpx.Event, run *wizardRunState, stackID string) ([]string, bool) {
	if h.crud.serverStore == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Canonical server inventory is unavailable", wizardRunDetails(
				"wizard_join_server_baseline_unavailable", true,
				"Server inventory unavailable",
				"Retry when the canonical server inventory is available; no Node was added.",
			))
		return nil, true
	}
	servers, err := h.crud.serverStore.ListServerRuntimesByTenant(e.Request.Context(), run.tenantID, stackID)
	if err != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Failed to read the canonical server inventory", wizardRunDetails(
				"wizard_join_server_baseline_unavailable", true,
				"Server inventory unavailable",
				"Retry when the canonical server inventory is available; no Node was added.",
			))
		return nil, true
	}
	ids := make([]string, 0, len(servers))
	for _, server := range servers {
		if id := strings.TrimSpace(server.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, false
}

// resolveJoinProjection returns the join's projection. When an earlier failed
// attempt with this key already appended and persisted its node (recorded in
// the ledger), the stored spec is reused as-is (persisted == true) so the
// retry re-mints pairing for the SAME node instead of appending a phantom
// sibling; otherwise the node is projected and CLI-validated normally.
func (h wizardRunHandlers) resolveJoinProjection(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack, base map[string]any) (*specv2.Projection, bool, bool) {
	if prior := run.priorFailed; prior != nil && prior.StackID == stack.ID && prior.NodeID != "" && specHasNode(base, prior.NodeID) {
		// Reusing the committed spec also reuses what that projection evaluated.
		// A newly loaded retry backlog must never expand the old scope.
		run.evaluatedGoals = nil
		if raw, ok := prior.Result["evaluated_goals"]; ok {
			run.evaluatedGoals = wizardStringValues(raw)
		}
		unmapped := run.priorUnmappedGoals
		if raw, ok := prior.Result["unmapped_goals"]; ok {
			unmapped = wizardStringValues(raw)
		}
		purpose, _ := prior.Result["unmapped_purpose"].(string)
		return &specv2.Projection{
			Spec: base, NodeID: prior.NodeID,
			UnmappedGoals: append([]string(nil), unmapped...), UnmappedPurpose: purpose,
		}, true, false
	}
	intent := run.request.Intent
	if metadata, ok := base["metadata"].(map[string]any); ok {
		if name, ok := metadata["name"].(string); ok && strings.TrimSpace(name) != "" {
			// Joining must never rename the deployment: pin the intent name
			// to the stored contract id before projecting.
			intent.Name = name
		}
	}
	projection, handled := h.projectAndValidate(e, base, intent, run.homelabID, h.layoutAcceptanceHook(e, run, stack.ID))
	if !handled {
		run.captureGoalActivations(projection)
	}
	return projection, false, handled
}

// specHasNode reports whether the spec's nodes list contains the id.
func specHasNode(spec map[string]any, nodeID string) bool {
	nodes, _ := spec["nodes"].([]any)
	for _, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			if id, _ := node["id"].(string); id == nodeID {
				return true
			}
		}
	}
	return false
}

// persistWizardHomelabIntent applies only this run's decisions to the latest
// canonical intent under the existing store transaction. Explicit off and empty
// answers remain writes; unrelated fields are never reconstructed from a snapshot.
func (h wizardRunHandlers) persistWizardHomelabIntent(ctx context.Context, run *wizardRunState, stackID string, projection *specv2.Projection) error {
	store, ok := h.crud.homelabStore.(controlplane.AtomicHomelabIntentStore)
	if !ok {
		return errors.New("atomic homelab intent persistence is unavailable")
	}
	decisions := run.explicitIntent
	updated, err := store.MutateHomelabIntent(ctx, run.tenantID, run.homelab.ID, func(intentDoc map[string]any) error {
		wizard, _ := intentDoc["wizard"].(map[string]any)
		if wizard == nil {
			wizard = map[string]any{}
		}
		wizard["goals"] = mergeWizardGoals(wizardStringValues(wizard["goals"]), run.request.Intent.Goals)
		// Decisions ride beside the goals they belong to, merged per use case so a
		// later run that mentions one use case does not erase the others' choices.
		if len(decisions.UseCaseSettings) > 0 {
			stored, _ := wizard["use_case_settings"].(map[string]any)
			if stored == nil {
				stored = map[string]any{}
			}
			for slug, values := range decisions.UseCaseSettings {
				merged, _ := stored[slug].(map[string]any)
				if merged == nil {
					merged = map[string]any{}
				}
				for id, value := range values {
					merged[id] = value
				}
				stored[slug] = merged
			}
			wizard["use_case_settings"] = stored
		}
		if len(decisions.UseCaseCapabilities) > 0 {
			stored, _ := wizard["use_case_capabilities"].(map[string]any)
			if stored == nil {
				stored = map[string]any{}
			}
			for slug, values := range decisions.UseCaseCapabilities {
				merged, _ := stored[slug].(map[string]any)
				if merged == nil {
					merged = map[string]any{}
				}
				for id, value := range values {
					merged[id] = value
				}
				stored[slug] = merged
			}
			wizard["use_case_capabilities"] = stored
		}
		mergeWizardSetupAnswers(wizard, decisions.UseCaseSetupAnswers)
		if decisions.Access.Mode != "" || decisions.Access.Publications != nil {
			wizard["access"] = decisions.Access
		}
		if decisions.Household.Profile != "" || decisions.Household.PlannedPeople != nil {
			wizard["household"] = decisions.Household
		}
		// Clear only backlog entries this projection actually reconsidered. A
		// concurrent expansion's new goal was not evaluated by this run.
		unmapped := projection.UnmappedGoals
		for _, goal := range wizardStringValues(wizard["unmapped_goals"]) {
			if !slices.Contains(run.evaluatedGoals, goal) {
				unmapped = append(unmapped, goal)
			}
		}
		unmapped = mergeWizardGoals(unmapped)
		if len(unmapped) > 0 {
			wizard["unmapped_goals"] = unmapped
		} else {
			delete(wizard, "unmapped_goals")
		}
		if projection.UnmappedPurpose != "" {
			wizard["unmapped_purpose"] = projection.UnmappedPurpose
		}
		wizard["last_run_id"] = run.runID
		wizard["last_run_kind"] = run.effectiveKind
		wizard["last_goal_projection_release"] = strings.TrimSpace(h.cfg.ReleaseVersion)
		// Re-evaluate the latest merged document while its row is locked. A
		// disjoint write cannot clear another run's unresolved recorded choice.
		canonical, err := wizardIntentFromDocument(wizard)
		if err != nil {
			return err
		}
		_, blocked, err := admitWizardDesiredCapabilities(canonical)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			wizard["projection_status"] = "blocked"
			wizard["blocked_capabilities"] = blocked
		} else {
			wizard["projection_status"] = "projected"
			delete(wizard, "blocked_capabilities")
		}
		intentDoc["wizard"] = wizard
		return nil
	})
	if err != nil {
		return err
	}
	run.homelab = updated
	h.enqueueWizardGoalActivationNotifications(ctx, run, stackID)
	return nil
}

// A projection failure precedes dispatch/pairing, so the existing same-key
// stack/node resume repairs intent without creating a second provider operation.
func (h wizardRunHandlers) failWizardIntentProjection(e *httpx.Event, run *wizardRunState, stackID string, projection *specv2.Projection, err error) error {
	logger.Default().Warn("wizard_homelab_intent_update_failed", "error", err, "homelab_id", run.homelab.ID)
	result := map[string]any{}
	addWizardProjectionFields(result, projection, h.cfg.ReleaseVersion)
	result["unmapped_goals"] = append([]string{}, projection.UnmappedGoals...)
	h.writeWizardRunLedger(e.Request.Context(), run, controlplane.WizardRun{StackID: stackID, NodeID: projection.NodeID}, "failed", "intent_projection_failed", result)
	details := wizardRunDetails("wizard_intent_projection_failed", true, "Decisions not saved", "Retry with the same Idempotency-Key to save the decisions on the existing deployment and Node.")
	details["run_id"], details["stack_id"], details["node_id"] = run.runID, stackID, projection.NodeID
	details["intent_projection_status"] = "failed"
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to persist wizard decisions", details)
}

// A completed response requires its durable outcome; a failed write is a
// retryable failure on the existing identity, never a misleading accepted202.
func (h wizardRunHandlers) recordWizardRun(ctx context.Context, run *wizardRunState, outcome controlplane.WizardRun, result map[string]any) error {
	return h.writeWizardRunLedger(ctx, run, outcome, "completed", "", result)
}

// recordWizardRunFailure writes a failed ledger entry after a side-effectful
// partial failure so a same-key retry converges on the partial outcome (the
// join lane reuses the recorded node id) instead of duplicating it.
func (h wizardRunHandlers) recordWizardRunFailure(ctx context.Context, run *wizardRunState, outcome controlplane.WizardRun, reason string) {
	h.writeWizardRunLedger(ctx, run, outcome, "failed", reason, map[string]any{})
}

func (h wizardRunHandlers) writeWizardRunLedger(ctx context.Context, run *wizardRunState, outcome controlplane.WizardRun, status, reason string, result map[string]any) error {
	outcome.Status, outcome.ErrorReason = status, reason
	entry := h.wizardRunLedgerEntry(run, outcome, result)
	stored, err := h.wizardRuns.UpsertWizardRun(ctx, entry)
	if err != nil {
		logger.Default().Warn("wizard_run_ledger_write_failed", "error", err, "run_id", run.runID)
		return err
	}
	run.runID = stored.ID
	run.pairingJobID = stored.PairingJobID
	return nil
}

func (h wizardRunHandlers) wizardRunLedgerEntry(run *wizardRunState, outcome controlplane.WizardRun, result map[string]any) controlplane.WizardRun {
	addWizardResumeContext(result, run.request, run.effectiveKind)
	if _, ok := result["unmapped_goals"]; !ok && run.priorFailed != nil {
		if raw, present := run.priorFailed.Result["unmapped_goals"]; present {
			result["unmapped_goals"] = raw
		}
	}
	if run.evaluatedGoals != nil {
		result["evaluated_goals"] = append([]string{}, run.evaluatedGoals...)
	}
	if len(run.intentAdjustments) > 0 {
		result["intent_adjustments"] = append([]specv2.IntentAdjustment(nil), run.intentAdjustments...)
		result["effective_intent"] = run.request.Intent
	}
	if outcome.PairingJobID == "" {
		outcome.PairingJobID = run.pairingJobID
	}
	ledgerRequest := wizardRunRequestWithIntent(run.request, run.requestedIntent)
	entry := controlplane.WizardRun{
		ID:               run.runID,
		TenantID:         run.tenantID,
		OwnerSubjectID:   run.ownerID,
		IdempotencyKey:   run.key,
		RequestSHA256:    run.requestHash,
		RunKind:          run.effectiveKind,
		RequestedRunKind: run.request.Intent.RunKind,
		HomelabID:        run.homelabIDForLedger(),
		StackID:          outcome.StackID,
		NodeID:           outcome.NodeID,
		JobID:            outcome.JobID,
		PairingJobID:     outcome.PairingJobID,
		Status:           outcome.Status,
		ErrorReason:      outcome.ErrorReason,
		Intent:           wizardRunIntentDocument(ledgerRequest),
		Result:           result,
	}
	return entry
}

// addWizardResumeContext keeps the owner-scoped run ledger authoritative for
// browser resume without persisting credentials or the raw owner options. The
// creating page needs only display and routing facts; pairing tokens remain in
// their short-lived job result.
func addWizardResumeContext(result map[string]any, request wizardRunRequest, runKind string) {
	resume := map[string]any{}
	if transport := strings.TrimSpace(request.Intent.Server.Transport); transport != "" {
		resume["server_provisioning_mode"] = transport
	}
	if request.Remote != nil {
		if host := strings.TrimSpace(request.Remote.Host); host != "" {
			resume["remote_server_host"] = host
			resume["expected_device_name"] = host
		}
		if user := strings.TrimSpace(request.Remote.User); user != "" {
			resume["remote_server_user"] = user
		}
		if request.Remote.Port != nil {
			resume["remote_server_port"] = *request.Remote.Port
		}
		if method := strings.TrimSpace(request.Remote.AuthMethod); method != "" {
			resume["remote_server_auth_method"] = method
		}
		if ref := strings.TrimSpace(request.Remote.SSHKeyLabel); ref != "" {
			resume["remote_server_credential_ref"] = ref
		}
		if itemID := strings.TrimSpace(request.Remote.SSHWalletItemID); itemID != "" {
			resume["remote_server_credential_item_id"] = itemID
		}
	}
	if _, ok := resume["expected_device_name"]; !ok {
		for _, role := range request.Intent.Server.Roles {
			if role = strings.TrimSpace(role); role != "" {
				resume["expected_device_name"] = strings.TrimSpace(request.Intent.Name) + "-" + role
				break
			}
		}
	}

	bootstrapMode := strings.TrimSpace(stringFromAny(request.Owner["owner_bootstrap_mode"]))
	ownerSource := strings.TrimSpace(stringFromAny(request.Owner["owner_source"]))
	if runKind == specv2.RunKindFirstRun && bootstrapMode != "" && bootstrapMode != ownerBootstrapModeNone &&
		(ownerSource == ownerSourceLocal || ownerSource == ownerSourceCloudLinked) {
		resume["owner_seed_expected"] = true
		summary := map[string]any{"source": ownerSource}
		for option, field := range map[string]string{
			"owner_email":        "email",
			"owner_username":     "username",
			"owner_display_name": "display_name",
		} {
			if value := strings.TrimSpace(stringFromAny(request.Owner[option])); value != "" {
				summary[field] = value
			}
		}
		resume["owner_seed_summary"] = summary
	}
	if len(resume) > 0 {
		result["resume_context"] = resume
	}
}

// homelabIDForLedger returns the homelab id only once the row exists — the
// ledger's composite FK requires a real row, and failure entries may be
// written before ensureWizardHomelab ran.
func (run *wizardRunState) homelabIDForLedger() string {
	if run.homelab != nil {
		return run.homelab.ID
	}
	return ""
}

// wizardRunIntentDocument stores the run request in the ledger for audit and
// resume. The owner section may carry bootstrap references, so it is reduced
// to its key names; raw values never enter the ledger.
func wizardRunIntentDocument(request wizardRunRequest) map[string]any {
	doc := map[string]any{}
	if raw, err := json.Marshal(request.Intent); err == nil {
		var intent map[string]any
		if err := json.Unmarshal(raw, &intent); err == nil {
			doc["intent"] = intent
		}
	}
	if len(request.Services) > 0 {
		doc["services"] = request.Services
	}
	if len(request.Owner) > 0 {
		keys := make([]string, 0, len(request.Owner))
		for key := range request.Owner {
			keys = append(keys, key)
		}
		doc["owner_option_keys"] = keys
	}
	return doc
}

// addWizardProjectionFields attaches the projection's intent leftovers and the
// pinned release version to a run response/result map.
func addWizardProjectionFields(data map[string]any, projection *specv2.Projection, releaseVersion string) {
	if len(projection.UnmappedGoals) > 0 {
		data["unmapped_goals"] = projection.UnmappedGoals
	}
	if projection.UnmappedPurpose != "" {
		data["unmapped_purpose"] = projection.UnmappedPurpose
	}
	if releaseVersion != "" {
		data["release_version"] = releaseVersion
	}
}

// mergeWizardSetupAnswers projects desired decisions into the existing intent
// document. Empty arrays are explicit "none/off", never absence or defaults.
func mergeWizardSetupAnswers(wizard map[string]any, answers map[string]map[string][]string) {
	if len(answers) == 0 {
		return
	}
	stored, _ := wizard["use_case_setup_answers"].(map[string]any)
	if stored == nil {
		stored = map[string]any{}
	}
	for slug, values := range answers {
		merged, _ := stored[slug].(map[string]any)
		if merged == nil {
			merged = map[string]any{}
		}
		for id, choices := range values {
			kept := make([]string, len(choices))
			copy(kept, choices)
			merged[id] = kept
		}
		stored[slug] = merged
	}
	wizard["use_case_setup_answers"] = stored
}
