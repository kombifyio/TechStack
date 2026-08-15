//nolint:goconst
package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

const (
	stackModeEasy                    = "easy"
	stackModeTechie                  = "techie"
	runtimeModeKombifyCloud          = "kombify-cloud"
	runtimeModeMonthlyRuntime        = "monthly-runtime"
	runtimeModeManagedCloud          = "managed-cloud"
	runtimeModeUserOwned             = "user-owned"
	runtimeProvisioningConnectRemote = "connect-remote"
	runtimeProvisioningInstall       = "install-command"
	managedRuntimeCapacityTopic      = "system.service-degraded"
	managedRuntimeCapacitySource     = "techstack"
)

type crudRouteHandlers struct {
	app                core.App
	orch               *orchestrator.Orchestrator
	deploymentMode     config.DeploymentMode
	stackStore         controlplane.StackStore
	homelabStore       controlplane.HomelabStore
	jobStore           controlplane.JobStore
	walletStore        controlplane.WalletStore
	serverStore        controlplane.ServerRuntimeStore
	routingStore       stackrouting.Store
	routingLeases      stackrouting.ManagedLeaseLister
	routingDispatch    stackrouting.RolloutDispatcher
	runtimeFeatures    managedRuntimeFeatureChecker
	managedLeases      jobs.ManagedLeaseManager
	notificationOutbox productnotifications.ProductEventEnqueuer
}

type createStackRequest struct {
	Name               string                 `json:"name"`
	Mode               string                 `json:"mode"`
	StackSpec          map[string]interface{} `json:"stack_spec"`
	UserConfig         map[string]interface{} `json:"user_config"`
	UserConfigRaw      string                 `json:"user_config_raw"`
	UserConfigFormat   string                 `json:"user_config_format"`
	ProviderID         string                 `json:"provider_id"`
	LeaseProvider      string                 `json:"lease_provider"`
	SimulateProviderID string                 `json:"simulate_provider_id"`
	Provider           string                 `json:"provider"`
	Services           []string               `json:"services"`
	Options            map[string]interface{} `json:"options"`
}

type normalizedCreateStackRequest struct {
	Name             string
	Mode             string
	UserConfig       map[string]interface{}
	UserConfigRaw    string
	UserConfigFormat string
	Options          map[string]interface{}
	ProviderID       string
	// HomelabID is resolved to the owner's canonical umbrella by the control
	// plane persistence boundary. StackSpecV2 is set by the wizard-run facade
	// and persisted under config_json.stack_spec_v2.
	HomelabID   string
	StackSpecV2 map[string]interface{}
}

// createStackDispatch bundles the persisted-stack inputs the create flow hands
// to the orchestrator/legacy start paths, keeping those functions below the
// 4-argument threshold instead of threading id/name/spec/access separately.
type createStackDispatch struct {
	stackID         string
	serverID        string
	name            string
	spec            map[string]interface{}
	ownerSpecAccess ownerSpecBootstrapAccess
	preparedLease   *jobs.ManagedLeaseRequest
}

// queuedJobParams bundles the persistence inputs for createQueuedJob so the
// queue-creation call site stays below the 4-argument threshold.
type queuedJobParams struct {
	jobType     string
	stackID     string
	currentStep string
	stackStatus string
}

func (h crudRouteHandlers) listStacks(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}

	tenantID := tenantIDFromRequest(e)
	if h.stackStore != nil && tenantID != "" {
		return h.listStacksFromStore(e, ownerID, tenantID)
	}
	if guardErr := tenantguard.RequireTenant(tenantID, "techstack.stacks.list"); guardErr != nil {
		return guardErr
	}

	stacks, err := h.app.FindRecordsByFilter(
		"stacks",
		"owner_id = {:ownerId}",
		"-created",
		100,
		0,
		map[string]any{"ownerId": ownerID},
	)
	if err != nil {
		return httpx.Success(e, http.StatusOK, []any{})
	}

	result := make([]map[string]any, 0, len(stacks))
	for _, stack := range stacks {
		result = append(result, stackListItem(stack))
	}
	return httpx.Success(e, http.StatusOK, result)
}

// listStacksFromStore serves the control-plane (Postgres) list path, filtered to
// the authenticated owner within the tenant.
func (h crudRouteHandlers) listStacksFromStore(e *httpx.Event, ownerID, tenantID string) error {
	result, err := h.ownedStackItems(e.Request.Context(), ownerID, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to list stacks", nil)
	}
	return httpx.Success(e, http.StatusOK, result)
}

func (h crudRouteHandlers) getStack(e *httpx.Event) error {
	stackID := e.Request.PathValue("id")
	if h.useControlPlaneStore(e) {
		ownerID, authErr := requireStackAuth(e)
		if authErr != nil {
			return authErr
		}
		tenantID := tenantIDFromRequest(e)
		stack, err := h.stackStore.GetStack(e.Request.Context(), tenantID, strings.TrimSpace(stackID))
		if err == nil {
			if stack.OwnerSubjectID != ownerID {
				return httpx.NewForbiddenError("Not your stack", nil)
			}
			return httpx.Success(e, http.StatusOK, stackListItemFromStore(*stack))
		}
		if !errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NewInternalServerError("Failed to fetch stack", nil)
		}
		if legacy, legacyErr := h.findOwnedStack(e, stackID); legacyErr == nil {
			return httpx.Success(e, http.StatusOK, stackListItem(legacy))
		}
		return httpx.NewNotFoundError("Stack not found", nil)
	}

	if _, authErr := requireStackAuth(e); authErr != nil {
		return authErr
	}
	if err := tenantguard.RequireTenant(tenantIDFromRequest(e), "techstack.stacks.get"); err != nil {
		return err
	}
	stack, err := h.findOwnedStack(e, stackID)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, stackListItem(stack))
}

