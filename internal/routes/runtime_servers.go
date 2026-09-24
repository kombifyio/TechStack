package routes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type ServerRuntimeRouteConfig struct {
	Store           controlplane.ServerRuntimeStore
	PortInventory   portinventory.ReadAuthority
	Detacher        controlplane.SelfOwnedServerDetacher
	Policy          InventoryPolicy
	AgentDisconnect func(string) error
	Now             func() time.Time
}

type serverRuntimeHandlers struct {
	store           controlplane.ServerRuntimeStore
	ports           portinventory.ReadAuthority
	detacher        controlplane.SelfOwnedServerDetacher
	policy          InventoryPolicy
	agentDisconnect func(string) error
	now             func() time.Time
}

type serverRuntimeResponse struct {
	ID              string `json:"id"`
	NodeID          string `json:"node_id"`
	KitDeploymentID string `json:"kit_deployment_id,omitempty"`
	Name            string `json:"name"`
	// WorkerID is the bound Guard agent identity. It is additive on this
	// response and exists because the canonical read model is now the UI's only
	// server source (kombify-Techstack-nzy1.7): the pairing flow has to be able
	// to tell "a Guard agent is bound to this aggregate" apart from "a server
	// row exists" without consulting a secondary Registry projection.
	WorkerID          string                       `json:"worker_id,omitempty"`
	NodeRole          string                       `json:"node_role,omitempty"`
	Lifecycle         serverRuntimeLifecycle       `json:"lifecycle"`
	Connection        serverRuntimeConnection      `json:"connection"`
	Health            serverRuntimeHealth          `json:"health"`
	Channels          []controlplane.ServerChannel `json:"channels"`
	InventoryRevision int64                        `json:"inventory_revision"`
	Provider          serverRuntimeProvider        `json:"provider"`
	// EnvironmentClass and Offering are the canonical hosting classification.
	// Managed VPS remains cloud/managed_vps; provider-native managed workloads
	// do not appear in this server response at all.
	EnvironmentClass  string                      `json:"environment_class"`
	Offering          string                      `json:"offering,omitempty"`
	ProviderID        string                      `json:"provider_id,omitempty"`
	ProviderTargetRef string                      `json:"provider_target_ref,omitempty"`
	AvailabilityOwner string                      `json:"availability_owner,omitempty"`
	OperationsOwner   string                      `json:"operations_owner,omitempty"`
	TargetEvidence    serverRuntimeTargetEvidence `json:"target_evidence"`
	LastOutcome       *outcome.Decision           `json:"last_outcome,omitempty"`
	MutationsAllowed  bool                        `json:"mutations_allowed"`
	// AllowedActions is the node-scoped capability contract: exactly the
	// operations this server's own state admits, each backed by an endpoint that
	// exists. StackActions is the separate StackKit-deployment scope. They are
	// never merged - a node operation and a kit operation have different blast
	// radius, different authority and different failure modes.
	AllowedActions []string  `json:"allowed_actions"`
	StackActions   []string  `json:"stack_actions"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type serverRuntimeLifecycle struct {
	State        string     `json:"state"`
	DesiredState string     `json:"desired_state"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

type serverRuntimeConnection struct {
	State            string     `json:"state"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	ChangedAt        time.Time  `json:"changed_at"`
	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at,omitempty"`
	StalenessSeconds *int64     `json:"staleness_seconds,omitempty"`
}

type serverRuntimeHealth struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

type serverRuntimeProvider struct {
	LeaseID string `json:"lease_id,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

// serverRuntimeTargetEvidence describes the freshness of the classification
// evidence only. "recorded" is not an availability or SLA assertion.
type serverRuntimeTargetEvidence struct {
	Ref        string                       `json:"ref,omitempty"`
	ObservedAt *time.Time                   `json:"observed_at,omitempty"`
	Freshness  serverRuntimeTargetFreshness `json:"freshness"`
}

type serverRuntimeTargetFreshness struct {
	State      string `json:"state"`
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
}

func RegisterServerRuntimeRoutes(r *httpx.Router, cfg ServerRuntimeRouteConfig) {
	if cfg.Store == nil {
		panic("RegisterServerRuntimeRoutes: server runtime store required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Detacher != nil && cfg.Policy == nil {
		panic("RegisterServerRuntimeRoutes: inventory policy required for server detach")
	}
	h := serverRuntimeHandlers{
		store: cfg.Store, ports: cfg.PortInventory, detacher: cfg.Detacher, policy: cfg.Policy,
		agentDisconnect: cfg.AgentDisconnect, now: cfg.Now,
	}
	r.GET("/api/v1/servers", h.list)
	r.GET("/api/v1/servers/{serverId}", h.get)
	r.GET("/api/v1/servers/{serverId}/ports", h.portInventory)
	r.GET("/api/v1/servers/{serverId}/transitions", h.transitions)
	if cfg.Detacher != nil {
		r.POST("/api/v1/servers/{serverId}/detach", h.detach)
	}
}

type selfOwnedServerDetachBody struct {
	ConfirmServerID string `json:"confirm_server_id"`
}

func (h serverRuntimeHandlers) detach(e *httpx.Event) error {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.servers.detach")
	if tenantErr != nil {
		return tenantErr
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return httpx.BadRequest(e, "Server ID is required", nil)
	}
	if err := authorizeInventoryServerOperate(e.Request.Context(), h.policy, inventoryScope{tenantID: tenantID, ownerID: ownerID}, serverID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	var body selfOwnedServerDetachBody
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return httpx.BadRequest(e, "Exact server detach confirmation is required", nil)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return httpx.BadRequest(e, "Server detach request must contain one JSON object", nil)
	}
	receipt, err := h.detacher.DetachSelfOwnedServer(e.Request.Context(), controlplane.SelfOwnedServerDetachRequest{
		TenantID: tenantID, OwnerSubjectID: ownerID,
		ServerID: serverID, ConfirmServerID: body.ConfirmServerID,
	})
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return httpx.NotFound(e, "Server not found")
	case errors.Is(err, controlplane.ErrServerDetachConfirmation):
		return httpx.BadRequest(e, "Exact server detach confirmation is required", nil)
	case errors.Is(err, controlplane.ErrServerDetachUnsupported):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Only customer-operated BYO servers can be detached", nil)
	case errors.Is(err, controlplane.ErrServerAgentCustody):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The exact server Agent enrollment is unavailable", nil)
	case err != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server detach is unavailable", nil)
	default:
		if h.agentDisconnect != nil {
			_ = h.agentDisconnect(receipt.AgentID)
		}
		return httpx.Success(e, http.StatusOK, receipt)
	}
}

// list returns the caller's own servers. The store read is tenant-scoped, and a
// tenant can hold several unrelated owners, so the owner filter below is the
// isolation boundary and applies to every caller. An operator role does not
// widen it here: the customer inventory surface has no admin mode, and
// cross-owner inspection belongs to an explicit operator surface with its own
// audit trail.
func (h serverRuntimeHandlers) list(e *httpx.Event) error {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.servers.list")
	if tenantErr != nil {
		return tenantErr
	}
	kitDeploymentID := strings.TrimSpace(e.Request.URL.Query().Get("kit_deployment_id"))
	rows, err := h.store.ListServerRuntimesByTenant(e.Request.Context(), tenantID, kitDeploymentID)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	items := make([]serverRuntimeResponse, 0, len(rows))
	for _, row := range rows {
		if !serverRuntimeOwnedBy(row, ownerID) || serverRuntimeHiddenFromCurrentInventory(row) {
			continue
		}
		items = append(items, h.response(row))
	}
	qualifyServerRuntimeDisplayNames(items)
	sort.SliceStable(items, func(i, j int) bool {
		return serverRuntimeDisplayPriority(items[i]) < serverRuntimeDisplayPriority(items[j])
	})
	return httpx.Success(e, http.StatusOK, items)
}

func qualifyServerRuntimeDisplayNames(items []serverRuntimeResponse) {
	names := make([]string, len(items))
	qualifiers := make([]string, len(items))
	for i, item := range items {
		names[i] = item.Name
		qualifiers[i] = serverregistry.DisplayQualifier(item.ProviderID, item.Offering, item.ID)
	}
	for i, name := range qualifyCollidingServerNames(names, qualifiers) {
		items[i].Name = name
	}
}

func serverRuntimeDisplayPriority(server serverRuntimeResponse) int {
	lifecycle := strings.ToLower(strings.TrimSpace(server.Lifecycle.State))
	connection := strings.ToLower(strings.TrimSpace(server.Connection.State))
	health := strings.ToLower(strings.TrimSpace(server.Health.State))
	switch {
	case lifecycle == string(serverregistry.LifecycleActive) && connection == string(serverregistry.ConnectionConnected) && health == string(serverregistry.HealthHealthy):
		return 0
	case lifecycle == string(serverregistry.LifecycleActive) && connection == string(serverregistry.ConnectionConnected):
		return 1
	case lifecycle == string(serverregistry.LifecycleActive):
		return 2
	case lifecycle == string(serverregistry.LifecycleEnrolling) || lifecycle == string(serverregistry.LifecycleProvisioning) || lifecycle == string(serverregistry.LifecyclePlanned):
		return 3
	case lifecycle == string(serverregistry.LifecycleFailed):
		return 4
	case lifecycle == string(serverregistry.LifecycleDecommissioning):
		return 5
	default:
		return 6
	}
}

// serverRuntimeOwnedBy is the single owner predicate for this surface. It is
// fail-closed: a row without a recorded owner belongs to nobody and is never
// served, so an unbackfilled row cannot become tenant-wide readable.
func serverRuntimeOwnedBy(server controlplane.ServerRuntime, ownerID string) bool {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return false
	}
	return strings.TrimSpace(server.OwnerSubjectID) == ownerID
}

func serverRuntimeIsTerminalTombstone(server controlplane.ServerRuntime) bool {
	return strings.EqualFold(strings.TrimSpace(server.LifecycleState), string(serverregistry.LifecycleDecommissioned)) &&
		strings.EqualFold(strings.TrimSpace(server.DesiredState), "absent")
}

func serverRuntimeHiddenFromCurrentInventory(server controlplane.ServerRuntime) bool {
	return serverRuntimeIsTerminalTombstone(server) ||
		strings.EqualFold(strings.TrimSpace(server.LifecycleState), string(serverregistry.LifecycleDecommissioned))
}

func (h serverRuntimeHandlers) get(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.read")
	if err != nil || server == nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, h.response(*server))
}

func (h serverRuntimeHandlers) portInventory(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.read")
	if err != nil || server == nil {
		return err
	}
	if h.ports == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Port inventory is unavailable", nil)
	}
	result, err := h.ports.ReadCurrent(e.Request.Context(), portinventory.InventoryRequest{
		TenantID: server.TenantID, ServerID: server.ID, OwnerSubjectID: server.OwnerSubjectID,
	}, h.now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Port inventory is unavailable", nil)
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, result)
}

func (h serverRuntimeHandlers) transitions(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.transitions")
	if err != nil || server == nil {
		return err
	}
	rows, err := h.store.ListServerTransitions(e.Request.Context(), server.TenantID, server.ID, 100)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server history is unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, rows)
}

func (h serverRuntimeHandlers) ownedServer(e *httpx.Event, capability string) (*controlplane.ServerRuntime, error) {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return nil, httpx.Unauthorized(e, "Authentication required")
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return nil, httpx.BadRequest(e, "Server ID is required", nil)
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if tenantErr != nil {
		return nil, tenantErr
	}
	server, err := h.store.GetServerRuntime(e.Request.Context(), tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	if !serverRuntimeOwnedBy(*server, ownerID) {
		return nil, httpx.NotFound(e, "Server not found")
	}
	return server, nil
}

// response returns the persisted observed state. The registry sweeper is the
// demotion authority (heartbeat freshness becomes durable connection/health
// writes through ApplyServerEvent); read-time recompute overrides are gone so
// API responses, transitions, and the aggregate head always agree.
// DeriveObservedState remains a test-only cross-check for fresh heartbeats.
func (h serverRuntimeHandlers) response(server controlplane.ServerRuntime) serverRuntimeResponse {
	now := h.now().UTC()
	connection, health := server.ConnectionState, server.HealthState
	var staleness *int64
	if server.LastHeartbeatAt != nil {
		seconds := int64(now.Sub(server.LastHeartbeatAt.UTC()).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		staleness = &seconds
	}
	target := serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	if !serverregistry.RuntimeTargetIntentPresent(target) {
		target = serverregistry.UnknownRuntimeTarget()
	}
	targetFreshness := serverRuntimeTargetFreshness{State: "unknown"}
	if target.EvidenceRef != "" && target.ObservedAt != nil {
		seconds := int64(now.Sub(target.ObservedAt.UTC()).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		targetFreshness = serverRuntimeTargetFreshness{State: "recorded", AgeSeconds: &seconds}
	}
	return serverRuntimeResponse{
		ID: server.ID, NodeID: server.ID, KitDeploymentID: server.StackID,
		Name: serverRuntimeDisplayName(server, target), WorkerID: server.WorkerID,
		NodeRole:   stringFromAnyMap(server.Metadata, "server_node_role"),
		Lifecycle:  serverRuntimeLifecycle{State: server.LifecycleState, DesiredState: server.DesiredState, EndedAt: server.DecommissionedAt},
		Connection: serverRuntimeConnection{State: connection, ReasonCode: server.ReasonCode, ChangedAt: server.ConnectionChangedAt, LastHeartbeatAt: server.LastHeartbeatAt, StalenessSeconds: staleness},
		Health:     serverRuntimeHealth{State: health, ObservedAt: server.LastHeartbeatAt},
		Channels:   server.Channels, InventoryRevision: server.InventoryRevision,
		Provider:         serverRuntimeProvider{LeaseID: server.LeaseID, Ref: server.ProviderRef},
		EnvironmentClass: string(target.EnvironmentClass), Offering: string(target.Offering),
		ProviderID: target.ProviderID, ProviderTargetRef: target.ProviderTargetRef,
		AvailabilityOwner: string(target.AvailabilityOwner), OperationsOwner: string(target.OperationsOwner),
		TargetEvidence: serverRuntimeTargetEvidence{
			Ref: target.EvidenceRef, ObservedAt: target.ObservedAt, Freshness: targetFreshness,
		},
		LastOutcome:      outcome.Clone(server.LastOutcome),
		MutationsAllowed: serverregistry.MutationsAllowed(connection) && server.LifecycleState == string(serverregistry.LifecycleActive),
		AllowedActions:   serverNodeActions(server, h.detacher != nil),
		StackActions:     serverStackActions(server),
		CreatedAt:        server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}
