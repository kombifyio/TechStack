package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

const (
	workerInventoryArchKey           = "arch"
	workerInventoryChannelsKey       = "channels"
	workerInventoryCPUPercentKey     = "cpu_percent"
	workerInventoryDiskTotalBytesKey = "disk_total_bytes"
	workerInventoryDiskUsedBytesKey  = "disk_used_bytes"
	workerInventoryEndpointsKey      = "endpoints"
	workerInventoryHealthKey         = "health"
	workerInventoryHostKey           = "host"
	workerInventoryLastSeenKey       = "last_seen"
	workerInventoryMemoryTotalKey    = "memory_total_bytes"
	workerInventoryMemoryUsedKey     = "memory_used_bytes"
	workerInventoryObservedAtKey     = "observed_at"
	workerInventoryRuntimeAgentIDKey = "runtime_agent_id"
	workerInventorySourceGuard       = "guard-inventory"
	workerInventoryUptimeSecondsKey  = "uptime_seconds"
	workerRuntimeStatusConnected     = "connected"
)

type workerInventoryRequest struct {
	SourceEpoch      string    `json:"source_epoch"`
	SourceSequence   int64     `json:"source_sequence"`
	ObservedAt       time.Time `json:"observed_at"`
	TenantID         string    `json:"tenant_id"`
	OwnerID          string    `json:"owner_id"`
	StackID          string    `json:"stack_id"`
	LeaseID          string    `json:"lease_id"`
	ServerID         string    `json:"server_id"`
	RuntimeAgentID   string    `json:"runtime_agent_id"`
	Hostname         string    `json:"hostname"`
	OS               string    `json:"os"`
	Arch             string    `json:"arch"`
	AgentVersion     string    `json:"agent_version"`
	StackKit         string    `json:"stackkit"`
	StackKitVersion  string    `json:"stackkit_version"`
	StackKitMode     string    `json:"stackkit_mode"`
	Domain           string    `json:"domain"`
	ManifestObserved bool      `json:"manifest_observed"`
	// ManifestServiceCount distinguishes "manifest declares zero services"
	// (legitimate prune-to-zero) from "probing produced none" (no evidence).
	ManifestServiceCount int `json:"manifest_service_count"`
	// DiscoveryObserved reports whether the Guard's unmanaged-service probes
	// actually ran on this host. It separates "the host runs nothing we can
	// see" from "discovery produced no evidence at all", which an empty
	// services array cannot express on its own.
	DiscoveryObserved bool `json:"discovery_observed"`
	// DiscoveredServiceCount is how many unmanaged services the Guard
	// enumerated before any control-plane filtering.
	DiscoveredServiceCount int                          `json:"discovered_service_count"`
	Host                   workerInventoryHost          `json:"host"`
	Services               []workerInventoryService     `json:"services"`
	Channels               []workerInventoryChannel     `json:"channels"`
	Endpoints              []workerInventoryEndpoint    `json:"endpoints"`
	RuntimeConvergence     *runtimeconvergence.Snapshot `json:"runtime_convergence,omitempty"`
}

type workerInventoryHost struct {
	Hostname         string  `json:"hostname"`
	OS               string  `json:"os"`
	OSVersion        string  `json:"os_version"`
	Arch             string  `json:"arch"`
	PublicIP         string  `json:"public_ip"`
	PrivateIP        string  `json:"private_ip"`
	LocalIP          string  `json:"local_ip"`
	CPUCores         int     `json:"cpu_cores"`
	RAMMB            int     `json:"ram_mb"`
	DiskGB           int     `json:"disk_gb"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	DiskUsedBytes    int64   `json:"disk_used_bytes"`
	DiskTotalBytes   int64   `json:"disk_total_bytes"`
	UptimeSeconds    float64 `json:"uptime_seconds"`
}

type workerInventoryService struct {
	ID        string `json:"id"`
	ServiceID string `json:"service_id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// Source is the Guard-reported provenance on the closed
	// pkg/serviceregistry vocabulary. See inventoryServiceSource for why an
	// absent value stays `stackkits-inventory`.
	Source       string                    `json:"source"`
	URL          string                    `json:"url"`
	OwnerStack   string                    `json:"owner_stack"`
	TargetServer string                    `json:"target_server"`
	ContainerID  string                    `json:"container_id"`
	PlatformID   string                    `json:"platform_id"`
	PlatformType string                    `json:"platform_type"`
	Image        string                    `json:"image"`
	Description  string                    `json:"description"`
	Instance     string                    `json:"instance"`
	StackKit     string                    `json:"stackkit_version"`
	Actions      []string                  `json:"actions"`
	DesiredState string                    `json:"desired_state"`
	EvidenceRef  string                    `json:"evidence_ref"`
	Health       map[string]any            `json:"health"`
	Endpoints    []workerInventoryEndpoint `json:"endpoints"`
}

type workerInventoryEndpoint struct {
	URL              string `json:"url"`
	Address          string `json:"address"`
	Visibility       string `json:"visibility"`
	Provenance       string `json:"provenance"`
	Kind             string `json:"kind"`
	Health           string `json:"health"`
	TargetType       string `json:"target_type"`
	RouteID          string `json:"route_id"`
	AccessProfileRef string `json:"access_profile_ref"`
}

type workerInventoryChannel struct {
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	Provenance string `json:"provenance"`
}

type runtimeAgentAuthContext struct {
	TenantID       string
	OwnerID        string
	StackID        string
	LeaseID        string
	ServerID       string
	RuntimeAgentID string
	Worker         *controlplane.Worker
}

