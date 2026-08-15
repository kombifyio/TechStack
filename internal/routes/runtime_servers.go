package routes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type ServerRuntimeRouteConfig struct {
	Store controlplane.ServerRuntimeStore
	Now   func() time.Time
}

type serverRuntimeHandlers struct {
	store controlplane.ServerRuntimeStore
	now   func() time.Time
}

type serverRuntimeResponse struct {
	ID          string `json:"id"`
	TechstackID string `json:"techstack_id,omitempty"`
	Name        string `json:"name"`
	// WorkerID is the bound Guard agent identity. It is additive on this
	// response and exists because the canonical read model is now the UI's only
	// server source (kombify-Techstack-nzy1.7): the pairing flow has to be able
	// to tell "a Guard agent is bound to this aggregate" apart from "a server
	// row exists", and the legacy /api/v1/registry/servers projection it used
	// to read that from is being retired.
	WorkerID          string                       `json:"worker_id,omitempty"`
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
	MutationsAllowed  bool                        `json:"mutations_allowed"`
	CreatedAt         time.Time                   `json:"created_at"`
	UpdatedAt         time.Time                   `json:"updated_at"`
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
	h := serverRuntimeHandlers{store: cfg.Store, now: cfg.Now}
	r.GET("/api/v1/servers", h.list)
	r.GET("/api/v1/servers/{serverId}", h.get)
	r.GET("/api/v1/servers/{serverId}/transitions", h.transitions)
}

func (h serverRuntimeHandlers) list(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID := requestTenantID(e, ownerID)
	rows, err := h.store.ListServerRuntimesByTenant(e.Request.Context(), tenantID, strings.TrimSpace(e.Request.URL.Query().Get("techstack_id")))
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	items := make([]serverRuntimeResponse, 0, len(rows))
	for _, row := range rows {
		if (!isAdmin && row.OwnerSubjectID != ownerID) || serverRuntimeIsTerminalTombstone(row) {
			continue
		}
		items = append(items, h.response(row))
	}
	return httpx.Success(e, http.StatusOK, items)
}

func serverRuntimeIsTerminalTombstone(server controlplane.ServerRuntime) bool {
	return strings.EqualFold(strings.TrimSpace(server.LifecycleState), string(serverregistry.LifecycleDecommissioned)) &&
		strings.EqualFold(strings.TrimSpace(server.DesiredState), "absent")
}

func (h serverRuntimeHandlers) get(e *httpx.Event) error {
	server, err := h.ownedServer(e)
	if err != nil || server == nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, h.response(*server))
}

func (h serverRuntimeHandlers) transitions(e *httpx.Event) error {
	server, err := h.ownedServer(e)
	if err != nil || server == nil {
		return err
	}
	rows, err := h.store.ListServerTransitions(e.Request.Context(), server.TenantID, server.ID, 100)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server history is unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, rows)
}

func (h serverRuntimeHandlers) ownedServer(e *httpx.Event) (*controlplane.ServerRuntime, error) {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return nil, httpx.Unauthorized(e, "Authentication required")
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return nil, httpx.BadRequest(e, "Server ID is required", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	server, err := h.store.GetServerRuntime(e.Request.Context(), tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	if !isAdmin && server.OwnerSubjectID != ownerID {
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
		ID: server.ID, TechstackID: server.StackID, Name: server.Name, WorkerID: server.WorkerID,
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
		MutationsAllowed: serverregistry.MutationsAllowed(connection) && server.LifecycleState == string(serverregistry.LifecycleActive),
		CreatedAt:        server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}