func (h crudRouteHandlers) legacyOwnedStacks(ownerID, tenantID string) ([]*core.Record, error) {
	if h.app == nil {
		return nil, nil
	}
	if _, err := h.app.FindCollectionByNameOrId("stacks"); err != nil {
		return nil, nil
	}
	stacks, err := h.app.FindAllRecords("stacks", dbx.HashExp{"owner_id": ownerID})
	if err != nil {
		return nil, err
	}
	result := make([]*core.Record, 0, len(stacks))
	for _, stack := range stacks {
		if !stack.GetDateTime("deleted_at").IsZero() {
			continue
		}
		stackTenantID := strings.TrimSpace(stack.GetString("tenant_id"))
		if tenantID != "" && stackTenantID != "" && stackTenantID != tenantID {
			continue
		}
		result = append(result, stack)
	}
	return result, nil
}

func (h crudRouteHandlers) createStack(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}

	req, decodeErr := decodeCreateStackRequest(e.Request.Body)
	if decodeErr != nil {
		return httpx.BadRequest(e, "Invalid JSON")
	}
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		return httpx.BadRequest(e, msg)
	}
	if laneMsg := validateDeploymentLane(normalized, h.deploymentMode); laneMsg != "" {
		return httpx.BadRequest(e, laneMsg)
	}
	if rejected, entitlementErr := h.rejectUnauthorizedManagedRuntime(e, ownerID, normalized); rejected || entitlementErr != nil {
		return entitlementErr
	}
	normalized, denial := resolveCreateOwnerBootstrap(normalized, h.ownerBootstrapContextForCreate(e, ownerID, normalized))
	if denial != nil {
		return denial.write(e)
	}
	return h.createNormalizedStack(e, ownerID, normalized)
}

// ownerBootstrapContextForCreate builds the resolve context for the create
// flow, attaching the operator's verified kombify Cloud link only when the
// request selects the cloud-linked owner source.
func (h crudRouteHandlers) ownerBootstrapContextForCreate(e *httpx.Event, ownerID string, normalized normalizedCreateStackRequest) ownerBootstrapContext {
	ctx := ownerBootstrapContextFromRequest(e)
	if requestsCloudLinkedOwner(normalized) {
		ctx.CloudLink = cloudLinkForOwner(h.app, ownerID)
	}
	return ctx
}

func (h crudRouteHandlers) rejectUnauthorizedManagedRuntime(e *httpx.Event, ownerID string, normalized normalizedCreateStackRequest) (bool, error) {
	if decision := evaluateManagedRuntimeEntitlement(e.Request.Context(), normalized, ownerID, h.runtimeFeatures); decision.Denied {
		return true, httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, decision.Message, decision.Details())
	}
	return false, nil
}

// createNormalizedStack runs the create flow as a flat sequence of phases:
// persist -> apply owner bootstrap -> issue owner-spec access -> dispatch. Each
// phase is a helper so this function stays a single, readable happy path and the
// per-phase conditional nesting lives where it belongs.
func (h crudRouteHandlers) createNormalizedStack(e *httpx.Event, ownerID string, normalized normalizedCreateStackRequest) error {
	canonicalConfig, providerErr := canonicalizeFreshProvisionSpec(runtimePolicyConfigFromRequest(normalized))
	if providerErr != nil {
		return httpx.BadRequest(e, "Invalid provider selection: "+providerErr.Error(), nil)
	}
	if providerID := fieldString(canonicalConfig, "provider_id"); providerID != "" {
		normalized.ProviderID = providerID
		applyCanonicalProviderID(normalized.UserConfig, providerID)
	}
	if hasManagedRuntimeFields(canonicalConfig, runtimeFieldsFromConfig(canonicalConfig)) && createStackIdempotencyKey(e) == "" {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"X-Idempotency-Key is required for managed server creation", map[string]any{
				"reason_code": "idempotency_key_invalid", "retryable": false,
			})
	}
	stack, err := h.persistStack(e, ownerID, normalized)
	if err != nil {
		return err
	}
	if stack == nil {
		return nil
	}
	// persistStack may have auto-resolved a duplicate name; downstream specs,
	// bootstrap, and the create response must use the actually-persisted name.
	if stack.Name != "" {
		normalized.Name = stack.Name
	}
	serverID, preparedLease, admissionHandled, admissionErr := h.admitManagedCreate(e, ownerID, stack, normalized)
	if admissionHandled || admissionErr != nil {
		h.markStackProvisionStartFailed(e.Request.Context(), stack.Id, tenantIDFromRequest(e))
		return admissionErr
	}
	var serverErr error
	if serverID == "" {
		serverID, serverErr = h.persistCreateServerIntent(e, ownerID, stack, normalized)
	}
	if serverErr != nil {
		h.markStackProvisionStartFailed(e.Request.Context(), stack.Id, tenantIDFromRequest(e))
		logger.Default().Error("create_server_intent_failed", "error", serverErr, "stack_id", stack.Id, "tenant_id", tenantIDFromRequest(e))
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist server intent", map[string]any{
			creationStackIDField: stack.Id, creationOperationsURLField: operationsURL(stack.Id),
		})
	}
	if stack.IdempotentReplay {
		if handled, replayErr := h.replayCreateJob(e, stack, serverID); handled || replayErr != nil {
			return replayErr
		}
	}
	if bootstrapErr := h.applyCreateOwnerBootstrap(e, ownerID, stack, normalized); bootstrapErr != nil {
		return bootstrapErr
	}
	ownerSpecAccess, accessErr := h.issueCreateOwnerSpecAccess(e, stack, ownerID, normalized)
	if accessErr != nil {
		return accessErr
	}
	return h.dispatchCreateStack(e, createStackDispatch{
		stackID:         stack.Id,
		serverID:        serverID,
		name:            normalized.Name,
		spec:            createStackJobSpec(normalized),
		ownerSpecAccess: ownerSpecAccess,
		preparedLease:   preparedLease,
	})
}

