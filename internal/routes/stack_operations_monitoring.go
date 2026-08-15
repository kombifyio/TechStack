//nolint:goconst
package routes

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/pocketbase/pocketbase/core"
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

func (h stackOperationsRouteHandlers) serverLogs(stackID string, server stackOperationServer) []map[string]any {
	records, err := h.app.FindRecordsByFilter(
		"activity_log",
		"stack_id = {:stackId}",
		"-created",
		20,
		0,
		map[string]any{"stackId": stackID},
	)
	if err != nil {
		return nil
	}
	logs := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if !activityLogMatchesServer(record, server) {
			continue
		}
		logs = append(logs, map[string]any{
			"id":       record.Id,
			"action":   record.GetString("action"),
			"details":  record.GetString("details"),
			"status":   record.GetString("status"),
			"created":  record.GetDateTime("created").String(),
			"metadata": record.Get("metadata"),
		})
	}
	return logs
}

func activityLogMatchesServer(record *core.Record, server stackOperationServer) bool {
	metadata, ok := mapFromJSONAny(record.Get("metadata"))
	if !ok || len(metadata) == 0 {
		return false
	}
	if server.Source == managedRuntimeInventorySource && activityLogMatchesManagedRuntime(metadata, server) {
		return true
	}
	for _, key := range []string{"worker_id", "server_id", "node_id"} {
		if value := activityLogMetadataValue(metadata, key); value != "" {
			return value == server.ID
		}
	}
	for _, key := range []string{"agent_id", "agent"} {
		if value := activityLogMetadataValue(metadata, key); value != "" {
			return value == server.AgentID
		}
	}
	for _, key := range []string{"host", "hostname"} {
		if value := activityLogMetadataValue(metadata, key); value != "" {
			return strings.EqualFold(value, server.Hostname)
		}
	}
	return false
}

func activityLogMatchesManagedRuntime(metadata map[string]any, server stackOperationServer) bool {
	for _, key := range []string{"lease_id", "runtime_lease_id"} {
		if value := activityLogMetadataValue(metadata, key); value != "" {
			return value == server.LeaseID
		}
	}
	return false
}

func activityLogMetadataValue(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
