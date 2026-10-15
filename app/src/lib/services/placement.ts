import type {
  RegistryCatalogService,
  RegistryServer,
  RegistryService,
} from "#lib/api/registry.js";
import type { StackOperationServer } from "#lib/api/stacks.js";
import type {
  ServiceCardAction,
  ServiceMigration,
  ServicePlacement,
  ServiceStatusKind,
} from "@kombiverselabs/ui/service";

/** Capacity of one Node for the Placement board, from the cockpit snapshot. */
export interface ServerCapacity {
  storageFree?: number;
  storageTotal?: number;
  storagePercent?: number;
  ramUsed?: number;
  ramTotal?: number;
  ramPercent?: number;
  source: "telemetry" | "capabilities" | "unknown";
}

export function cockpitServerFor(
  cockpitServers: StackOperationServer[],
  server: RegistryServer,
): StackOperationServer | undefined {
  return cockpitServers.find(
    (candidate) =>
      candidate.id === server.id ||
      candidate.id === server.worker_id ||
      candidate.hostname === server.hostname ||
      candidate.hostname === server.name,
  );
}

export function serverCapacity(
  opsServer: StackOperationServer | undefined,
): ServerCapacity {
  if (!opsServer) return { source: "unknown" };

  const diskPercent = opsServer.health?.disk_percent?.value;
  const ramPercent = opsServer.health?.memory_percent?.value;
  const diskTotal = Number(opsServer.capabilities?.disk_gb ?? 0);
  const ramTotalGb = Number(opsServer.capabilities?.ram_mb ?? 0) / 1024;

  if (diskTotal <= 0 && ramTotalGb <= 0) return { source: "unknown" };

  return {
    storageTotal: diskTotal > 0 ? Math.round(diskTotal) : undefined,
    storageFree:
      diskTotal > 0 && typeof diskPercent === "number"
        ? Number((diskTotal * (1 - diskPercent / 100)).toFixed(1))
        : undefined,
    storagePercent:
      typeof diskPercent === "number" ? Math.round(diskPercent) : undefined,
    ramTotal: ramTotalGb > 0 ? Number(ramTotalGb.toFixed(1)) : undefined,
    ramUsed:
      ramTotalGb > 0 && typeof ramPercent === "number"
        ? Number((ramTotalGb * (ramPercent / 100)).toFixed(1))
        : undefined,
    ramPercent:
      typeof ramPercent === "number" ? Math.round(ramPercent) : undefined,
    source:
      typeof diskPercent === "number" || typeof ramPercent === "number"
        ? "telemetry"
        : "capabilities",
  };
}

export function serviceTypeLabel(
  service: RegistryCatalogService | RegistryService,
): string {
  return service.type.replace(/[-_]/g, " ");
}

export function serviceMeta(service: RegistryService): string {
  const meta = [serviceTypeLabel(service)];
  if (service.port) meta.push(`:${service.port}`);
  return meta.join(" · ");
}

/** Nodes of the same deployment, other than its own, that accept a rollout. */
export function moveTargetsFor(
  servers: RegistryServer[],
  service: RegistryService,
): RegistryServer[] {
  return servers.filter(
    (server) =>
      server.kit_deployment_id === service.kit_deployment_id &&
      server.id !== service.server_id &&
      server.rollout_ready,
  );
}

/** Statuses in which a service has no address a person can open. */
const CLOSED_STATUSES = [
  "unknown",
  "migrating",
  "deploying",
  "pending_verification",
  "archived",
  "pending",
  "adopting",
  "stopped",
  "offline",
];

export function serviceAccessEnabled(
  service: RegistryService,
  migrating: boolean,
): boolean {
  if (!service.url || migrating) return false;
  return !CLOSED_STATUSES.includes(service.status.toLowerCase());
}

/** Progress shown on a moving card, by the phase the backend reports. */
export function migrationProgress(phase: string | undefined): number {
  return phase === "pending_verification"
    ? 95
    : phase === "deploying"
      ? 75
      : 45;
}

/** One application card on the Placement board, resolved by the page. */
export interface PlacementCard {
  key: string;
  service: RegistryService;
  name: string;
  meta: string;
  placement: ServicePlacement;
  status: ServiceStatusKind;
  migration?: ServiceMigration;
  canMove: boolean;
  migrating: boolean;
  migrationMessage: string;
  actions: ServiceCardAction[];
}

/** One Node column on the Placement board. */
export interface PlacementColumn {
  server: RegistryServer;
  /** The Node is a cloud VPS rather than local hardware. */
  cloud: boolean;
  capacity: ServerCapacity;
  cards: PlacementCard[];
}