func (h crudRouteHandlers) admitManagedCreate(e *httpx.Event, ownerID string, stack *persistedStack, normalized normalizedCreateStackRequest) (string, *jobs.ManagedLeaseRequest, bool, error) {
	policyConfig := runtimePolicyConfigFromRequest(normalized)
	if !hasManagedRuntimeFields(policyConfig, runtimeFieldsFromConfig(policyConfig)) {
		return "", nil, false, nil
	}
	if h.managedLeases == nil {
		return "", nil, true, managedCreateUnavailable(e, stack.Id, "native_admission_unavailable", true, "Native managed runtime admission is not configured")
	}
	request, err := jobs.PrimaryManagedLeaseRequestFromUIConfig(
		createStackJobSpec(normalized), stack.Id, stack.Name, tenantIDFromRequest(e), ownerID,
	)
	if err != nil {
		return "", nil, true, httpx.BadRequest(e, "Invalid managed runtime specification: "+err.Error(), nil)
	}
	if !stack.IdempotentReplay {
		preflight, ok := h.managedLeases.(jobs.ManagedLeaseAdmissionPreflighter)
		if !ok || preflight == nil {
			return "", nil, true, managedCreateUnavailable(e, stack.Id, "native_admission_preflight_unavailable", true, "Managed server availability could not be verified")
		}
		if err := preflight.PreflightCreateOrBindLease(e.Request.Context(), request); err != nil {
			return "", nil, true, h.writeManagedCreateAdmissionError(e, stack.Id, request, err, "preflight")
		}
	}
	result, err := h.managedLeases.CreateOrBindLease(e.Request.Context(), request)
	if err != nil {
		return "", nil, true, h.writeManagedCreateAdmissionError(e, stack.Id, request, err, "admission")
	}
	if result == nil || strings.TrimSpace(result.RuntimeServerID) == "" || strings.TrimSpace(result.LeaseID) == "" ||
		strings.TrimSpace(result.RuntimeSlotID) == "" || strings.TrimSpace(result.ResourceGenerationID) == "" ||
		strings.TrimSpace(result.OperationID) == "" || result.RuntimeSlotKey != request.RuntimeSlotKey ||
		result.RuntimeSlotGeneration != request.RuntimeSlotGeneration || result.Provider != request.Provider {
		return "", nil, true, managedCreateUnavailable(e, stack.Id, "native_admission_outcome_unconfirmed", true, "Native managed runtime admission has not been confirmed")
	}
	// Preserve the exact admitted provider intent for the durable provision job.
	// StackKit projection can legitimately enrich the later spec, but it must not
	// synthesize a different request for this already-admitted slot generation.
	if normalized.UserConfig != nil {
		normalized.UserConfig[jobs.PreparedManagedLeaseRequestPayloadKey] = jobs.ManagedLeaseRequestPayload(request)
	}
	return strings.TrimSpace(result.RuntimeServerID), &request, false, nil
}

func (h crudRouteHandlers) writeManagedCreateAdmissionError(e *httpx.Event, stackID string, request jobs.ManagedLeaseRequest, err error, phase string) error {
	_ = request
	_ = err
	return managedCreateUnavailable(e, stackID, "provider_control_not_available_in_open_core", false,
		"Managed provider creation is not part of the Open-Core runtime")
}

func managedCreateUnavailable(e *httpx.Event, stackID, reasonCode string, retryable bool, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, map[string]any{
		creationStackIDField: stackID, "reason_code": reasonCode, "retryable": retryable,
	})
}

// applyCreateOwnerBootstrap applies the owner bootstrap side effects when the
// normalized request carries one; it returns nil (no-op) otherwise. Encapsulating
// the request lookup plus the apply call keeps createNormalizedStack flat.
func (h crudRouteHandlers) applyCreateOwnerBootstrap(e *httpx.Event, ownerID string, stack *persistedStack, normalized normalizedCreateStackRequest) error {
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok {
		return nil
	}
	tenantID := firstNonEmpty(tenantIDFromRequest(e), ownerID)
	if err := h.applyOwnerBootstrap(e.Request.Context(), tenantID, ownerID, stack.Id, normalized.Name, bootstrap); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to apply owner bootstrap", nil)
	}
	return nil
}

