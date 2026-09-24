package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	managedRuntimeJobClaimDelay        = time.Hour
	managedRuntimeJobStoreWriteTimeout = 5 * time.Second
	managedRuntimeJobTypeUpdate        = "update"
	managedRuntimeJobStateCompleted    = "completed"
	managedRuntimeDetailsReasonCodeKey = "reason_code"
	managedRuntimeDetailsJobIDKey      = "job_id"
	managedRuntimeDetailsRetryableKey  = "retryable"
	// managedRuntimeAuthorityPreflightOperationKey is deliberately distinct
	// from every product operation. The authority route is read-only: it
	// exercises the same native admission preflight but never creates a job,
	// lease, runtime server, or provider operation.
	managedRuntimeAuthorityPreflightOperationKey = "managed-runtime-authority-preflight"
	managedRuntimeAuthorityPreflightStackID      = "managed-runtime-authority-preflight"
	managedRuntimeAuthorityPreflightStackName    = "Managed runtime authority preflight"
	managedRuntimeAuthorityPreflightSlotKey      = "authority-preflight"
)

var managedRuntimeServiceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var managedRuntimeSlotKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type stackLifecycleLeaseService interface {
	Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error)
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
	Patch(ctx context.Context, tenantID string, id vmlease.LeaseID, req vmleases.PatchRequest) (*vmlease.Lease, error)
}

type stackLifecycleRuntimeClient interface {
	RuntimeAction(ctx context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error)
}

type stackLifecycleRouteHandlers struct {
	leases         stackLifecycleLeaseService
	runtime        stackLifecycleRuntimeClient
	features       monthlyruntime.FeatureChecker
	stacks         controlplane.StackStore
	workers        controlplane.WorkerStore
	jobs           controlplane.JobStore
	managedLeases  jobruntime.ManagedLeaseManager
	decommissioner jobruntime.ManagedLeaseDecommissioner
	now            func() time.Time
}

// StackLifecycleStores carries the control-plane stores the lifecycle handlers
// need to operate on the authoritative saas-standalone data.
type StackLifecycleStores struct {
	Stacks         controlplane.StackStore
	Workers        controlplane.WorkerStore
	Jobs           controlplane.JobStore
	Registry       controlplane.ServerRuntimeStore
	ManagedLeases  jobruntime.ManagedLeaseManager
	Decommissioner jobruntime.ManagedLeaseDecommissioner
}

type managedRuntimeServerRequest struct {
	RuntimeSlotKey     string   `json:"runtime_slot_key"`
	NodeRole           string   `json:"node_role"`
	RuntimeOfferingID  string   `json:"runtime_offering_id"`
	ProviderID         string   `json:"provider_id"`
	LeaseProvider      string   `json:"lease_provider"`
	SimulateProviderID string   `json:"simulate_provider_id"`
	ProviderRegion     string   `json:"provider_region"`
	IONOSDatacenter    string   `json:"ionos_datacenter"`
	StackKit           string   `json:"stackkit"`
	Services           []string `json:"services"`
}

// ManagedRuntimeExpansionResult is the public, secret-free outcome of the
// central managed-runtime expansion authority. Both the HTTP route and the
// Wizard adapter consume this exact result instead of defining parallel
// provider or receipt contracts.
type ManagedRuntimeExpansionResult struct {
	KitDeploymentID      string   `json:"kit_deployment_id"`
	JobID                string   `json:"job_id,omitempty"`
	RuntimeSlotKey       string   `json:"runtime_slot_key"`
	RuntimeSlotID        string   `json:"runtime_slot_id"`
	LeaseID              string   `json:"lease_id"`
	RuntimeServerID      string   `json:"runtime_server_id"`
	ResourceGenerationID string   `json:"resource_generation_id"`
	OperationID          string   `json:"operation_id"`
	ProviderID           string   `json:"provider_id"`
	ProviderRegion       string   `json:"provider_region,omitempty"`
	IONOSDatacenter      string   `json:"ionos_datacenter,omitempty"`
	NodeRole             string   `json:"node_role"`
	RuntimeOfferingID    string   `json:"runtime_offering_id"`
	EnrollmentStatus     string   `json:"enrollment_status"`
	RuntimePhase         string   `json:"runtime_phase"`
	IdempotentReplay     bool     `json:"idempotent_replay"`
	Message              string   `json:"message"`
	Warnings             []string `json:"warnings,omitempty"`
}

