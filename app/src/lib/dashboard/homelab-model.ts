/**
 * Pure view model for the homelab dashboards. Both presets (Complete and
 * Strata) read the same facts through these helpers, so a Node, its apps and
 * its federation bundle mean the same thing whichever layout draws them.
 */
import type { CanonicalServer } from "#lib/api/registry.js";
import type { CanonicalService } from "#lib/api/services.js";
import type { StackMetricValue } from "#lib/api/stacks.js";
import type { ServerStatusAxes } from "@kombiverselabs/ui/server";
import {
  canonicalServerAxes,
  canonicalServerFor,
  serverAxesFromStates,
  serverCardHostname,
  serverPrimaryAddress,
  serverStackKitName,
  statusLabel,
  type DashboardServer,
} from "#lib/server-card-adapter.js";

import { tr, stateLabel } from "#lib/i18n.svelte.js";
/**
 * Platform components every StackKit brings along. They are real services,
 * but not what an owner opens day to day, so the dashboards leave them to the
 * server's own service list instead of spending an app tile on them. Pocket ID
 * is not one of them: owners open it to manage their passkeys.
 */
const SYSTEM_SERVICE_KEYS = new Set([
  "traefik",
  "tinyauth",
  "crowdsec",
  "dozzle",
  "whoami",
  "step-ca",
  "lldap",
  "unbound",
  "socket-proxy",
  "docker-socket-proxy",
  "kombify-point",
  "kombify-agent",
  "guard",
  "node-hub",
  "hub",
  "base",
]);

function token(value: string | undefined | null): string {
  return (value || "")
    .trim()
    .toLowerCase()
    .replace(/[\s_]+/g, "-");
}

/** Catalog categories that never make an application on their own. */
const NON_APP_CATEGORIES = new Set(["infra", "database", "unknown"]);

export function isSystemService(service: CanonicalService): boolean {
  if (token(service.application_key) === "system") return true;
  if (token(service.catalog?.category) === "infra") return true;
  return [service.application_key, service.service_key, service.name].some(
    (candidate) => SYSTEM_SERVICE_KEYS.has(token(candidate)),
  );
}

/** Tokens of the workloads the owner selected in the StackSpec. */
export function primaryWorkloadKeys(
  deployments: ReadonlyArray<{
    desired_workloads?: Array<{ id: string; alternative?: string }>;
  }>,
): Set<string> {
  const keys = new Set<string>();
  for (const deployment of deployments) {
    for (const workload of deployment.desired_workloads ?? []) {
      if (workload.id) keys.add(token(workload.id));
      if (workload.alternative) keys.add(token(workload.alternative));
    }
  }
  return keys;
}

function isPrimary(service: CanonicalService, primary: Set<string>): boolean {
  return [service.application_key, service.service_key, service.name].some(
    (candidate) => primary.has(token(candidate)),
  );
}

export interface NodeApps {
  /**
   * One entry per user application with a UI: the component that speaks for
   * it (the one to open), the selected use cases' primary services first.
   */
  apps: CanonicalService[];
  /** Everything else on the Node: platform, databases, units without a UI. */
  system: CanonicalService[];
  /** Every component of an application, keyed by its `apps` entry's id. */
  members: Record<string, CanonicalService[]>;
}

/** The web address a component can be opened at, if one is known. */
function componentOpenUrl(service: CanonicalService): string | undefined {
  const address = (service.internal_address || "").trim();
  return (
    serviceAccessUrl(service) ??
    (/^https?:\/\/[^\s]+$/.test(address) ? address : undefined)
  );
}

/** A recognized application whose catalog entry is not plumbing or storage. */
function isCatalogApp(service: CanonicalService): boolean {
  const catalog = service.catalog;
  return Boolean(
    catalog &&
    catalog.id !== "custom" &&
    !NON_APP_CATEGORIES.has(token(catalog.category)),
  );
}

/**
 * Whether an owner opens this component in a browser: it has a web address,
 * or the catalog knows it as an application with a web UI.
 */
export function hasUserInterface(service: CanonicalService): boolean {
  return Boolean(componentOpenUrl(service)) || isCatalogApp(service);
}

/** The component that speaks for an application on its tile. */
function representative(members: CanonicalService[]): CanonicalService {
  return (
    members.find((member) => serviceAccessUrl(member)) ??
    members.find(isCatalogApp) ??
    members.find((member) => componentOpenUrl(member)) ??
    members[0]
  );
}

/** Where an application opens: its representative first, then any member. */
export function applicationOpenUrl(
  members: readonly CanonicalService[],
): string | undefined {
  for (const member of members) {
    const url = serviceAccessUrl(member);
    if (url) return url;
  }
  for (const member of members) {
    const url = componentOpenUrl(member);
    if (url) return url;
  }
  return undefined;
}

