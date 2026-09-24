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
	"github.com/pocketbase/pocketbase/core"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/providercontrol"
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
	activityStore      controlplane.ActivityStore
	serverStore        controlplane.ServerRuntimeStore
	serviceStore       controlplane.ServiceRuntimeStore
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
	tenantID        string
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
	tenantID    string
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
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.list")
	if tenantErr != nil {
		return tenantErr
	}
	if h.stackStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack authority is temporarily unavailable", map[string]any{
				"reason_code": "stack_authority_unavailable",
				"retryable":   true,
			})
	}
	return h.listStacksFromStore(e, ownerID, tenantID)
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
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.read")
	if tenantErr != nil {
		return tenantErr
	}
	if h.stackStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack authority is temporarily unavailable", map[string]any{
				"reason_code": "stack_authority_unavailable",
				"retryable":   true,
			})
	}
	stack, err := h.stackStore.GetStack(e.Request.Context(), tenantID, strings.TrimSpace(e.Request.PathValue("id")))
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NewNotFoundError("Stack not found", nil)
	}
	if err != nil {
		return httpx.NewInternalServerError("Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return httpx.NewForbiddenError("Not your stack", nil)
	}
	return httpx.Success(e, http.StatusOK, stackListItemFromStore(*stack))
}

func (h crudRouteHandlers) createStack(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.create")
	if tenantErr != nil {
		return tenantErr
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
	return h.createNormalizedStack(e, ownerID, tenantID, normalized)
}

// ownerBootstrapContextForCreate builds the resolve context for the create
// flow, attaching only the server-side profile authority selected by the
// request. Neither source accepts identity fields from request JSON.
func (h crudRouteHandlers) ownerBootstrapContextForCreate(e *httpx.Event, ownerID string, normalized normalizedCreateStackRequest) ownerBootstrapContext {
	ctx := ownerBootstrapContextFromRequest(e)
	if requestsCloudLinkedOwner(normalized) {
		ctx.CloudLink = cloudLinkForOwner(h.app, ownerID)
	}
	if requestsAutomaticCloudOwner(normalized) {
		ctx.CurrentProfile = verifiedCurrentCloudProfile(h.app, ownerID)
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
func (h crudRouteHandlers) createNormalizedStack(e *httpx.Event, ownerID, tenantID string, normalized normalizedCreateStackRequest) error {
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
	stack, err := h.persistStack(e, ownerID, tenantID, normalized)
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
	serverID, preparedLease, admissionHandled, admissionErr := h.admitManagedCreate(e, ownerID, tenantID, stack, normalized)
	if admissionHandled || admissionErr != nil {
		h.markStackProvisionStartFailed(e.Request.Context(), stack.Id, tenantID)
		return admissionErr
	}
	var serverErr error
	if serverID == "" {
		serverID, serverErr = h.persistCreateServerIntent(e, ownerID, tenantID, stack, normalized)
	}
	if serverErr != nil {
		h.markStackProvisionStartFailed(e.Request.Context(), stack.Id, tenantID)
		logger.Default().Error("create_server_intent_failed", "error", serverErr, "stack_id", stack.Id, "tenant_id", tenantID)
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist server intent", map[string]any{
			creationStackIDField: stack.Id, creationOperationsURLField: homelabDashboardURL(),
		})
	}
	if stack.IdempotentReplay {
		if handled, replayErr := h.replayCreateJob(e, tenantID, stack, serverID); handled || replayErr != nil {
			return replayErr
		}
	}
	if bootstrapErr := h.applyCreateOwnerBootstrap(e, ownerID, tenantID, stack, normalized); bootstrapErr != nil {
		return bootstrapErr
	}
	ownerSpecAccess, accessErr := h.issueCreateOwnerSpecAccess(e, stack, ownerID, tenantID, normalized)
	if accessErr != nil {
		return accessErr
	}
	return h.dispatchCreateStack(e, createStackDispatch{
		tenantID:        tenantID,
		stackID:         stack.Id,
		serverID:        serverID,
		name:            normalized.Name,
		spec:            createStackJobSpec(normalized),
		ownerSpecAccess: ownerSpecAccess,
		preparedLease:   preparedLease,
	})
}

func (h crudRouteHandlers) admitManagedCreate(e *httpx.Event, ownerID, tenantID string, stack *persistedStack, normalized normalizedCreateStackRequest) (string, *jobs.ManagedLeaseRequest, bool, error) {
	policyConfig := runtimePolicyConfigFromRequest(normalized)
	if !hasManagedRuntimeFields(policyConfig, runtimeFieldsFromConfig(policyConfig)) {
		return "", nil, false, nil
	}
	if h.managedLeases == nil {
		return "", nil, true, managedCreateUnavailable(e, stack.Id, "native_admission_unavailable", true, "Native managed runtime admission is not configured")
	}
	request, err := jobs.PrimaryManagedLeaseRequestFromUIConfig(
		createStackJobSpec(normalized), stack.Id, stack.Name, tenantID, ownerID,
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
	details := map[string]any{creationStackIDField: stackID, "admission_phase": phase}
	var capacity *providercontrol.ManagedRuntimeCapacityExceededError
	if errors.As(err, &capacity) {
		details["reason_code"] = "managed_runtime_capacity_exceeded"
		details["retryable"] = false
		h.enqueueManagedRuntimeCapacityNotification(e.Request.Context(), stackID, request, capacity)
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "This account already holds the maximum number of managed server slots", details)
	}
	var createBlocked providercontrol.ProviderCreateBlockedError
	if errors.As(err, &createBlocked) {
		details["reason_code"] = createBlocked.ReasonCode
		details["retryable"] = false
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "New managed servers are temporarily paused for this provider", details)
	}
	switch {
	case errors.Is(err, providercontrol.ErrMutationActivationBlocked):
		details["reason_code"] = "provider_control_not_activated"
		details["retryable"] = false
	case errors.Is(err, providercontrol.ErrManagedRuntimeUnclassifiedCustody):
		details["reason_code"] = "provider_custody_reconciliation_required"
		details["retryable"] = false
	case errors.Is(err, providercontrol.ErrNativeAdmissionConflict):
		details["reason_code"] = "native_admission_conflict"
		details["retryable"] = false
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Native managed runtime admission conflicts with the persisted operation", details)
	default:
		details["reason_code"] = "native_admission_outcome_unconfirmed"
		details["retryable"] = true
	}
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Managed server admission was not accepted", details)
}