// ManagedRuntimeExpansionRequest is the normalized caller intent accepted by
// the event-callable expansion authority. Stack ownership and identity are
// still derived from the authenticated event; callers cannot provide them.
type ManagedRuntimeExpansionRequest struct {
	StackID           string
	IdempotencyKey    string
	RuntimeSlotKey    string
	NodeRole          string
	RuntimeOfferingID string
	ProviderID        string
	ProviderRegion    string
	IONOSDatacenter   string
	StackKit          string
	Services          []string
	// RecreateExpectedGeneration and RecreateRequiresReleasedSlot are set only
	// by the confirmed Recreate route. They bind the shared expansion authority
	// to exactly old generation N -> new generation N+1 without creating a
	// second provider mutation path.
	RecreateExpectedGeneration   uint64
	RecreateRequiresReleasedSlot bool
}

type managedRuntimeExpansionConstraints struct {
	expectedGeneration   uint64
	requiresReleasedSlot bool
}

// ManagedRuntimeExpansion owns the one provider-control expansion path. Its
// unexported handler retains the existing idempotency and completion-seal
// implementation while allowing another authenticated route to invoke it.
type ManagedRuntimeExpansion struct {
	h stackLifecycleRouteHandlers
}

// RegisterStackLifecycleRoutesWithStores wires lifecycle endpoints to the
// canonical control-plane authorities.
func RegisterStackLifecycleRoutesWithStores(r *httpx.Router, leases stackLifecycleLeaseService, runtime stackLifecycleRuntimeClient, featureChecker monthlyruntime.FeatureChecker, stores StackLifecycleStores) *ManagedRuntimeExpansion {
	if r == nil {
		return nil
	}
	h := stackLifecycleRouteHandlers{
		leases:         leases,
		runtime:        runtime,
		features:       featureChecker,
		stacks:         stores.Stacks,
		workers:        stores.Workers,
		jobs:           stores.Jobs,
		managedLeases:  stores.ManagedLeases,
		decommissioner: stores.Decommissioner,
	}
	// Provider-backed rows use the canonical decommission lifecycle; only true
	// lease-free local projections are archived by the generic cleanup action.
	r.POST("/api/v1/stacks/prune-orphans", h.pruneOrphanStacks)
	// This is a read-only diagnostic for the Gateway-to-native-admission
	// authority chain. It is intentionally separate from the Wizard mutation so
	// an E2E lane can prove the request-bound credit budget before it queues any
	// product or provider work.
	r.GET("/api/v1/managed-runtimes/authority", h.managedRuntimeAuthorityPreflight)
	r.POST("/api/v1/stacks/{id}/managed-runtimes", h.addManagedRuntimeServer)
	return &ManagedRuntimeExpansion{h: h}
}

// managedRuntimeAuthorityPreflight proves that a Gateway-authenticated request
// carries the exact signed managed-runtime authority required for one provider.
// It does not grant or reserve capacity: native admission repeats the mutable
// checks atomically when the real Wizard request is accepted.
func (h stackLifecycleRouteHandlers) managedRuntimeAuthorityPreflight(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	if e == nil || e.Request == nil || !middleware.IsEdgeAuthenticated(e.Request.Context()) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime authority must be verified by the Gateway", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "request_bound_edge_authority_required",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.managed-runtimes.authority")
	if tenantErr != nil {
		return tenantErr
	}
	providerID, err := providercatalog.CanonicalProviderID(strings.TrimSpace(e.Request.URL.Query().Get("provider_id")))
	if err != nil {
		return httpx.BadRequest(e, "provider_id must be exactly centron or ionos", nil)
	}

	effective := managedRuntimeEffectiveIntent{
		Version:               managedRuntimeEffectiveIntentVersion,
		TenantID:              tenantID,
		OwnerSubjectID:        ownerID,
		StackID:               managedRuntimeAuthorityPreflightStackID,
		StackName:             managedRuntimeAuthorityPreflightStackName,
		RuntimeSlotKey:        managedRuntimeAuthorityPreflightSlotKey,
		RuntimeSlotGeneration: 1,
		ProviderID:            providerID,
		NodeRole:              "worker",
		RuntimeOfferingID:     string(monthlyruntime.DefaultOfferingID()),
		StackKit:              registryCloudKit,
		Services:              []string{},
	}
	if handled, err := h.preflightManagedRuntimeServer(
		e,
		effective,
		managedRuntimeAuthorityPreflightOperationKey,
		"",
	); handled || err != nil {
		return err
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"provider_id":         providerID,
		"budget_key":          monthlyruntime.CapacityBudgetCloudRuntimeCredits,
		"decision_source":     monthlyruntime.CapacityDecisionSourceSignedRuntimeBudget,
		"request_bound":       true,
		"admission_preflight": true,
	})
}

