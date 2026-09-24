//nolint:goconst
package routes

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

type stackOperationKPIs struct {
	RegisteredServers int `json:"registered_servers"`
	HealthyServers    int `json:"healthy_servers"`
	RunningServices   int `json:"running_services"`
	ActiveAlerts      int `json:"active_alerts"`
}

type stackOperationMonitoring struct {
	Status             string `json:"status"`
	QueryBackend       string `json:"queryBackend"`
	QueryBackendStatus string `json:"queryBackendStatus,omitempty"`
	IngestBackend      string `json:"ingestBackend"`
	IngestStatus       string `json:"ingestStatus,omitempty"`
	OTLPStatus         string `json:"otlpStatus,omitempty"`
	CollectorMode      string `json:"collectorMode"`
	CompatibilityMode  string `json:"compatibilityMode"`
	SeriesCount        uint64 `json:"seriesCount,omitempty"`
	UnscopedAlerts     int    `json:"unscopedAlerts,omitempty"`
	AlertRuleCount     int    `json:"alertRuleCount,omitempty"`
	QueryProof         string `json:"queryProof,omitempty"`
	RangeProof         string `json:"rangeProof,omitempty"`
	Message            string `json:"message,omitempty"`
}

type stackOperationAlertState struct {
	Name     string            `json:"name"`
	Severity string            `json:"severity"`
	Message  string            `json:"message"`
	Value    float64           `json:"value"`
	Status   string            `json:"status"`
	Labels   map[string]string `json:"labels,omitempty"`
}

func buildStackKPIs(servers []stackOperationServer, services []stackOperationService, alerts []stackOperationAlertState) stackOperationKPIs {
	kpis := stackOperationKPIs{
		RegisteredServers: len(servers),
		ActiveAlerts:      len(alerts),
	}
	for _, server := range servers {
		if server.Assignment == "stack" && server.Health.State == "healthy" {
			kpis.HealthyServers++
		}
	}
	for _, service := range services {
		if service.Status == "healthy" || service.Status == "running" {
			kpis.RunningServices++
		}
	}
	return kpis
}

func (h stackOperationsRouteHandlers) operationAlerts(stackID string, servers []stackOperationServer) ([]stackOperationAlertState, int) {
	if h.alerts == nil {
		return nil, 0
	}
	return stackScopedAlertsFromStates(h.alerts.ActiveAlerts(), stackID, servers)
}

func stackScopedAlertsFromStates(active []monitoring.AlertState, stackID string, servers []stackOperationServer) ([]stackOperationAlertState, int) {
	serverScope := stackAlertServerScope(servers)
	alerts := make([]stackOperationAlertState, 0, len(active))
	unscoped := 0
	for _, alert := range active {
		belongs, scoped := alertBelongsToStack(alert.Rule.Labels, stackID, serverScope)
		if !belongs {
			if !scoped {
				unscoped++
			}
			continue
		}
		alerts = append(alerts, stackOperationAlertState{
			Name:     alert.Rule.Name,
			Severity: alert.Rule.Severity,
			Message:  alert.Rule.Message,
			Value:    alert.Value,
			Status:   "firing",
			Labels:   cloneStringMap(alert.Rule.Labels),
		})
	}
	return alerts, unscoped
}

type stackAlertScope struct {
	AgentIDs  map[string]bool
	ServerIDs map[string]bool
	Hosts     map[string]bool
}

func stackAlertServerScope(servers []stackOperationServer) stackAlertScope {
	scope := stackAlertScope{
		AgentIDs:  map[string]bool{},
		ServerIDs: map[string]bool{},
		Hosts:     map[string]bool{},
	}
	for _, server := range servers {
		if server.Assignment != "stack" {
			continue
		}
		if value := strings.TrimSpace(server.AgentID); value != "" {
			scope.AgentIDs[value] = true
		}
		if value := strings.TrimSpace(server.ID); value != "" {
			scope.ServerIDs[value] = true
		}
		if value := strings.TrimSpace(server.Hostname); value != "" {
			scope.Hosts[strings.ToLower(value)] = true
		}
	}
	return scope
}