func (h crudRouteHandlers) enqueueManagedRuntimeCapacityNotification(
	ctx context.Context,
	stackID string,
	request jobs.ManagedLeaseRequest,
	capacity *providercontrol.ManagedRuntimeCapacityExceededError,
) {
	if h.notificationOutbox == nil || capacity == nil {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	tenantID := strings.TrimSpace(request.TenantID)
	ownerID := strings.TrimSpace(request.OwnerID)
	if provider == "" || tenantID == "" || ownerID == "" {
		return
	}
	occurredAt := time.Now().UTC()
	key := fmt.Sprintf("techstack-managed-runtime-capacity:%s:%s:%s:%s", tenantID, provider, stackID, request.OperationKey)
	payload := map[string]any{
		"subject":     "Managed runtime capacity exhausted",
		"body":        "New managed servers cannot be deployed because the provider capacity is full.",
		"severity":    "critical",
		"reason_code": "managed_runtime_capacity_exceeded",
		"provider":    provider,
		"tenant_id":   tenantID,
		"held":        capacity.Held,
		"limit":       capacity.Limit,
		"occurred_at": occurredAt.Format(time.RFC3339Nano),
		"source_app":  managedRuntimeCapacitySource,
		"event_key":   "managed_runtime.capacity_exceeded",
		"link_url":    "/dashboard",
	}
	for _, channel := range []string{"in_app", "email"} {
		err := h.notificationOutbox.Enqueue(ctx, productnotifications.ProductEvent{
			Topic: managedRuntimeCapacityTopic, Channel: channel,
			Auth0UserID: ownerID, OrganizationID: tenantID,
			IdempotencyKey: key + ":" + channel, Payload: payload,
		})
		if err != nil {
			logger.Get().Warn("managed_runtime_capacity_notification_enqueue_failed",
				"tenant_id", tenantID, "provider", provider, "channel", channel, "error", err)
		}
	}
}

func managedCreateUnavailable(e *httpx.Event, stackID, reasonCode string, retryable bool, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, map[string]any{
		creationStackIDField: stackID, "reason_code": reasonCode, "retryable": retryable,
	})
}

