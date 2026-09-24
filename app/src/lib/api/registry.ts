import { fetchApi } from "./client";
import {
  normalizeServerOutcome,
  type ServerOutcome,
} from "#lib/support/server-outcome.js";

/**
 * Canonical server read model (`GET /api/v1/servers`, `GET /api/v1/servers/{id}`).
 *
 * These routes return the PERSISTED aggregate head written by the registry
 * sweeper. Nothing here recomputes freshness or health at read time — the
 * client must never re-derive a state the backend did not persist
 * (kombify-Techstack-nzy1.4 / #577).
 */
export type CanonicalLifecycleState =
  | "planned"
  | "provisioning"
  | "enrolling"
  | "active"
  | "failed"
  | "decommissioning"
  | "decommissioned"
  | string;

export type CanonicalConnectionState =
  | "pending"
  | "connecting"
  | "connected"
  | "degraded"
  | "stale"
  | "offline"
  | "revoked"
  | string;

export type CanonicalHealthState =
  "unknown" | "healthy" | "degraded" | "unhealthy" | string;

export interface CanonicalServerChannel {
  type: string;
  role: string;
  state: string;
  endpoint_ref?: string;
  observed_at?: string;
  metadata?: Record<string, unknown>;
}

export interface CanonicalServerLifecycle {
  state: CanonicalLifecycleState;
  desired_state: string;
  ended_at?: string;
}

export interface CanonicalServerConnection {
  state: CanonicalConnectionState;
  reason_code?: string;
  changed_at: string;
  last_heartbeat_at?: string;
  staleness_seconds?: number;
}

export interface CanonicalServerHealth {
  state: CanonicalHealthState;
  observed_at?: string;
}

export interface CanonicalServerProvider {
  lease_id?: string;
  ref?: string;
  id?: string;
  target_ref?: string;
}

export interface CanonicalRuntimeTargetEvidence {
  ref?: string;
  observed_at?: string;
  freshness: {
    state: "recorded" | "unknown" | string;
    age_seconds?: number;
  };
}

export interface CanonicalServer {
  node_role?: string;
  id: string;
  node_id: string;
  kit_deployment_id?: string;
  name: string;
  worker_id?: string;
  lifecycle: CanonicalServerLifecycle;
  connection: CanonicalServerConnection;
  health: CanonicalServerHealth;
  channels: CanonicalServerChannel[];
  inventory_revision: number;
  provider: CanonicalServerProvider;
  environment_class?: "local" | "cloud" | "unknown" | string;
  offering?: "self_owned_device" | "external_vps" | "managed_vps" | string;
  provider_id?: string;
  provider_target_ref?: string;
  availability_owner?: "customer" | "provider" | string;
  operations_owner?: "customer" | "kombify" | string;
  target_evidence?: CanonicalRuntimeTargetEvidence;
  last_outcome?: ServerOutcome;
  mutations_allowed: boolean;
  /**
   * Node-scoped capability contract: exactly what this server's own state
   * admits right now, each id backed by an endpoint that exists. Absent on a
   * backend that predates the capability field — treat that as "no grant"
   * rather than as "everything allowed".
   */
  allowed_actions?: string[];
  /** StackKit-deployment scope, kept separate from the node scope. */
  stack_actions?: string[];
  created_at: string;
  updated_at: string;
}

type CanonicalServerWire = Omit<CanonicalServer, "last_outcome"> & {
  last_outcome?: unknown;
};

function normalizeCanonicalServer(
  server: CanonicalServerWire,
): CanonicalServer {
  return {
    ...server,
    last_outcome: normalizeServerOutcome(server.last_outcome) ?? undefined,
  };
}

export type LegacyServerStateName =
  "provisioned" | "healthy" | "degraded" | "stale" | "offline";

/**
 * Client mirror of `pkg/serverregistry.LegacyServerState`.
 *
 * The canonical read model publishes connection and health as two orthogonal
 * dimensions. Views that still speak the single-valued legacy vocabulary
 * collapse them HERE, from persisted state, instead of asking a legacy route to
 * do it. The mapping is copied verbatim from the Go doc comment and pinned by
 * `registry.test.ts`; changing one side without the other is a contract break.
 */
export function legacyServerState(
  connection: string | undefined,
  health: string | undefined,
): LegacyServerStateName {
  switch ((connection ?? "").trim().toLowerCase()) {
    case "connected":
      switch ((health ?? "").trim().toLowerCase()) {
        case "degraded":
        case "unhealthy":
          return "degraded";
        default:
          return "healthy";
      }
    case "degraded":
      return "degraded";
    case "stale":
      return "stale";
    case "offline":
    case "revoked":
      return "offline";
    default:
      return "provisioned";
  }
}

/**
 * Client mirror of `pkg/serverregistry.LegacyRolloutReady`. Deliberately
 * stricter than `mutations_allowed`, which also permits a degraded connection.
 */
