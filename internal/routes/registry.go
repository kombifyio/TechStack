package routes

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	registryCollectionServices = "services"
	registryCollectionStacks   = "stacks"

	registryResponseServersKey = "servers"

	registryBaseKit                    = "basement-kit"
	registryCloudKit                   = "cloud-kit"
	registryFoundationRole             = "foundation"
	registryServicePocketID            = "pocket_id"
	registryServiceTypeAuth            = "auth"
	registryServiceTraefik             = "traefik"
	registryServiceMonitor             = "monitoring"
	registryServiceVault               = "vaultwarden"
	registryServiceImmich              = "immich"
	registryServiceFiles               = "files"
	registryUnknownStatus              = "unknown"
	registryObservedState              = "observed"
	registryManagedState               = "managed"
	registryCustomService              = "custom"
	registryStatusArchived             = "archived"
	registryStatusDeploying            = "deploying"
	registryStatusError                = "error"
	registryStatusMigrating            = "migrating"
	registryStatusPendingVerification  = "pending_verification"
	registryStatusRunning              = "running"
	registryStatusStopped              = "stopped"
	registryPlacementScopeStack        = "stack"
	stackKitOutputKey                  = "stackkit_outputs"
	stackKitsInventorySource           = "stackkits-inventory"
	runtimeLeaseIDKey                  = "lease_id"
	runtimeHealthDegraded              = "degraded"
	registryMigrationUnavailableReason = "Runtime service migration is not enabled until a real deploy, health verification, cutover, and source-drain executor is available."
)

// stackKitOutputManagementState pins the StackKit job-output projection to the
// canonical source rule instead of hard-coding "managed" a second time.
var stackKitOutputManagementState = string(serviceregistry.ManagementStateForSource(stackKitOutputKey))

type RegistryRouteStores struct {
	// AllowLocalHomeAssistant permits network probes from a self-hosted executor.
	// Hosted instances need a separately authorized LAN executor instead.
	AllowLocalHomeAssistant bool
	Stacks                  controlplane.StackStore
	Workers                 controlplane.WorkerStore
	Registry                controlplane.RegistryStore
	Jobs                    controlplane.JobStore
	// Servers is the canonical serverregistry read model. The Registry Services
	// BFF sources Node lifecycle, connection, health, and last-heartbeat from it
	// instead of composing runtime state from `nodes` + `workers`.
	Servers controlplane.ServerRuntimeStore
}

func RegisterRegistryRoutesWithStores(r *httpx.Router, stores RegistryRouteStores) {
	h := registryRouteHandlers{
		stackStore:              stores.Stacks,
		workerStore:             stores.Workers,
		registryStore:           stores.Registry,
		jobStore:                stores.Jobs,
		serverStore:             stores.Servers,
		allowLocalHomeAssistant: stores.AllowLocalHomeAssistant,
	}
	r.GET("/api/v1/registry/services", h.services)
	r.POST("/api/v1/registry/services/attach", h.attachService)
	r.POST("/api/v1/registry/services/import", h.importUnmanagedService)
	r.POST("/api/v1/registry/services/home-assistant/import", h.importHomeAssistant)
	r.POST("/api/v1/registry/services/migrate", h.migrateService)
	r.POST("/api/v1/registry/services/verify", h.verifyService)
	r.DELETE("/api/v1/registry/services/{id}", h.deleteService)
}

type registryRouteHandlers struct {
	allowLocalHomeAssistant bool
	stackStore              controlplane.StackStore
	workerStore             controlplane.WorkerStore
	registryStore           controlplane.RegistryStore
	jobStore                controlplane.JobStore
	serverStore             controlplane.ServerRuntimeStore
}

type registryPayload struct {
	Catalog                    []registryCatalogService `json:"catalog"`
	Stacks                     []registryStack          `json:"kit_deployments"`
	Servers                    []registryServer         `json:"servers"`
	Services                   []registryService        `json:"services"`
	MigrationAvailable         bool                     `json:"migration_available"`
	MigrationUnavailableReason string                   `json:"migration_unavailable_reason,omitempty"`
}

type registryStack struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Status                 string `json:"status"`
	StackKitFoundation     string `json:"stackkit_foundation"`
	ServerMode             string `json:"server_mode,omitempty"`
	RuntimeLane            string `json:"runtime_lane,omitempty"`
	RuntimeOfferingID      string `json:"runtime_offering_id,omitempty"`
	LeaseProvider          string `json:"lease_provider,omitempty"`
	ProviderRegion         string `json:"provider_region,omitempty"`
	IONOSDatacenter        string `json:"ionos_datacenter,omitempty"`
	ServerProvisioningMode string `json:"server_provisioning_mode,omitempty"`
}

type registryServer struct {
	ID              string `json:"id"`
	KitDeploymentID string `json:"kit_deployment_id"`
	Name            string `json:"name"`
	Hostname        string `json:"hostname"`
	Role            string `json:"role"`
	RoleLabel       string `json:"role_label"`
	WorkerID        string `json:"worker_id,omitempty"`
	LeaseID         string `json:"lease_id,omitempty"`
	Status          string `json:"status,omitempty"`
	HealthState     string `json:"health_state,omitempty"`
	LastSeen        string `json:"last_seen,omitempty"`
	RolloutReady    bool   `json:"rollout_ready"`
}

type registryService struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	ApplicationKey    string `json:"application_key"`
	ApplicationName   string `json:"application_name"`
	Type              string `json:"type"`
	Status            string `json:"status"`
	HealthState       string `json:"health_state,omitempty"`
	ObservedAt        string `json:"observed_at,omitempty"`
	ManagementState   string `json:"management_state"`
	MigrationStatus   string `json:"migration_status,omitempty"`
	PlacementScope    string `json:"placement_scope"`
	MoveAllowed       bool   `json:"move_allowed"`
	MoveBlockedReason string `json:"move_blocked_reason,omitempty"`
	KitDeploymentID   string `json:"kit_deployment_id"`
	StackName         string `json:"stack_name"`
	ServerID          string `json:"server_id"`
	ServerName        string `json:"server_name"`
	Port              int    `json:"port,omitempty"`
	URL               string `json:"url,omitempty"`
}

type registryCatalogService struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Recommended bool     `json:"recommended"`
	Foundations []string `json:"foundations"`
}