// issueCreateOwnerSpecAccess mints the owner-spec bootstrap access for the new
// stack when the request carries an owner bootstrap; it returns the zero value
// (no access) otherwise. A token-issue failure is surfaced as the same internal
// error the inline create flow returned.
func (h crudRouteHandlers) issueCreateOwnerSpecAccess(e *httpx.Event, stack *persistedStack, ownerID string, normalized normalizedCreateStackRequest) (ownerSpecBootstrapAccess, error) {
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok || !ownerSourceSeedsPocketID(bootstrap.Source) {
		return ownerSpecBootstrapAccess{}, nil
	}
	access, tokenErr := h.issueOwnerSpecBootstrapAccess(stack.Id, ownerID, time.Now().UTC())
	if tokenErr != nil {
		return ownerSpecBootstrapAccess{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}
	return access, nil
}

// dispatchCreateStack starts the provision job via the orchestrator when one is
// wired, falling back to the legacy queued-job path otherwise.
func (h crudRouteHandlers) dispatchCreateStack(e *httpx.Event, dispatch createStackDispatch) error {
	if h.orch != nil {
		return h.startCreateStackWithOrchestrator(e, dispatch)
	}
	return h.startCreateStackLegacy(e, dispatch)
}

func (h crudRouteHandlers) provisionStack(e *httpx.Event) error {
	stackID := e.Request.PathValue("id")
	if h.useControlPlaneStore(e) {
		stack, findErr := h.findOwnedStoreStack(e, stackID)
		if findErr != nil {
			return findErr
		}
		if activeErr := h.rejectActiveStoreStackDeploy(e, stack); activeErr != nil {
			return activeErr
		}
		spec, msg := stackSpecFromRequestOrStore(e.Request.Body, stack)
		if msg != "" {
			return httpx.BadRequest(e, msg)
		}
		spec, providerErr := canonicalizeFreshProvisionSpec(spec)
		if providerErr != nil {
			return httpx.BadRequest(e, "Invalid provider selection: "+providerErr.Error(), nil)
		}

		ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForStoreDeploy(stack)
		if ownerSpecErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
		}

		if h.orch != nil {
			jobID, provisionErr := h.orch.ProvisionStackWithOptions(stackID, spec, orchestrator.ProvisionStackOptions{
				RequestContext:     e.Request.Context(),
				TenantID:           stack.TenantID,
				OwnerID:            stack.OwnerSubjectID,
				StackName:          stack.Name,
				OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerSpecAccess),
			})
			if provisionErr != nil {
				h.markStackProvisionStartFailed(e.Request.Context(), stackID, stack.TenantID)
				return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
					"reason": provisionErr.Error(),
				})
			}
			return jobAccepted(e, "Provisioning started", jobID)
		}

		jobID, err := h.createQueuedJob(e, queuedJobParams{
			jobType:     "provision",
			stackID:     stackID,
			currentStep: "Queued (no orchestrator available)",
			stackStatus: "provisioning",
		})
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
		}
		return jobAccepted(e, "Provisioning job created (orchestrator not connected)", jobID)
	}

	stack, findErr := h.findOwnedStack(e, stackID)
	if findErr != nil {
		return findErr
	}
	if activeErr := rejectActiveStack(e, stack); activeErr != nil {
		return activeErr
	}

	spec, msg := stackSpecFromRequestOrRecord(e.Request.Body, stack)
	if msg != "" {
		return httpx.BadRequest(e, msg)
	}
	spec, providerErr := canonicalizeFreshProvisionSpec(spec)
	if providerErr != nil {
		return httpx.BadRequest(e, "Invalid provider selection: "+providerErr.Error(), nil)
	}

	ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForProvision(stack, spec)
	if ownerSpecErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}

	if h.orch != nil {
		jobID, provisionErr := h.orch.ProvisionStackWithOptions(stackID, spec, orchestrator.ProvisionStackOptions{
			OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerSpecAccess),
		})
		if provisionErr != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
				"reason": provisionErr.Error(),
			})
		}
		return jobAccepted(e, "Provisioning started", jobID)
	}

	jobID, err := h.createQueuedJob(e, queuedJobParams{
		jobType:     "provision",
		stackID:     stackID,
		currentStep: "Queued (no orchestrator available)",
		stackStatus: "provisioning",
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
	}
	return jobAccepted(e, "Provisioning job created (orchestrator not connected)", jobID)
}

