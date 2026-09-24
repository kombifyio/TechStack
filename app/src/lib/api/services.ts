import { fetchApi } from "./client";
import { parseManagementState, type RegistryManagementState } from "./registry";

export type ManagedServiceAction =
  "start" | "stop" | "restart" | "logs" | "freeze" | "unfreeze";

/**
 * Guardrail actions are applied synchronously by the control plane: no job is
 * enqueued and no agent is contacted, so they carry no inventory revision and
 * their response has no job id.
 */
export const SERVICE_LOCK_ACTIONS: ReadonlySet<ManagedServiceAction> = new Set([
  "freeze",
  "unfreeze",
]);

/**
 * Canonical service read model (`GET /api/v1/services`,
 * `GET /api/v1/services/{id}`), served from the persisted service aggregate
 * (migration 073/074, `ApplyServiceEvent`).
 */
export interface CanonicalServiceHealth {
  state: string;
  observed_at?: string;
  reason_code?: string;
}

export interface CanonicalPlacementFreshness {
  state: "recorded" | "unknown" | string;
  age_seconds?: number;
}

export interface CanonicalServicePlacement {
  provider_id?: string;
  managed_target_ref?: string;
  provider_receipt_ref?: string;
  sla_policy_ref?: string;
  backup_policy_ref?: string;
  evidence_ref?: string;
  observed_at?: string;
  freshness: CanonicalPlacementFreshness;
}

export interface CanonicalServiceMutationLock {
  state: "unlocked" | "locked" | string;
  reason_code?: string;
  actor?: string;
  changed_at?: string;
}