type registryServiceRequest struct {
	StackID     string `json:"stack_id"`
	ServerID    string `json:"server_id"`
	ServiceID   string `json:"service_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
}

func (h registryRouteHandlers) services(e *httpx.Event) error {
	payload, err := h.registryPayload(e)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, payload)
}

func (h registryRouteHandlers) attachService(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	req, err := readRegistryServiceRequest(e)
	if err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	catalogService, ok := registryCatalogServiceByID(req.ServiceID)
	if !ok {
		return httpx.BadRequest(e, "Unknown catalog service", map[string]any{"service_id": req.ServiceID})
	}

	tenantID, err := h.requireRegistryMutationTenant(e, ownerID, "techstack.registry.services.attach")
	if err != nil {
		return err
	}
	stack, node, ok, err := h.ownedRegistryNode(e, tenantID, ownerID, req.StackID, req.ServerID)
	if err != nil || !ok {
		return err
	}
	serviceKey := normalizeServiceKey(catalogService.ID)
	service, err := h.registryStore.UpsertService(e.Request.Context(), controlplane.Service{
		ID: runtimeidentity.ServiceID(stack.ID, node.ID, serviceKey, "default"), TenantID: tenantID,
		InstanceID: stack.InstanceID, StackID: stack.ID, NodeID: node.ID, ServiceKey: serviceKey,
		Name: serviceKey, Status: preCheckStatusPending, Source: serviceregistry.SourceTechstackRegistry,
		Metadata: map[string]any{"display_name": catalogService.DisplayName, "type": catalogService.Type},
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to attach service", nil)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"service": serviceRegistryRecordFromStore(*service, *stack, *node),
	})
}

func (h registryRouteHandlers) importUnmanagedService(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	req, err := readRegistryServiceRequest(e)
	if err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	name := normalizeServiceKey(firstNonEmptyString(req.Name, req.ServiceID))
	if name == "" {
		return httpx.BadRequest(e, "Service name is required", nil)
	}

	tenantID, err := h.requireRegistryMutationTenant(e, ownerID, "techstack.registry.services.import")
	if err != nil {
		return err
	}
	stack, node, ok, err := h.ownedRegistryNode(e, tenantID, ownerID, req.StackID, req.ServerID)
	if err != nil || !ok {
		return err
	}
	metadata := map[string]any{
		"display_name": firstNonEmptyString(req.DisplayName, canonicalServiceDisplayName(name), name),
		"type":         firstNonEmptyString(req.Type, registryCustomService),
	}
	if req.Port > 0 {
		metadata["port"] = req.Port
	}
	service, err := h.registryStore.UpsertService(e.Request.Context(), controlplane.Service{
		ID: runtimeidentity.ServiceID(stack.ID, node.ID, name, "default"), TenantID: tenantID,
		InstanceID: stack.InstanceID, StackID: stack.ID, NodeID: node.ID, ServiceKey: name,
		Name: name, Status: registryObservedState, Source: serviceregistry.SourceObserved,
		URL: strings.TrimSpace(req.URL), Metadata: metadata,
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to import unmanaged service", nil)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"service": serviceRegistryRecordFromStore(*service, *stack, *node),
	})
}

func (h registryRouteHandlers) registryPayload(e *httpx.Event) (registryPayload, error) {
	ownerID, err := requireAuth(e)
	if err != nil {
		return registryPayload{}, err
	}
	tenantID, err := requireRegistryRouteTenant(e, ownerID, "techstack.registry.read")
	if err != nil {
		return registryPayload{}, err
	}
	if h.stackStore == nil || h.registryStore == nil {
		return registryPayload{}, httpx.NewInternalServerError("Canonical registry stores are unavailable", nil)
	}
	return h.registryPayloadFromStore(e, tenantID, ownerID)
}

func requireRegistryRouteTenant(e *httpx.Event, ownerID, capability string) (string, error) {
	return tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
}

//nolint:gocyclo // Aggregates services, workers, runtime metrics, and stack ownership into one registry BFF payload.
func (h registryRouteHandlers) registryPayloadFromStore(e *httpx.Event, tenantID, ownerID string) (registryPayload, error) {
	stacks, err := h.stackStore.ListStacksByTenant(e.Request.Context(), tenantID)
	if err != nil {
		return registryPayload{}, httpx.NewInternalServerError("Failed to fetch stacks", nil)
	}

	payload := registryPayload{
		Catalog:                    registryServiceCatalog(),
		Stacks:                     []registryStack{},
		Servers:                    []registryServer{},
		Services:                   []registryService{},
		MigrationAvailable:         false,
		MigrationUnavailableReason: registryMigrationUnavailableReason,
	}
	workersByID := map[string]controlplane.Worker{}
	if h.workerStore != nil {
		workers, workersErr := h.workerStore.ListWorkersByTenant(e.Request.Context(), tenantID)
		if workersErr == nil {
			for _, worker := range workers {
				if worker.OwnerSubjectID == ownerID {
					workersByID[worker.ID] = worker
				}
			}
		}
	}
	now := time.Now().UTC()
	canonicalServers := h.canonicalServersByStack(e.Request.Context(), tenantID, ownerID)
	for _, stack := range stacks {
		if stack.OwnerSubjectID != ownerID {
			continue
		}
		payload.Stacks = append(payload.Stacks, stackRegistryRecordFromStore(stack))

		nodes, err := h.registryStore.ListNodesByStack(e.Request.Context(), tenantID, stack.ID)
		if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			return registryPayload{}, httpx.NewInternalServerError("Failed to fetch servers", nil)
		}
		nodesByID := make(map[string]controlplane.Node, len(nodes))
		for _, node := range nodes {
			nodesByID[node.ID] = node
		}
		stackServers := registryServersForStack(canonicalServers[stack.ID], nodes, workersByID, now)
		payload.Servers = append(payload.Servers, stackServers...)
		if len(nodesByID) == 0 && len(canonicalServers[stack.ID]) == 0 && h.workerStore != nil {
			for _, worker := range workersByID {
				if worker.OwnerSubjectID != ownerID || strings.TrimSpace(worker.StackID) != stack.ID {
					continue
				}
				server := serverRegistryRecordFromWorkerStore(worker)
				payload.Servers = append(payload.Servers, server)
				stackServers = append(stackServers, server)
			}
		}

		services, err := h.registryStore.ListServicesByStack(e.Request.Context(), tenantID, stack.ID)
		if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			return registryPayload{}, httpx.NewInternalServerError("Failed to fetch services", nil)
		}
		stackServices := make([]registryService, 0, len(services))
		for _, service := range services {
			node := nodesByID[service.NodeID]
			stackServices = append(stackServices, serviceRegistryRecordFromStoreWithHealth(service, stack, node, workersByID[node.WorkerID], now))
		}
		if len(stackServices) == 0 {
			outputs := stackKitOutputsFromLatestDeployJob(e.Request.Context(), h.jobStore, tenantID, stack.ID)
			stackServices = registryServicesFromStackKitOutputs(outputs, stack, stackServers)
		}
		payload.Services = append(payload.Services, stackServices...)
	}

	sort.SliceStable(payload.Servers, func(i, j int) bool {
		if payload.Servers[i].KitDeploymentID != payload.Servers[j].KitDeploymentID {
			return payload.Servers[i].KitDeploymentID < payload.Servers[j].KitDeploymentID
		}
		return strings.ToLower(payload.Servers[i].Name) < strings.ToLower(payload.Servers[j].Name)
	})
	sort.SliceStable(payload.Services, func(i, j int) bool {
		if payload.Services[i].StackName != payload.Services[j].StackName {
			return strings.ToLower(payload.Services[i].StackName) < strings.ToLower(payload.Services[j].StackName)
		}
		return strings.ToLower(payload.Services[i].DisplayName) < strings.ToLower(payload.Services[j].DisplayName)
	})
	return payload, nil
}

func (h registryRouteHandlers) ownedRegistryNode(e *httpx.Event, tenantID, ownerID, stackID, nodeID string) (*controlplane.Stack, *controlplane.Node, bool, error) {
	stackID = strings.TrimSpace(stackID)
	nodeID = strings.TrimSpace(nodeID)
	if stackID == "" {
		return nil, nil, false, httpx.BadRequest(e, "Stack ID is required", nil)
	}
	if nodeID == "" {
		return nil, nil, false, httpx.BadRequest(e, "Server ID is required", nil)
	}
	stack, err := h.stackStore.GetStack(e.Request.Context(), tenantID, stackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, nil, false, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return nil, nil, false, httpx.NewInternalServerError("Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return nil, nil, false, httpx.Forbidden(e, "Not your stack")
	}
	node, err := h.registryStore.GetNode(e.Request.Context(), tenantID, nodeID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, nil, false, httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return nil, nil, false, httpx.NewInternalServerError("Failed to fetch server", nil)
	}
	if node.StackID != stack.ID {
		return nil, nil, false, httpx.NotFound(e, "Server not found for stack")
	}
	return stack, node, true, nil
}

func readRegistryServiceRequest(e *httpx.Event) (registryServiceRequest, error) {
	var req registryServiceRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return req, err
	}
	req.StackID = strings.TrimSpace(req.StackID)
	req.ServerID = strings.TrimSpace(req.ServerID)
	req.ServiceID = normalizeServiceKey(req.ServiceID)
	req.Name = normalizeServiceKey(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Type = normalizeServiceKey(req.Type)
	req.URL = strings.TrimSpace(req.URL)
	return req, nil
}

func stackRegistryRecordFromStore(stack controlplane.Stack) registryStack {
	foundation := normalizeRegistryStackKitFoundation(firstNonEmptyString(
		stringFromAnyMap(stack.RuntimeSummary, "stackkit_catalog_ref"),
		stringFromAnyMap(stack.Config, "stackkit_catalog_ref"),
		stringFromAnyMap(stack.Config, "stackkit"),
		registryBaseKit,
	))
	return registryStack{
		ID:                     stack.ID,
		Name:                   firstNonEmptyString(stack.Name, stack.ID),
		Status:                 firstNonEmptyString(stack.Status, registryUnknownStatus),
		StackKitFoundation:     foundation,
		ServerMode:             firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "server_mode"), stringFromAnyMap(stack.Config, "server_mode")),
		RuntimeLane:            firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "runtime_lane"), stringFromAnyMap(stack.Config, "runtime_lane")),
		RuntimeOfferingID:      firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "runtime_offering_id"), stringFromAnyMap(stack.Config, "runtime_offering_id")),
		LeaseProvider:          firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "lease_provider"), stringFromAnyMap(stack.Config, "lease_provider")),
		ProviderRegion:         firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "provider_region"), stringFromAnyMap(stack.Config, "provider_region")),
		IONOSDatacenter:        firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "ionos_datacenter"), stringFromAnyMap(stack.Config, "ionos_datacenter")),
		ServerProvisioningMode: firstNonEmptyString(stringFromAnyMap(stack.RuntimeSummary, "server_provisioning_mode"), stringFromAnyMap(stack.Config, "server_provisioning_mode")),
	}
}

func normalizeRegistryStackKitFoundation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "basement", "basementkit":
		return registryBaseKit
	case "cloud", "cloudkit", "kombify-cloud-kit":
		return registryCloudKit
	default:
		return strings.TrimSpace(value)
	}
}

// canonicalServersByStack loads the canonical serverregistry read model for
// the tenant and groups it by stack. A missing or failing canonical store is
// not an error here: the legacy projection then falls back to its historical
// `nodes` + `workers` composition until the Wave 2 cutover removes it.
func (h registryRouteHandlers) canonicalServersByStack(ctx context.Context, tenantID, ownerID string) map[string][]controlplane.ServerRuntime {
	if h.serverStore == nil {
		return nil
	}
	runtimes, err := h.serverStore.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return nil
	}
	byStack := make(map[string][]controlplane.ServerRuntime, len(runtimes))
	for _, runtime := range runtimes {
		// Same owner filter as /api/v1/servers. Keeping it identical is what
		// makes the two routes return the same server set; a row this owner
		// cannot see canonically must not appear in the legacy list either.
		if runtime.OwnerSubjectID != ownerID {
			continue
		}
		stackID := strings.TrimSpace(runtime.StackID)
		byStack[stackID] = append(byStack[stackID], runtime)
	}
	for stackID := range byStack {
		servers := byStack[stackID]
		sort.SliceStable(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })
	}
	return byStack
}

// registryServersForStack composes the legacy server list for one stack. The
// canonical aggregate is authoritative for identity, lifecycle, connection,
// health, heartbeat, lease, and stack association; the legacy `nodes` row only
// contributes the shape fields that have no canonical equivalent yet
// (hostname, role, role_label). Nodes without a canonical counterpart keep the
// historical worker-heartbeat composition so an un-migrated deployment does
// not lose its server list; that fallback disappears with the legacy tables in
// kombify-Techstack-nzy1.7.
func registryServersForStack(
	runtimes []controlplane.ServerRuntime,
	nodes []controlplane.Node,
	workersByID map[string]controlplane.Worker,
	now time.Time,
) []registryServer {
	servers := make([]registryServer, 0, len(runtimes)+len(nodes))
	consumed := make(map[string]bool, len(runtimes))
	for _, node := range nodes {
		runtime, ok := matchCanonicalServerForNode(runtimes, consumed, node)
		if !ok {
			servers = append(servers, serverRegistryRecordFromStoreWithHealth(node, workersByID[node.WorkerID], now))
			continue
		}
		consumed[runtime.ID] = true
		servers = append(servers, registryServerFromCanonical(runtime, node))
	}
	for _, runtime := range runtimes {
		if consumed[runtime.ID] {
			continue
		}
		servers = append(servers, registryServerFromCanonical(runtime, controlplane.Node{}))
	}
	return servers
}

// matchCanonicalServerForNode binds a legacy node row to its canonical
// aggregate. The Guard inventory path already writes the node under the
// canonical server identity, so identity equality is the primary match; worker
// and lease identity cover rows written before that convention.
func matchCanonicalServerForNode(runtimes []controlplane.ServerRuntime, consumed map[string]bool, node controlplane.Node) (controlplane.ServerRuntime, bool) {
	nodeID := strings.TrimSpace(node.ID)
	nodeWorkerID := strings.TrimSpace(node.WorkerID)
	nodeLeaseID := strings.TrimSpace(stringFromAnyMap(node.Metadata, runtimeLeaseIDKey))
	for _, match := range []func(controlplane.ServerRuntime) bool{
		func(runtime controlplane.ServerRuntime) bool { return nodeID != "" && runtime.ID == nodeID },
		func(runtime controlplane.ServerRuntime) bool {
			return nodeWorkerID != "" && strings.TrimSpace(runtime.WorkerID) == nodeWorkerID
		},
		func(runtime controlplane.ServerRuntime) bool {
			return nodeLeaseID != "" && strings.TrimSpace(runtime.LeaseID) == nodeLeaseID
		},
	} {
		for _, runtime := range runtimes {
			if consumed[runtime.ID] || !match(runtime) {
				continue
			}
			return runtime, true
		}
	}
	return controlplane.ServerRuntime{}, false
}

// registryServerFromCanonical maps the canonical aggregate onto the Registry
// Services BFF Node projection. Documented mapping (canonical -> projection):
//
//	id            <- Aggregate.ID                (same value as /api/v1/servers `id`)
//	kit_deployment_id <- Aggregate.StackID
//	name          <- Aggregate.Name
//	worker_id     <- Aggregate.WorkerID
//	lease_id      <- Aggregate.LeaseID
//	last_seen     <- Aggregate.LastHeartbeatAt   (RFC3339Nano, same instant as
//	                                              connection.last_heartbeat_at)
//	status        <- serverregistry.LegacyServerState(connection, health)
//	health_state  <- same value as status; the BFF shape carries the single
//	                 collapsed state twice and clients read both
//	rollout_ready <- serverregistry.LegacyRolloutReady(lifecycle, connection, health)
//
// Fields with no canonical equivalent are taken from the legacy node row and
// are never invented: `hostname`, `role`, and `role_label` stay node-derived,
// and `role` keeps its historical "foundation" default when the node carries
// none. Nothing here recomputes freshness — persisted canonical state is the
// only source, per kombify-Techstack-nzy1.4 / #577.
func registryServerFromCanonical(runtime controlplane.ServerRuntime, node controlplane.Node) registryServer {
	role := strings.TrimSpace(node.Role)
	if role == "" {
		role = registryFoundationRole
	}
	name := firstNonEmptyString(runtime.Name, node.Name, runtime.WorkerID, runtime.ID)
	state := serverregistry.LegacyServerState(runtime.ConnectionState, runtime.HealthState)
	return registryServer{
		ID:              runtime.ID,
		KitDeploymentID: firstNonEmptyString(runtime.StackID, node.StackID),
		Name:            name,
		Hostname:        firstNonEmptyString(node.Name, name),
		Role:            role,
		RoleLabel:       registryRoleLabel(role),
		WorkerID:        firstNonEmptyString(runtime.WorkerID, node.WorkerID),
		LeaseID:         firstNonEmptyString(runtime.LeaseID, stringFromAnyMap(node.Metadata, runtimeLeaseIDKey)),
		Status:          string(state),
		HealthState:     string(state),
		LastSeen:        formatOptionalTime(runtime.LastHeartbeatAt),
		RolloutReady:    serverregistry.LegacyRolloutReady(runtime.LifecycleState, runtime.ConnectionState, runtime.HealthState),
	}
}

// serverRegistryRecordFromStoreWithHealth shapes a legacy `nodes` row that has
// no canonical server aggregate behind it.
//
// Wave 2 (kombify-Techstack-nzy1.7) collapsed the read here: it no longer
// recomputes freshness from the satellite `workers.last_seen_at` at request
// time. `nodes` and `workers` are read-only satellites now — they contribute
// identity and shape (hostname, role), never a runtime verdict. A row without
// an aggregate projects as `provisioned` and is never rollout-ready, so the
// Registry Services can still show that the server exists without claiming a
// health state nothing persisted. The row itself is untouched.
func serverRegistryRecordFromStoreWithHealth(node controlplane.Node, worker controlplane.Worker, _ time.Time) registryServer {
	role := strings.TrimSpace(node.Role)
	if role == "" {
		role = registryFoundationRole
	}
	name := firstNonEmptyString(node.Name, node.WorkerID, node.ID)
	state := serverregistry.LegacySatelliteState()
	return registryServer{
		ID:              node.ID,
		KitDeploymentID: node.StackID,
		Name:            name,
		Hostname:        name,
		Role:            role,
		RoleLabel:       registryRoleLabel(role),
		WorkerID:        node.WorkerID,
		LeaseID:         firstNonEmptyString(stringFromAnyMap(node.Metadata, "lease_id"), stringFromAnyMap(worker.Capabilities, "lease_id")),
		Status:          string(state),
		HealthState:     string(state),
		LastSeen:        formatOptionalTime(worker.LastSeenAt),
		RolloutReady:    state == runtimehealth.ServerHealthy,
	}
}

// serverRegistryRecordFromWorkerStore shapes a `workers` row that has neither a
// canonical aggregate nor a `nodes` row. Same Wave 2 collapse as above: the
// worker heartbeat is satellite evidence, not a persisted runtime verdict, so
// it is reported as `last_seen` and nothing more.
func serverRegistryRecordFromWorkerStore(worker controlplane.Worker) registryServer {
	role := strings.TrimSpace(worker.Type)
	if role == "" {
		role = registryFoundationRole
	}
	name := firstNonEmptyString(worker.Hostname, worker.ID)
	state := serverregistry.LegacySatelliteState()
	return registryServer{
		ID:              worker.ID,
		KitDeploymentID: worker.StackID,
		Name:            name,
		Hostname:        name,
		Role:            role,
		RoleLabel:       registryRoleLabel(role),
		WorkerID:        worker.ID,
		LeaseID:         stringFromAnyMap(worker.Capabilities, "lease_id"),
		Status:          string(state),
		HealthState:     string(state),
		LastSeen:        formatOptionalTime(worker.LastSeenAt),
		RolloutReady:    worker.Approved && state == runtimehealth.ServerHealthy,
	}
}

func serviceRegistryRecordFromStore(service controlplane.Service, stack controlplane.Stack, node controlplane.Node) registryService {
	return serviceRegistryRecordFromStoreWithHealth(service, stack, node, controlplane.Worker{}, time.Now().UTC())
}

func serviceRegistryRecordFromStoreWithHealth(service controlplane.Service, stack controlplane.Stack, node controlplane.Node, worker controlplane.Worker, now time.Time) registryService {
	name := normalizeServiceKey(firstNonEmptyString(service.ServiceKey, service.Name, service.ID))
	status := firstNonEmptyString(service.Status, registryUnknownStatus)
	healthState, observedAt, accessFresh := registryStoreServiceHealthState(service, node, worker, now)
	migrationState := registryServiceActiveMigrationState(service)
	if migrationState != "" {
		status = migrationState
	} else {
		switch service.Source {
		case stackKitsInventorySource:
			status = string(healthState)
		case stackKitOutputKey:
			status = registryUnknownStatus
		}
	}
	managementState := registryStoreManagementState(service)
	displayName := firstNonEmptyString(stringFromAnyMap(service.Metadata, "display_name"), canonicalServiceDisplayName(name), name)
	moveAllowed, moveBlockedReason := registryServiceMoveEligibility(status, managementState)
	nodeName := firstNonEmptyString(node.Name, node.WorkerID, node.ID, service.NodeID)
	serviceURL := service.URL
	if migrationState != "" || (service.Source == stackKitsInventorySource && !accessFresh) {
		serviceURL = ""
	}
	return registryService{
		ID:                service.ID,
		Name:              name,
		DisplayName:       displayName,
		ApplicationKey:    name,
		ApplicationName:   displayName,
		Type:              firstNonEmptyString(stringFromAnyMap(service.Metadata, "type"), canonicalServiceType(name)),
		Status:            status,
		HealthState:       string(healthState),
		ObservedAt:        observedAt,
		ManagementState:   managementState,
		MigrationStatus:   strings.TrimSpace(service.MigrationStatus),
		PlacementScope:    registryPlacementScopeStack,
		MoveAllowed:       moveAllowed,
		MoveBlockedReason: moveBlockedReason,
		KitDeploymentID:   stack.ID,
		StackName:         firstNonEmptyString(stack.Name, stack.ID),
		ServerID:          firstNonEmptyString(node.ID, service.NodeID),
		ServerName:        nodeName,
		Port:              intFromAnyMap(service.Metadata, "port"),
		URL:               serviceURL,
	}
}

func registryStoreServiceHealthState(service controlplane.Service, node controlplane.Node, worker controlplane.Worker, now time.Time) (runtimehealth.ServiceState, string, bool) {
	if service.Source != stackKitsInventorySource {
		return runtimehealth.ServiceUnknown, "", false
	}
	observedAtRaw := stringFromAnyMap(service.Metadata, "observed_at")
	observedAt, observedErr := time.Parse(time.RFC3339Nano, observedAtRaw)
	observationAge := now.Sub(observedAt.UTC())
	observationFresh := observedErr == nil && observationAge >= -30*time.Second && observationAge <= runtimehealth.FreshHeartbeatWindow
	if !observationFresh {
		return runtimehealth.ServiceUnknown, observedAtRaw, false
	}
	serverState := runtimehealth.DeriveServerState(runtimehealth.ServerInput{
		Now:           now,
		HeartbeatAt:   worker.LastSeenAt,
		ObservedState: firstNonEmptyString(stringFromAnyMap(node.Metadata, "health_state"), node.Status),
	})
	if serverState != runtimehealth.ServerHealthy && serverState != runtimehealth.ServerDegraded {
		return runtimehealth.ServiceUnknown, observedAtRaw, false
	}
	health, _ := service.Metadata["health"].(map[string]any)
	endpointHealths := []string{}
	for _, endpoint := range sliceOfMapsFromAny(service.Metadata["endpoints"]) {
		endpointHealths = append(endpointHealths, stringFromAnyMap(endpoint, "health"))
	}
	return runtimehealth.DeriveServiceState(serverState, firstNonEmptyString(stringFromAnyMap(service.Metadata, "reported_status"), service.Status), health, endpointHealths), observedAtRaw, true
}

func registryServicesFromStackKitOutputs(outputs map[string]any, stack controlplane.Stack, servers []registryServer) []registryService {
	items := stackKitServiceOutputItems(outputs)
	if len(items) == 0 {
		return []registryService{}
	}
	services := make([]registryService, 0, len(items))
	for _, item := range items {
		name := normalizeServiceKey(firstNonEmptyString(stringFromAnyMap(item, "name"), stringFromAnyMap(item, "service_key"), stringFromAnyMap(item, "key")))
		if name == "" {
			continue
		}
		server := registryServerForStackKitOutput(item, servers)
		displayName := firstNonEmptyString(stringFromAnyMap(item, "display_name"), canonicalServiceDisplayName(name), name)
		// Generic StackKits job output is never health. The only exception is
		// the explicit, fresh, versioned runtime observation normalized by
		// stackKitServiceOutputItems.
		now := time.Now().UTC()
		status := stackKitObservedServiceStatus(item, now)
		// StackKit job output is by construction a rollout of ours, so its
		// ownership comes from the same source rule the aggregate applies.
		moveAllowed, moveBlockedReason := registryServiceMoveEligibility(status, stackKitOutputManagementState)
		services = append(services, registryService{
			ID:                firstNonEmptyString(stringFromAnyMap(item, "id"), stack.ID+":"+name),
			Name:              name,
			DisplayName:       displayName,
			ApplicationKey:    name,
			ApplicationName:   displayName,
			Type:              firstNonEmptyString(stringFromAnyMap(item, "type"), canonicalServiceType(name)),
			Status:            status,
			HealthState:       status,
			ObservedAt:        stringFromAnyMap(item, "observed_at"),
			ManagementState:   stackKitOutputManagementState,
			MigrationStatus:   strings.TrimSpace(stringFromAnyMap(item, "migration_status")),
			PlacementScope:    registryPlacementScopeStack,
			MoveAllowed:       moveAllowed,
			MoveBlockedReason: moveBlockedReason,
			KitDeploymentID:   stack.ID,
			StackName:         firstNonEmptyString(stack.Name, stack.ID),
			ServerID:          server.ID,
			ServerName:        firstNonEmptyString(server.Name, server.Hostname),
			Port:              intFromAnyMap(item, "port"),
			URL:               stackKitObservedServiceURL(item, now),
		})
	}
	return services
}

func registryServerForStackKitOutput(item map[string]any, servers []registryServer) registryServer {
	target := firstNonEmptyString(stringFromAnyMap(item, "target_server"), stringFromAnyMap(item, "target_server_id"), stringFromAnyMap(item, "server"), stringFromAnyMap(item, "node"), stringFromAnyMap(item, "node_id"))
	for _, server := range servers {
		if strings.EqualFold(target, server.ID) || strings.EqualFold(target, server.Name) || strings.EqualFold(target, server.Hostname) || strings.EqualFold(target, server.WorkerID) {
			return server
		}
	}
	if len(servers) == 1 || target == "" && len(servers) > 0 {
		return servers[0]
	}
	return registryServer{}
}

func stringFromAnyMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func intFromAnyMap(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		if value > uint64(math.MaxInt) {
			return math.MaxInt
		}
		return int(value) // #nosec G115 -- value is clamped to the platform int range above.
	case float32:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func registryRoleLabel(role string) string {
	switch role {
	case registryFoundationRole, "main", "standalone", "control-plane":
		return "Foundation Node"
	case "storage":
		return "Storage Node"
	default:
		return "Worker Node"
	}
}

func registryServiceCatalog() []registryCatalogService {
	return []registryCatalogService{
		{ID: registryServicePocketID, DisplayName: "Pocket ID", Type: registryServiceTypeAuth, Description: "Identity head for StackKit login and owner activation.", Required: true, Foundations: []string{registryBaseKit}},
		{ID: registryServiceTraefik, DisplayName: "Traefik", Type: "reverse-proxy", Description: "Ingress and routing layer for StackKit services.", Required: true, Foundations: []string{registryBaseKit}},
		{ID: registryServiceMonitor, DisplayName: "Monitoring", Type: registryServiceMonitor, Description: "OTLP collector and service health baseline.", Recommended: true, Foundations: []string{registryBaseKit}},
		{ID: registryServiceVault, DisplayName: "Vaultwarden", Type: registryServiceTypeAuth, Description: "Password vault managed by the StackKit gateway.", Recommended: true, Foundations: []string{registryBaseKit}},
		{ID: registryServiceImmich, DisplayName: "Immich", Type: "media", Description: "Photo and video service with supporting database/cache services.", Recommended: true, Foundations: []string{registryBaseKit}},
		{ID: registryServiceFiles, DisplayName: "Files", Type: "storage", Description: "File storage service slot for the StackKit storage module.", Foundations: []string{registryBaseKit}},
	}
}

func registryCatalogServiceByID(id string) (registryCatalogService, bool) {
	id = normalizeServiceKey(id)
	for _, service := range registryServiceCatalog() {
		if normalizeServiceKey(service.ID) == id {
			return service, true
		}
	}
	return registryCatalogService{}, false
}

func (h registryRouteHandlers) migrateService(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	var req struct {
		ServiceID      string `json:"service_id"`
		TargetServerID string `json:"target_server_id"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.TargetServerID = strings.TrimSpace(req.TargetServerID)
	if req.ServiceID == "" || req.TargetServerID == "" {
		return httpx.BadRequest(e, "Service ID and target Server ID are required", nil)
	}
	tenantID, err := h.requireRegistryMutationTenant(e, ownerID, "techstack.registry.services.migrate")
	if err != nil {
		return err
	}
	// Fail closed until this route is backed by a durable runtime workflow.
	// The retired metadata-only implementation copied a row and completed a job
	// without deploying, probing, cutting over, or draining a workload.
	if !registryMigrationRuntimeAvailable() {
		return registryMigrationUnavailable(e)
	}
	handled, err := h.migrateServiceFromStore(e, tenantID, ownerID, req.ServiceID, req.TargetServerID)
	if !handled && err == nil {
		return httpx.NotFound(e, "Service not found")
	}
	return err
}