func (h crudRouteHandlers) deployStack(e *httpx.Event) error {
	stackID := e.Request.PathValue("id")
	if h.useControlPlaneStore(e) {
		stack, findErr := h.findOwnedStoreStack(e, stackID)
		if findErr != nil {
			return findErr
		}
		if activeErr := rejectActiveStoreStack(e, stack); activeErr != nil {
			return activeErr
		}
		if h.orch == nil {
			return deployOrchestratorUnavailable(e, stackID)
		}

		// The create-time owner-spec bootstrap token (15 min TTL) is long
		// expired by the time a user-owned stack reaches "Review + Start", so
		// every rollout mints fresh access. Without it StackKit cannot fetch
		// the owner seed and the deploy would finish without a login handoff.
		ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForStoreDeploy(stack)
		if ownerSpecErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
		}

		jobID, deployErr := h.orch.DeployStackWithOptions(stackID, orchestrator.ProvisionStackOptions{
			RequestContext:     e.Request.Context(),
			TenantID:           stack.TenantID,
			OwnerID:            stack.OwnerSubjectID,
			StackName:          stack.Name,
			OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerSpecAccess),
		})
		if deployErr != nil {
			return h.deployStartError(e, stackID, deployErr)
		}
		return deployAccepted(e, jobID, ownerSpecAccess)
	}

	stack, findErr := h.findOwnedStack(e, stackID)
	if findErr != nil {
		return findErr
	}
	if activeErr := rejectActiveStack(e, stack); activeErr != nil {
		return activeErr
	}
	if h.orch == nil {
		return deployOrchestratorUnavailable(e, stackID)
	}

	// Same fresh owner-spec access as the store path; the stored stack spec
	// carries the seeded owner bootstrap for self-hosted rollouts.
	ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForProvision(stack, nil)
	if ownerSpecErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}

	jobID, deployErr := h.orch.DeployStackWithOptions(stackID, orchestrator.ProvisionStackOptions{
		OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerSpecAccess),
	})
	if deployErr != nil {
		return h.deployStartError(e, stackID, deployErr)
	}
	return deployAccepted(e, jobID, ownerSpecAccess)
}

func deployOrchestratorUnavailable(e *httpx.Event, stackID string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Rollout execution is unavailable; no orchestrator is connected", map[string]any{
		"stack_id":    stackID,
		"retryable":   true,
		"reason_code": "rollout_executor_unavailable",
	})
}

// deployStartError maps orchestrator deploy-start failures onto the existing
// response contract shared by both deploy paths.
func (h crudRouteHandlers) deployStartError(e *httpx.Event, stackID string, deployErr error) error {
	if errors.Is(deployErr, orchestrator.ErrDeployRuntimeEvidenceUnavailable) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical Guard runtime evidence is temporarily unavailable", map[string]any{
			"stack_id":    stackID,
			"retryable":   true,
			"reason_code": "deploy_runtime_evidence_unavailable",
		})
	}
	if errors.Is(deployErr, orchestrator.ErrNoAssignedWorkers) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Connect at least one assigned server with a fresh canonical Guard heartbeat before rollout", map[string]any{
			"stack_id": stackID,
		})
	}
	return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start deployment", map[string]any{
		"reason": deployErr.Error(),
	})
}

// deployAccepted mirrors jobAccepted but also surfaces the freshly minted
// owner-spec bootstrap access on the deploy response for observability.
func deployAccepted(e *httpx.Event, jobID string, access ownerSpecBootstrapAccess) error {
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		"success": true,
		"message": "Deployment started",
		"job_id":  jobID,
	}, access))
}

func (h crudRouteHandlers) destroyStack(e *httpx.Event) error {
	stackID := e.Request.PathValue("id")
	if h.useControlPlaneStore(e) {
		stack, err := h.findOwnedStoreStack(e, stackID)
		if err == nil {
			// Only the explicitly marked public-demo anchor is protected. Failed
			// visitor stacks in the same tenant remain removable.
			if demoProtectedStoreStackRequest(e, stack) {
				return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
					"The protected kombify demo anchor cannot be destroyed", demoRestrictedStackDetails("stack_destroy"))
			}
			if h.orch != nil {
				jobID, destroyErr := h.orch.DestroyStackWithOptions(stackID, orchestrator.ProvisionStackOptions{
					RequestContext: e.Request.Context(),
					TenantID:       stack.TenantID,
					OwnerID:        stack.OwnerSubjectID,
					StackName:      stack.Name,
				})
				if destroyErr != nil {
					return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start destruction", map[string]any{
						"reason": destroyErr.Error(),
					})
				}
				return jobAccepted(e, "Destroy started", jobID)
			}

			jobID, err := h.createQueuedJob(e, queuedJobParams{
				jobType:     "destroy",
				stackID:     stackID,
				currentStep: "Queued (no orchestrator available)",
				stackStatus: "stopping",
			})
			if err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
			}
			return jobAccepted(e, "Destroy job created (orchestrator not connected)", jobID)
		}
		var apiErr *httpx.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			return err
		}
	}

	legacyStack, err := h.findOwnedStack(e, stackID)
	if err != nil {
		return err
	}
	if demoProtectedLegacyStackRequest(e, legacyStack) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"The protected kombify demo anchor cannot be destroyed", demoRestrictedStackDetails("stack_destroy"))
	}

	if h.orch != nil {
		jobID, destroyErr := h.orch.DestroyStack(stackID)
		if destroyErr != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start destruction", map[string]any{
				"reason": destroyErr.Error(),
			})
		}
		h.markLegacyStackDeleted(legacyStack)
		return jobAccepted(e, "Destroy started", jobID)
	}

	jobID, err := h.createQueuedJob(e, queuedJobParams{
		jobType:     "destroy",
		stackID:     stackID,
		currentStep: "Queued (no orchestrator available)",
		stackStatus: "stopping",
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
	}
	h.markLegacyStackDeleted(legacyStack)
	return jobAccepted(e, "Destroy job created (orchestrator not connected)", jobID)
}

