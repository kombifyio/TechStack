//nolint:goconst
package routes

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

func (h workerRouteHandlers) heartbeat(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	if id == "" {
		return httpx.BadRequest(e, "worker id is required", nil)
	}
	if bearerToken(e.Request) != "" {
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
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist server heartbeat", nil)
	}
	acceptedSamples := 0
	if h.metricWriter != nil {
		samples := workerHeartbeatSamplesFromStore(*updated, req, now)
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

func workerHeartbeatSamplesFromStore(worker controlplane.Worker, req workerHeartbeatRequest, now time.Time) []monitoring.MetricSample {
	labels := map[string]string{
		"agent_id":  worker.ID,
		"worker_id": worker.ID,
		"hostname":  worker.Hostname,
		"provider":  worker.Provider,
		"source":    "worker-heartbeat",
	}
	samples := []monitoring.MetricSample{}
	if validPercent(req.CPUPercent) {
		samples = append(samples, monitoring.MetricSample{Name: "node_cpu_usage_percent", Value: req.CPUPercent, Labels: labels, Timestamp: now})
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