func (h registryRouteHandlers) verifyService(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	var req struct {
		ServiceID string `json:"service_id"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	if req.ServiceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	tenantID, err := h.requireRegistryMutationTenant(e, ownerID, "techstack.registry.services.verify")
	if err != nil {
		return err
	}
	// Manual confirmation must never manufacture a healthy target. A future
	// executor will replace this guard only after current target inventory and
	// health evidence have been verified.
	if !registryMigrationRuntimeAvailable() {
		return registryMigrationUnavailable(e)
	}
	handled, err := h.verifyServiceFromStore(e, tenantID, ownerID, req.ServiceID)
	if !handled && err == nil {
		return httpx.NotFound(e, "Service not found")
	}
	return err
}

func (h registryRouteHandlers) deleteService(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	serviceID := e.Request.PathValue("id")
	if serviceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	tenantID, err := h.requireRegistryMutationTenant(e, ownerID, "techstack.registry.services.delete")
	if err != nil {
		return err
	}
	handled, err := h.deleteServiceFromStore(e, tenantID, ownerID, serviceID)
	if !handled && err == nil {
		return httpx.NotFound(e, "Service not found")
	}
	return err
}

func (h registryRouteHandlers) requireRegistryMutationTenant(e *httpx.Event, ownerID, capability string) (string, error) {
	tenantID, err := requireRegistryRouteTenant(e, ownerID, capability)
	if err != nil {
		return "", err
	}
	if h.registryStore == nil || h.stackStore == nil {
		return "", httpx.NewInternalServerError("Canonical registry stores are unavailable", nil)
	}
	return tenantID, nil
}

func (h registryRouteHandlers) migrateServiceFromStore(e *httpx.Event, tenantID, ownerID, serviceID, targetServerID string) (bool, error) {
	if blocked, response := h.blockUnfencedCanonicalServiceMutation(e, "migrate"); blocked {
		return true, response
	}
	ctx := e.Request.Context()
	service, err := h.registryStore.GetService(ctx, tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch service", nil)
	}
	sourceNode, err := h.registryStore.GetNode(ctx, tenantID, service.NodeID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Source server not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch source server", nil)
	}
	stack, err := h.stackStore.GetStack(ctx, tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return true, httpx.Forbidden(e, "Not your stack")
	}
	targetNode, err := h.registryStore.GetNode(ctx, tenantID, targetServerID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Target server not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch target server", nil)
	}
	if targetNode.StackID != stack.ID {
		return true, httpx.BadRequest(e, "Target server must belong to the same stack", nil)
	}
	if targetNode.ID == sourceNode.ID {
		return true, httpx.BadRequest(e, "Target server must be different from the source server", nil)
	}

	moveAllowed, moveBlockedReason := registryStoreServiceMoveEligibility(*service)
	if !moveAllowed {
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Application cannot be moved", map[string]any{
			"service_id": serviceID,
			"status":     service.Status,
			"reason":     moveBlockedReason,
		})
	}
	if duplicate := h.activeServiceOnStoreNode(ctx, tenantID, stack.ID, targetNode.ID, registryStoreServiceKey(*service)); duplicate != nil {
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Target server already has an active service with this name", map[string]any{
			"service_id":        serviceID,
			"target_server_id":  targetServerID,
			"target_service_id": duplicate.ID,
		})
	}

	source := *service
	source.Status = registryStatusMigrating
	source.MigrationStatus = registryStatusMigrating
	source.URL = ""
	updatedSource, err := h.registryStore.UpsertService(ctx, source)
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update source service status", nil)
	}

	target := controlplane.Service{
		ID:              runtimeidentity.ServiceID(stack.ID, targetNode.ID, registryStoreServiceKey(*service), "default"),
		TenantID:        tenantID,
		InstanceID:      firstNonEmptyString(service.InstanceID, stack.InstanceID),
		StackID:         stack.ID,
		NodeID:          targetNode.ID,
		ServiceKey:      registryStoreServiceKey(*service),
		Name:            firstNonEmptyString(service.Name, service.ServiceKey),
		Status:          registryStatusPendingVerification,
		Source:          firstNonEmptyString(service.Source, stackKitOutputKey),
		URL:             "",
		MigrationStatus: registryStatusPendingVerification,
		Metadata:        cloneRegistryMetadata(service.Metadata),
	}
	target.Metadata["migrated_from_service_id"] = service.ID
	target.Metadata["migrated_from_node_id"] = sourceNode.ID
	updatedTarget, err := h.registryStore.UpsertService(ctx, target)
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create target service", nil)
	}

	jobID, err := h.createServiceMigrationJobFromStore(ctx, tenantID, *stack, *updatedSource, *updatedTarget)
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create migration job", nil)
	}

	return true, httpx.Success(e, http.StatusOK, map[string]any{
		"job_id":         jobID,
		"source_service": serviceRegistryRecordFromStore(*updatedSource, *stack, *sourceNode),
		"target_service": serviceRegistryRecordFromStore(*updatedTarget, *stack, *targetNode),
	})
}

func (h registryRouteHandlers) verifyServiceFromStore(e *httpx.Event, tenantID, ownerID, serviceID string) (bool, error) {
	if blocked, response := h.blockUnfencedCanonicalServiceMutation(e, "verify"); blocked {
		return true, response
	}
	ctx := e.Request.Context()
	service, err := h.registryStore.GetService(ctx, tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch service", nil)
	}
	node, err := h.registryStore.GetNode(ctx, tenantID, service.NodeID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch server", nil)
	}
	stack, err := h.stackStore.GetStack(ctx, tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return true, httpx.Forbidden(e, "Not your stack")
	}
	if service.Status != registryStatusPendingVerification {
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Only applications pending verification can be finished", map[string]any{
			"service_id": serviceID,
			"status":     service.Status,
		})
	}

	verified := *service
	verified.Status = registryStatusRunning
	verified.MigrationStatus = ""
	updatedService, err := h.registryStore.UpsertService(ctx, verified)
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update service status", nil)
	}

	var archivedService *controlplane.Service
	var archivedNode *controlplane.Node
	services, listErr := h.registryStore.ListServicesByStack(ctx, tenantID, stack.ID)
	if listErr == nil {
		serviceKey := registryStoreServiceKey(*service)
		for _, candidate := range services {
			if candidate.ID == service.ID || candidate.NodeID == node.ID {
				continue
			}
			if registryStoreServiceKey(candidate) != serviceKey || candidate.Status != registryStatusMigrating {
				continue
			}
			candidate.Status = registryStatusArchived
			candidate.MigrationStatus = registryStatusArchived
			saved, saveErr := h.registryStore.UpsertService(ctx, candidate)
			if saveErr != nil {
				continue
			}
			archivedService = saved
			if n, nErr := h.registryStore.GetNode(ctx, tenantID, saved.NodeID); nErr == nil {
				archivedNode = n
			}
			break
		}
	}

	var archivedServiceData any
	if archivedService != nil && archivedNode != nil {
		archivedServiceData = serviceRegistryRecordFromStore(*archivedService, *stack, *archivedNode)
	}
	return true, httpx.Success(e, http.StatusOK, map[string]any{
		"service":          serviceRegistryRecordFromStore(*updatedService, *stack, *node),
		"archived_service": archivedServiceData,
	})
}

func (h registryRouteHandlers) deleteServiceFromStore(e *httpx.Event, tenantID, ownerID, serviceID string) (bool, error) {
	if blocked, response := h.blockUnfencedCanonicalServiceMutation(e, "delete"); blocked {
		return true, response
	}
	ctx := e.Request.Context()
	service, err := h.registryStore.GetService(ctx, tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch service", nil)
	}
	_, err = h.registryStore.GetNode(ctx, tenantID, service.NodeID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch server", nil)
	}
	stack, err := h.stackStore.GetStack(ctx, tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return true, httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
	}
	if stack.OwnerSubjectID != ownerID {
		return true, httpx.Forbidden(e, "Not your stack")
	}
	if service.Status != registryStatusArchived {
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Only archived application copies can be deleted from the Registry", map[string]any{
			"service_id": serviceID,
			"status":     service.Status,
		})
	}
	if err := h.registryStore.DeleteService(ctx, tenantID, serviceID); err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to delete service", nil)
	}
	return true, httpx.Success(e, http.StatusOK, map[string]any{
		"message": "Service successfully deleted",
		"id":      serviceID,
	})
}

// blockUnfencedCanonicalServiceMutation keeps the current canonical
// PostgreSQL/Memory inventory lane read-only for service-control transitions.
// Those transitions must join the same server-row fence as Guard before they
// can safely change migration/archival state; the former multi-transaction
// Registry sequence could race an authoritative inventory prune.
func (h registryRouteHandlers) blockUnfencedCanonicalServiceMutation(e *httpx.Event, action string) (bool, error) {
	if h.registryStore == nil {
		return false, nil
	}
	if _, canonical := h.registryStore.(controlplane.GuardInventoryProjectionStore); !canonical {
		return false, nil
	}
	return true, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal,
		"Service control is temporarily unavailable until the atomic server fence is active",
		map[string]any{"action": action, "reason": "service_control_server_fence_unavailable"})
}

// registryStoreManagementState is the ONE read of the persisted management
// dimension for store-backed rows. It never recomputes ownership from `source`
// or `status`: the aggregate write boundary resolved that once and persisted it
// (migration 074). An empty value can only come from a row written before that
// migration, and it fails closed to `observed` rather than claiming a contract
// that was never declared.
func registryStoreManagementState(service controlplane.Service) string {
	if strings.TrimSpace(service.ManagementState) == "" {
		return registryObservedState
	}
	return string(serviceregistry.CanonicalManagementState(service.ManagementState))
}

func registryStoreServiceMoveEligibility(service controlplane.Service) (bool, string) {
	return registryServiceMoveEligibility(
		firstNonEmptyString(service.Status, registryUnknownStatus),
		registryStoreManagementState(service),
	)
}

func registryServiceActiveMigrationState(service controlplane.Service) string {
	for _, state := range []string{service.MigrationStatus, service.Status} {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case registryStatusMigrating, registryStatusDeploying, registryStatusPendingVerification:
			return strings.ToLower(strings.TrimSpace(state))
		}
	}
	return ""
}

func (h registryRouteHandlers) activeServiceOnStoreNode(ctx context.Context, tenantID, stackID, nodeID, serviceName string) *controlplane.Service {
	serviceName = normalizeServiceKey(serviceName)
	if serviceName == "" {
		return nil
	}
	services, err := h.registryStore.ListServicesByStack(ctx, tenantID, stackID)
	if err != nil {
		return nil
	}
	for _, service := range services {
		if service.NodeID != nodeID {
			continue
		}
		if normalizeServiceKey(registryStoreServiceKey(service)) != serviceName {
			continue
		}
		if service.Status == registryStatusArchived {
			continue
		}
		candidate := service
		return &candidate
	}
	return nil
}

func (h registryRouteHandlers) createServiceMigrationJobFromStore(ctx context.Context, tenantID string, stack controlplane.Stack, source, target controlplane.Service) (string, error) {
	if h.jobStore == nil {
		return "", nil
	}
	jobID := registryStoreServiceID("job", tenantID, stack.ID, source.ID, target.ID)
	job, err := h.jobStore.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:         jobID,
		TenantID:   tenantID,
		InstanceID: firstNonEmptyString(stack.InstanceID, source.InstanceID, target.InstanceID),
		StackID:    stack.ID,
		Type:       "update",
		State:      "completed",
		Priority:   0,
		Progress:   100,
		Step:       "service-migration",
		Message:    "Service migration handoff recorded; verify the target service when the runtime is healthy.",
		Result: map[string]any{
			"application_key":   registryStoreServiceKey(source),
			"kind":              "service_migration",
			"source_service_id": source.ID,
			"source_server_id":  source.NodeID,
			"target_service_id": target.ID,
			"target_server_id":  target.NodeID,
			"service_name":      registryStoreServiceKey(source),
		},
		ScheduledFor: time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

func registryStoreServiceKey(service controlplane.Service) string {
	return normalizeServiceKey(firstNonEmptyString(service.ServiceKey, service.Name, service.ID))
}

func registryStoreServiceID(prefix string, values ...string) string {
	parts := append([]string{prefix}, values...)
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		var b strings.Builder
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r)
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
					b.WriteRune('-')
				}
			}
		}
		item := strings.Trim(b.String(), "-")
		if item != "" {
			clean = append(clean, item)
		}
	}
	return strings.Join(clean, "-")
}

func cloneRegistryMetadata(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func registryServiceMoveEligibility(status, managementState string) (bool, string) {
	// The Registry currently has no runtime migration executor. Keeping every
	// service immovable prevents the UI and direct API clients from presenting a
	// metadata handoff as a completed workload migration.
	if !registryMigrationRuntimeAvailable() {
		return false, registryMigrationUnavailableReason
	}
	status = strings.TrimSpace(status)
	managementState = strings.TrimSpace(managementState)
	if managementState == registryObservedState {
		return false, "Observed unmanaged applications must be adopted before they can be moved."
	}
	switch status {
	case registryStatusRunning, registryStatusStopped, string(runtimehealth.ServiceHealthy):
		return true, ""
	case preCheckStatusPending:
		return false, "Application rollout must finish before it can be moved."
	case registryStatusMigrating, registryStatusDeploying, registryStatusPendingVerification:
		return false, "Application is already moving or waiting for verification."
	case registryStatusArchived:
		return false, "Archived application copies cannot be moved."
	case registryStatusError:
		return false, "Resolve the application error before moving it."
	default:
		return false, "Application needs a stable running or stopped state before it can be moved."
	}
}

func registryMigrationUnavailable(e *httpx.Event) error {
	return httpx.Error(e, http.StatusNotImplemented, ksapi.ErrCodeUnavailable, "Runtime service migration is unavailable", map[string]any{
		"reason": registryMigrationUnavailableReason,
	})
}

// registryMigrationRuntimeAvailable stays false until the route is wired to a
// durable runtime executor. It is a function (rather than a user-controlled
// flag) so no deployment can accidentally re-enable the legacy metadata-only
// transition.
func registryMigrationRuntimeAvailable() bool {
	return false
}
