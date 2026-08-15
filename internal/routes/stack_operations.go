//nolint:goconst
package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/prometheus/prometheus/promql"
)

type StackOperationsRouteStores struct {
	Stacks   controlplane.StackStore
	Servers  controlplane.ServerRuntimeStore
	Services controlplane.ServiceRuntimeStore
	Workers  controlplane.WorkerStore
	Registry controlplane.RegistryStore
	Jobs     controlplane.JobStore
}

func RegisterStackOperationsRoutesWithStores(r *httpx.Router, app core.App, backend monitoring.MetricsQueryBackend, metadata MonitoringStatusMetadata, alerts *monitoring.AlertEngine, ingestHealth monitoring.IngestHealthProvider, stores StackOperationsRouteStores, managedRuntimeLeases ...managedRuntimeLeaseLister) { // pocketbase-migration-compat: legacy app bridge while operations stores are wired
	var leaseLister managedRuntimeLeaseLister
	if len(managedRuntimeLeases) > 0 {
		leaseLister = managedRuntimeLeases[0]
	}
	h := stackOperationsRouteHandlers{
		app:                  app,
		stackStore:           stores.Stacks,
		serverStore:          stores.Servers,
		serviceStore:         stores.Services,
		workerStore:          stores.Workers,
		registryStore:        stores.Registry,
		jobStore:             stores.Jobs,
		backend:              backend,
		metadata:             metadata,
		alerts:               alerts,
		ingestHealth:         ingestHealth,
		managedRuntimeLeases: leaseLister,
	}
	registerInventoryMCPStackOperationsHandler(r, h.operations)
	r.GET("/api/v1/stacks/{id}/operations", h.operations)
	r.GET("/api/v1/stacks/{id}/servers/{serverId}", h.serverDetails)
	r.POST("/api/v1/stacks/{id}/workers/{workerId}/assign", h.assignWorker)
	r.GET("/api/v1/monitor/cockpit", h.monitorCockpit)
}

type stackOperationsRouteHandlers struct {
	app                  core.App
	stackStore           controlplane.StackStore
	serverStore          controlplane.ServerRuntimeStore
	serviceStore         controlplane.ServiceRuntimeStore
	workerStore          controlplane.WorkerStore
	registryStore        controlplane.RegistryStore
	jobStore             controlplane.JobStore
	backend              monitoring.MetricsQueryBackend
	metadata             MonitoringStatusMetadata
	alerts               *monitoring.AlertEngine
	ingestHealth         monitoring.IngestHealthProvider
	managedRuntimeLeases managedRuntimeLeaseLister
}

type stackOperationsPayload struct {
	Stack            map[string]any             `json:"stack"`
	Readiness        stackReadiness             `json:"readiness"`
	NextSteps        []stackNextStep            `json:"nextSteps"`
	KPIs             stackOperationKPIs         `json:"kpis"`
	Servers          []stackOperationServer     `json:"servers"`
	Services         []stackOperationService    `json:"services"`
	Monitoring       stackOperationMonitoring   `json:"monitoring"`
	Alerts           []stackOperationAlertState `json:"alerts"`
	CurrentJob       *monitorCockpitJob         `json:"currentJob,omitempty"`
	RuntimeLifecycle *stackRuntimeLifecycle     `json:"runtimeLifecycle,omitempty"`
	LatestFailure    *stackLatestFailure        `json:"latestFailure,omitempty"`
	// CustodyLeases carries managed-runtime leases that still hold custody (and
	// can still cost money) but have no machine behind them. They are
	// deliberately not servers: a lease whose VM was deleted must never present
	// as infrastructure.
	CustodyLeases []stackCustodyLease `json:"custodyLeases,omitempty"`
}

// stackCustodyLease is a lease without machine evidence. It stays visible so
// billing exposure and cleanup work remain discoverable, but it carries no
// health, no metrics, and no fabricated address.
type stackCustodyLease struct {
	LeaseID        string   `json:"lease_id"`
	ServerID       string   `json:"server_id,omitempty"`
	Label          string   `json:"label"`
	Provider       string   `json:"provider,omitempty"`
	Reason         string   `json:"reason"`
	Status         string   `json:"status,omitempty"`
	LastKnownIP    string   `json:"last_known_ip,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
	AllowedActions []string `json:"allowed_actions,omitempty"`
}

type monitorCockpitPayload struct {
	Stacks      []map[string]any           `json:"stacks"`
	TechstackID string                     `json:"techstack_id"`
	Stack       map[string]any             `json:"stack"`
	Readiness   stackReadiness             `json:"readiness"`
	NextSteps   []stackNextStep            `json:"nextSteps"`
	KPIs        stackOperationKPIs         `json:"kpis"`
	Servers     []stackOperationServer     `json:"servers"`
	Services    []stackOperationService    `json:"services"`
	Monitoring  stackOperationMonitoring   `json:"monitoring"`
	Alerts      []stackOperationAlertState `json:"alerts"`
	Jobs        []monitorCockpitJob        `json:"jobs"`
}

type monitorCockpitJob struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	State             string `json:"state"`
	Progress          int    `json:"progress"`
	Step              string `json:"step,omitempty"`
	Message           string `json:"message,omitempty"`
	Error             string `json:"error,omitempty"`
	WaitReason        string `json:"wait_reason,omitempty"`
	NextResumeAt      string `json:"next_resume_at,omitempty"`
	ResumeAvailableAt string `json:"resume_available_at,omitempty"`
	ResumeAvailable   bool   `json:"resume_available,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type stackRuntimeLifecycle struct {
	Version            string                       `json:"version"`
	CurrentPhase       string                       `json:"current_phase,omitempty"`
	LastSafeCheckpoint string                       `json:"last_safe_checkpoint,omitempty"`
	Phases             []stackRuntimeLifecyclePhase `json:"phases"`
}

type stackRuntimeLifecyclePhase struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Message    string         `json:"message,omitempty"`
	ObservedAt string         `json:"observed_at,omitempty"`
	Evidence   map[string]any `json:"evidence,omitempty"`
}

type stackLatestFailure struct {
	JobID                string         `json:"job_id"`
	Type                 string         `json:"type"`
	State                string         `json:"state"`
	Step                 string         `json:"step,omitempty"`
	Message              string         `json:"message,omitempty"`
	Error                string         `json:"error,omitempty"`
	Reason               string         `json:"reason,omitempty"`
	LeaseID              string         `json:"lease_id,omitempty"`
	RuntimeIP            string         `json:"runtime_ip,omitempty"`
	RuntimePhase         string         `json:"runtime_phase,omitempty"`
	TargetBootstrap      map[string]any `json:"target_bootstrap,omitempty"`
	RuntimeDiagnostics   map[string]any `json:"runtime_diagnostics,omitempty"`
	DiagnosticsAvailable bool           `json:"diagnostics_available"`
	CreatedAt            string         `json:"created_at,omitempty"`
	UpdatedAt            string         `json:"updated_at,omitempty"`
}

type stackOperationServer struct {
	ID               string                `json:"id"`
	ServerID         string                `json:"server_id,omitempty"`
	Hostname         string                `json:"hostname"`
	Role             string                `json:"role"`
	Status           string                `json:"status"`
	Assignment       string                `json:"assignment"`
	TechstackID      string                `json:"techstack_id,omitempty"`
	AgentID          string                `json:"agent_id"`
	IP               string                `json:"ip,omitempty"`
	HostAddresses    []stackServerAddress  `json:"host_addresses,omitempty"`
	OS               string                `json:"os,omitempty"`
	OSVersion        string                `json:"os_version,omitempty"`
	Arch             string                `json:"arch,omitempty"`
	Domains          []string              `json:"domains,omitempty"`
	ServiceEndpoints []stackServerEndpoint `json:"service_endpoints,omitempty"`
	StackKit         *stackServerStackKit  `json:"stackkit,omitempty"`
	LastSeen         string                `json:"last_seen,omitempty"`
	Approved         bool                  `json:"approved"`
	ApprovedAt       string                `json:"approved_at,omitempty"`
	PreCheck         string                `json:"precheck_state"`
	Source           string                `json:"source,omitempty"`
	LeaseID          string                `json:"lease_id,omitempty"`
	RuntimeLane      string                `json:"runtime_lane,omitempty"`
	RuntimeOffering  string                `json:"runtime_offering_id,omitempty"`
	DesiredState     string                `json:"desired_state,omitempty"`
	EnrollmentStatus string                `json:"enrollment_status,omitempty"`
	Assignable       bool                  `json:"assignable"`
	Capabilities     map[string]any        `json:"capabilities"`
	Health           stackServerHealth     `json:"health"`
	heartbeatAt      *time.Time
	observedAt       time.Time
}

type stackServerAddress struct {
	Address    string `json:"address"`
	Scope      string `json:"scope"`
	Provenance string `json:"provenance"`
}

type stackServerEndpoint struct {
	ServiceID  string `json:"service_id,omitempty"`
	ServiceKey string `json:"service_key,omitempty"`
	Name       string `json:"name,omitempty"`
	URL        string `json:"url"`
	Domain     string `json:"domain,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Health     string `json:"health,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Source     string `json:"source"`
	ObservedAt string `json:"observed_at,omitempty"`
}