/**
 * The services placed on one Node, folded into applications. A service
 * belongs to a Node only through its recorded `server_id`; nothing is placed
 * by name or guess. Components of one application (a web UI and its backend)
 * share the application key and become one entry; only applications with a
 * UI are apps, the rest stays with the server's own service list.
 */
export function nodeApps(
  services: readonly CanonicalService[],
  serverIds: ReadonlySet<string>,
  primary: ReadonlySet<string>,
): NodeApps {
  const placed = services.filter(
    (service) => service.server_id && serverIds.has(service.server_id),
  );
  return foldApplications(placed, primary);
}

/**
 * Services folded into applications: everything a user opens through a UI
 * (Immich, Hermes, a kombify tool running on the server). Components of one
 * application share its key and become one entry; platform services, databases
 * and units without a UI stay services.
 */
export function foldApplications(
  placed: readonly CanonicalService[],
  primary: ReadonlySet<string> = new Set(),
): NodeApps {
  const groups = new Map<string, CanonicalService[]>();
  const system: CanonicalService[] = [];
  for (const service of placed) {
    if (isSystemService(service)) {
      system.push(service);
      continue;
    }
    const key = token(service.application_key) || `service:${service.id}`;
    const group = groups.get(key);
    if (group) group.push(service);
    else groups.set(key, [service]);
  }
  const apps: CanonicalService[] = [];
  const members: Record<string, CanonicalService[]> = {};
  for (const group of groups.values()) {
    if (!group.some(hasUserInterface)) {
      system.push(...group);
      continue;
    }
    const lead = representative(group);
    apps.push(lead);
    members[lead.id] = group;
  }
  apps.sort(
    (left, right) =>
      Number(isPrimary(right, primary as Set<string>)) -
        Number(isPrimary(left, primary as Set<string>)) ||
      serviceDisplayName(left).localeCompare(serviceDisplayName(right)),
  );
  return { apps, system, members };
}

/** An app is stacked when any of its components is governed by a StackKit. */
export function isStackedApp(members: readonly CanonicalService[]): boolean {
  return members.some((member) => member.management_state === "managed");
}

/**
 * Once the StackKit on a Node is rolled out cleanly (its manifest is observed
 * and it governs at least one app), the Node shows only the apps it deployed;
 * the unstacked ones it found stay with the server's own service list. Until
 * then every found app shows, so Techstack also works on servers without a
 * StackKit.
 */
export function stackedAppsOnly(
  found: NodeApps,
  manifestObserved: boolean,
): NodeApps {
  const stacked = (app: CanonicalService) =>
    isStackedApp(found.members[app.id] ?? [app]);
  if (!manifestObserved || !found.apps.some(stacked)) return found;
  const hidden = found.apps.filter((app) => !stacked(app));
  return {
    apps: found.apps.filter(stacked),
    system: [
      ...found.system,
      ...hidden.flatMap((app) => found.members[app.id] ?? [app]),
    ],
    members: found.members,
  };
}

export function serviceDisplayName(service: CanonicalService): string {
  return (
    service.application_display_name?.trim() ||
    service.name?.trim() ||
    service.service_key
  );
}

export type ServiceTone = "ok" | "warn" | "error" | "off";

/** Canonical placeholders the control plane writes when nothing was measured. */
const NO_EVIDENCE_STATES = new Set(["", "unknown", "not-required"]);

/**
 * The tile status of one service. Health wins when it carries evidence; the
 * control plane defaults health to `unknown` when no probe ran (a container
 * without HEALTHCHECK, a host unit), so that value falls through to the
 * observed runtime state. With neither measured the label stays empty: the
 * tile shows nothing instead of "unknown".
 */
export function serviceState(service: CanonicalService): {
  label: string;
  tone: ServiceTone;
} {
  const reported = [service.health?.state, service.observed_state]
    .map((value) => (value || "").trim().toLowerCase())
    .find((value) => !NO_EVIDENCE_STATES.has(value));
  const state = reported ?? "";
  if (["healthy", "running", "reachable"].includes(state)) {
    return { label: stateLabel("running"), tone: "ok" };
  }
  if (["error", "failed", "unhealthy", "degraded"].includes(state)) {
    return { label: statusLabel(state), tone: "error" };
  }
  if (["pending", "starting", "adopting", "deploying"].includes(state)) {
    return { label: statusLabel(state), tone: "warn" };
  }
  if (["stopped", "exited", "offline"].includes(state)) {
    return { label: stateLabel("stopped"), tone: "off" };
  }
  return { label: state ? statusLabel(state) : "", tone: "off" };
}

