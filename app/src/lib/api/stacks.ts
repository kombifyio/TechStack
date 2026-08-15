/**
 * kombify-TechStack API - Stacks Module
 *
 * Stack management API functions including CRUD operations
 * and StackKits stack-spec import/export functionality.
 */

import { ApiRequestError, fetchApi, parseApiErrorResponse } from "./client";
import { getPocketBaseCompatStoredAuthToken } from "$lib/auth/pocketbase-compat";
import { clearCreatingSessionState } from "$lib/logout-cleanup";

function getOptionalAuthHeaders(): HeadersInit {
  const token = getPocketBaseCompatStoredAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function looksLikeHtml(body: string): boolean {
  const t = body.trimStart().toLowerCase();
  return t.startsWith("<!doctype") || t.startsWith("<html");
}

function hasDataEnvelope(payload: unknown): payload is { data: unknown } {
  return typeof payload === "object" && payload !== null && "data" in payload;
}

function unwrapApiEnvelope<T>(payload: unknown): T {
  return hasDataEnvelope(payload) ? (payload.data as T) : (payload as T);
}

/**
 * Guard for the raw-`fetch` spec endpoints (yaml import/validate/export) which
 * may be hijacked by a reverse proxy or auth redirect returning an HTML page.
 * Throws a descriptive {@link ApiRequestError} mirroring the original inline
 * checks; otherwise returns so the caller can continue parsing.
 */
function assertNotHtmlResponse(
  response: Response,
  body: string,
  action: string,
  method: string,
): void {
  const contentType = response.headers.get("content-type") || "";
  if (!looksLikeHtml(body) && !contentType.includes("text/html")) {
    return;
  }
  throw new ApiRequestError(
    `${action}: server returned HTML. Check that the backend is reachable and you are authenticated.`,
    {
      status: response.status,
      url: response.url,
      method,
      contentType,
      responseBodySnippet: body.slice(0, 2000),
    },
  );
}

/**
 * Thin wrapper over {@link fetchApi} that performs the request and returns the
 * unwrapped `data` payload. Most stack endpoints share this exact shape; the
 * helper removes the per-method `const res = await fetchApi(...)` / `return
 * res.data` boilerplate while preserving endpoint, method, body, and the
 * envelope/error semantics of {@link fetchApi}.
 */
async function apiRequest<T>(
  method: string,
  path: string,
  body?: BodyInit,
  timeoutMs?: number,
  headers?: HeadersInit,
): Promise<T> {
  const res = await fetchApi<T>(
    path,
    body === undefined
      ? { method, timeoutMs, headers }
      : { method, body, timeoutMs, headers },
  );
  return res.data;
}

/**
 * Shared implementation for the raw-`fetch` yaml spec endpoints
 * ({@link validateKombinationImport} / {@link importKombinationSpec}). POSTs the
 * raw `content` as `application/yaml`, applies the HTML guard, unwraps the data
 * envelope, and surfaces an `invalid JSON response` error on parse failure.
 * Endpoints, headers, and error messages match the original inline code.
 */
async function postYamlSpec<T>(
  action: string,
  path: string,
  content: string,
): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    credentials: "include",
    headers: {
      "Content-Type": "application/yaml",
      ...getOptionalAuthHeaders(),
    },
    body: content,
  });

  if (!response.ok) {
    throw await parseApiErrorResponse(response, { action, method: "POST" });
  }

  const text = await response.text();
  assertNotHtmlResponse(response, text, action, "POST");

  try {
    return unwrapApiEnvelope<T>(JSON.parse(text));
  } catch {
    throw new Error(`${action}: invalid JSON response`);
  }
}

// ============================================================================
// Stack Types
// ============================================================================

