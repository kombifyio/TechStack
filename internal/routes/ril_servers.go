// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains the RIL (Runtime Intelligence Layer) server inventory endpoints.
package routes

import (
	"errors"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RILServerResponse represents a server in the RIL API response.
type RILServerResponse struct {
	ID            string               `json:"id"`
	ServerID      string               `json:"server_id"`
	Name          string               `json:"name"`
	Hostname      string               `json:"hostname"`
	Status        string               `json:"status"`
	HealthScore   float64              `json:"health_score"`
	StackID       string               `json:"stack_id,omitempty"`
	OSName        string               `json:"os_name,omitempty"`
	OSVersion     string               `json:"os_version,omitempty"`
	Arch          string               `json:"arch,omitempty"`
	KernelVersion string               `json:"kernel_version,omitempty"`
	PublicIP      string               `json:"public_ip,omitempty"`
	PrivateIP     string               `json:"private_ip,omitempty"`
	AgentVersion  string               `json:"agent_version,omitempty"`
	Resources     *RILResourceResponse `json:"resources,omitempty"`
	LastSeenAt    *time.Time           `json:"last_seen_at,omitempty"`
	RegisteredAt  *time.Time           `json:"registered_at,omitempty"`
	Capabilities  []string             `json:"capabilities,omitempty"`
}

// RILResourceResponse is the resource usage in the API response.
type RILResourceResponse struct {
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	DiskUsedBytes    int64   `json:"disk_used_bytes"`
	DiskTotalBytes   int64   `json:"disk_total_bytes"`
}

// RILServerStatusResponse is the detailed live status response.
type RILServerStatusResponse struct {
	RILServerResponse
	Services   []RILServiceResponse   `json:"services,omitempty"`
	Containers []RILContainerResponse `json:"containers,omitempty"`
	UptimeSecs int64                  `json:"uptime_seconds,omitempty"`
}

// RILServiceResponse represents a running service on the server.
type RILServiceResponse struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Type         string  `json:"type"`
	UptimeSecs   int64   `json:"uptime_seconds,omitempty"`
	MemoryBytes  int64   `json:"memory_bytes,omitempty"`
	CPUPercent   float64 `json:"cpu_percent,omitempty"`
	RestartCount int32   `json:"restart_count,omitempty"`
}

// RILContainerResponse represents a running container on the server.
type RILContainerResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Status      string   `json:"status"`
	MemoryBytes int64    `json:"memory_bytes,omitempty"`
	CPUPercent  float64  `json:"cpu_percent,omitempty"`
	Ports       []string `json:"ports,omitempty"`
}

// RegisterRILServerRoutes adds RIL server inventory API endpoints backed by the
// Postgres control-plane store.
func RegisterRILServerRoutes(r *httpx.Router, store controlplane.RILStore, grpcServer *grpcserver.Server) {
	h := rilServerHandler{store: store, grpcServer: grpcServer}
	r.GET("/api/v1/ril/servers", h.listServers)
	r.GET("/api/v1/ril/servers/{serverId}", h.getServer)
	r.GET("/api/v1/ril/servers/{serverId}/status", h.getServerStatus)
}

type rilServerHandler struct {
	store      controlplane.RILStore
	grpcServer *grpcserver.Server
}