func alertBelongsToStack(labels map[string]string, stackID string, scope stackAlertScope) (bool, bool) {
	if len(labels) == 0 {
		return false, false
	}
	if labelStackID := strings.TrimSpace(labels["stack_id"]); labelStackID != "" {
		return labelStackID == stackID, true
	}
	for _, key := range []string{"agent_id", "agent"} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return scope.AgentIDs[value], true
		}
	}
	for _, key := range []string{"worker_id", "server_id", "node_id"} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return scope.ServerIDs[value], true
		}
	}
	for _, key := range []string{"host", "hostname", "instance"} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return scope.Hosts[strings.ToLower(value)], true
		}
	}
	return false, false
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (h stackOperationsRouteHandlers) monitoringSummary(ctx context.Context) stackOperationMonitoring {
	health := evaluateMonitoringHealth(ctx, h.backend, h.metadata, h.ingestHealth)
	summary := stackOperationMonitoring{
		Status:             "unknown",
		QueryBackend:       defaultMonitoringStatusValue(h.metadata.QueryBackend, "embedded-tsdb"),
		QueryBackendStatus: health.queryStatus,
		IngestBackend:      defaultMonitoringStatusValue(h.metadata.IngestBackend, "embedded-tsdb"),
		IngestStatus:       health.ingestStatus,
		OTLPStatus:         health.otlp.Status,
		CollectorMode:      normalizeMonitoringCollectorMode(h.metadata.CollectorMode),
		CompatibilityMode:  defaultMonitoringStatusValue(h.metadata.CompatibilityMode, "dual-ingest"),
		QueryProof:         "vector:pending",
		RangeProof:         "matrix:pending",
		Message:            "metrics unavailable",
	}
	if h.alerts != nil {
		summary.AlertRuleCount = len(h.alerts.AllStates())
	}
	if health.queryStatus == "unknown" {
		return summary
	}
	if health.queryStatus == "error" {
		summary.Status = "degraded"
		summary.Message = health.queryError
		return summary
	}
	summary.Status = "ok"
	summary.Message = "metrics backend reachable"
	if health.stats != nil {
		summary.SeriesCount = health.stats.NumSeries
		if health.stats.NumSeries > 0 {
			summary.QueryProof = "vector:non-empty"
			summary.RangeProof = "matrix:non-empty"
		}
	}
	return summary
}

// serverLogLimit is the number of newest activity entries the server details
// view returns for the selected server.
const serverLogLimit = 20

func (h stackOperationsRouteHandlers) serverLogs(ctx context.Context, tenantID, stackID string, server stackOperationServer) []map[string]any {
	limit := serverLogLimit
	seen := make(map[string]struct{})
	events := make([]controlplane.ActivityEvent, 0, limit)
	for _, scopeKey := range serverActivityScopeKeys(server) {
		scoped, err := h.activityStore.ListActivityScoped(ctx, tenantID, controlplane.ActivityFilter{
			StackID:        stackID,
			ServerScopeKey: scopeKey,
			Limit:          limit,
		})
		if err != nil {
			return nil
		}
		for _, event := range scoped {
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			events = append(events, event)
		}
	}
	// Each scoped read already returns its own newest `limit` rows, so the
	// newest `limit` of their union is the newest `limit` overall.
	sort.Slice(events, func(i, j int) bool {
		if !events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].CreatedAt.After(events[j].CreatedAt)
		}
		return events[i].ID > events[j].ID
	})
	if len(events) > limit {
		events = events[:limit]
	}

	logs := make([]map[string]any, 0, len(events))
	for _, event := range events {
		logs = append(logs, map[string]any{
			"id":       event.ID,
			"action":   event.Action,
			"details":  event.Message,
			"status":   event.Severity,
			"created":  event.CreatedAt.UTC().Format(time.RFC3339Nano),
			"metadata": event.Details,
		})
	}
	return logs
}

// serverActivityScopeKeys lists every identity this server is addressed by.
// A managed runtime server has a synthetic `lease:<id>` server id, so its
// lease id is a distinct identity rather than a duplicate of the first.
// Hostname is not an identity: it is not unique and survives a rename.
func serverActivityScopeKeys(server stackOperationServer) []string {
	keys := make([]string, 0, 3)
	for _, candidate := range []string{server.ID, server.AgentID, server.LeaseID} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if slices.Contains(keys, candidate) {
			continue
		}
		keys = append(keys, candidate)
	}
	return keys
}