export interface Stack {
  id: string;
  name: string;
  provider: string;
  state: string;
  status?: string;
  services: string[];
  server_ip?: string;
  runtime_phase?: string;
  server_mode?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  provider_id?: string;
  /** Historical non-authoritative provider label. */
  lease_provider?: string;
  ionos_datacenter?: string;
  provider_region?: string;
  simulate_provider_id?: string;
  simulate_node_lifecycle?: string;
  lease_id?: string;
  desired_state?: string;
  billing_mode?: string;
  billing_cadence?: string;
  catalog_ref?: string;
  stackkit_catalog_ref?: string;
  verification_status?: string;
  server_provisioning_mode?: string;
  server_connection_mode?: string;
  server_remote_host_present?: boolean;
  server_remote_user_present?: boolean;
  server_remote_auth_method?: string;
  server_remote_credential_ref?: string;
  server_remote_use_sudo?: boolean;
  server_install_command_required?: boolean;
  /** Row served from the retired legacy store; shown with a "Legacy" label. */
  legacy?: boolean;
  /** Protected public-demo anchor stack; destroy is disabled for it. */
  demo_anchor?: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateStackOptions {
  vpn?: string | null;
  cloudflare_zero_trust?: boolean;
  public_access?: boolean;
  identity_head?: "pocket_id" | "pocketbase" | "generic_oidc";
  enable_pocket_id?: boolean;
  enable_pocketbase_backend?: boolean;
  enable_pocketbase?: boolean;
  enable_headscale?: boolean;
  enable_monitoring?: boolean;
  enable_traefik?: boolean;
  identity?: {
    toolProvider?: string;
    homelabProvider?: string;
    requiresPasskeys?: boolean;
    backendCapability?: string;
  };
  identity_provider?: string;
  tool_identity_provider?: string;
  homelab_identity_provider?: string;
  requires_passkeys?: boolean;
  identity_backend_capability?: string;
  auto_updates?: boolean;
  backups?: boolean;
  multi_user?: boolean;
  server_provisioning_mode?:
    | "kombify-cloud"
    | "connect-remote"
    | "install-command";
  server_connection_mode?:
    | "managed-subscription"
    | "remote-ssh"
    | "agent-oneliner";
  server_remote_host?: string;
  server_remote_port?: number;
  server_remote_user?: string;
  server_remote_auth_method?: "ssh-key" | "password";
  server_remote_ssh_key_label?: string;
  server_remote_use_sudo?: boolean;
  server_install_command_required?: boolean;
  server_mode?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  provider_id?: string;
  ionos_datacenter?: string;
  provider_region?: string;
  simulate_node_lifecycle?: string;
  desired_state?: string;
  billing_mode?: string;
  billing_cadence?: string;
  stackkit_catalog_ref?: string;
  verification_status?: string;
  use_cases?: string[];
}

export interface CreateStackRequest {
  name: string;
  mode?: "easy" | "techie";
  provider?: string;
  server_ip?: string;
  ssh_user?: string;
  services: string[];
  ssh_key_path?: string;
  ssh_key_content?: string; // User can paste private key directly
  stack_spec?: Record<string, unknown>;
  user_config?: Record<string, unknown>;
  user_config_format?: "json" | "yaml";
  options?: CreateStackOptions;
}

export interface CreateStackResponse {
  stack_id: string;
  job_id: string;
  name: string;
  state: string;
  message: string;
  server_id?: string;
  operations_url?: string;
  idempotent_replay?: boolean;
  registration_token?: string;
  bootstrap_token?: string;
  bootstrap_token_expires_at?: string;
  owner_spec_endpoint?: string;
  owner_spec_scopes?: string[];
}

export interface StackJobAcceptedResponse {
  success: boolean;
  message: string;
  job_id: string;
}

export type StackKitLifecycleOperation =
  | "plan"
  | "apply"
  | "verify"
  | "upgrade"
  | "drift_detect"
  | "drift_reconcile";

export interface StackKitLifecycleRequest {
  operation: StackKitLifecycleOperation;
  agent_id: string;
  target_release?: string;
  owner_approved?: boolean;
}

export interface StackKitLifecycleAcceptedResponse {
  job_id: string;
  stack_id: string;
  agent_id: string;
  operation: StackKitLifecycleOperation;
  status: string;
  message?: string;
}

export interface ResumeStackEnrollmentRequest {
  job_id: string;
  lease_id: string;
}

export interface ResumeStackEnrollmentResponse extends StackJobAcceptedResponse {
  stack_id: string;
  source_job_id: string;
  lease_id: string;
  server_id: string;
  idempotent_replay: boolean;
  provider_vm_create_requested: false;
  bootstrap_token?: string;
  bootstrap_token_expires_at?: string;
  owner_spec_endpoint?: string;
  owner_spec_scopes?: string[];
}

export interface RetryStackRolloutRequest {
  source_job_id: string;
  lease_id: string;
}

export interface RetryStackRolloutResponse extends StackJobAcceptedResponse {
  stack_id: string;
  source_job_id: string;
  lease_id: string;
  server_id: string;
  idempotent_replay: boolean;
  provider_vm_create_requested: false;
  bootstrap_token?: string;
  bootstrap_token_expires_at?: string;
  owner_spec_endpoint?: string;
  owner_spec_scopes?: string[];
}

export interface StackPruneResponse {
  message: string;
  pruned_stacks: number;
  pruned_legacy?: number;
  skipped_active: number;
  skipped_other_owner?: number;
  warnings?: string[];
}

export interface MonthlyRuntimeOffering {
  id: string;
  name: string;
  billing_cadence: string;
  image?: string;
  vcpus?: number;
  memory_mb?: number;
  disk_gb?: number;
  region?: string;
}

export interface MonthlyRuntimeStatus {
  tenant_id?: string;
  lease_id: string;
  action?: string;
  runtime_offering_id?: string;
  desired_state?: string;
  observed_state?: string;
  lease_state?: string;
  lease_reason?: string;
  enrollment_status?: string;
  ssh_enabled?: boolean;
  status?: {
    id?: string;
    state?: string;
    public_ip?: string;
    ssh_enabled?: boolean;
  } | null;
  ssh?: {
    host?: string;
    port?: number;
    user?: string;
    enabled?: boolean;
  } | null;
}

export interface MonthlyRuntimeOperation {
  tenant_id: string;
  lease_id: string;
  event_type: string;
  status: string;
  actor?: string;
  error?: string;
  created_at: string;
}

export interface MonthlyRuntimeActionOptions {
  force?: boolean;
}

export interface StackMetricValue {
  status: "ok" | "unknown" | "error" | string;
  value?: number;
  unit?: string;
}

export interface StackServerHealth {
  state: "healthy" | "stale" | "offline" | "pending" | "unknown" | string;
  source: string;
  cpu_percent: StackMetricValue;
  memory_percent: StackMetricValue;
  disk_percent: StackMetricValue;
  uptime_seconds: StackMetricValue;
  updated_at?: string;
  notes?: string[];
}

export interface StackServerAddress {
  address: string;
  scope: string;
  provenance: string;
}

export interface StackServerEndpoint {
  service_id?: string;
  service_key?: string;
  name?: string;
  url: string;
  domain?: string;
  visibility?: string;
  health?: string;
  provenance?: string;
  source: string;
  observed_at?: string;
}

export interface StackServerStackKit {
  name?: string;
  catalog_ref?: string;
  version?: string;
  mode?: string;
  context?: string;
  paas?: string;
  compute_tier?: string;
  state: string;
  sources: string[];
}

export interface StackOperationServer {
  id: string;
  hostname: string;
  role: string;
  status: string;
  assignment: "stack" | "unassigned" | string;
  techstack_id?: string;
  agent_id: string;
  ip?: string;
  host_addresses?: StackServerAddress[];
  os?: string;
  os_version?: string;
  arch?: string;
  domains?: string[];
  service_endpoints?: StackServerEndpoint[];
  stackkit?: StackServerStackKit;
  last_seen?: string;
  approved: boolean;
  approved_at?: string;
  precheck_state: string;
  source?: "worker-registry" | "managed-runtime" | string;
  lease_id?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  desired_state?: string;
  enrollment_status?: string;
  assignable?: boolean;
  capabilities: {
    cpu_cores?: number;
    ram_mb?: number;
    disk_gb?: number;
    gpu?: string;
    has_nvme?: boolean;
    has_hw_transcode?: boolean;
    docker_version?: string;
    // Agent identity and the two provenance halves of the service model. They
    // are what lets the dashboard explain an empty service list instead of
    // only reporting it.
    agent_version?: string;
    stackkit_manifest_observed?: boolean;
    service_discovery_observed?: boolean;
    provider?: string;
    tags?: string;
    tenant_id?: string;
    runtime_lane?: string;
    runtime_offering_id?: string;
    lease_id?: string;
    desired_state?: string;
    enrollment_status?: string;
    runtime_ssh_host?: string;
    runtime_ssh_user?: string;
    runtime_ssh_port?: number;
    region?: string;
    image?: string;
    // These fields are emitted by the backend authority-aware inventory.
    // They are the only basis for exposing stale-custody resolution in the
    // server Settings view.
    execution_authority?: string;
    authority_state?: string;
  };
  health: StackServerHealth;
}

export interface StackOperationService {
  id?: string;
  name: string;
  display_name: string;
  type: string;
  status: string;
  url?: string;
  port?: number;
  target_server?: string;
  target_server_id?: string;
  desired_state?: string;
  observed_state?: string;
  access?: {
    mode: "direct" | "relay" | "unavailable" | string;
    url?: string;
    access_profile_ref?: string;
    reason_code?: string;
    route_id?: string;
  };
  allowed_actions?: Array<"start" | "stop" | "restart" | string>;
}

export interface StackReadiness {
  status: string;
  can_start: boolean;
  required_servers: number;
  approved_servers: number;
  connected_servers: number;
  pending_servers: number;
  assigned_servers: number;
  available_servers: number;
  unassigned_servers: number;
  message: string;
  review_required: boolean;
}

export interface StackNextStep {
  id: string;
  label: string;
  description: string;
  status: "completed" | "current" | "pending" | string;
  action?: string;
}

export interface StackOperationKPIs {
  registered_servers: number;
  healthy_servers: number;
  running_services: number;
  active_alerts: number;
}

export interface StackOperationMonitoring {
  status: string;
  queryBackend: string;
  queryBackendStatus?: string;
  ingestBackend: string;
  ingestStatus?: string;
  otlpStatus?: string;
  collectorMode: string;
  compatibilityMode: string;
  seriesCount?: number;
  unscopedAlerts?: number;
  alertRuleCount?: number;
  queryProof?: string;
  rangeProof?: string;
  message?: string;
}

export interface StackOperationAlert {
  name: string;
  severity: string;
  message: string;
  value: number;
  status: string;
  labels?: Record<string, string>;
}

export interface StackLatestFailure {
  job_id: string;
  type: string;
  state: string;
  step?: string;
  message?: string;
  error?: string;
  reason?: string;
  lease_id?: string;
  runtime_ip?: string;
  runtime_phase?: string;
  target_bootstrap?: Record<string, unknown>;
  runtime_diagnostics?: Record<string, unknown>;
  diagnostics_available: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface StackRuntimeLifecyclePhase {
  id: string;
  status: "pending" | "running" | "completed" | "failed" | string;
  message?: string;
  observed_at?: string;
  evidence?: Record<string, unknown>;
}

export interface StackRuntimeLifecycle {
  version: "techstack.runtime-lifecycle/v1" | string;
  current_phase?: string;
  last_safe_checkpoint?: string;
  phases: StackRuntimeLifecyclePhase[];
}

export interface StackOperationsJob {
  id: string;
  type: string;
  state: string;
  progress: number;
  step?: string;
  message?: string;
  error?: string;
  wait_reason?: string;
  next_resume_at?: string;
  resume_available_at?: string;
  resume_available?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface StackOperationsPayload {
  stack: Stack & { status?: string; state?: string };
  readiness: StackReadiness;
  nextSteps: StackNextStep[];
  kpis: StackOperationKPIs;
  servers: StackOperationServer[];
  services: StackOperationService[];
  monitoring: StackOperationMonitoring;
  alerts: StackOperationAlert[];
  currentJob?: StackOperationsJob | null;
  runtimeLifecycle?: StackRuntimeLifecycle | null;
  latestFailure?: StackLatestFailure | null;
  custodyLeases?: StackCustodyLease[] | null;
}

/**
 * A managed-runtime lease that still holds custody but has no machine behind
 * it. It is deliberately not a server: rendering it as one produced cards for
 * VMs that had already been deleted at the provider.
 */
export interface StackCustodyLease {
  lease_id: string;
  server_id?: string;
  label: string;
  provider?: string;
  reason: string;
  status?: string;
  last_known_ip?: string;
  updated_at?: string;
  allowed_actions?: Array<"decommission" | "resolve_custody">;
}

export interface StackServerDetailsPayload {
  stack: Stack & { status?: string; state?: string };
  server: StackOperationServer;
  services: StackOperationService[];
  checks: Array<{
    id: string;
    worker_id: string;
    stack_id?: string;
    check_type: string;
    description?: string;
    blocking: boolean;
    status: string;
    message?: string;
    details?: Record<string, unknown>;
    error?: string;
    executed_at?: string;
    duration_ms?: number;
    request_id?: string;
  }>;
  logs: Array<Record<string, unknown>>;
  health: StackServerHealth;
  monitoring: StackOperationMonitoring;
}

// ============================================================================
// Import/Export Types
// ============================================================================

/**
 * Validation error from import validation
 */
export interface ImportValidationError {
  path: string;
  code: string;
  message: string;
}

/**
 * Result of import validation
 * NOTE: StackKit detection happens AFTER import in the Unifier process,
 * not during validation preview.
 */
export interface ImportValidationResult {
  valid: boolean;
  errors?: ImportValidationError[];
  warnings?: ImportValidationError[];
}

/**
 * Result of a successful import
 */
export interface ImportResult {
  stack_id: string;
  job_id: string;
  name: string;
  state: string;
  message: string;
  source: string;
  redirect?: string;
}

export interface AssignStackWorkerResponse {
  stack_id: string;
  worker_id: string;
  server: StackOperationServer;
}

export interface AddManagedRuntimeServerRequest {
  /** Stable logical server slot. Reusing it resumes the same intent. */
  runtime_slot_key?: string;
  node_role?: "foundation" | "worker" | "storage" | string;
  runtime_offering_id?: string;
  provider_id: "centron" | "ionos";
  ionos_datacenter?: string;
  provider_region?: string;
  stackkit?: string;
  services?: string[];
}

export interface AddManagedRuntimeServerResponse {
  stack_id: string;
  job_id?: string;
  runtime_slot_key: string;
  runtime_slot_id: string;
  lease_id: string;
  runtime_server_id: string;
  resource_generation_id: string;
  operation_id: string;
  provider_id: "centron" | "ionos";
  ionos_datacenter?: string;
  provider_region?: string;
  node_role: string;
  runtime_offering_id: string;
  enrollment_status: string;
  runtime_phase: "lease_pending";
  idempotent_replay: boolean;
  message: string;
  warnings?: string[];
}

/**
 * Result of export
 */
export interface ExportResult {
  content: string;
  format: "yaml" | "json";
  stack_id: string;
  exported_at: string;
}

// ============================================================================
// Stack API Functions
// ============================================================================

export async function listStacks(): Promise<Stack[]> {
  return apiRequest<Stack[]>("GET", "/api/v1/stacks");
}

export async function getStack(stackId: string): Promise<Stack> {
  return apiRequest<Stack>(
    "GET",
    `/api/v1/stacks/${encodeURIComponent(stackId)}`,
  );
}

export async function createStack(
  req: CreateStackRequest,
  idempotencyKey?: string,
): Promise<CreateStackResponse> {
  return apiRequest<CreateStackResponse>(
    "POST",
    "/api/v1/stacks",
    JSON.stringify(req),
    undefined,
    idempotencyKey ? { "X-Idempotency-Key": idempotencyKey } : undefined,
  );
}

// pruneOrphanStacks removes only orphan entries (no live lease, no worker). It
// never decommissions a lease or stops a server, so it is the safe target for
// the generic "clean up old entries" button. It makes no lease/provider calls,
// so it uses a shorter timeout.
export async function pruneOrphanStacks(
  stackId?: string,
): Promise<StackPruneResponse> {
  const query = stackId ? `?stack_id=${encodeURIComponent(stackId)}` : "";
  const res = await fetchApi<StackPruneResponse>(
    `/api/v1/stacks/prune-orphans${query}`,
    {
      method: "POST",
      timeoutMs: 30_000,
    },
  );
  clearCreatingSessionState();
  return res.data;
}

export async function deployStack(
  stackId: string,
): Promise<StackJobAcceptedResponse> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<StackJobAcceptedResponse>(
    "POST",
    `/api/v1/stacks/${encoded}/deploy`,
  );
}

export async function provisionStack(
  stackId: string,
): Promise<StackJobAcceptedResponse> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<StackJobAcceptedResponse>(
    "POST",
    `/api/v1/stacks/${encoded}/provision`,
  );
}

export async function resumeStackEnrollment(
  stackId: string,
  req: ResumeStackEnrollmentRequest,
): Promise<ResumeStackEnrollmentResponse> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<ResumeStackEnrollmentResponse>(
    "POST",
    `/api/v1/stacks/${encoded}/resume-enrollment`,
    JSON.stringify(req),
  );
}