export function serverRolloutReady(server: CanonicalServer): boolean {
  return (
    legacyServerState(server.connection.state, server.health.state) ===
      "healthy" && server.lifecycle.state.trim() === "active"
  );
}

export type RegistryServiceStatus =
  | "pending"
  | "starting"
  | "healthy"
  | "reachable"
  | "unhealthy"
  | "running"
  | "stopped"
  | "error"
  | "observed"
  | "adopting"
  | "migrating"
  | "deploying"
  | "archived"
  | "pending_verification"
  | "unknown";

export type RegistryManagementState = "managed" | "observed";

export const registryManagementStates: readonly RegistryManagementState[] = [
  "managed",
  "observed",
];

/**
 * Raised when a service projection arrives without a usable
 * `management_state`.
 *
 * `management_state` is a REQUIRED field of the canonical service read model
 * (`serviceregistry.ManagementState`, persisted since migration 073/074 and
 * emitted non-omitempty by every service response). Treating a missing or
 * unrecognized value as "not managed" would silently strip Move, Adopt, and
 * every managed-only control from a genuinely managed application, and the UI
 * would look correct while lying. The client fails loudly instead.
 */
export class ManagementStateContractError extends Error {
  readonly received: unknown;
  readonly context: string;

  constructor(context: string, received: unknown) {
    super(
      `Service ${context} is missing a usable management_state (received ${JSON.stringify(received) ?? "undefined"}). ` +
        "The canonical read model always reports 'managed' or 'observed'; " +
        "refusing to guess an ownership state.",
    );
    this.name = "ManagementStateContractError";
    this.context = context;
    this.received = received;
  }
}

/**
 * Parse a `management_state` value from an API response. Never falls back to a
 * default — an absent or unknown value is a contract violation, not an
 * "observed" service.
 */
export function parseManagementState(
  value: unknown,
  context: string,
): RegistryManagementState {
  if (typeof value !== "string") {
    throw new ManagementStateContractError(context, value);
  }
  const normalized = value.trim().toLowerCase();
  const match = registryManagementStates.find(
    (candidate) => candidate === normalized,
  );
  if (!match) {
    throw new ManagementStateContractError(context, value);
  }
  return match;
}

type RegistryServiceWire = Omit<RegistryService, "management_state"> & {
  management_state: unknown;
};

function normalizeRegistryService(
  service: RegistryServiceWire,
  context: string,
): RegistryService {
  const { management_state, ...rest } = service;
  return {
    ...rest,
    management_state: parseManagementState(management_state, context),
  };
}

export interface RegistryCatalogService {
  id: string;
  display_name: string;
  type: string;
  description: string;
  required?: boolean;
  recommended?: boolean;
  foundations: string[];
}

export interface RegistryKitDeployment {
  id: string;
  name: string;
  status: string;
  stackkit_foundation: string;
  server_mode?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  provider_id?: string;
  /** Historical non-authoritative provider label. */
  lease_provider?: string;
  ionos_datacenter?: string;
  provider_region?: string;
  server_provisioning_mode?: string;
}

export interface RegistryServer {
  id: string;
  kit_deployment_id: string;
  name: string;
  hostname?: string;
  role: string;
  role_label: string;
  worker_id?: string;
  lease_id?: string;
  status?:
    "provisioned" | "healthy" | "degraded" | "stale" | "offline" | string;
  health_state?:
    "provisioned" | "healthy" | "degraded" | "stale" | "offline" | string;
  last_seen?: string;
  rollout_ready: boolean;
}

export interface RegistryService {
  id?: string;
  name: string;
  display_name: string;
  application_key?: string;
  application_name?: string;
  type: string;
  status: RegistryServiceStatus;
  health_state?:
    "starting" | "healthy" | "reachable" | "unhealthy" | "unknown" | string;
  observed_at?: string;
  management_state: RegistryManagementState;
  migration_status?: string;
  placement_scope?: "stack";
  move_allowed?: boolean;
  move_blocked_reason?: string;
  kit_deployment_id: string;
  stack_name: string;
  server_id: string;
  server_name: string;
  port?: number;
  url?: string;
}

export interface ServiceRegistryPayload {
  catalog: RegistryCatalogService[];
  kit_deployments: RegistryKitDeployment[];
  servers: RegistryServer[];
  services: RegistryService[];
  migration_available?: boolean;
  migration_unavailable_reason?: string;
}

export interface RegistryServiceMutationRequest {
  kit_deployment_id: string;
  server_id: string;
  service_id?: string;
  name?: string;
  display_name?: string;
  type?: string;
  port?: number;
  url?: string;
}

type ServiceRegistryWirePayload = Omit<ServiceRegistryPayload, "services"> & {
  services: RegistryServiceWire[];
};

