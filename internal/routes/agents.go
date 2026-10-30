// Package routes provides custom HTTP routes for kombifyTechstack API.
package routes

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// AgentResponse represents an agent in the API response.
type AgentResponse struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	Version      string            `json:"version"`
	Status       string            `json:"status"`
	Capabilities []string          `json:"capabilities"`
	Resources    *ResourceResponse `json:"resources,omitempty"`
	ConnectedAt  string            `json:"connectedAt"`
	LastSeen     string            `json:"lastSeen"`
}

// ResourceResponse represents agent resource usage.
type ResourceResponse struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  int64   `json:"memoryUsedBytes"`
	MemoryTotalBytes int64   `json:"memoryTotalBytes"`
	DiskUsedBytes    int64   `json:"diskUsedBytes"`
	DiskTotalBytes   int64   `json:"diskTotalBytes"`
}

// RegisterAgentRoutes adds agent API endpoints.
// These endpoints provide access to connected gRPC agents.
func RegisterAgentRoutes(r *httpx.Router, grpcServer *grpcserver.Server) {
	RegisterAgentRoutesWithRuntimeLogs(r, grpcServer, grpcServer.RuntimeLogStore())
}

// RegisterAgentRoutesWithRuntimeLogs keeps HTTP runtime history independent of
// whether this channel owns a gRPC listener. Connected-agent APIs are unchanged.
func RegisterAgentRoutesWithRuntimeLogs(r *httpx.Router, grpcServer *grpcserver.Server, runtimeLogs *grpcserver.RuntimeLogStore) {
	handler := agentRouteHandler{grpcServer: grpcServer, runtimeLogs: runtimeLogs}
	r.GET("/api/v1/agents", handler.listAgents)
	r.GET("/api/v1/agents/{id}", handler.getAgent)
	r.POST("/api/v1/agents/{id}/command", handler.sendCommand)
	r.DELETE("/api/v1/agents/{id}", handler.removeAgent)
	r.GET("/api/v1/agents/{id}/logs", handler.getAgentLogs)
	r.GET("/api/v1/agents/{id}/logs/stream", handler.streamAgentLogs)
	r.GET("/api/v1/runtime/logs", handler.getRuntimeLogs)
	r.GET("/api/v1/runtime/logs/stream", handler.streamRuntimeLogs)
}

type agentRouteHandler struct {
	grpcServer  *grpcserver.Server
	runtimeLogs *grpcserver.RuntimeLogStore
}

func (h agentRouteHandler) runtimeLogStore() *grpcserver.RuntimeLogStore {
	if h.runtimeLogs != nil {
		return h.runtimeLogs
	}
	return h.grpcServer.RuntimeLogStore()
}

// agentTenantScope resolves the tenant a hosted caller may see agents of. The
// connected-agent table is process-wide, so in hosted mode every agent read and
// write is narrowed to agents whose enrollment pinned the caller's tenant;
// self-hosted keeps the single-tenant view (enforce false).
func agentTenantScope(e *httpx.Event, capability string) (tenantID string, enforce bool, err error) {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return "", false, authErr
	}
	if !tenantguard.Active() {
		return "", false, nil
	}
	tenantID, err = tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if err != nil {
		return "", false, err
	}
	return tenantID, true, nil
}

func agentVisible(agent *grpcserver.ConnectedAgent, tenantID string, enforce bool) bool {
	if agent == nil {
		return false
	}
	return !enforce || (tenantID != "" && agent.Tenant == tenantID)
}

// visibleAgent loads one agent for the caller. A foreign agent is reported as
// not found, never as forbidden, so agent ids cannot be probed across tenants.
func (h agentRouteHandler) visibleAgent(e *httpx.Event, capability string) (*grpcserver.Server, string, error) {
	tenantID, enforce, err := agentTenantScope(e, capability)
	if err != nil {
		return nil, "", err
	}
	agentID, err := agentIDFromRequest(e)
	if err != nil {
		return nil, "", err
	}
	server, err := h.requireGRPCServer(e)
	if err != nil {
		return nil, "", err
	}
	agent, found := server.GetAgent(agentID)
	if !found || !agentVisible(agent, tenantID, enforce) {
		return nil, "", httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Agent not found", nil)
	}
	return server, agentID, nil
}