export async function retryStackRollout(
  stackId: string,
  req: RetryStackRolloutRequest,
): Promise<RetryStackRolloutResponse> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<RetryStackRolloutResponse>(
    "POST",
    `/api/v1/stacks/${encoded}/retry-rollout`,
    JSON.stringify(req),
  );
}

// destroyStack tears down the stack's runtime resources and removes the entry
// from the dashboard. The backend rejects it for the protected demo anchor.
export async function destroyStack(
  stackId: string,
): Promise<StackJobAcceptedResponse> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<StackJobAcceptedResponse>(
    "POST",
    `/api/v1/stacks/${encoded}/destroy`,
  );
}

export async function getStackOperations(
  stackId: string,
): Promise<StackOperationsPayload> {
  const encoded = encodeURIComponent(stackId);
  return apiRequest<StackOperationsPayload>(
    "GET",
    `/api/v1/stacks/${encoded}/operations`,
  );
}

export async function runStackKitLifecycleOperation(
  stackId: string,
  request: StackKitLifecycleRequest,
): Promise<StackKitLifecycleAcceptedResponse> {
  return apiRequest<StackKitLifecycleAcceptedResponse>(
    "POST",
    `/api/v1/stacks/${encodeURIComponent(stackId)}/stackkit/operations`,
    JSON.stringify(request),
  );
}