func (h workerRouteHandlers) inventory(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if id == "" {
		return httpx.BadRequest(e, "runtime agent id is required", nil)
	}
	var req workerInventoryRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if normalizeErr := req.normalize(id); normalizeErr != nil {
		return httpx.BadRequest(e, "Invalid runtime convergence", nil)
	}

	authCtx, authenticated := h.authenticateRuntimeAgent(e, id, req)
	if !authenticated {
		return nil
	}
	bindInventoryToRuntimeAuthority(&req, authCtx)
	now := time.Now().UTC()
	position, positionErr := validateGuardEventPosition(now, guardEventPosition{
		Epoch: req.SourceEpoch, Sequence: req.SourceSequence, ObservedAt: req.ObservedAt,
	})
	if positionErr != nil {
		return httpx.BadRequest(e, "Invalid Guard event position", nil)
	}
	observedAt := position.ObservedAt
	worker := inventoryWorker(authCtx, req, observedAt)
	nodeID := firstNonEmpty(runtimeidentity.LeaseServerID(authCtx.LeaseID), authCtx.ServerID, runtimeServerIDForWorker(id))
	projection, projectionErr := h.projectServerInventory(e.Request.Context(), *worker, nodeID, authCtx.LeaseID, req, now)
	if projectionErr != nil {
		if errors.Is(projectionErr, controlplane.ErrConflict) {
			// Every distinct admission conflict used to collapse into one opaque
			// message, so a permanently rejected agent looked identical to a
			// benign race. The bounded reason names the failing invariant.
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Guard inventory conflicts with the accepted source position", map[string]any{
				walletAuditReasonKey: guardInventoryConflictReason(projectionErr),
			})
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist canonical server inventory", nil)
	}
	if projection != nil && (projection.ServerEvent == nil || projection.ServerEvent.Server == nil) {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Canonical Guard inventory result is unavailable", nil)
	}
	if projection != nil && !projection.ServerEvent.Applied && !projection.Replayed {
		return httpx.Success(e, http.StatusAccepted, map[string]any{
			workerFieldServerID: nodeID, "applied": false,
		})
	}
	inventoryRevision := int64(0)
	if projection != nil {
		inventoryRevision = projection.ServerEvent.Server.InventoryRevision
		if projection.ServerEvent.Inventory != nil {
			inventoryRevision = projection.ServerEvent.Inventory.Revision
		}
		if inventoryRevision <= 0 {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Canonical server inventory revision is unavailable", nil)
		}
	}
	if projection != nil {
		satelliteStore, ok := h.serverStore.(controlplane.GuardInventorySatelliteStore)
		if !ok {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Canonical server store does not support fenced inventory satellites", nil)
		}
		canonical := projection.ServerEvent.Server
		if canonical.SourceObservedAt == nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Canonical Guard source timestamp is unavailable", nil)
		}
		satellite, satelliteErr := satelliteStore.ApplyGuardInventorySatellites(e.Request.Context(), controlplane.GuardInventorySatelliteProjection{
			TenantID: canonical.TenantID, ServerID: canonical.ID, Generation: canonical.Generation,
			SourceID: canonical.SourceID, SourceEpoch: canonical.SourceEpoch, SourceSequence: canonical.SourceSequence,
			SourceObservedAt: *canonical.SourceObservedAt, InventoryRevision: inventoryRevision,
			Worker: *worker, RILServer: inventoryRILServer(*worker, nodeID, req, *canonical.SourceObservedAt),
		})
		if satelliteErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update fenced inventory satellites", nil)
		}
		if satellite == nil || !satellite.Applied {
			return httpx.Success(e, http.StatusAccepted, map[string]any{workerFieldServerID: nodeID, "applied": false})
		}
		worker = satellite.Worker
		observedAt = canonical.SourceObservedAt.UTC()
	} else {
		var err error
		worker, err = h.saveInventoryWorker(e, *worker)
		if err != nil {
			return err
		}
	}
	if projection == nil {
		if metricErr := h.writeInventoryMetrics(*worker, req, observedAt); metricErr != nil {
			return metricErr
		}
		if registryErr := h.upsertInventoryRegistry(e, *worker, nodeID, req, inventoryRevision, observedAt); registryErr != nil {
			return registryErr
		}
	}
	if projection == nil {
		if rilErr := h.upsertInventoryRIL(e, *worker, nodeID, req, observedAt); rilErr != nil {
			return rilErr
		}
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		workerFieldServerID:              nodeID,
		workerInventoryRuntimeAgentIDKey: id,
		preCheckWorkerIDField:            worker.ID,
		preCheckStatusField:              worker.Status,
		registryCollectionServices:       len(req.Services),
		workerInventoryEndpointsKey:      inventoryEndpointCount(req),
		workerInventoryLastSeenKey:       observedAt.Format(time.RFC3339Nano),
	})
}

func (req *workerInventoryRequest) normalize(runtimeAgentID string) error {
	req.SourceEpoch = strings.TrimSpace(req.SourceEpoch)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.ServerID = strings.TrimSpace(req.ServerID)
	req.RuntimeAgentID = firstNonEmpty(req.RuntimeAgentID, runtimeAgentID)
	req.Hostname = firstNonEmpty(req.Hostname, req.Host.Hostname)
	req.OS = firstNonEmpty(req.OS, req.Host.OS)
	req.Arch = firstNonEmpty(req.Arch, req.Host.Arch)
	req.AgentVersion = strings.TrimSpace(req.AgentVersion)
	req.StackKit = strings.TrimSpace(req.StackKit)
	req.StackKitVersion = strings.TrimSpace(req.StackKitVersion)
	req.StackKitMode = strings.TrimSpace(req.StackKitMode)
	req.Domain = strings.TrimSpace(req.Domain)
	if err := normalizeRuntimeConvergence(&req.RuntimeConvergence); err != nil {
		return err
	}
	req.Host.Hostname = firstNonEmpty(req.Host.Hostname, req.Hostname)
	req.Host.OS = firstNonEmpty(req.Host.OS, req.OS)
	req.Host.OSVersion = strings.TrimSpace(req.Host.OSVersion)
	req.Host.Arch = firstNonEmpty(req.Host.Arch, req.Arch)
	for i := range req.Services {
		req.Services[i].normalize()
	}
	for i := range req.Endpoints {
		req.Endpoints[i].normalize()
	}
	for i := range req.Channels {
		req.Channels[i].normalize()
	}
	return nil
}

