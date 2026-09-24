import type {
  ServerActionId,
  ServerCardAction,
  ServerConnectionState,
  ServerHealthState,
  ServerKitInfo,
  ServerLifecycleState,
  ServerMetric,
  ServerStackAction,
  ServerStatusAxes,
  ServerStatusKind,
  StackActionId,
} from "@kombiverselabs/ui/server";

import type {
  StackMetricValue,
  StackOperationServer,
} from "#lib/api/stacks.js";
import type { CanonicalServer } from "#lib/api/registry.js";
import {
  isActionableOutcome,
  type ServerOutcome,
} from "#lib/support/server-outcome.js";
import { isManagedRuntimeServer } from "#lib/managed-runtime-server.js";

export type DashboardServer = StackOperationServer & {
  /** The StackKit deployment that owns this Node in the combined Home Hub. */
  kit_deployment_id: string;
};

export function statusLabel(status: string): string {
  return status.replace(/_/g, " ");
}

export function canonicalServerFor(
  telemetry: StackOperationServer,
  canonicalServers: CanonicalServer[],
): CanonicalServer | undefined {
  return canonicalServers.find(
    (server) => server.id === telemetry.id || server.id === telemetry.agent_id,
  );
}

/**
 * The canonical aggregate is the durable authority for user guidance. The
 * operations projection is retained only as a compatibility fallback while a
 * server is first appearing in canonical inventory.
 */
export function actionableServerOutcome(
  telemetry: StackOperationServer | undefined,
  canonical: CanonicalServer | undefined,
): ServerOutcome | null {
  const outcome = canonical?.last_outcome ?? telemetry?.last_outcome;
  return isActionableOutcome(outcome) ? outcome : null;
}

export function dashboardServerMeta(
  telemetry: StackOperationServer,
  canonical: CanonicalServer | undefined,
): string {
  if (canonical?.node_role === "substrate") return "Proxmox hypervisor";
  if (!canonical) return serverCardMeta(telemetry);
  return [
    statusLabel(canonical.environment_class || "unknown"),
    statusLabel(canonical.offering || "unknown_offering"),
    canonical.provider_id || canonical.provider.ref,
  ]
    .filter(Boolean)
    .join(" · ");
}

export function canonicalServerMeta(server: CanonicalServer): string {
  if (server.node_role === "substrate") return "Proxmox hypervisor";
  return [
    statusLabel(server.environment_class || "unknown"),
    statusLabel(server.offering || "unknown_offering"),
    server.provider_id || server.provider.ref,
  ]
    .filter(Boolean)
    .join(" · ");
}

export function canonicalServerStatusLabel(server: CanonicalServer): string {
  return statusLabel(
    server.connection.state === "connected"
      ? server.health.state
      : server.connection.state,
  );
}

export function formatMetric(metric: StackMetricValue | undefined): string {
  if (!metric || metric.value === undefined || metric.status !== "ok") {
    return "unknown";
  }
  // The agent reports a raw float, so a live card rendered
  // "CPU 1.2345678882817885%" next to a neighbour showing "0%". One decimal
  // is the most precision a utilisation reading carries meaning at, and it
  // keeps the three metric tiles the same width as the numbers move.
  const value =
    typeof metric.value === "number" && Number.isFinite(metric.value)
      ? Number(metric.value.toFixed(1))
      : metric.value;
  return `${value}${metric.unit || ""}`;
}

export function formatCapacity(
  value: number | undefined,
  suffix: string,
): string {
  if (!value || value <= 0) return "unknown";
  return `${Math.round(value)} ${suffix}`;
}

export function serverOSLabel(server: StackOperationServer): string {
  const os = server.os?.trim() || "os unknown";
  const version = server.os_version?.trim();
  const osWithVersion =
    version && !os.toLowerCase().includes(version.toLowerCase())
      ? `${os} ${version}`
      : os;
  return `${osWithVersion}/${server.arch?.trim() || "arch unknown"}`;
}

export function serverPrimaryAddress(server: StackOperationServer): string {
  const addresses = server.host_addresses || [];
  return (
    server.ip?.trim() ||
    addresses.find((address) => address.scope === "public")?.address ||
    addresses.find((address) => address.scope === "primary")?.address ||
    addresses.find((address) => address.scope === "private")?.address ||
    addresses[0]?.address ||
    "not reported"
  );
}