export async function getStackServerDetails(
  stackId: string,
  serverId: string,
): Promise<StackServerDetailsPayload> {
  return apiRequest<StackServerDetailsPayload>(
    "GET",
    `/api/v1/stacks/${encodeURIComponent(stackId)}/servers/${encodeURIComponent(
      serverId,
    )}`,
  );
}

export async function assignStackWorker(
  stackId: string,
  workerId: string,
): Promise<AssignStackWorkerResponse> {
  return apiRequest<AssignStackWorkerResponse>(
    "POST",
    `/api/v1/stacks/${encodeURIComponent(stackId)}/workers/${encodeURIComponent(
      workerId,
    )}/assign`,
  );
}

export async function addManagedRuntimeServer(
  stackId: string,
  req: AddManagedRuntimeServerRequest,
  idempotencyKey: string,
): Promise<AddManagedRuntimeServerResponse> {
  return apiRequest<AddManagedRuntimeServerResponse>(
    "POST",
    `/api/v1/stacks/${encodeURIComponent(stackId)}/managed-runtimes`,
    JSON.stringify(req),
    undefined,
    { "Idempotency-Key": idempotencyKey },
  );
}

function monthlyRuntimePath(
  leaseId: string,
  suffix = "",
  tenantId?: string,
  extraQuery: Record<string, string | number | undefined> = {},
): string {
  const encoded = encodeURIComponent(leaseId);
  const params = new URLSearchParams();
  if (tenantId) params.set("tenant_id", tenantId);
  for (const [key, value] of Object.entries(extraQuery)) {
    if (value !== undefined) params.set(key, String(value));
  }
  const serialized = params.toString();
  const query = serialized ? `?${serialized}` : "";
  return `/api/v1/monthly-runtimes/${encoded}${suffix}${query}`;
}