// applyCreateOwnerBootstrap applies the owner bootstrap side effects when the
// normalized request carries one; it returns nil (no-op) otherwise. Encapsulating
// the request lookup plus the apply call keeps createNormalizedStack flat.
func (h crudRouteHandlers) applyCreateOwnerBootstrap(e *httpx.Event, ownerID, tenantID string, stack *persistedStack, normalized normalizedCreateStackRequest) error {
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok {
		return nil
	}
	if err := h.applyOwnerBootstrap(e.Request.Context(), tenantID, ownerID, stack.Id, normalized.Name, bootstrap); err != nil {
		return httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to apply owner bootstrap", nil)
	}
	return nil
}

// issueCreateOwnerSpecAccess mints the owner-spec bootstrap access for the new
// stack when the request carries an owner bootstrap; it returns the zero value
// (no access) otherwise. A token-issue failure is surfaced as the same internal
// error the inline create flow returned.
func (h crudRouteHandlers) issueCreateOwnerSpecAccess(e *httpx.Event, stack *persistedStack, ownerID, tenantID string, normalized normalizedCreateStackRequest) (ownerSpecBootstrapAccess, error) {
	bootstrap, ok := ownerBootstrapFromRequest(normalized)
	if !ok || !ownerSourceSeedsPocketID(bootstrap.Source) {
		return ownerSpecBootstrapAccess{}, nil
	}
	access, tokenErr := h.issueOwnerSpecBootstrapAccessForTenant(e.Request.Context(), tenantID, stack.Id, ownerID, time.Now().UTC())
	if tokenErr != nil {
		return ownerSpecBootstrapAccess{}, httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}
	return access, nil
}

// dispatchCreateStack starts the provision job via the orchestrator when one is
// wired, otherwise persisting a canonical queued job.
func (h crudRouteHandlers) dispatchCreateStack(e *httpx.Event, dispatch createStackDispatch) error {
	if h.orch != nil {
		return h.startCreateStackWithOrchestrator(e, dispatch)
	}
	return h.startCreateStackQueued(e, dispatch)
}