export interface CanonicalService {
  id: string;
  kit_deployment_id: string;
  server_id?: string;
  target_kind: "server" | "managed_workload" | "unknown" | string;
  placement: CanonicalServicePlacement;
  service_key: string;
  service_instance: string;
  name: string;
  application_key?: string;
  application_display_name?: string;
  role?: string;
  lifecycle?: string;
  operational_impact?: string;
  internal_address?: string;
  runtime_identity?: Record<string, string>;
  /** Persisted ownership dimension; always present on this route. */
  management_state: RegistryManagementState;
  /**
   * The owner guardrail. Orthogonal to the measured dimensions: a locked
   * service keeps running and keeps reporting it — only `allowed_actions`
   * narrows. Optional so a backend that predates the field reads as unlocked.
   */
  mutation_lock?: CanonicalServiceMutationLock;
  desired_state: string;
  observed_state: string;
  health: CanonicalServiceHealth;
  stackkit_version?: string;
  access: Record<string, unknown>;
  allowed_actions: string[];
  inventory_revision: number;
  evidence_ref?: string;
  source: string;
  provenance: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

type CanonicalServiceWire = Omit<
  CanonicalService,
  "kit_deployment_id" | "management_state"
> & {
  kit_deployment_id?: string;
  management_state: unknown;
};

function requireKitDeploymentId(
  kitDeploymentId: string | undefined,
  context: string,
): string {
  const value = kitDeploymentId?.trim();
  if (!value) throw new Error(`${context}: missing kit deployment identity`);
  return value;
}

function normalizeCanonicalService(
  service: CanonicalServiceWire,
  context: string,
): CanonicalService {
  const { kit_deployment_id, ...result } = service;
  return {
    ...result,
    kit_deployment_id: requireKitDeploymentId(kit_deployment_id, context),
    management_state: parseManagementState(service.management_state, context),
  };
}

export interface ServiceApplicationAccess {
  kind: "public" | "internal" | "unavailable" | string;
  address?: string;
  open_url?: string;
  reason?: string;
}

export interface ServiceApplication {
  application_id: string;
  kit_deployment_id: string;
  server_id: string;
  application_key: string;
  display_name: string;
  status: string;
  access: ServiceApplicationAccess;
  components: CanonicalService[];
  observed_at?: string;
  system: boolean;
  provenance: Record<string, unknown>;
}

type ServiceApplicationWire = Omit<
  ServiceApplication,
  "kit_deployment_id" | "components"
> & {
  kit_deployment_id?: string;
  components: CanonicalServiceWire[];
};

function normalizeServiceApplication(
  application: ServiceApplicationWire,
  context: string,
): ServiceApplication {
  const { kit_deployment_id, components, ...result } = application;
  return {
    ...result,
    kit_deployment_id: requireKitDeploymentId(kit_deployment_id, context),
    components: components.map((component, index) =>
      normalizeCanonicalService(component, `${context}.components[${index}]`),
    ),
  };
}

export async function listServiceApplications(
  kitDeploymentId?: string,
): Promise<ServiceApplication[]> {
  const query = kitDeploymentId
    ? `?kit_deployment_id=${encodeURIComponent(kitDeploymentId)}`
    : "";
  const response = await fetchApi<ServiceApplicationWire[]>(
    `/api/v1/service-applications${query}`,
  );
  return (response.data ?? []).map((application, index) =>
    normalizeServiceApplication(application, `service-applications[${index}]`),
  );
}

export async function getServiceApplication(
  applicationId: string,
): Promise<ServiceApplication> {
  const response = await fetchApi<ServiceApplicationWire>(
    `/api/v1/service-applications/${encodeURIComponent(applicationId)}`,
  );
  return normalizeServiceApplication(
    response.data,
    `service-applications/${applicationId}`,
  );
}

/** List the canonical service aggregates for the caller's tenant. */
export async function listCanonicalServices(
  kitDeploymentId?: string,
): Promise<CanonicalService[]> {
  const query = kitDeploymentId
    ? `?kit_deployment_id=${encodeURIComponent(kitDeploymentId)}`
    : "";
  const response = await fetchApi<CanonicalServiceWire[]>(
    `/api/v1/services${query}`,
  );
  return (response.data ?? []).map((service, index) =>
    normalizeCanonicalService(service, `services[${index}]`),
  );
}

/** Read one canonical service aggregate. */
export async function getCanonicalService(
  serviceId: string,
): Promise<CanonicalService> {
  const response = await fetchApi<CanonicalServiceWire>(
    `/api/v1/services/${encodeURIComponent(serviceId)}`,
  );
  return normalizeCanonicalService(response.data, `services/${serviceId}`);
}

export interface ManagedServiceActionResponse {
  job_id: string;
  service_id: string;
  action: ManagedServiceAction;
  status: string;
}

export interface ManagedServiceLogEntry {
  timestamp: string;
  message: string;
}

export interface ManagedServiceLogsResponse {
  service_id: string;
  job_id?: string;
  status: "queued" | "running" | "completed" | "empty" | string;
  entries: ManagedServiceLogEntry[];
  next_cursor?: string;
}

export interface ManagedServiceLogOptions {
  /** The service API contract deliberately bounds a single page to 200 rows. */
  limit?: number;
  cursor?: string;
}

const defaultServiceLogLimit = 100;
const maxServiceLogLimit = 200;

function normalizeServiceLogOptions(
  options: ManagedServiceLogOptions = {},
): Required<ManagedServiceLogOptions> {
  const limit = options.limit ?? defaultServiceLogLimit;
  if (!Number.isInteger(limit) || limit < 1 || limit > maxServiceLogLimit) {
    throw new RangeError(
      `Service log limit must be an integer between 1 and ${maxServiceLogLimit}`,
    );
  }
  return { limit, cursor: options.cursor?.trim() ?? "" };
}

export async function runManagedServiceAction(
  serviceId: string,
  action: ManagedServiceAction,
  expectedInventoryRevision: number,
  idempotencyKey: string,
  logOptions?: ManagedServiceLogOptions,
): Promise<ManagedServiceActionResponse> {
  const normalizedLogOptions =
    action === "logs" ? normalizeServiceLogOptions(logOptions) : undefined;
  // The lock is not bound to a runtime observation - sending a revision would
  // claim a guarantee the endpoint does not make, and it rejects one.
  const body: Record<string, unknown> = SERVICE_LOCK_ACTIONS.has(action)
    ? { action, owner_approved: true }
    : {
        action,
        expected_inventory_revision: expectedInventoryRevision,
        owner_approved: true,
      };
  if (normalizedLogOptions) {
    body.limit = normalizedLogOptions.limit;
    if (normalizedLogOptions.cursor) body.cursor = normalizedLogOptions.cursor;
  }
  const response = await fetchApi<ManagedServiceActionResponse>(
    `/api/v1/registry/services/${encodeURIComponent(serviceId)}/actions`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify(body),
    },
  );
  return response.data;
}

/**
 * Read the redacted, bounded log page produced by a completed `logs` action.
 * The backend owns log collection and redaction; the client only follows its
 * opaque cursor and never requests an unbounded transcript.
 */
export async function getManagedServiceLogs(
  serviceId: string,
  options?: ManagedServiceLogOptions,
): Promise<ManagedServiceLogsResponse> {
  const { limit, cursor } = normalizeServiceLogOptions(options);
  const query = new URLSearchParams({ limit: String(limit) });
  if (cursor) query.set("cursor", cursor);
  const response = await fetchApi<ManagedServiceLogsResponse>(
    `/api/v1/registry/services/${encodeURIComponent(serviceId)}/logs?${query.toString()}`,
  );
  return response.data;
}
