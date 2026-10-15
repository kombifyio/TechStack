package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/appcatalog"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

type ServiceRuntimeRouteConfig struct {
	Metrics              monitoring.MetricsQueryBackend
	Policy               InventoryPolicy
	Store                controlplane.ServiceRuntimeStore
	Stacks               controlplane.StackStore
	Servers              controlplane.ServerRuntimeStore
	Jobs                 controlplane.JobStore
	Now                  func() time.Time
	Orchestrator         serviceActionOrchestrator
	ApplicationAdmission func() (*agentpb.StackKitReleasePin, error)
	// UpdateChecker answers registry update questions for observed container
	// images; nil uses the public-registry checker.
	UpdateChecker *appcatalog.UpdateChecker
	// StackKitReleaseVersion reports the StackKits release this control plane
	// pins, the target of the `update` action; nil resolves the configured pin.
	StackKitReleaseVersion func() string
}

type serviceActionOrchestrator interface {
	EnqueueStackKitLifecycle(context.Context, jobs.StackKitLifecycleRequest) (string, error)
	// StackKitLifecycleJobQueued reports whether this process still holds the
	// job in its queue; a durable pending job it does not hold was lost to a
	// restart and must be enqueued again.
	StackKitLifecycleJobQueued(jobID string) bool
}

type serviceRuntimeHandlers struct {
	metrics              monitoring.MetricsQueryBackend
	policy               InventoryPolicy
	store                controlplane.ServiceRuntimeStore
	stacks               controlplane.StackStore
	servers              controlplane.ServerRuntimeStore
	jobs                 controlplane.JobStore
	now                  func() time.Time
	orch                 serviceActionOrchestrator
	mu                   sync.Mutex
	connectorAdmission   func() (*agentpb.StackKitReleasePin, error)
	applicationAdmission func() (*agentpb.StackKitReleasePin, error)
	// updates and stackKitRelease are optional; without them a service reports
	// no registry or release comparison instead of an invented one.
	updates         *appcatalog.UpdateChecker
	stackKitRelease func() string
}

type serviceActionRequest struct {
	Action                    string `json:"action"`
	ExpectedInventoryRevision int64  `json:"expected_inventory_revision"`
	OwnerApproved             bool   `json:"owner_approved"`
	Limit                     int32  `json:"limit,omitempty"`
	Cursor                    string `json:"cursor,omitempty"`
}

type serviceActionResponse struct {
	JobID     string `json:"job_id"`
	ServiceID string `json:"service_id"`
	Action    string `json:"action"`
	Status    string `json:"status"`
}

type serviceLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type serviceLogsResponse struct {
	ServiceID  string            `json:"service_id"`
	JobID      string            `json:"job_id,omitempty"`
	Status     string            `json:"status"`
	Entries    []serviceLogEntry `json:"entries"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type serviceRuntimeResponse struct {
	Adoption               serviceAdoptionAvailability `json:"adoption"`
	ID                     string                      `json:"id"`
	KitDeploymentID        string                      `json:"kit_deployment_id"`
	ServerID               string                      `json:"server_id,omitempty"`
	TargetKind             string                      `json:"target_kind"`
	Placement              serviceRuntimePlacement     `json:"placement"`
	ServiceKey             string                      `json:"service_key"`
	ServiceInstance        string                      `json:"service_instance"`
	Name                   string                      `json:"name"`
	ApplicationKey         string                      `json:"application_key,omitempty"`
	ApplicationDisplayName string                      `json:"application_display_name,omitempty"`
	Role                   string                      `json:"role,omitempty"`
	Lifecycle              string                      `json:"lifecycle,omitempty"`
	OperationalImpact      string                      `json:"operational_impact,omitempty"`
	InternalAddress        string                      `json:"internal_address,omitempty"`
	RuntimeIdentity        map[string]string           `json:"runtime_identity,omitempty"`
	// ManagementState is the persisted ownership dimension. `desired_state` is
	// only a declared contract when this is `managed`; for an `observed` service
	// there is no declared target and drift comparison is undefined.
	ManagementState string `json:"management_state"`
	// MutationLock is the owner guardrail. It is orthogonal to the measured
	// dimensions: a locked service keeps running and keeps reporting its real
	// observed state, and only `allowed_actions` narrows.
	MutationLock    serviceRuntimeMutationLock `json:"mutation_lock"`
	DesiredState    string                     `json:"desired_state"`
	ObservedState   string                     `json:"observed_state"`
	Health          serviceRuntimeHealth       `json:"health"`
	StackKitVersion string                     `json:"stackkit_version,omitempty"`
	Access          map[string]any             `json:"access"`
	AllowedActions  []string                   `json:"allowed_actions"`
	// Catalog is set when the service is a recognized well-known application.
	Catalog *appcatalog.Entry `json:"catalog,omitempty"`
	// Version is omitted when nothing about the running version was measured.
	Version *serviceVersion `json:"version,omitempty"`
	// PlacementClass is the evidence-backed local, cloud, managed or unknown
	// class; unknown means no placement evidence is recorded.
	PlacementClass    string                 `json:"placement_class"`
	InventoryRevision int64                  `json:"inventory_revision"`
	EvidenceRef       string                 `json:"evidence_ref,omitempty"`
	Source            string                 `json:"source"`
	Provenance        map[string]interface{} `json:"provenance"`
	ActionGuard       map[string]any         `json:"action_guard,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

func serviceMutationLockResponse(lock controlplane.ServiceMutationLock) serviceRuntimeMutationLock {
	state := serviceregistry.CanonicalMutationLockState(string(lock.State))
	if state != serviceregistry.MutationLocked {
		return serviceRuntimeMutationLock{State: string(state)}
	}
	return serviceRuntimeMutationLock{
		State: string(state), ReasonCode: lock.ReasonCode, Actor: lock.Actor, ChangedAt: lock.ChangedAt,
	}
}

// serviceRuntimeMutationLock is the read projection of the owner guardrail.
// `reason_code` and `actor` are only populated while locked.
type serviceRuntimeMutationLock struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Actor      string     `json:"actor,omitempty"`
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
}