// markLegacyStackDeleted stamps deleted_at on a legacy PocketBase stack record
// once its destroy job is dispatched, so the dashboard row disappears
// immediately instead of lingering as a dead card. Best-effort: the destroy job
// remains the runtime source of truth.
func (h crudRouteHandlers) markLegacyStackDeleted(stack *core.Record) {
	if stack == nil || h.app == nil || stack.Collection().Fields.GetByName("deleted_at") == nil {
		return
	}
	stack.Set("deleted_at", time.Now().UTC())
	_ = h.app.Save(stack) // pocketbase-migration-compat: legacy dead-card removal during PB retirement
}

func (h crudRouteHandlers) jobStats(e *httpx.Event) error {
	if h.orch == nil {
		return httpx.Success(e, http.StatusOK, map[string]any{
			"available": false,
			"message":   "Orchestrator not connected",
		})
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"available": true,
		"stats":     h.orch.GetQueueStats(),
	})
}

func tenantIDFromRequest(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	id := identity.FromContext(e.Request.Context())
	if id != nil && strings.TrimSpace(id.OrgID) != "" {
		return strings.TrimSpace(id.OrgID)
	}
	if e.Auth != nil {
		return strings.TrimSpace(e.Auth.GetString("org_id"))
	}
	return ""
}

func (h crudRouteHandlers) useControlPlaneStore(e *httpx.Event) bool {
	return h.stackStore != nil && tenantIDFromRequest(e) != ""
}

func (h crudRouteHandlers) findOwnedStack(e *httpx.Event, stackID string) (*core.Record, error) {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return nil, authErr
	}
	if strings.TrimSpace(stackID) == "" {
		return nil, httpx.NewBadRequestError("Stack ID is required", nil)
	}
	stack, err := h.app.FindRecordById("stacks", stackID)
	if err != nil {
		return nil, httpx.NewNotFoundError("Stack not found", nil)
	}
	if stack.GetString("owner_id") != ownerID {
		return nil, httpx.NewForbiddenError("Not your stack", nil)
	}
	if !stack.GetDateTime("deleted_at").IsZero() {
		return nil, httpx.NewNotFoundError("Stack not found", nil)
	}
	return stack, nil
}

func (h crudRouteHandlers) findOwnedStoreStack(e *httpx.Event, stackID string) (*controlplane.Stack, error) {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return nil, authErr
	}
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return nil, httpx.NewBadRequestError("Stack ID is required", nil)
	}
	tenantID := tenantIDFromRequest(e)
	if tenantID == "" {
		return nil, httpx.NewNotFoundError("Stack not found", nil)
	}
	stack, err := h.stackStore.GetStack(e.Request.Context(), tenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil, httpx.NewNotFoundError("Stack not found", nil)
		}
		return nil, httpx.NewInternalServerError("Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return nil, httpx.NewForbiddenError("Not your stack", nil)
	}
	return stack, nil
}

func (h crudRouteHandlers) startCreateStackWithOrchestrator(e *httpx.Event, dispatch createStackDispatch) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	autoDeploy := shouldStartRolloutAfterCreate(dispatch.spec)
	startCtx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	jobID, err := h.orch.ProvisionStackWithOptions(dispatch.stackID, dispatch.spec, orchestrator.ProvisionStackOptions{
		AutoDeploy:           autoDeploy,
		OwnerSpecBootstrap:   ownerSpecRuntimeBootstrap(dispatch.ownerSpecAccess),
		RequestContext:       startCtx,
		OwnerID:              ownerID,
		StackName:            dispatch.name,
		TenantID:             tenantIDFromRequest(e),
		PreparedManagedLease: dispatch.preparedLease,
	})
	if err != nil {
		if createStackIdempotencyKey(e) != "" {
			if handled, replayErr := h.replayCreateJob(e, &persistedStack{Id: dispatch.stackID, Name: dispatch.name, IdempotentReplay: true}, dispatch.serverID); handled || replayErr != nil {
				return replayErr
			}
		}
		h.markStackProvisionStartFailed(e.Request.Context(), dispatch.stackID, tenantIDFromRequest(e))
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
			"reason": err.Error(),
		})
	}
	message := "Stack created; Unifier started"
	if autoDeploy {
		message = "Stack created; managed runtime preparation started. Rollout waits for fresh Guard verification."
	}
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		"stack_id":       dispatch.stackID,
		"server_id":      dispatch.serverID,
		"job_id":         jobID,
		"name":           dispatch.name,
		"state":          "provisioning",
		"message":        message,
		"auto_deploy":    autoDeploy,
		"operations_url": operationsURL(dispatch.stackID),
	}, dispatch.ownerSpecAccess))
}

func (h crudRouteHandlers) markStackProvisionStartFailed(ctx context.Context, stackID, tenantID string) {
	if h.stackStore != nil && strings.TrimSpace(tenantID) != "" {
		if _, err := h.stackStore.UpdateStackRuntime(ctx, tenantID, stackID, controlplane.RuntimeUpdate{
			Status: "failed",
		}); err == nil {
			return
		}
	}
	if stack, err := h.app.FindRecordById("stacks", stackID); err == nil { // pocketbase-migration-compat: legacy status fallback when store update is unavailable
		stack.Set("status", "failed")
		_ = h.app.Save(stack) // pocketbase-migration-compat: legacy status fallback when store update is unavailable
	}
}