export function serverDomains(server: StackOperationServer): string[] {
  return Array.from(
    new Set(
      [
        ...(server.domains || []),
        ...(server.service_endpoints || []).map(
          (endpoint) => endpoint.domain || "",
        ),
      ]
        .map((domain) => domain.trim())
        .filter(Boolean),
    ),
  );
}

export function serverStackKitName(server: StackOperationServer): string {
  return (
    server.stackkit?.name?.trim() ||
    server.stackkit?.catalog_ref?.trim() ||
    "not reported"
  );
}

export function serverCardHostname(
  server: StackOperationServer,
  canonical?: CanonicalServer,
): string {
  return (
    canonical?.name?.trim() ||
    server.hostname?.trim() ||
    server.agent_id?.trim() ||
    server.id
  );
}

export function serverStackKitVariant(server: StackOperationServer): string {
  if (!server.stackkit) return "deployment unknown";
  const parts = [
    server.stackkit.version,
    server.stackkit.mode,
    server.stackkit.context,
    server.stackkit.paas,
    server.stackkit.compute_tier,
  ]
    .map((part) => part?.trim())
    .filter(Boolean);
  parts.push(server.stackkit.state || "state unknown");
  return parts.join(" · ");
}

export function serverCardStatus(
  server: StackOperationServer,
): ServerStatusKind {
  const state = (server.health?.state || "").toLowerCase();
  if (state === "healthy") return "healthy";
  if (state === "degraded" || state === "error" || state === "failed") {
    return "degraded";
  }
  if (state === "offline" || state === "revoked") return "offline";
  if (state === "stale") return "degraded";
  if (state === "pending") return "pending";
  return "unknown";
}

export function canonicalServerCardStatus(
  connection: string | undefined,
  health: string | undefined,
): ServerStatusKind {
  const connectionState = (connection || "").trim().toLowerCase();
  const healthState = (health || "").trim().toLowerCase();
  if (connectionState === "offline" || connectionState === "revoked") {
    return "offline";
  }
  if (
    connectionState === "stale" ||
    connectionState === "degraded" ||
    healthState === "degraded" ||
    healthState === "unhealthy" ||
    healthState === "failed" ||
    healthState === "error"
  ) {
    return "degraded";
  }
  if (connectionState === "connected" && healthState === "healthy") {
    return "healthy";
  }
  return connectionState === "pending" || connectionState === "connecting"
    ? "pending"
    : "unknown";
}

const LIFECYCLE_STATES: ReadonlySet<ServerLifecycleState> = new Set([
  "planned",
  "provisioning",
  "enrolling",
  "active",
  "failed",
  "decommissioning",
  "decommissioned",
]);
const CONNECTION_STATES: ReadonlySet<ServerConnectionState> = new Set([
  "pending",
  "connecting",
  "connected",
  "degraded",
  "stale",
  "offline",
  "revoked",
]);
const HEALTH_STATES: ReadonlySet<ServerHealthState> = new Set([
  "unknown",
  "healthy",
  "degraded",
  "unhealthy",
]);

/**
 * Map the canonical aggregate onto the card's three status axes — the axes
 * render together and never collapse (SERVER-CONNECTION-STANDARD §5). A value
 * outside the closed contract vocabulary falls back to the earliest/unknown
 * state of its axis: visibly "not yet real" instead of silently optimistic.
 */
export function canonicalServerAxes(server: CanonicalServer): ServerStatusAxes {
  const lifecycle = (server.lifecycle?.state || "").trim().toLowerCase();
  const connection = (server.connection?.state || "").trim().toLowerCase();
  const health = (server.health?.state || "").trim().toLowerCase();
  return {
    lifecycle: LIFECYCLE_STATES.has(lifecycle as ServerLifecycleState)
      ? (lifecycle as ServerLifecycleState)
      : "planned",
    connection: CONNECTION_STATES.has(connection as ServerConnectionState)
      ? (connection as ServerConnectionState)
      : "pending",
    health: HEALTH_STATES.has(health as ServerHealthState)
      ? (health as ServerHealthState)
      : "unknown",
  };
}

