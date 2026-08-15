/**
 * Wizard-run facade client (POST /api/v1/wizard/runs, ADR-0036 phase 3/4).
 *
 * One endpoint executes a wizard run end to end: found a new kit deployment
 * or join an existing one under the homelab umbrella. Requests are gated by
 * the native_v2_wizard beta flag server-side (403 with required_features)
 * and fail closed without the Postgres control plane (503).
 */
import { fetchApi, ApiRequestError } from "./client";

/** Wire schema literal the backend's closed intent contract requires. */
export const WIZARD_INTENT_SCHEMA = "techstack.wizard-intent/v1";

export interface WizardIntent {
  schema: typeof WIZARD_INTENT_SCHEMA;
  run_kind: "first-run" | "expansion";
  name: string;
  domain_base?: string;
  goals?: string[];
  server: {
    roles?: string[];
    purpose?: string;
    transport?: "install-command" | "connect-remote" | "kombify-cloud";
  };
  kit_assignment: {
    mode: "found" | "join";
    kit_slug?: string;
    kit_deployment_id?: string;
  };
}

export interface WizardRunManagedParams {
  provider_id: string;
  runtime_offering_id?: string;
  provider_region?: string;
  ionos_datacenter?: string;
}

export interface WizardRunRemoteParams {
  host?: string;
  port?: number;
  user?: string;
  auth_method?: string;
  ssh_key_label?: string;
  use_sudo?: boolean;
}

export interface WizardRunRequest {
  intent: WizardIntent;
  /**
   * Owner-bootstrap options, copied verbatim into the create core — keys must
   * be the backend option spellings (owner_bootstrap_mode, owner_source,
   * owner_email, owner_username, owner_display_name,
   * recovery_passphrase_hash). First-run only; expansions omit it.
   */
  owner?: Record<string, unknown>;
  managed?: WizardRunManagedParams;
  remote?: WizardRunRemoteParams;
  services?: string[];
}

export interface WizardRunResponse {
  run_id: string;
  run_kind: "first-run" | "expansion";
  requested_run_kind?: string;
  coerced?: boolean;
  homelab_id: string;
  kit_assignment_mode: "found" | "join";
  kit_slug?: string;
  stack_id: string;
  server_id?: string;
  node_id: string;
  name?: string;
  job_id?: string;
  pairing_job_id?: string;
  state: "provisioning" | "awaiting_pairing";
  auto_deploy?: boolean;
  operations_url?: string;
  idempotent_replay?: boolean;
  unmapped_goals?: string[];
  unmapped_purpose?: string;
  release_version?: string;
  bootstrap_token?: string;
  bootstrap_token_expires_at?: string;
  owner_spec_endpoint?: string;
  owner_spec_scopes?: string[];
}

export interface ActiveWizardRunJob {
  id: string;
  state: string;
  progress?: number;
  step?: string;
  message?: string;
}

export interface ActiveWizardRun {
  run_id: string;
  status: "completed" | "failed";
  run_kind: string;
  requested_run_kind?: string;
  homelab_id?: string;
  stack_id?: string;
  node_id?: string;
  job_id?: string;
  pairing_job_id?: string;
  error_reason?: string;
  created_at?: string;
  updated_at?: string;
  result?: Record<string, unknown>;
  job?: ActiveWizardRunJob | null;
}

async function apiRequest<T>(
  method: string,
  path: string,
  body?: BodyInit,
  headers?: HeadersInit,
  timeoutMs?: number,
): Promise<T> {
  const res = await fetchApi<T>(
    path,
    body === undefined
      ? { method, headers, timeoutMs }
      : { method, body, headers, timeoutMs },
  );
  return res.data;
}

/**
 * Execute a wizard run. Pass a stable idempotency key per semantic attempt:
 * the backend replays completed runs and resumes failed ones on the same key
 * (a different payload on a used key is a 409).
 */
export async function createWizardRun(
  req: WizardRunRequest,
  idempotencyKey?: string,
): Promise<WizardRunResponse> {
  // The run validates via the pinned CLI and dispatches provisioning with a
  // 30s server-side budget; the default 10s client abort would orphan
  // completed runs behind a fake timeout error.
  return apiRequest<WizardRunResponse>(
    "POST",
    "/api/v1/wizard/runs",
    JSON.stringify(req),
    idempotencyKey ? { "X-Idempotency-Key": idempotencyKey } : undefined,
    60_000,
  );
}

/**
 * The caller's latest wizard run with a live provision-job snapshot, or null
 * when none exists (also null while the native_v2_wizard flag is off — the
 * server hides ledger reads behind the same gate).
 */
export async function getActiveWizardRun(): Promise<ActiveWizardRun | null> {
  try {
    const res = await fetchApi<{ run: ActiveWizardRun | null }>(
      "/api/v1/wizard/runs/active",
      { method: "GET" },
    );
    return res.data?.run ?? null;
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 404) {
      return null;
    }
    throw error;
  }
}

/** Failed runs stay resumable, but an abandoned one must not nag forever. */
const FAILED_RUN_ATTENTION_MS = 24 * 60 * 60 * 1000;
/** Pairing tokens live at most 30 minutes; after that a resume mints anew. */
const PAIRING_ATTENTION_MS = 35 * 60 * 1000;

function runAgeMs(run: ActiveWizardRun): number {
  const updated = Date.parse(run.updated_at ?? "");
  if (Number.isNaN(updated)) return Number.POSITIVE_INFINITY;
  return Math.max(0, Date.now() - updated);
}

/**
 * True when an active run should surface a resume affordance: its provision
 * job is still moving, a join still awaits pairing (within the pairing
 * token's lifetime), or the run recently failed with a resumable partial
 * state. Terminal completed runs and stale leftovers stay quiet.
 */
export function wizardRunNeedsAttention(run: ActiveWizardRun | null): boolean {
  if (!run) return false;
  if (run.status === "failed") {
    return runAgeMs(run) < FAILED_RUN_ATTENTION_MS;
  }
  const jobState = run.job?.state ?? "";
  if (["pending", "running", "waiting", "in_progress"].includes(jobState)) {
    return true;
  }
  const resultState =
    typeof run.result?.state === "string" ? run.result.state : "";
  if (resultState === "awaiting_pairing" && !run.job) {
    return runAgeMs(run) < PAIRING_ATTENTION_MS;
  }
  return false;
}