// shouldStartRolloutAfterCreate returns true when the persisted stack spec
// selects a managed cloud runtime (kombify-cloud / monthly runtime). In that
// case the provision job requests a guarded auto-rollout. The job may chain
// into deploy only after the canonical control plane proves the exact native
// lease and a fresh Guard runtime. Self-hosted and connect-remote flows still
// require explicit "Review + Start" because they need worker registration first.
func shouldStartRolloutAfterCreate(spec map[string]interface{}) bool {
	if spec == nil {
		return false
	}
	fields := runtimeFieldsFromConfig(spec)
	return hasManagedRuntimeFields(spec, fields)
}

func ownerSpecRuntimeBootstrap(access ownerSpecBootstrapAccess) *jobs.OwnerSpecBootstrap {
	if !access.complete() {
		return nil
	}
	return &jobs.OwnerSpecBootstrap{
		Endpoint:  access.Endpoint,
		Token:     access.Token,
		ExpiresAt: access.ExpiresAt.UTC().Format(time.RFC3339),
		Scopes:    []string{ownerSpecReadScope},
	}
}

func (h crudRouteHandlers) ownerSpecBootstrapAccessForProvision(stack *core.Record, spec map[string]interface{}) (ownerSpecBootstrapAccess, error) {
	if stack == nil || !stackHasOwnerBootstrapForProvision(stack, spec) {
		return ownerSpecBootstrapAccess{}, nil
	}
	ownerID := strings.TrimSpace(stack.GetString("owner_id"))
	if ownerID == "" {
		return ownerSpecBootstrapAccess{}, fmt.Errorf("stack owner id is required for owner spec bootstrap")
	}
	return h.issueOwnerSpecBootstrapAccess(stack.Id, ownerID, time.Now().UTC())
}

// ownerSpecBootstrapAccessForStoreDeploy mints owner-spec bootstrap access for
// a control-plane (Postgres) stack whose stored config carries a seeded owner
// bootstrap. It returns the zero value when the stack has no owner seed.
func (h crudRouteHandlers) ownerSpecBootstrapAccessForStoreDeploy(stack *controlplane.Stack) (ownerSpecBootstrapAccess, error) {
	if stack == nil {
		return ownerSpecBootstrapAccess{}, nil
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalizedCreateStackRequest{UserConfig: stack.Config})
	if !ok || !ownerSourceSeedsPocketID(bootstrap.Source) {
		return ownerSpecBootstrapAccess{}, nil
	}
	ownerID := strings.TrimSpace(stack.OwnerSubjectID)
	if ownerID == "" {
		return ownerSpecBootstrapAccess{}, fmt.Errorf("stack owner id is required for owner spec bootstrap")
	}
	return h.issueOwnerSpecBootstrapAccess(stack.ID, ownerID, time.Now().UTC())
}

func stackHasOwnerBootstrapForProvision(stack *core.Record, spec map[string]interface{}) bool {
	if bootstrap, ok := ownerBootstrapFromRequest(normalizedCreateStackRequest{UserConfig: spec}); ok {
		return ownerSourceSeedsPocketID(bootstrap.Source)
	}
	storedSpec, msg := stackSpecFromRecord(stack)
	if msg != "" {
		return false
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalizedCreateStackRequest{UserConfig: storedSpec})
	return ok && ownerSourceSeedsPocketID(bootstrap.Source)
}

func (h crudRouteHandlers) startCreateStackLegacy(e *httpx.Event, dispatch createStackDispatch) error {
	jobID, err := h.createQueuedJob(e, queuedJobParams{
		jobType:     "provision",
		stackID:     dispatch.stackID,
		currentStep: "Queued (no orchestrator available)",
		stackStatus: "provisioning",
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
	}
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		"stack_id":       dispatch.stackID,
		"server_id":      dispatch.serverID,
		"job_id":         jobID,
		"name":           dispatch.name,
		"state":          "provisioning",
		"message":        "Stack created; job queued (orchestrator not connected)",
		"operations_url": operationsURL(dispatch.stackID),
	}, dispatch.ownerSpecAccess))
}

func (h crudRouteHandlers) createQueuedJob(e *httpx.Event, params queuedJobParams) (string, error) {
	if h.jobStore == nil {
		return createLegacyJob(h.app, params.jobType, params.stackID, params.currentStep, params.stackStatus)
	}
	tenantID := tenantIDFromRequest(e)
	if tenantID == "" {
		return createLegacyJob(h.app, params.jobType, params.stackID, params.currentStep, params.stackStatus)
	}
	job, err := h.jobStore.UpsertJob(e.Request.Context(), controlplane.UpsertJobRequest{
		ID:       uuid.NewString(),
		TenantID: tenantID,
		StackID:  params.stackID,
		Type:     params.jobType,
		State:    "pending",
		Step:     params.currentStep,
		Message:  params.currentStep,
	})
	if err != nil {
		return "", err
	}
	if h.stackStore != nil {
		_, _ = h.stackStore.UpdateStackRuntime(e.Request.Context(), tenantID, params.stackID, controlplane.RuntimeUpdate{
			Status: params.stackStatus,
		})
	}
	return job.ID, nil
}
func stackSpecFromRequestOrRecord(body io.Reader, stack *core.Record) (map[string]interface{}, string) {
	spec, msg := stackSpecFromRequest(body)
	if msg != "" || spec != nil {
		return spec, msg
	}
	return stackSpecFromRecord(stack)
}