export function serverCardMeta(server: StackOperationServer): string {
  return [
    server.capabilities?.provider,
    server.role,
    serverOSLabel(server),
    isManagedRuntimeServer(server) ? "managed runtime" : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

export function serverCardMetrics(
  server: StackOperationServer,
): ServerMetric[] {
  return [
    { label: "CPU", value: formatMetric(server.health?.cpu_percent) },
    { label: "RAM", value: formatMetric(server.health?.memory_percent) },
    { label: "Disk", value: formatMetric(server.health?.disk_percent) },
  ];
}

export function serverCardKit(server: StackOperationServer): ServerKitInfo {
  return {
    name: serverStackKitName(server) || "not reported",
    detail: serverStackKitVariant(server),
  };
}

/**
 * Node action ids the card family renders. The backend owns which of them a
 * server currently admits (`allowed_actions`); this map only decides which of
 * those the card can actually draw, so an id from a newer backend never
 * reaches an owner as a nameless button.
 */
const NODE_ACTION_IDS: ReadonlySet<ServerActionId> = new Set([
  "connect",
  "terminal",
  "reconnect",
  "rotate_credentials",
  "ssh_enable",
  "ssh_disable",
  "detach",
  "decommission",
  "resolve_custody",
]);

const STACK_ACTION_IDS: ReadonlySet<StackActionId> = new Set([
  "plan",
  "apply",
  "verify",
  "upgrade",
  "drift_detect",
  "drift_reconcile",
]);

/** Retire actions the surface must never fire without an explicit dialog. */
export const DESTRUCTIVE_NODE_ACTIONS: ReadonlySet<ServerActionId> = new Set([
  "detach",
  "decommission",
]);

export interface ServerCardActionContext {
  /** `CanonicalServer.allowed_actions` — the node-scoped grant. */
  allowedActions: readonly string[];
  /** `CanonicalServer.stack_actions` — the StackKit-deployment grant. */
  stackActions?: readonly string[];
  /** The node action currently in flight, if any. */
  activeAction?: string;
  /** True while a post-mutation verify pass is running. */
  converging?: boolean;
  /** Runs a node action; the caller owns the owner-approval dialog. */
  onNodeAction: (action: ServerActionId) => void;
  /** Runs a StackKit operation. */
  onStackAction?: (action: StackActionId) => void;
  /** Opens the server detail sheet. */
  onDetails?: () => void;
  /** Opens the StackKit deployment view. */
  onOpenStack?: () => void;
}

/**
 * Build the node action array from the backend grant. Nothing is inferred: an
 * action the backend did not grant is not rendered at all, which is the whole
 * point of replacing the frontend lifecycle heuristics.
 */
export function serverCardActions(
  context: ServerCardActionContext,
): ServerCardAction[] {
  const busy = Boolean(context.activeAction);
  const actions: ServerCardAction[] = [];
  for (const id of context.allowedActions) {
    if (!NODE_ACTION_IDS.has(id as ServerActionId)) continue;
    const action = id as ServerActionId;
    const isActive = context.activeAction === action;
    const state = isActive
      ? context.converging
        ? "verifying"
        : "executing"
      : busy
        ? "disabled"
        : "needs_approval";
    actions.push({
      id: action,
      state,
      disabledReason:
        state === "disabled" ? "Another node action is running" : undefined,
      onSelect: () => context.onNodeAction(action),
    });
  }
  if (context.onDetails) {
    actions.push({ id: "details", onSelect: context.onDetails });
  }
  return actions;
}

/** Build the StackKit-scoped action array from the same grant model. */
export function serverStackCardActions(
  context: ServerCardActionContext,
): ServerStackAction[] {
  const actions: ServerStackAction[] = [];
  const handler = context.onStackAction;
  if (handler) {
    for (const id of context.stackActions ?? []) {
      if (!STACK_ACTION_IDS.has(id as StackActionId)) continue;
      const action = id as StackActionId;
      actions.push({ id: action, onSelect: () => handler(action) });
    }
  }
  if (context.onOpenStack) {
    actions.push({ id: "open_stack", onSelect: context.onOpenStack });
  }
  return actions;
}