func (h agentRouteHandler) listAgents(e *httpx.Event) error {
	tenantID, enforce, err := agentTenantScope(e, "techstack.agents.list")
	if err != nil {
		return err
	}

	pagination := ksapi.ParsePagination(e.Request)
	allAgents := []AgentResponse{}
	if h.grpcServer != nil {
		for _, agent := range h.grpcServer.GetAgents() {
			if agentVisible(agent, tenantID, enforce) {
				allAgents = append(allAgents, agentResponse(agent))
			}
		}
	}

	total := len(allAgents)
	agents := ksapi.Paginate(allAgents, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, agents, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func (h agentRouteHandler) getAgent(e *httpx.Event) error {
	server, agentID, err := h.visibleAgent(e, "techstack.agents.read")
	if err != nil {
		return err
	}
	agent, found := server.GetAgent(agentID)
	if !found {
		return httpx.NotFound(e, "Agent not found")
	}
	return httpx.Success(e, http.StatusOK, agentResponse(agent))
}

func (h agentRouteHandler) sendCommand(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	server, agentID, err := h.visibleAgent(e, "techstack.agents.command")
	if err != nil {
		return err
	}

	var req struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	}
	if bindErr := e.BindBody(&req); bindErr != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}

	commandType, commandName, err := normalizeAgentCommandRequest(req.Type, req.Command)
	if err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	cmdID, err := generateCommandID()
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to generate command ID", nil)
	}

	cmd := &grpcserver.AgentCommand{
		ID:      cmdID,
		AgentID: agentID,
		Type:    commandType,
		Command: commandName,
		Args:    req.Args,
	}
	if err := server.SendCommandWithPersistence(cmd, ownerID); err != nil {
		return httpx.BadRequest(e, err.Error(), nil)
	}

	return httpx.Success(e, http.StatusAccepted, map[string]any{
		"commandId": cmd.ID,
		"agentId":   agentID,
		"status":    "queued",
	})
}

func (h agentRouteHandler) removeAgent(e *httpx.Event) error {
	server, agentID, err := h.visibleAgent(e, "techstack.agents.remove")
	if err != nil {
		return err
	}
	if err := server.RemoveAgent(agentID); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]string{"message": "Agent removed successfully", "agentId": agentID})
}

func (h agentRouteHandler) getAgentLogs(e *httpx.Event) error {
	server, agentID, err := h.visibleAgent(e, "techstack.agents.logs")
	if err != nil {
		return err
	}

	logs, ok := server.GetAgentLogs(agentID, 200)
	if !ok {
		return httpx.NotFound(e, "Agent not found")
	}
	return httpx.Success(e, http.StatusOK, agentLogsResponse(logs))
}

func (h agentRouteHandler) streamAgentLogs(e *httpx.Event) error {
	server, agentID, err := h.visibleAgent(e, "techstack.agents.logs.stream")
	if err != nil {
		return err
	}

	e.Response.Header().Set("Content-Type", "text/event-stream")
	e.Response.Header().Set("Cache-Control", "no-cache")
	e.Response.Header().Set("Connection", "keep-alive")
	e.Response.WriteHeader(http.StatusOK)

	flusher, ok := e.Response.(http.Flusher)
	if !ok {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "SSE not supported", nil)
	}

	history, _ := server.GetAgentLogs(agentID, 200)
	if sendErr := sendSSEEvent(e.Response, "history", agentLogsResponse(history)); sendErr != nil {
		return nil
	}
	flusher.Flush()

	ch, cancel, err := server.SubscribeAgentLogs(agentID, 200)
	if err != nil {
		return httpx.NotFound(e, "Agent not found")
	}
	defer cancel()

	return streamAgentLogEvents(e, flusher, ch)
}

