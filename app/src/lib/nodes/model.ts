/**
 * Nodes page model: every server of the homelab in one list, whatever
 * StackKit deployment it belongs to. The canonical registry is the authority
 * for identity, placement and the three status axes; the worker registry adds
 * the hardware it reported at enrollment. A worker the registry does not know
 * yet (waiting for approval or enrollment) is still a Node and is listed.
 */
import type { ServerStatusAxes } from "@kombiverselabs/ui/server";
import type { CanonicalServer } from "#lib/api/registry.js";
import type { Worker } from "#lib/api/workers.js";
import {
  canonicalServerAxes,
  serverAxesFromStates,
} from "#lib/server-card-adapter.js";

export type NodePlacement = "local" | "cloud" | "unknown";

export interface ServerNode {
  key: string;
  /** Canonical id when the registry knows the Node; it opens the detail. */
  serverId?: string;
  name: string;
  placement: NodePlacement;
  /** "substrate" for a hypervisor, otherwise the registry's role. */
  role: string;
  axes: ServerStatusAxes;
  deploymentId: string;
  os: string;
  cpuCores?: number;
  ramMb?: number;
  diskGb?: number;
  gpu?: string;
  address: string;
  lastSeen?: string;
  addedAt: string;
  /** Known to the worker registry only: not enrolled or approved yet. */
  pending: boolean;
}

function placementOf(server: CanonicalServer | undefined): NodePlacement {
  const offering = (server?.offering || "").trim();
  if (offering === "self_owned_device") return "local";
  if (offering === "external_vps" || offering === "managed_vps") return "cloud";
  const env = (server?.environment_class || "").trim();
  return env === "local" || env === "cloud" ? env : "unknown";
}

function workerAxes(worker: Worker): ServerStatusAxes {
  const status = (worker.status || "").trim().toLowerCase();
  const connection =
    status === "online" || status === "connected"
      ? "connected"
      : status === "offline"
        ? "offline"
        : "pending";
  return serverAxesFromStates(
    worker.approved ? "enrolling" : "planned",
    connection,
    undefined,
  );
}

function osLabel(worker: Worker | undefined): string {
  if (!worker) return "";
  return [worker.os, worker.arch].filter(Boolean).join("/");
}

/** Join both registries; decommissioned Nodes are history, not inventory. */
export function buildServerNodes(
  servers: readonly CanonicalServer[],
  workers: readonly Worker[],
): ServerNode[] {
  const workerById = new Map(workers.map((worker) => [worker.id, worker]));
  const claimed = new Set<string>();
  const nodes: ServerNode[] = [];
  for (const server of servers) {
    if (server.lifecycle.state.trim().toLowerCase() === "decommissioned")
      continue;
    const worker = server.worker_id
      ? workerById.get(server.worker_id)
      : undefined;
    if (worker) claimed.add(worker.id);
    nodes.push({
      key: `server:${server.id}`,
      serverId: server.id,
      name: server.name || worker?.hostname || server.node_id,
      placement: placementOf(server),
      role: (server.node_role || "").trim(),
      axes: canonicalServerAxes(server),
      deploymentId: server.kit_deployment_id || worker?.kit_deployment_id || "",
      os: osLabel(worker),
      cpuCores: worker?.cpu_cores || undefined,
      ramMb: worker?.ram_mb || undefined,
      diskGb: worker?.disk_gb || undefined,
      gpu: worker?.gpu || undefined,
      address: worker?.ip || "",
      lastSeen:
        server.connection?.last_heartbeat_at || worker?.last_seen || undefined,
      addedAt: server.created_at || worker?.created || "",
      pending: false,
    });
  }
  for (const worker of workers) {
    if (claimed.has(worker.id)) continue;
    nodes.push({
      key: `worker:${worker.id}`,
      name: worker.hostname || worker.id,
      placement: "unknown",
      role: "",
      axes: workerAxes(worker),
      deploymentId: worker.kit_deployment_id || "",
      os: osLabel(worker),
      cpuCores: worker.cpu_cores || undefined,
      ramMb: worker.ram_mb || undefined,
      diskGb: worker.disk_gb || undefined,
      gpu: worker.gpu || undefined,
      address: worker.ip || "",
      lastSeen: worker.last_seen || undefined,
      addedAt: worker.created || "",
      pending: true,
    });
  }
  const time = (node: ServerNode) => {
    const value = Date.parse(node.addedAt);
    return Number.isNaN(value) ? Number.POSITIVE_INFINITY : value;
  };
  return nodes.sort((a, b) => time(a) - time(b));
}

export const PLACEMENT_ORDER: NodePlacement[] = ["local", "cloud", "unknown"];

export function nodesByPlacement(
  nodes: readonly ServerNode[],
): { placement: NodePlacement; nodes: ServerNode[] }[] {
  return PLACEMENT_ORDER.map((placement) => ({
    placement,
    nodes: nodes.filter((node) => node.placement === placement),
  })).filter((group) => group.nodes.length > 0);
}

/** Human capacity, e.g. "4 cores · 16 GB RAM · 512 GB disk". */
export function nodeCapacity(
  node: ServerNode,
  ram: string,
  disk: string,
  cores: string,
): string[] {
  return [
    node.cpuCores ? `${node.cpuCores} ${cores}` : "",
    node.ramMb ? `${Math.round(node.ramMb / 1024)} GB ${ram}` : "",
    node.diskGb ? `${Math.round(node.diskGb)} GB ${disk}` : "",
  ].filter(Boolean);
}