func (svc *workerInventoryService) normalize() {
	svc.ID = strings.TrimSpace(svc.ID)
	svc.ServiceID = strings.TrimSpace(svc.ServiceID)
	svc.Key = strings.TrimSpace(svc.Key)
	svc.Name = strings.TrimSpace(svc.Name)
	svc.Status = strings.TrimSpace(svc.Status)
	svc.URL = strings.TrimSpace(svc.URL)
	svc.OwnerStack = strings.TrimSpace(svc.OwnerStack)
	svc.TargetServer = strings.TrimSpace(svc.TargetServer)
	svc.Source = strings.ToLower(strings.TrimSpace(svc.Source))
	svc.ContainerID = strings.TrimSpace(svc.ContainerID)
	svc.PlatformID = strings.TrimSpace(svc.PlatformID)
	svc.PlatformType = strings.TrimSpace(svc.PlatformType)
	svc.Image = strings.TrimSpace(svc.Image)
	svc.Description = strings.TrimSpace(svc.Description)
	svc.Instance = firstNonEmpty(strings.TrimSpace(svc.Instance), "default")
	svc.StackKit = strings.TrimSpace(svc.StackKit)
	svc.Actions = canonicalServiceActions(svc.Actions)
	for i := range svc.Endpoints {
		svc.Endpoints[i].normalize()
	}
}

func (endpoint *workerInventoryEndpoint) normalize() {
	endpoint.URL = firstNonEmpty(endpoint.URL, endpoint.Address)
	endpoint.Address = firstNonEmpty(endpoint.Address, endpoint.URL)
	endpoint.Visibility = strings.TrimSpace(endpoint.Visibility)
	endpoint.Provenance = strings.TrimSpace(endpoint.Provenance)
	endpoint.Kind = strings.TrimSpace(endpoint.Kind)
	endpoint.Health = strings.TrimSpace(endpoint.Health)
	endpoint.TargetType = strings.TrimSpace(endpoint.TargetType)
	endpoint.RouteID = strings.TrimSpace(endpoint.RouteID)
	endpoint.AccessProfileRef = strings.TrimSpace(endpoint.AccessProfileRef)
}

func (channel *workerInventoryChannel) normalize() {
	channel.Kind = strings.TrimSpace(channel.Kind)
	channel.URL = strings.TrimSpace(channel.URL)
	channel.Status = strings.TrimSpace(channel.Status)
	channel.Provenance = strings.TrimSpace(channel.Provenance)
}

func (h workerRouteHandlers) authenticateRuntimeAgent(e *httpx.Event, id string, req workerInventoryRequest) (runtimeAgentAuthContext, bool) {
	var out runtimeAgentAuthContext
	token := bearerToken(e.Request)
	if token == "" {
		_ = httpx.Unauthorized(e, "Missing runtime agent token")
		return out, false
	}
	if claims, err := workerauth.Verify(workerauth.SecretFromEnv(), token, time.Now().UTC()); err == nil {
		return h.authenticateSignedRuntimeAgent(e, id, req, token, claims)
	}
	return h.authenticateOpaqueOrLegacyRuntimeAgent(e, id, req, token)
}

