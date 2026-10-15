import { fetchApi } from "./client";
import { parseManagementState, type RegistryManagementState } from "./registry";

export type ManagedServiceAction =
  "start" | "stop" | "restart" | "logs" | "freeze" | "unfreeze" | "update" | "backup";

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

/** Catalog recognition of a service or application (additive backend field). */
export interface ServiceCatalogRef {
  id: string;
  display_name?: string;
  category?: string;
  /** Vendor domain used for the logo lookup. */
  brand_domain?: string;
  /** Default UI port inside the container. */
  web_port?: number;
  /** Our guide or article for this application, when one exists. */
  guide_url?: string;
  /** The upstream project, from the StackKits catalog (additive backend field). */
  vendor?: ServiceVendor;
  /** Official end-user clients (stores and installers), when the catalog lists any. */
  clients?: ServiceClient[];
}

export interface ServiceSupport {
  kind: "donate" | "sponsor" | "purchase";
  url: string;
}

export interface ServiceVendor {
  name?: string;
  website?: string;
  docs?: string;
  license?: string;
  support?: ServiceSupport[];
}

export interface ServiceClient {
  platform: "android" | "ios" | "windows" | "macos" | "linux";
  kind: "store" | "download";
  url: string;
}

/**
 * Measured version facts (additive backend field). `current` and `latest`
 * are never invented: `latest` exists only after a real registry or release
 * check succeeded, otherwise `reason_code` says why it is unknown.
 */
export interface ServiceVersionInfo {
  current?: string;
  source?: "image_tag" | "image_label" | "stackkit" | string;
  image?: string;
  latest?: string;
  update_available?: boolean;
  checked_at?: string;
  update_mode?: "stackkit_upgrade" | "adopt_required" | "unavailable" | string;
  reason_code?: string;
}

/** Where the backend says a service runs; `serverless` is not emitted. */
export type ServicePlacementClass = "local" | "cloud" | "managed" | "unknown";

export interface CanonicalService {
  adoption?: {
    preview_available: boolean;
    execution_available: boolean;
    release_available: boolean;
    profile_ref?: string;
    reason_code?: string;
  };
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
  catalog?: ServiceCatalogRef | null;
  version?: ServiceVersionInfo | null;
  placement_class?: ServicePlacementClass | string;
  access: Record<string, unknown>;
  allowed_actions: string[];
  inventory_revision: number;
  evidence_ref?: string;
  source: string;
  provenance: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface ApplicationAdoptionPlan {
  plan_id: string;
  service_id: string;
  status: string;
  error?: string;
  assessment?: {
    planDigest: string;
    profileRef: string;
    mode: string;
    source: { containerId: string; imageDigest: string; configMount: string };
    preservedFields: string[];
    admittedOperations: string[];
  };
}
export async function prepareApplicationAdoption(
  service: CanonicalService,
  key: string,
) {
  const response = await fetchApi<ManagedServiceActionResponse>(
    `/api/v1/services/${encodeURIComponent(service.id)}/adoption-plans`,
    {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify({
        mode: "adopt_in_place",
        expected_inventory_revision: service.inventory_revision,
      }),
    },
  );
  return response.data;
}
export async function readApplicationAdoption(
  serviceId: string,
  planId: string,
) {
  const response = await fetchApi<ApplicationAdoptionPlan>(
    `/api/v1/services/${encodeURIComponent(serviceId)}/adoption-plans/${encodeURIComponent(planId)}`,
  );
  return response.data;
}
export async function executeApplicationAdoption(
  service: CanonicalService,
  plan: ApplicationAdoptionPlan,
  key: string,
) {
  const response = await fetchApi<ManagedServiceActionResponse>(
    `/api/v1/services/${encodeURIComponent(service.id)}/adoption-plans/${encodeURIComponent(plan.plan_id)}/execute`,
    {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify({
        mode: "adopt_in_place",
        plan_digest: plan.assessment?.planDigest,
        expected_inventory_revision: service.inventory_revision,
        owner_approved: true,
      }),
    },
  );
  return response.data;
}
export async function releaseApplicationAdoption(
  service: CanonicalService,
  key: string,
) {
  const response = await fetchApi<ManagedServiceActionResponse>(
    `/api/v1/services/${encodeURIComponent(service.id)}/release`,
    {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify({
        expected_inventory_revision: service.inventory_revision,
        owner_approved: true,
      }),
    },
  );
  return response.data;
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

const PLACEMENT_CLASSES: readonly ServicePlacementClass[] = [
  "local",
  "cloud",
  "managed",
  "unknown",
];

/** A recognised placement class, or undefined so callers fall back to heuristics. */
export function parsePlacementClass(
  value: unknown,
): ServicePlacementClass | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim().toLowerCase();
  // `serverless` is a deprecated alias of managed on the Brand side.
  if (normalized === "serverless") return "managed";
  return PLACEMENT_CLASSES.find((candidate) => candidate === normalized);
}

function textField(
  record: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = record[key];
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function normalizeCatalogRef(value: unknown): ServiceCatalogRef | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  const id = textField(record, "id");
  if (!id) return undefined;
  return {
    id,
    display_name: textField(record, "display_name"),
    category: textField(record, "category"),
    brand_domain: textField(record, "brand_domain"),
    web_port:
      typeof record.web_port === "number" && record.web_port > 0
        ? record.web_port
        : undefined,
    guide_url: httpsUrl(record.guide_url),
    vendor: normalizeVendor(record.vendor),
    clients: normalizeClients(record.clients),
  };
}

function httpsUrl(value: unknown): string | undefined {
  return typeof value === "string" && value.startsWith("https://")
    ? value
    : undefined;
}

function normalizeVendor(value: unknown): ServiceVendor | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  const support = Array.isArray(record.support)
    ? record.support.flatMap((entry) => {
        const item = (entry ?? {}) as Record<string, unknown>;
        const url = httpsUrl(item.url);
        const kind = item.kind;
        return url && (kind === "donate" || kind === "sponsor" || kind === "purchase")
          ? [{ kind, url } as ServiceSupport]
          : [];
      })
    : undefined;
  const vendor: ServiceVendor = {
    name: textField(record, "name"),
    website: httpsUrl(record.website),
    docs: httpsUrl(record.docs),
    license: textField(record, "license"),
    support: support?.length ? support : undefined,
  };
  return Object.values(vendor).some(Boolean) ? vendor : undefined;
}