func (h crudRouteHandlers) provisionStack(e *httpx.Event) error {
	if h.stackStore == nil || h.jobStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack provision authority is temporarily unavailable", map[string]any{
				"reason_code": "stack_provision_authority_unavailable",
				"retryable":   true,
			})
	}
	stackID := e.Request.PathValue("id")
	stack, findErr := h.findOwnedStoreStack(e, stackID)
	if findErr != nil {
		return findErr
	}
	idempotencyKey, idempotencyErr := readStackLifecycleIdempotencyKey(e.Request)
	if idempotencyErr != nil {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Exactly one valid Idempotency-Key header is required when provision retries are keyed", map[string]any{
				"reason_code": "idempotency_key_invalid", "retryable": false,
			})
	}
	if idempotencyKey == "" {
		if activeErr := h.rejectActiveStoreStackDeploy(e, stack); activeErr != nil {
			return activeErr
		}
	} else {
		replayJobID, replayIDErr := orchestrator.ProvisionIdempotencyJobID(stack.TenantID, stack.OwnerSubjectID, stack.ID, idempotencyKey)
		if replayIDErr != nil {
			return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
				"Invalid Idempotency-Key header", map[string]any{"reason_code": "idempotency_key_invalid", "retryable": false})
		}
		if _, replayErr := h.jobStore.GetJob(e.Request.Context(), stack.TenantID, replayJobID); errors.Is(replayErr, controlplane.ErrNotFound) {
			if activeErr := h.rejectActiveStoreStackDeploy(e, stack); activeErr != nil {
				return activeErr
			}
		} else if replayErr != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Provision retry state is temporarily unavailable", map[string]any{
					"reason_code": "provision_idempotency_unavailable", "retryable": true,
				})
		}
	}
	if idempotencyKey != "" && h.orch == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Keyed provision retries require the durable orchestrator", map[string]any{
				"reason_code": "provision_idempotency_unavailable", "retryable": true,
			})
	}
	spec, msg := stackSpecFromRequestOrStore(e.Request.Body, stack)
	if msg != "" {
		return httpx.BadRequest(e, msg)
	}
	spec, providerErr := canonicalizeFreshProvisionSpec(spec)
	if providerErr != nil {
		return httpx.BadRequest(e, "Invalid provider selection: "+providerErr.Error(), nil)
	}

	ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForStoreDeploy(e.Request.Context(), stack)
	if ownerSpecErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}

	if h.orch != nil {
		jobID, provisionErr := h.orch.ProvisionStackWithOptions(stackID, spec, orchestrator.ProvisionStackOptions{
			RequestContext:     e.Request.Context(),
			TenantID:           stack.TenantID,
			OwnerID:            stack.OwnerSubjectID,
			StackName:          stack.Name,
			IdempotencyKey:     idempotencyKey,
			OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerSpecAccess),
		})
		if provisionErr != nil {
			if errors.Is(provisionErr, orchestrator.ErrProvisionIdempotencyConflict) {
				return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
					"Idempotency-Key was already used for a different provision request", map[string]any{
						"reason_code": "idempotency_conflict", "retryable": false,
					})
			}
			h.markStackProvisionStartFailed(e.Request.Context(), stackID, stack.TenantID)
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
				"reason": provisionErr.Error(),
			})
		}
		return jobAccepted(e, "Provisioning started", jobID)
	}

	jobID, err := h.createQueuedJob(e, queuedJobParams{
		tenantID:    stack.TenantID,
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

func readStackLifecycleIdempotencyKey(request *http.Request) (string, error) {
	if request == nil {
		return "", nil
	}
	values := request.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("multiple Idempotency-Key values")
	}
	return orchestrator.ValidateProvisionIdempotencyKey(values[0])
}

func (h crudRouteHandlers) deployStack(e *httpx.Event) error {
	if h.stackStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack deploy authority is temporarily unavailable", map[string]any{
				"reason_code": "stack_deploy_authority_unavailable",
				"retryable":   true,
			})
	}
	stackID := e.Request.PathValue("id")
	stack, findErr := h.findOwnedStoreStack(e, stackID)
	if findErr != nil {
		return findErr
	}
	idempotencyKey, idempotencyErr := readStackLifecycleIdempotencyKey(e.Request)
	if idempotencyErr != nil {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Exactly one valid Idempotency-Key header is required when deploy retries are keyed", map[string]any{
				"reason_code": "idempotency_key_invalid", "retryable": false,
			})
	}
	if idempotencyKey == "" {
		if activeErr := rejectActiveStoreStack(e, stack); activeErr != nil {
			return activeErr
		}
	} else if h.jobStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Deploy retry state is temporarily unavailable", map[string]any{
				"reason_code": "deploy_idempotency_unavailable", "retryable": true,
			})
	} else {
		replayJobID, replayIDErr := orchestrator.DeployIdempotencyJobID(stack.TenantID, stack.OwnerSubjectID, stack.ID, idempotencyKey)
		if replayIDErr != nil {
			return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
				"Invalid Idempotency-Key header", map[string]any{"reason_code": "idempotency_key_invalid", "retryable": false})
		}
		if _, replayErr := h.jobStore.GetJob(e.Request.Context(), stack.TenantID, replayJobID); errors.Is(replayErr, controlplane.ErrNotFound) {
			if activeErr := rejectActiveStoreStack(e, stack); activeErr != nil {
				return activeErr
			}
		} else if replayErr != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Deploy retry state is temporarily unavailable", map[string]any{
					"reason_code": "deploy_idempotency_unavailable", "retryable": true,
				})
		}
	}
	if h.orch == nil {
		return deployOrchestratorUnavailable(e, stackID)
	}

	// The create-time owner-spec bootstrap token (15 min TTL) is long expired by
	// "Review + Start", so every rollout mints fresh canonical access.
	ownerSpecAccess, ownerSpecErr := h.ownerSpecBootstrapAccessForStoreDeploy(e.Request.Context(), stack)
	if ownerSpecErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue owner bootstrap token", nil)
	}

	jobID, deployErr := h.orch.DeployStackWithOptions(stackID, orchestrator.ProvisionStackOptions{
		RequestContext:     e.Request.Context(),
		TenantID:           stack.TenantID,
		OwnerID:            stack.OwnerSubjectID,
		StackName:          stack.Name,
		IdempotencyKey:     idempotencyKey,
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
	if errors.Is(deployErr, orchestrator.ErrDeployIdempotencyConflict) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Idempotency-Key was already used for a different deploy request", map[string]any{
				"reason_code": "idempotency_conflict", "retryable": false,
			})
	}
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
	if h.stackStore == nil || h.jobStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack destroy authority is temporarily unavailable", map[string]any{
				"reason_code": "stack_destroy_authority_unavailable",
				"retryable":   true,
			})
	}
	stackID := e.Request.PathValue("id")
	stack, err := h.findOwnedStoreStack(e, stackID)
	if err != nil {
		return err
	}
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
		tenantID:    stack.TenantID,
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

