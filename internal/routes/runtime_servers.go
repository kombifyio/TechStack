package routes

import (
	"context"
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
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
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
	// WalletWithdraw tells the owner's kombify Wallet that a detached server
	// is gone, so the Wallet stops delivering to its node and marks the items
	// it held for rotation. Nil (no Wallet) leaves the detach answer without
	// a wallet_withdrawal member.
	WalletWithdraw ServerWalletWithdrawer
	// Maintenance registers POST /api/v1/servers/{serverId}/actions when set.
	Maintenance *ServerMaintenanceRouteConfig
	// Reach adds the kombify Guard reach state to the server read model. Nil in
	// editions without a Cloud connection; the field is then absent.
	Reach ServerReachReader
	// Posture adds the kombify Guard security, recovery and update posture to
	// the server read model and serves the security routes. Nil in editions
	// without posture; the fields are then absent.
	Posture ServerPosture
	// Workers adds the Guard's reported capabilities (identity binding, durable
	// operations, isolation and update posture, credential generation) to the
	// server read model. Nil leaves the section absent.
	Workers ServerWorkerReader
	// CurrentMTLSCapabilities distinguishes a present transport with no ABI
	// grant from an absent transport. HTTPS reports are considered only absent it.
	CurrentMTLSCapabilities func(controlplane.ServerRuntime) ([]string, bool)
	// PostureEntitlements gates the repair routes; nil is the fail-closed hosted
	// gate.
	PostureEntitlements EntitlementGate
	// Network serves the owner's home network move routes
	// (GET /servers/{id}/network and POST /servers/{id}/network/rebind). Nil
	// leaves them unregistered.
	Network *ServerNetworkRouteConfig
}

// ServerWalletWithdrawer withdraws a detached server's node from the owner's
// kombify Wallet by the server's Agent id. Repeating it is harmless.
type ServerWalletWithdrawer interface {
	HostWithdraw(ctx context.Context, owner, agentID string) (kombifywallet.HostWithdrawal, error)
}

// serverWalletWithdrawalTimeout bounds the Wallet call after a detach that
// has already committed; the detach never waits longer for it.
const serverWalletWithdrawalTimeout = 10 * time.Second

// selfOwnedServerDetachResponse is the detach receipt and, with a Wallet,
// what the Wallet recorded for the server.
type selfOwnedServerDetachResponse struct {
	*controlplane.SelfOwnedServerDetachReceipt
	WalletWithdrawal *serverWalletWithdrawal `json:"wallet_withdrawal,omitempty"`
}

// serverWalletWithdrawal reports the Wallet bookkeeping of a detach: state
// "recorded" with the item counts, or "failed" with the Wallet's reason. A
// failure leaves the detach in place; repeating the detach retries it.
type serverWalletWithdrawal struct {
	State             string `json:"state"`
	TargetsRemoved    int    `json:"targets_removed"`
	ItemsNeedRotation int    `json:"items_need_rotation"`
	ReasonCode        string `json:"reason_code,omitempty"`
	Retryable         bool   `json:"retryable,omitempty"`
}

// ServerReachReader reads the persisted kombify Guard reach rows of a tenant.
type ServerReachReader interface {
	ListByTenant(ctx context.Context, tenantID string) ([]guardreach.Row, error)
}

type serverRuntimeHandlers struct {
	store           controlplane.ServerRuntimeStore
	ports           portinventory.ReadAuthority
	detacher        controlplane.SelfOwnedServerDetacher
	policy          InventoryPolicy
	agentDisconnect func(string) error
	now             func() time.Time
	maintenance     *serverMaintenanceHandlers
	reach           ServerReachReader
	walletWithdraw  ServerWalletWithdrawer

	posture                 ServerPosture
	postureEntitlements     EntitlementGate
	workers                 ServerWorkerReader
	currentMTLSCapabilities func(controlplane.ServerRuntime) ([]string, bool)
	network                 *ServerNetworkRouteConfig
}