/**
 * The tile status of an application: a failing component speaks for the
 * whole application, otherwise its representative does.
 */
export function applicationState(
  lead: CanonicalService,
  members: readonly CanonicalService[] = [lead],
): { label: string; tone: ServiceTone } {
  for (const member of members) {
    const state = serviceState(member);
    if (state.tone === "error") return state;
  }
  return serviceState(lead);
}

export function serviceAccessUrl(
  service: CanonicalService,
): string | undefined {
  const url = service.access?.url;
  return typeof url === "string" && /^https?:\/\//.test(url) ? url : undefined;
}

/** A 0-100 reading, or null when the agent reported nothing usable. */
export function meterPercent(
  metric: StackMetricValue | undefined,
): number | null {
  if (!metric || metric.status !== "ok") return null;
  const value = metric.value;
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  return Math.max(0, Math.min(100, value));
}

export function meterTone(value: number | null): ServiceTone {
  if (value === null) return "off";
  if (value >= 90) return "error";
  if (value >= 75) return "warn";
  return "ok";
}

const PLACEMENT_LABELS: Record<string, string> = {
  get self_owned_device() {
    return tr("ui.homelabModel.ownDevice");
  },
  external_vps: "VPS",
  get managed_vps() {
    return tr("ui.homelabModel.managedVPS");
  },
};

/** Where a Node runs, as the Local/Cloud class its application cards show. */
export function offeringPlacement(
  offering: string | undefined,
): "local" | "cloud" | undefined {
  const key = (offering || "").trim();
  if (key === "self_owned_device") return "local";
  if (key === "external_vps" || key === "managed_vps") return "cloud";
  return undefined;
}

export function placementLabel(offering: string | undefined): string {
  const key = (offering || "").trim();
  if (!key) return "";
  return PLACEMENT_LABELS[key] ?? statusLabel(key);
}

export function nodeRoleLabel(
  server: DashboardServer,
  canonical: CanonicalServer | undefined,
): string {
  const role = (canonical?.node_role || server.role || "").trim();
  if (role === "substrate")
    return tr("ui.managedCreationFlow.proxmoxHypervisor");
  if (!role) return tr("ui.services.node");
  const label = statusLabel(role);
  return label.charAt(0).toUpperCase() + label.slice(1);
}

export interface NodeStatusSummary {
  /** One compact line, e.g. "Healthy · Connected" or "Provisioning · Pending". */
  label: string;
  /** The worst axis decides the dot. */
  tone: ServiceTone;
  /** All three axes spelled out for the tooltip and screen readers. */
  detail: string;
}

const capitalize = (value: string) => {
  const label = stateLabel(value);
  return label.charAt(0).toUpperCase() + label.slice(1);
};

/**
 * One status element for a Node that still carries all three axes
 * (SERVER-CONNECTION-STANDARD §5): health and connection are always named,
 * lifecycle only when it is not the steady `active`, and an unmeasured health
 * stays out of the short label. `detail` always lists every axis.
 */
export function nodeStatusSummary(axes: ServerStatusAxes): NodeStatusSummary {
  const parts = [
    axes.lifecycle === "active" ? "" : capitalize(axes.lifecycle),
    axes.health === "unknown" ? "" : capitalize(axes.health),
    capitalize(axes.connection),
  ].filter(Boolean);
  let tone: ServiceTone = "ok";
  if (
    axes.lifecycle === "failed" ||
    ["offline", "revoked"].includes(axes.connection) ||
    axes.health === "unhealthy"
  ) {
    tone = "error";
  } else if (
    axes.lifecycle !== "active" ||
    axes.connection !== "connected" ||
    axes.health === "degraded"
  ) {
    tone = axes.lifecycle === "decommissioned" ? "off" : "warn";
  }
  return {
    label: parts.join(" · "),
    tone,
    detail: tr("ui.homelabModel.lifecycleConnectionHealth", {
      lifecycle: capitalize(axes.lifecycle),
      connection: capitalize(axes.connection),
      health: capitalize(axes.health),
    }),
  };
}

export interface NodeView {
  key: string;
  deploymentId: string;
  /** Live telemetry; absent for a Node only the canonical registry knows. */
  server?: DashboardServer;
  canonical: CanonicalServer | undefined;
  /** The canonical id where one exists; the id metrics and services use. */
  nodeId: string;
  name: string;
  /** Lifecycle, connection and health; summarized by nodeStatusSummary. */
  axes: ServerStatusAxes;
  role: string;
  kit: string;
  placement: string;
  address: string;
  cpu: number | null;
  ram: number | null;
  disk: number | null;
  apps: CanonicalService[];
  system: CanonicalService[];
  /** Every component of each app, keyed by the app entry's id. */
  members: Record<string, CanonicalService[]>;
  /** When the Node joined the homelab; empty when no source reports it. */
  addedAt: string;
}