/**
 * Status-returning monthly-runtime endpoints (status / start / stop /
 * enable-ssh / disable-ssh / decommission / ssh-info). They share a single
 * `(leaseId, tenantId?)` signature and a {@link MonthlyRuntimeStatus} payload,
 * so the public functions below are generated from this table rather than
 * hand-duplicated. Each entry pins the exact HTTP method + path suffix.
 */
const MONTHLY_RUNTIME_STATUS_ACTIONS = {
  getMonthlyRuntimeStatus: ["GET", ""],
  startMonthlyRuntime: ["POST", "/start"],
  stopMonthlyRuntime: ["POST", "/stop"],
  enableMonthlyRuntimeSSH: ["POST", "/enable-ssh"],
  disableMonthlyRuntimeSSH: ["POST", "/disable-ssh"],
  decommissionMonthlyRuntime: ["POST", "/decommission", 190_000],
  reconnectMonthlyRuntime: ["POST", "/reconnect"],
  getMonthlyRuntimeSSHInfo: ["GET", "/ssh"],
} as const satisfies Record<string, readonly [string, string, number?]>;

type MonthlyRuntimeStatusAction = (
  leaseId: string,
  tenantId?: string,
) => Promise<MonthlyRuntimeStatus>;

const monthlyRuntimeStatusActions = Object.fromEntries(
  Object.entries(MONTHLY_RUNTIME_STATUS_ACTIONS).map(
    ([name, [method, suffix, timeoutMs]]) => [
      name,
      (leaseId: string, tenantId?: string) =>
        apiRequest<MonthlyRuntimeStatus>(
          method,
          monthlyRuntimePath(leaseId, suffix, tenantId),
          undefined,
          timeoutMs,
        ),
    ],
  ),
) as Record<
  keyof typeof MONTHLY_RUNTIME_STATUS_ACTIONS,
  MonthlyRuntimeStatusAction