func (h agentRouteHandler) getRuntimeLogs(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.runtime-logs.read")
	if tenantErr != nil {
		return tenantErr
	}
	query := runtimeLogQueryFromRequest(e.Request, tenantID)
	store := h.runtimeLogStore()
	return httpx.Success(e, http.StatusOK, agentLogsResponse(store.GetRuntimeLogs(query)))
}

func (h agentRouteHandler) streamRuntimeLogs(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.runtime-logs.stream")
	if tenantErr != nil {
		return tenantErr
	}
	store := h.runtimeLogStore()
	if store == nil {
		return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Runtime log store not available", nil)
	}

	e.Response.Header().Set("Content-Type", "text/event-stream")
	e.Response.Header().Set("Cache-Control", "no-cache")
	e.Response.Header().Set("Connection", "keep-alive")
	e.Response.WriteHeader(http.StatusOK)

	flusher, ok := e.Response.(http.Flusher)
	if !ok {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "SSE not supported", nil)
	}

	query := runtimeLogQueryFromRequest(e.Request, tenantID)
	history := store.GetRuntimeLogs(query)
	if sendErr := sendSSEEvent(e.Response, "history", agentLogsResponse(history)); sendErr != nil {
		return nil
	}
	flusher.Flush()

	ch, cancel := store.SubscribeRuntimeLogs(query, 200)
	defer cancel()
	return streamAgentLogEvents(e, flusher, ch)
}

func (h agentRouteHandler) requireGRPCServer(e *httpx.Event) (*grpcserver.Server, error) {
	if h.grpcServer == nil {
		// Reject, not Error: the callers stop on a non-nil error, and a nil one
		// here sent them on to dereference the missing server.
		return nil, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "gRPC server not available", nil)
	}
	return h.grpcServer, nil
}

func agentIDFromRequest(e *httpx.Event) (string, error) {
	agentID := e.Request.PathValue("id")
	if agentID == "" {
		return "", httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Agent ID is required", nil)
	}
	return agentID, nil
}

func agentResponse(agent *grpcserver.ConnectedAgent) AgentResponse {
	resp := AgentResponse{
		ID:           agent.ID,
		Hostname:     agent.Hostname,
		OS:           agent.OS,
		Arch:         agent.Arch,
		Version:      agent.Version,
		Status:       agent.Status,
		Capabilities: agent.Capabilities,
		ConnectedAt:  agent.ConnectedAt.Format("2006-01-02T15:04:05Z07:00"),
		LastSeen:     agent.LastSeen.Format("2006-01-02T15:04:05Z07:00"),
	}
	if agent.Resources != nil {
		resp.Resources = &ResourceResponse{
			CPUPercent:       agent.Resources.CPUPercent,
			MemoryUsedBytes:  agent.Resources.MemoryUsedBytes,
			MemoryTotalBytes: agent.Resources.MemoryTotalBytes,
			DiskUsedBytes:    agent.Resources.DiskUsedBytes,
			DiskTotalBytes:   agent.Resources.DiskTotalBytes,
		}
	}
	return resp
}

func agentLogsResponse(logs []grpcserver.AgentLogEntry) []map[string]any {
	resp := make([]map[string]any, 0, len(logs))
	for _, entry := range logs {
		resp = append(resp, agentLogResponse(entry))
	}
	return resp
}