/**
 * Nodes in the order they joined: a new Node lands at the end of the list
 * instead of pushing the running ones down. Nodes without a join time keep
 * their order after the dated ones.
 */
export function oldestFirst<T extends { addedAt: string }>(
  nodes: readonly T[],
): T[] {
  const time = (node: T) => {
    const value = Date.parse(node.addedAt);
    return Number.isNaN(value) ? Number.POSITIVE_INFINITY : value;
  };
  return [...nodes].sort((a, b) => time(a) - time(b));
}

function canonicalRole(canonical: CanonicalServer): string {
  const role = (canonical.node_role || "").trim();
  if (role === "substrate")
    return tr("ui.managedCreationFlow.proxmoxHypervisor");
  if (!role) return tr("ui.services.node");
  const label = statusLabel(role);
  return label.charAt(0).toUpperCase() + label.slice(1);
}

export function buildNodeViews(
  servers: readonly DashboardServer[],
  canonicalOnly: readonly CanonicalServer[],
  canonicalServers: readonly CanonicalServer[],
  services: readonly CanonicalService[],
  primary: ReadonlySet<string>,
): NodeView[] {
  const live = servers.map((server): NodeView => {
    const canonical = canonicalServerFor(server, [...canonicalServers]);
    const ids = new Set(
      [canonical?.id, server.server_id, server.id, server.agent_id].filter(
        (value): value is string => Boolean(value),
      ),
    );
    const { apps, system, members } = stackedAppsOnly(
      nodeApps(services, ids, primary),
      server.capabilities?.stackkit_manifest_observed === true,
    );
    const kit = serverStackKitName(server);
    return {
      key: `${server.kit_deployment_id}:${server.id}`,
      deploymentId: server.kit_deployment_id,
      server,
      canonical,
      nodeId: canonical?.id || server.server_id || server.id,
      name: serverCardHostname(server, canonical),
      axes: canonical
        ? canonicalServerAxes(canonical)
        : serverAxesFromStates(
            server.capabilities?.lifecycle_state,
            server.capabilities?.connection_state,
            server.capabilities?.health_state || server.health?.state,
          ),
      role: nodeRoleLabel(server, canonical),
      kit:
        canonical?.node_role === "substrate" || kit === "not reported"
          ? ""
          : kit,
      placement: placementLabel(canonical?.offering),
      address: serverPrimaryAddress(server),
      cpu: meterPercent(server.health?.cpu_percent),
      ram: meterPercent(server.health?.memory_percent),
      disk: meterPercent(server.health?.disk_percent),
      apps,
      system,
      members,
      addedAt: canonical?.created_at || server.approved_at || "",
    };
  });
  const registryOnly = canonicalOnly.map((canonical): NodeView => {
    const { apps, system, members } = nodeApps(
      services,
      new Set([canonical.id, canonical.worker_id || ""].filter(Boolean)),
      primary,
    );
    return {
      key: `canonical:${canonical.id}`,
      deploymentId: canonical.kit_deployment_id || "",
      canonical,
      nodeId: canonical.id,
      name: canonical.name,
      axes: canonicalServerAxes(canonical),
      role: canonicalRole(canonical),
      kit: "",
      placement: placementLabel(canonical.offering),
      address: "not reported",
      cpu: null,
      ram: null,
      disk: null,
      apps,
      system,
      members,
      addedAt: canonical.created_at,
    };
  });
  return oldestFirst([...live, ...registryOnly]);
}

export interface NodeGroup<T> {
  key: string;
  /** True when one deployment spans two or more Nodes (Modern Homelab). */
  federated: boolean;
  deploymentId: string;
  label: string;
  nodes: T[];
}

/**
 * Keep the Nodes of one federated deployment together. A deployment with a
 * single Node is not a federation and gets no bundle. Order follows the
 * first appearance of each deployment, so the list does not jump around.
 */
export function groupNodesByDeployment<T extends { deploymentId: string }>(
  nodes: readonly T[],
  deploymentName: (id: string) => string,
): NodeGroup<T>[] {
  const byDeployment = new Map<string, T[]>();
  for (const node of nodes) {
    const id = node.deploymentId;
    const list = byDeployment.get(id);
    if (list) list.push(node);
    else byDeployment.set(id, [node]);
  }
  return [...byDeployment.entries()].map(([deploymentId, list]) => ({
    key: deploymentId,
    federated: list.length >= 2,
    deploymentId,
    label: deploymentName(deploymentId),
    nodes: list,
  }));
}