>;

export const getMonthlyRuntimeStatus =
  monthlyRuntimeStatusActions.getMonthlyRuntimeStatus;
export const startMonthlyRuntime =
  monthlyRuntimeStatusActions.startMonthlyRuntime;
export const stopMonthlyRuntime =
  monthlyRuntimeStatusActions.stopMonthlyRuntime;
export const enableMonthlyRuntimeSSH =
  monthlyRuntimeStatusActions.enableMonthlyRuntimeSSH;
export const disableMonthlyRuntimeSSH =
  monthlyRuntimeStatusActions.disableMonthlyRuntimeSSH;
export const decommissionMonthlyRuntime =
  monthlyRuntimeStatusActions.decommissionMonthlyRuntime;
export const reconnectMonthlyRuntime =
  monthlyRuntimeStatusActions.reconnectMonthlyRuntime;
export const getMonthlyRuntimeSSHInfo =
  monthlyRuntimeStatusActions.getMonthlyRuntimeSSHInfo;

export async function forceDecommissionMonthlyRuntime(
  leaseId: string,
  tenantId?: string,
): Promise<MonthlyRuntimeStatus> {
  return apiRequest<MonthlyRuntimeStatus>(
    "POST",
    monthlyRuntimePath(leaseId, "/decommission", tenantId),
    JSON.stringify({ force: true } satisfies MonthlyRuntimeActionOptions),
    190_000,
  );
}