func (h stackLifecycleRouteHandlers) addManagedRuntimeServer(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "Stack ID is required", nil)
	}
	stack, err := h.findManagedRuntimeStack(e, ownerID, stackID)
	if err != nil {
		return err
	}
	idempotencyKey, err := readManagedRuntimeIdempotencyKey(e.Request)
	if err != nil {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Exactly one valid Idempotency-Key header is required", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "idempotency_key_invalid",
			})
	}
	req, err := readManagedRuntimeServerRequest(e)
	if err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}
	result, err := h.executeManagedRuntimeExpansion(e, ownerID, stack, idempotencyKey, req, managedRuntimeExpansionConstraints{})
	if result == nil {
		return err
	}
	return httpx.Success(e, http.StatusAccepted, result)
}

// Execute invokes the same normalized expansion authority as the public POST
// route. It derives owner and tenant scope from the authenticated event and
// returns the existing receipt-validated result without writing a second
// success envelope; failures are written through the shared route helpers.
func (service *ManagedRuntimeExpansion) Execute(e *httpx.Event, request ManagedRuntimeExpansionRequest) (*ManagedRuntimeExpansionResult, error) {
	if service == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime expansion is not configured", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "native_admission_unavailable",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	ownerID, err := requireAuth(e)
	if err != nil {
		return nil, err
	}
	stackID := strings.TrimSpace(request.StackID)
	if stackID == "" {
		return nil, httpx.BadRequest(e, "Stack ID is required", nil)
	}
	stack, err := service.h.findManagedRuntimeStack(e, ownerID, stackID)
	if err != nil {
		return nil, err
	}
	idempotencyKey, err := validateManagedRuntimeIdempotencyKey(request.IdempotencyKey)
	if err != nil {
		return nil, httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeValidation,
			"Exactly one valid Idempotency-Key is required", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "idempotency_key_invalid",
			})
	}
	normalized, err := normalizeManagedRuntimeServerRequest(managedRuntimeServerRequest{
		RuntimeSlotKey: request.RuntimeSlotKey, NodeRole: request.NodeRole,
		RuntimeOfferingID: request.RuntimeOfferingID, ProviderID: request.ProviderID,
		ProviderRegion: request.ProviderRegion, IONOSDatacenter: request.IONOSDatacenter,
		StackKit: request.StackKit, Services: append([]string(nil), request.Services...),
	})
	if err != nil {
		return nil, httpx.BadRequest(e, err.Error(), nil)
	}
	return service.h.executeManagedRuntimeExpansion(e, ownerID, stack, idempotencyKey, normalized, managedRuntimeExpansionConstraints{
		expectedGeneration:   request.RecreateExpectedGeneration,
		requiresReleasedSlot: request.RecreateRequiresReleasedSlot,
	})
}