func agentLogResponse(entry grpcserver.AgentLogEntry) map[string]any {
	resp := map[string]any{
		"timestamp": entry.Timestamp.UTC().Format(time.RFC3339Nano),
		"level":     normalizeAgentLogLevel(entry.Level),
		"message":   entry.Message,
	}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			resp[key] = value
		}
	}
	put("id", entry.ID)
	if !entry.IngestedAt.IsZero() {
		put("ingested_at", entry.IngestedAt.UTC().Format(time.RFC3339Nano))
	}
	put("tenant_id", entry.TenantID)
	put("agent_id", entry.AgentID)
	put("source", entry.Source)
	put("kit_deployment_id", entry.StackID)
	put("job_id", entry.JobID)
	put("lease_id", entry.LeaseID)
	put("provider", entry.Provider)
	put("runtime_action_id", entry.RuntimeActionID)
	put("runtime_target_id", entry.RuntimeTargetID)
	put("server_id", entry.ServerID)
	put("service_id", entry.ServiceID)
	put("service_name", entry.ServiceName)
	put("container_id", entry.ContainerID)
	put("trace_id", entry.TraceID)
	put("span_id", entry.SpanID)
	put("runtime_scope_key", entry.RuntimeScopeKey)
	put("server_scope_key", entry.ServerScopeKey)
	put("service_scope_key", entry.ServiceScopeKey)
	put("scope_status", entry.ScopeStatus)
	if len(entry.Fields) > 0 {
		resp["fields"] = entry.Fields
	}
	return resp
}

func streamAgentLogEvents(e *httpx.Event, flusher http.Flusher, ch <-chan grpcserver.AgentLogEntry) error {
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-e.Request.Context().Done():
			return nil
		case <-keepAlive.C:
			_ = sendSSEEvent(e.Response, "ping", map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano)})
			flusher.Flush()
		case entry, ok := <-ch:
			if !ok {
				return nil
			}
			if err := sendSSEEvent(e.Response, "log", agentLogResponse(entry)); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

var restAgentCommandAllowlist = map[string]struct{}{
	"get_logs":     {},
	"health_check": {},
}

func normalizeAgentCommandRequest(commandType, commandName string) (string, string, error) {
	normalizedType := strings.ToLower(strings.TrimSpace(commandType))
	normalizedCommand := strings.ToLower(strings.TrimSpace(commandName))
	if normalizedType == "" || normalizedCommand == "" {
		return "", "", fmt.Errorf("type and command are required")
	}
	if normalizedType != normalizedCommand {
		return "", "", fmt.Errorf("command type %q must match command %q", normalizedType, normalizedCommand)
	}
	if _, ok := restAgentCommandAllowlist[normalizedCommand]; !ok {
		return "", "", fmt.Errorf("agent command %q is not allowed", normalizedCommand)
	}
	return normalizedType, normalizedCommand, nil
}

func normalizeAgentLogLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "warning" {
		return "warn"
	}
	if level == "" {
		return "info"
	}
	return level
}

func runtimeLogQueryFromRequest(req *http.Request, tenantID string) grpcserver.RuntimeLogQuery {
	if req == nil {
		return grpcserver.RuntimeLogQuery{TenantID: tenantID, Limit: 200}
	}
	q := req.URL.Query()
	limit := 200
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	var since time.Time
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			since = parsed
		}
	}
	return grpcserver.RuntimeLogQuery{
		TenantID:        tenantID,
		AgentID:         q.Get("agent_id"),
		StackID:         q.Get("stack_id"),
		JobID:           q.Get("job_id"),
		LeaseID:         q.Get("lease_id"),
		Provider:        q.Get("provider"),
		ServiceName:     firstNonEmptyRouteString(q.Get("service_name"), q.Get("service")),
		ServerID:        q.Get("server_id"),
		ServiceID:       q.Get("service_id"),
		RuntimeTargetID: q.Get("runtime_target_id"),
		Cursor:          q.Get("cursor"),
		MinLevel:        firstNonEmptyRouteString(q.Get("min_level"), q.Get("level")),
		Since:           since,
		Limit:           limit,
	}
}

func firstNonEmptyRouteString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// generateCommandID creates a unique command ID using crypto/rand.
// Returns error if crypto/rand fails (indicates serious system issue).
func generateCommandID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure indicates serious system issue (e.g., no entropy).
		return "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	return "cmd-" + hex.EncodeToString(b), nil
}