type stackServerStackKit struct {
	Name        string   `json:"name,omitempty"`
	CatalogRef  string   `json:"catalog_ref,omitempty"`
	Version     string   `json:"version,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Context     string   `json:"context,omitempty"`
	PaaS        string   `json:"paas,omitempty"`
	ComputeTier string   `json:"compute_tier,omitempty"`
	State       string   `json:"state"`
	Sources     []string `json:"sources"`
}

type stackServerHealth struct {
	State         string           `json:"state"`
	Source        string           `json:"source"`
	CPUPercent    stackMetricValue `json:"cpu_percent"`
	MemoryPercent stackMetricValue `json:"memory_percent"`
	DiskPercent   stackMetricValue `json:"disk_percent"`
	UptimeSeconds stackMetricValue `json:"uptime_seconds"`
	UpdatedAt     string           `json:"updated_at,omitempty"`
	Notes         []string         `json:"notes,omitempty"`
}

type stackMetricValue struct {
	Status string   `json:"status"`
	Value  *float64 `json:"value,omitempty"`
	Unit   string   `json:"unit,omitempty"`
}

type stackServerDetailsPayload struct {
	Stack      map[string]any           `json:"stack"`
	Server     stackOperationServer     `json:"server"`
	Services   []stackOperationService  `json:"services"`
	Checks     []PreCheckResultResponse `json:"checks"`
	Logs       []map[string]any         `json:"logs"`
	Health     stackServerHealth        `json:"health"`
	Monitoring stackOperationMonitoring `json:"monitoring"`
}

func (h stackOperationsRouteHandlers) operations(e *httpx.Event) error {
	// The durable store is authoritative whenever it is configured. In
	// particular, an authenticated hosted request can legitimately use its
	// owner subject as the fallback tenant when no explicit organization claim
	// is present. Requiring an explicit claim here selected the legacy
	// projection and hid the durable current job from the Operations view.
	// ownedControlPlaneStack still authenticates first, scopes the lookup to
	// requestTenantID, and verifies the stack owner before any projection.
	if h.stackStore != nil {
		if err := h.operationsFromStore(e); err == nil || !isHTTPNotFound(err) {
			return err
		}
	}

	return h.operationsFromLegacy(e)
}

func (h stackOperationsRouteHandlers) operationsFromLegacy(e *httpx.Event) error {
	stack, ownerID, err := h.ownedStack(e)
	if err != nil {
		return err
	}
	if guardErr := tenantguard.RequireTenant(requestExplicitTenantID(e), "techstack.stacks.operations"); guardErr != nil {
		return guardErr
	}

	ctx := e.Request.Context()
	tenantID := requestTenantID(e, ownerID)
	servers, err := h.operationServers(ctx, ownerID, tenantID, requestExplicitTenantID(e), stack.Id)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	services := h.operationServices(stack, servers, tenantID)
	alerts, unscopedAlerts := h.operationAlerts(stack.Id, servers)
	latestFailure := latestStackFailureFromLegacyJobs(h.stackJobs(stack.Id))
	readiness := buildStackReadiness(stack, servers, latestFailure)
	monitoring := h.monitoringSummary(ctx)
	monitoring.UnscopedAlerts = unscopedAlerts

	return httpx.Success(e, http.StatusOK, stackOperationsPayload{
		Stack:         stackListItemFromRecord(stack),
		Readiness:     readiness,
		NextSteps:     buildStackNextSteps(stack, readiness),
		KPIs:          buildStackKPIs(servers, services, alerts),
		Servers:       servers,
		Services:      services,
		Monitoring:    monitoring,
		Alerts:        alerts,
		LatestFailure: latestFailure,
	})
}

func isHTTPNotFound(err error) bool {
	var apiErr *httpx.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func managedRuntimeInventoryUnavailable(e *httpx.Event) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		"Managed runtime inventory is temporarily unavailable", map[string]any{
			"reason_code": "managed_runtime_authority_unavailable",
			"retryable":   true,
		})
}

func (h stackOperationsRouteHandlers) monitorCockpit(e *httpx.Event) error {
	if h.useControlPlaneStore(e) {
		return h.monitorCockpitFromStore(e)
	}

	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	stacks, err := h.ownedStacks(ownerID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stacks", nil)
	}
	if len(stacks) == 0 {
		return httpx.Success(e, http.StatusOK, monitorCockpitPayload{
			Stacks:     []map[string]any{},
			Servers:    []stackOperationServer{},
			Services:   []stackOperationService{},
			Alerts:     []stackOperationAlertState{},
			Jobs:       []monitorCockpitJob{},
			Monitoring: h.monitoringSummary(e.Request.Context()),
		})
	}

	selectedID := strings.TrimSpace(e.Request.URL.Query().Get("techstack_id"))
	selected := stacks[0]
	if selectedID != "" {
		selected = nil
		for _, stack := range stacks {
			if stack.Id == selectedID {
				selected = stack
				break
			}
		}
		if selected == nil {
			return httpx.NotFound(e, "Stack not found")
		}
	}

	ctx := e.Request.Context()
	tenantID := requestTenantID(e, ownerID)
	servers, err := h.operationServers(ctx, ownerID, tenantID, requestExplicitTenantID(e), selected.Id)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	services := h.operationServices(selected, servers, tenantID)
	alerts, unscopedAlerts := h.operationAlerts(selected.Id, servers)
	// One read feeds both the jobs list and the readiness message, so the two
	// can never describe different snapshots of the same stack.
	jobs := h.stackJobs(selected.Id)
	readiness := buildStackReadiness(selected, servers, latestStackFailureFromLegacyJobs(jobs))
	monitoring := h.monitoringSummary(ctx)
	monitoring.UnscopedAlerts = unscopedAlerts

	stackItems := make([]map[string]any, 0, len(stacks))
	for _, stack := range stacks {
		stackItems = append(stackItems, stackListItemFromRecord(stack))
	}

	return httpx.Success(e, http.StatusOK, monitorCockpitPayload{
		Stacks:      stackItems,
		TechstackID: selected.Id,
		Stack:       stackListItemFromRecord(selected),
		Readiness:   readiness,
		NextSteps:   buildStackNextSteps(selected, readiness),
		KPIs:        buildStackKPIs(servers, services, alerts),
		Servers:     servers,
		Services:    services,
		Monitoring:  monitoring,
		Alerts:      alerts,
		Jobs:        jobs,
	})
}

func (h stackOperationsRouteHandlers) operationsFromStore(e *httpx.Event) error {
	stack, ownerID, tenantID, err := h.ownedControlPlaneStack(e)
	if err != nil {
		return err
	}

	ctx := e.Request.Context()
	servers, custodyLeases, err := h.operationServersFromStore(ctx, ownerID, tenantID, stack.ID)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	currentJob, runtimeLifecycle := h.latestStackRuntimeLifecycleFromStore(ctx, tenantID, stack.ID)
	latestFailure := h.latestStackFailureFromStore(ctx, tenantID, stack.ID)
	latestRecordedFailure := latestFailure
	latestFailure = activeStackFailure(latestFailure, servers, custodyLeases)
	servers = annotateManagedRuntimeServersWithFailure(servers, latestFailure)
	services := h.operationServicesFromStore(ctx, tenantID, stack, servers)
	alerts, unscopedAlerts := h.operationAlerts(stack.ID, servers)
	readiness := buildStackReadinessFromStore(stack, servers, latestFailure)
	readiness = reconcileResolvedDestroyReadiness(readiness, latestRecordedFailure, latestFailure, servers, custodyLeases)
	monitoring := h.monitoringSummary(ctx)
	monitoring.UnscopedAlerts = unscopedAlerts

	return httpx.Success(e, http.StatusOK, stackOperationsPayload{
		Stack:            stackListItemFromControlPlane(stack),
		Readiness:        readiness,
		NextSteps:        buildStackNextStepsFromStore(stack, readiness),
		KPIs:             buildStackKPIs(servers, services, alerts),
		Servers:          servers,
		Services:         services,
		Monitoring:       monitoring,
		Alerts:           alerts,
		CurrentJob:       currentJob,
		RuntimeLifecycle: runtimeLifecycle,
		LatestFailure:    latestFailure,
		CustodyLeases:    custodyLeases,
	})
}

func (h stackOperationsRouteHandlers) monitorCockpitFromStore(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenantID := requestTenantID(e, ownerID)
	stacks, err := h.ownedControlPlaneStacks(e.Request.Context(), tenantID, ownerID)
	if err != nil {
		return httpx.NewInternalServerError("Failed to fetch stacks", nil)
	}
	if len(stacks) == 0 {
		return httpx.Success(e, http.StatusOK, monitorCockpitPayload{
			Stacks:     []map[string]any{},
			Servers:    []stackOperationServer{},
			Services:   []stackOperationService{},
			Alerts:     []stackOperationAlertState{},
			Jobs:       []monitorCockpitJob{},
			Monitoring: h.monitoringSummary(e.Request.Context()),
		})
	}

	selectedID := strings.TrimSpace(e.Request.URL.Query().Get("techstack_id"))
	selected := &stacks[0]
	if selectedID != "" {
		selected = nil
		for i := range stacks {
			if stacks[i].ID == selectedID {
				selected = &stacks[i]
				break
			}
		}
		if selected == nil {
			return httpx.NotFound(e, "Stack not found")
		}
	}

	ctx := e.Request.Context()
	servers, _, err := h.operationServersFromStore(ctx, ownerID, tenantID, selected.ID)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	services := h.operationServicesFromStore(ctx, tenantID, selected, servers)
	alerts, unscopedAlerts := h.operationAlerts(selected.ID, servers)
	// One read feeds both the jobs list and the readiness message. Two reads
	// would not only double the query on a 10s poll, they could also observe
	// different snapshots - readiness naming a failure the rendered jobs list
	// no longer contains.
	jobs, latestFailure := h.stackJobsAndFailureFromStore(ctx, tenantID, selected.ID)
	readiness := buildStackReadinessFromStore(selected, servers, latestFailure)
	monitoring := h.monitoringSummary(ctx)
	monitoring.UnscopedAlerts = unscopedAlerts

	stackItems := make([]map[string]any, 0, len(stacks))
	for i := range stacks {
		stackItems = append(stackItems, stackListItemFromControlPlane(&stacks[i]))
	}

	return httpx.Success(e, http.StatusOK, monitorCockpitPayload{
		Stacks:      stackItems,
		TechstackID: selected.ID,
		Stack:       stackListItemFromControlPlane(selected),
		Readiness:   readiness,
		NextSteps:   buildStackNextStepsFromStore(selected, readiness),
		KPIs:        buildStackKPIs(servers, services, alerts),
		Servers:     servers,
		Services:    services,
		Monitoring:  monitoring,
		Alerts:      alerts,
		Jobs:        jobs,
	})
}

func (h stackOperationsRouteHandlers) serverDetails(e *httpx.Event) error {
	if h.useControlPlaneStore(e) {
		return h.serverDetailsFromStore(e)
	}

	stack, ownerID, err := h.ownedStack(e)
	if err != nil {
		return err
	}

	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return httpx.BadRequest(e, "Server ID is required", nil)
	}

	ctx := e.Request.Context()
	tenantID := requestTenantID(e, ownerID)
	servers, err := h.operationServers(ctx, ownerID, tenantID, requestExplicitTenantID(e), stack.Id)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	server, found := findOperationServer(servers, serverID)
	if !found {
		return httpx.NotFound(e, "Server not found for stack")
	}
	services := servicesForServer(h.operationServices(stack, servers, tenantID), server)
	checks := []PreCheckResultResponse{}
	if server.Source != managedRuntimeInventorySource {
		worker, err := h.app.FindRecordById("workers", serverID)
		if err != nil {
			return httpx.NotFound(e, "Server not found")
		}
		if worker.GetString("owner_id") != ownerID {
			return httpx.Forbidden(e, "Not allowed")
		}
		if workerStackID := worker.GetString("stack_id"); workerStackID != "" && workerStackID != stack.Id {
			return httpx.NotFound(e, "Server not found for stack")
		}
		checks = h.serverPreChecks(ownerID, stack.Id, worker.Id)
	}

	return httpx.Success(e, http.StatusOK, stackServerDetailsPayload{
		Stack:      stackListItemFromRecord(stack),
		Server:     server,
		Services:   services,
		Checks:     checks,
		Logs:       h.serverLogs(stack.Id, server),
		Health:     server.Health,
		Monitoring: h.monitoringSummary(ctx),
	})
}

func (h stackOperationsRouteHandlers) serverDetailsFromStore(e *httpx.Event) error {
	stack, ownerID, tenantID, err := h.ownedControlPlaneStack(e)
	if err != nil {
		return err
	}

	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return httpx.BadRequest(e, "Server ID is required", nil)
	}

	ctx := e.Request.Context()
	servers, _, err := h.operationServersFromStore(ctx, ownerID, tenantID, stack.ID)
	if err != nil {
		return managedRuntimeInventoryUnavailable(e)
	}
	server, found := findOperationServer(servers, serverID)
	if !found {
		return httpx.NotFound(e, "Server not found for stack")
	}

	checks := []PreCheckResultResponse{}
	if server.Source == workerRegistryInventorySource && h.workerStore != nil {
		worker, err := h.workerStore.GetWorker(ctx, tenantID, serverID)
		if err != nil {
			if errors.Is(err, controlplane.ErrNotFound) {
				return httpx.NotFound(e, "Server not found")
			}
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch server", nil)
		}
		if worker.OwnerSubjectID != ownerID {
			return httpx.Forbidden(e, "Not allowed")
		}
		if workerStackID := strings.TrimSpace(worker.StackID); workerStackID != "" && workerStackID != stack.ID {
			return httpx.NotFound(e, "Server not found for stack")
		}
		checks = h.serverPreChecks(ownerID, stack.ID, worker.ID)
	}

	logs := []map[string]any{}
	if h.app != nil {
		logs = h.serverLogs(stack.ID, server)
	}

	return httpx.Success(e, http.StatusOK, stackServerDetailsPayload{
		Stack:      stackListItemFromControlPlane(stack),
		Server:     server,
		Services:   servicesForServer(h.operationServicesFromStore(ctx, tenantID, stack, servers), server),
		Checks:     checks,
		Logs:       logs,
		Health:     server.Health,
		Monitoring: h.monitoringSummary(ctx),
	})
}

func (h stackOperationsRouteHandlers) assignWorker(e *httpx.Event) error {
	if h.useControlPlaneStore(e) && h.workerStore != nil {
		return h.assignWorkerFromStore(e)
	}

	stack, ownerID, err := h.ownedStack(e)
	if err != nil {
		return err
	}

	workerID := strings.TrimSpace(e.Request.PathValue("workerId"))
	if workerID == "" {
		return httpx.BadRequest(e, "Worker ID is required", nil)
	}

	worker, err := h.app.FindRecordById("workers", workerID)
	if err != nil {
		return httpx.NotFound(e, "Worker not found")
	}
	if worker.GetString("owner_id") != ownerID {
		return httpx.Forbidden(e, "Not allowed")
	}
	if !worker.GetBool("approved") || !workerAssignmentStatusAllowed(worker.GetString("status")) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Confirm the server registration before assigning it to a stack", map[string]any{
			"worker_id": worker.Id,
		})
	}

	currentStackID := strings.TrimSpace(worker.GetString("stack_id"))
	if currentStackID != "" && currentStackID != stack.Id {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Worker is already assigned to another stack", map[string]any{
			"worker_id": worker.Id,
			"stack_id":  currentStackID,
		})
	}

	worker.Set("stack_id", stack.Id)
	if err := h.app.Save(worker); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to assign worker", nil)
	}

	server := h.operationServerFromWorker(e.Request.Context(), stack.Id, worker)
	return httpx.Success(e, http.StatusOK, map[string]any{
		"stack_id":  stack.Id,
		"worker_id": worker.Id,
		"server":    server,
	})
}

func (h stackOperationsRouteHandlers) useControlPlaneStore(e *httpx.Event) bool {
	return h.stackStore != nil && requestExplicitTenantID(e) != ""
}

func (h stackOperationsRouteHandlers) assignWorkerFromStore(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "Stack ID is required", nil)
	}
	workerID := strings.TrimSpace(e.Request.PathValue("workerId"))
	if workerID == "" {
		return httpx.BadRequest(e, "Worker ID is required", nil)
	}

	ctx := e.Request.Context()
	tenantID := requestTenantID(e, ownerID)
	stack, err := h.stackStore.GetStack(ctx, tenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "Stack not found")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return httpx.Forbidden(e, "Not your stack")
	}

	worker, err := h.workerStore.GetWorker(ctx, tenantID, workerID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "Worker not found")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch worker", nil)
	}
	if worker.OwnerSubjectID != ownerID {
		return httpx.Forbidden(e, "Not allowed")
	}
	if !worker.Approved || !workerAssignmentStatusAllowed(worker.Status) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Confirm the server registration before assigning it to a stack", map[string]any{
			"worker_id": worker.ID,
		})
	}
	currentStackID := strings.TrimSpace(worker.StackID)
	if currentStackID != "" && currentStackID != stack.ID {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Worker is already assigned to another stack", map[string]any{
			"worker_id": worker.ID,
			"stack_id":  currentStackID,
		})
	}

	if _, bindErr := h.bindCanonicalWorkerRuntime(ctx, tenantID, ownerID, stack.ID, *worker); bindErr != nil {
		switch {
		case errors.Is(bindErr, controlplane.ErrNotFound):
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The worker has no canonical runtime registration to assign", map[string]any{
				"worker_id": worker.ID,
				"stack_id":  stack.ID,
			})
		case errors.Is(bindErr, controlplane.ErrConflict):
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The canonical worker runtime cannot be assigned to this stack", map[string]any{
				"worker_id": worker.ID,
				"stack_id":  stack.ID,
			})
		default:
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Canonical server assignment is unavailable; the worker was not assigned", nil)
		}
	}

	// ServerRuntime is the authority binding. workers.stack_id is its legacy
	// worker projection and is written only after the CAS command succeeds. If
	// this projection write fails, deploy admission still fails closed because
	// it requires both records to carry the exact same tenant/owner/stack.
	worker.StackID = stack.ID
	updated, err := h.workerStore.UpsertWorkerHeartbeat(ctx, *worker)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to assign worker", nil)
	}
	server := h.operationServerFromControlPlaneWorker(ctx, ownerID, stack.ID, updated)
	return httpx.Success(e, http.StatusOK, map[string]any{
		"stack_id":  stack.ID,
		"worker_id": updated.ID,
		"server":    server,
	})
}

func workerAssignmentStatusAllowed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "approved", "connected":
		return true
	default:
		return false
	}
}

func (h stackOperationsRouteHandlers) bindCanonicalWorkerRuntime(
	ctx context.Context,
	tenantID, ownerID, stackID string,
	worker controlplane.Worker,
) (*controlplane.ServerRuntime, error) {
	if h.serverStore == nil {
		return nil, fmt.Errorf("canonical server store is not configured")
	}
	eventStore, ok := h.serverStore.(controlplane.ServerEventStore)
	if !ok {
		return nil, fmt.Errorf("canonical server store does not support atomic events")
	}
	serverID, err := assignmentServerID(worker)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 4; attempt++ {
		current, getErr := h.serverStore.GetServerRuntime(ctx, tenantID, serverID)
		if getErr != nil {
			return nil, getErr
		}
		if validateErr := validateWorkerRuntimeAssignment(*current, worker, tenantID, ownerID, stackID); validateErr != nil {
			return nil, validateErr
		}
		if strings.TrimSpace(current.StackID) == stackID {
			return current, nil
		}

		result, applyErr := eventStore.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: tenantID, ServerID: serverID,
			ExpectedRevision: current.Revision, Generation: current.Generation,
			Authority: controlplane.ServerEventAuthorityControlPlane,
			Source:    "stack-worker-assignment", SourceID: "stack-operations",
			ObservedAt: time.Now().UTC(),
			Evidence: map[string]any{
				"worker_id": worker.ID,
				"stack_id":  stackID,
			},
			Runtime: controlplane.ServerRuntime{StackID: stackID},
		})
		if applyErr == nil {
			if result == nil || result.Server == nil || strings.TrimSpace(result.Server.StackID) != stackID {
				return nil, fmt.Errorf("canonical server assignment returned no bound aggregate")
			}
			return result.Server, nil
		}
		if !errors.Is(applyErr, controlplane.ErrConflict) {
			return nil, applyErr
		}

		latest, retry, resolveErr := h.resolveWorkerRuntimeAssignmentConflict(
			ctx, tenantID, ownerID, stackID, serverID, worker, current.Revision, applyErr,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if !retry {
			return latest, nil
		}
	}
	return nil, fmt.Errorf("%w: canonical worker assignment contention exceeded retry budget", controlplane.ErrConflict)
}

func (h stackOperationsRouteHandlers) resolveWorkerRuntimeAssignmentConflict(
	ctx context.Context,
	tenantID, ownerID, stackID, serverID string,
	worker controlplane.Worker,
	previousRevision int64,
	conflictErr error,
) (*controlplane.ServerRuntime, bool, error) {
	latest, err := h.serverStore.GetServerRuntime(ctx, tenantID, serverID)
	if err != nil {
		return nil, false, err
	}
	if err := validateWorkerRuntimeAssignment(*latest, worker, tenantID, ownerID, stackID); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(latest.StackID) == stackID {
		return latest, false, nil
	}
	if latest.Revision == previousRevision {
		return nil, false, conflictErr
	}
	return nil, true, nil
}

func assignmentServerID(worker controlplane.Worker) (string, error) {
	leaseID := strings.TrimSpace(runtimeLeaseIDFromMetadata(worker.Capabilities))
	reportedServerID := strings.TrimSpace(stringFromAny(worker.Capabilities[workerFieldServerID]))
	if leaseID != "" {
		expectedServerID := runtimeidentity.LeaseServerID(leaseID)
		if expectedServerID == "" || (reportedServerID != "" && reportedServerID != expectedServerID) {
			return "", fmt.Errorf("%w: worker lease and server identity do not match", controlplane.ErrConflict)
		}
		return expectedServerID, nil
	}
	serverID := firstNonEmpty(reportedServerID, runtimeServerIDForWorker(worker.ID))
	if serverID == "" {
		return "", fmt.Errorf("%w: worker server identity is missing", controlplane.ErrConflict)
	}
	return serverID, nil
}

func validateWorkerRuntimeAssignment(
	runtime controlplane.ServerRuntime,
	worker controlplane.Worker,
	tenantID, ownerID, stackID string,
) error {
	if runtime.TenantID != tenantID || worker.TenantID != tenantID ||
		runtime.OwnerSubjectID != ownerID || worker.OwnerSubjectID != ownerID ||
		strings.TrimSpace(runtime.WorkerID) == "" || runtime.WorkerID != worker.ID {
		return fmt.Errorf("%w: canonical runtime identity does not match worker authority", controlplane.ErrConflict)
	}
	if currentStackID := strings.TrimSpace(runtime.StackID); currentStackID != "" && currentStackID != stackID {
		return fmt.Errorf("%w: canonical runtime is already assigned to another stack", controlplane.ErrConflict)
	}
	switch serverregistry.LifecycleState(strings.ToLower(strings.TrimSpace(runtime.LifecycleState))) {
	case serverregistry.LifecycleDecommissioning, serverregistry.LifecycleDecommissioned:
		return fmt.Errorf("%w: decommissioned canonical runtime cannot be assigned", controlplane.ErrConflict)
	}
	leaseID := strings.TrimSpace(runtimeLeaseIDFromMetadata(worker.Capabilities))
	if leaseID != "" && strings.TrimSpace(runtime.LeaseID) != leaseID {
		return fmt.Errorf("%w: canonical runtime lease does not match worker", controlplane.ErrConflict)
	}
	return nil
}

func (h stackOperationsRouteHandlers) ownedStack(e *httpx.Event) (*core.Record, string, error) {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return nil, "", authErr
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return nil, "", httpx.NewBadRequestError("Stack ID is required", nil)
	}
	stack, err := h.app.FindRecordById("stacks", stackID)
	if err != nil {
		return nil, "", httpx.NewNotFoundError("Stack not found", nil)
	}
	// An explicit hosted tenant is authoritative even while the operations
	// read model is falling back to PocketBase during the migration window.
	// Owner subjects may belong to more than one organization, so owner_id
	// alone must never make another tenant's legacy stack visible. Tenantless
	// legacy rows also stay fail-closed until the startup backfill assigns
	// their durable tenant boundary.
	if tenantID := requestExplicitTenantID(e); tenantID != "" && strings.TrimSpace(stack.GetString("tenant_id")) != tenantID {
		return nil, "", httpx.NewNotFoundError("Stack not found", nil)
	}
	if stack.GetString("owner_id") != ownerID {
		return nil, "", httpx.NewForbiddenError("Not your stack", nil)
	}
	// A soft-deleted stack must read as gone, exactly like the control-plane
	// store's "deleted_at IS NULL" filter. Without this the operations endpoint
	// keeps serving a pruned stack (a ghost that blocks a clean re-create).
	if !stack.GetDateTime("deleted_at").IsZero() {
		return nil, "", httpx.NewNotFoundError("Stack not found", nil)
	}
	return stack, ownerID, nil
}

func (h stackOperationsRouteHandlers) ownedStacks(ownerID string) ([]*core.Record, error) {
	records, err := h.app.FindRecordsByFilter(
		"stacks",
		"owner_id = {:ownerId}",
		"-updated",
		100,
		0,
		map[string]any{"ownerId": ownerID},
	)
	if err != nil {
		return nil, err
	}
	// Exclude soft-deleted stacks, matching the control-plane store's
	// "deleted_at IS NULL" filter. Filtered in Go because PocketBase's
	// null-datetime filter syntax is inconsistent across field types.
	live := make([]*core.Record, 0, len(records))
	for _, r := range records {
		if r.GetDateTime("deleted_at").IsZero() {
			live = append(live, r)
		}
	}
	return live, nil
}

func (h stackOperationsRouteHandlers) ownedControlPlaneStack(e *httpx.Event) (*controlplane.Stack, string, string, error) {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return nil, "", "", authErr
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return nil, "", "", httpx.NewBadRequestError("Stack ID is required", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	stack, err := h.stackStore.GetStack(e.Request.Context(), tenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil, "", "", httpx.NewNotFoundError("Stack not found", nil)
		}
		return nil, "", "", httpx.NewInternalServerError("Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return nil, "", "", httpx.NewForbiddenError("Not your stack", nil)
	}
	return stack, ownerID, tenantID, nil
}

func (h stackOperationsRouteHandlers) ownedControlPlaneStacks(ctx context.Context, tenantID, ownerID string) ([]controlplane.Stack, error) {
	stacks, err := h.stackStore.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]controlplane.Stack, 0, len(stacks))
	for _, stack := range stacks {
		if stack.OwnerSubjectID == ownerID {
			out = append(out, stack)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func stackListItemFromControlPlane(stack *controlplane.Stack) map[string]any {
	if stack == nil {
		return map[string]any{}
	}
	runtime := stack.RuntimeSummary
	config := stack.Config
	catalogRef := normalizeRegistryStackKitFoundation(firstNonEmptyString(
		stringFromAnyMap(runtime, "stackkit_catalog_ref"),
		stringFromAnyMap(config, "stackkit_catalog_ref"),
		stringFromAnyMap(config, "stackkit"),
	))
	return map[string]any{
		"id":                              stack.ID,
		"name":                            stack.Name,
		"mode":                            stack.Mode,
		"status":                          stack.Status,
		"state":                           stack.Status,
		"runtime_phase":                   firstNonEmptyString(stringFromAnyMap(runtime, "runtime_phase"), stringFromAnyMap(config, "runtime_phase")),
		"server_mode":                     firstNonEmptyString(stringFromAnyMap(runtime, "server_mode"), stringFromAnyMap(config, "server_mode")),
		"runtime_lane":                    firstNonEmptyString(stringFromAnyMap(runtime, "runtime_lane"), stringFromAnyMap(config, "runtime_lane")),
		"runtime_offering_id":             firstNonEmptyString(stringFromAnyMap(runtime, "runtime_offering_id"), stringFromAnyMap(config, "runtime_offering_id")),
		"lease_provider":                  firstNonEmptyString(stringFromAnyMap(runtime, "lease_provider"), stringFromAnyMap(config, "lease_provider")),
		"provider_region":                 firstNonEmptyString(stringFromAnyMap(runtime, "provider_region"), stringFromAnyMap(config, "provider_region")),
		"ionos_datacenter":                firstNonEmptyString(stringFromAnyMap(runtime, "ionos_datacenter"), stringFromAnyMap(config, "ionos_datacenter")),
		"lease_id":                        firstNonEmptyString(stringFromAnyMap(runtime, "lease_id"), stringFromAnyMap(config, "lease_id")),
		"simulate_provider_id":            firstNonEmptyString(stringFromAnyMap(runtime, "simulate_provider_id"), stringFromAnyMap(config, "simulate_provider_id")),
		"simulate_node_lifecycle":         firstNonEmptyString(stringFromAnyMap(runtime, "simulate_node_lifecycle"), stringFromAnyMap(config, "simulate_node_lifecycle")),
		"desired_state":                   firstNonEmptyString(stringFromAnyMap(runtime, "desired_state"), stringFromAnyMap(config, "desired_state")),
		"billing_mode":                    firstNonEmptyString(stringFromAnyMap(runtime, "billing_mode"), stringFromAnyMap(config, "billing_mode")),
		"billing_cadence":                 firstNonEmptyString(stringFromAnyMap(runtime, "billing_cadence"), stringFromAnyMap(config, "billing_cadence")),
		"catalog_ref":                     catalogRef,
		"stackkit_catalog_ref":            catalogRef,
		"verification_status":             firstNonEmptyString(stringFromAnyMap(runtime, "verification_status"), stringFromAnyMap(config, "verification_status")),
		"server_provisioning_mode":        firstNonEmptyString(stringFromAnyMap(runtime, "server_provisioning_mode"), stringFromAnyMap(config, "server_provisioning_mode")),
		"server_connection_mode":          firstNonEmptyString(stringFromAnyMap(runtime, "server_connection_mode"), stringFromAnyMap(config, "server_connection_mode")),
		"server_remote_host_present":      boolFromAnyMap(runtime, config, "server_remote_host_present"),
		"server_remote_user_present":      boolFromAnyMap(runtime, config, "server_remote_user_present"),
		"server_remote_auth_method":       firstNonEmptyString(stringFromAnyMap(runtime, "server_remote_auth_method"), stringFromAnyMap(config, "server_remote_auth_method")),
		"server_remote_credential_ref":    firstNonEmptyString(stringFromAnyMap(runtime, "server_remote_credential_ref"), stringFromAnyMap(config, "server_remote_credential_ref")),
		"server_remote_use_sudo":          boolFromAnyMap(runtime, config, "server_remote_use_sudo"),
		"server_install_command_required": boolFromAnyMap(runtime, config, "server_install_command_required"),
		"created":                         formatTime(stack.CreatedAt),
		"updated":                         formatTime(stack.UpdatedAt),
	}
}

func (h stackOperationsRouteHandlers) stackJobs(stackID string) []monitorCockpitJob {
	jobs, err := h.app.FindRecordsByFilter(
		"jobs",
		"stack_id = {:stackId}",
		"-updated",
		20,
		0,
		map[string]any{"stackId": stackID},
	)
	if err != nil {
		return []monitorCockpitJob{}
	}
	result := make([]monitorCockpitJob, 0, len(jobs))
	for _, job := range jobs {
		state, waitReason, nextResumeAt := apiJobWaitProjection(
			firstNonEmptyString(job.GetString("state"), "pending"),
			job.Get("result"),
		)
		resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
		result = append(result, monitorCockpitJob{
			ID:                job.Id,
			Type:              job.GetString("type"),
			State:             state,
			Progress:          job.GetInt("progress"),
			Step:              job.GetString("step"),
			Message:           job.GetString("message"),
			Error:             firstNonEmptyString(job.GetString("error"), job.GetString("error_message")),
			WaitReason:        waitReason,
			NextResumeAt:      nextResumeAt,
			ResumeAvailableAt: resumeAvailableAt,
			ResumeAvailable:   resumeAvailable,
			CreatedAt:         job.GetDateTime("created").String(),
			UpdatedAt:         job.GetDateTime("updated").String(),
		})
	}
	return result
}

func (h stackOperationsRouteHandlers) stackJobsFromStore(ctx context.Context, tenantID, stackID string) []monitorCockpitJob {
	jobs, _ := h.stackJobsAndFailureFromStore(ctx, tenantID, stackID)
	return jobs
}

// stackJobsAndFailureFromStore reads the stack's recent jobs once and derives
// both projections callers need from that single snapshot.
func (h stackOperationsRouteHandlers) stackJobsAndFailureFromStore(
	ctx context.Context, tenantID, stackID string,
) ([]monitorCockpitJob, *stackLatestFailure) {
	if h.jobStore == nil {
		return []monitorCockpitJob{}, nil
	}
	jobs, err := h.jobStore.ListJobsByStack(ctx, tenantID, stackID, 20)
	if err != nil {
		return []monitorCockpitJob{}, nil
	}
	result := make([]monitorCockpitJob, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, monitorCockpitJobFromStore(job))
	}
	return result, latestStackFailureFromJobs(jobs)
}

func (h stackOperationsRouteHandlers) latestStackFailureFromStore(ctx context.Context, tenantID, stackID string) *stackLatestFailure {
	if h.jobStore == nil {
		return nil
	}
	jobs, err := h.jobStore.ListJobsByStack(ctx, tenantID, stackID, 20)
	if err != nil {
		return nil
	}
	return latestStackFailureFromJobs(jobs)
}

// latestStackFailureFromLegacyJobs applies the same "current attempt" rule to
// the PocketBase lane. Without it the legacy readiness message could never name
// the operation that failed, and a failed teardown would read like a failed
// rollout - the exact confusion the typed message exists to remove. The legacy
// records carry no lease or runtime diagnostics, so the projection stays thin.
func latestStackFailureFromLegacyJobs(jobs []monitorCockpitJob) *stackLatestFailure {
	if len(jobs) == 0 || !isFailedJobState(jobs[0].State) {
		return nil
	}
	job := jobs[0]
	return &stackLatestFailure{
		JobID:     job.ID,
		Type:      job.Type,
		State:     job.State,
		Step:      job.Step,
		Message:   job.Message,
		Error:     job.Error,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}
}

func latestStackFailureFromJobs(jobs []controlplane.Job) *stackLatestFailure {
	// ListJobsByStack is newest-first. "Latest failure" is tied to the current
	// attempt PER OPERATION: a newer attempt at the SAME operation supersedes
	// an older failure of it, but a job of a different type does not.
	//
	// Only comparing jobs[0] made any later job of any type erase the failure.
	// A stack whose rollout failed and that then merely registered another
	// server ("update", completed) kept status=error while reporting no
	// failure at all, so the dashboard could only say "the last operation
	// failed, open the latest job" — and the latest job was the successful
	// one. That dead end is what this per-type rule removes.
	supersededTypes := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		jobType := strings.ToLower(strings.TrimSpace(job.Type))
		if !isFailedJobState(job.State) {
			supersededTypes[jobType] = struct{}{}
			continue
		}
		if _, superseded := supersededTypes[jobType]; superseded {
			continue
		}
		return stackLatestFailureFromJob(job)
	}
	return nil
}

func (h stackOperationsRouteHandlers) latestStackRuntimeLifecycleFromStore(ctx context.Context, tenantID, stackID string) (*monitorCockpitJob, *stackRuntimeLifecycle) {
	if h.jobStore == nil {
		return nil, nil
	}
	jobs, err := h.jobStore.ListJobsByStack(ctx, tenantID, stackID, 20)
	if err != nil || len(jobs) == 0 {
		return nil, nil
	}
	job := jobs[0]
	current := monitorCockpitJobFromStore(job)
	return &current, stackRuntimeLifecycleFromResult(job.Result)
}

func monitorCockpitJobFromStore(job controlplane.Job) monitorCockpitJob {
	state, waitReason, nextResumeAt := apiJobWaitProjection(
		firstNonEmptyString(job.State, "pending"),
		job.Result,
	)
	resumeAvailableAt, resumeAvailable := apiJobResumeAvailability(waitReason, nextResumeAt, time.Now().UTC())
	return monitorCockpitJob{
		ID:                job.ID,
		Type:              job.Type,
		State:             state,
		Progress:          job.Progress,
		Step:              job.Step,
		Message:           job.Message,
		Error:             job.Error,
		WaitReason:        waitReason,
		NextResumeAt:      nextResumeAt,
		ResumeAvailableAt: resumeAvailableAt,
		ResumeAvailable:   resumeAvailable,
		CreatedAt:         formatTime(job.CreatedAt),
		UpdatedAt:         formatTime(job.UpdatedAt),
	}
}

func stackRuntimeLifecycleFromResult(result map[string]any) *stackRuntimeLifecycle {
	raw, ok := result["runtime_lifecycle"].(map[string]any)
	if !ok || stringFromAnyMap(raw, "version") != "techstack.runtime-lifecycle/v1" {
		return nil
	}
	lifecycle := &stackRuntimeLifecycle{
		Version:            stringFromAnyMap(raw, "version"),
		CurrentPhase:       stringFromAnyMap(raw, "current_phase"),
		LastSafeCheckpoint: stringFromAnyMap(raw, "last_safe_checkpoint"),
		Phases:             []stackRuntimeLifecyclePhase{},
	}
	phases, _ := raw["phases"].([]any)
	for _, item := range phases {
		phase, ok := item.(map[string]any)
		if !ok || stringFromAnyMap(phase, "id") == "" {
			continue
		}
		evidence, _ := phase["evidence"].(map[string]any)
		lifecycle.Phases = append(lifecycle.Phases, stackRuntimeLifecyclePhase{
			ID:         stringFromAnyMap(phase, "id"),
			Status:     firstNonEmptyString(stringFromAnyMap(phase, "status"), "pending"),
			Message:    stringFromAnyMap(phase, "message"),
			ObservedAt: stringFromAnyMap(phase, "observed_at"),
			Evidence:   evidence,
		})
	}
	return lifecycle
}

func isFailedJobState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func stackLatestFailureFromJob(job controlplane.Job) *stackLatestFailure {
	result := job.Result
	targetBootstrap := sanitizedTargetBootstrap(result["target_bootstrap"])
	runtimeDiagnostics := sanitizedRuntimeDiagnostics(result["runtime_diagnostics"])
	failure := &stackLatestFailure{
		JobID:                job.ID,
		Type:                 job.Type,
		State:                firstNonEmptyString(job.State, "failed"),
		Step:                 job.Step,
		Message:              job.Message,
		Error:                job.Error,
		LeaseID:              firstNonEmptyString(stringFromAnyMap(result, "lease_id"), stringFromAnyMap(result, "runtime_lease_id")),
		RuntimeIP:            firstNonEmptyString(stringFromAnyMap(result, "runtime_public_ip"), stringFromAnyMap(result, "public_ip")),
		RuntimePhase:         stringFromAnyMap(result, "runtime_phase"),
		TargetBootstrap:      targetBootstrap,
		RuntimeDiagnostics:   runtimeDiagnostics,
		DiagnosticsAvailable: len(runtimeDiagnostics) > 0,
		CreatedAt:            formatTime(job.CreatedAt),
		UpdatedAt:            formatTime(job.UpdatedAt),
	}
	// A target-bootstrap receipt is recorded on every managed rollout, success
	// included. Its reason may describe the failure only when the bootstrap
	// itself failed; anything else would mask the real cause (e.g. a rollout
	// action error) behind a success receipt like target_bootstrap_not_applicable.
	bootstrapReason := ""
	bootstrapMessage := ""
	if stringFromAnyMap(targetBootstrap, "status") == "failed" {
		bootstrapReason = stringFromAnyMap(targetBootstrap, "reason_code")
		bootstrapMessage = stringFromAnyMap(targetBootstrap, "message")
	}
	failure.Reason = firstNonEmptyString(
		bootstrapReason,
		stringFromAnyMap(runtimeDiagnostics, "reason"),
		stringFromAnyMap(result, "failure_class"),
		job.Error,
	)
	if failure.Message == "" {
		failure.Message = firstNonEmptyString(
			bootstrapMessage,
			stringFromAnyMap(runtimeDiagnostics, "error"),
		)
	}
	return failure
}

func sanitizedTargetBootstrap(value any) map[string]any {
	raw, ok := mapFromJSONAny(value)
	if !ok {
		return nil
	}
	result := map[string]any{}
	for _, key := range []string{"status", "reason_code", "message", "duration_ms", "attempts"} {
		if v, exists := raw[key]; exists {
			result[key] = v
		}
	}
	if snippet := strings.TrimSpace(stringFromAnyMap(raw, "output_snippet")); snippet != "" {
		result["output_available"] = true
		result["output_hint"] = truncateString(snippet, 320)
	}
	return result
}

func sanitizedRuntimeDiagnostics(value any) map[string]any {
	raw, ok := mapFromJSONAny(value)
	if !ok {
		return nil
	}
	result := map[string]any{}
	for _, key := range []string{"status", "reason", "action", "error", "job_id", "stack_id", "elapsed_before_collection_ms"} {
		if v, exists := raw[key]; exists {
			result[key] = v
		}
	}
	if commands, ok := raw["commands"].([]any); ok {
		result["commands"] = len(commands)
	} else if commands, ok := raw["commands"].([]map[string]any); ok {
		result["commands"] = len(commands)
	}
	return result
}

func annotateManagedRuntimeServersWithFailure(servers []stackOperationServer, failure *stackLatestFailure) []stackOperationServer {
	if failure == nil {
		return servers
	}
	for i := range servers {
		if servers[i].Source != managedRuntimeInventorySource {
			continue
		}
		if failure.LeaseID != "" && servers[i].LeaseID != "" && failure.LeaseID != servers[i].LeaseID {
			continue
		}
		if servers[i].Capabilities == nil {
			servers[i].Capabilities = map[string]any{}
		}
		servers[i].Capabilities["last_job_id"] = failure.JobID
		servers[i].Capabilities["last_step"] = failure.Step
		servers[i].Capabilities["failure_reason"] = failure.Reason
		servers[i].Capabilities["diagnostics_available"] = failure.DiagnosticsAvailable
		servers[i].Health.Notes = appendUniqueString(servers[i].Health.Notes, "latest rollout failure available")
	}
	return servers
}

func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stackListItemFromRecord(stack *core.Record) map[string]any {
	catalogRef := normalizeRegistryStackKitFoundation(stack.GetString("stackkit_catalog_ref"))
	return map[string]any{
		"id":                              stack.Id,
		"name":                            stack.GetString("name"),
		"mode":                            stack.GetString("mode"),
		"status":                          stack.GetString("status"),
		"state":                           stack.GetString("status"),
		"runtime_phase":                   stack.GetString("runtime_phase"),
		"server_mode":                     stack.GetString("server_mode"),
		"runtime_lane":                    stack.GetString("runtime_lane"),
		"runtime_offering_id":             stack.GetString("runtime_offering_id"),
		"lease_provider":                  stack.GetString("lease_provider"),
		"provider_region":                 stack.GetString("provider_region"),
		"ionos_datacenter":                stack.GetString("ionos_datacenter"),
		"lease_id":                        stack.GetString("lease_id"),
		"simulate_provider_id":            stack.GetString("simulate_provider_id"),
		"simulate_node_lifecycle":         stack.GetString("simulate_node_lifecycle"),
		"desired_state":                   stack.GetString("desired_state"),
		"billing_mode":                    stack.GetString("billing_mode"),
		"billing_cadence":                 stack.GetString("billing_cadence"),
		"catalog_ref":                     catalogRef,
		"stackkit_catalog_ref":            catalogRef,
		"verification_status":             stack.GetString("verification_status"),
		"server_provisioning_mode":        stack.GetString("server_provisioning_mode"),
		"server_connection_mode":          stack.GetString("server_connection_mode"),
		"server_remote_host_present":      stack.GetBool("server_remote_host_present"),
		"server_remote_user_present":      stack.GetBool("server_remote_user_present"),
		"server_remote_auth_method":       stack.GetString("server_remote_auth_method"),
		"server_remote_credential_ref":    stack.GetString("server_remote_credential_ref"),
		"server_remote_use_sudo":          stack.GetBool("server_remote_use_sudo"),
		"server_install_command_required": stack.GetBool("server_install_command_required"),
		"created":                         stack.GetString("created"),
		"updated":                         stack.GetString("updated"),
	}
}

func (h stackOperationsRouteHandlers) operationServers(ctx context.Context, ownerID, tenantID, explicitTenantID, stackID string) ([]stackOperationServer, error) {
	managedRuntimeItems, err := projectManagedRuntimeLeasesChecked(ctx, h.managedRuntimeLeases, tenantID, ownerID, stackID)
	if err != nil {
		return nil, err
	}
	servers := []stackOperationServer{}
	hasStackRegistryNode := false
	if h.registryStore != nil {
		nodes, err := h.registryStore.ListNodesByStack(ctx, tenantID, stackID)
		if err == nil {
			for _, node := range nodes {
				servers = append(servers, h.operationServerFromControlPlaneNode(ctx, stackID, node))
			}
			hasStackRegistryNode = len(nodes) > 0
		}
	}
	workerFilter := "owner_id = {:ownerId} && (stack_id = {:stackId} || stack_id = '')"
	workerParams := map[string]any{"ownerId": ownerID, "stackId": stackID}
	if explicitTenantID = strings.TrimSpace(explicitTenantID); explicitTenantID != "" {
		// Unassigned workers are not stack children yet. In hosted mode their
		// tenant binding must therefore be applied explicitly; owner_id alone is
		// not an isolation boundary for a subject that belongs to multiple orgs.
		workerFilter = "tenant_id = {:tenantId} && " + workerFilter
		workerParams["tenantId"] = explicitTenantID
	}
	records, err := h.app.FindRecordsByFilter(
		"workers",
		workerFilter,
		"hostname",
		200,
		0,
		workerParams,
	)
	if err != nil {
		return servers, nil
	}

	for _, record := range records {
		if hasStackRegistryNode && strings.TrimSpace(record.GetString("stack_id")) == stackID {
			continue
		}
		servers = append(servers, h.operationServerFromWorker(ctx, stackID, record))
	}
	if !hasStackRegistryNode {
		for _, item := range managedRuntimeItems {
			server := stackServerFromManagedRuntime(item)
			h.applyManagedRuntimeMetrics(ctx, &server)
			servers = append(servers, server)
		}
	}
	applyManagedRuntimeAuthorityToServers(servers, managedRuntimeItems)
	servers = dedupeOperationServers(servers)
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Assignment != servers[j].Assignment {
			return servers[i].Assignment < servers[j].Assignment
		}
		return strings.ToLower(servers[i].Hostname) < strings.ToLower(servers[j].Hostname)
	})
	return servers, nil
}

func (h stackOperationsRouteHandlers) operationServersFromStore(ctx context.Context, ownerID, tenantID, stackID string) ([]stackOperationServer, []stackCustodyLease, error) {
	allManagedItems, err := projectManagedRuntimeLeasesChecked(ctx, h.managedRuntimeLeases, tenantID, ownerID, stackID)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := h.canonicalOperationServers(ctx, ownerID, tenantID, stackID)
	if err != nil {
		return nil, nil, err
	}
	// A lease only becomes a server with positive machine evidence. Everything
	// else is reported separately so a deleted VM cannot keep presenting as
	// infrastructure.
	managedRuntimeItems, custodyLeases := splitManagedRuntimeLeasesByMachineEvidence(allManagedItems, canonical)
	servers := []stackOperationServer{}
	hasStackRegistryNode := false
	if h.registryStore != nil {
		nodes, err := h.registryStore.ListNodesByStack(ctx, tenantID, stackID)
		if err == nil {
			for _, node := range nodes {
				servers = append(servers, h.operationServerFromControlPlaneNode(ctx, stackID, node))
			}
			hasStackRegistryNode = len(nodes) > 0
		}
	}
	if h.workerStore == nil {
		servers = h.appendManagedRuntimeLeaseProjections(ctx, servers, managedRuntimeItems, !hasStackRegistryNode)
		return h.finalizeOperationServers(ctx, tenantID, stackID, canonical, servers, managedRuntimeItems), custodyLeases, nil
	}
	servers = h.appendLegacyWorkerServers(ctx, servers, ownerID, tenantID, stackID, hasStackRegistryNode)
	servers = h.appendManagedRuntimeLeaseProjections(ctx, servers, managedRuntimeItems, !hasStackRegistryNode)
	return h.finalizeOperationServers(ctx, tenantID, stackID, canonical, servers, managedRuntimeItems), custodyLeases, nil
}

// appendLegacyWorkerServers adds the legacy control-plane worker rows and
// re-projects registry-node snapshots from the live worker heartbeat, so a
// formerly connected node cannot stay green forever after its agent stops.
func (h stackOperationsRouteHandlers) appendLegacyWorkerServers(
	ctx context.Context,
	servers []stackOperationServer,
	ownerID, tenantID, stackID string,
	hasStackRegistryNode bool,
) []stackOperationServer {
	workers, err := h.workerStore.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		return servers
	}
	workersByID := map[string]*controlplane.Worker{}
	for i := range workers {
		worker := &workers[i]
		if worker.OwnerSubjectID != ownerID {
			continue
		}
		workerStackID := strings.TrimSpace(worker.StackID)
		if workerStackID != "" && workerStackID != stackID {
			continue
		}
		workersByID[worker.ID] = worker
		if hasStackRegistryNode && workerStackID == stackID {
			continue
		}
		servers = append(servers, h.operationServerFromControlPlaneWorker(ctx, ownerID, stackID, worker))
	}
	for index := range servers {
		server := &servers[index]
		if server.Source != "registry-store" {
			continue
		}
		h.applyRegistryServerHeartbeat(server, workersByID[server.AgentID])
	}
	return servers
}

// splitManagedRuntimeLeasesByMachineEvidence separates leases that back a real
// machine from custody-only records. A canonical server row bound to the lease
// is the strongest evidence; anything else must satisfy the lease-side
// evidence rule.
func splitManagedRuntimeLeasesByMachineEvidence(
	items []managedRuntimeInventoryItem,
	canonical []stackOperationServer,
) ([]managedRuntimeInventoryItem, []stackCustodyLease) {
	canonicalLeases := make(map[string]struct{}, len(canonical))
	for _, server := range canonical {
		if leaseID := strings.TrimSpace(server.LeaseID); leaseID != "" &&
			server.Capabilities["lifecycle_state"] != string(serverregistry.LifecycleDecommissioned) {
			canonicalLeases[leaseID] = struct{}{}
		}
	}
	backed := make([]managedRuntimeInventoryItem, 0, len(items))
	custody := []stackCustodyLease{}
	for _, item := range items {
		if item.CustodyResolved {
			continue
		}
		_, hasCanonical := canonicalLeases[item.LeaseID]
		if isMachine, reason := managedRuntimeLeaseMachineEvidence(item, hasCanonical); isMachine {
			backed = append(backed, item)
		} else {
			custody = append(custody, stackCustodyLease{
				LeaseID:        item.LeaseID,
				ServerID:       item.ID,
				Label:          item.Hostname,
				Provider:       item.Provider,
				Reason:         reason,
				Status:         item.Status,
				LastKnownIP:    item.IP,
				UpdatedAt:      item.UpdatedAt,
				AllowedActions: managedRuntimeCustodyActions(item),
			})
		}
	}
	return backed, custody
}

func managedRuntimeCustodyActions(item managedRuntimeInventoryItem) []string {
	if item.NativeActive && item.ExecutionAuthority == string(vmleases.LeaseExecutionAuthorityTechStackProviderControl) {
		return []string{"decommission"}
	}
	if item.ExecutionAuthority != string(vmleases.LeaseExecutionAuthorityTechStackProviderControl) {
		return []string{"resolve_custody"}
	}
	return nil
}

func isDestroyFailure(jobType string) bool {
	switch strings.ToLower(strings.TrimSpace(jobType)) {
	case "destroy", "decommission":
		return true
	default:
		return false
	}
}

func activeStackFailure(failure *stackLatestFailure, servers []stackOperationServer, custody []stackCustodyLease) *stackLatestFailure {
	if failure != nil && isDestroyFailure(failure.Type) && len(servers) == 0 && len(custody) == 0 {
		return nil
	}
	return failure
}

// A failed destroy remains useful history, but it is no longer the current
// lifecycle state once the exact custody records have been resolved and no
// runtime server remains. Keep the persisted job intact while projecting the
// actionable state the owner sees today.
func reconcileResolvedDestroyReadiness(
	readiness stackReadiness,
	recordedFailure, activeFailure *stackLatestFailure,
	servers []stackOperationServer,
	custody []stackCustodyLease,
) stackReadiness {
	if recordedFailure == nil || activeFailure != nil || !isDestroyFailure(recordedFailure.Type) || len(servers) != 0 || len(custody) != 0 {
		return readiness
	}
	readiness.Status = "waiting_for_server"
	readiness.CanStart = false
	readiness.ReviewRequired = false
	readiness.Message = "No server is currently connected. Add a local server, connect your own server or VPS, or create a Managed VPS to continue."
	return readiness
}

func (h stackOperationsRouteHandlers) finalizeOperationServers(ctx context.Context, tenantID, stackID string, canonical, projections []stackOperationServer, managedRuntimeItems []managedRuntimeInventoryItem) []stackOperationServer {
	servers := mergeCanonicalOperationServers(canonical, projections)
	markNonCanonicalOperationServerHealthUnverified(servers)
	// Execution authority is an immutable lease boundary. Apply it after the
	// canonical merge so a fresh Guard row cannot make a quarantined lease
	// actionable again.
	applyManagedRuntimeAuthorityToServers(servers, managedRuntimeItems)
	servers = h.enrichOperationServers(ctx, tenantID, stackID, servers)
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Assignment != servers[j].Assignment {
			return servers[i].Assignment < servers[j].Assignment
		}
		return strings.ToLower(servers[i].Hostname) < strings.ToLower(servers[j].Hostname)
	})
	return servers
}

func markNonCanonicalOperationServerHealthUnverified(servers []stackOperationServer) {
	for index := range servers {
		server := &servers[index]
		if server.Source == "canonical-server" {
			continue
		}
		server.Health.State = "unknown"
		server.Health.CPUPercent = metricUnknown("%")
		server.Health.MemoryPercent = metricUnknown("%")
		server.Health.DiskPercent = metricUnknown("%")
		server.Health.UptimeSeconds = metricUnknown("s")
		server.Health.Notes = appendUniqueString(server.Health.Notes, "current health requires canonical Guard evidence")
	}
}

// canonicalServerPreCheckState derives a truthful pre-check projection from
// the runtime state instead of a perpetual "pending" literal: provider-managed
// servers are vetted by the lease lane, an active Guard enrollment has passed
// its checks, and terminal lifecycle states are reported as such.
func canonicalServerPreCheckState(runtime controlplane.ServerRuntime) string {
	if strings.TrimSpace(runtime.LeaseID) != "" {
		return managedRuntimePreCheckManaged
	}
	switch runtime.LifecycleState {
	case string(serverregistry.LifecycleActive):
		return "passed"
	case string(serverregistry.LifecycleFailed):
		return "failed"
	case string(serverregistry.LifecycleDecommissioning), string(serverregistry.LifecycleDecommissioned):
		return "not_applicable"
	default:
		return "pending"
	}
}

func (h stackOperationsRouteHandlers) canonicalOperationServers(ctx context.Context, ownerID, tenantID, stackID string) ([]stackOperationServer, error) {
	if h.serverStore == nil {
		return nil, nil
	}
	runtimes, err := h.serverStore.ListServerRuntimesByTenant(ctx, tenantID, stackID)
	if err != nil {
		return nil, err
	}
	observedAt := time.Now().UTC()
	servers := make([]stackOperationServer, 0, len(runtimes))
	for _, runtime := range runtimes {
		if runtime.OwnerSubjectID != ownerID {
			continue
		}
		// The operations dashboard is the current, actionable inventory. A
		// decommissioned aggregate remains available through /api/v1/servers/{id}
		// and its transition history, but must not keep contributing a server
		// card, KPI, or readiness input after lifecycle completion.
		if runtime.LifecycleState == string(serverregistry.LifecycleDecommissioned) {
			continue
		}
		// Persisted canonical state is the truth. The registry sweeper is the
		// demotion authority: heartbeat freshness becomes a durable
		// connection/health write through ApplyServerEvent, so the cockpit,
		// /api/v1/servers, the transitions log, and the aggregate head all
		// report the same state (kombify-Techstack-nzy1.4, builds on #577).
		connection, health := runtime.ConnectionState, runtime.HealthState
		lastSeen := ""
		var heartbeatAt *time.Time
		if runtime.LastHeartbeatAt != nil {
			heartbeatObservedAt := runtime.LastHeartbeatAt.UTC()
			heartbeatAt = &heartbeatObservedAt
			lastSeen = heartbeatObservedAt.Format(time.RFC3339Nano)
		}
		dashboardHealth := health
		if connection == string(serverregistry.ConnectionStale) || connection == string(serverregistry.ConnectionOffline) {
			dashboardHealth = connection
		}
		server := stackOperationServer{
			ID: runtime.ID, ServerID: runtime.ID, Hostname: firstNonEmptyString(runtime.Name, runtime.ID), Role: "primary",
			Status: connection, Assignment: "stack", TechstackID: runtime.StackID, AgentID: runtime.WorkerID,
			LastSeen: lastSeen, Approved: runtime.LifecycleState == string(serverregistry.LifecycleActive),
			PreCheck: canonicalServerPreCheckState(runtime), Source: "canonical-server", LeaseID: runtime.LeaseID,
			DesiredState: runtime.DesiredState, Assignable: false,
			Capabilities: map[string]any{
				"lifecycle_state": runtime.LifecycleState, "connection_state": connection,
				"health_state": health, "reason_code": runtime.ReasonCode,
				"inventory_revision": runtime.InventoryRevision, "channels": runtime.Channels,
				"provider": runtime.ProviderRef, "worker_id": runtime.WorkerID,
			},
			Health: stackServerHealth{
				State: dashboardHealth, Source: "canonical-server", UpdatedAt: lastSeen,
				CPUPercent: metricUnknown("%"), MemoryPercent: metricUnknown("%"),
				DiskPercent: metricUnknown("%"), UptimeSeconds: metricUnknown("s"),
			},
			heartbeatAt: heartbeatAt,
			observedAt:  observedAt,
		}
		applyServerInventoryMetadata(&server, runtime.Metadata, "canonical-server")
		servers = append(servers, server)
	}
	return servers, nil
}

func mergeCanonicalOperationServers(canonical, legacy []stackOperationServer) []stackOperationServer {
	if len(canonical) == 0 {
		return dedupeOperationServers(legacy)
	}
	result := append([]stackOperationServer{}, canonical...)
	for _, candidate := range legacy {
		representedAt := -1
		for index, runtime := range canonical {
			if sameOperationServerIdentity(candidate, runtime) {
				representedAt = index
				break
			}
		}
		if representedAt < 0 {
			result = append(result, candidate)
			continue
		}
		mergeOperationServerFallback(&result[representedAt], candidate)
	}
	return dedupeOperationServers(result)
}

func dedupeOperationServers(servers []stackOperationServer) []stackOperationServer {
	result := make([]stackOperationServer, 0, len(servers))
	for _, candidate := range servers {
		representedAt := -1
		for index := range result {
			if sameOperationServerIdentity(result[index], candidate) {
				representedAt = index
				break
			}
		}
		if representedAt < 0 {
			candidate.ServerID = operationServerCanonicalID(candidate)
			candidate.LeaseID = operationServerLeaseID(candidate)
			result = append(result, candidate)
			continue
		}
		mergeOperationServerFallback(&result[representedAt], candidate)
	}
	return result
}

func sameOperationServerIdentity(left, right stackOperationServer) bool {
	if left.ID != "" && left.ID == right.ID {
		return true
	}
	leftServerID, rightServerID := operationServerCanonicalID(left), operationServerCanonicalID(right)
	if leftServerID != "" && leftServerID == rightServerID {
		return true
	}
	leftLeaseID, rightLeaseID := operationServerLeaseID(left), operationServerLeaseID(right)
	if leftLeaseID != "" && leftLeaseID == rightLeaseID {
		return true
	}
	return left.AgentID != "" && left.AgentID == right.AgentID
}

func operationServerCanonicalID(server stackOperationServer) string {
	if serverID := firstNonEmptyString(server.ServerID, stringFromAnyMap(server.Capabilities, "server_id")); serverID != "" {
		return serverID
	}
	if leaseID := operationServerLeaseID(server); leaseID != "" {
		return runtimeidentity.LeaseServerID(leaseID)
	}
	if strings.HasPrefix(strings.TrimSpace(server.ID), "server_") {
		return strings.TrimSpace(server.ID)
	}
	return ""
}

func operationServerLeaseID(server stackOperationServer) string {
	return firstNonEmptyString(server.LeaseID, stringFromAnyMap(server.Capabilities, "lease_id"), stringFromAnyMap(server.Capabilities, "runtime_lease_id"))
}

// mergeOperationServerFallback keeps the first row authoritative. Callers put
// canonical/agent-observed rows first; lease projections may only fill missing
// inventory and unknown metrics.
func mergeOperationServerFallback(primary *stackOperationServer, fallback stackOperationServer) {
	if primary == nil {
		return
	}
	primary.ServerID = firstNonEmptyString(operationServerCanonicalID(*primary), operationServerCanonicalID(fallback))
	primary.LeaseID = firstNonEmptyString(operationServerLeaseID(*primary), operationServerLeaseID(fallback))
	primary.IP = firstNonEmptyString(primary.IP, fallback.IP)
	primary.HostAddresses = mergeServerAddresses(primary.HostAddresses, fallback.HostAddresses)
	primary.OS = firstNonEmptyString(primary.OS, fallback.OS)
	primary.OSVersion = firstNonEmptyString(primary.OSVersion, fallback.OSVersion)
	primary.Arch = firstNonEmptyString(primary.Arch, fallback.Arch)
	primary.Domains = mergeUniqueStrings(primary.Domains, fallback.Domains)
	primary.ServiceEndpoints = mergeServerEndpoints(primary.ServiceEndpoints, fallback.ServiceEndpoints)
	primary.StackKit = mergeServerStackKit(primary.StackKit, fallback.StackKit)
	primary.AgentID = firstNonEmptyString(primary.AgentID, fallback.AgentID)
	primary.ApprovedAt = firstNonEmptyString(primary.ApprovedAt, fallback.ApprovedAt)
	if primary.PreCheck == "" {
		primary.PreCheck = fallback.PreCheck
	}
	primary.Capabilities = mergeAnyMaps(fallback.Capabilities, primary.Capabilities)
	if primary.Health.CPUPercent.Status == "unknown" && fallback.Health.CPUPercent.Status != "unknown" {
		primary.Health.CPUPercent = fallback.Health.CPUPercent
	}
	if primary.Health.MemoryPercent.Status == "unknown" && fallback.Health.MemoryPercent.Status != "unknown" {
		primary.Health.MemoryPercent = fallback.Health.MemoryPercent
	}
	if primary.Health.DiskPercent.Status == "unknown" && fallback.Health.DiskPercent.Status != "unknown" {
		primary.Health.DiskPercent = fallback.Health.DiskPercent
	}
	if primary.Health.UptimeSeconds.Status == "unknown" && fallback.Health.UptimeSeconds.Status != "unknown" {
		primary.Health.UptimeSeconds = fallback.Health.UptimeSeconds
	}
}

func applyServerInventoryMetadata(server *stackOperationServer, metadata map[string]any, fallbackProvenance string) {
	if server == nil || len(metadata) == 0 {
		return
	}
	host, _ := mapFromJSONAny(metadata["host"])
	deployment, _ := mapFromJSONAny(metadata["deployment"])
	provenance := firstNonEmptyString(
		stringFromAnyMap(metadata, "inventory_source"),
		stringFromAnyMap(metadata, "source"),
		fallbackProvenance,
	)
	server.LeaseID = firstNonEmptyString(server.LeaseID, stringFromAnyMap(metadata, "lease_id"), stringFromAnyMap(metadata, "runtime_lease_id"))
	server.ServerID = firstNonEmptyString(server.ServerID, stringFromAnyMap(metadata, "server_id"))
	if server.ServerID == "" && server.LeaseID != "" {
		server.ServerID = runtimeidentity.LeaseServerID(server.LeaseID)
	}
	server.Hostname = firstNonEmptyString(stringFromAnyMap(host, "hostname"), server.Hostname)
	server.IP = firstNonEmptyString(
		server.IP,
		stringFromAnyMap(host, "public_ip"),
		stringFromAnyMap(host, "private_ip"),
		stringFromAnyMap(host, "local_ip"),
	)
	server.OS = firstNonEmptyString(stringFromAnyMap(host, "os"), server.OS)
	server.OSVersion = firstNonEmptyString(stringFromAnyMap(host, "os_version"), stringFromAnyMap(metadata, "os_version"), server.OSVersion)
	server.Arch = firstNonEmptyString(stringFromAnyMap(host, "arch"), server.Arch)
	server.HostAddresses = mergeServerAddresses(server.HostAddresses, inventoryServerAddresses(host, server.IP, provenance))

	if server.Capabilities == nil {
		server.Capabilities = map[string]any{}
	}
	// Agent identity and manifest provenance are what make an empty service
	// list explainable. Without them "0 services" on a healthy, connected
	// server is indistinguishable from a broken projection, and the operator
	// has nothing to act on.
	if agentVersion := strings.TrimSpace(stringFromAnyMap(metadata, "agent_version")); agentVersion != "" {
		server.Capabilities["agent_version"] = agentVersion
	}
	for _, key := range []string{"stackkit_manifest_observed", "service_discovery_observed"} {
		if observed, ok := metadata[key].(bool); ok {
			server.Capabilities[key] = observed
		}
	}
	for _, key := range []string{"cpu_cores", "ram_mb", "disk_gb", "memory_total_bytes", "disk_total_bytes"} {
		if value, ok := float64FromAny(host[key]); ok && value > 0 {
			server.Capabilities[key] = value
		}
	}
	if metric := metricFromAnyMaps("%", []string{"cpu_percent"}, host); metric.Status == "ok" {
		server.Health.CPUPercent = metric
	}
	if metric := metricPercentFromInventory(host, nil, []string{"memory_percent"}, "memory_used_bytes", "memory_total_bytes"); metric.Status == "ok" {
		server.Health.MemoryPercent = metric
	}
	if metric := metricPercentFromInventory(host, nil, []string{"disk_percent"}, "disk_used_bytes", "disk_total_bytes"); metric.Status == "ok" {
		server.Health.DiskPercent = metric
	}
	if metric := metricFromAnyMaps("s", []string{"uptime_seconds"}, host); metric.Status == "ok" {
		server.Health.UptimeSeconds = metric
	}

	server.ServiceEndpoints = mergeServerEndpoints(server.ServiceEndpoints, serverEndpointsFromAny(metadata["endpoints"], stackServerEndpoint{
		Source:     provenance,
		ObservedAt: firstNonEmptyString(stringFromAnyMap(metadata, "observed_at"), stringFromAnyMap(metadata, "inventory_observed_at")),
	}))
	server.Domains = mergeUniqueStrings(server.Domains, []string{observedDomain(firstNonEmptyString(
		stringFromAnyMap(metadata, "domain"),
		stringFromAnyMap(deployment, "domain"),
	))})
	server.Domains = mergeUniqueStrings(server.Domains, endpointDomains(server.ServiceEndpoints))

	stackKit := &stackServerStackKit{
		Name:    firstNonEmptyString(stringFromAnyMap(metadata, "stackkit"), stringFromAnyMap(deployment, "stackkit")),
		Version: firstNonEmptyString(stringFromAnyMap(metadata, "stackkit_version"), stringFromAnyMap(deployment, "stackkit_version")),
		Mode:    firstNonEmptyString(stringFromAnyMap(metadata, "stackkit_mode"), stringFromAnyMap(deployment, "stackkit_mode")),
		State:   "observed",
		Sources: []string{provenance},
	}
	if stackKit.Name != "" || stackKit.Version != "" || stackKit.Mode != "" {
		server.StackKit = mergeServerStackKit(stackKit, server.StackKit)
	}
}

func inventoryServerAddresses(host map[string]any, primary, provenance string) []stackServerAddress {
	addresses := make([]stackServerAddress, 0, 4)
	for _, item := range []struct{ scope, key string }{
		{scope: "public", key: "public_ip"},
		{scope: "private", key: "private_ip"},
		{scope: "local", key: "local_ip"},
	} {
		if address := stringFromAnyMap(host, item.key); address != "" {
			addresses = append(addresses, stackServerAddress{Address: address, Scope: item.scope, Provenance: provenance})
		}
	}
	if primary = strings.TrimSpace(primary); primary != "" {
		addresses = append(addresses, stackServerAddress{Address: primary, Scope: "primary", Provenance: provenance})
	}
	return addresses
}

func mergeServerAddresses(primary, fallback []stackServerAddress) []stackServerAddress {
	result := append([]stackServerAddress{}, primary...)
	for _, candidate := range fallback {
		candidate.Address = strings.TrimSpace(candidate.Address)
		if candidate.Address == "" {
			continue
		}
		matched := false
		for index := range result {
			if !strings.EqualFold(result[index].Address, candidate.Address) {
				continue
			}
			matched = true
			if result[index].Scope == "primary" && candidate.Scope != "" && candidate.Scope != "primary" {
				result[index].Scope = candidate.Scope
			}
			if result[index].Provenance == "" {
				result[index].Provenance = candidate.Provenance
			}
			break
		}
		if !matched {
			result = append(result, candidate)
		}
	}
	return result
}

func serverEndpointsFromAny(value any, defaults stackServerEndpoint) []stackServerEndpoint {
	items := []map[string]any{}
	switch typed := value.(type) {
	case []map[string]any:
		items = typed
	case []any:
		for _, item := range typed {
			if values, ok := mapFromJSONAny(item); ok {
				items = append(items, values)
			}
		}
	case types.JSONRaw:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return serverEndpointsFromAny(decoded, defaults)
		}
	}
	result := make([]stackServerEndpoint, 0, len(items))
	for _, item := range items {
		address := firstNonEmptyString(stringFromAnyMap(item, serviceAccessURLKey), stringFromAnyMap(item, "address"))
		parsed, ok := parseServiceURL(address)
		if !ok {
			continue
		}
		endpoint := defaults
		endpoint.URL = parsed.String()
		endpoint.Domain = observedDomain(parsed.Hostname())
		endpoint.Visibility = stringFromAnyMap(item, "visibility")
		endpoint.Health = stringFromAnyMap(item, "health")
		endpoint.Provenance = firstNonEmptyString(stringFromAnyMap(item, "provenance"), endpoint.Provenance)
		result = append(result, endpoint)
	}
	return result
}

func mergeServerEndpoints(primary, fallback []stackServerEndpoint) []stackServerEndpoint {
	result := append([]stackServerEndpoint{}, primary...)
	for _, candidate := range fallback {
		candidate.URL = strings.TrimSpace(candidate.URL)
		if candidate.URL == "" {
			continue
		}
		matched := -1
		for index := range result {
			if strings.EqualFold(result[index].URL, candidate.URL) {
				matched = index
				break
			}
		}
		if matched < 0 {
			result = append(result, candidate)
			continue
		}
		existing := &result[matched]
		existing.ServiceID = firstNonEmptyString(existing.ServiceID, candidate.ServiceID)
		existing.ServiceKey = firstNonEmptyString(existing.ServiceKey, candidate.ServiceKey)
		existing.Name = firstNonEmptyString(existing.Name, candidate.Name)
		existing.Domain = firstNonEmptyString(existing.Domain, candidate.Domain)
		existing.Visibility = firstNonEmptyString(existing.Visibility, candidate.Visibility)
		existing.Health = firstNonEmptyString(existing.Health, candidate.Health)
		existing.Provenance = firstNonEmptyString(existing.Provenance, candidate.Provenance)
		existing.Source = firstNonEmptyString(existing.Source, candidate.Source)
		existing.ObservedAt = firstNonEmptyString(existing.ObservedAt, candidate.ObservedAt)
	}
	return result
}

func endpointDomains(endpoints []stackServerEndpoint) []string {
	result := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		result = append(result, endpoint.Domain)
	}
	return result
}

func observedDomain(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, ok := parseServiceURL(value); ok {
		value = parsed.Hostname()
	} else if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || net.ParseIP(value) != nil || strings.ContainsAny(value, `/\\@`) {
		return ""
	}
	return value
}

func mergeUniqueStrings(primary, fallback []string) []string {
	result := make([]string, 0, len(primary)+len(fallback))
	seen := map[string]bool{}
	for _, values := range [][]string{primary, fallback} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func mergeServerStackKit(primary, fallback *stackServerStackKit) *stackServerStackKit {
	if primary == nil && fallback == nil {
		return nil
	}
	if primary == nil {
		copy := *fallback
		copy.Sources = mergeUniqueStrings(nil, fallback.Sources)
		return &copy
	}
	copy := *primary
	copy.Sources = mergeUniqueStrings(nil, primary.Sources)
	if fallback == nil {
		return &copy
	}
	copy.Name = firstNonEmptyString(copy.Name, fallback.Name)
	copy.CatalogRef = firstNonEmptyString(copy.CatalogRef, fallback.CatalogRef)
	copy.Version = firstNonEmptyString(copy.Version, fallback.Version)
	copy.Mode = firstNonEmptyString(copy.Mode, fallback.Mode)
	copy.Context = firstNonEmptyString(copy.Context, fallback.Context)
	copy.PaaS = firstNonEmptyString(copy.PaaS, fallback.PaaS)
	copy.ComputeTier = firstNonEmptyString(copy.ComputeTier, fallback.ComputeTier)
	if copy.State == "" || fallback.State == "observed" {
		copy.State = fallback.State
	}
	copy.Sources = mergeUniqueStrings(copy.Sources, fallback.Sources)
	return &copy
}

func metricPercentFromInventory(primary, fallback map[string]any, directKeys []string, usedKey, totalKey string) stackMetricValue {
	if metric := metricFromAnyMaps("%", directKeys, primary, fallback); metric.Status == "ok" {
		return metric
	}
	for _, values := range []map[string]any{primary, fallback} {
		used, usedOK := float64FromAny(values[usedKey])
		total, totalOK := float64FromAny(values[totalKey])
		if usedOK && totalOK && used >= 0 && total > 0 {
			return metricKnown(math.Round((used/total)*1000)/10, "%")
		}
	}
	return metricUnknown("%")
}

func (h stackOperationsRouteHandlers) enrichOperationServers(ctx context.Context, tenantID, stackID string, servers []stackOperationServer) []stackOperationServer {
	if len(servers) == 0 {
		return servers
	}
	configured := h.configuredStackKit(ctx, tenantID, stackID)
	for index := range servers {
		servers[index].StackKit = mergeServerStackKit(servers[index].StackKit, configured)
	}
	if h.serviceStore != nil {
		services, err := h.serviceStore.ListServiceRuntimes(ctx, tenantID, stackID, "")
		if err == nil {
			for _, service := range services {
				index := operationServerIndex(servers, service.ServerID)
				if index < 0 {
					continue
				}
				if address := stringFromAnyMap(service.Access, serviceAccessURLKey); address != "" {
					servers[index].ServiceEndpoints = mergeServerEndpoints(servers[index].ServiceEndpoints, serverEndpointsFromAny([]any{map[string]any{
						serviceAccessURLKey: address,
						"visibility":        serviceVisibilityFromAccess(service.Access),
						"health":            service.HealthState,
						"provenance":        service.Source,
					}}, stackServerEndpoint{
						ServiceID: service.ID, ServiceKey: service.ServiceKey, Name: service.Name,
						Source: "service-runtime", ObservedAt: formatOptionalTime(service.ObservedAt),
					}))
				}
				if version := strings.TrimSpace(service.StackKitVersion); version != "" {
					servers[index].StackKit = mergeServerStackKit(&stackServerStackKit{
						Version: version, State: "observed", Sources: []string{"service-runtime"},
					}, servers[index].StackKit)
				}
			}
		}
	}
	if h.registryStore != nil {
		services, err := h.registryStore.ListServicesByStack(ctx, tenantID, stackID)
		if err == nil {
			for _, service := range services {
				index := operationServerIndex(servers, service.NodeID)
				if index < 0 {
					continue
				}
				defaults := stackServerEndpoint{
					ServiceID: service.ID, ServiceKey: service.ServiceKey, Name: service.Name,
					Health: service.Status, Provenance: service.Source, Source: "registry-store",
					ObservedAt: firstNonEmptyString(stringFromAnyMap(service.Metadata, "observed_at"), formatTime(service.UpdatedAt)),
				}
				endpoints := serverEndpointsFromAny(service.Metadata["endpoints"], defaults)
				if service.URL != "" {
					endpoints = mergeServerEndpoints(endpoints, serverEndpointsFromAny([]any{map[string]any{
						serviceAccessURLKey: service.URL,
						"health":            service.Status,
						"provenance":        service.Source,
					}}, defaults))
				}
				servers[index].ServiceEndpoints = mergeServerEndpoints(servers[index].ServiceEndpoints, endpoints)
			}
		}
	}
	for index := range servers {
		servers[index].Domains = mergeUniqueStrings(servers[index].Domains, endpointDomains(servers[index].ServiceEndpoints))
		applyServerEndpointFreshness(&servers[index], time.Now().UTC())
	}
	return servers
}

func applyServerEndpointFreshness(server *stackOperationServer, now time.Time) {
	if server == nil {
		return
	}
	serverState := runtimehealth.ServerState(strings.ToLower(strings.TrimSpace(server.Health.State)))
	serverFresh := serverState == runtimehealth.ServerHealthy || serverState == runtimehealth.ServerDegraded
	for index := range server.ServiceEndpoints {
		endpoint := &server.ServiceEndpoints[index]
		if strings.TrimSpace(endpoint.Health) == "" {
			continue
		}
		observedRaw := strings.TrimSpace(endpoint.ObservedAt)
		observedAt, err := time.Parse(time.RFC3339Nano, observedRaw)
		age := now.Sub(observedAt)
		observationFresh := err == nil && age >= -time.Minute && age <= runtimehealth.FreshHeartbeatWindow
		if !serverFresh || !observationFresh {
			endpoint.Health = monitoringStatusUnknown
		}
	}
}

func (h stackOperationsRouteHandlers) configuredStackKit(ctx context.Context, tenantID, stackID string) *stackServerStackKit {
	if h.stackStore == nil {
		return nil
	}
	stack, err := h.stackStore.GetStack(ctx, tenantID, stackID)
	if err != nil || stack == nil {
		return nil
	}
	runtime, config := stack.RuntimeSummary, stack.Config
	runtimeMetadata, _ := mapFromJSONAny(runtime["metadata"])
	configMetadata, _ := mapFromJSONAny(config["metadata"])
	compute, _ := mapFromJSONAny(config["compute"])
	catalogRef := firstNonEmptyString(
		stringFromAnyMap(runtime, "stackkit_catalog_ref"),
		stringFromAnyMap(runtime, "stackkit_foundation"),
		stringFromAnyMap(runtime, "stackkit"),
		stringFromAnyMap(config, "stackkit_catalog_ref"),
		stringFromAnyMap(config, "stackkit_foundation"),
		stringFromAnyMap(config, "stackkit"),
		stringFromAnyMap(config, "kit"),
	)
	if catalogRef != "" {
		catalogRef = normalizeRegistryStackKitFoundation(catalogRef)
	}
	deployment := &stackServerStackKit{
		Name:       catalogRef,
		CatalogRef: catalogRef,
		Mode: firstNonEmptyString(
			stringFromAnyMap(runtime, "stackkit_mode"), stringFromAnyMap(runtimeMetadata, "stackkit_mode"),
			stringFromAnyMap(config, "stackkit_mode"), stringFromAnyMap(configMetadata, "stackkit_mode"),
		),
		Context: firstNonEmptyString(
			stringFromAnyMap(runtime, "context"), stringFromAnyMap(runtimeMetadata, "context"),
			stringFromAnyMap(config, "context"), stringFromAnyMap(configMetadata, "context"),
		),
		PaaS: firstNonEmptyString(
			stringFromAnyMap(runtime, "paas"), stringFromAnyMap(runtimeMetadata, "paas"),
			stringFromAnyMap(config, "paas"), stringFromAnyMap(configMetadata, "paas"),
		),
		ComputeTier: firstNonEmptyString(
			stringFromAnyMap(runtime, "compute_tier"), stringFromAnyMap(runtimeMetadata, "compute_tier"),
			stringFromAnyMap(config, "compute_tier"), stringFromAnyMap(configMetadata, "compute_tier"), stringFromAnyMap(compute, "tier"),
		),
		State:   "configured",
		Sources: []string{"stack-config"},
	}
	if deployment.Name == "" && deployment.Mode == "" && deployment.Context == "" && deployment.PaaS == "" && deployment.ComputeTier == "" {
		return nil
	}
	return deployment
}

func operationServerIndex(servers []stackOperationServer, serverID string) int {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return -1
	}
	for index := range servers {
		if servers[index].ID == serverID || servers[index].AgentID == serverID || stringFromAnyMap(servers[index].Capabilities, "worker_id") == serverID {
			return index
		}
	}
	return -1
}

func serviceVisibilityFromAccess(access map[string]any) string {
	switch stringFromAnyMap(access, serviceAccessModeKey) {
	case serviceAccessDirect, serviceAccessRelay:
		return "public"
	default:
		return ""
	}
}

func (h stackOperationsRouteHandlers) appendManagedRuntimeLeaseProjections(ctx context.Context, servers []stackOperationServer, items []managedRuntimeInventoryItem, includeAll bool) []stackOperationServer {
	for _, item := range items {
		if !includeAll && managedRuntimeLeaseRepresented(servers, item) {
			continue
		}
		server := stackServerFromManagedRuntime(item)
		h.applyManagedRuntimeMetrics(ctx, &server)
		servers = append(servers, server)
	}
	applyManagedRuntimeAuthorityToServers(servers, items)
	return servers
}

func applyManagedRuntimeAuthorityToServers(servers []stackOperationServer, items []managedRuntimeInventoryItem) {
	for _, item := range items {
		if item.NativeActive {
			continue
		}
		for index := range servers {
			if !managedRuntimeLeaseRepresentsServer(servers[index], item) {
				continue
			}
			server := &servers[index]
			server.Status = item.Status
			server.Approved = false
			server.Assignable = false
			server.PreCheck = "blocked"
			server.Health.State = item.Status
			server.Health.Notes = append(server.Health.Notes, "provider actions disabled: execution authority is "+firstNonEmptyString(item.AuthorityState, "unknown"))
			if server.Capabilities == nil {
				server.Capabilities = map[string]any{}
			}
			server.Capabilities["execution_authority"] = item.ExecutionAuthority
			server.Capabilities["authority_state"] = item.AuthorityState
		}
	}
}

func managedRuntimeLeaseRepresented(servers []stackOperationServer, item managedRuntimeInventoryItem) bool {
	for _, server := range servers {
		if managedRuntimeLeaseRepresentsServer(server, item) {
			return true
		}
	}
	return false
}

func managedRuntimeLeaseRepresentsServer(server stackOperationServer, item managedRuntimeInventoryItem) bool {
	leaseID := strings.TrimSpace(item.LeaseID)
	if leaseID != "" && (strings.TrimSpace(server.LeaseID) == leaseID || stringFromAnyMap(server.Capabilities, "lease_id") == leaseID) {
		return true
	}
	if item.IP != "" && server.IP == item.IP {
		return true
	}
	return item.RuntimeHost != "" && (server.IP == item.RuntimeHost || stringFromAnyMap(server.Capabilities, "runtime_ssh_host") == item.RuntimeHost)
}

func (h stackOperationsRouteHandlers) operationServerFromWorker(ctx context.Context, stackID string, worker *core.Record) stackOperationServer {
	workerStackID := worker.GetString("stack_id")
	assignment := "unassigned"
	if workerStackID == stackID {
		assignment = "stack"
	}
	role := strings.TrimSpace(worker.GetString("type"))
	if role == "" {
		role = "worker"
	}
	lastSeen := worker.GetDateTime("last_seen").String()
	approvedAt := worker.GetDateTime("approved_at").String()
	capabilities := workerOperationCapabilitiesFromMetadata(map[string]any{
		"cpu_cores":        worker.GetFloat("cpu_cores"),
		"ram_mb":           worker.GetFloat("ram_mb"),
		"disk_gb":          worker.GetFloat("disk_gb"),
		"gpu":              worker.GetString("gpu"),
		"has_nvme":         worker.GetBool("has_nvme"),
		"has_hw_transcode": worker.GetBool("has_hw_transcode"),
		"docker_version":   worker.GetString("docker_version"),
		"provider":         worker.GetString("provider"),
		"tags":             worker.GetString("tags"),
		"tenant_id":        worker.GetString("tenant_id"),
	}, nodehandoff.MetadataFromTags(worker.GetString("tags")))

	return stackOperationServer{
		ID:           worker.Id,
		Hostname:     firstNonEmptyString(worker.GetString("hostname"), worker.Id),
		Role:         role,
		Status:       workerConnectivityStatus(worker),
		Assignment:   assignment,
		TechstackID:  workerStackID,
		AgentID:      workerAgentID(worker),
		IP:           worker.GetString("ip"),
		OS:           worker.GetString("os"),
		Arch:         worker.GetString("arch"),
		LastSeen:     lastSeen,
		Approved:     worker.GetBool("approved"),
		ApprovedAt:   approvedAt,
		PreCheck:     h.preCheckState(worker.GetString("owner_id"), stackID, worker.Id),
		Source:       workerRegistryInventorySource,
		Assignable:   true,
		Capabilities: capabilities,
		Health:       h.serverHealth(ctx, worker),
	}
}

func (h stackOperationsRouteHandlers) operationServerFromControlPlaneNode(ctx context.Context, stackID string, node controlplane.Node) stackOperationServer {
	role := strings.TrimSpace(node.Role)
	if role == "" {
		role = managedRuntimeNodeFoundation
	}
	name := firstNonEmptyString(node.Name, node.WorkerID, node.ID)
	address := firstNonEmptyString(
		node.Address,
		stringFromAnyMap(node.Metadata, "runtime_public_ip"),
		stringFromAnyMap(node.Metadata, "runtime_private_ip"),
		stringFromAnyMap(node.Metadata, "runtime_ssh_host"),
	)
	host, _ := mapFromJSONAny(node.Metadata["host"])
	capabilities := map[string]any{
		"cpu_cores":          numberFromAnyMaps("cpu_cores", host, node.Metadata),
		"ram_mb":             numberFromAnyMaps("ram_mb", host, node.Metadata),
		"disk_gb":            numberFromAnyMaps("disk_gb", host, node.Metadata),
		"memory_total_bytes": numberFromAnyMaps("memory_total_bytes", host, node.Metadata),
		"disk_total_bytes":   numberFromAnyMaps("disk_total_bytes", host, node.Metadata),
		"runtime_ssh_host":   stringFromAnyMap(node.Metadata, "runtime_ssh_host"),
		"provider":           firstNonEmptyString(stringFromAnyMap(node.Metadata, "provider"), stringFromAnyMap(node.Metadata, "lease_provider")),
		"source":             firstNonEmptyString(stringFromAnyMap(node.Metadata, "source"), "registry-store"),
	}
	server := stackOperationServer{
		ID:           node.ID,
		Hostname:     name,
		Role:         role,
		Status:       controlPlaneNodeConnectivityStatus(node),
		Assignment:   "stack",
		TechstackID:  firstNonEmptyString(node.StackID, stackID),
		AgentID:      firstNonEmptyString(node.WorkerID, node.ID),
		IP:           address,
		OS:           stringFromAnyMap(node.Metadata, "os"),
		Arch:         stringFromAnyMap(node.Metadata, "arch"),
		LastSeen:     formatTime(node.UpdatedAt),
		Approved:     true,
		ApprovedAt:   formatTime(node.UpdatedAt),
		PreCheck:     managedRuntimePreCheckManaged,
		Source:       "registry-store",
		Assignable:   true,
		Capabilities: capabilities,
		Health:       h.controlPlaneNodeHealth(ctx, node),
	}
	applyServerInventoryMetadata(&server, node.Metadata, "registry-store")
	return server
}

func controlPlaneNodeConnectivityStatus(node controlplane.Node) string {
	return string(runtimehealth.DeriveServerState(runtimehealth.ServerInput{
		Now:           time.Now().UTC(),
		ObservedState: node.Status,
	}))
}

func (h stackOperationsRouteHandlers) applyRegistryServerHeartbeat(server *stackOperationServer, worker *controlplane.Worker) {
	if server == nil {
		return
	}
	var heartbeatAt *time.Time
	if worker != nil {
		heartbeatAt = worker.LastSeenAt
	}
	state := runtimehealth.DeriveServerState(runtimehealth.ServerInput{
		Now:           time.Now().UTC(),
		HeartbeatAt:   heartbeatAt,
		ObservedState: server.Status,
	})
	server.Status = string(state)
	server.Health.State = string(state)
	server.Health.Source = "worker-heartbeat"
	if worker == nil || worker.LastSeenAt == nil || worker.LastSeenAt.IsZero() {
		server.LastSeen = ""
		server.Health.UpdatedAt = ""
		return
	}
	server.LastSeen = formatOptionalTime(worker.LastSeenAt)
	server.Health.UpdatedAt = server.LastSeen
}

func (h stackOperationsRouteHandlers) controlPlaneNodeHealth(ctx context.Context, node controlplane.Node) stackServerHealth {
	state := controlPlaneNodeConnectivityStatus(node)
	updatedAt := formatTime(node.UpdatedAt)
	host, _ := mapFromJSONAny(node.Metadata["host"])
	health := stackServerHealth{
		State:         state,
		Source:        "registry-store",
		CPUPercent:    metricFromAnyMaps("%", []string{"runtime_cpu_percent", "cpu_percent"}, host, node.Metadata),
		MemoryPercent: metricPercentFromInventory(host, node.Metadata, []string{"runtime_memory_percent", "memory_percent"}, "memory_used_bytes", "memory_total_bytes"),
		DiskPercent:   metricPercentFromInventory(host, node.Metadata, []string{"runtime_disk_percent", "disk_percent"}, "disk_used_bytes", "disk_total_bytes"),
		UptimeSeconds: metricFromAnyMaps("s", []string{"runtime_uptime_seconds", "uptime_seconds"}, host, node.Metadata),
		UpdatedAt:     updatedAt,
	}
	if h.backend != nil {
		agentID := promLabelValue(firstNonEmptyString(node.WorkerID, node.ID))
		source := health.Source
		metricQueries := map[string]*stackMetricValue{
			fmt.Sprintf(`avg(node_cpu_usage_percent{agent_id="%s"})`, agentID):    &health.CPUPercent,
			fmt.Sprintf(`avg(node_memory_usage_percent{agent_id="%s"})`, agentID): &health.MemoryPercent,
			fmt.Sprintf(`max(node_disk_usage_percent{agent_id="%s"})`, agentID):   &health.DiskPercent,
			fmt.Sprintf(`max(node_uptime_seconds{agent_id="%s"})`, agentID):       &health.UptimeSeconds,
		}
		for query, target := range metricQueries {
			value, ok := h.queryInstantFloat(ctx, query)
			if !ok {
				continue
			}
			*target = metricKnown(value, target.Unit)
			source = "promql"
		}
		health.Source = source
	}
	if health.CPUPercent.Status != "ok" || health.MemoryPercent.Status != "ok" || health.DiskPercent.Status != "ok" {
		health.Notes = append(health.Notes, "metrics unknown")
	}
	return health
}

// applyManagedRuntimeMetrics overlays measured telemetry onto a managed-runtime
// projection server. A managed lease projection carries no telemetry of its own,
// so without this overlay it always reports "unknown" health behind the honest
// "provisioned" placeholder. Once the runtime worker reports node_*{agent_id}
// samples into the TSDB (the same worker-heartbeat path manual nodes use), the
// real CPU/RAM/disk/uptime replace the placeholders — mirroring
// controlPlaneNodeHealth so managed and manual nodes read health identically.
func (h stackOperationsRouteHandlers) applyManagedRuntimeMetrics(ctx context.Context, server *stackOperationServer) {
	if h.backend == nil {
		return
	}
	overlayManagedRuntimeMetrics(server, func(query string) (float64, bool) {
		return h.queryInstantFloat(ctx, query)
	})
}

// overlayManagedRuntimeMetrics is the backend-agnostic core of
// applyManagedRuntimeMetrics: given an instant-query func it overlays the four
// dashboard metrics keyed by the node's agent_id. When at least one value is
// measured it marks the source promql, drops the "no live telemetry" note, and
// promotes a "provisioned" placeholder to measured "healthy". With no samples the
// honest lease-state projection is left untouched.
func overlayManagedRuntimeMetrics(server *stackOperationServer, query func(string) (float64, bool)) {
	if server == nil || query == nil {
		return
	}
	agentID := promLabelValue(server.AgentID)
	if agentID == "" {
		return
	}
	metricQueries := []struct {
		expr   string
		target *stackMetricValue
	}{
		{fmt.Sprintf(`avg(node_cpu_usage_percent{agent_id="%s"})`, agentID), &server.Health.CPUPercent},
		{fmt.Sprintf(`avg(node_memory_usage_percent{agent_id="%s"})`, agentID), &server.Health.MemoryPercent},
		{fmt.Sprintf(`max(node_disk_usage_percent{agent_id="%s"})`, agentID), &server.Health.DiskPercent},
		{fmt.Sprintf(`max(node_uptime_seconds{agent_id="%s"})`, agentID), &server.Health.UptimeSeconds},
	}
	measured := false
	for _, mq := range metricQueries {
		value, ok := query(mq.expr)
		if !ok {
			continue
		}
		*mq.target = metricKnown(value, mq.target.Unit)
		measured = true
	}
	if !measured {
		return
	}
	server.Health.Source = "promql"
	server.Health.Notes = dropManagedNoTelemetryNotes(server.Health.Notes)
	if server.Status == managedRuntimeStatusProvisioned {
		server.Status = "healthy"
		server.Health.State = "healthy"
	}
}

// dropManagedNoTelemetryNotes removes the "no live telemetry" projection note
// once real telemetry has been overlaid, so the placeholder note cannot
// contradict measured values.
func dropManagedNoTelemetryNotes(notes []string) []string {
	if len(notes) == 0 {
		return notes
	}
	filtered := make([]string, 0, len(notes))
	for _, note := range notes {
		if strings.Contains(note, "no live telemetry") {
			continue
		}
		filtered = append(filtered, note)
	}
	return filtered
}

func (h stackOperationsRouteHandlers) operationServerFromControlPlaneWorker(ctx context.Context, ownerID, stackID string, worker *controlplane.Worker) stackOperationServer {
	if worker == nil {
		return stackOperationServer{}
	}
	workerStackID := strings.TrimSpace(worker.StackID)
	assignment := "unassigned"
	if workerStackID == stackID {
		assignment = "stack"
	}
	role := strings.TrimSpace(worker.Type)
	if role == "" {
		role = "worker"
	}
	lastSeen := formatOptionalTime(worker.LastSeenAt)
	approvedAt := formatOptionalTime(worker.ApprovedAt)
	tags := controlPlaneWorkerTags(worker)
	metadata := nodehandoff.MergeMetadata(nodehandoff.MergeMetadata(worker.Resources, worker.Capabilities), nodehandoff.MetadataFromTags(tags))
	capabilities := workerOperationCapabilitiesFromMetadata(map[string]any{
		"cpu_cores":        worker.CPUCores,
		"ram_mb":           worker.RAMMB,
		"disk_gb":          worker.DiskGB,
		"gpu":              worker.GPU,
		"has_nvme":         worker.HasNVME,
		"has_hw_transcode": worker.HasHWTranscode,
		"docker_version":   worker.DockerVersion,
		"provider":         worker.Provider,
		"tags":             tags,
		"tenant_id":        worker.TenantID,
	}, metadata)

	server := stackOperationServer{
		ID:           worker.ID,
		Hostname:     firstNonEmptyString(worker.Hostname, worker.ID),
		Role:         role,
		Status:       controlPlaneWorkerConnectivityStatus(worker),
		Assignment:   assignment,
		TechstackID:  workerStackID,
		AgentID:      worker.ID,
		IP:           worker.IP,
		OS:           worker.OS,
		Arch:         worker.Arch,
		LastSeen:     lastSeen,
		Approved:     worker.Approved,
		ApprovedAt:   approvedAt,
		PreCheck:     h.preCheckState(ownerID, stackID, worker.ID),
		Source:       workerRegistryInventorySource,
		Assignable:   true,
		Capabilities: capabilities,
		Health:       h.controlPlaneWorkerHealth(ctx, worker),
	}
	applyServerInventoryMetadata(&server, metadata, workerRegistryInventorySource)
	return server
}

func workerOperationCapabilitiesFromMetadata(base map[string]any, metadata map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	if role := nodehandoff.StringFromMap(metadata, nodehandoff.KeyServerNodeRole); role != "" {
		base[nodehandoff.KeyServerNodeRole] = nodehandoff.NormalizeNodeRole(role)
		base["node_role"] = nodehandoff.NormalizeNodeRole(role)
	}
	if services := nodehandoff.ServiceKeysFromAny(metadata[nodehandoff.KeyRequestedServices]); len(services) > 0 {
		base[nodehandoff.KeyRequestedServices] = services
		base["services"] = services
	}
	for _, key := range []string{
		nodehandoff.KeyServerRemoteHost,
		nodehandoff.KeyServerRemoteUser,
		nodehandoff.KeyServerRemoteAuthMethod,
		nodehandoff.KeyServerRemoteCredential,
		nodehandoff.KeyServerRemoteSSHKey,
	} {
		if value := nodehandoff.StringFromMap(metadata, key); value != "" {
			base[key] = value
		}
	}
	if port := nodehandoff.IntFromMap(metadata, nodehandoff.KeyServerRemotePort); port > 0 {
		base[nodehandoff.KeyServerRemotePort] = port
	}
	if nodehandoff.BoolFromMap(metadata, nodehandoff.KeyServerRemoteUseSudo) {
		base[nodehandoff.KeyServerRemoteUseSudo] = true
	}
	return base
}

func controlPlaneWorkerConnectivityStatus(worker *controlplane.Worker) string {
	if worker == nil || !worker.Approved {
		return "pending"
	}
	return string(runtimehealth.DeriveServerState(runtimehealth.ServerInput{Now: time.Now().UTC(), HeartbeatAt: worker.LastSeenAt}))
}

func (h stackOperationsRouteHandlers) controlPlaneWorkerHealth(ctx context.Context, worker *controlplane.Worker) stackServerHealth {
	state := controlPlaneWorkerConnectivityStatus(worker)
	updatedAt := ""
	if worker != nil {
		updatedAt = formatOptionalTime(worker.LastSeenAt)
	}
	health := stackServerHealth{
		State:         state,
		Source:        "worker-registry",
		CPUPercent:    metricUnknown("%"),
		MemoryPercent: metricUnknown("%"),
		DiskPercent:   metricUnknown("%"),
		UptimeSeconds: metricUnknown("s"),
		UpdatedAt:     updatedAt,
	}
	if h.backend != nil && worker != nil {
		agentID := promLabelValue(worker.ID)
		source := "worker-registry"
		metricQueries := map[string]*stackMetricValue{
			fmt.Sprintf(`avg(node_cpu_usage_percent{agent_id="%s"})`, agentID):    &health.CPUPercent,
			fmt.Sprintf(`avg(node_memory_usage_percent{agent_id="%s"})`, agentID): &health.MemoryPercent,
			fmt.Sprintf(`max(node_disk_usage_percent{agent_id="%s"})`, agentID):   &health.DiskPercent,
			fmt.Sprintf(`max(node_uptime_seconds{agent_id="%s"})`, agentID):       &health.UptimeSeconds,
		}
		for query, target := range metricQueries {
			value, ok := h.queryInstantFloat(ctx, query)
			if !ok {
				continue
			}
			*target = metricKnown(value, target.Unit)
			source = "promql"
		}
		health.Source = source
		if source != "promql" {
			health.Notes = []string{"metrics unknown"}
		}
		return health
	}
	health.Notes = []string{"metrics unavailable"}
	return health
}

func controlPlaneWorkerTags(worker *controlplane.Worker) string {
	if worker == nil || len(worker.Tags) == 0 {
		return ""
	}
	if raw, ok := worker.Tags["raw"].(string); ok {
		return raw
	}
	values := make([]string, 0, len(worker.Tags))
	for key := range worker.Tags {
		values = append(values, key)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func formatOptionalTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func workerAgentID(worker *core.Record) string {
	if id := strings.TrimSpace(worker.GetString("agent_id")); id != "" {
		return id
	}
	return worker.Id
}

func workerConnectivityStatus(worker *core.Record) string {
	if !worker.GetBool("approved") {
		return "pending"
	}
	lastSeen := worker.GetDateTime("last_seen")
	if lastSeen.IsZero() {
		return "unknown"
	}
	age := time.Since(lastSeen.Time())
	switch {
	case age <= 90*time.Second:
		return "healthy"
	case age <= 5*time.Minute:
		return "stale"
	default:
		return "offline"
	}
}

func (h stackOperationsRouteHandlers) serverHealth(ctx context.Context, worker *core.Record) stackServerHealth {
	state := workerConnectivityStatus(worker)
	source := "worker-registry"
	updatedAt := worker.GetDateTime("last_seen").String()

	health := stackServerHealth{
		State:         state,
		Source:        source,
		CPUPercent:    metricUnknown("%"),
		MemoryPercent: metricUnknown("%"),
		DiskPercent:   metricUnknown("%"),
		UptimeSeconds: metricUnknown("s"),
		UpdatedAt:     updatedAt,
	}
	if h.backend == nil {
		health.Notes = []string{"metrics unavailable"}
		return health
	}

	agentID := promLabelValue(workerAgentID(worker))
	metricQueries := map[string]*stackMetricValue{
		fmt.Sprintf(`avg(node_cpu_usage_percent{agent_id="%s"})`, agentID):    &health.CPUPercent,
		fmt.Sprintf(`avg(node_memory_usage_percent{agent_id="%s"})`, agentID): &health.MemoryPercent,
		fmt.Sprintf(`max(node_disk_usage_percent{agent_id="%s"})`, agentID):   &health.DiskPercent,
		fmt.Sprintf(`max(node_uptime_seconds{agent_id="%s"})`, agentID):       &health.UptimeSeconds,
	}
	for query, target := range metricQueries {
		value, ok := h.queryInstantFloat(ctx, query)
		if !ok {
			continue
		}
		*target = metricKnown(value, target.Unit)
		source = "promql"
	}
	health.Source = source
	if source != "promql" {
		health.Notes = []string{"metrics unknown"}
	}
	return health
}

func (h stackOperationsRouteHandlers) queryInstantFloat(parent context.Context, query string) (float64, bool) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	result, err := h.backend.InstantQuery(ctx, query, time.Now())
	if err != nil {
		return 0, false
	}
	return promQueryFloat(result)
}

func promQueryFloat(result *monitoring.QueryResult) (float64, bool) {
	if result == nil || result.Value == nil {
		return 0, false
	}
	vector, ok := result.Value.(promql.Vector)
	if !ok || len(vector) == 0 {
		return 0, false
	}
	value := vector[0].F
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return math.Round(value*10) / 10, true
}

func metricUnknown(unit string) stackMetricValue {
	return stackMetricValue{Status: "unknown", Unit: unit}
}

func metricKnown(value float64, unit string) stackMetricValue {
	return stackMetricValue{Status: "ok", Value: float64Pointer(value), Unit: unit}
}

func metricFromAnyMaps(unit string, keys []string, maps ...map[string]any) stackMetricValue {
	for _, values := range maps {
		for _, key := range keys {
			if value, ok := float64FromAny(values[key]); ok {
				return metricKnown(value, unit)
			}
		}
	}
	return metricUnknown(unit)
}

func numberFromAnyMaps(key string, maps ...map[string]any) float64 {
	for _, values := range maps {
		if value, ok := float64FromAny(values[key]); ok {
			return value
		}
	}
	return 0
}

func float64FromAny(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func float64Pointer(v float64) *float64 {
	return &v
}

func promLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func (h stackOperationsRouteHandlers) preCheckState(ownerID, stackID, workerID string) string {
	records, err := h.preCheckRecords(ownerID, stackID, workerID, 20)
	if err != nil || len(records) == 0 {
		return "unknown"
	}
	status := "passed"
	for _, record := range records {
		switch record.GetString(preCheckStatusField) {
		case "failed":
			return "failed"
		case "pending":
			status = "pending"
		case "":
			status = "unknown"
		}
	}
	return status
}

func (h stackOperationsRouteHandlers) serverPreChecks(ownerID, stackID, workerID string) []PreCheckResultResponse {
	records, err := h.preCheckRecords(ownerID, stackID, workerID, 50)
	if err != nil {
		return nil
	}
	return preCheckResultResponses(records)
}

func (h stackOperationsRouteHandlers) preCheckRecords(ownerID, stackID, workerID string, limit int) ([]*core.Record, error) {
	if h.app == nil {
		return nil, nil
	}
	return h.app.FindRecordsByFilter(
		preCheckResultsCollection,
		"owner_id = {:ownerId} && worker_id = {:workerId} && (stack_id = {:stackId} || stack_id = '')",
		"-created",
		limit,
		0,
		map[string]any{"ownerId": ownerID, "workerId": workerID, "stackId": stackID},
	)
}

func stringListFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case map[string]any:
		out := make([]string, 0, len(v))
		for key := range v {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	case types.JSONRaw:
		var parsed any
		if err := json.Unmarshal(v, &parsed); err == nil {
			return stringListFromAny(parsed)
		}
	}
	return nil
}

func mapFromJSONAny(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case types.JSONRaw:
		return decodeJSONMap(v)
	case json.RawMessage:
		return decodeJSONMap(v)
	case []byte:
		return decodeJSONMap(v)
	case string:
		return decodeJSONMap([]byte(v))
	}
	return nil, false
}

func decodeJSONMap(data []byte) (map[string]any, bool) {
	if len(data) == 0 {
		return nil, false
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func boolFromAnyMap(primary, fallback map[string]any, key string) bool {
	if value, ok := boolValueFromAny(primary[key]); ok {
		return value
	}
	if value, ok := boolValueFromAny(fallback[key]); ok {
		return value
	}
	return false
}

func boolValueFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y":
			return true, true
		case "0", "false", "no", "n":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