function registryServiceMutationWire(
  request: RegistryServiceMutationRequest,
): Omit<RegistryServiceMutationRequest, "kit_deployment_id"> & {
  stack_id: string;
} {
  const { kit_deployment_id, ...rest } = request;
  return { stack_id: kit_deployment_id, ...rest };
}

/** List canonical Nodes, optionally scoped to one StackKit deployment. */
export async function listCanonicalServers(
  kitDeploymentId?: string,
): Promise<CanonicalServer[]> {
  const query = kitDeploymentId
    ? `?kit_deployment_id=${encodeURIComponent(kitDeploymentId)}`
    : "";
  const res = await fetchApi<CanonicalServerWire[]>(`/api/v1/servers${query}`);
  return (res.data ?? []).map(normalizeCanonicalServer);
}

/** Current dashboard inventory excludes terminal aggregates but keeps their direct audit route. */
export function isCurrentCanonicalServer(server: CanonicalServer): boolean {
  return server.lifecycle.state.trim().toLowerCase() !== "decommissioned";
}

/** Read one canonical server aggregate. */
export async function getCanonicalServer(
  serverId: string,
): Promise<CanonicalServer> {
  const res = await fetchApi<CanonicalServerWire>(
    `/api/v1/servers/${encodeURIComponent(serverId)}`,
  );
  return normalizeCanonicalServer(res.data);
}

export interface SelfOwnedServerDetachReceipt {
  server_id: string;
  agent_id: string;
  revision: number;
  generation: number;
  detached_at: string;
  replay: boolean;
}

/** Revoke one exact BYO Agent and retain its terminal canonical server receipt. */
export async function detachSelfOwnedServer(
  serverId: string,
): Promise<SelfOwnedServerDetachReceipt> {
  const res = await fetchApi<SelfOwnedServerDetachReceipt>(
    `/api/v1/servers/${encodeURIComponent(serverId)}/detach`,
    {
      method: "POST",
      body: JSON.stringify({ confirm_server_id: serverId }),
    },
  );
  return res.data;
}

export async function listServiceRegistry(): Promise<ServiceRegistryPayload> {
  const res = await fetchApi<ServiceRegistryWirePayload>(
    "/api/v1/registry/services",
  );
  const { services, ...payload } = res.data;
  return {
    ...payload,
    services: (services ?? []).map((service, index) =>
      normalizeRegistryService(service, `services[${index}]`),
    ),
  };
}

export async function attachCatalogService(
  request: RegistryServiceMutationRequest,
): Promise<RegistryService> {
  const res = await fetchApi<{ service: RegistryServiceWire }>(
    "/api/v1/registry/services/attach",
    {
      method: "POST",
      body: JSON.stringify(registryServiceMutationWire(request)),
    },
  );
  return normalizeRegistryService(res.data.service, "attach.service");
}

export async function importObservedService(
  request: RegistryServiceMutationRequest,
): Promise<RegistryService> {
  const res = await fetchApi<{ service: RegistryServiceWire }>(
    "/api/v1/registry/services/import",
    {
      method: "POST",
      body: JSON.stringify(registryServiceMutationWire(request)),
    },
  );
  return normalizeRegistryService(res.data.service, "import.service");
}

export async function migrateRegistryService(
  serviceId: string,
  targetServerId: string,
): Promise<{
  job_id?: string;
  source_service: RegistryService;
  target_service: RegistryService;
}> {
  const res = await fetchApi<{
    job_id?: string;
    source_service: RegistryServiceWire;
    target_service: RegistryServiceWire;
  }>("/api/v1/registry/services/migrate", {
    method: "POST",
    body: JSON.stringify({
      service_id: serviceId,
      target_server_id: targetServerId,
    }),
  });
  return {
    ...res.data,
    source_service: normalizeRegistryService(
      res.data.source_service,
      "migrate.source_service",
    ),
    target_service: normalizeRegistryService(
      res.data.target_service,
      "migrate.target_service",
    ),
  };
}

export async function verifyRegistryService(
  serviceId: string,
): Promise<{ service: RegistryService; archived_service?: RegistryService }> {
  const res = await fetchApi<{
    service: RegistryServiceWire;
    archived_service?: RegistryServiceWire;
  }>("/api/v1/registry/services/verify", {
    method: "POST",
    body: JSON.stringify({ service_id: serviceId }),
  });
  return {
    ...res.data,
    service: normalizeRegistryService(res.data.service, "verify.service"),
    archived_service: res.data.archived_service
      ? normalizeRegistryService(
          res.data.archived_service,
          "verify.archived_service",
        )
      : undefined,
  };
}

export async function deleteRegistryService(
  serviceId: string,
): Promise<{ message: string; id: string }> {
  const res = await fetchApi<{ message: string; id: string }>(
    `/api/v1/registry/services/${serviceId}`,
    {
      method: "DELETE",
    },
  );
  return res.data;
}