func (h stackLifecycleRouteHandlers) executeManagedRuntimeExpansion(
	e *httpx.Event,
	ownerID string,
	stack managedRuntimeStackRef,
	idempotencyKey string,
	req managedRuntimeServerRequest,
	constraints managedRuntimeExpansionConstraints,
) (*ManagedRuntimeExpansionResult, error) {
	tenantID := stack.TenantID
	if h.managedLeases == nil {
		if stackUsesManagedRuntime(stack) {
			if decision := h.evaluateManagedRuntimeServerEntitlement(e.Request.Context(), ownerID, req); decision.Denied {
				return nil, httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, decision.Message, decision.Details())
			}
		}
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Native managed runtime admission is not configured", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "native_admission_unavailable",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	effective := newManagedRuntimeEffectiveIntent(tenantID, ownerID, stack, req)
	resolver, ok := h.managedLeases.(jobruntime.ManagedRuntimeSlotGenerationResolver)
	if !ok || resolver == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime slot generation could not be resolved", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "runtime_slot_generation_resolution_unavailable",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	resolvedSlot, err := resolver.ResolveManagedRuntimeSlotGeneration(
		e.Request.Context(),
		jobruntime.ManagedRuntimeSlotGenerationRequest{
			TenantID: tenantID, StackID: stack.ID, RuntimeSlotKey: effective.RuntimeSlotKey,
		},
	)
	if err != nil || resolvedSlot.GenerationOrdinal == 0 ||
		resolvedSlot.RuntimeSlotID != providercontrol.DeriveManagedRuntimeSlotID(tenantID, stack.ID, effective.RuntimeSlotKey) {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime slot generation could not be resolved", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "runtime_slot_generation_resolution_failed",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	if constraints.expectedGeneration != 0 && resolvedSlot.GenerationOrdinal != constraints.expectedGeneration {
		return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Managed runtime slot generation changed; refresh before recreating", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "runtime_slot_generation_changed",
				managedRuntimeDetailsRetryableKey:  false,
			})
	}
	effective.RuntimeSlotGeneration = resolvedSlot.GenerationOrdinal
	idempotency, err := newManagedRuntimeIdempotency(
		tenantID, stack.ID, idempotencyKey, req, resolvedSlot.GenerationOrdinal,
	)
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime request identity could not be prepared", nil)
	}
	if h.jobs == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime execution coordination is not configured", nil)
	}
	existing, getErr := h.jobs.GetJob(e.Request.Context(), tenantID, idempotency.JobID)
	if getErr == nil {
		effective, replayErr := managedRuntimeReplayIntent(existing, idempotency, ownerID)
		if replayErr != nil {
			return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, idempotency.JobID, replayErr)
		}
		return h.resumeManagedRuntimeServerJob(e, stack, existing, idempotency, effective, true)
	}
	if !errors.Is(getErr, controlplane.ErrNotFound) {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime replay state is temporarily unavailable", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "idempotency_lookup_unavailable",
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	if constraints.requiresReleasedSlot && resolvedSlot.ExistingUnreleased {
		return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The current managed runtime generation still holds this slot", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "runtime_slot_generation_unreleased",
				managedRuntimeDetailsRetryableKey:  false,
			})
	}
	if !stackUsesManagedRuntime(stack) {
		return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This stack is not configured for kombify Cloud managed runtimes", map[string]any{
			"stack_id": stack.ID,
		})
	}
	if decision := h.evaluateManagedRuntimeServerEntitlement(e.Request.Context(), ownerID, req); decision.Denied {
		return nil, httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, decision.Message, decision.Details())
	}
	offeringID := serverruntime.RuntimeOfferingID(effective.RuntimeOfferingID)
	if _, ok := monthlyruntime.OfferingByID(offeringID); !ok {
		return nil, httpx.BadRequest(e, "A valid managed runtime offering is required", nil)
	}
	if handled, err := h.preflightManagedRuntimeServer(e, effective, idempotency.JobID, ""); handled || err != nil {
		return nil, err
	}
	jobResult, err := managedRuntimeInitialJobResult(idempotency, effective)
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime request snapshot could not be prepared", nil)
	}
	now := time.Now().UTC()
	created, createErr := h.jobs.CreateJob(e.Request.Context(), controlplane.UpsertJobRequest{
		ID: idempotency.JobID, TenantID: tenantID, StackID: stack.ID,
		Type: managedRuntimeJobTypeUpdate, State: monthlyRuntimeEnrollmentStatusPending,
		Progress: 25, Step: "create_lease", Message: "Requesting managed runtime server",
		Result: jobResult, ScheduledFor: now.Add(managedRuntimeJobClaimDelay),
	})
	if createErr != nil {
		if !errors.Is(createErr, controlplane.ErrConflict) {
			return nil, writeManagedRuntimeServerClaimError(e, stack.ID, idempotency.JobID, createErr)
		}
		created, getErr = h.jobs.GetJob(e.Request.Context(), tenantID, idempotency.JobID)
		if getErr != nil {
			return nil, writeManagedRuntimeServerClaimError(e, stack.ID, idempotency.JobID, getErr)
		}
		effective, err = managedRuntimeReplayIntent(created, idempotency, ownerID)
		if err != nil {
			return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, idempotency.JobID, err)
		}
		return h.resumeManagedRuntimeServerJob(e, stack, created, idempotency, effective, true)
	}
	return h.resumeManagedRuntimeServerJob(e, stack, created, idempotency, effective, false)
}

