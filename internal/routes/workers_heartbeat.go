//nolint:goconst
package routes

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func (h workerRouteHandlers) heartbeat(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if id == "" {
		return httpx.BadRequest(e, "worker id is required", nil)
	}
	if bearerToken(e.Request) != "" || workerRequestCredential(e.Request) != "" {
		return h.heartbeatWithRuntimeAgent(e, id)
	}
	return httpx.Unauthorized(e, "Runtime-agent bearer token required")
}

func (h workerRouteHandlers) heartbeatWithRuntimeAgent(e *httpx.Event, id string) error {
	var req workerHeartbeatRequest
	if decodeErr := json.NewDecoder(e.Request.Body).Decode(&req); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if convergenceErr := normalizeRuntimeConvergence(&req.RuntimeConvergence); convergenceErr != nil {
		return httpx.BadRequest(e, "Invalid runtime convergence", nil)
	}
	capabilities, capabilityErr := normalizeObservedStackKitCapabilities(req.StackKitCapabilities)
	if capabilityErr != nil {
		return httpx.BadRequest(e, "Invalid StackKit capabilities", nil)
	}
	authCtx, authenticated := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !authenticated {
		return nil
	}
	now := time.Now().UTC()
	position, positionErr := validateGuardEventPosition(now, guardEventPosition{
		Epoch: req.SourceEpoch, Sequence: req.SourceSequence, ObservedAt: req.ObservedAt,
	})
	if positionErr != nil {
		return httpx.BadRequest(e, "Invalid Guard event position", nil)
	}
	worker := controlplane.Worker{
		ID:             id,
		TenantID:       authCtx.TenantID,
		StackID:        authCtx.StackID,
		Status:         "pending",
		Approved:       false,
		LastSeenAt:     &now,
		Type:           "runtime",
		OwnerSubjectID: authCtx.OwnerID,
		Capabilities: map[string]any{
			"server_id":        authCtx.ServerID,
			"runtime_agent_id": id,
		},
	}
	if existing := authCtx.Worker; existing != nil {
		worker.InstanceID = existing.InstanceID
		worker.Hostname = existing.Hostname
		worker.IP = existing.IP
		worker.OS = existing.OS
		worker.Arch = existing.Arch
		worker.TokenHash = existing.TokenHash
		worker.Status = existing.Status
		worker.Approved = existing.Approved
		worker.ApprovedAt = existing.ApprovedAt
		worker.CPUCores = existing.CPUCores
		worker.RAMMB = existing.RAMMB
		worker.DiskGB = existing.DiskGB
		worker.Provider = existing.Provider
		worker.Tags = existing.Tags
		worker.Resources = existing.Resources
		worker.Capabilities = mergeAnyMaps(existing.Capabilities, worker.Capabilities)
		worker.Resources = mergeAnyMaps(existing.Resources, worker.Resources)
	}
	if req.RuntimeConvergence != nil {
		convergence := runtimeconvergence.Map(*req.RuntimeConvergence)
		worker.Resources = mergeAnyMaps(worker.Resources, map[string]any{"runtime_convergence": convergence})
		worker.Capabilities = mergeAnyMaps(worker.Capabilities, map[string]any{"runtime_convergence": convergence})
	}
	worker.Capabilities = mergeAnyMaps(worker.Capabilities, map[string]any{"guard_operations": guardOperationsProjection(req.GuardJournal)})
	worker.Capabilities = mergeAnyMaps(worker.Capabilities, map[string]any{"guard_posture": guardPostureProjection(req.GuardPosture)})
	// Every heartbeat restates the host maintenance facts, so a downgraded
	// agent stops advertising host maintenance on its next beat.
	worker.Capabilities = mergeAnyMaps(worker.Capabilities, req.metadata())
	// Every authenticated heartbeat replaces this observation. Absence on an
	// older or downgraded Guard removes its grant instead of retaining it.
	worker.Capabilities["stackkit_capabilities"] = capabilities
	if authCtx.IdentityBinding != "" {
		worker.Capabilities = mergeAnyMaps(worker.Capabilities, map[string]any{workerCapabilityIdentityBinding: authCtx.IdentityBinding})
	}
	if worker.Approved {
		worker.Status = "connected"
	} else if !strings.EqualFold(worker.Status, "rejected") {
		worker.Status = "pending"
	}
	if worker.Approved && worker.ApprovedAt == nil {
		worker.ApprovedAt = &now
	}
	updated, err := h.wst.UpsertWorkerHeartbeat(e.Request.Context(), worker)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to update worker heartbeat", nil)
	}
	serverID := firstNonEmpty(authCtx.ServerID, runtimeServerIDForWorker(id))
	if err := h.projectServerHeartbeat(e.Request.Context(), *updated, serverID, authCtx.LeaseID, now, "guard-heartbeat", position); err != nil {
		httpx.RecordDependencyFailure(e, "workers.heartbeat.project_server", err)
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist server heartbeat", nil)
	}
	h.observeGuardPosture(e.Request.Context(), authCtx, req)
	acceptedSamples := 0
	if h.metricWriter != nil {
		samples := workerHeartbeatSamplesFromStore(*updated, serverID, req, now)
		if len(samples) > 0 {
			if err := h.metricWriter.Write(samples); err != nil {
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to ingest worker metrics", map[string]any{
					"error": err.Error(),
				})
			}
			acceptedSamples = len(samples)
		}
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		"worker_id":        updated.ID,
		"server_id":        serverID,
		"runtime_agent_id": id,
		"last_seen":        now.UTC().Format(time.RFC3339Nano),
		"samples_accepted": acceptedSamples,
	})
}

