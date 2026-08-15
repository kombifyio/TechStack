import { fetchApi } from "./client";

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
  | "unknown"
  | "healthy"
  | "degraded"
  | "unhealthy"
  | string;

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
  id: string;
  techstack_id?: string;
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
  mutations_allowed: boolean;
  created_at: string;
  updated_at: string;
}

export type LegacyServerStateName =
  | "provisioned"
  | "healthy"
  | "degraded"
  | "stale"
  | "offline";

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

function withCheckedManagementState<T extends { management_state: unknown }>(
  service: T,
  context: string,
): T & { management_state: RegistryManagementState } {
  return {
    ...service,
    management_state: parseManagementState(service.management_state, context),
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

export interface RegistryStack {
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
  stack_id: string;
  name: string;
  hostname?: string;
  role: string;
  role_label: string;
  worker_id?: string;
  lease_id?: string;
  status?:
    | "provisioned"
    | "healthy"
    | "degraded"
    | "stale"
    | "offline"
    | string;
  health_state?:
    | "provisioned"
    | "healthy"
    | "degraded"
    | "stale"
    | "offline"
    | string;
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
    | "starting"
    | "healthy"
    | "reachable"
    | "unhealthy"
    | "unknown"
    | string;
  observed_at?: string;
  management_state: RegistryManagementState;
  migration_status?: string;
  placement_scope?: "stack";
  move_allowed?: boolean;
  move_blocked_reason?: string;
  stack_id: string;
  stack_name: string;
  server_id: string;
  server_name: string;
  port?: number;
  url?: string;
}

export interface ServiceRegistryPayload {
  catalog: RegistryCatalogService[];
  stacks: RegistryStack[];
  servers: RegistryServer[];
  services: RegistryService[];
  migration_available?: boolean;
  migration_unavailable_reason?: string;
}

export interface RegistryServiceMutationRequest {
  stack_id: string;
  server_id: string;
  service_id?: string;
  name?: string;
  display_name?: string;
  type?: string;
  port?: number;
  url?: string;
}

/** List the canonical server aggregates for the caller's tenant. */
export async function listCanonicalServers(
  techstackId?: string,
): Promise<CanonicalServer[]> {
  const query = techstackId
    ? `?techstack_id=${encodeURIComponent(techstackId)}`
    : "";
  const res = await fetchApi<CanonicalServer[]>(`/api/v1/servers${query}`);
  return res.data ?? [];
}

/** Current dashboard inventory excludes terminal aggregates but keeps their direct audit route. */
export function isCurrentCanonicalServer(server: CanonicalServer): boolean {
  return server.lifecycle.state.trim().toLowerCase() !== "decommissioned";
}

/** Read one canonical server aggregate. */
export async function getCanonicalServer(
  serverId: string,
): Promise<CanonicalServer> {
  const res = await fetchApi<CanonicalServer>(
    `/api/v1/servers/${encodeURIComponent(serverId)}`,
  );
  return res.data;
}

export async function listServiceRegistry(): Promise<ServiceRegistryPayload> {
  const res = await fetchApi<ServiceRegistryPayload>(
    "/api/v1/registry/services",
  );
  return {
    ...res.data,
    services: (res.data.services ?? []).map((service, index) =>
      withCheckedManagementState(service, `services[${index}]`),
    ),
  };
}

export async function attachCatalogService(
  request: RegistryServiceMutationRequest,
): Promise<RegistryService> {
  const res = await fetchApi<{ service: RegistryService }>(
    "/api/v1/registry/services/attach",
    {
      method: "POST",
      body: JSON.stringify(request),
    },
  );
  return withCheckedManagementState(res.data.service, "attach.service");
}

export async function importObservedService(
  request: RegistryServiceMutationRequest,
): Promise<RegistryService> {
  const res = await fetchApi<{ service: RegistryService }>(
    "/api/v1/registry/services/import",
    {
      method: "POST",
      body: JSON.stringify(request),
    },
  );
  return withCheckedManagementState(res.data.service, "import.service");
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
    source_service: RegistryService;
    target_service: RegistryService;
  }>("/api/v1/registry/services/migrate", {
    method: "POST",
    body: JSON.stringify({
      service_id: serviceId,
      target_server_id: targetServerId,
    }),
  });
  return {
    ...res.data,
    source_service: withCheckedManagementState(
      res.data.source_service,
      "migrate.source_service",
    ),
    target_service: withCheckedManagementState(
      res.data.target_service,
      "migrate.target_service",
    ),
  };
}

export async function verifyRegistryService(
  serviceId: string,
): Promise<{ service: RegistryService; archived_service?: RegistryService }> {
  const res = await fetchApi<{
    service: RegistryService;
    archived_service?: RegistryService;
  }>("/api/v1/registry/services/verify", {
    method: "POST",
    body: JSON.stringify({ service_id: serviceId }),
  });
  return {
    ...res.data,
    service: withCheckedManagementState(res.data.service, "verify.service"),
    archived_service: res.data.archived_service
      ? withCheckedManagementState(
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
