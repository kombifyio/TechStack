import { fetchApi } from "./client";
import { parseManagementState, type RegistryManagementState } from "./registry";

export type ManagedServiceAction = "start" | "stop" | "restart" | "logs";

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

export interface CanonicalService {
  id: string;
  techstack_id: string;
  server_id?: string;
  target_kind: "server" | "managed_workload" | "unknown" | string;
  placement: CanonicalServicePlacement;
  service_key: string;
  service_instance: string;
  name: string;
  /** Persisted ownership dimension; always present on this route. */
  management_state: RegistryManagementState;
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

/** List the canonical service aggregates for the caller's tenant. */
export async function listCanonicalServices(
  techstackId?: string,
): Promise<CanonicalService[]> {
  const query = techstackId
    ? `?techstack_id=${encodeURIComponent(techstackId)}`
    : "";
  const response = await fetchApi<CanonicalService[]>(
    `/api/v1/services${query}`,
  );
  return (response.data ?? []).map((service, index) => ({
    ...service,
    management_state: parseManagementState(
      service.management_state,
      `services[${index}]`,
    ),
  }));
}

/** Read one canonical service aggregate. */
export async function getCanonicalService(
  serviceId: string,
): Promise<CanonicalService> {
  const response = await fetchApi<CanonicalService>(
    `/api/v1/services/${encodeURIComponent(serviceId)}`,
  );
  return {
    ...response.data,
    management_state: parseManagementState(
      response.data.management_state,
      `services/${serviceId}`,
    ),
  };
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
  const body: Record<string, unknown> = {
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