type serviceApplicationAccess struct {
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	OpenURL string `json:"open_url,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type serviceApplicationResponse struct {
	ID              string                   `json:"application_id"`
	KitDeploymentID string                   `json:"kit_deployment_id"`
	ServerID        string                   `json:"server_id"`
	ApplicationKey  string                   `json:"application_key"`
	DisplayName     string                   `json:"display_name"`
	Status          string                   `json:"status"`
	Access          serviceApplicationAccess `json:"access"`
	Components      []serviceRuntimeResponse `json:"components"`
	ObservedAt      *time.Time               `json:"observed_at,omitempty"`
	System          bool                     `json:"system"`
	Provenance      map[string]any           `json:"provenance"`
	// AllowedActions is the union of the actions its components offer.
	AllowedActions []string `json:"allowed_actions"`
	// Catalog, Version and PlacementClass describe the primary component: the
	// recognized application component, else the first running one.
	Catalog        *appcatalog.Entry `json:"catalog,omitempty"`
	Version        *serviceVersion   `json:"version,omitempty"`
	PlacementClass string            `json:"placement_class"`
}

type serviceRuntimeHealth struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	ReasonCode string     `json:"reason_code,omitempty"`
}

// serviceRuntimePlacement is evidence-backed placement metadata. The
// managed_workload shape is intentionally data-only until a dedicated provider
// slice proves its SLA, backup, runtime and cleanup semantics.
type serviceRuntimePlacement struct {
	ProviderID         string                       `json:"provider_id,omitempty"`
	ManagedTargetRef   string                       `json:"managed_target_ref,omitempty"`
	ProviderReceiptRef string                       `json:"provider_receipt_ref,omitempty"`
	SLAPolicyRef       string                       `json:"sla_policy_ref,omitempty"`
	BackupPolicyRef    string                       `json:"backup_policy_ref,omitempty"`
	EvidenceRef        string                       `json:"evidence_ref,omitempty"`
	ObservedAt         *time.Time                   `json:"observed_at,omitempty"`
	Freshness          serverRuntimeTargetFreshness `json:"freshness"`
}

func RegisterServiceRuntimeRoutes(r *httpx.Router, cfg ServiceRuntimeRouteConfig) {
	if cfg.Store == nil || cfg.Stacks == nil || cfg.Servers == nil {
		panic("RegisterServiceRuntimeRoutes: service, stack and server stores required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	h := serviceRuntimeHandlers{store: cfg.Store, stacks: cfg.Stacks, servers: cfg.Servers, jobs: cfg.Jobs, now: cfg.Now, orch: cfg.Orchestrator}
	h.policy = cfg.Policy
	h.metrics = cfg.Metrics
	if configurable, ok := cfg.Orchestrator.(interface {
		ConfigureApplicationOperationAuthorization(func(context.Context, string, string, string) error)
	}); ok {
		configurable.ConfigureApplicationOperationAuthorization(func(ctx context.Context, tenantID, ownerID, serverID string) error {
			return authorizeInventoryServerOperate(ctx, cfg.Policy, inventoryScope{tenantID: tenantID, ownerID: ownerID}, serverID)
		})
	}
	h.applicationAdmission = cfg.ApplicationAdmission
	if h.applicationAdmission == nil {
		h.applicationAdmission = jobs.ApplicationAdoptionRelease
	}
	h.updates = cfg.UpdateChecker
	if h.updates == nil {
		h.updates = appcatalog.NewUpdateChecker()
	}
	h.stackKitRelease = cfg.StackKitReleaseVersion
	if h.stackKitRelease == nil {
		h.stackKitRelease = pinnedStackKitRelease()
	}
	r.GET("/api/v1/services", h.list)
	r.GET("/api/v1/services/{serviceId}", h.get)
	r.GET("/api/v1/services/{serviceId}/metrics", h.serviceMetrics)
	r.GET("/api/v1/service-applications", h.listApplications)
	r.GET("/api/v1/service-applications/{applicationId}", h.getApplication)
	r.POST("/api/v1/registry/services/{serviceId}/actions", h.action)
	r.GET("/api/v1/registry/services/{serviceId}/logs", h.logs)
	r.GET("/api/v1/services/{serviceId}/connector", h.readConnector)
	r.POST("/api/v1/services/{serviceId}/connector", h.setupConnector)
	r.POST("/api/v1/services/{serviceId}/connector/verify", h.verifyConnector)
	r.POST("/api/v1/services/{serviceId}/adoption-plans", h.prepareAdoption)
	r.GET("/api/v1/services/{serviceId}/adoption-plans/{planId}", h.readAdoption)
	r.POST("/api/v1/services/{serviceId}/adoption-plans/{planId}/execute", h.executeAdoption)
	r.POST("/api/v1/services/{serviceId}/release", h.releaseAdoption)
}

func (h *serviceRuntimeHandlers) listApplications(e *httpx.Event) error {
	applications, err := h.applicationReadModel(e)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, applications)
}

func (h *serviceRuntimeHandlers) getApplication(e *httpx.Event) error {
	want := strings.TrimSpace(e.Request.PathValue("applicationId"))
	if want == "" {
		return httpx.BadRequest(e, "Application ID is required", nil)
	}
	applications, err := h.applicationReadModel(e)
	if err != nil {
		return err
	}
	for _, application := range applications {
		if application.ID == want {
			return httpx.Success(e, http.StatusOK, application)
		}
	}
	return httpx.NotFound(e, "Application not found")
}

func (h *serviceRuntimeHandlers) applicationReadModel(e *httpx.Event) ([]serviceApplicationResponse, error) {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return nil, httpx.RejectUnauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.service-applications.read")
	if tenantErr != nil {
		return nil, tenantErr
	}
	stackID := strings.TrimSpace(e.Request.URL.Query().Get("kit_deployment_id"))
	serverID := strings.TrimSpace(e.Request.URL.Query().Get("server_id"))
	ownedStacks, err := h.ownedStackIDs(e, tenantID, ownerID, isAdmin)
	if err != nil {
		return nil, err
	}
	if stackID != "" && !ownedStacks[stackID] {
		return nil, httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Stack not found", nil)
	}
	rows, err := h.store.ListServiceRuntimes(e.Request.Context(), tenantID, stackID, "")
	if err != nil {
		return nil, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	servers, err := h.serverMap(e, tenantID, stackID)
	if err != nil {
		return nil, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	rows = projectDeclaredServiceControls(rows, servers)
	grouped := make(map[string]*serviceApplicationResponse)
	response := h.responseForRequest()
	for _, row := range rows {
		if !ownedStacks[row.StackID] || stringFromAnyMap(row.Metadata, serviceSupersededBy) != "" {
			continue
		}
		if server := servers[row.ServerID]; server != nil {
			row.ServerID = server.ID
		}
		if serverID != "" && row.ServerID != serverID {
			continue
		}
		key := applicationKeyForService(row)
		id := runtimeidentity.ApplicationID(row.StackID, row.ServerID, key)
		if id == "" {
			continue
		}
		application := grouped[id]
		if application == nil {
			application = &serviceApplicationResponse{
				ID: id, KitDeploymentID: row.StackID, ServerID: row.ServerID, ApplicationKey: key,
				DisplayName: applicationDisplayName(row, key), Status: monitoringStatusUnknown,
				Access: serviceApplicationAccess{Kind: serviceAccessUnavailable, Reason: "no_reachable_address_reported"},
				System: key == "system", Provenance: map[string]any{"definition_authority": serviceStackKits, "runtime_authority": serviceRuntimeAuthority},
			}
			grouped[id] = application
		}
		component := response(row, servers[row.ServerID])
		application.Components = append(application.Components, component)
		application.Status = aggregateApplicationStatus(application.Status, component.Health.State, component.ObservedState)
		if row.ObservedAt != nil && (application.ObservedAt == nil || row.ObservedAt.After(*application.ObservedAt)) {
			observed := row.ObservedAt.UTC()
			application.ObservedAt = &observed
		}
		application.Access = preferApplicationAccess(application.Access, component.Access, component.InternalAddress)
	}
	result := make([]serviceApplicationResponse, 0, len(grouped))
	for _, application := range grouped {
		sort.Slice(application.Components, func(i, j int) bool { return application.Components[i].Name < application.Components[j].Name })
		primary := primaryApplicationComponent(application.Components)
		application.AllowedActions = applicationActionUnion(application.Components)
		application.Catalog, application.Version, application.PlacementClass = primary.Catalog, primary.Version, primary.PlacementClass
		result = append(result, *application)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].System != result[j].System {
			return !result[i].System
		}
		return result[i].DisplayName < result[j].DisplayName
	})
	return result, nil
}

// primaryApplicationComponent picks the component that speaks for its
// application: a recognized, non-infrastructure, non-database component first,
// then any recognized one, then the first running one, then the first.
func primaryApplicationComponent(components []serviceRuntimeResponse) serviceRuntimeResponse {
	if len(components) == 0 {
		return serviceRuntimeResponse{PlacementClass: placementClassUnknown}
	}
	best, bestRank := components[0], -1
	for _, component := range components {
		rank := 0
		switch {
		case component.Catalog != nil && !component.Catalog.Infra && component.Catalog.Category != "database":
			rank = 3
		case component.Catalog != nil:
			rank = 2
		case component.ObservedState == string(serviceregistry.ObservedRunning):
			rank = 1
		}
		if rank > bestRank {
			best, bestRank = component, rank
		}
	}
	return best
}

func aggregateApplicationStatus(current, health, observed string) string {
	for _, state := range []string{health, observed} {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "unhealthy", "failed", "error":
			return runtimeHealthDegraded
		case "healthy", "running":
			if current == monitoringStatusUnknown {
				current = "healthy"
			}
		}
	}
	return current
}

func preferApplicationAccess(current serviceApplicationAccess, public map[string]any, internal string) serviceApplicationAccess {
	mode := stringFromAnyMap(public, serviceAccessModeKey)
	address := stringFromAnyMap(public, serviceAccessURLKey)
	if mode == serviceAccessDirect {
		address = safeObservedDirectServiceURL(address)
	} else if mode == serviceAccessRelay {
		address = safeKombifyRelayURL(address)
	} else {
		address = ""
	}
	if address != "" {
		return serviceApplicationAccess{Kind: "public", Address: address, OpenURL: address}
	}
	if current.OpenURL != "" {
		return current
	}
	if internal = boundedInternalServiceAddress(internal); internal != "" {
		return serviceApplicationAccess{Kind: "internal", Address: internal, Reason: "internal_address_only"}
	}
	return current
}

func (h *serviceRuntimeHandlers) action(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.services.actions")
	if tenantErr != nil {
		return tenantErr
	}
	if h.orch == nil || h.jobs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Managed service control is unavailable", nil)
	}
	serviceID, idempotencyKey, request, lockAction, requestErr := decodeServiceActionRequest(e)
	if requestErr != nil {
		return requestErr
	}
	service, err := h.loadServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load service", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to authorize service", nil)
	}
	if lockAction {
		return h.applyMutationLock(e, tenantID, ownerID, service, request.Action)
	}
	digestBytes := sha256.Sum256([]byte(strings.Join([]string{
		tenantID, serviceID, request.Action, strconv.FormatInt(request.ExpectedInventoryRevision, 10),
		strconv.FormatBool(request.OwnerApproved), strconv.FormatInt(int64(request.Limit), 10), request.Cursor,
	}, "\x00")))
	digest := hex.EncodeToString(digestBytes[:])
	operation := map[string]string{"start": jobs.StackKitLifecycleServiceStart, "stop": jobs.StackKitLifecycleServiceStop, serviceActionRestart: jobs.StackKitLifecycleServiceRestart, "logs": jobs.StackKitLifecycleServiceLogs, serviceActionUpdate: jobs.StackKitLifecycleUpgrade, serviceActionBackup: jobs.StackKitLifecycleBackupRun}[request.Action]
	lifecycleRequest := jobs.StackKitLifecycleRequest{
		StackID: stack.ID, StackKitInstanceID: stack.StackKitInstanceID, TenantID: tenantID, OwnerID: ownerID,
		Operation: operation, OwnerApproved: request.Action != "logs", ServiceKey: serviceActionControlKey(service),
		LogTail: request.Limit, LogCursor: request.Cursor, ServiceID: service.ID,
		DurableJobID: jobs.StackKitServiceActionJobID(tenantID, ownerID, idempotencyKey), ServiceActionDigest: digest,
	}
	// Replay before validating the target: a lost-response retry must return
	if adoptedApplication(service) && request.Action != "logs" && request.Action != serviceActionUpdate && request.Action != serviceActionBackup {
		if err := authorizeInventoryServerOperate(e.Request.Context(), h.policy, inventoryScope{tenantID: tenantID, ownerID: ownerID}, service.ServerID); err != nil {
			return writeInventoryHTTPError(e, err)
		}
		lifecycleRequest.Operation = jobs.StackKitLifecycleApplicationControl
		lifecycleRequest.ServiceKey = "smart-home"
	}
	// the stored receipt even though the first attempt already moved the
	// inventory revision, locked the service or lost its agent connection.
	if replayed, replayErr := h.replayServiceAction(e, tenantID, service, request.Action, lifecycleRequest); replayed || replayErr != nil {
		return replayErr
	}
	// Fail-closed: an owner guardrail refuses every agent-executed mutation.
	// Reads stay open, so `logs` is still how an owner inspects a frozen service.
	if service.MutationLock.Locked() && serviceMutatingActions[request.Action] {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service is locked", map[string]any{
			"reason":                 "service_locked",
			"mutation_lock":          serviceMutationLockResponse(service.MutationLock),
			"unlock_required_action": serviceActionUnfreeze,
		})
	}
	if sources := serviceGroupLockSources(*service); len(sources) != 0 && serviceMutatingActions[request.Action] {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Application control is locked", map[string]any{
			"reason_code": "application_control_locked", "unlock_required_action": serviceActionUnfreeze, "unlock_service_ids": sources,
		})
	}
	target, targetErr := h.validateServiceActionTarget(e, tenantID, service, stack, request)
	if targetErr != nil {
		return targetErr
	}
	if maintenanceErr := refuseDuringServerMaintenance(e, h.servers, tenantID, controlplane.ServerMaintenanceScope{
		ServerID: target.server.ID, AgentID: target.server.WorkerID,
	}); maintenanceErr != nil {
		return maintenanceErr
	}
	lifecycleRequest.AgentID = target.server.WorkerID
	lifecycleRequest.StackKit = target.stackKit
	if request.Action == serviceActionUpdate {
		// An update is the stack upgrade to the pinned StackKits release. It
		// rewrites every service of the stack, so a locked sibling refuses it.
		if lockedErr := h.refuseLockedStackServices(e, tenantID, stack.ID); lockedErr != nil {
			return lockedErr
		}
		if h.stackKitRelease != nil {
			lifecycleRequest.TargetRelease = upgradeTargetRelease(h.stackKitRelease())
		}
	}
	if adoptedApplication(service) && request.Action != "logs" && request.Action != serviceActionUpdate && request.Action != serviceActionBackup {
		head, readErr := h.applicationHead(e.Request.Context(), tenantID, service.ID)
		if readErr != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Application identity authority is unavailable", nil)
		}
		pin, pinErr := h.applicationAdmission()
		if pinErr != nil {
			return adoptionUnavailable(e, pinErr)
		}
		lifecycleRequest.Operation = jobs.StackKitLifecycleApplicationControl
		lifecycleRequest.ServiceKey = "smart-home"
		lifecycleRequest.Application = retainedApplicationBinding(adoptionTarget{head: head})
		lifecycleRequest.Application.Action = request.Action
		lifecycleRequest.Application.ReleasePin = pin
	}
	return h.enqueueServiceAction(e, tenantID, stack.ID, service, request.Action, lifecycleRequest)
}

// replayServiceAction answers a retry from the stored job receipt. The job id
// is derived from tenant, principal and Idempotency-Key, and the stored
// receipt must match the request fingerprint, so a replay never crosses scopes
// and a different body under the same key stays a conflict. Only a durable
// pending job that this process no longer queues falls through, so the
// validated enqueue path can rebuild a job lost to a restart.
func (h *serviceRuntimeHandlers) replayServiceAction(
	e *httpx.Event,
	tenantID string,
	service *controlplane.ServiceRuntime,
	action string,
	lifecycleRequest jobs.StackKitLifecycleRequest,
) (bool, error) {
	existing, err := h.jobs.GetJob(e.Request.Context(), tenantID, lifecycleRequest.DurableJobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect service action replay", nil)
	}
	if !jobs.MatchesStackKitServiceActionReceipt(existing.Result, lifecycleRequest) {
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different request", nil)
	}
	if strings.EqualFold(existing.State, "pending") && !h.orch.StackKitLifecycleJobQueued(existing.ID) {
		return false, nil
	}
	return true, httpx.Success(e, http.StatusAccepted, serviceActionResponse{
		JobID: existing.ID, ServiceID: service.ID, Action: action, Status: serviceActionJobStatus(existing.State),
	})
}

type serviceActionTarget struct {
	server   *controlplane.ServerRuntime
	stackKit string
}

func decodeServiceActionRequest(e *httpx.Event) (string, string, serviceActionRequest, bool, error) {
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	idempotencyKey := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if serviceID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Service ID and a bounded Idempotency-Key are required", nil)
	}
	var request serviceActionRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid request body", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Request body must contain one JSON document", nil)
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if !serviceActionVocabulary[request.Action] {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Action must be start, stop, restart, logs, update, backup, freeze, or unfreeze", nil)
	}
	if request.Action != serviceActionLogs && !request.OwnerApproved {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Service mutations require explicit Owner approval", nil)
	}
	lockAction := request.Action == serviceActionFreeze || request.Action == serviceActionUnfreeze
	// The guardrail is a control-plane fact, not an observation of the runtime.
	// Binding it to an inventory revision would imply a runtime guarantee we do
	// not make - and would make a service unlockable exactly when its agent is
	// unreachable, which is when an owner most wants to freeze it.
	if lockAction && request.ExpectedInventoryRevision != 0 {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "expected_inventory_revision is not applicable to freeze and unfreeze", nil)
	}
	request.Cursor = strings.TrimSpace(request.Cursor)
	if request.Action == serviceActionLogs {
		if request.Limit == 0 {
			request.Limit = 100
		}
		if request.Limit < 1 || request.Limit > 200 || len(request.Cursor) > 2048 {
			return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Service logs require limit 1..200 and a bounded cursor", nil)
		}
	} else if request.Limit != 0 || request.Cursor != "" {
		return "", "", serviceActionRequest{}, false, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "limit and cursor are only valid for logs", nil)
	}
	return serviceID, idempotencyKey, request, lockAction, nil
}

func (h *serviceRuntimeHandlers) validateServiceActionTarget(
	e *httpx.Event,
	tenantID string,
	service *controlplane.ServiceRuntime,
	stack *controlplane.Stack,
	request serviceActionRequest,
) (serviceActionTarget, error) {
	if strings.TrimSpace(stack.StackKitInstanceID) == "" {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service StackKit instance identity is unavailable", nil)
	}
	placement := serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	if placement.TargetKind != serviceregistry.TargetKindServer {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service runtime target is not server-bound", map[string]any{"target_kind": placement.TargetKind})
	}
	server, err := h.servers.GetServerRuntime(e.Request.Context(), tenantID, service.ServerID)
	if err != nil || server == nil {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service server is unavailable", nil)
	}
	if request.ExpectedInventoryRevision <= 0 || request.ExpectedInventoryRevision != server.InventoryRevision || metadataInt64(service.Metadata, "inventory_revision") != server.InventoryRevision {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Inventory revision is stale", map[string]any{"current_inventory_revision": server.InventoryRevision})
	}
	if service.ObservedAt == nil || h.now().UTC().Sub(service.ObservedAt.UTC()) > runtimehealth.FreshHeartbeatWindow {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service observation is stale", nil)
	}
	if server.ConnectionState != string(serverregistry.ConnectionConnected) && server.ConnectionState != string(serverregistry.ConnectionDegraded) {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service agent is not connected", nil)
	}
	// Validated against the same projection the read model advertises, so the
	// advertised and the enforced capability set cannot drift apart.
	if request.Action == serviceActionUpdate {
		if !updateAllowed(*service, h.serviceVersionFor(*service), server) {
			return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service action is not allowed", map[string]any{"reason_code": "service_update_unavailable"})
		}
	} else if request.Action == serviceActionBackup {
		if !backupAllowed(*service, server) {
			return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service action is not allowed", map[string]any{"reason_code": "service_backup_unavailable"})
		}
	} else if !containsString(serviceRuntimeAllowedActions(service.Capabilities, service.MutationLock.Locked()), request.Action) {
		return serviceActionTarget{}, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service action is not allowed", nil)
	}
	stackKit := serviceActionKitName(service.StackKitVersion)
	if stackKit == "" {
		stackKit = firstNonEmptyString(stackString(stack, "stackkit_catalog_ref"), stackString(stack, "stackkit_foundation"), stringFromAnyMap(stack.Config, "stackkit"), stringFromAnyMap(stack.Config, "kit"))
	}
	return serviceActionTarget{server: server, stackKit: stackKit}, nil
}

func (h *serviceRuntimeHandlers) enqueueServiceAction(
	e *httpx.Event,
	tenantID, stackID string,
	service *controlplane.ServiceRuntime,
	action string,
	lifecycleRequest jobs.StackKitLifecycleRequest,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, getErr := h.jobs.GetJob(e.Request.Context(), tenantID, lifecycleRequest.DurableJobID); getErr == nil {
		if !jobs.MatchesStackKitServiceActionReceipt(existing.Result, lifecycleRequest) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different request", nil)
		}
		if !strings.EqualFold(existing.State, "pending") {
			return httpx.Success(e, http.StatusAccepted, serviceActionResponse{
				JobID: existing.ID, ServiceID: service.ID, Action: action, Status: serviceActionJobStatus(existing.State),
			})
		}
	} else if !errors.Is(getErr, controlplane.ErrNotFound) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect service action replay", nil)
	}
	if conflictID, conflictErr := h.activeServiceActionJob(e.Request.Context(), tenantID, stackID, service.ID, lifecycleRequest.DurableJobID); conflictErr != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect active service actions", nil)
	} else if conflictID != "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A service action is already active", map[string]any{"job_id": conflictID})
	}
	jobID, err := h.orch.EnqueueStackKitLifecycle(e.Request.Context(), lifecycleRequest)
	if err != nil {
		if errors.Is(err, controlplane.ErrServiceOperationBusy) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A service action is already active", nil)
		}
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different request", nil)
		}
		if errors.Is(err, orchestrator.ErrStackKitBackupCustodyManaged) {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Backups for this StackKit run through the managed backup schedule",
				map[string]any{
					"reason_code": "managed_backup_custody",
					"retryable":   false,
					"user_guidance": map[string]any{
						"title":      "Backups for this stack are managed",
						"body":       "This StackKit keeps its backups in kombify-managed storage, so they run on the stack's backup schedule under your plan's backup entitlement.",
						"next_steps": []string{"Use the stack's backup schedule to run managed backups."},
					},
				})
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to enqueue managed service action", map[string]any{"reason": err.Error()})
	}
	return httpx.Success(e, http.StatusAccepted, serviceActionResponse{
		JobID: jobID, ServiceID: service.ID, Action: action, Status: "queued",
	})
}

// applyMutationLock asserts or clears the owner guardrail. It is executed
// entirely in the control plane: no job is enqueued and no agent is contacted,
// because the lock does not change what runs on the node - it changes what this
// control plane is willing to do to it. That is also why it deliberately skips
// the connection, freshness and inventory-revision preconditions of the
// agent-executed actions: an owner must be able to freeze a service whose agent
// is unreachable.
//
// Idempotency needs no separate ledger here. The aggregate write boundary is
// change-only, so a repeated freeze writes nothing and reports the same state;
// the required Idempotency-Key is kept for contract symmetry with the
// agent-executed actions.
func (h *serviceRuntimeHandlers) applyMutationLock(
	e *httpx.Event,
	tenantID, ownerID string,
	service *controlplane.ServiceRuntime,
	action string,
) error {
	writer, ok := h.store.(controlplane.ServiceMutationLockStore)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service lock control is unavailable", nil)
	}
	state := serviceregistry.MutationLocked
	reasonCode := "owner_locked"
	actor := ownerID
	if action == serviceActionUnfreeze {
		state, reasonCode, actor = serviceregistry.MutationUnlocked, "owner_unlocked", ""
	}
	if _, err := writer.SetServiceMutationLock(e.Request.Context(), tenantID, service.ID,
		controlplane.ServiceMutationLock{State: state, ReasonCode: reasonCode, Actor: actor},
		reasonCode,
	); err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service lock was concurrently changed", nil)
		}
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "Service not found")
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to apply the service lock", nil)
	}
	return httpx.Success(e, http.StatusOK, serviceActionResponse{
		ServiceID: service.ID, Action: action, Status: "completed",
	})
}

func serviceActionJobStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "pending", "waiting":
		return "queued"
	case "running", "completed", "failed", "cancelled", "canceled":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "queued"
	}
}

func (h *serviceRuntimeHandlers) activeServiceActionJob(ctx context.Context, tenantID, stackID, serviceID, excludeJobID string) (string, error) {
	serviceJobs, ok := h.jobs.(controlplane.ServiceOperationJobStore)
	if !ok {
		return "", errors.New("complete service operation store is unavailable")
	}
	return serviceJobs.FindUnresolvedServiceJob(ctx, tenantID, stackID, serviceID, excludeJobID)
}

func (h *serviceRuntimeHandlers) logs(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.services.logs")
	if tenantErr != nil {
		return tenantErr
	}
	if h.jobs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service logs are unavailable", nil)
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	if serviceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	limit := 100
	if raw := strings.TrimSpace(e.Request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return httpx.BadRequest(e, "limit must be between 1 and 200", nil)
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(e.Request.URL.Query().Get("cursor"))
	if len(cursor) > 2048 {
		return httpx.BadRequest(e, "cursor is too long", nil)
	}
	service, err := h.loadServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load service", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to authorize service", nil)
	}
	rows, err := h.jobs.ListJobsByStack(e.Request.Context(), tenantID, service.StackID, 100)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to load service logs", nil)
	}
	for _, row := range rows {
		receipt, _ := row.Result["service_action_receipt"].(map[string]any)
		if stringFromAnyMap(receipt, "service_id") != serviceID || stringFromAnyMap(receipt, "action") != "logs" ||
			stringFromAnyMap(receipt, "log_cursor") != cursor {
			continue
		}
		if strings.EqualFold(row.State, "pending") || strings.EqualFold(row.State, "running") || strings.EqualFold(row.State, "waiting") {
			return httpx.Success(e, http.StatusAccepted, serviceLogsResponse{
				ServiceID: serviceID, JobID: row.ID, Status: serviceActionJobStatus(row.State), Entries: []serviceLogEntry{},
			})
		}
		if !strings.EqualFold(row.State, "completed") {
			continue
		}
		page, _ := row.Result["service_logs"].(map[string]any)
		entries := sanitizedServiceLogEntries(page["entries"], limit)
		return httpx.Success(e, http.StatusOK, serviceLogsResponse{
			ServiceID: serviceID, JobID: row.ID, Status: "completed", Entries: entries,
			NextCursor: firstNonEmptyString(stringFromAnyMap(page, "nextCursor"), stringFromAnyMap(page, "next_cursor")),
		})
	}
	return httpx.Success(e, http.StatusOK, serviceLogsResponse{ServiceID: serviceID, Status: "empty", Entries: []serviceLogEntry{}})
}

func sanitizedServiceLogEntries(value any, limit int) []serviceLogEntry {
	rows, _ := value.([]any)
	entries := make([]serviceLogEntry, 0, min(len(rows), limit))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		timestamp, err := time.Parse(time.RFC3339Nano, stringFromAnyMap(row, "timestamp"))
		if err != nil {
			continue
		}
		message := strings.TrimSpace(secrets.Redact(stringFromAnyMap(row, "message")))
		if len(message) > 16*1024 {
			message = message[:16*1024]
		}
		entries = append(entries, serviceLogEntry{Timestamp: timestamp.UTC(), Message: message})
		if len(entries) == limit {
			break
		}
	}
	return entries
}

func metadataInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	}
	return 0
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (h *serviceRuntimeHandlers) list(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.services.list")
	if tenantErr != nil {
		return tenantErr
	}
	stackID := strings.TrimSpace(e.Request.URL.Query().Get("kit_deployment_id"))
	serverID := strings.TrimSpace(e.Request.URL.Query().Get("server_id"))
	ownedStacks, err := h.ownedStackIDs(e, tenantID, ownerID, isAdmin)
	if err != nil {
		return err
	}
	if stackID != "" && !ownedStacks[stackID] {
		return httpx.NotFound(e, "Stack not found")
	}
	rows, err := h.store.ListServiceRuntimes(e.Request.Context(), tenantID, stackID, "")
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	servers, serverMapErr := h.serverMap(e, tenantID, stackID)
	if serverMapErr != nil {
		// The server aggregate is the placement authority. Returning an empty
		// map here would turn a tenant/FGA/store outage into a false offline
		// service fleet, so preserve it as a typed unavailable state instead.
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	rows = projectDeclaredServiceControls(rows, servers)
	items := make([]serviceRuntimeResponse, 0, len(rows))
	response := h.responseForRequest()
	for _, row := range rows {
		if !ownedStacks[row.StackID] || stringFromAnyMap(row.Metadata, serviceSupersededBy) != "" {
			continue
		}
		canonicalID := row.ServerID
		if server := servers[row.ServerID]; server != nil {
			canonicalID = server.ID
		}
		if serverID != "" && canonicalID != serverID {
			continue
		}
		items = append(items, response(row, servers[row.ServerID]))
	}
	validator := make([]serviceRuntimeResponse, len(items))
	for i, item := range items {
		validator[i] = item.withoutElapsedCounters()
	}
	return httpx.ConditionalSuccess(e, items, validator)
}

// withoutElapsedCounters clears `placement.freshness.age_seconds`, the clock
// minus a timestamp the response also carries (`health.observed_at`, or
// `placement.observed_at` for a managed workload). It is served as-is but
// excluded from the response validator, because it changes every second and
// would make a conditional read of an unchanged service never match.
func (r serviceRuntimeResponse) withoutElapsedCounters() serviceRuntimeResponse {
	r.Placement.Freshness.AgeSeconds = nil
	return r
}

func (h *serviceRuntimeHandlers) get(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	if serviceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.services.read")
	if tenantErr != nil {
		return tenantErr
	}
	service, err := h.loadServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service authorization is unavailable", nil)
	}
	var server *controlplane.ServerRuntime
	if serviceregistry.NormalizePlacement(service.ServerID, service.Placement).TargetKind == serviceregistry.TargetKindServer {
		server, err = h.servers.GetServerRuntime(e.Request.Context(), tenantID, service.ServerID)
		if err != nil || server == nil {
			// A server-bound service without readable server authority is not an
			// offline service. Keep the API honest about the unavailable inventory.
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service server inventory is unavailable", nil)
		}
	}
	item := h.response(*service, server)
	return httpx.ConditionalSuccess(e, item, item.withoutElapsedCounters())
}

func (h *serviceRuntimeHandlers) ownedStackIDs(e *httpx.Event, tenantID, ownerID string, isAdmin bool) (map[string]bool, error) {
	stacks, err := h.stacks.ListStacksByTenant(e.Request.Context(), tenantID)
	if err != nil {
		return nil, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service authorization is unavailable", nil)
	}
	result := make(map[string]bool, len(stacks))
	for _, stack := range stacks {
		if isAdmin || stack.OwnerSubjectID == ownerID {
			result[stack.ID] = true
		}
	}
	return result, nil
}

func (h *serviceRuntimeHandlers) serverMap(e *httpx.Event, tenantID, stackID string) (map[string]*controlplane.ServerRuntime, error) {
	rows, err := h.servers.ListServerRuntimesByTenant(e.Request.Context(), tenantID, stackID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*controlplane.ServerRuntime, len(rows))
	for i := range rows {
		result[rows[i].ID] = &rows[i]
		if rows[i].NodeID != "" {
			result[rows[i].NodeID] = &rows[i]
		}
	}
	return result, nil
}

// response gates service state on the PERSISTED server connection dimension.
// The registry sweeper demotes stale/offline servers as durable writes, so the
// read path no longer recomputes heartbeat freshness inline; persisted state
// is returned and demoted servers mask their services as unknown.
func (h *serviceRuntimeHandlers) response(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime) serviceRuntimeResponse {
	return h.responseWithAdmission(service, server, h.applicationAdmission)
}

// A list projects one current signed admission across its eligible rows. The
// closure belongs only to this read request; execution still admits afresh.
func (h *serviceRuntimeHandlers) responseForRequest() func(controlplane.ServiceRuntime, *controlplane.ServerRuntime) serviceRuntimeResponse {
	admission := h.applicationAdmission
	if admission != nil {
		admission = sync.OnceValues(admission)
	}
	return func(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime) serviceRuntimeResponse {
		return h.responseWithAdmission(service, server, admission)
	}
}

func (h *serviceRuntimeHandlers) responseWithAdmission(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime, admission func() (*agentpb.StackKitReleasePin, error)) serviceRuntimeResponse {
	if server != nil {
		service.ServerID = server.ID
	}
	placement := serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	observedState, healthState := service.ObservedState, service.HealthState
	reasonCode := ""
	access := sanitizedServiceAccess(service.Access)
	locked := service.MutationLock.Locked()
	allowedActions := serviceRuntimeAllowedActions(service.Capabilities, locked)
	version := h.serviceVersionFor(service)
	switch placement.TargetKind {
	case serviceregistry.TargetKindServer:
		connection := string(serverregistry.ConnectionOffline)
		if server != nil {
			connection = server.ConnectionState
		}
		if connection != string(serverregistry.ConnectionConnected) && connection != string(serverregistry.ConnectionDegraded) {
			observedState, healthState, reasonCode = monitoringStatusUnknown, monitoringStatusUnknown, "server_connection_"+connection
		}
	case serviceregistry.TargetKindUnknown:
		observedState, healthState, reasonCode = monitoringStatusUnknown, monitoringStatusUnknown, "service_placement_unknown"
		allowedActions = []string{}
	case serviceregistry.TargetKindManagedWorkload:
		// Provider-native controls are not implemented by this release. Preserve
		// measured state but do not advertise server/StackKit actions.
		allowedActions = []string{}
	}
	// A placement that supports no action at all must still be able to clear an
	// owner guardrail it acquired earlier, otherwise the lock is a trap.
	if locked && !containsString(allowedActions, serviceActionUnfreeze) {
		allowedActions = append(allowedActions, serviceActionUnfreeze)
	}
	adoption := h.adoptionAvailability(service, admission)
	// Serve the key and name the application read model actually groups by: an
	// observed row persisted before recognition carries the agent's `system`
	// placeholder, and a row stamped with its StackKit platform project would
	// otherwise name every service of the kit after the kit.
	grouping := resolveServiceApplication(service)
	applicationKey, applicationName := grouping.Key, applicationDisplayName(service, grouping.Key)
	if placement.TargetKind == serviceregistry.TargetKindServer {
		// Advertised from the same rules the endpoints enforce: update runs the
		// stack upgrade, adopt starts the adoption plan of an observed service.
		if updateAllowed(service, version, server) {
			allowedActions = append(allowedActions, serviceActionUpdate)
		}
		if backupAllowed(service, server) {
			allowedActions = append(allowedActions, serviceActionBackup)
		}
		if adoption.ExecutionAvailable && !locked && serviceregistry.CanonicalManagementState(service.ManagementState) == serviceregistry.ManagementObserved {
			allowedActions = append(allowedActions, serviceActionAdopt)
		}
		sort.Strings(allowedActions)
	}
	if reasonCode != "" {
		access = serviceAccess(serviceAccessUnavailable, "", "", reasonCode, "")
	}
	placementFreshness := h.placementFreshness(placement, service.ObservedAt)
	provenance := map[string]interface{}{
		"definition_authority": serviceStackKits, "runtime_authority": serviceRuntimeAuthority, "observation_source": service.Source,
	}
	if control := stringFromAnyMap(service.Metadata, serviceControlKey); control != "" {
		provenance[serviceControlKey] = control
		provenance[serviceControlID] = stringFromAnyMap(service.Metadata, serviceControlID)
		provenance["control_scope"] = "application"
		provenance["control_application_key"] = applicationKey
	}
	var actionGuard map[string]any
	if sources := serviceGroupLockSources(service); len(sources) != 0 {
		actionGuard = map[string]any{"reason_code": "application_control_locked", "unlock_required_action": serviceActionUnfreeze, "unlock_service_ids": sources}
	}
	if counterpart := stringFromAnyMap(service.Metadata, serviceSupersededBy); counterpart != "" {
		provenance[serviceSupersededBy] = counterpart
		allowedActions = []string{}
		if locked {
			allowedActions = append(allowedActions, serviceActionUnfreeze)
		}
	}
	if backfill, _ := service.Metadata["backfill"].(bool); backfill {
		provenance["migration_mode"] = "read_through_backfill"
	}
	return serviceRuntimeResponse{
		ID: service.ID, KitDeploymentID: service.StackID, ServerID: service.ServerID,
		TargetKind: string(placement.TargetKind),
		Placement: serviceRuntimePlacement{
			ProviderID: placement.ProviderID, ManagedTargetRef: placement.ManagedTargetRef,
			ProviderReceiptRef: placement.ProviderReceiptRef, SLAPolicyRef: placement.SLAPolicyRef,
			BackupPolicyRef: placement.BackupPolicyRef, EvidenceRef: placement.EvidenceRef,
			ObservedAt: placement.ObservedAt, Freshness: placementFreshness,
		},
		ServiceKey: service.ServiceKey, ServiceInstance: service.ServiceInstance,
		Name:                   service.Name,
		ApplicationKey:         applicationKey,
		ApplicationDisplayName: applicationName,
		Catalog:                serviceCatalogEntry(service),
		Version:                version,
		PlacementClass:         servicePlacementClass(placement, server),
		Role:                   stringFromAnyMap(service.Metadata, "role"),
		Lifecycle:              stringFromAnyMap(service.Metadata, "lifecycle"),
		OperationalImpact:      stringFromAnyMap(service.Metadata, "operational_impact"),
		InternalAddress:        boundedInternalServiceAddress(stringFromAnyMap(service.Metadata, "internal_address")),
		RuntimeIdentity:        stringMapFromAny(service.Metadata["runtime_identity"]),
		Adoption:               adoption,
		ManagementState:        string(serviceregistry.CanonicalManagementState(service.ManagementState)),
		MutationLock:           serviceMutationLockResponse(service.MutationLock),
		DesiredState:           service.DesiredState, ObservedState: observedState,
		Health:          serviceRuntimeHealth{State: healthState, ObservedAt: service.ObservedAt, ReasonCode: reasonCode},
		StackKitVersion: service.StackKitVersion, Access: access,
		AllowedActions: allowedActions, Source: service.Source,
		InventoryRevision: metadataInt64(service.Metadata, "inventory_revision"),
		EvidenceRef:       stringFromAnyMap(service.Metadata, "evidence_ref"),
		Provenance:        provenance,
		ActionGuard:       actionGuard,
		CreatedAt:         service.CreatedAt, UpdatedAt: service.UpdatedAt,
	}
}

// placementFreshness reports how old the evidence behind a placement is. The
// placement shapes are disjoint (migration 076): a server placement carries no
// placement evidence of its own, because the server binding is the placement
// and the service's runtime observation on that server is its evidence. A
// managed workload carries its own evidence reference and observation time.
// Anything else stays unknown.
func (h *serviceRuntimeHandlers) placementFreshness(placement serviceregistry.Placement, serviceObservedAt *time.Time) serverRuntimeTargetFreshness {
	var observedAt *time.Time
	switch placement.TargetKind {
	case serviceregistry.TargetKindServer:
		observedAt = serviceObservedAt
	case serviceregistry.TargetKindManagedWorkload:
		if placement.EvidenceRef != "" {
			observedAt = placement.ObservedAt
		}
	}
	if observedAt == nil || observedAt.IsZero() {
		return serverRuntimeTargetFreshness{State: "unknown"}
	}
	seconds := max(int64(h.now().UTC().Sub(observedAt.UTC()).Seconds()), 0)
	return serverRuntimeTargetFreshness{State: "recorded", AgeSeconds: &seconds}
}

func stringMapFromAny(value any) map[string]string {
	result := map[string]string{}
	values, _ := value.(map[string]any)
	if direct, ok := value.(map[string]string); ok {
		return boundedStringMap(direct, 16, 128, 1024)
	}
	for key, item := range values {
		if text, ok := item.(string); ok {
			result[key] = text
		}
	}
	return boundedStringMap(result, 16, 128, 1024)
}

func sanitizedServiceAccess(access map[string]any) map[string]any {
	mode := stringFromAnyMap(access, serviceAccessModeKey)
	switch mode {
	case serviceAccessDirect:
		if address := safePublicServiceURL(stringFromAnyMap(access, serviceAccessURLKey)); address != "" {
			return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", "")
		}
		observed, _ := boolValueFromAny(access[serviceAccessObservedKey])
		if observed && stringFromAnyMap(access, serviceAccessSourceKey) == serviceStackKitManifest {
			if address := safeObservedDirectServiceURL(stringFromAnyMap(access, serviceAccessURLKey)); address != "" {
				return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", "")
			}
		}
	case serviceAccessRelay:
		address := safeKombifyRelayURL(stringFromAnyMap(access, serviceAccessURLKey))
		routeID := boundedAccessValue(access, "route_id")
		if address != "" && routeID != "" {
			return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", routeID)
		}
	}
	return serviceAccess(serviceAccessUnavailable, "", "", firstNonEmptyString(boundedAccessValue(access, "reason_code"), "invalid_access_contract"), "")
}

func boundedAccessValue(access map[string]any, key string) string {
	value := strings.TrimSpace(stringFromAnyMap(access, key))
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

// serviceActionKitName extracts the kit name from a service's StackKit
// identity. The inventory records either "kit@version" or only the bare
// version ("5.1.0"); a bare version names no kit, and handing it to the local
// execution binding made every action on a cloud-kit service fail with "no
// local execution binding is known for StackKit \"5.1.0\"".
func serviceActionKitName(identity string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(identity), "@")
	name = strings.TrimSpace(name)
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return ""
	}
	return name
}
