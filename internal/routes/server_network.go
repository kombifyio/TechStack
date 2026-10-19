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

const (
	networkReasonUnavailable = "server_network_unavailable"
	networkReasonNotReady    = "server_network_not_ready"
	// networkRebindReuseWindow is how long a queued or running rebind answers a
	// repeated request instead of queueing another one.
	networkRebindReuseWindow = 15 * time.Minute
)

// ServerNetworkRouteConfig enables the owner's home network move routes.
type ServerNetworkRouteConfig struct {
	// Rebinder queues `stackkit network rebind` for the server's stack.
	Rebinder NetworkRebinder
	// Workers reads the Guard facts and records the rebind attempt.
	Workers controlplane.WorkerStore
	// Jobs reads the rebind job so the owner sees its live state.
	Jobs controlplane.JobStore
	// Entitlements gates the rebind; nil is the fail-closed hosted gate.
	Entitlements EntitlementGate
}

type serverNetworkResponse struct {
	ServerID    string         `json:"server_id"`
	Current     map[string]any `json:"current"`
	NetworkMove map[string]any `json:"network_move,omitempty"`
	Rebind      map[string]any `json:"rebind,omitempty"`
	CanRebind   bool           `json:"can_rebind"`
	Connection  string         `json:"connection_state"`
	ObservedAt  *time.Time     `json:"observed_at,omitempty"`
	Guidance    []string       `json:"guidance,omitempty"`
}

// networkRead serves the server's current network facts, the recorded home
// network move and the state of its rebind.
func (h serverRuntimeHandlers) networkRead(e *httpx.Event) error {
	server, worker, err := h.networkServer(e, "techstack.servers.network.read", false)
	if err != nil || server == nil {
		return err
	}
	host := previousHostFacts(worker)
	current := map[string]any{}
	for key, value := range networkPoint(host) {
		current[key] = value
	}
	if network, ok := host[workerNetworkKey].(map[string]any); ok {
		for _, key := range []string{"interface", "wireless", "fingerprint"} {
			if value, present := network[key]; present {
				current[key] = value
			}
		}
	}
	response := serverNetworkResponse{ServerID: server.ID, Current: current, Connection: server.ConnectionState}
	if worker != nil {
		response.ObservedAt = worker.LastSeenAt
		if move, ok := worker.Resources[workerNetworkMoveKey].(map[string]any); ok {
			response.NetworkMove = mergeAnyMaps(move, nil)
		}
		if rebind, ok := worker.Resources[workerNetworkRebindKey].(map[string]any); ok {
			response.Rebind = h.liveRebind(e, server.TenantID, rebind)
			if response.NetworkMove != nil && response.Rebind["status"] != nil {
				response.NetworkMove["rebind_status"] = response.Rebind["status"]
			}
		}
	}
	response.CanRebind = h.network != nil && h.network.Rebinder != nil &&
		strings.TrimSpace(server.WorkerID) != "" && serverregistry.MutationsAllowed(server.ConnectionState) &&
		server.LifecycleState == string(serverregistry.LifecycleActive)
	if !serverregistry.MutationsAllowed(server.ConnectionState) {
		response.Guidance = []string{"offline_move_hint"}
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, response)
}

// liveRebind overlays the job's current state on the recorded rebind attempt.
func (h serverRuntimeHandlers) liveRebind(e *httpx.Event, tenantID string, recorded map[string]any) map[string]any {
	out := mergeAnyMaps(recorded, nil)
	if out == nil {
		return nil
	}
	jobID := stringFromAny(out["job_id"])
	if jobID == "" || h.network == nil || h.network.Jobs == nil {
		return out
	}
	job, err := h.network.Jobs.GetJob(e.Request.Context(), tenantID, jobID)
	if err != nil || job == nil {
		return out
	}
	switch strings.ToLower(strings.TrimSpace(job.State)) {
	case "running":
		out["status"] = networkRebindRunning
	case "completed", "succeeded":
		out["status"] = networkRebindSucceeded
	case "failed", "cancelled", "canceled":
		out["status"] = networkRebindFailed
	default:
		out["status"] = networkRebindQueued
	}
	return out
}