func (h rilServerHandler) requireRILServer(e *httpx.Event) (*controlplane.RILServer, string, error) {
	ownerID, err := requireAuth(e)
	if err != nil {
		return nil, "", err
	}
	serverID := e.Request.PathValue("serverId")
	if serverID == "" {
		return nil, "", httpx.BadRequest(e, "Server ID is required", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	srv, err := h.store.GetRILServer(e.Request.Context(), tenantID, serverID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil, "", httpx.NotFound(e, "Server not found")
		}
		return nil, "", httpx.Error(e, http.StatusInternalServerError, "ril_get_failed", "Failed to load server", nil)
	}
	return srv, ownerID, nil
}

func (h rilServerHandler) listServers(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	tenantID := requestTenantID(e, ownerID)
	servers, err := h.store.ListRILServersByTenant(e.Request.Context(), tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_list_failed", "Failed to list servers", nil)
	}

	// RIL responses report the persisted observed state only. The former live
	// gRPC enrichment fabricated LastSeenAt=now and status=online for any
	// connected agent; the registry sweeper now keeps the persisted state
	// honest instead.
	responses := make([]RILServerResponse, 0, len(servers))
	for _, s := range servers {
		responses = append(responses, rilServerToResponse(s))
	}

	pagination := ksapi.ParsePagination(e.Request)
	total := len(responses)
	paged := ksapi.Paginate(responses, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, paged, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func (h rilServerHandler) getServer(e *httpx.Event) error {
	srv, _, err := h.requireRILServer(e)
	if err != nil {
		return err
	}

	// Persisted observed state only; no read-time liveness fabrication.
	return httpx.Success(e, http.StatusOK, rilServerToResponse(*srv))
}

func (h rilServerHandler) getServerStatus(e *httpx.Event) error {
	srv, _, err := h.requireRILServer(e)
	if err != nil {
		return err
	}

	// Persisted observed state only; live gRPC data below augments services
	// and uptime but never rewrites status or LastSeenAt.
	base := rilServerToResponse(*srv)
	agentID := rilServerAgentID(*srv)

	status := RILServerStatusResponse{
		RILServerResponse: base,
		Services:          rilInventoryServices(*srv),
	}
	if uptime, ok := float64FromAny(srv.Health["uptime_seconds"]); ok && uptime > 0 {
		status.UptimeSecs = int64(uptime)
	}

	if h.grpcServer != nil {
		if agent, found := h.grpcServer.GetAgent(agentID); found {
			status.UptimeSecs = int64(time.Since(agent.ConnectedAt).Seconds())
			status.Services = status.Services[:0]
			for _, svc := range agent.Services {
				status.Services = append(status.Services, RILServiceResponse{
					Name:         svc.Name,
					Status:       svc.Status,
					Type:         svc.Type,
					UptimeSecs:   svc.UptimeSecs,
					MemoryBytes:  svc.MemoryBytes,
					CPUPercent:   svc.CPUPercent,
					RestartCount: svc.RestartCount,
				})
			}
			for _, c := range agent.Containers {
				status.Containers = append(status.Containers, RILContainerResponse{
					ID:          c.ID,
					Name:        c.Name,
					Image:       c.Image,
					Status:      c.Status,
					MemoryBytes: c.MemoryBytes,
					CPUPercent:  c.CPUPercent,
					Ports:       c.Ports,
				})
			}
		}
	}

	return httpx.Success(e, http.StatusOK, status)
}

// rilServerAgentID resolves the runtime agent id reported with the last
// inventory push; it keys the live gRPC agent lookup.
func rilServerAgentID(s controlplane.RILServer) string {
	return stringFromAnyMap(s.Health, "runtime_agent_id")
}

func rilServerHostMap(s controlplane.RILServer) map[string]any {
	host, _ := s.Inventory["host"].(map[string]any)
	return host
}

func rilServerToResponse(s controlplane.RILServer) RILServerResponse {
	host := rilServerHostMap(s)
	resp := RILServerResponse{
		ID:         s.ID,
		ServerID:   firstNonEmpty(s.NodeID, s.ID),
		Name:       s.Name,
		Hostname:   stringFromAnyMap(host, "hostname"),
		Status:     s.Status,
		StackID:    s.StackID,
		OSName:     stringFromAnyMap(host, "os"),
		Arch:       stringFromAnyMap(host, "arch"),
		PublicIP:   stringFromAnyMap(host, "public_ip"),
		PrivateIP:  stringFromAnyMap(host, "private_ip"),
		LastSeenAt: s.LastSeenAt,
	}
	if !s.CreatedAt.IsZero() {
		registeredAt := s.CreatedAt
		resp.RegisteredAt = &registeredAt
	}
	if resources := rilServerResources(s, host); resources != nil {
		resp.Resources = resources
	}
	return resp
}

func rilServerResources(s controlplane.RILServer, host map[string]any) *RILResourceResponse {
	cpuPercent, hasCPU := float64FromAny(s.Health["cpu_percent"])
	if !hasCPU {
		cpuPercent, hasCPU = float64FromAny(host["cpu_percent"])
	}
	memoryUsed, hasMemory := float64FromAny(host["memory_used_bytes"])
	if !hasCPU && !hasMemory {
		return nil
	}
	memoryTotal, _ := float64FromAny(host["memory_total_bytes"])
	diskUsed, _ := float64FromAny(host["disk_used_bytes"])
	diskTotal, _ := float64FromAny(host["disk_total_bytes"])
	return &RILResourceResponse{
		CPUPercent:       cpuPercent,
		MemoryUsedBytes:  int64(memoryUsed),
		MemoryTotalBytes: int64(memoryTotal),
		DiskUsedBytes:    int64(diskUsed),
		DiskTotalBytes:   int64(diskTotal),
	}
}

// rilInventoryServices projects the persisted inventory service snapshot into
// the status response, so the endpoint stays useful between agent connections.
func rilInventoryServices(s controlplane.RILServer) []RILServiceResponse {
	raw, _ := s.Inventory["services"].([]any)
	out := make([]RILServiceResponse, 0, len(raw))
	for _, entry := range raw {
		svc, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name := firstNonEmpty(stringFromAnyMap(svc, "name"), stringFromAnyMap(svc, "key"), stringFromAnyMap(svc, "service_id"))
		if name == "" {
			continue
		}
		out = append(out, RILServiceResponse{
			Name:   name,
			Status: stringFromAnyMap(svc, "status"),
			Type:   stringFromAnyMap(svc, "platform_type"),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