const CLIENT_PLATFORMS = ["android", "ios", "windows", "macos", "linux"];

function normalizeClients(value: unknown): ServiceClient[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const clients = value.flatMap((entry) => {
    const item = (entry ?? {}) as Record<string, unknown>;
    const url = httpsUrl(item.url);
    if (
      !url ||
      !CLIENT_PLATFORMS.includes(String(item.platform)) ||
      (item.kind !== "store" && item.kind !== "download")
    )
      return [];
    return [
      { platform: item.platform, kind: item.kind, url } as ServiceClient,
    ];
  });
  return clients.length ? clients : undefined;
}

function normalizeVersionInfo(value: unknown): ServiceVersionInfo | undefined {
  if (!value || typeof value !== "object") return undefined;
  const record = value as Record<string, unknown>;
  const version: ServiceVersionInfo = {
    current: textField(record, "current"),
    source: textField(record, "source"),
    image: textField(record, "image"),
    latest: textField(record, "latest"),
    update_available: record.update_available === true,
    checked_at: textField(record, "checked_at"),
    update_mode: textField(record, "update_mode"),
    reason_code: textField(record, "reason_code"),
  };
  // A version block with nothing measured carries no information.
  return version.current ||
    version.latest ||
    version.image ||
    version.reason_code
    ? version
    : undefined;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : [];
}

/**
 * Normalize one canonical service. Identity and ownership are contract
 * violations that throw; every other field is optional so a backend that
 * predates it (or omits it for one row) still renders.
 */