func normalizeObservedStackKitCapabilities(values []string) ([]string, error) {
	if len(values) > 6 {
		return nil, fmt.Errorf("too many StackKit capabilities")
	}
	allowed := map[string]bool{
		"stackkit": true,
		stackkitcommand.ExpectedPlanHashCapability:        true,
		stackkitcommand.WorkspaceInstanceCapability:       true,
		stackkitcommand.RestoreActivationCapability:       true,
		stackkitcommand.OriginalRecoveryReleaseCapability: true,
		stackkitcommand.AdvancedOperationsCapability:      true,
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if !allowed[value] || seen[value] {
			return nil, fmt.Errorf("unsupported or duplicate StackKit capability")
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

// guardOperationsProjection is the control-plane view of a Guard's durable
// operation journal: "operations durable" is true only while the journal is
// writable and nothing is left unresolved. A Guard that reports nothing is
// flagged legacy and is never presented as durable.
func guardOperationsProjection(journal *guardbinding.JournalHealth) map[string]any {
	if journal == nil {
		return map[string]any{"contract": guardbinding.LegacyContractVersion, "durable": false}
	}
	health := journal.Normalize()
	out := map[string]any{
		"contract":                   health.Contract,
		"journal":                    health.State,
		"durable":                    health.State == guardbinding.JournalOK && health.UnresolvedOutcomeUnknown == 0,
		"unresolved_outcome_unknown": health.UnresolvedOutcomeUnknown,
		"unacked_results":            health.UnackedResults,
		"in_flight":                  health.InFlight,
		"journal_bytes":              health.Bytes,
		"journal_budget_bytes":       health.BudgetBytes,
	}
	if health.Reason != "" {
		out["reason"] = health.Reason
	}
	return out
}

// guardPostureProjection is the control-plane view of how a Guard is isolated. A
// Guard that reports nothing predates the privilege split and is shown as legacy
// root, never as isolated.
func guardPostureProjection(posture *guardbinding.Posture) map[string]any {
	if posture == nil {
		return map[string]any{"mode": guardbinding.PostureLegacyRoot, "reported": false}
	}
	normalized := posture.Normalize()
	out := map[string]any{"mode": normalized.Mode, "reported": true, "deny_mutations": normalized.DenyMutations}
	if normalized.UpdateState != "" {
		out["update_state"] = normalized.UpdateState
		out["update_version"] = normalized.UpdateVersion
		if normalized.UpdateReason != "" {
			out["update_reason"] = normalized.UpdateReason
		}
	}
	return out
}

// workerHeartbeatSamplesFromStore turns a Guard host report into TSDB samples.
// serverID is the canonical server the report was projected onto; the Node
// dashboards and the Node monitoring page select series by node_id, so without
// it the host history is stored but never found.
func workerHeartbeatSamplesFromStore(worker controlplane.Worker, serverID string, req workerHeartbeatRequest, now time.Time) []monitoring.MetricSample {
	labels := map[string]string{
		"agent_id":  worker.ID,
		"worker_id": worker.ID,
		"hostname":  worker.Hostname,
		"provider":  worker.Provider,
		"source":    "worker-heartbeat",
	}
	// The tenant comes from the authenticated worker record, never the agent
	// payload; SaaS monitor queries only return series carrying it.
	if tenantID := strings.TrimSpace(worker.TenantID); tenantID != "" {
		labels["tenant_id"] = tenantID
	}
	if serverID = strings.TrimSpace(serverID); serverID != "" {
		labels["server_id"] = serverID
		labels["node_id"] = serverID
	}
	samples := []monitoring.MetricSample{}
	if validPercent(req.CPUPercent) {
		// The Guard reports whole-host CPU, the agent collector's core="total".
		cpuLabels := make(map[string]string, len(labels)+1)
		for key, value := range labels {
			cpuLabels[key] = value
		}
		cpuLabels["core"] = "total"
		samples = append(samples, monitoring.MetricSample{Name: "node_cpu_usage_percent", Value: req.CPUPercent, Labels: cpuLabels, Timestamp: now})
	}
	if percent, ok := bytesPercent(req.MemoryUsedBytes, req.MemoryTotalBytes); ok {
		samples = append(samples, monitoring.MetricSample{Name: "node_memory_usage_percent", Value: percent, Labels: labels, Timestamp: now})
	}
	if percent, ok := bytesPercent(req.DiskUsedBytes, req.DiskTotalBytes); ok {
		samples = append(samples, monitoring.MetricSample{Name: "node_disk_usage_percent", Value: percent, Labels: labels, Timestamp: now})
	}
	if req.UptimeSeconds > 0 && !math.IsNaN(req.UptimeSeconds) && !math.IsInf(req.UptimeSeconds, 0) {
		samples = append(samples, monitoring.MetricSample{Name: "node_uptime_seconds", Value: req.UptimeSeconds, Labels: labels, Timestamp: now})
	}
	return samples
}

func validPercent(value float64) bool {
	return value >= 0 && value <= 100 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func bytesPercent(used, total int64) (float64, bool) {
	if total <= 0 || used < 0 || used > total {
		return 0, false
	}
	percent := (float64(used) / float64(total)) * 100
	if !validPercent(percent) {
		return 0, false
	}
	return math.Round(percent*10) / 10, true
}