func stackSpecFromRequestOrStore(body io.Reader, stack *controlplane.Stack) (map[string]interface{}, string) {
	spec, msg := stackSpecFromRequest(body)
	if msg != "" || spec != nil {
		return spec, msg
	}
	if stack == nil || stack.Config == nil {
		return nil, "No spec provided and no config stored in stack"
	}
	if userConfig, ok := stackSpecMapFromValue(stack.Config["user_config"]); ok {
		// Native-v2 stacks keep their projected spec as a config sibling of
		// user_config; a re-provision must carry it into the job payload or
		// the rollout degrades to the template projection.
		if specV2, ok := stackSpecMapFromValue(stack.Config[stackConfigKeySpecV2]); ok {
			if _, exists := userConfig[stackConfigKeySpecV2]; !exists {
				withSpec := make(map[string]interface{}, len(userConfig)+1)
				for key, value := range userConfig {
					withSpec[key] = value
				}
				withSpec[stackConfigKeySpecV2] = specV2
				return withSpec, ""
			}
		}
		return userConfig, ""
	}
	if config, ok := stackSpecMapFromValue(stack.Config); ok {
		return config, ""
	}
	return nil, "Invalid config stored in stack"
}

func stackSpecFromRequest(body io.Reader) (map[string]interface{}, string) {
	var spec map[string]interface{}
	data, err := io.ReadAll(body)
	if err == nil && len(data) > 0 {
		if jsonErr := json.Unmarshal(data, &spec); jsonErr != nil {
			return nil, "Invalid JSON in request body"
		}
	}
	if spec != nil {
		// Only the wizard-run facade (after fail-closed projection + pinned
		// CLI validation) and the stored config may carry the v2 projection;
		// a request body must not smuggle one past that admission.
		delete(spec, stackConfigKeySpecV2)
	}
	return spec, ""
}

func stackSpecMapFromValue(value any) (map[string]interface{}, bool) {
	if value == nil {
		return nil, false
	}
	if configMap, ok := value.(map[string]interface{}); ok {
		return configMap, true
	}
	configBytes, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(configBytes, &spec); err != nil {
		return nil, false
	}
	return spec, true
}

func stackSpecFromRecord(stack *core.Record) (map[string]interface{}, string) {
	configVal := stack.Get("user_config")
	if configVal == nil {
		configVal = stack.Get("config")
	}
	if configVal == nil {
		return nil, "No spec provided and no user_config/config stored in stack"
	}
	if configMap, ok := configVal.(map[string]interface{}); ok {
		return configMap, ""
	}

	configBytes, err := json.Marshal(configVal)
	if err != nil {
		return nil, "Failed to read config"
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(configBytes, &spec); err != nil {
		return nil, "Invalid config stored in stack"
	}
	return spec, ""
}

func rejectActiveStack(e *httpx.Event, stack *core.Record) error {
	status := stack.GetString("status")
	if status == "running" || status == "provisioning" {
		return httpx.BadRequest(e, "Stack is already running or provisioning")
	}
	return nil
}

func rejectActiveStoreStack(e *httpx.Event, stack *controlplane.Stack) error {
	if stack == nil {
		return httpx.NewNotFoundError("Stack not found", nil)
	}
	status := strings.TrimSpace(stack.Status)
	if status == "running" || status == "provisioning" {
		return httpx.NewBadRequestError("Stack is already running or provisioning", nil)
	}
	return nil
}

// rejectActiveStoreStackDeploy keeps generic provision fencing intact while
// allowing a user-owned server to start a fresh StackKit rollout after the
// newest deploy/provision attempt failed. The newest durable job is the
// authority; an older historical failure never reopens a running stack.
func (h crudRouteHandlers) rejectActiveStoreStackDeploy(e *httpx.Event, stack *controlplane.Stack) error {
	if stack == nil {
		return httpx.NewNotFoundError("Stack not found", nil)
	}
	status := strings.TrimSpace(stack.Status)
	if status != "running" {
		return rejectActiveStoreStack(e, stack)
	}
	if h.jobStore != nil {
		jobs, err := h.jobStore.ListJobsByStack(e.Request.Context(), stack.TenantID, stack.ID, 1)
		if err == nil && latestJobAllowsFreshRollout(jobs) {
			return nil
		}
	}
	return httpx.NewBadRequestError("Stack is already running or provisioning", nil)
}

func latestJobAllowsFreshRollout(jobs []controlplane.Job) bool {
	if len(jobs) == 0 || strings.TrimSpace(strings.ToLower(jobs[0].State)) != "failed" {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(jobs[0].Type)) {
	case "deploy", "provision":
		return true
	default:
		return false
	}
}

func jobAccepted(e *httpx.Event, message, jobID string) error {
	return httpx.Success(e, http.StatusAccepted, map[string]any{
		"success": true,
		"message": message,
		"job_id":  jobID,
	})
}