function normalizeCanonicalService(
  service: CanonicalServiceWire,
  context: string,
): CanonicalService {
  const { kit_deployment_id, ...result } = service;
  return {
    ...result,
    kit_deployment_id: requireKitDeploymentId(kit_deployment_id, context),
    management_state: parseManagementState(service.management_state, context),
    catalog: normalizeCatalogRef(service.catalog) ?? null,
    version: normalizeVersionInfo(service.version) ?? null,
    placement_class: parsePlacementClass(service.placement_class),
    allowed_actions: stringList(service.allowed_actions),
    access:
      service.access && typeof service.access === "object"
        ? service.access
        : {},
    health:
      service.health && typeof service.health === "object"
        ? service.health
        : { state: "" },
    placement: {
      ...service.placement,
      freshness: service.placement?.freshness ?? { state: "unknown" },
    },
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
  /** Union of the actions that apply app-wide (additive backend field). */
  allowed_actions?: string[];
  catalog?: ServiceCatalogRef | null;
  /** Version of the primary component. */
  version?: ServiceVersionInfo | null;
  placement_class?: ServicePlacementClass | string;
}

type ServiceApplicationWire = Omit<
  ServiceApplication,
  "kit_deployment_id" | "components"
> & {
  kit_deployment_id?: string;
  components?: CanonicalServiceWire[];
};

/** A row the client could not read; reported, never fatal for the list. */
export interface ServiceRowRejection {
  index: number;
  id?: string;
  reason: string;
}

export interface ServiceRowsResult<T> {
  items: T[];
  rejected: ServiceRowRejection[];
}

function rowId(value: unknown, keys: string[]): string | undefined {
  if (!value || typeof value !== "object") return undefined;
  for (const key of keys) {
    const candidate = (value as Record<string, unknown>)[key];
    if (typeof candidate === "string" && candidate) return candidate;
  }
  return undefined;
}

/**
 * Normalize a list row by row. One malformed row (unknown ownership, empty
 * deployment identity) is dropped and reported instead of blanking the list.
 */
function normalizeRows<Wire, T>(
  rows: Wire[] | null | undefined,
  idKeys: string[],
  normalize: (row: Wire, index: number) => T,
): ServiceRowsResult<T> {
  const items: T[] = [];
  const rejected: ServiceRowRejection[] = [];
  (rows ?? []).forEach((row, index) => {
    try {
      items.push(normalize(row, index));
    } catch (error) {
      rejected.push({
        index,
        id: rowId(row, idKeys),
        reason: error instanceof Error ? error.message : String(error),
      });
    }
  });
  return { items, rejected };
}

function normalizeServiceApplication(
  application: ServiceApplicationWire,
  context: string,
  rejected?: ServiceRowRejection[],
): ServiceApplication {
  const { kit_deployment_id, components, ...result } = application;
  const normalizedComponents = normalizeRows(
    components,
    ["id"],
    (component: CanonicalServiceWire, index) =>
      normalizeCanonicalService(component, `${context}.components[${index}]`),
  );
  rejected?.push(...normalizedComponents.rejected);
  return {
    ...result,
    kit_deployment_id: requireKitDeploymentId(kit_deployment_id, context),
    components: normalizedComponents.items,
    access:
      application.access && typeof application.access === "object"
        ? application.access
        : { kind: "unavailable" },
    system: application.system === true,
    provenance: application.provenance ?? {},
    allowed_actions: stringList(application.allowed_actions),
    catalog: normalizeCatalogRef(application.catalog) ?? null,
    version: normalizeVersionInfo(application.version) ?? null,
    placement_class: parsePlacementClass(application.placement_class),
  };
}

/** Applications plus the rows that could not be read. */
export async function listServiceApplicationsReport(
  kitDeploymentId?: string,
): Promise<ServiceRowsResult<ServiceApplication>> {
  const query = kitDeploymentId
    ? `?kit_deployment_id=${encodeURIComponent(kitDeploymentId)}`
    : "";
  const response = await fetchApi<ServiceApplicationWire[]>(
    `/api/v1/service-applications${query}`,
  );
  const componentRejections: ServiceRowRejection[] = [];
  const result = normalizeRows(
    response.data,
    ["application_id", "application_key"],
    (application: ServiceApplicationWire, index) =>
      normalizeServiceApplication(
        application,
        `service-applications[${index}]`,
        componentRejections,
      ),
  );
  return {
    items: result.items,
    rejected: [...result.rejected, ...componentRejections],
  };
}

export async function listServiceApplications(
  kitDeploymentId?: string,
): Promise<ServiceApplication[]> {
  return (await listServiceApplicationsReport(kitDeploymentId)).items;
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

/** Canonical services plus the rows that could not be read. */
export async function listCanonicalServicesReport(
  kitDeploymentId?: string,
): Promise<ServiceRowsResult<CanonicalService>> {
  const query = kitDeploymentId
    ? `?kit_deployment_id=${encodeURIComponent(kitDeploymentId)}`
    : "";
  const response = await fetchApi<CanonicalServiceWire[]>(
    `/api/v1/services${query}`,
  );
  return normalizeRows(
    response.data,
    ["id"],
    (service: CanonicalServiceWire, index) =>
      normalizeCanonicalService(service, `services[${index}]`),
  );
}

/** List the canonical service aggregates for the caller's tenant. */
export async function listCanonicalServices(
  kitDeploymentId?: string,
): Promise<CanonicalService[]> {
  return (await listCanonicalServicesReport(kitDeploymentId)).items;
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