func (h crudRouteHandlers) jobStats(e *httpx.Event) error {
	if _, err := requireStackAuth(e); err != nil {
		return err
	}
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

func (h crudRouteHandlers) findOwnedStoreStack(e *httpx.Event, stackID string) (*controlplane.Stack, error) {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return nil, authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.read")
	if tenantErr != nil {
		return nil, tenantErr
	}
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return nil, httpx.NewBadRequestError("Stack ID is required", nil)
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
	tenantID := strings.TrimSpace(dispatch.tenantID)
	jobID, err := h.orch.ProvisionStackWithOptions(dispatch.stackID, dispatch.spec, orchestrator.ProvisionStackOptions{
		AutoDeploy:           autoDeploy,
		OwnerSpecBootstrap:   ownerSpecRuntimeBootstrap(dispatch.ownerSpecAccess),
		RequestContext:       startCtx,
		OwnerID:              ownerID,
		StackName:            dispatch.name,
		TenantID:             tenantID,
		PreparedManagedLease: dispatch.preparedLease,
	})
	if err != nil {
		if createStackIdempotencyKey(e) != "" {
			if handled, replayErr := h.replayCreateJob(e, tenantID, &persistedStack{Id: dispatch.stackID, Name: dispatch.name, IdempotentReplay: true}, dispatch.serverID); handled || replayErr != nil {
				return replayErr
			}
		}
		h.markStackProvisionStartFailed(e.Request.Context(), dispatch.stackID, tenantID)
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Failed to start provisioning", map[string]any{
			"reason": err.Error(),
		})
	}
	message := "Stack created; Unifier started"
	if autoDeploy {
		message = "Stack created; managed runtime preparation started. Rollout waits for fresh Guard verification."
	}
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		"kit_deployment_id": dispatch.stackID,
		"server_id":         dispatch.serverID,
		"job_id":            jobID,
		"name":              dispatch.name,
		"state":             "provisioning",
		"message":           message,
		"auto_deploy":       autoDeploy,
		"operations_url":    homelabDashboardURL(),
	}, dispatch.ownerSpecAccess))
}

func (h crudRouteHandlers) markStackProvisionStartFailed(ctx context.Context, stackID, tenantID string) {
	if h.stackStore != nil && strings.TrimSpace(tenantID) != "" {
		_, _ = h.stackStore.UpdateStackRuntime(ctx, tenantID, stackID, controlplane.RuntimeUpdate{
			Status: "failed",
		})
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

// ownerSpecBootstrapAccessForStoreDeploy mints owner-spec bootstrap access for
// a canonical control-plane stack whose config carries a seeded owner bootstrap.
func (h crudRouteHandlers) ownerSpecBootstrapAccessForStoreDeploy(ctx context.Context, stack *controlplane.Stack) (ownerSpecBootstrapAccess, error) {
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
	tenantID := strings.TrimSpace(stack.TenantID)
	if tenantID == "" {
		return ownerSpecBootstrapAccess{}, fmt.Errorf("stack tenant id is required for owner spec bootstrap")
	}
	return h.issueOwnerSpecBootstrapAccessForTenant(ctx, tenantID, stack.ID, ownerID, time.Now().UTC())
}

func (h crudRouteHandlers) startCreateStackQueued(e *httpx.Event, dispatch createStackDispatch) error {
	if h.jobStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack job authority is temporarily unavailable", map[string]any{
				detailsKeyReasonCode: "stack_job_authority_unavailable",
				detailsKeyRetryable:  true,
			})
	}
	jobID, err := h.createQueuedJob(e, queuedJobParams{
		tenantID:    dispatch.tenantID,
		jobType:     "provision",
		stackID:     dispatch.stackID,
		currentStep: "Queued (no orchestrator available)",
		stackStatus: "provisioning",
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create job", nil)
	}
	return httpx.Success(e, http.StatusAccepted, addOwnerSpecResponseFields(map[string]any{
		"kit_deployment_id": dispatch.stackID,
		"server_id":         dispatch.serverID,
		"job_id":            jobID,
		"name":              dispatch.name,
		"state":             "provisioning",
		"message":           "Stack created; job queued (orchestrator not connected)",
		"operations_url":    homelabDashboardURL(),
	}, dispatch.ownerSpecAccess))
}

func (h crudRouteHandlers) createQueuedJob(e *httpx.Event, params queuedJobParams) (string, error) {
	if h.jobStore == nil {
		return "", fmt.Errorf("canonical job store is required")
	}
	tenantID := strings.TrimSpace(params.tenantID)
	if tenantID == "" {
		return "", fmt.Errorf("tenant context is required")
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
