// RIL command/inspection endpoints; response keys intentionally mirror the RIL
// wire schema.
//
//nolint:goconst
package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/google/uuid"
)

// RILCommandRequest represents a command to dispatch to an agent.
type RILCommandRequest struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

// RILCommandResponse represents the result of a dispatched command.
type RILCommandResponse struct {
	CommandID    string     `json:"command_id"`
	ServerID     string     `json:"server_id"`
	Command      string     `json:"command"`
	Status       string     `json:"status"`
	Output       string     `json:"output,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	DispatchedAt time.Time  `json:"dispatched_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// RILSearchRequest defines a cross-server search query.
type RILSearchRequest struct {
	Query   string   `json:"query"`
	Scope   string   `json:"scope,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

// RILSearchResult is a single search hit.
type RILSearchResult struct {
	ServerID string `json:"server_id"`
	Source   string `json:"source"`
	Line     string `json:"line"`
	Context  string `json:"context,omitempty"`
}

// RegisterRILCommandRoutes adds command dispatch, search, and detailed
// server inspection endpoints backed by the Postgres control-plane store.
func RegisterRILCommandRoutes(r *httpx.Router, store controlplane.RILStore, grpcServer *grpcserver.Server) {
	h := rilCommandHandler{store: store, grpcServer: grpcServer}

	r.POST("/api/v1/ril/servers/{serverId}/cmd", h.dispatchCommand)
	r.GET("/api/v1/ril/servers/{serverId}/cmd/{cmdId}", h.getCommandStatus)

	r.GET("/api/v1/ril/servers/{serverId}/services", h.listServices)
	r.GET("/api/v1/ril/servers/{serverId}/containers", h.listContainers)
	r.GET("/api/v1/ril/servers/{serverId}/metrics", h.queryMetrics)
	r.GET("/api/v1/ril/servers/{serverId}/diff/{otherId}", h.diffServers)

	r.POST("/api/v1/ril/search", h.crossServerSearch)
}

type rilCommandHandler struct {
	store      controlplane.RILStore
	grpcServer *grpcserver.Server
}

func (h rilCommandHandler) requireRILServer(e *httpx.Event, ownerID string) (*controlplane.RILServer, error) {
	serverID := e.Request.PathValue("serverId")
	if serverID == "" {
		return nil, httpx.BadRequest(e, "Server ID is required", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	srv, err := h.store.GetRILServer(e.Request.Context(), tenantID, serverID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return nil, httpx.NotFound(e, "Server not found")
		}
		return nil, httpx.Error(e, http.StatusInternalServerError, "ril_get_failed", "Failed to load server", nil)
	}
	return srv, nil
}

func (h rilCommandHandler) dispatchCommand(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	// Shell-level command dispatch is disabled for the shared public demo
	// account: with published credentials it would be remote code execution on
	// real provider VMs.
	if demoRestrictedRequest(e, ownerID) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Command dispatch is disabled on the kombify demo account", demoRestrictedDetails("ril_command_dispatch"))
	}

	srv, srvErr := h.requireRILServer(e, ownerID)
	if srvErr != nil {
		return srvErr
	}

	var req RILCommandRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if req.Command == "" {
		return httpx.BadRequest(e, "Command is required", nil)
	}

	cmdID := uuid.New().String()
	now := time.Now().UTC()

	resp := RILCommandResponse{
		CommandID:    cmdID,
		ServerID:     firstNonEmpty(srv.NodeID, srv.ID),
		Command:      req.Command,
		Status:       "dispatched",
		DispatchedAt: now,
	}

	agentID := rilServerAgentID(*srv)
	if h.grpcServer != nil && agentID != "" {
		if _, found := h.grpcServer.GetAgent(agentID); found {
			timeout := 60 * time.Second
			if req.Timeout > 0 {
				timeout = time.Duration(req.Timeout) * time.Second
			}
			cmd := &grpcserver.AgentCommand{
				ID:      cmdID,
				AgentID: agentID,
				Type:    "ril-cmd",
				Command: req.Command,
				Timeout: timeout,
			}
			if err := h.grpcServer.SendCommand(cmd); err != nil {
				resp.Status = "queue_error"
			} else {
				resp.Status = "queued"
			}
		} else {
			resp.Status = "agent_offline"
		}
	} else {
		resp.Status = "agent_offline"
	}

	if _, err := h.store.EnqueueRILCommand(e.Request.Context(), controlplane.RILCommand{
		ID:             cmdID,
		TenantID:       srv.TenantID,
		ServerID:       srv.ID,
		ActorSubjectID: ownerID,
		CommandClass:   "ril-cmd",
		Status:         resp.Status,
		Request: map[string]any{
			"command":         req.Command,
			"description":     req.Description,
			"timeout_seconds": req.Timeout,
			"dispatched_at":   now.Format(time.RFC3339Nano),
		},
	}); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_command_persist_failed", "Failed to persist command", nil)
	}

	return httpx.Success(e, http.StatusAccepted, resp)
}

func (h rilCommandHandler) getCommandStatus(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	cmdID := e.Request.PathValue("cmdId")
	tenantID := requestTenantID(e, ownerID)
	command, err := h.store.GetRILCommand(e.Request.Context(), tenantID, cmdID)
	if err != nil {
		return httpx.NotFound(e, "Command not found")
	}

	resp := RILCommandResponse{
		CommandID:    command.ID,
		ServerID:     command.ServerID,
		Command:      stringFromAnyMap(command.Request, "command"),
		Status:       command.Status,
		Output:       stringFromAnyMap(command.Result, "output"),
		DispatchedAt: command.CreatedAt,
		CompletedAt:  command.CompletedAt,
	}
	if exitCode, ok := float64FromAny(command.Result["exit_code"]); ok {
		code := int(exitCode)
		resp.ExitCode = &code
	}

	return httpx.Success(e, http.StatusOK, resp)
}

func (h rilCommandHandler) listServices(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	srv, srvErr := h.requireRILServer(e, ownerID)
	if srvErr != nil {
		return srvErr
	}

	services := make([]RILServiceResponse, 0)
	if h.grpcServer != nil {
		if agent, found := h.grpcServer.GetAgent(rilServerAgentID(*srv)); found {
			for _, svc := range agent.Services {
				services = append(services, RILServiceResponse{
					Name:         svc.Name,
					Status:       svc.Status,
					Type:         svc.Type,
					UptimeSecs:   svc.UptimeSecs,
					MemoryBytes:  svc.MemoryBytes,
					CPUPercent:   svc.CPUPercent,
					RestartCount: svc.RestartCount,
				})
			}
		}
	}
	if len(services) == 0 {
		services = append(services, rilInventoryServices(*srv)...)
	}

	return httpx.Success(e, http.StatusOK, services)
}

func (h rilCommandHandler) listContainers(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	srv, srvErr := h.requireRILServer(e, ownerID)
	if srvErr != nil {
		return srvErr
	}

	containers := make([]RILContainerResponse, 0)
	if h.grpcServer != nil {
		if agent, found := h.grpcServer.GetAgent(rilServerAgentID(*srv)); found {
			for _, c := range agent.Containers {
				containers = append(containers, RILContainerResponse{
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

	return httpx.Success(e, http.StatusOK, containers)
}

func (h rilCommandHandler) queryMetrics(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	srv, srvErr := h.requireRILServer(e, ownerID)
	if srvErr != nil {
		return srvErr
	}

	query := e.Request.URL.Query().Get("query")

	resp := map[string]any{
		"server_id": firstNonEmpty(srv.NodeID, srv.ID),
		"query":     query,
		"data":      []any{},
	}

	populated := false
	if h.grpcServer != nil {
		if agent, found := h.grpcServer.GetAgent(rilServerAgentID(*srv)); found && agent.Resources != nil {
			resp["data"] = map[string]any{
				"cpu_percent":        agent.Resources.CPUPercent,
				"memory_used_bytes":  agent.Resources.MemoryUsedBytes,
				"memory_total_bytes": agent.Resources.MemoryTotalBytes,
				"disk_used_bytes":    agent.Resources.DiskUsedBytes,
				"disk_total_bytes":   agent.Resources.DiskTotalBytes,
			}
			populated = true
		}
	}
	if !populated {
		host := rilServerHostMap(*srv)
		if resources := rilServerResources(*srv, host); resources != nil {
			resp["data"] = map[string]any{
				"cpu_percent":        resources.CPUPercent,
				"memory_used_bytes":  resources.MemoryUsedBytes,
				"memory_total_bytes": resources.MemoryTotalBytes,
				"disk_used_bytes":    resources.DiskUsedBytes,
				"disk_total_bytes":   resources.DiskTotalBytes,
			}
		}
	}

	return httpx.Success(e, http.StatusOK, resp)
}

func (h rilCommandHandler) diffServers(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	tenantID := requestTenantID(e, ownerID)
	serverA := e.Request.PathValue("serverId")
	serverB := e.Request.PathValue("otherId")

	srvA, errA := h.store.GetRILServer(e.Request.Context(), tenantID, serverA)
	srvB, errB := h.store.GetRILServer(e.Request.Context(), tenantID, serverB)
	if errA != nil || errB != nil {
		return httpx.NotFound(e, "One or both servers not found")
	}

	resp := map[string]any{
		"server_a": rilDiffServerSummary(*srvA),
		"server_b": rilDiffServerSummary(*srvB),
		"diffs":    []any{},
	}

	return httpx.Success(e, http.StatusOK, resp)
}

func rilDiffServerSummary(srv controlplane.RILServer) map[string]any {
	host := rilServerHostMap(srv)
	return map[string]any{
		"server_id":       firstNonEmpty(srv.NodeID, srv.ID),
		backupNamePathKey: srv.Name,
		"os":              fmt.Sprintf("%s %s", stringFromAnyMap(host, "os"), stringFromAnyMap(host, "arch")),
		"status":          srv.Status,
	}
}

func (h rilCommandHandler) crossServerSearch(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	var req RILSearchRequest
	if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if req.Query == "" {
		return httpx.BadRequest(e, "Query is required", nil)
	}

	tenantID := requestTenantID(e, ownerID)
	if _, err := h.store.ListRILServersByTenant(e.Request.Context(), tenantID); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_search_failed", "Failed to list servers", nil)
	}

	results := make([]RILSearchResult, 0)

	resp := map[string]any{
		"query":   req.Query,
		"scope":   req.Scope,
		"results": results,
		"total":   len(results),
	}

	return httpx.Success(e, http.StatusOK, resp)
}