type networkRebindResponse struct {
	ServerID string `json:"server_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
}

// networkRebind re-binds the server to the network it is on now. Everything the
// CLI changes (LAN address, signed LAN DNS record, regenerated workspace) is
// re-detected on the node; the request carries no input.
func (h serverRuntimeHandlers) networkRebind(e *httpx.Event) error {
	server, worker, err := h.networkServer(e, "techstack.servers.network.rebind", true)
	if err != nil || server == nil {
		return err
	}
	if h.network == nil || h.network.Rebinder == nil || worker == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Network re-bind is unavailable", map[string]any{"reason": networkReasonUnavailable})
	}
	if !serverregistry.MutationsAllowed(server.ConnectionState) || server.LifecycleState != string(serverregistry.LifecycleActive) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The Guard is not connected", map[string]any{
			"reason":   networkReasonNotReady,
			"guidance": "If the server moved to a WiFi-only place, connect it once by cable, or stage the WiFi before the move. It re-binds as soon as it reconnects.",
		})
	}
	if strings.TrimSpace(worker.StackID) == "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server is not part of a stack yet", map[string]any{"reason": networkReasonNotReady})
	}
	if !entitlementGateOrHosted(h.network.Entitlements).SignedGrant(e.Request.Context(), ServerMaintenanceEntitlement) {
		return writeEntitlementDenial(e, "techstack.servers.network.rebind", ServerMaintenanceEntitlement)
	}
	if existing := h.liveRebind(e, server.TenantID, mapOf(worker.Resources[workerNetworkRebindKey])); existing != nil {
		status, jobID := stringFromAny(existing["status"]), stringFromAny(existing["job_id"])
		requested, _ := time.Parse(time.RFC3339Nano, stringFromAny(existing["requested_at"]))
		if jobID != "" && (status == networkRebindQueued || status == networkRebindRunning) && h.now().Sub(requested) < networkRebindReuseWindow {
			return httpx.Success(e, http.StatusAccepted, networkRebindResponse{ServerID: server.ID, JobID: jobID, Status: status})
		}
	}
	jobID, enqueueErr := h.network.Rebinder.EnqueueStackKitLifecycle(e.Request.Context(), networkRebindRequest(server.TenantID, server.OwnerSubjectID, worker.StackID, worker.ID))
	if enqueueErr != nil {
		if errors.Is(enqueueErr, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A network re-bind is already active", nil)
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to queue the network re-bind", map[string]any{"reason": networkReasonUnavailable})
	}
	recordNetworkRebind(e.Request.Context(), h.network.Workers, worker, map[string]any{
		"trigger": networkRebindTriggerManual, "status": networkRebindQueued, "job_id": jobID,
		"requested_at": h.now().UTC().Format(time.RFC3339Nano),
	})
	return httpx.Success(e, http.StatusAccepted, networkRebindResponse{ServerID: server.ID, JobID: jobID, Status: networkRebindQueued})
}

// networkServer resolves the owner's server and its Guard worker. A mutating
// call also needs the Inventory operate grant on the server.
func (h serverRuntimeHandlers) networkServer(e *httpx.Event, capability string, mutating bool) (*controlplane.ServerRuntime, *controlplane.Worker, error) {
	if h.network == nil || h.network.Workers == nil {
		return nil, nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server network facts are unavailable", map[string]any{"reason": networkReasonUnavailable})
	}
	server, err := h.ownedServer(e, capability)
	if err != nil || server == nil {
		return nil, nil, err
	}
	if mutating {
		ownerID, _, _ := authenticatedUser(e)
		if authErr := authorizeOwnerServerOperate(e.Request.Context(), h.policy, h.store, inventoryScope{tenantID: server.TenantID, ownerID: ownerID}, server.ID); authErr != nil {
			return nil, nil, writeInventoryHTTPError(e, authErr)
		}
	}
	workerID := strings.TrimSpace(server.WorkerID)
	if workerID == "" {
		return server, nil, nil
	}
	worker, workerErr := h.network.Workers.GetWorker(e.Request.Context(), server.TenantID, workerID)
	if workerErr != nil && !errors.Is(workerErr, controlplane.ErrNotFound) {
		return nil, nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server network facts are unavailable", map[string]any{"reason": networkReasonUnavailable})
	}
	return server, worker, nil
}

func mapOf(value any) map[string]any {
	typed, _ := value.(map[string]any)
	return typed
}