export async function resolveMonthlyRuntimeCustody(
  leaseId: string,
  tenantId?: string,
): Promise<MonthlyRuntimeStatus> {
  return apiRequest<MonthlyRuntimeStatus>(
    "POST",
    monthlyRuntimePath(leaseId, "/resolve-custody", tenantId),
    JSON.stringify({ provider_cleanup_confirmed: true }),
  );
}

export async function getMonthlyRuntimeOfferings(): Promise<
  MonthlyRuntimeOffering[]
> {
  return apiRequest<MonthlyRuntimeOffering[]>(
    "GET",
    "/api/v1/monthly-runtimes/offerings",
  );
}

export async function getMonthlyRuntimeOperations(
  leaseId: string,
  tenantId?: string,
  limit = 50,
): Promise<MonthlyRuntimeOperation[]> {
  return apiRequest<MonthlyRuntimeOperation[]>(
    "GET",
    monthlyRuntimePath(leaseId, "/operations", tenantId, { limit }),
  );
}

// ============================================================================
// Stack Spec Import/Export API
// ============================================================================

/**
 * Validate a stack-spec.yaml or legacy kombination.yaml before importing.
 * @param content - The YAML or JSON content to validate
 */
export async function validateKombinationImport(
  content: string,
): Promise<ImportValidationResult> {
  return postYamlSpec<ImportValidationResult>(
    "Validate import",
    "/api/v1/stacks/import/validate",
    content,
  );
}