func (h workerRouteHandlers) authenticateSignedRuntimeAgent(e *httpx.Event, id string, req workerInventoryRequest, token string, claims workerauth.Claims) (runtimeAgentAuthContext, bool) {
	var out runtimeAgentAuthContext
	if !validateRuntimeAgentClaims(e, id, req, claims) {
		return out, false
	}
	if h.wst == nil {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	worker, getErr := h.wst.GetWorker(e.Request.Context(), claims.TenantID, id)
	if getErr != nil || worker == nil || strings.EqualFold(worker.Status, managedRuntimeStatusRejected) {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	if !validateRuntimeAgentWorkerBinding(e, *worker, claims, workerauth.SHA256Hex(token)) {
		return out, false
	}
	return runtimeAgentAuthContext{
		TenantID:       claims.TenantID,
		OwnerID:        claims.OwnerID,
		StackID:        claims.StackID,
		LeaseID:        claims.LeaseID,
		ServerID:       claims.ServerID,
		RuntimeAgentID: claims.RuntimeAgentID,
		Worker:         worker,
	}, true
}

func (h workerRouteHandlers) authenticateOpaqueOrLegacyRuntimeAgent(e *httpx.Event, id string, req workerInventoryRequest, token string) (runtimeAgentAuthContext, bool) {
	var out runtimeAgentAuthContext
	// A token that declares the signed wire format must never fall through to
	// the stateful hash path. Otherwise expiry or a signing-secret rotation can
	// be bypassed whenever the exact old token hash still exists on the worker.
	if workerauth.IsSignedToken(token) {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	// Reject malformed tokens in our reserved namespace. Only well-formed
	// opaque credentials and explicit pre-tsra legacy credentials may use the
	// exact-hash compatibility path below.
	if strings.HasPrefix(strings.TrimSpace(token), "tsra.") && !workerauth.IsOpaqueToken(token) {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	if h.wst == nil {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	tenantID := firstNonEmpty(req.TenantID, runtimeAgentTenantIDFromRequest(e))
	if tenantID == "" {
		_ = httpx.Unauthorized(e, "Runtime agent tenant is required")
		return out, false
	}
	worker, err := h.wst.GetWorker(e.Request.Context(), tenantID, id)
	if err != nil || worker == nil || strings.EqualFold(worker.Status, managedRuntimeStatusRejected) {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	tokenHash := workerauth.SHA256Hex(token)
	if !workerAcceptsRuntimeAgentToken(*worker, tokenHash) {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return out, false
	}
	return runtimeAgentAuthContext{
		TenantID:       worker.TenantID,
		OwnerID:        worker.OwnerSubjectID,
		StackID:        worker.StackID,
		LeaseID:        runtimeLeaseIDFromMetadata(worker.Capabilities),
		ServerID:       stringFromAny(worker.Capabilities[workerFieldServerID]),
		RuntimeAgentID: worker.ID,
		Worker:         worker,
	}, true
}

func validateRuntimeAgentWorkerBinding(e *httpx.Event, worker controlplane.Worker, claims workerauth.Claims, tokenHash string) bool {
	if worker.ID != claims.RuntimeAgentID || worker.TenantID != claims.TenantID {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	if worker.OwnerSubjectID != "" && worker.OwnerSubjectID != claims.OwnerID {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	if worker.StackID != claims.StackID {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	if storedHash := stringFromAny(worker.Resources["agent_token_sha256"]); storedHash != "" && storedHash != tokenHash {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	if leaseID := runtimeLeaseIDFromMetadata(worker.Capabilities); leaseID != "" && leaseID != claims.LeaseID {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	if serverID := stringFromAny(worker.Capabilities["server_id"]); serverID != "" && serverID != claims.ServerID {
		_ = httpx.Unauthorized(e, "Invalid runtime agent token")
		return false
	}
	return true
}

func validateRuntimeAgentClaims(e *httpx.Event, id string, req workerInventoryRequest, claims workerauth.Claims) bool {
	if claims.RuntimeAgentID != id || (req.RuntimeAgentID != "" && req.RuntimeAgentID != id) {
		_ = httpx.Forbidden(e, "Runtime agent token does not match worker")
		return false
	}
	if claims.LeaseID != "" && (claims.OwnerID == "" || claims.StackID == "" || claims.ServerID != runtimeidentity.LeaseServerID(claims.LeaseID)) {
		_ = httpx.Forbidden(e, "Runtime agent token has an invalid managed lease binding")
		return false
	}
	if req.TenantID != "" && req.TenantID != claims.TenantID {
		_ = httpx.Forbidden(e, "Runtime agent token does not match tenant")
		return false
	}
	if req.OwnerID != "" && req.OwnerID != claims.OwnerID {
		_ = httpx.Forbidden(e, "Runtime agent token does not match owner")
		return false
	}
	return validateRuntimeAgentPlacementClaims(e, req, claims)
}

func validateRuntimeAgentPlacementClaims(e *httpx.Event, req workerInventoryRequest, claims workerauth.Claims) bool {
	if req.StackID != "" && req.StackID != claims.StackID {
		_ = httpx.Forbidden(e, "Runtime agent token does not match stack")
		return false
	}
	if req.LeaseID != "" && req.LeaseID != claims.LeaseID {
		_ = httpx.Forbidden(e, "Runtime agent token does not match lease")
		return false
	}
	if req.ServerID != "" && req.ServerID != claims.ServerID {
		_ = httpx.Forbidden(e, "Runtime agent token does not match server")
		return false
	}
	return true
}

// bindInventoryToRuntimeAuthority removes request-controlled placement fields
// once authentication has succeeded. Service inventory belongs to the signed
// agent's stack; an agent must never move a service by submitting owner_stack.
// guardInventoryConflictReason reduces an admission conflict to the bounded,
// secret-free invariant name the store reported. The store never puts observed
// values into these messages, only the binding or position that disagreed, so
// the reason is safe to hand back to the runtime agent that caused it.
func guardInventoryConflictReason(err error) string {
	if err == nil {
		return ""
	}
	reason := err.Error()
	if _, remainder, found := strings.Cut(reason, ": "); found {
		reason = remainder
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return reason
}

func bindInventoryToRuntimeAuthority(req *workerInventoryRequest, authCtx runtimeAgentAuthContext) {
	if req == nil {
		return
	}
	req.TenantID = authCtx.TenantID
	req.OwnerID = authCtx.OwnerID
	req.StackID = authCtx.StackID
	req.LeaseID = authCtx.LeaseID
	req.ServerID = authCtx.ServerID
	req.RuntimeAgentID = authCtx.RuntimeAgentID
	for index := range req.Services {
		req.Services[index].OwnerStack = authCtx.StackID
	}
}

func runtimeAgentTenantIDFromRequest(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	return firstNonEmpty(
		e.Request.Header.Get("X-Kombify-Tenant-ID"),
		e.Request.Header.Get("X-TechStack-Tenant-ID"),
		e.Request.URL.Query().Get("tenant_id"),
	)
}

func workerAcceptsRuntimeAgentToken(worker controlplane.Worker, tokenHash string) bool {
	if tokenHash == "" {
		return false
	}
	// Once a worker has a dedicated runtime-agent credential, it is the sole
	// authority. Worker.TokenHash is the redeemed pairing-token hash; falling
	// back to it here would keep a used or expired one-time secret valid and
	// would also bypass an agent-credential rotation.
	if agentHash := strings.TrimSpace(stringFromAny(worker.Resources["agent_token_sha256"])); agentHash != "" {
		return agentHash == tokenHash
	}
	// Compatibility for workers created before dedicated agent credentials were
	// persisted. New registrations always populate agent_token_sha256.
	return strings.TrimSpace(worker.TokenHash) == tokenHash
}

func inventoryWorker(authCtx runtimeAgentAuthContext, req workerInventoryRequest, observedAt time.Time) *controlplane.Worker {
	capabilities := map[string]any{
		workerFieldServerID:              firstNonEmpty(runtimeidentity.LeaseServerID(authCtx.LeaseID), authCtx.ServerID),
		workerInventoryRuntimeAgentIDKey: authCtx.RuntimeAgentID,
		"agent_version":                  req.AgentVersion,
		workerInventoryChannelsKey:       inventoryChannels(req.Channels),
		"service_inventory":              serviceStackKits,
	}
	capabilities = mergeAnyMaps(capabilities, inventoryDeploymentMetadata(req))
	if req.RuntimeConvergence != nil {
		capabilities = mergeAnyMaps(capabilities, map[string]any{"runtime_convergence": runtimeconvergence.Map(*req.RuntimeConvergence)})
	}
	if authCtx.LeaseID != "" {
		capabilities[runtimeLeaseIDKey] = authCtx.LeaseID
	}
	worker := &controlplane.Worker{
		ID:             authCtx.RuntimeAgentID,
		TenantID:       authCtx.TenantID,
		StackID:        authCtx.StackID,
		Hostname:       firstNonEmpty(req.Hostname, req.Hostname, req.ServerID),
		OS:             req.OS,
		Arch:           req.Arch,
		IP:             firstNonEmpty(req.Host.PublicIP, req.Host.PrivateIP, req.Host.LocalIP),
		Status:         monthlyRuntimeEnrollmentStatusPending,
		Approved:       false,
		LastSeenAt:     &observedAt,
		CPUCores:       req.Host.CPUCores,
		RAMMB:          req.Host.RAMMB,
		DiskGB:         req.Host.DiskGB,
		Type:           "runtime",
		OwnerSubjectID: authCtx.OwnerID,
		Capabilities:   capabilities,
		Resources: map[string]any{
			workerInventoryHostKey:      inventoryHostMap(req.Host),
			workerInventoryEndpointsKey: inventoryEndpoints(req.Endpoints),
			"deployment":                inventoryDeploymentMetadata(req),
		},
	}
	if existing := authCtx.Worker; existing != nil {
		worker.InstanceID = existing.InstanceID
		worker.TokenHash = existing.TokenHash
		worker.IP = firstNonEmpty(worker.IP, existing.IP)
		worker.Provider = existing.Provider
		worker.Tags = existing.Tags
		worker.Status = existing.Status
		worker.Approved = existing.Approved
		worker.ApprovedAt = existing.ApprovedAt
		worker.Capabilities = mergeAnyMaps(existing.Capabilities, worker.Capabilities)
		worker.Resources = mergeAnyMaps(existing.Resources, worker.Resources)
	} else {
		// A valid stack-scoped signed token carries the owner's placement
		// approval. An unscoped token must remain pending until /approve.
		worker.Approved = strings.TrimSpace(authCtx.StackID) != ""
	}
	if req.RuntimeConvergence != nil {
		worker.Resources = mergeAnyMaps(worker.Resources, map[string]any{"runtime_convergence": runtimeconvergence.Map(*req.RuntimeConvergence)})
	}
	if worker.Approved {
		worker.Status = workerRuntimeStatusConnected
		if worker.ApprovedAt == nil {
			worker.ApprovedAt = &observedAt
		}
	} else if !strings.EqualFold(worker.Status, "rejected") {
		worker.Status = monthlyRuntimeEnrollmentStatusPending
	}
	return worker
}

func (h workerRouteHandlers) saveInventoryWorker(e *httpx.Event, worker controlplane.Worker) (*controlplane.Worker, error) {
	if h.wst == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Worker store is required for inventory", nil)
	}
	updated, err := h.wst.UpsertWorkerHeartbeat(e.Request.Context(), worker)
	if err != nil {
		return nil, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update worker inventory", nil)
	}
	return updated, nil
}

func (h workerRouteHandlers) writeInventoryMetrics(worker controlplane.Worker, req workerInventoryRequest, now time.Time) error {
	if h.metricWriter == nil {
		return nil
	}
	heartbeat := workerHeartbeatRequest{
		CPUPercent:       req.Host.CPUPercent,
		MemoryUsedBytes:  req.Host.MemoryUsedBytes,
		MemoryTotalBytes: req.Host.MemoryTotalBytes,
		DiskUsedBytes:    req.Host.DiskUsedBytes,
		DiskTotalBytes:   req.Host.DiskTotalBytes,
		UptimeSeconds:    req.Host.UptimeSeconds,
	}
	if heartbeat.MemoryTotalBytes == 0 && req.Host.RAMMB > 0 {
		heartbeat.MemoryTotalBytes = int64(req.Host.RAMMB) * 1024 * 1024
	}
	if heartbeat.DiskTotalBytes == 0 && req.Host.DiskGB > 0 {
		heartbeat.DiskTotalBytes = int64(req.Host.DiskGB) * 1024 * 1024 * 1024
	}
	samples := workerHeartbeatSamplesFromStore(worker, heartbeat, now)
	if len(samples) == 0 {
		return nil
	}
	return h.metricWriter.Write(samples)
}

func (h workerRouteHandlers) upsertInventoryRegistry(e *httpx.Event, worker controlplane.Worker, nodeID string, req workerInventoryRequest, inventoryRevision int64, now time.Time) error {
	if h.registryStore == nil {
		return nil
	}
	node, projectedServices := buildInventoryRegistryProjection(worker, nodeID, req, inventoryRevision, now)
	if _, err := h.registryStore.UpsertNode(e.Request.Context(), node); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update runtime node inventory", nil)
	}
	observedServiceIDs := make(map[string]bool, len(projectedServices))
	for _, projected := range projectedServices {
		serviceRecord := projected.Legacy
		runtimeRecord := projected.Runtime
		observedServiceIDs[serviceRecord.ID] = true
		migrationState := ""
		existing, getErr := h.registryStore.GetService(e.Request.Context(), worker.TenantID, serviceRecord.ID)
		if getErr == nil && existing != nil {
			migrationState = registryServiceActiveMigrationState(*existing)
			if migrationState != "" {
				serviceRecord.Status = existing.Status
				serviceRecord.MigrationStatus = existing.MigrationStatus
				serviceRecord.URL = ""
				serviceRecord.Metadata = mergeAnyMaps(existing.Metadata, serviceRecord.Metadata)
				runtimeRecord.Access = serviceAccess(serviceAccessUnavailable, "", "", "migration_in_progress", "")
				runtimeRecord.Metadata = mergeAnyMaps(existing.Metadata, runtimeRecord.Metadata)
			}
		} else if getErr != nil && !errors.Is(getErr, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to inspect service migration state", nil)
		}
		savedService, err := h.registryStore.UpsertService(e.Request.Context(), serviceRecord)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update service inventory", nil)
		}
		if runtimeStore, ok := h.registryStore.(controlplane.ServiceRuntimeStore); ok {
			if _, err := runtimeStore.UpsertServiceRuntime(e.Request.Context(), runtimeRecord); err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update canonical service runtime", nil)
			}
		}
		// ServiceRuntimeStore shares persistence with the legacy registry row in
		// both supported stores. Restore the control-plane workflow state after
		// recording measured health so an inventory sample cannot turn an active
		// migration green or re-enable its link.
		if migrationState != "" && savedService != nil {
			savedService.Status = serviceRecord.Status
			savedService.MigrationStatus = serviceRecord.MigrationStatus
			savedService.URL = ""
			savedService.Metadata = serviceRecord.Metadata
			if _, err := h.registryStore.UpsertService(e.Request.Context(), *savedService); err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to preserve service migration state", nil)
			}
		}
	}
	// Absence of service evidence is not evidence of absence: a manifest that
	// exists but reports no services (e.g. after a failed or partial apply)
	// must not reconcile observed history away.
	if req.ManifestObserved && len(observedServiceIDs) > 0 {
		if err := h.reconcileInventoryRegistryServices(e, worker, nodeID, observedServiceIDs); err != nil {
			return err
		}
	}
	return nil
}

func buildInventoryRegistryProjection(worker controlplane.Worker, nodeID string, req workerInventoryRequest, inventoryRevision int64, observedAt time.Time) (controlplane.Node, []controlplane.GuardInventoryServiceProjection) {
	serviceStates := make([]runtimehealth.ServiceState, 0, len(req.Services))
	for _, svc := range req.Services {
		serviceStates = append(serviceStates, inventoryServiceHealthState(svc, runtimehealth.ServerHealthy))
	}
	observedHostState := ""
	for _, state := range serviceStates {
		if state == runtimehealth.ServiceUnhealthy {
			observedHostState = runtimeHealthDegraded
			break
		}
	}
	serverState := runtimehealth.DeriveServerState(runtimehealth.ServerInput{
		Now:           observedAt,
		HeartbeatAt:   &observedAt,
		ObservedState: observedHostState,
	})
	nodeMetadata := mergeAnyMaps(map[string]any{
		workerInventoryRuntimeAgentIDKey: worker.ID,
		runtimeLeaseIDKey:                runtimeLeaseIDFromMetadata(worker.Capabilities),
		"agent_version":                  req.AgentVersion,
		"inventory_source":               workerInventorySourceGuard,
		"manifest_observed":              req.ManifestObserved,
		"manifest_service_count":         req.ManifestServiceCount,
		// Discovery evidence is projected next to manifest evidence so an empty
		// service list stays readable: it tells the reader whether the Guard's
		// unmanaged probes ran at all on this host.
		"discovery_observed":         req.DiscoveryObserved,
		"discovered_service_count":   req.DiscoveredServiceCount,
		"health_state":               string(serverState),
		workerInventoryObservedAtKey: observedAt.Format(time.RFC3339Nano),
		workerInventoryHostKey:       inventoryHostMap(req.Host),
		workerInventoryChannelsKey:   inventoryChannels(req.Channels),
		workerInventoryEndpointsKey:  inventoryEndpoints(req.Endpoints),
	}, inventoryDeploymentMetadata(req))
	if inventoryRevision > 0 {
		nodeMetadata["inventory_revision"] = inventoryRevision
	}
	node := controlplane.Node{
		ID:         nodeID,
		TenantID:   worker.TenantID,
		InstanceID: worker.InstanceID,
		StackID:    worker.StackID,
		WorkerID:   worker.ID,
		Name:       firstNonEmpty(req.Hostname, nodeID),
		Role:       managedRuntimeNodeFoundation,
		Status:     string(serverState),
		Address:    firstNonEmpty(req.Host.PublicIP, req.Host.PrivateIP, req.Host.LocalIP),
		Metadata:   nodeMetadata,
	}
	projectedServices := make([]controlplane.GuardInventoryServiceProjection, 0, len(req.Services))
	for index, svc := range req.Services {
		serviceKey := normalizeServiceKey(firstNonEmpty(svc.ServiceID, svc.Key, svc.Name))
		serviceID := runtimeidentity.ServiceID(worker.StackID, nodeID, serviceKey, svc.Instance)
		if serviceID == "" {
			continue
		}
		access := deriveInventoryServiceAccess(svc)
		serviceSource := inventoryServiceSource(svc)
		serviceMetadata := map[string]any{
			workerInventoryRuntimeAgentIDKey: worker.ID,
			runtimeLeaseIDKey:                runtimeLeaseIDFromMetadata(worker.Capabilities),
			workerInventoryObservedAtKey:     observedAt.Format(time.RFC3339Nano),
			"reported_status":                svc.Status,
			"target_server":                  nodeID,
			"container_id":                   svc.ContainerID,
			"platform_id":                    svc.PlatformID,
			"platform_type":                  svc.PlatformType,
			workerInventoryHealthKey:         svc.Health,
			workerInventoryEndpointsKey:      inventoryEndpoints(svc.Endpoints),
			"evidence_ref":                   svc.EvidenceRef,
		}
		if svc.Image != "" {
			serviceMetadata["image"] = svc.Image
		}
		if svc.Description != "" {
			serviceMetadata["description"] = svc.Description
		}
		if inventoryRevision > 0 {
			serviceMetadata["inventory_revision"] = inventoryRevision
		}
		serviceRecord := controlplane.Service{
			ID:         serviceID,
			TenantID:   worker.TenantID,
			InstanceID: worker.InstanceID,
			StackID:    worker.StackID,
			NodeID:     nodeID,
			ServiceKey: firstNonEmpty(serviceKey, serviceID),
			Name:       firstNonEmpty(svc.Name, svc.Key, svc.ServiceID, serviceID),
			// The legacy status column describes what the runtime is doing, not
			// its health. Health stays on the canonical runtime projection.
			Status:   canonicalServiceObservedState(svc.Status),
			Source:   serviceSource,
			URL:      stringFromAnyMap(access, serviceAccessURLKey),
			Metadata: serviceMetadata,
		}
		runtimeMetadata := mergeAnyMaps(serviceMetadata, map[string]any{
			"reported_service_id": svc.ID, "container_id": svc.ContainerID,
			"platform_id": svc.PlatformID, "platform_type": svc.PlatformType,
		})
		projectedServices = append(projectedServices, controlplane.GuardInventoryServiceProjection{
			Legacy: serviceRecord,
			Runtime: controlplane.ServiceRuntime{
				ID: serviceID, TenantID: worker.TenantID, InstanceID: worker.InstanceID,
				StackID: worker.StackID, ServerID: nodeID, ServiceKey: serviceKey,
				ServiceInstance: svc.Instance, Name: firstNonEmpty(svc.Name, serviceKey),
				DesiredState: inventoryServiceDesiredSeed(svc, serviceSource), ObservedState: canonicalServiceObservedState(svc.Status),
				HealthState: string(serviceStates[index]), ObservedAt: &observedAt,
				StackKitVersion: svc.StackKit, Access: access,
				Capabilities: svc.Actions, Source: serviceSource,
				Metadata: runtimeMetadata,
			},
		})
	}
	return node, projectedServices
}

// inventoryServiceSource resolves the provenance of one Guard-reported
// service onto the closed pkg/serviceregistry vocabulary.
//
// An absent provenance stays `stackkits-inventory`. That is not an optimistic
// default: before unmanaged discovery existed, the Guard could only ever report
// services read from a StackKit access manifest, so an agent that says nothing
// is reporting exactly those. Only an agent that explicitly says `observed`
// gets the observed, contract-free projection, and any value outside the
// vocabulary falls closed to `observed` in CanonicalSource rather than claiming
// a contract we cannot prove.
func inventoryServiceSource(svc workerInventoryService) string {
	if strings.TrimSpace(svc.Source) == "" {
		return stackKitsInventorySource
	}
	return serviceregistry.CanonicalSource(svc.Source)
}

// inventoryServiceDesiredSeed returns the desired state a Guard observation may
// seed while a service aggregate is created. An observed service has no
// declared contract, so it carries no intent at all: seeding `running` for it
// would make drift comparison report against a target nobody ever declared.
func inventoryServiceDesiredSeed(svc workerInventoryService, source string) string {
	if !serviceregistry.DesiredStateApplicable(serviceregistry.ManagementStateForSource(source)) {
		return ""
	}
	return firstNonEmpty(svc.DesiredState, registryStatusRunning)
}

func (h workerRouteHandlers) reconcileInventoryRegistryServices(e *httpx.Event, worker controlplane.Worker, nodeID string, observed map[string]bool) error {
	if h.registryStore == nil || strings.TrimSpace(worker.StackID) == "" || strings.TrimSpace(nodeID) == "" {
		return nil
	}
	existing, err := h.registryStore.ListServicesByStack(e.Request.Context(), worker.TenantID, worker.StackID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to reconcile service inventory", nil)
	}
	for _, service := range existing {
		if service.Source != stackKitsInventorySource || observed[service.ID] || registryServiceActiveMigrationState(service) != "" {
			continue
		}
		belongsToServer := service.NodeID == nodeID ||
			stringFromAnyMap(service.Metadata, "target_server") == nodeID ||
			stringFromAnyMap(service.Metadata, workerInventoryRuntimeAgentIDKey) == worker.ID
		if !belongsToServer {
			continue
		}
		if err := h.registryStore.DeleteService(e.Request.Context(), worker.TenantID, service.ID); err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to remove missing service inventory", nil)
		}
	}
	return nil
}

func (h workerRouteHandlers) upsertInventoryRIL(e *httpx.Event, worker controlplane.Worker, nodeID string, req workerInventoryRequest, now time.Time) error {
	if h.rilStore == nil {
		return nil
	}
	_, err := h.rilStore.UpsertRILServer(e.Request.Context(), inventoryRILServer(worker, nodeID, req, now))
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update RIL server inventory", nil)
	}
	return nil
}

func inventoryRILServer(worker controlplane.Worker, nodeID string, req workerInventoryRequest, now time.Time) controlplane.RILServer {
	observedState := ""
	for _, svc := range req.Services {
		if inventoryServiceHealthState(svc, runtimehealth.ServerHealthy) == runtimehealth.ServiceUnhealthy {
			observedState = runtimeHealthDegraded
			break
		}
	}
	serverState := runtimehealth.DeriveServerState(runtimehealth.ServerInput{Now: now, HeartbeatAt: &now, ObservedState: observedState})
	runtimeConvergence := runtimeConvergenceMetadata(worker)
	health := map[string]any{
		preCheckStatusField:              string(serverState),
		workerInventoryRuntimeAgentIDKey: worker.ID,
		runtimeLeaseIDKey:                runtimeLeaseIDFromMetadata(worker.Capabilities),
		workerInventoryCPUPercentKey:     req.Host.CPUPercent,
		workerInventoryUptimeSecondsKey:  req.Host.UptimeSeconds,
	}
	inventory := map[string]any{
		workerInventoryHostKey:      inventoryHostMap(req.Host),
		registryCollectionServices:  inventoryServices(req.Services),
		workerInventoryChannelsKey:  inventoryChannels(req.Channels),
		workerInventoryEndpointsKey: inventoryEndpoints(req.Endpoints),
		"deployment":                inventoryDeploymentMetadata(req),
	}
	if runtimeConvergence != nil {
		health["runtime_convergence"] = runtimeConvergence
		inventory["runtime_convergence"] = runtimeConvergence
	}
	return controlplane.RILServer{
		ID:         nodeID,
		TenantID:   worker.TenantID,
		InstanceID: worker.InstanceID,
		StackID:    worker.StackID,
		NodeID:     nodeID,
		Name:       firstNonEmpty(req.Hostname, nodeID),
		Status:     string(serverState),
		LastSeenAt: &now,
		Health:     health,
		Inventory:  inventory,
	}
}

func inventoryServiceHealthState(svc workerInventoryService, serverState runtimehealth.ServerState) runtimehealth.ServiceState {
	endpointHealths := make([]string, 0, len(svc.Endpoints))
	for _, endpoint := range svc.Endpoints {
		endpointHealths = append(endpointHealths, endpoint.Health)
	}
	return runtimehealth.DeriveServiceState(serverState, svc.Status, svc.Health, endpointHealths)
}

func inventoryEndpointCount(req workerInventoryRequest) int {
	count := len(req.Endpoints)
	for _, service := range req.Services {
		count += len(service.Endpoints)
	}
	return count
}

func inventoryHostMap(host workerInventoryHost) map[string]any {
	return map[string]any{
		workerFieldHostname:              host.Hostname,
		"os":                             host.OS,
		"os_version":                     host.OSVersion,
		workerInventoryArchKey:           host.Arch,
		"public_ip":                      host.PublicIP,
		"private_ip":                     host.PrivateIP,
		"local_ip":                       host.LocalIP,
		workerFieldCPU:                   host.CPUCores,
		workerFieldRAM:                   host.RAMMB,
		workerFieldDisk:                  host.DiskGB,
		workerInventoryCPUPercentKey:     host.CPUPercent,
		workerInventoryMemoryUsedKey:     host.MemoryUsedBytes,
		workerInventoryMemoryTotalKey:    host.MemoryTotalBytes,
		workerInventoryDiskUsedBytesKey:  host.DiskUsedBytes,
		workerInventoryDiskTotalBytesKey: host.DiskTotalBytes,
		workerInventoryUptimeSecondsKey:  host.UptimeSeconds,
	}
}

func inventoryDeploymentMetadata(req workerInventoryRequest) map[string]any {
	metadata := map[string]any{}
	for key, value := range map[string]string{
		serviceStackKit:    req.StackKit,
		"stackkit_version": req.StackKitVersion,
		"stackkit_mode":    req.StackKitMode,
		"domain":           req.Domain,
	} {
		if value = strings.TrimSpace(value); value != "" {
			metadata[key] = value
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func inventoryServices(services []workerInventoryService) []map[string]any {
	out := make([]map[string]any, 0, len(services))
	for _, svc := range services {
		access := deriveInventoryServiceAccess(svc)
		out = append(out, map[string]any{
			"id":                        svc.ID,
			serviceInventoryIDKey:       svc.ServiceID,
			"key":                       svc.Key,
			backupNamePathKey:           svc.Name,
			preCheckStatusField:         svc.Status,
			"source":                    inventoryServiceSource(svc),
			serviceAccessURLKey:         stringFromAnyMap(access, serviceAccessURLKey),
			"access":                    access,
			"owner_stack":               svc.OwnerStack,
			"target_server":             svc.TargetServer,
			"container_id":              svc.ContainerID,
			"platform_id":               svc.PlatformID,
			"platform_type":             svc.PlatformType,
			"image":                     svc.Image,
			"description":               svc.Description,
			"instance":                  svc.Instance,
			"stackkit_version":          svc.StackKit,
			"actions":                   svc.Actions,
			workerInventoryHealthKey:    svc.Health,
			workerInventoryEndpointsKey: inventoryEndpoints(svc.Endpoints),
		})
	}
	return out
}

func inventoryEndpoints(endpoints []workerInventoryEndpoint) []map[string]any {
	out := make([]map[string]any, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpointURL := ""
		address := endpoint.Address
		if strings.EqualFold(endpoint.TargetType, "tunnel") {
			endpointURL = safeKombifyRelayURL(endpoint.URL)
			address = ""
		} else if parsed, ok := parseServiceURL(endpoint.URL); ok {
			endpointURL = parsed.String()
		}
		if parsed, ok := parseServiceURL(address); ok {
			address = parsed.String()
		} else if strings.Contains(address, "@") {
			address = ""
		}
		out = append(out, map[string]any{
			serviceAccessURLKey:      endpointURL,
			"address":                address,
			"visibility":             endpoint.Visibility,
			serviceAccessSourceKey:   endpoint.Provenance,
			serviceInventoryKindKey:  endpoint.Kind,
			workerInventoryHealthKey: endpoint.Health,
			"target_type":            endpoint.TargetType,
			"route_id":               endpoint.RouteID,
			"access_profile_ref":     endpoint.AccessProfileRef,
		})
	}
	return out
}

func inventoryChannels(channels []workerInventoryChannel) []map[string]any {
	out := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		out = append(out, map[string]any{
			serviceInventoryKindKey: channel.Kind,
			serviceAccessURLKey:     channel.URL,
			preCheckStatusField:     channel.Status,
			serviceAccessSourceKey:  channel.Provenance,
		})
	}
	return out
}

func mergeAnyMaps(left, right map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range left {
		out[key] = value
	}
	for key, value := range right {
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}
