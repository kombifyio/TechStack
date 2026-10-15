package routes

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	workerNetworkKey       = "network"
	workerNetworkMoveKey   = "network_move"
	workerNetworkRebindKey = "network_rebind"

	networkRebindQueued    = "queued"
	networkRebindRunning   = "running"
	networkRebindSucceeded = "succeeded"
	networkRebindFailed    = "failed"

	networkRebindTriggerAuto   = "auto"
	networkRebindTriggerManual = "manual"
)

// workerInventoryNetwork mirrors the Guard's observed network position
// (pkg/agent/httpguard.Network). Every field is optional.
type workerInventoryNetwork struct {
	Interface   string `json:"interface,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	CIDR        string `json:"cidr,omitempty"`
	Wireless    bool   `json:"wireless,omitempty"`
	SSID        string `json:"ssid,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// normalize bounds the reported values; a value that is not what it claims to
// be (an address that does not parse, an SSID over the 802.11 limit) is dropped
// rather than stored.
func (n *workerInventoryNetwork) normalize() {
	n.Interface = boundedNetworkToken(n.Interface, 32)
	n.Gateway = normalizedIP(n.Gateway)
	n.CIDR = normalizedCIDR(n.CIDR)
	n.SSID = strings.TrimSpace(n.SSID)
	if len(n.SSID) > 32 {
		n.SSID = ""
	}
	n.Fingerprint = boundedNetworkToken(n.Fingerprint, 64)
	if !n.Wireless {
		n.SSID = ""
	}
}

func boundedNetworkToken(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func normalizedIP(value string) string {
	if ip := net.ParseIP(strings.TrimSpace(value)); ip != nil {
		return ip.String()
	}
	return ""
}

func normalizedCIDR(value string) string {
	if _, network, err := net.ParseCIDR(strings.TrimSpace(value)); err == nil {
		return network.String()
	}
	return ""
}

func networkMap(n *workerInventoryNetwork) map[string]any {
	if n == nil {
		return nil
	}
	out := map[string]any{"wireless": n.Wireless}
	for key, value := range map[string]string{
		"interface": n.Interface, "gateway": n.Gateway, "cidr": n.CIDR, "ssid": n.SSID, "fingerprint": n.Fingerprint,
	} {
		if value != "" {
			out[key] = value
		}
	}
	return out
}

// networkPoint is one side of a move: where the server was, or is.
func networkPoint(host map[string]any) map[string]any {
	network, _ := host[workerNetworkKey].(map[string]any)
	point := map[string]any{}
	for key, value := range map[string]string{
		"ip":      firstNonEmpty(stringFromAny(host["private_ip"]), stringFromAny(host["local_ip"])),
		"gateway": stringFromAny(network["gateway"]),
		"cidr":    stringFromAny(network["cidr"]),
		"ssid":    stringFromAny(network["ssid"]),
	} {
		if value != "" {
			point[key] = value
		}
	}
	return point
}

// networkIdentity is what a move is detected on: the network fingerprint, or the
// private address when the Guard reports no fingerprint.
func networkIdentity(host map[string]any) (fingerprint, privateIP string) {
	network, _ := host[workerNetworkKey].(map[string]any)
	return stringFromAny(network["fingerprint"]), stringFromAny(host["private_ip"])
}

// detectNetworkMove compares the host facts stored by the previous inventory
// with the ones that just arrived. It returns the network_move record when the
// server now sits on another network, and nil when nothing moved or when there
// is nothing to compare (first inventory, or a side that reports neither a
// fingerprint nor a private address). It is a pure function of both inventories:
// the same fingerprint twice never yields an event, which is what keeps the
// automatic rebind idempotent.
func detectNetworkMove(previousHost map[string]any, current workerInventoryHost, observedAt time.Time) map[string]any {
	if len(previousHost) == 0 {
		return nil
	}
	currentHost := inventoryHostMap(current)
	oldFingerprint, oldIP := networkIdentity(previousHost)
	newFingerprint, newIP := networkIdentity(currentHost)
	switch {
	case oldFingerprint != "" && newFingerprint != "":
		if oldFingerprint == newFingerprint {
			return nil
		}
	case oldIP != "" && newIP != "":
		if oldIP == newIP {
			return nil
		}
	default:
		return nil
	}
	return map[string]any{
		"detected_at":   observedAt.UTC().Format(time.RFC3339Nano),
		"previous":      networkPoint(previousHost),
		"current":       networkPoint(currentHost),
		"rebind_status": networkRebindQueued,
	}
}

// previousHostFacts returns the host facts the previous inventory stored.
func previousHostFacts(worker *controlplane.Worker) map[string]any {
	if worker == nil {
		return nil
	}
	host, _ := worker.Resources[workerInventoryHostKey].(map[string]any)
	return host
}

// NetworkRebinder enqueues the managed rebind of one server after a home network
// move. *orchestrator.Orchestrator satisfies it.
type NetworkRebinder interface {
	EnqueueStackKitLifecycle(context.Context, jobs.StackKitLifecycleRequest) (string, error)
}

// networkRebindRequest is the lifecycle request that re-binds a moved server.
func networkRebindRequest(tenantID, ownerID, stackID, workerID string) jobs.StackKitLifecycleRequest {
	return jobs.StackKitLifecycleRequest{
		StackID: stackID, TenantID: tenantID, OwnerID: ownerID, AgentID: workerID,
		Operation: jobs.StackKitLifecycleNetworkRebind, OwnerApproved: true,
	}
}

// queueNetworkRebind enqueues the rebind for a detected move and records the
// job on the worker, or the reason it could not be queued. It runs after the
// inventory committed and never fails the inventory: the move record is the
// durable fact, and the owner can re-bind from the server page.
func (h workerRouteHandlers) queueNetworkRebind(ctx context.Context, worker *controlplane.Worker, move map[string]any) {
	if worker == nil || move == nil {
		return
	}
	patch := map[string]any{"trigger": networkRebindTriggerAuto}
	switch {
	case h.networkRebinder == nil:
		patch["status"] = networkRebindFailed
		patch["reason"] = "rebind_unavailable"
	case strings.TrimSpace(worker.StackID) == "" || strings.TrimSpace(worker.OwnerSubjectID) == "":
		patch["status"] = networkRebindFailed
		patch["reason"] = "server_not_in_a_stack"
	default:
		jobID, err := h.networkRebinder.EnqueueStackKitLifecycle(ctx, networkRebindRequest(worker.TenantID, worker.OwnerSubjectID, worker.StackID, worker.ID))
		if err != nil {
			slog.Warn("network_rebind_enqueue_failed", "tenant_id", worker.TenantID, "worker_id", worker.ID, "error", err.Error())
			patch["status"] = networkRebindFailed
			patch["reason"] = "enqueue_failed"
		} else {
			patch["status"] = networkRebindQueued
			patch["job_id"] = jobID
		}
	}
	patch["requested_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	recordNetworkRebind(ctx, h.wst, worker, patch)
	slog.Warn("server_network_move_detected", "tenant_id", worker.TenantID, "worker_id", worker.ID, "rebind_status", patch["status"])
}

// recordNetworkRebind stores the rebind attempt next to the move record.
func recordNetworkRebind(ctx context.Context, workers controlplane.WorkerStore, worker *controlplane.Worker, rebind map[string]any) {
	if workers == nil || worker == nil {
		return
	}
	updated := *worker
	updated.Resources = mergeAnyMaps(worker.Resources, map[string]any{workerNetworkRebindKey: rebind})
	if move, ok := updated.Resources[workerNetworkMoveKey].(map[string]any); ok {
		moveCopy := mergeAnyMaps(move, map[string]any{"rebind_status": rebind["status"]})
		updated.Resources[workerNetworkMoveKey] = moveCopy
	}
	if _, err := workers.UpsertWorkerHeartbeat(ctx, updated); err != nil {
		slog.Warn("network_rebind_record_failed", "tenant_id", worker.TenantID, "worker_id", worker.ID, "error", err.Error())
	}
}