/**
 * Import a stack-spec.yaml or legacy kombination.yaml and create a new stack.
 * Triggers the same flow as the config wizard (animated creating page)
 * @param content - The YAML or JSON content to import
 */
export async function importKombinationSpec(
  content: string,
): Promise<ImportResult> {
  return postYamlSpec<ImportResult>("Import", "/api/v1/stacks/import", content);
}

/**
 * Export a stack configuration as stack-spec.yaml.
 * @param stackId - The stack ID to export
 * @param format - The desired format (yaml or json)
 */
export async function exportKombinationSpec(
  stackId: string,
  format: "yaml" | "json" = "yaml",
): Promise<ExportResult> {
  const acceptHeader =
    format === "yaml" ? "application/yaml" : "application/json";

  const response = await fetch(
    `/api/v1/stacks/${encodeURIComponent(stackId)}/export`,
    {
      method: "GET",
      credentials: "include",
      headers: {
        Accept: acceptHeader,
        ...getOptionalAuthHeaders(),
      },
    },
  );

  if (!response.ok) {
    throw await parseApiErrorResponse(response, {
      action: "Export",
      method: "GET",
    });
  }

  // For YAML format, we get the raw content
  if (format === "yaml") {
    const content = await response.text();
    assertNotHtmlResponse(response, content, "Export", "GET");
    return {
      content,
      format: "yaml",
      stack_id: stackId,
      exported_at: new Date().toISOString(),
    };
  }

  // For JSON, we get a structured response
  const text = await response.text();
  assertNotHtmlResponse(response, text, "Export", "GET");

  const data = JSON.parse(text);
  const exportedSpec = data.stack_spec ?? data.kombination;
  return {
    content: JSON.stringify(exportedSpec, null, 2),
    format: "json",
    stack_id: data.stack_id || stackId,
    exported_at: data.exported_at || new Date().toISOString(),
  };
}