type serverRuntimeResponse struct {
	ID              string `json:"id"`
	NodeID          string `json:"node_id"`
	KitDeploymentID string `json:"kit_deployment_id,omitempty"`
	Name            string `json:"name"`
	// DisplayName is the owner-chosen name when one is set; Name already
	// reports it, so clients can tell a rename apart from a projected name.
	DisplayName string `json:"display_name,omitempty"`
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
	AllowedActions []string `json:"allowed_actions"`
	StackActions   []string `json:"stack_actions"`
	// Reach is the persisted kombify Guard reach state: whether kombify.me
	// answers for this server and whether Connect lists it. It is never derived
	// client-side, and it never blocks or changes Guard enrollment.
	Reach *guardreach.View `json:"reach,omitempty"`
	// Security, Recovery and Updates are the persisted kombify Guard posture.
	// Nothing in them is derived from the heartbeat: a connected, healthy server
	// without fresh evidence is unknown, and a snapshot's age never says that a
	// restore works.
	Security *guardposture.Security `json:"security,omitempty"`
	Recovery *guardposture.Recovery `json:"recovery,omitempty"`
	Updates  *guardposture.Updates  `json:"updates,omitempty"`
	// Capabilities are the Guard facts the control plane persisted for this
	// server's worker; see serverRuntimeCapabilities.
	Capabilities *serverRuntimeCapabilities `json:"capabilities,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	UpdatedAt    time.Time                  `json:"updated_at"`
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

// ServerMaintenanceRoutes exposes only the canonical maintenance leaves that
// MCP can invoke with its already verified request context.
type ServerMaintenanceRoutes struct {
	Action httpx.HandlerFunc
	Impact httpx.HandlerFunc
}

// RegisterServerRuntimeRoutes returns disabled maintenance leaves as nil.
func RegisterServerRuntimeRoutes(r *httpx.Router, cfg ServerRuntimeRouteConfig) ServerMaintenanceRoutes {
	if cfg.Store == nil {
		panic("RegisterServerRuntimeRoutes: server runtime store required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	_, reachControl := cfg.Reach.(ServerReachControl)
	if (cfg.Detacher != nil || cfg.Maintenance != nil || cfg.Posture != nil || reachControl || cfg.Network != nil) && cfg.Policy == nil {
		panic("RegisterServerRuntimeRoutes: inventory policy required for server detach, maintenance, security repair and reach")
	}
	h := serverRuntimeHandlers{
		store: cfg.Store, ports: cfg.PortInventory, detacher: cfg.Detacher, policy: cfg.Policy,
		agentDisconnect: cfg.AgentDisconnect, now: cfg.Now, maintenance: newServerMaintenanceHandlers(cfg.Maintenance),
		reach: cfg.Reach, posture: cfg.Posture, postureEntitlements: cfg.PostureEntitlements, workers: cfg.Workers,
		network:                 cfg.Network,
		currentMTLSCapabilities: cfg.CurrentMTLSCapabilities,
		walletWithdraw:          cfg.WalletWithdraw,
	}
	r.GET("/api/v1/servers", h.list)
	r.GET("/api/v1/servers/{serverId}", h.get)
	r.GET("/api/v1/servers/{serverId}/ports", h.portInventory)
	r.GET("/api/v1/servers/{serverId}/transitions", h.transitions)
	if cfg.Detacher != nil {
		r.POST("/api/v1/servers/{serverId}/detach", h.detach)
	}
	if _, ok := h.reach.(ServerReachControl); ok {
		r.PUT("/api/v1/servers/{serverId}/reach", h.reachSetPublic)
	}
	if h.network != nil && h.network.Workers != nil {
		r.GET("/api/v1/servers/{serverId}/network", h.networkRead)
		r.POST("/api/v1/servers/{serverId}/network/rebind", h.networkRebind)
	}
	if h.posture != nil {
		r.POST("/api/v1/servers/{serverId}/security/verify", h.postureVerify)
		r.GET("/api/v1/servers/{serverId}/security/findings", h.postureFindings)
		r.GET("/api/v1/servers/{serverId}/security/repairs", h.postureRepairs)
		r.POST("/api/v1/servers/{serverId}/security/repairs", h.postureRequestRepair)
		r.GET("/api/v1/servers/{serverId}/security/auto-repair", h.postureAutoRepair)
		r.PUT("/api/v1/servers/{serverId}/security/auto-repair", h.postureSetAutoRepair)
	}
	if h.maintenance != nil {
		r.GET("/api/v1/servers/{serverId}/maintenance-impact", h.maintenanceImpact)
		r.POST("/api/v1/servers/{serverId}/actions", h.maintenanceAction)
		r.GET("/api/v1/servers/{serverId}/actions/{jobId}", h.maintenanceJob)
		r.POST("/api/v1/servers/{serverId}/actions/{jobId}/cancel", h.cancelMaintenanceJob)
		return ServerMaintenanceRoutes{Action: h.maintenanceAction, Impact: h.maintenanceImpact}
	}
	return ServerMaintenanceRoutes{}
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
	if err := authorizeOwnerServerOperate(e.Request.Context(), h.policy, h.store, inventoryScope{tenantID: tenantID, ownerID: ownerID}, serverID); err != nil {
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
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The exact server Agent is unavailable", nil)
	case err != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server detach is unavailable", nil)
	default:
		if h.agentDisconnect != nil {
			_ = h.agentDisconnect(receipt.AgentID)
		}
		return httpx.Success(e, http.StatusOK, selfOwnedServerDetachResponse{
			SelfOwnedServerDetachReceipt: receipt,
			WalletWithdrawal:             h.withdrawFromWallet(e.Request.Context(), ownerID, receipt.AgentID),
		})
	}
}

// withdrawFromWallet runs after the detach committed, also on a replay, so a
// failed earlier attempt is retried by detaching again.
//
// The Wallet owner is the server's recorded owner: DetachSelfOwnedServer
// refuses anyone but the server's OwnerSubjectID, so ownerID is that owner,
// not merely whoever is signed in. Techstack does not record which Wallet
// owner each delivery to the server was made for (deliveries use the stack
// owner); a delivery made from another owner's Wallet, such as a stack owned
// by someone else in the tenant, is not withdrawn here, and that owner
// withdraws the server in their own Wallet.
func (h serverRuntimeHandlers) withdrawFromWallet(ctx context.Context, ownerID, agentID string) *serverWalletWithdrawal {
	if h.walletWithdraw == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serverWalletWithdrawalTimeout)
	defer cancel()
	withdrawn, err := h.walletWithdraw.HostWithdraw(ctx, ownerID, agentID)
	if err != nil {
		failed := &serverWalletWithdrawal{State: "failed", ReasonCode: "wallet_unavailable", Retryable: true}
		if denial, ok := kombifywallet.AsDenial(err); ok {
			failed.ReasonCode, failed.Retryable = denial.Reason, denial.Retryable
		}
		return failed
	}
	return &serverWalletWithdrawal{State: "recorded", TargetsRemoved: withdrawn.TargetsRemoved, ItemsNeedRotation: withdrawn.NeedsRotation}
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
		httpx.RecordDependencyFailure(e, "servers.list.runtimes", err)
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	reach := h.reachByWorker(e.Request.Context(), tenantID)
	postureViews := h.postureViews(e.Request.Context(), tenantID)
	capabilities := h.capabilitiesByWorker(e.Request.Context(), tenantID)
	items := make([]serverRuntimeResponse, 0, len(rows))
	for _, row := range rows {
		if !serverRuntimeOwnedBy(row, ownerID) || serverRuntimeHiddenFromCurrentInventory(row) {
			continue
		}
		item := h.response(row)
		item.Reach = reach[strings.TrimSpace(row.WorkerID)]
		h.applyPosture(&item, row.WorkerID, postureViews)
		item.Capabilities = capabilities[strings.TrimSpace(row.WorkerID)]
		h.applyStackKitActions(&item, row)
		items = append(items, item)
	}
	qualifyServerRuntimeDisplayNames(items)
	sort.SliceStable(items, func(i, j int) bool {
		return serverRuntimeDisplayPriority(items[i]) < serverRuntimeDisplayPriority(items[j])
	})
	validator := make([]serverRuntimeResponse, len(items))
	for i, item := range items {
		validator[i] = item.withoutElapsedCounters()
	}
	return httpx.ConditionalSuccess(e, items, validator)
}

// withoutElapsedCounters clears the two fields that are only the clock minus a
// timestamp the response also carries (`connection.last_heartbeat_at` and
// `target_evidence.observed_at`). They are served as-is but excluded from the
// response validator: they change every second, so hashing them would make a
// conditional read of an unchanged server never match. Every state they feed
// (stale and offline demotion, freshness state) stays in the validator.
func (r serverRuntimeResponse) withoutElapsedCounters() serverRuntimeResponse {
	r.Connection.StalenessSeconds = nil
	r.TargetEvidence.Freshness.AgeSeconds = nil
	return r
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
	item := h.response(*server)
	item.Reach = h.reachByWorker(e.Request.Context(), server.TenantID)[strings.TrimSpace(server.WorkerID)]
	h.applyPosture(&item, server.WorkerID, h.postureViews(e.Request.Context(), server.TenantID))
	item.Capabilities = h.capabilitiesForWorker(e.Request.Context(), server.TenantID, server.WorkerID)
	h.applyStackKitActions(&item, *server)
	return httpx.ConditionalSuccess(e, item, item.withoutElapsedCounters())
}

// reachByWorker reads the tenant's reach rows once. A read failure leaves the
// reach section absent instead of failing the inventory, which never depends
// on reach.
func (h serverRuntimeHandlers) reachByWorker(ctx context.Context, tenantID string) map[string]*guardreach.View {
	if h.reach == nil {
		return nil
	}
	rows, err := h.reach.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil
	}
	out := make(map[string]*guardreach.View, len(rows))
	for _, row := range rows {
		view := guardreach.ViewOf(row)
		out[row.WorkerID] = &view
	}
	return out
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
		httpx.RecordDependencyFailure(e, "servers.get.runtime", err)
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	if !serverRuntimeOwnedBy(*server, ownerID) {
		return nil, httpx.NotFound(e, "Server not found")
	}
	return server, nil
}

// response returns the persisted observed state. The registry sweeper stays
// the writer of durable demotions (heartbeat freshness becomes connection and
// health writes through ApplyServerEvent). Between sweeps the response applies
// the same demotion the heartbeat age already earns, so the documented 90 s
// stale and 5 min offline windows hold exactly at the API instead of lagging by
// the sweep interval. The overlay only demotes, never promotes, and never
// writes; a fresh Guard heartbeat is still the only way back to connected.
func (h serverRuntimeHandlers) response(server controlplane.ServerRuntime) serverRuntimeResponse {
	now := h.now().UTC()
	connection, health := server.ConnectionState, server.HealthState
	if demotedConnection, demotedHealth, reason, due := serverregistry.EffectiveDemotion(now, server); due {
		connection, health = string(demotedConnection), string(demotedHealth)
		// Derived actions below read the same effective state.
		server.ConnectionState, server.HealthState, server.ReasonCode = connection, health, reason
	}
	// A detached or retiring server is no longer served by its Guard: its
	// credential and host identity are revoked in the detach transaction. The
	// control plane may not write Guard observation state, so the owner-facing
	// read derives it: such a server never reads as Connected (finding Q-9).
	switch serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState)) {
	case serverregistry.LifecycleDecommissioning, serverregistry.LifecycleDecommissioned:
		switch serverregistry.ConnectionState(strings.TrimSpace(server.ConnectionState)) {
		case serverregistry.ConnectionRevoked, serverregistry.ConnectionOffline:
		default:
			connection = string(serverregistry.ConnectionRevoked)
			server.ConnectionState = connection
		}
	}
	var staleness *int64
	if server.LastHeartbeatAt != nil {
		seconds := int64(now.Sub(server.LastHeartbeatAt.UTC()).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		staleness = &seconds
	}
	target := servedRuntimeTarget(server)
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
		Name: serverRuntimeDisplayName(server, target), DisplayName: strings.TrimSpace(server.DisplayName), WorkerID: server.WorkerID,
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
		AllowedActions:   h.allowedNodeActions(server),
		StackActions:     serverStackActions(server),
		CreatedAt:        server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

// servedRuntimeTarget is the hosting classification every read surface serves
// for one canonical server: the persisted target, or the fail-closed unknown
// target when no placement evidence was recorded (ADR-039).
func servedRuntimeTarget(server controlplane.ServerRuntime) serverregistry.RuntimeTarget {
	target := serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	if !serverregistry.RuntimeTargetIntentPresent(target) {
		return serverregistry.UnknownRuntimeTarget()
	}
	return target
}

func (h serverRuntimeHandlers) allowedNodeActions(server controlplane.ServerRuntime) []string {
	actions := append(serverNodeActions(server, h.detacher != nil), h.maintenance.advertisedActions(server)...)
	sort.Strings(actions)
	return actions
}