type managedRuntimeStackRef struct {
	ID             string
	Name           string
	OwnerSubjectID string
	TenantID       string
	Status         string
	Config         map[string]any
	RuntimeSummary map[string]any
	DriftStatus    string
	DriftCheckedAt *time.Time
}

func (s managedRuntimeStackRef) field(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Config is desired authority for a new lifecycle mutation. RuntimeSummary
	// is an observed projection of an earlier attempt and may legitimately lag
	// after the user changes an offering, region, or StackKit.
	return firstNonEmptyString(
		stringFromAnyMap(s.Config, name),
		stringFromAnyMap(s.RuntimeSummary, name),
	)
}

func managedRuntimeStackRefFromStore(stack controlplane.Stack) managedRuntimeStackRef {
	return managedRuntimeStackRef{
		ID:             stack.ID,
		Name:           firstNonEmptyString(stack.Name, stack.ID),
		OwnerSubjectID: stack.OwnerSubjectID,
		TenantID:       stack.TenantID,
		Status:         stack.Status,
		Config:         cloneLifecycleMap(stack.Config),
		RuntimeSummary: cloneLifecycleMap(stack.RuntimeSummary),
		DriftStatus:    stack.DriftStatus,
		DriftCheckedAt: stack.DriftCheckedAt,
	}
}

func (h stackLifecycleRouteHandlers) findManagedRuntimeStack(e *httpx.Event, ownerID, stackID string) (managedRuntimeStackRef, error) {
	if h.stacks == nil {
		return managedRuntimeStackRef{}, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Stack authority is temporarily unavailable", nil)
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.managed-runtime.create")
	if tenantErr != nil {
		return managedRuntimeStackRef{}, tenantErr
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, stackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return managedRuntimeStackRef{}, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return managedRuntimeStackRef{}, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return managedRuntimeStackRef{}, httpx.Forbidden(e, "Not your stack")
	}
	return managedRuntimeStackRefFromStore(*stack), nil
}

func writeManagedRuntimeServerClaimError(e *httpx.Event, stackID, jobID string, err error) error {
	details := map[string]any{
		"stack_id":                        stackID,
		managedRuntimeDetailsRetryableKey: true,
	}
	if strings.TrimSpace(jobID) != "" {
		details[managedRuntimeDetailsJobIDKey] = jobID
	}
	switch {
	case errors.Is(err, controlplane.ErrStackExecutionBusy):
		details[managedRuntimeDetailsReasonCodeKey] = "stack_execution_busy"
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Another operation is already running for this stack", details)
	case errors.Is(err, controlplane.ErrConflict):
		details[managedRuntimeDetailsReasonCodeKey] = "stack_execution_claim_conflict"
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The managed runtime operation could not claim this stack", details)
	default:
		details[managedRuntimeDetailsReasonCodeKey] = "stack_execution_claim_unavailable"
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime execution coordination is temporarily unavailable", details)
	}
}

func cloneLifecycleMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func readManagedRuntimeServerRequest(e *httpx.Event) (managedRuntimeServerRequest, error) {
	var req managedRuntimeServerRequest
	if e.Request.Body == nil {
		return req, nil
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return req, err
	}
	return normalizeManagedRuntimeServerRequest(req)
}

func normalizeManagedRuntimeServerRequest(req managedRuntimeServerRequest) (managedRuntimeServerRequest, error) {
	if err := providercatalog.ValidateNoLegacyProviderFields(req.LeaseProvider, req.SimulateProviderID); err != nil {
		return req, err
	}
	providerID, err := providercatalog.CanonicalProviderID(req.ProviderID)
	if err != nil {
		return req, err
	}
	req.ProviderID = providerID
	req.NodeRole, err = normalizeManagedRuntimeRequestNodeRole(req.NodeRole)
	if err != nil {
		return req, err
	}
	req.RuntimeSlotKey = strings.ToLower(strings.TrimSpace(req.RuntimeSlotKey))
	if req.RuntimeSlotKey == "" {
		req.RuntimeSlotKey = jobruntime.PrimaryManagedRuntimeSlotKey
	}
	if !managedRuntimeSlotKeyPattern.MatchString(req.RuntimeSlotKey) {
		return req, fmt.Errorf("runtime_slot_key must be a lowercase 1-64 character slot key")
	}
	req.RuntimeOfferingID = strings.TrimSpace(req.RuntimeOfferingID)
	req.ProviderRegion = strings.TrimSpace(req.ProviderRegion)
	req.IONOSDatacenter = strings.TrimSpace(req.IONOSDatacenter)
	if req.ProviderID == providercatalog.ProviderIONOS {
		req.IONOSDatacenter = monthlyruntime.NormalizeIONOSDatacenter(firstNonEmptyString(req.IONOSDatacenter, req.ProviderRegion))
		req.ProviderRegion = req.IONOSDatacenter
	}
	req.StackKit = strings.TrimSpace(req.StackKit)
	req.Services, err = normalizeManagedRuntimeRequestServices(req.Services)
	if err != nil {
		return req, err
	}
	return req, nil
}

func normalizeManagedRuntimeRequestNodeRole(value string) (string, error) {
	if value == "" {
		return "worker", nil
	}
	switch value {
	case "foundation", "worker", "storage":
		return value, nil
	default:
		return "", fmt.Errorf("node_role must be one of foundation, worker, or storage")
	}
}

func normalizeManagedRuntimeRequestServices(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, fmt.Errorf("services must contain at most 128 entries")
	}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value ||
			!managedRuntimeServiceKeyPattern.MatchString(value) || managedRuntimeContainsControl(value) {
			return nil, fmt.Errorf("each service must be a 1-128 character service key")
		}
	}
	return normalizedServiceKeys(values), nil
}

func managedRuntimeContainsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func managedRuntimeProviderRegion(provider string, req managedRuntimeServerRequest, stack managedRuntimeStackRef) string {
	if provider != providercatalog.ProviderIONOS {
		return ""
	}
	return monthlyruntime.NormalizeIONOSDatacenter(firstNonEmptyString(
		req.IONOSDatacenter,
		req.ProviderRegion,
		stack.field("ionos_datacenter"),
		stack.field("provider_region"),
	))
}

func normalizeLifecycleStackKit(value, defaultKit string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return defaultKit
	case "basement", "basementkit":
		return registryBaseKit
	case "cloud", "cloudkit":
		return registryCloudKit
	default:
		return strings.TrimSpace(value)
	}
}

func (h stackLifecycleRouteHandlers) evaluateManagedRuntimeServerEntitlement(ctx context.Context, ownerID string, req managedRuntimeServerRequest) monthlyruntime.ManagedRuntimeEntitlementDecision {
	return monthlyruntime.EvaluateManagedRuntimeEntitlement(ctx, h.features, ownerID, req.ProviderID)
}

func stackUsesManagedRuntime(stack managedRuntimeStackRef) bool {
	if stack.ID == "" {
		return false
	}
	for _, value := range []string{
		stack.field("server_provisioning_mode"),
		stack.field("server_mode"),
		stack.field("runtime_lane"),
		stack.field("lease_id"),
	} {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "kombify-cloud", serverruntime.RuntimeLaneMonthly, monthlyruntime.ServerModeManagedCloud:
			return true
		}
		if strings.HasPrefix(normalized, "lease-") {
			return true
		}
	}
	return false
}

func managedRuntimeNodeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "foundation", "main":
		return "foundation"
	case "storage":
		return "storage"
	default:
		return "worker"
	}
}

func normalizedServiceKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		key := normalizeServiceKey(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}
