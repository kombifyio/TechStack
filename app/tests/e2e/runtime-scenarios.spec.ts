import { expect, test, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import type { MonthlyRuntimeCleanupReadback } from "../../src/lib/api/stacks";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import {
  runManagedDay2Evidence,
  type ManagedDay2Evidence,
} from "./managed-day2-evidence";
import { managedLifecycleReceipt } from "./managed-lifecycle-receipt";
import path from "node:path";
import { promisify } from "node:util";
import {
  createJobObservation,
  managedRuntimeObservationBudgets,
} from "./runtime-job-observation";
import {
  sanitizeSensitiveText,
  scrubSensitiveValue,
} from "../../src/lib/security/sensitive-data";
import {
  approveWorkerViaApi,
  createPairingTokenViaApi,
  listWorkersViaApi,
  selectUseCase,
} from "../helpers/test-utils";
import {
  authenticateRuntimeUser,
  authorizedFetch,
  bearerToken,
  captureGatewayApiSession,
  createGatewaySessionKeeper,
  type GatewaySessionKeeper,
  type RuntimeBearer,
} from "./runtime-auth";

const execFileAsync = promisify(execFile);

async function testSourceCommit(): Promise<string> {
  const { stdout } = await execFileAsync("git", ["rev-parse", "HEAD"]);
  return stdout.trim();
}
const PRODUCT_BASE = requireLiveProductURL(
  "TechStack product",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL,
    process.env.TECHSTACK_E2E_PRODUCT_URL,
    process.env.PLAYWRIGHT_BASE_URL,
  ) || "https://techstack.kombify.io",
);
const API_BASE = requireLiveProductURL(
  "TechStack API",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_API_URL,
    process.env.TECHSTACK_API_URL,
    PRODUCT_BASE,
  ) || PRODUCT_BASE,
);
// Agents talk to the stable control plane: on the Preview channel the owner
// server enrolls against the stable origin, not the Preview one.
const AGENT_ORIGIN =
  normalizeURL(process.env.TECHSTACK_RUNTIME_E2E_AGENT_ORIGIN) || API_BASE;
const ARTIFACTS_DIR =
  process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e";
const DO_TAG = process.env.TECHSTACK_E2E_DO_TAG ?? "techstack-e2e";
const DO_REGION = process.env.TECHSTACK_E2E_DO_REGION ?? "fra1";
const DO_IMAGE = process.env.TECHSTACK_E2E_DO_IMAGE ?? "ubuntu-24-04-x64";
const DO_SIZE = process.env.TECHSTACK_E2E_DO_SIZE ?? "s-2vcpu-4gb";
// Includes the visible Wizard's bounded pipeline preview before the create
// request. The preview itself is capped at 60 seconds, so the response watcher
// must not expire at the same instant.
const CREATE_STACK_REQUEST_TIMEOUT_MS = 120_000;
const SESSION_COOKIE_NAME =
  process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session";
const USER_OWNED_RUNTIME_STACKKIT = "basement-kit";
const MANAGED_RUNTIME_STACKKIT = "cloud-kit";
const SSH_USER_KNOWN_HOSTS = process.platform === "win32" ? "NUL" : "/dev/null";
const MANAGED_RUNTIME_BUDGETS = managedRuntimeObservationBudgets(process.env);
const MANAGED_RUNTIME_PROVISION_TIMEOUT_MS =
  MANAGED_RUNTIME_BUDGETS.preparationMs;
const MANAGED_RUNTIME_RECOVERY_STACK_ID = String(
  process.env.TECHSTACK_RUNTIME_E2E_RECOVERY_STACK_ID ?? "",
).trim();
const MANAGED_RUNTIME_RECOVERY_CONFIRM = String(
  process.env.TECHSTACK_RUNTIME_E2E_RECOVERY_CONFIRM ?? "",
).trim();
// A persistent test environment (for example the long-lived TEST_RIL_PRO_USER
// managed server) is created by the same product path but must survive the
// run. Only this exact acknowledgement skips the teardown of the stack the run
// created; every other value keeps the default destroy-to-absence cleanup.
const MANAGED_RUNTIME_RETAIN_CONFIRMATION =
  "retain-persistent-test-environment";
const MANAGED_RUNTIME_RETAIN =
  String(process.env.TECHSTACK_RUNTIME_E2E_RETAIN_MANAGED ?? "").trim() ===
  MANAGED_RUNTIME_RETAIN_CONFIRMATION;
const RUNTIME_AUTH_OPTIONS = {
  productBase: PRODUCT_BASE,
  apiBase: API_BASE,
  sessionCookieName: SESSION_COOKIE_NAME,
};

type RuntimeScenario =
  "install-command" | "connect-remote" | "kombify-cloud" | "stack-lifecycle";

interface RuntimeWorkerRecord {
  id: string;
  hostname: string;
  ip?: string;
  os?: string;
  arch?: string;
  status?: string;
  approved?: boolean;
  last_seen?: string;
  source?: string;
  stack_id?: string;
  lease_id?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  desired_state?: string;
  enrollment_status?: string;
  assignable?: boolean;
}

interface RuntimeStackOperationServer {
  id: string;
  hostname: string;
  role?: string;
  status?: string;
  assignment?: string;
  stack_id?: string;
  stackId?: string;
  source?: string;
  lease_id?: string;
  runtime_lane?: string;
  runtime_offering_id?: string;
  desired_state?: string;
  enrollment_status?: string;
  approved?: boolean;
  assignable?: boolean;
}

interface RuntimeStackOperationService {
  id?: string;
  name?: string;
  display_name?: string;
  application_name?: string;
  stack_id?: string;
  stack_name?: string;
  server_id?: string;
  server_name?: string;
  status?: string;
  url?: string;
  desired_state?: string;
  observed_state?: string;
  allowed_actions?: string[];
  inventory_revision?: number;
}

interface ManagedServiceActionResponse {
  job_id?: string;
  service_id?: string;
  action?: string;
  status?: string;
}

interface ManagedServiceLogsResponse {
  service_id?: string;
  job_id?: string;
  status?: string;
  entries?: Array<{ timestamp?: string; message?: string }>;
  next_cursor?: string;
}

interface RuntimeStackOperationsPayload {
  readiness?: Record<string, unknown>;
  kpis?: Record<string, unknown>;
  servers?: RuntimeStackOperationServer[];
  services?: RuntimeStackOperationService[];
  currentJob?: {
    id?: string;
    type?: string;
    state?: string;
  };
}

interface RuntimeJobListPayload {
  items?: Array<{
    id?: string;
    type?: string;
    state?: string;
  }>;
}

interface RuntimeRegistryServicePayload {
  services?: RuntimeStackOperationService[];
}

type ProvisionedDropletResource = Awaited<ReturnType<typeof provisionDroplet>>;

interface DigitalOceanDroplet {
  id: number;
  name: string;
  status: string;
  created_at: string;
  networks?: {
    v4?: Array<{ type: string; ip_address: string }>;
  };
}

interface DigitalOceanSSHKey {
  id: number;
  fingerprint: string;
  name: string;
}

class DigitalOceanRuntimeClient {
  constructor(private readonly token: string) {}

  async request<T>(
    endpoint: string,
    init: RequestInit = {},
  ): Promise<T | null> {
    const response = await fetch(`https://api.digitalocean.com/v2${endpoint}`, {
      ...init,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.token}`,
        ...(init.headers ?? {}),
      },
    });
    if (response.status === 204) return null;
    const text = await response.text();
    const body = text ? JSON.parse(text) : null;
    if (!response.ok) {
      throw new Error(
        `DigitalOcean API ${endpoint} failed with HTTP ${response.status}: ${text}`,
      );
    }
    return body as T;
  }

  async ensureTag(name: string) {
    const existing = await this.request(
      `/tags/${encodeURIComponent(name)}`,
    ).catch((err) => {
      if (String(err?.message ?? "").includes("HTTP 404")) return null;
      throw err;
    });
    if (existing) return;
    await this.request("/tags", {
      method: "POST",
      body: JSON.stringify({ name }),
    });
  }

  async createSSHKey(
    name: string,
    publicKey: string,
  ): Promise<DigitalOceanSSHKey> {
    const body = await this.request<{ ssh_key: DigitalOceanSSHKey }>(
      "/account/keys",
      {
        method: "POST",
        body: JSON.stringify({ name, public_key: publicKey }),
      },
    );
    return body!.ssh_key;
  }

  async deleteSSHKey(idOrFingerprint?: string | number) {
    if (!idOrFingerprint) return;
    await this.request(
      `/account/keys/${encodeURIComponent(String(idOrFingerprint))}`,
      {
        method: "DELETE",
      },
    ).catch((err) => {
      if (!String(err?.message ?? "").includes("HTTP 404")) throw err;
    });
  }

  async createDroplet(args: {
    name: string;
    scenario: RuntimeScenario;
    sshKeyId: number;
  }): Promise<DigitalOceanDroplet> {
    const tags = [DO_TAG, `scenario:${args.scenario}`, `run:${runtimeRunId()}`];
    const body = await this.request<{ droplet: DigitalOceanDroplet }>(
      "/droplets",
      {
        method: "POST",
        body: JSON.stringify({
          name: args.name,
          region: DO_REGION,
          size: DO_SIZE,
          image: DO_IMAGE,
          ssh_keys: [args.sshKeyId],
          tags,
          backups: false,
          ipv6: false,
          monitoring: false,
          user_data:
            "#cloud-config\npackage_update: true\npackages:\n  - curl\n  - ca-certificates\n",
        }),
      },
    );
    return body!.droplet;
  }

  async getDroplet(id: number): Promise<DigitalOceanDroplet> {
    const body = await this.request<{ droplet: DigitalOceanDroplet }>(
      `/droplets/${id}`,
    );
    return body!.droplet;
  }

  async waitForPublicIPv4(id: number, timeoutMs = 180_000) {
    const deadline = Date.now() + timeoutMs;
    let last: DigitalOceanDroplet | null = null;
    while (Date.now() < deadline) {
      last = await this.getDroplet(id);
      const ip = publicIPv4(last);
      if (last.status === "active" && ip) return { droplet: last, ip };
      await delay(5_000);
    }
    throw new Error(
      `Timed out waiting for droplet ${id} public IPv4; last status=${last?.status ?? "unknown"}`,
    );
  }

  async deleteDroplet(id?: number) {
    if (!id) return;
    await this.request(`/droplets/${id}`, { method: "DELETE" }).catch((err) => {
      if (!String(err?.message ?? "").includes("HTTP 404")) throw err;
    });
  }
}

async function attachJson(name: string, data: unknown) {
  await test.info().attach(name, {
    body: Buffer.from(`${JSON.stringify(data, null, 2)}\n`, "utf8"),
    contentType: "application/json",
  });
}

async function attachText(name: string, data: string) {
  await test.info().attach(name, {
    body: Buffer.from(data, "utf8"),
    contentType: "text/plain",
  });
}

async function attachScreenshot(page: Page, name: string) {
  const screenshot = await page.screenshot({ fullPage: true });
  await test
    .info()
    .attach(name, { body: screenshot, contentType: "image/png" });
}

interface RuntimeHTTPResponse {
  ok: boolean;
  status: number;
  text(): Promise<string>;
}

async function postJsonWithCurl(
  url: string,
  token: string,
  payload: unknown,
  timeoutMs: number,
  label: string,
): Promise<RuntimeHTTPResponse> {
  const dir = await mkdtemp(path.join(tmpdir(), "techstack-runtime-post-"));
  const bodyPath = path.join(dir, "body.json");
  try {
    logRuntimeE2E("runtime api post start", { label, url });
    await writeFile(bodyPath, JSON.stringify(payload), "utf8");
    const { stdout } = await execFileAsync(
      process.platform === "win32" ? "curl.exe" : "curl",
      [
        "--silent",
        "--show-error",
        "--max-time",
        String(Math.ceil(timeoutMs / 1000)),
        "--connect-timeout",
        "10",
        "--request",
        "POST",
        "--header",
        "Content-Type: application/json",
        "--header",
        `Authorization: Bearer ${token}`,
        "--data-binary",
        `@${bodyPath}`,
        "--write-out",
        "\n%{http_code}",
        url,
      ],
      { timeout: timeoutMs + 5_000, maxBuffer: 8 * 1024 * 1024 },
    );
    const lines = stdout.split("\n");
    const status = Number(lines.pop() ?? 0);
    const text = lines.join("\n");
    logRuntimeE2E("runtime api post completed", { label, status });
    return {
      ok: status >= 200 && status < 300,
      status,
      async text() {
        return text;
      },
    };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    logRuntimeE2E("runtime api post failed", { label, message });
    throw new Error(
      `${label} timed out or failed after ${timeoutMs}ms: ${message}`,
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

async function abandonExactStaleManagedRuntimeJob(
  token: RuntimeBearer,
  apiBase: string,
  stackId: string,
  jobs: RuntimeJobListPayload,
) {
  const candidates = (jobs.items ?? []).filter((job) => {
    const state = String(job.state ?? "")
      .trim()
      .toLowerCase();
    const type = String(job.type ?? "")
      .trim()
      .toLowerCase();
    return state === "running" && (type === "provision" || type === "deploy");
  });
  if (candidates.length === 0) return undefined;
  if (candidates.length !== 1) {
    throw new Error(
      `Managed runtime recovery refuses ${candidates.length} concurrent running provision/deploy jobs`,
    );
  }

  const jobId = String(candidates[0].id ?? "").trim();
  const jobType = String(candidates[0].type ?? "")
    .trim()
    .toLowerCase();
  if (!jobId) throw new Error("Managed runtime recovery job id is missing");

  const response = await postJsonWithCurl(
    runtimeApiUrl(
      apiBase,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/jobs/${encodeURIComponent(jobId)}/abandon`,
    ),
    await bearerToken(token),
    {},
    30_000,
    "Abandon exact stale managed runtime job",
  );
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Abandon exact stale managed runtime job failed with HTTP ${response.status}: ${text}`,
    );
  }
  return {
    job_id: jobId,
    job_type: jobType,
    response: body,
  };
}

async function createRuntimeStack(
  token: string,
  name: string,
  scenario: RuntimeScenario,
  metadata: Record<string, string>,
  extraSpec: Record<string, unknown> = {},
) {
  logRuntimeE2E("create runtime stack request preparing", {
    scenario,
    stack_name: name,
  });
  const stackkit =
    scenario === "kombify-cloud"
      ? MANAGED_RUNTIME_STACKKIT
      : USER_OWNED_RUNTIME_STACKKIT;
  const stackSpec = {
    name,
    stackkit,
    mode: "simple",
    runtime: "docker",
    context: scenario === "kombify-cloud" ? "cloud" : "local",
    network: { mode: scenario === "kombify-cloud" ? "public" : "local" },
    compute: { tier: "standard" },
    services: {
      include: ["pocket-id", "traefik", "otel-collector"],
    },
    metadata: {
      spec_format: "stack-spec",
      spec_version: "2.0",
      created_by: "wizard",
      created_by_detail: "runtime-e2e",
      scenario,
      stackkit_catalog_ref: stackkit,
      verification_status: "pending",
      desired_state: "running",
      ...metadata,
    },
    ...extraSpec,
  };

  const response = await postJsonWithCurl(
    `${API_BASE}/api/v1/stacks`,
    token,
    {
      name,
      mode: "easy",
      stack_spec: stackSpec,
    },
    CREATE_STACK_REQUEST_TIMEOUT_MS,
    "Create runtime stack request",
  );
  const text = await response.text();
  logRuntimeE2E("create runtime stack response body received", {
    status: response.status,
    body_chars: text.length,
  });
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    logRuntimeE2E("create runtime stack request rejected", {
      status: response.status,
      body_preview: text.slice(0, 500),
    });
    console.error(
      `Create runtime stack failed (HTTP ${response.status}): ${text}`,
    );
    process.exit(1);
  }
  const data = json.data ?? json;
  await attachJson(`${scenario}-stack-create.json`, redactObject(data));
  return data as { stack_id: string; job_id?: string };
}

async function createManagedRuntimeStackViaWizard(
  page: Page,
  providerId: "centron" | "ionos",
  onCreated: (stack: { stack_id: string; lease_id?: string }) => void,
): Promise<{
  stack: { stack_id: string; job_id?: string };
  stackName: string;
  api: { token: string; apiBase: string };
}> {
  await page.goto("/stacks/new", { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("easy-wizard")).toBeVisible({
    timeout: 30_000,
  });

  await page.getByTestId("easy-feature-storage").click();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await page.getByTestId("server-branch-new").click();
  await page.getByTestId("server-mode-kombify-cloud").click();
  await expect(page.getByTestId("managed-provider-selector")).toBeVisible();
  await page.getByTestId(`managed-provider-${providerId}`).click();
  await expect(
    page
      .getByTestId(`managed-provider-${providerId}`)
      .locator('input[type="radio"]'),
  ).toBeChecked();
  await attachScreenshot(
    page,
    `runtime-kombify-cloud-${providerId}-wizard.png`,
  );

  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-3")).toBeVisible();
  await page.getByTestId("easy-access-anywhere").click();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-4")).toBeVisible();
  await page.getByTestId("easy-users-solo").click();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-5")).toBeVisible();
  await expect(page.getByTestId("wizard-create")).toBeVisible();

  const createResponsePromise = page.waitForResponse(
    (response) => {
      const url = new URL(response.url());
      return (
        response.request().method() === "POST" &&
        (url.pathname === "/v1/techstack/wizard/runs" ||
          url.pathname === "/api/v1/wizard/runs")
      );
    },
    { timeout: CREATE_STACK_REQUEST_TIMEOUT_MS },
  );
  await page.getByTestId("wizard-create").click();
  const response = await createResponsePromise;
  const request = response.request();
  const responseText = await response.text();
  const responseJson = responseText ? JSON.parse(responseText) : {};
  if (!response.ok()) {
    throw new Error(
      `Managed Wizard create failed with HTTP ${response.status()}: ${responseText}`,
    );
  }

  const data = responseJson.data ?? responseJson;
  const stackId = String(data.stack_id ?? "").trim();
  if (!stackId) {
    throw new Error("Managed Wizard create response omitted stack_id");
  }
  // Custody must survive any subsequent contract or artifact failure. The
  // caller already captured its owner-bound Gateway session before creating.
  onCreated({
    stack_id: stackId,
    lease_id: String(data.lease_id ?? "").trim() || undefined,
  });

  const requestUrl = new URL(request.url());
  if (requestUrl.pathname !== "/v1/techstack/wizard/runs") {
    throw new Error(
      `Managed Wizard create bypassed the signed Gateway path: ${requestUrl.origin}${requestUrl.pathname}`,
    );
  }
  const authorization = request.headers()["authorization"] ?? "";
  const gatewayToken = authorization.replace(/^Bearer\s+/i, "").trim();
  if (!gatewayToken) {
    throw new Error(
      "Managed Wizard create omitted the Auth0 Gateway bearer token",
    );
  }

  const requestBody = request.postDataJSON() as Record<string, any>;
  const intent = requestBody?.intent ?? {};
  const managed = requestBody?.managed ?? {};
  if (
    intent.schema !== "techstack.wizard-intent/v1" ||
    intent.run_kind !== "first-run" ||
    intent.server?.transport !== "kombify-cloud" ||
    intent.kit_assignment?.mode !== "found" ||
    managed.provider_id !== providerId ||
    managed.runtime_offering_id !== "monthly-runtime-standard"
  ) {
    throw new Error(
      `Managed Wizard payload did not bind provider ${providerId}: ${JSON.stringify(redactObject(requestBody))}`,
    );
  }

  const stackName = String(
    data.name ?? requestBody.intent?.name ?? stackId,
  ).trim();
  const gatewayBase = `${requestUrl.origin}/v1/techstack`;
  await attachJson(`kombify-cloud-${providerId}-wizard-create.json`, {
    provider_id: providerId,
    request_url: `${requestUrl.origin}${requestUrl.pathname}`,
    gateway_base: gatewayBase,
    request: redactObject(requestBody),
    response: redactObject(data),
    visible_wizard: true,
    signed_gateway_path: true,
  });

  return {
    stack: data as { stack_id: string; job_id?: string },
    stackName,
    api: { token: gatewayToken, apiBase: gatewayBase },
  };
}

function runtimeApiUrl(apiBase: string, apiPath: string) {
  const base = apiBase.replace(/\/+$/, "");
  const path = apiPath.startsWith("/") ? apiPath : `/${apiPath}`;
  if (base.endsWith("/v1/techstack")) {
    if (path === "/api/v1") return base;
    if (path.startsWith("/api/v1/")) return `${base}${path.slice(7)}`;
  }
  return `${base}${path}`;
}

function managedRuntimeE2ECleanupCandidates(
  stacks: Array<Record<string, unknown>>,
  providerId: "centron" | "ionos",
) {
  const namePattern = new RegExp(`^runtime-cloud-${providerId}-`);
  return stacks.filter((stack) => {
    const name = String(stack.name ?? "").trim();
    const provider = String(stack.provider_id ?? "")
      .trim()
      .toLowerCase();
    const runtimeLane = String(stack.runtime_lane ?? "")
      .trim()
      .toLowerCase();
    const serverMode = String(stack.server_mode ?? "")
      .trim()
      .toLowerCase();
    const leaseId = String(stack.lease_id ?? "").trim();
    return (
      namePattern.test(name) &&
      provider === providerId &&
      (runtimeLane === "monthly-runtime" || serverMode === "monthly-runtime") &&
      leaseId.length > 0
    );
  });
}

function exactManagedRecoveryLeaseId(
  stack: Record<string, unknown>,
  operations: RuntimeStackOperationsPayload,
) {
  const projected = String(stack.lease_id ?? "").trim();
  if (projected) return projected;

  const stackId = String(stack.id ?? "").trim();
  const foundationLeases = new Set(
    (operations.servers ?? [])
      .filter((server) => {
        const serverStackId = String(server.stack_id ?? "").trim();
        const role = String(server.role ?? "")
          .trim()
          .toLowerCase();
        return (
          (!serverStackId || serverStackId === stackId) &&
          (role === "primary" || role === "foundation")
        );
      })
      .map((server) => String(server.lease_id ?? "").trim())
      .filter(Boolean),
  );
  return foundationLeases.size === 1 ? [...foundationLeases][0] : "";
}

async function getStackList(token: RuntimeBearer, apiBase = API_BASE) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(apiBase, "/api/v1/stacks"),
  );
  if (!response.ok) {
    throw new Error(`List stacks failed with HTTP ${response.status}`);
  }
  const json = await response.json();
  return (json.data ?? json) as Array<Record<string, unknown>>;
}

async function waitForStack(
  token: RuntimeBearer,
  stackId: string,
  predicate: (stack: Record<string, unknown>) => boolean,
  timeoutMs = 240_000,
  apiBase = API_BASE,
) {
  const deadline = Date.now() + timeoutMs;
  let last: Record<string, unknown> | undefined;
  while (Date.now() < deadline) {
    const stacks = await getStackList(token, apiBase);
    last = stacks.find((stack) => stack.id === stackId);
    if (last && predicate(last)) return last;
    await delay(2_000);
  }
  throw new Error(
    `Timed out waiting for stack ${stackId}; last=${JSON.stringify(last)}`,
  );
}

async function waitForJobTerminal(
  token: RuntimeBearer,
  jobId: string,
  timeoutMs = 600_000,
  apiBase = API_BASE,
  managedObservation?: {
    onJob: (job: Record<string, unknown>) => void;
  },
) {
  const observation = createJobObservation(
    timeoutMs,
    managedObservation ? MANAGED_RUNTIME_BUDGETS.rolloutMs : undefined,
    Date.now(),
    managedObservation
      ? MANAGED_RUNTIME_BUDGETS.backendVerificationMs
      : undefined,
  );
  let last: Record<string, unknown> | undefined;
  let lastProgressSignature = "";
  let lastProgressLoggedAt = 0;
  let phase = "preparation";
  try {
    while (observation.remaining() > 0) {
      last = await getJob(
        token,
        jobId,
        apiBase,
        observation.snapshot().deadline_ms,
      );
      const observed = observation.observe(last);
      managedObservation?.onJob(last);
      if (managedObservation && observed.phase !== phase) {
        phase = observed.phase;
        managedObservationPhase(
          phase === "verification" ? "backend verification" : "rollout",
          (phase === "verification"
            ? MANAGED_RUNTIME_BUDGETS.backendVerificationMs
            : MANAGED_RUNTIME_BUDGETS.rolloutMs) + 30_000,
        );
      }
      const state = String(last.state ?? "");
      const progressSignature = [
        state,
        last.current_step ?? last.step,
        last.message,
        last.error,
        last.error_message,
      ]
        .map((value) => String(value ?? ""))
        .join("|");
      if (
        progressSignature !== lastProgressSignature ||
        Date.now() - lastProgressLoggedAt > 30_000
      ) {
        logRuntimeE2E("provision job progress", {
          job_id: jobId,
          state,
          current_step: last.current_step ?? last.step,
          phase: observed.phase,
          deadline: new Date(observed.deadline_ms).toISOString(),
          message: last.message,
          error: last.error,
          error_message: last.error_message,
        });
        lastProgressSignature = progressSignature;
        lastProgressLoggedAt = Date.now();
      }
      if (observed.terminal_observed && !observed.expired) return last;
      await delay(Math.min(3_000, observation.remaining()));
    }
    throw new Error(
      `Timed out waiting for job ${jobId} (${observation.snapshot().phase} observation); ${runtimeJobFailureSummary(last ?? {}).slice(0, 2400)}`,
    );
  } finally {
    let finalReadError: string | undefined;
    // One bounded read closes the poll/deadline race for custody evidence.
    // Expiry stays latched: a late terminal snapshot never turns timeout into PASS.
    if (managedObservation && observation.snapshot().expired) {
      try {
        const finalJob = await getJob(
          token,
          jobId,
          apiBase,
          Date.now() + 15_000,
        );
        observation.observe(finalJob);
        managedObservation.onJob(finalJob);
      } catch (error) {
        finalReadError = redactText(String(error)).slice(0, 2400);
      }
    }
    const observed = observation.snapshot();
    await attachJson(
      `job-${jobId}-observation.json`,
      redactObject({
        job_id: jobId,
        phase: observed.phase,
        deadline: new Date(observed.deadline_ms).toISOString(),
        expired: observed.expired,
        terminal_observed: observed.terminal_observed,
        last_job: observed.last_job,
        final_read_error: finalReadError,
      }),
    );
  }
}

async function getJob(
  token: RuntimeBearer,
  jobId: string,
  apiBase = API_BASE,
  deadline = Date.now() + 120_000,
) {
  // A control-plane restart or edge hiccup answers with a network error, a
  // 5xx, or an HTML error page. That is not a job outcome: keep polling
  // within the caller's deadline instead of ending the observation early.
  let transientFailures = 0;
  for (let attempt = 0; attempt < 4; attempt += 1) {
    let response: Response;
    let text: string;
    let json: Record<string, any> | undefined;
    try {
      response = await authorizedFetch(
        token,
        runtimeApiUrl(apiBase, `/api/v1/jobs/${encodeURIComponent(jobId)}`),
        {
          signal: AbortSignal.timeout(
            Math.max(1, Math.min(30_000, deadline - Date.now())),
          ),
        },
      );
      text = await response.text();
      json = text ? JSON.parse(text) : {};
    } catch (error) {
      response = new Response(null, { status: 599 });
      text = error instanceof Error ? error.message : String(error);
    }
    const transient = json === undefined || response.status >= 500;
    if (transient && deadline - Date.now() > 5_000) {
      transientFailures += 1;
      attempt -= 1; // transient reads do not use up the rate-limit retries
      logRuntimeE2E("transient job read, retrying", {
        job_id: jobId,
        status: response.status,
        transient_failures: transientFailures,
      });
      await delay(
        Math.min(
          5_000 * Math.min(transientFailures, 6),
          deadline - Date.now() - 1_000,
        ),
      );
      continue;
    }
    if (response.ok && json) {
      return (json.data ?? json) as Record<string, unknown>;
    }
    if (response.status === 429 && attempt < 3) {
      const retryAfterSeconds = Number(response.headers.get("retry-after"));
      await delay(
        Number.isFinite(retryAfterSeconds) && retryAfterSeconds > 0
          ? Math.max(
              0,
              Math.min(
                retryAfterSeconds * 1_000,
                30_000,
                deadline - Date.now(),
              ),
            )
          : Math.max(0, Math.min(5_000 * (attempt + 1), deadline - Date.now())),
      );
      continue;
    }
    throw new Error(
      `Read job ${jobId} failed with HTTP ${response.status}: ${text}`,
    );
  }
  throw new Error(`Read job ${jobId} exhausted rate-limit retries`);
}

async function fetchRuntimeApi<T>(
  token: RuntimeBearer,
  path: string,
  apiBase = API_BASE,
  deadline = Date.now() + 30_000,
): Promise<T> {
  const response = await authorizedFetch(token, runtimeApiUrl(apiBase, path), {
    signal: AbortSignal.timeout(
      Math.max(1, Math.min(30_000, deadline - Date.now())),
    ),
  });
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Runtime API GET ${path} failed with HTTP ${response.status}: ${text}`,
    );
  }
  return (json.data ?? json) as T;
}

// Read the owner-bound native receipt. Terminal server aggregates remain for
// audit, so their presence is not a cleanup failure or a substitute for proof.
class ManagedRuntimeCleanupReadbackError extends Error {
  readonly lastReadback: MonthlyRuntimeCleanupReadback | undefined;

  constructor(
    cause: unknown,
    lastReadback: MonthlyRuntimeCleanupReadback | undefined,
  ) {
    super(cause instanceof Error ? cause.message : String(cause), { cause });
    this.name = "ManagedRuntimeCleanupReadbackError";
    this.lastReadback = lastReadback;
  }
}

async function waitForManagedRuntimeCleanupReadback(args: {
  token: RuntimeBearer;
  apiBase: string;
  stackId: string;
  leaseId: string;
  timeoutMs?: number;
}): Promise<MonthlyRuntimeCleanupReadback> {
  const deadline = Date.now() + (args.timeoutMs ?? 120_000);
  let last: MonthlyRuntimeCleanupReadback | undefined;
  try {
    while (Date.now() < deadline) {
      last = await fetchRuntimeApi<MonthlyRuntimeCleanupReadback>(
        args.token,
        `/api/v1/monthly-runtimes/${encodeURIComponent(args.leaseId)}/cleanup-readback`,
        args.apiBase,
        deadline,
      );
      if (
        last?.lease_id === args.leaseId &&
        last.lease?.desired_terminal &&
        last.lease.observed_terminal &&
        last.server?.bound &&
        last.server.terminal &&
        last.provider_operation?.found &&
        last.provider_operation.terminal &&
        last.provider_operation.absence_evidence_ref?.trim() &&
        last.provider_operation.capacity_released
      ) {
        return last;
      }
      await delay(2_000);
    }
    throw new Error(
      `Timed out waiting for native managed cleanup stack=${args.stackId} lease=${args.leaseId}; last=${JSON.stringify(redactObject(last))}`,
    );
  } catch (error) {
    throw new ManagedRuntimeCleanupReadbackError(error, last);
  }
}

async function waitForManagedRuntimeInventoryEvidence(args: {
  token: RuntimeBearer;
  apiBase: string;
  stackId: string;
  stackName: string;
  leaseId: string;
  timeoutMs?: number;
}) {
  const deadline = Date.now() + (args.timeoutMs ?? 120_000);
  let lastWorkers: RuntimeWorkerRecord[] = [];
  let lastOperations: RuntimeStackOperationsPayload | undefined;
  let lastRegistry: RuntimeRegistryServicePayload | undefined;

  while (Date.now() < deadline) {
    lastWorkers = await fetchRuntimeApi<RuntimeWorkerRecord[]>(
      args.token,
      "/api/v1/workers",
      args.apiBase,
    );
    // The lease binds the node; which projection produced the row
    // (worker registry, canonical server) is not part of the contract.
    const worker = lastWorkers.find((item) => item.lease_id === args.leaseId);
    lastOperations = await fetchRuntimeApi<RuntimeStackOperationsPayload>(
      args.token,
      `/api/v1/stacks/${encodeURIComponent(args.stackId)}/operations`,
      args.apiBase,
    );
    lastRegistry = await fetchRuntimeApi<RuntimeRegistryServicePayload>(
      args.token,
      "/api/v1/registry/services",
      args.apiBase,
    );
    const server = (lastOperations.servers ?? []).find(
      (item) => item.lease_id === args.leaseId,
    );
    const operationServices = lastOperations.services ?? [];
    const registryServices = servicesForStack(
      lastRegistry.services ?? [],
      args.stackId,
      args.stackName,
    );
    const runningServices = numericRuntimeKpi(
      lastOperations.kpis,
      "running_services",
    );
    const serviceUrls = uniqueRuntimeServiceUrls([
      ...operationServices,
      ...registryServices,
    ]);
    if (
      worker &&
      server &&
      operationServices.length > 0 &&
      registryServices.length > 0 &&
      runningServices > 0 &&
      serviceUrls.length > 0
    ) {
      // Enrollment is proven earlier by the monthly-runtime status. Here the
      // node must be approved, connected and bound to this stack.
      expect(worker.approved).toBe(true);
      expect(worker.status).toBe("connected");
      expect(worker.runtime_lane).toBe("monthly-runtime");
      expect(server.assignment).toBe("stack");
      expect(server.approved).toBe(true);
      expect(server.status).toBe("connected");
      expect(operationServices.length).toBeGreaterThan(0);
      expect(registryServices.length).toBeGreaterThan(0);
      expect(runningServices).toBeGreaterThan(0);
      expect(serviceUrls.length).toBeGreaterThan(0);
      const evidence = {
        workers: {
          matched: worker,
          managed_runtime_rows: lastWorkers.filter(
            (item) => item.runtime_lane === "monthly-runtime",
          ),
        },
        stack_operations: {
          matched_server: server,
          readiness: lastOperations.readiness,
          kpis: lastOperations.kpis,
          services: operationServices,
        },
        service_registry: {
          services: registryServices,
        },
        service_urls: serviceUrls,
        release_contract: {
          stackkit: MANAGED_RUNTIME_STACKKIT,
          operation_services: operationServices.length,
          registry_services: registryServices.length,
          running_services: runningServices,
        },
      };
      await attachJson(
        "kombify-cloud-managed-runtime-inventory.json",
        redactObject(evidence),
      );
      return evidence;
    }
    await delay(2_000);
  }

  throw new Error(
    `Timed out waiting for managed-runtime inventory lease=${args.leaseId}; workers=${JSON.stringify(lastWorkers)} operations=${JSON.stringify(lastOperations)} registry=${JSON.stringify(lastRegistry)}`,
  );
}

async function runManagedServiceActionEvidence(args: {
  token: RuntimeBearer;
  apiBase: string;
  stackId: string;
  stackName: string;
  services: RuntimeStackOperationService[];
}) {
  const service = args.services.find(
    (candidate) =>
      Boolean(candidate.id) &&
      Number(candidate.inventory_revision ?? 0) > 0 &&
      candidate.allowed_actions?.includes("restart") &&
      candidate.allowed_actions.includes("logs"),
  );
  if (!service?.id || !service.inventory_revision) {
    // Decision (kombify-Techstack-j7fd.5, reversible): Techstack only mirrors
    // the capabilities StackKits declares in the inventory, so a Guard-managed
    // Cloud Kit that declares none cannot prove service Day-2. The lane records
    // that explicitly as not proven instead of failing every managed run; once
    // StackKits declares restart+logs the branch below proves them again.
    const notDeclared = {
      status: "not_declared" as const,
      reason:
        "no managed service declared both restart and logs; service Day-2 is not proven for this kit",
      services: args.services.map((candidate) => ({
        service_key: candidate.name,
        declared_actions: candidate.allowed_actions ?? [],
      })),
    };
    await attachJson("kombify-cloud-managed-service-actions.json", notDeclared);
    return notDeclared;
  }

  const invoke = async (
    action: "restart" | "logs",
    inventoryRevision: number,
    idempotencyKey: string,
  ) => {
    const response = await authorizedFetch(
      args.token,
      runtimeApiUrl(
        args.apiBase,
        `/api/v1/registry/services/${encodeURIComponent(service.id!)}/actions`,
      ),
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": idempotencyKey,
        },
        body: JSON.stringify({
          action,
          expected_inventory_revision: inventoryRevision,
          owner_approved: true,
          ...(action === "logs" ? { limit: 100 } : {}),
        }),
      },
    );
    const text = await response.text();
    const json = text ? JSON.parse(text) : {};
    if (response.status !== 202) {
      throw new Error(
        `Managed service ${action} failed with HTTP ${response.status}: ${text}`,
      );
    }
    return (json.data ?? json) as ManagedServiceActionResponse;
  };

  const restartKey = `runtime-e2e-${args.stackId}-${service.id}-restart`;
  const restart = await invoke(
    "restart",
    service.inventory_revision,
    restartKey,
  );
  const replay = await invoke(
    "restart",
    service.inventory_revision,
    restartKey,
  );
  expect(restart.job_id).toBeTruthy();
  expect(replay.job_id).toBe(restart.job_id);
  const restartJob = await waitForJobTerminal(
    args.token,
    String(restart.job_id),
    180_000,
    args.apiBase,
  );
  expect(restartJob.state).toBe("completed");

  const deadline = Date.now() + 120_000;
  let convergedService: RuntimeStackOperationService | undefined;
  while (Date.now() < deadline) {
    const registry = await fetchRuntimeApi<RuntimeRegistryServicePayload>(
      args.token,
      "/api/v1/registry/services",
      args.apiBase,
    );
    convergedService = servicesForStack(
      registry.services ?? [],
      args.stackId,
      args.stackName,
    ).find((candidate) => candidate.id === service.id);
    if (
      Number(convergedService?.inventory_revision ?? 0) >
      service.inventory_revision
    ) {
      break;
    }
    await delay(2_000);
  }
  if (
    !convergedService?.inventory_revision ||
    convergedService.inventory_revision <= service.inventory_revision
  ) {
    throw new Error(
      `Managed service restart completed without a newer inventory observation for ${service.id}`,
    );
  }

  const logsKey = `runtime-e2e-${args.stackId}-${service.id}-logs`;
  const logsAction = await invoke(
    "logs",
    convergedService.inventory_revision,
    logsKey,
  );
  expect(logsAction.job_id).toBeTruthy();
  const logsJob = await waitForJobTerminal(
    args.token,
    String(logsAction.job_id),
    180_000,
    args.apiBase,
  );
  expect(logsJob.state).toBe("completed");
  const logs = await fetchRuntimeApi<ManagedServiceLogsResponse>(
    args.token,
    `/api/v1/registry/services/${encodeURIComponent(service.id)}/logs?limit=100`,
    args.apiBase,
  );
  expect(logs.status).toBe("completed");
  expect(Array.isArray(logs.entries)).toBe(true);
  expect((logs.entries ?? []).length).toBeLessThanOrEqual(100);

  const evidence = {
    service_id: service.id,
    service_key: service.name,
    declared_actions: service.allowed_actions,
    initial_inventory_revision: service.inventory_revision,
    converged_inventory_revision: convergedService.inventory_revision,
    restart_job_id: restart.job_id,
    replay_job_id: replay.job_id,
    logs_job_id: logsAction.job_id,
    logs_status: logs.status,
    log_entry_count: logs.entries?.length ?? 0,
    next_cursor_present: Boolean(logs.next_cursor),
  };
  await attachJson("kombify-cloud-managed-service-actions.json", evidence);
  return evidence;
}

function servicesForStack(
  services: RuntimeStackOperationService[],
  stackId: string,
  stackName: string,
) {
  return services.filter(
    (service) =>
      service.stack_id === stackId ||
      service.stack_name === stackName ||
      service.stack_name === stackId,
  );
}

function numericRuntimeKpi(
  kpis: Record<string, unknown> | undefined,
  key: string,
) {
  const value = Number(kpis?.[key] ?? 0);
  return Number.isFinite(value) ? value : 0;
}

function runtimeServiceLabel(service: RuntimeStackOperationService) {
  return (
    service.display_name ||
    service.application_name ||
    service.name ||
    service.id ||
    "service"
  );
}

function uniqueRuntimeServiceUrls(services: RuntimeStackOperationService[]) {
  return Array.from(
    new Set(
      services
        .map((service) => String(service.url ?? "").trim())
        .filter((url) => url.length > 0),
    ),
  );
}

async function waitForProvisionJob(
  token: RuntimeBearer,
  scenario: RuntimeScenario,
  stack: { stack_id: string; job_id?: string },
  timeoutMs = 600_000,
  apiBase = API_BASE,
  onJob?: (job: Record<string, unknown>) => void,
) {
  if (!stack.job_id) {
    throw new Error(`${scenario} stack creation did not return a job_id`);
  }
  const job = await waitForJobTerminal(
    token,
    stack.job_id,
    timeoutMs,
    apiBase,
    onJob ? { onJob } : undefined,
  );
  await attachJson(`${scenario}-provision-job.json`, redactObject(job));
  if (job.state !== "completed") {
    throw new Error(
      `${scenario} provision job ${stack.job_id} ended with ${String(job.state)}: ${runtimeJobFailureSummary(job)}`,
    );
  }
  return job;
}

function isTerminalJobState(state: unknown) {
  return ["completed", "failed", "canceled", "cancelled"].includes(
    String(state ?? "")
      .trim()
      .toLowerCase(),
  );
}

function runtimeJobFailureSummary(job: Record<string, unknown>) {
  const fields = [
    ["current_step", job.current_step ?? job.step],
    ["message", job.message],
    ["error", job.error],
    ["error_message", job.error_message],
    ["error_details", job.error_details],
  ]
    .filter(([, value]) => String(value ?? "").trim() !== "")
    .map(([key, value]) => `${key}=${JSON.stringify(String(value))}`);
  if (fields.length === 0) return JSON.stringify(redactObject(job));
  return redactText(fields.join("; "));
}

async function waitForWorkerByHostname(
  token: string,
  hostname: string,
  timeoutMs = 90_000,
): Promise<RuntimeWorkerRecord> {
  const deadline = Date.now() + timeoutMs;
  let lastWorkers: RuntimeWorkerRecord[] = [];
  while (Date.now() < deadline) {
    lastWorkers = (await listWorkersViaApi(
      token,
      API_BASE,
    )) as RuntimeWorkerRecord[];
    const worker = lastWorkers.find((item) => item.hostname === hostname);
    if (worker?.id) return worker;
    await delay(2_000);
  }
  throw new Error(
    `Timed out waiting for worker hostname=${hostname}; last=${JSON.stringify(lastWorkers)}`,
  );
}

async function waitForApprovedWorker(
  token: string,
  workerId: string,
  timeoutMs = 60_000,
): Promise<RuntimeWorkerRecord> {
  const deadline = Date.now() + timeoutMs;
  let last: RuntimeWorkerRecord | undefined;
  while (Date.now() < deadline) {
    const workers = (await listWorkersViaApi(
      token,
      API_BASE,
    )) as RuntimeWorkerRecord[];
    last = workers.find((item) => item.id === workerId);
    if (last?.approved === true) return last;
    await delay(2_000);
  }
  throw new Error(
    `Timed out waiting for approved worker ${workerId}; last=${JSON.stringify(last)}`,
  );
}

async function registerWorkerThroughInstallScript(
  token: string,
  scenario: Extract<RuntimeScenario, "install-command" | "connect-remote">,
  resource: ProvisionedDropletResource,
) {
  const expectedHostname = (
    await runSSH(
      resource.ip,
      resource.keyPair.privateKeyPath,
      "hostname",
      30_000,
    )
  ).stdout.trim();
  const pairing = await createPairingTokenViaApi(
    token,
    API_BASE,
    `runtime-e2e-${scenario}-${resource.droplet.id}`,
    60,
  );
  const installCommand = [
    `curl -fsSL ${shellQuote(`${API_BASE}/install.sh`)} |`,
    `KOMBI_SERVER=${shellQuote(API_BASE)}`,
    `KOMBI_TOKEN=${shellQuote(pairing.token)}`,
    "sh",
  ].join(" ");
  const result = await runSSH(
    resource.ip,
    resource.keyPair.privateKeyPath,
    installCommand,
    180_000,
  );
  await attachText(
    `${scenario}-install-command.log`,
    redactText(`${result.stdout}\n${result.stderr}`),
  );
  const worker = await waitForWorkerByHostname(token, expectedHostname);
  await approveWorkerViaApi(token, worker.id, API_BASE);
  const approved = await waitForApprovedWorker(token, worker.id);
  expect(approved.hostname).toBe(expectedHostname);
  expect(approved.approved).toBe(true);
  return {
    worker: approved,
    install_command_redacted: redactText(installCommand),
  };
}

// Hosted Techstack never serves fleet-wide diagnostics to a customer: the
// status and health reads answer one documented tenant-isolation refusal
// (security: isolate hosted monitoring metrics by tenant, #2213). A customer
// reads their nodes through the stack operations, the server aggregate and the
// monitoring pages instead.
async function expectHostedMonitorDiagnosticsRefused(
  token: RuntimeBearer,
  read: "status" | "health",
  apiBase = API_BASE,
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(apiBase, `/api/v1/monitor/${read}`),
  );
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  expect(response.status, text).toBe(403);
  expect(json.error?.details?.error_code).toBe(
    "tenant_diagnostics_unavailable",
  );
  return {
    http_status: response.status,
    error_code: json.error.details.error_code,
    reason_code: json.error.details.reason_code,
  };
}

async function getMonitorAlertRulesViaApi(
  token: RuntimeBearer,
  apiBase = API_BASE,
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(apiBase, "/api/v1/monitor/alerts/rules"),
  );
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Read monitor alert rules failed with HTTP ${response.status}: ${text}`,
    );
  }
  return (json.data ?? json) as Record<string, unknown>[];
}

async function validateMonitoringUi(
  page: Page,
  token: RuntimeBearer,
  scenario: RuntimeScenario,
  opts: { minConnectedAgents?: number; apiBase?: string } = {},
) {
  const apiBase = opts.apiBase ?? API_BASE;
  const status = await expectHostedMonitorDiagnosticsRefused(
    token,
    "status",
    apiBase,
  );
  await attachJson(`${scenario}-monitor-status.json`, redactObject(status));
  const health = await expectHostedMonitorDiagnosticsRefused(
    token,
    "health",
    apiBase,
  );
  await attachJson(`${scenario}-monitor-health.json`, redactObject(health));
  const alertRules = await getMonitorAlertRulesViaApi(token, apiBase);
  await attachJson(
    `${scenario}-monitor-alert-rules.json`,
    redactObject(alertRules),
  );

  await page.goto("/monitoring", { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("monitoring-page")).toBeVisible();

  // The start page is the availability surface: current state per node, then
  // the history derived from the transition timeline. It deliberately depends
  // on no metrics backend, so it must render even when the TSDB is empty.
  await expect(page.getByTestId("monitoring-section-right-now")).toBeVisible();
  await expect(page.getByTestId("monitoring-node-summary").first()).toBeVisible(
    {
      timeout: 60_000,
    },
  );
  // All three axes are read together and never collapsed.
  await expect(
    page
      .getByTestId("monitoring-node-summary")
      .first()
      .getByTestId("monitoring-node-axis"),
  ).toHaveCount(3);
  await expect(
    page.getByTestId("monitoring-section-availability"),
  ).toBeVisible();
  await expect(page.getByTestId("monitoring-availability-ratio")).toBeVisible();
  await expect(page.getByTestId("monitoring-section-ribbons")).toBeVisible();
  await expect(page.getByTestId("monitoring-section-episodes")).toBeVisible();

  // The metrics pipeline proof moved to the per-server detail, where the
  // panels are driven by the same range-query endpoint the OTLP baseline used
  // to report on. Asserting the rendered panels proves the query path
  // end-to-end rather than a summary string about it.
  // Navigate from the inventory card rather than a ribbon row: the ribbon
  // comes from the availability projection, which legitimately omits a node
  // with no recorded transition in the window.
  const firstNode = page.getByTestId("monitoring-node-summary").first();
  await expect(firstNode).toBeVisible({ timeout: 60_000 });
  const firstServerId = await firstNode.getAttribute("data-server-id");
  expect(firstServerId, "the first node card names its server").toBeTruthy();
  await firstNode.click();
  // The card opens the server detail; its "History & metrics" link opens the
  // per-server monitoring page (navigation pass, #3521).
  await page.locator(`a[href="/monitoring/${firstServerId}"]`).first().click();
  await expect(page.getByTestId("server-monitoring-page")).toBeVisible({
    timeout: 60_000,
  });
  await expect(page.getByTestId("server-monitoring-axis")).toHaveCount(3);
  await expect(
    page.getByTestId("server-monitoring-panel").first(),
  ).toBeVisible();
  // A node whose window carries no recorded transition legitimately has no
  // ratio; the page must then say so rather than render neither.
  await expect(page.getByTestId("server-monitoring-history")).toBeVisible();
  // Every alert rule the API reports is in scope for at least one node, so the
  // detail page must render the same population the API returned.
  if (alertRules.length > 0) {
    await expect(page.getByTestId("server-monitoring-alerts")).toBeVisible();
  }
  await attachScreenshot(page, `runtime-${scenario}-server-monitoring.png`);
  await page.goto("/monitoring", { waitUntil: "domcontentloaded" });

  if ((opts.minConnectedAgents ?? 0) > 0) {
    await expect
      .poll(
        () =>
          page
            .locator(
              '[data-testid="monitoring-node-summary"][data-connection="connected"]',
            )
            .count(),
        {
          message:
            "fresh canonical Guard connections must be reflected in the monitoring UI",
          timeout: 60_000,
        },
      )
      .toBeGreaterThanOrEqual(opts.minConnectedAgents ?? 0);
  }

  await attachScreenshot(page, `runtime-${scenario}-monitoring.png`);
}

async function numericMetricValue(page: Page, testId: string) {
  const raw =
    (await page
      .getByTestId(testId)
      .getByTestId("metric-card-value")
      .textContent()) ?? "";
  const numeric = Number(raw.replace(/[^0-9.-]/g, ""));
  return Number.isFinite(numeric) ? numeric : 0;
}

async function validateUserOwnedPostSetupUi(args: {
  page: Page;
  token: string;
  scenario: Extract<RuntimeScenario, "install-command" | "connect-remote">;
  stackName: string;
  worker: RuntimeWorkerRecord;
}) {
  await args.page.goto("/stacks", { waitUntil: "domcontentloaded" });
  await expect(args.page.getByTestId("stacks-dashboard")).toBeVisible();
  await expect(
    args.page.getByText(args.stackName, { exact: false }),
  ).toBeVisible();
  await expect(args.page.getByTestId("worker-management-card")).toBeVisible({
    timeout: 60_000,
  });
  await expect(args.page.getByTestId("worker-connected-count")).toContainText(
    /[1-9]/,
    { timeout: 60_000 },
  );
  await expect(
    args.page
      .getByTestId("approved-worker-row")
      .filter({ hasText: args.worker.hostname }),
  ).toBeVisible({ timeout: 60_000 });
  await expect(args.page.getByTestId("deploy-homelab-button")).toBeEnabled({
    timeout: 60_000,
  });
  await attachScreenshot(args.page, `runtime-${args.scenario}-stacks.png`);
  await validateMonitoringUi(args.page, args.token, args.scenario, {
    minConnectedAgents: 1,
  });
}

async function validateManagedRuntimePostSetupUi(args: {
  page: Page;
  token: RuntimeBearer;
  apiBase: string;
  stackId: string;
  stackName: string;
  leaseId: string;
  inventory: Awaited<ReturnType<typeof waitForManagedRuntimeInventoryEvidence>>;
}) {
  const serviceLabels =
    args.inventory.service_registry.services.map(runtimeServiceLabel);
  const primaryServiceLabel = serviceLabels[0];

  await args.page.goto("/stacks", { waitUntil: "domcontentloaded" });
  await expect(args.page.getByTestId("stacks-dashboard")).toBeVisible();
  await expect(
    args.page.getByText(args.stackName, { exact: false }),
  ).toBeVisible();
  await attachScreenshot(args.page, "runtime-kombify-cloud-stacks.png");

  await args.page.goto(`/stacks/${args.stackId}`, {
    waitUntil: "domcontentloaded",
  });
  await expect(args.page.getByTestId("stack-operations-dashboard")).toBeVisible(
    { timeout: 60_000 },
  );
  await expect
    .poll(() => numericMetricValue(args.page, "stack-running-services-kpi"), {
      message: "stack operations must expose running services",
      timeout: 60_000,
    })
    .toBeGreaterThan(0);
  await expect(args.page.getByTestId("monthly-runtime-card")).toBeVisible({
    timeout: 60_000,
  });
  await expect(args.page.getByTestId("monthly-runtime-card")).toContainText(
    args.leaseId,
  );
  await expect(
    args.page.getByTestId("monthly-runtime-enrollment"),
  ).toContainText(/enrolled/i, { timeout: 60_000 });
  await expect(args.page.getByTestId("monthly-runtime-state")).toBeVisible();
  // The power controls follow the server's state: Start is offered while it
  // is stopped, Stop while it is running (both while the state is unknown), so
  // at least one of them is always available; the SSH switch and the
  // connection details stay available.
  const startEnabled = await args.page
    .getByTestId("monthly-runtime-action-start")
    .isEnabled();
  const stopEnabled = await args.page
    .getByTestId("monthly-runtime-action-stop")
    .isEnabled();
  expect(startEnabled || stopEnabled).toBe(true);
  await expect(
    args.page.getByTestId("monthly-runtime-ssh-switch"),
  ).toBeEnabled();
  await expect(
    args.page.getByTestId("monthly-runtime-action-ssh"),
  ).toBeEnabled();
  await attachScreenshot(args.page, "runtime-kombify-cloud-management.png");
  await args.page.goto(
    `/services?kit_deployment_id=${encodeURIComponent(args.stackId)}`,
    {
      waitUntil: "domcontentloaded",
    },
  );
  await expect(args.page.getByTestId("services-page")).toBeVisible({
    timeout: 60_000,
  });
  await expect(
    args.page.getByTestId("runtime-service-card").first(),
  ).toBeVisible({ timeout: 60_000 });
  if (primaryServiceLabel) {
    await expect(
      args.page
        .getByTestId("runtime-service-card")
        .filter({ hasText: primaryServiceLabel })
        .first(),
    ).toBeVisible({ timeout: 60_000 });
  }
  await attachScreenshot(args.page, "runtime-kombify-cloud-services.png");
  await validateMonitoringUi(args.page, args.token, "kombify-cloud", {
    apiBase: args.apiBase,
  });
}

async function destroyRuntimeStackViaApi(
  token: RuntimeBearer,
  stackId?: string,
  apiBase = API_BASE,
) {
  if (!stackId) return;
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(
      apiBase,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/destroy`,
    ),
    {
      method: "POST",
      signal: AbortSignal.timeout(15_000),
    },
  );
  if (response.status === 404) return;
  const text = await response.text();
  if (!response.ok) {
    throw new Error(
      `Destroy runtime stack ${stackId} failed with HTTP ${response.status}: ${text}`,
    );
  }
  const json = text ? JSON.parse(text) : {};
  const data = json.data ?? json;
  const jobId = String(data.job_id ?? "").trim();
  if (!jobId) {
    throw new Error(
      `Destroy runtime stack ${stackId} was accepted without a job_id`,
    );
  }
  const job = await waitForJobTerminal(token, jobId, 300_000, apiBase);
  await attachJson(
    `destroy-${stackId}-job.json`,
    redactObject({
      ...job,
      stack_id: stackId,
      terminal_provider_readback_required: true,
    }),
  );
  if (job.state !== "completed") {
    throw new Error(
      `Destroy runtime stack ${stackId} job ${jobId} ended with ${String(job.state)}: ${runtimeJobFailureSummary(job)}`,
    );
  }
  return job;
}

async function createSSHKeyPair(label: string) {
  const dir = await mkdtemp(path.join(tmpdir(), "techstack-runtime-e2e-"));
  const privateKeyPath = path.join(dir, "id_ed25519");
  await execFileAsync("ssh-keygen", [
    "-t",
    "ed25519",
    "-N",
    "",
    "-C",
    label,
    "-f",
    privateKeyPath,
  ]);
  await chmod(privateKeyPath, 0o600).catch(() => undefined);
  return {
    dir,
    privateKeyPath,
    publicKey: await readFile(`${privateKeyPath}.pub`, "utf8"),
  };
}

async function provisionDroplet(
  client: DigitalOceanRuntimeClient,
  scenario: RuntimeScenario,
) {
  const runId = runtimeRunId();
  const keyPair = await createSSHKeyPair(`techstack-e2e-${scenario}-${runId}`);
  let sshKey: DigitalOceanSSHKey | undefined;
  let droplet: DigitalOceanDroplet | undefined;
  try {
    await client.ensureTag(DO_TAG);
    sshKey = await client.createSSHKey(
      `techstack-e2e-${scenario}-${runId}`,
      keyPair.publicKey,
    );
    droplet = await client.createDroplet({
      name: `techstack-e2e-${scenario}-${runId}`.slice(0, 63),
      scenario,
      sshKeyId: sshKey.id,
    });
    const ready = await client.waitForPublicIPv4(droplet.id);
    await waitForSSH(ready.ip, keyPair.privateKeyPath);
    await runSSH(
      ready.ip,
      keyPair.privateKeyPath,
      [
        "cloud-init status --wait >/dev/null 2>&1 || true",
        "command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y curl ca-certificates)",
        "echo ready",
      ].join(" && "),
    );
    return {
      droplet: ready.droplet,
      ip: ready.ip,
      sshKey,
      keyPair,
    };
  } catch (err) {
    await client.deleteDroplet(droplet?.id).catch(() => undefined);
    await client.deleteSSHKey(sshKey?.id).catch(() => undefined);
    await rm(keyPair.dir, { recursive: true, force: true }).catch(
      () => undefined,
    );
    throw err;
  }
}

function sshArgs(host: string, privateKeyPath: string, remoteCommand?: string) {
  const args = [
    "-i",
    privateKeyPath,
    "-o",
    "BatchMode=yes",
    "-o",
    "StrictHostKeyChecking=no",
    "-o",
    `UserKnownHostsFile=${SSH_USER_KNOWN_HOSTS}`,
    "-o",
    "ConnectTimeout=10",
    `root@${host}`,
  ];
  if (remoteCommand) args.push(remoteCommand);
  return args;
}

async function waitForSSH(
  host: string,
  privateKeyPath: string,
  timeoutMs = 180_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last = "";
  while (Date.now() < deadline) {
    try {
      const result = await execFileAsync(
        "ssh",
        sshArgs(host, privateKeyPath, "echo ssh-ready"),
        {
          timeout: 15_000,
        },
      );
      if (result.stdout.includes("ssh-ready")) return;
    } catch (err) {
      last = String(
        (err as { stderr?: string; message?: string }).stderr ??
          (err as Error).message,
      );
    }
    await delay(5_000);
  }
  throw new Error(`Timed out waiting for SSH on ${host}: ${last}`);
}

async function runSSH(
  host: string,
  privateKeyPath: string,
  command: string,
  timeout = 120_000,
) {
  try {
    return await execFileAsync("ssh", sshArgs(host, privateKeyPath, command), {
      timeout,
      maxBuffer: 1024 * 1024,
    });
  } catch (err) {
    const detail = err as {
      stdout?: string;
      stderr?: string;
      message?: string;
    };
    throw new Error(
      `SSH command failed: ${detail.message ?? ""}\n${detail.stdout ?? ""}\n${detail.stderr ?? ""}`,
    );
  }
}

async function cleanupDropletResource(
  client: DigitalOceanRuntimeClient,
  resource?: Awaited<ReturnType<typeof provisionDroplet>>,
) {
  if (!resource) return;
  await client.deleteDroplet(resource.droplet.id).catch(() => undefined);
  await client.deleteSSHKey(resource.sshKey.id).catch(() => undefined);
  await rm(resource.keyPair.dir, { recursive: true, force: true }).catch(
    () => undefined,
  );
}

// The layout journey's owner servers come from one supplier per run
// (TECHSTACK_E2E_OWNER_SERVER_PROVIDER): DigitalOcean droplets or centron
// ccloud VPS. Either way the test plays the owner bringing their own machine;
// Techstack meets it only through the install command.
interface OwnerServer {
  ip: string;
  keyPair: Awaited<ReturnType<typeof createSSHKeyPair>>;
  release: () => Promise<void>;
  // Reads the supplier: true once no machine of this server remains.
  isAbsent?: () => Promise<boolean>;
}

function ownerServerSupplierFromEnv(): {
  provider: string;
  provision: (index: number) => Promise<OwnerServer>;
} {
  const provider = (
    process.env.TECHSTACK_E2E_OWNER_SERVER_PROVIDER ?? "digitalocean"
  )
    .trim()
    .toLowerCase();
  if (provider === "centron") {
    const client = centronClientFromEnv();
    return {
      provider,
      provision: (index) => provisionCentronServer(client, index),
    };
  }
  if (provider !== "digitalocean") {
    throw new Error(`Unknown owner server provider: ${provider}`);
  }
  const client = digitalOceanClientFromEnv();
  return {
    provider,
    provision: async () => {
      const resource = await provisionDroplet(client, "install-command");
      return {
        ip: resource.ip,
        keyPair: resource.keyPair,
        release: () => cleanupDropletResource(client, resource),
      };
    },
  };
}

// centron ccloud, as Techstack's verified adapter uses it
// (internal/provideradapters/centron): ccloud ignores the requested hostname,
// so a server is found again only by its unique tag (at most 20 characters),
// and it is deleted by the hostname ccloud assigned.
const CENTRON_API = "https://ccenter.centron.de/api/v1";
const CENTRON_TAG = "techstack-e2e";
// 4 vCPU / 8 GB per node: the 4 GB host floor plus the use cases.
const CENTRON_SIZE = { cores: 4, memoryGB: 8, diskGB: 80 };

interface CentronServer {
  hostname: string;
  building: boolean;
  deleting: boolean;
  ip: string;
}

class CentronRuntimeClient {
  private token = "";
  private tokenTill = 0;

  constructor(
    private readonly clientId: string,
    private readonly clientSecret: string,
    readonly pool: string,
    readonly image: string,
  ) {}

  private async bearer() {
    if (this.token && Date.now() < this.tokenTill - 60_000) return this.token;
    const response = await fetch(`${CENTRON_API}/oauth/token`, {
      method: "POST",
      headers: {
        accept: "application/json",
        "content-type": "application/x-www-form-urlencoded",
      },
      body: new URLSearchParams({
        grant_type: "client_credentials",
        client_id: this.clientId,
        client_secret: this.clientSecret,
      }),
    });
    const body = (await response.json().catch(() => ({}))) as {
      access_token?: string;
      expires_in?: number;
    };
    if (!response.ok || !body.access_token) {
      throw new Error(
        `centron token request failed with HTTP ${response.status}`,
      );
    }
    this.token = body.access_token;
    this.tokenTill = Date.now() + Math.max(120, body.expires_in ?? 600) * 1000;
    return this.token;
  }

  private async request(method: string, endpoint: string, body?: unknown) {
    const response = await fetch(`${CENTRON_API}${endpoint}`, {
      method,
      headers: {
        Authorization: `Bearer ${await this.bearer()}`,
        accept: "application/json",
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    let json: any = {};
    try {
      json = text ? JSON.parse(text) : {};
    } catch {
      json = {};
    }
    return { status: response.status, json };
  }

  async createServer(tag: string, publicKey: string) {
    const { status } = await this.request("POST", "/ccloud/servers", {
      pool: this.pool,
      hostname: tag,
      cores: CENTRON_SIZE.cores,
      memory: CENTRON_SIZE.memoryGB,
      disks: [CENTRON_SIZE.diskGB],
      image: this.image,
      sshkey: publicKey.trim(),
      type: "unmanaged",
      tags: [CENTRON_TAG, tag],
    });
    if (status < 200 || status >= 300) {
      throw new Error(`centron create for ${tag} failed with HTTP ${status}`);
    }
  }

  async serversByTag(tag: string): Promise<CentronServer[]> {
    const { status, json } = await this.request(
      "GET",
      `/ccloud/servers?tag=${encodeURIComponent(tag)}`,
    );
    if (status === 404) return [];
    if (status < 200 || status >= 300) {
      throw new Error(
        `centron server read for ${tag} failed with HTTP ${status}`,
      );
    }
    return ((json.data ?? []) as Array<Record<string, any>>)
      .filter((server) => (server.tags ?? []).includes(tag))
      .map((server) => ({
        hostname: String(server.hostname ?? "").trim(),
        building: Boolean(server.build_in_progress),
        deleting: Boolean(server.deletion_in_progress),
        ip: centronPublicIPv4(server),
      }));
  }

  async deleteServer(hostname: string) {
    const { status } = await this.request(
      "DELETE",
      `/ccloud/servers/${encodeURIComponent(hostname)}`,
      { hostname },
    );
    if (status !== 404 && (status < 200 || status >= 300)) {
      throw new Error(
        `centron delete of ${hostname} failed with HTTP ${status}`,
      );
    }
  }
}

function centronPublicIPv4(server: Record<string, any>) {
  for (const adapter of server.network_adapters ?? []) {
    for (const address of adapter.ip_addresses ?? []) {
      const ip = String(address.ip ?? "").trim();
      const octets = ip.split(".").map(Number);
      if (
        octets.length !== 4 ||
        octets.some((octet) => !(octet >= 0 && octet <= 255))
      )
        continue;
      const [a, b] = octets;
      const privateRange =
        a === 10 ||
        a === 127 ||
        (a === 172 && b >= 16 && b <= 31) ||
        (a === 192 && b === 168) ||
        (a === 100 && b >= 64 && b <= 127) ||
        (a === 169 && b === 254);
      if (!privateRange) return ip;
    }
  }
  return "";
}

function centronClientFromEnv() {
  const clientId = (process.env.KOMBISIM_CENTRON_CLIENT_ID ?? "").trim();
  const clientSecret = (
    process.env.KOMBISIM_CENTRON_CLIENT_SECRET ?? ""
  ).trim();
  const pool = (process.env.KOMBISIM_CENTRON_POOL ?? "").trim();
  if (!clientId || !clientSecret || !pool) {
    throw new Error(
      "Missing centron credentials. Configure KOMBISIM_CENTRON_CLIENT_ID, KOMBISIM_CENTRON_CLIENT_SECRET and KOMBISIM_CENTRON_POOL.",
    );
  }
  return new CentronRuntimeClient(
    clientId,
    clientSecret,
    pool,
    (process.env.TECHSTACK_E2E_CENTRON_IMAGE ?? "ubuntu-24").trim(),
  );
}

// The lane passes its run tag (kte2e-<run id>); each server adds -<index>.
function centronServerTag(index: number) {
  if (
    process.env.GITHUB_ACTIONS === "true" &&
    !process.env.TECHSTACK_E2E_CENTRON_RUN_TAG
  ) {
    throw new Error(
      "TECHSTACK_E2E_CENTRON_RUN_TAG is required in CI so the lane's cleanup finds these servers",
    );
  }
  const prefix = (
    process.env.TECHSTACK_E2E_CENTRON_RUN_TAG ??
    `kte2e-${Date.now().toString(36)}`
  ).trim();
  const tag = `${prefix}-${index}`;
  if (!/^[a-z0-9][a-z0-9-]{3,19}$/.test(tag)) {
    throw new Error(
      `centron tag ${tag} is not a ccloud tag of at most 20 characters`,
    );
  }
  return tag;
}

async function deleteCentronServers(client: CentronRuntimeClient, tag: string) {
  // ccloud creates asynchronously, so absence counts after two empty reads;
  // the window lane's cleanup step is the authoritative readback.
  const deadline = Date.now() + 180_000;
  let empty = 0;
  for (;;) {
    let servers: CentronServer[] | undefined;
    try {
      servers = await client.serversByTag(tag);
    } catch {
      servers = undefined;
    }
    if (servers?.length === 0) {
      empty += 1;
      if (empty >= 2) return;
    } else {
      empty = 0;
      for (const server of (servers ?? []).filter(
        (item) => item.hostname && !item.deleting,
      )) {
        await client.deleteServer(server.hostname).catch(() => undefined);
      }
    }
    if (Date.now() > deadline) {
      throw new Error(`centron servers tagged ${tag} remain after 3 minutes`);
    }
    await delay(20_000);
  }
}

async function provisionCentronServer(
  client: CentronRuntimeClient,
  index: number,
): Promise<OwnerServer> {
  const tag = centronServerTag(index);
  const keyPair = await createSSHKeyPair(`techstack-e2e-${tag}`);
  const release = async () => {
    await deleteCentronServers(client, tag).catch(() => undefined);
    await rm(keyPair.dir, { recursive: true, force: true }).catch(
      () => undefined,
    );
  };
  try {
    await client.createServer(tag, keyPair.publicKey);
    const deadline = Date.now() + 1_200_000;
    let server: CentronServer | undefined;
    while (!server) {
      const found = await client.serversByTag(tag);
      if (found.length > 1) {
        throw new Error(`centron created ${found.length} servers for ${tag}`);
      }
      if (found.length === 1 && !found[0].building && found[0].ip) {
        server = found[0];
        break;
      }
      if (Date.now() > deadline) {
        throw new Error(`centron server ${tag} was not ready after 20 minutes`);
      }
      await delay(15_000);
    }
    logRuntimeE2E("centron owner server ready", {
      tag,
      hostname: server.hostname,
    });
    await waitForSSH(server.ip, keyPair.privateKeyPath, 600_000);
    await runSSH(
      server.ip,
      keyPair.privateKeyPath,
      [
        "cloud-init status --wait >/dev/null 2>&1 || true",
        "command -v curl >/dev/null 2>&1 || (apt-get update && apt-get install -y curl ca-certificates)",
        "echo ready",
      ].join(" && "),
    );
    return {
      ip: server.ip,
      keyPair,
      release,
      isAbsent: async () => (await client.serversByTag(tag)).length === 0,
    };
  } catch (err) {
    await release();
    throw err;
  }
}

function digitalOceanClientFromEnv() {
  const token =
    process.env.DIGITALOCEAN_TOKEN ??
    process.env.DO_TOKEN ??
    process.env.KOMBISIM_DIGITALOCEAN_API_TOKEN;
  if (!token) {
    throw new Error(
      "Missing DigitalOcean token. Configure DIGITALOCEAN_TOKEN, DO_TOKEN, or KOMBISIM_DIGITALOCEAN_API_TOKEN before running runtime E2E.",
    );
  }
  return new DigitalOceanRuntimeClient(token);
}

function publicIPv4(droplet: DigitalOceanDroplet) {
  return droplet.networks?.v4?.find((network) => network.type === "public")
    ?.ip_address;
}

function runtimeRunId() {
  return new Date()
    .toISOString()
    .replace(/[^0-9]/g, "")
    .slice(0, 14);
}

function boundedPositiveInteger(
  value: string | undefined,
  fallback: number,
  max: number,
) {
  const parsed = Number.parseInt(String(value ?? ""), 10);
  if (!Number.isFinite(parsed) || parsed <= 0) return fallback;
  return Math.min(parsed, max);
}

function delay(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function shellQuote(value: string) {
  return `'${value.replace(/'/g, "'\"'\"'")}'`;
}

function firstProductURL(...values: Array<string | undefined>) {
  for (const value of values) {
    const normalized = normalizeURL(value);
    if (normalized && isLiveHTTPSURL(normalized)) return normalized;
  }
  return "";
}

function normalizeURL(value: string | undefined) {
  const raw = value?.trim();
  if (!raw) return "";
  try {
    const parsed = new URL(raw);
    parsed.hash = "";
    parsed.search = "";
    return parsed.toString().replace(/\/+$/g, "");
  } catch {
    return "";
  }
}

function requireLiveProductURL(label: string, value: string) {
  const normalized = normalizeURL(value);
  if (!normalized) {
    throw new Error(`${label} URL is required for SaaS Runtime E2E.`);
  }
  const parsed = new URL(normalized);
  if (parsed.protocol !== "https:" || isLoopbackURL(normalized)) {
    throw new Error(
      `${label} URL must be a real HTTPS product origin, got ${normalized}. Runtime E2E does not run against localhost/self-hosted targets.`,
    );
  }
  return normalized;
}

function isLiveHTTPSURL(value: string) {
  const parsed = new URL(value);
  return parsed.protocol === "https:" && !isLoopbackURL(value);
}

function isLoopbackURL(value: string) {
  const host = new URL(value).hostname.toLowerCase();
  return host === "localhost" || host === "127.0.0.1" || host === "::1";
}

function redactObject(value: unknown) {
  return scrubSensitiveValue(value);
}

function redactText(value: string) {
  return sanitizeSensitiveText(value);
}

function logRuntimeE2E(message: string, details: Record<string, unknown>) {
  console.log(
    `[runtime-e2e] ${message}: ${redactText(JSON.stringify(details))}`,
  );
}

// Playwright measures test timeouts from the test start. Each transition sets
// an absolute deadline for the current bounded phase; afterEach owns a fresh
// cleanup window even when the test itself times out.
const managedPhaseOrigins = new WeakMap<object, number>();
function managedObservationPhase(phase: string, budgetMs: number) {
  const info = test.info();
  const now = performance.now();
  const origin = managedPhaseOrigins.get(info) ?? now;
  managedPhaseOrigins.set(info, origin);
  const elapsed = now - origin;
  test.setTimeout(elapsed + budgetMs);
  logRuntimeE2E("phase started", {
    phase,
    budget_ms: budgetMs,
    deadline: new Date(Date.now() + budgetMs).toISOString(),
  });
}

function assertNoProviderSecrets(record: unknown) {
  const text = JSON.stringify(record).toLowerCase();
  for (const forbidden of [
    "digitalocean_token",
    "do_token",
    "provider_api_key",
    "client_secret",
    "private_key",
  ]) {
    expect(text).not.toContain(forbidden);
  }
}

const monthlyRuntimeLeaseProviders = [
  { id: "centron", label: "centron" },
  { id: "ionos", label: "ionos" },
] as const;

function selectedMonthlyRuntimeLeaseProviders() {
  const requested = (
    process.env.TECHSTACK_RUNTIME_E2E_PROVIDER_ID ?? "all"
  ).trim();
  if (!requested || requested === "all") return monthlyRuntimeLeaseProviders;
  const selected = monthlyRuntimeLeaseProviders.filter(
    (provider) => provider.id === requested || provider.label === requested,
  );
  if (selected.length === 0) {
    throw new Error(
      `Unsupported TECHSTACK_RUNTIME_E2E_PROVIDER_ID=${requested}. Expected all, centron, or ionos.`,
    );
  }
  return selected;
}

test.describe.serial("Runtime E2E scenarios", () => {
  test("managed-capacity-cleanup releases stale Runtime E2E leases", async ({
    page,
  }) => {
    test.skip(
      process.env.TECHSTACK_RUNTIME_E2E_MANAGED_CLEANUP_CONFIRM !==
        "managed-e2e-stacks",
      "Explicit managed Runtime E2E cleanup confirmation is required",
    );
    const providerId = process.env.TECHSTACK_RUNTIME_E2E_PROVIDER_ID;
    if (providerId !== "centron" && providerId !== "ionos") {
      throw new Error(
        "Managed Runtime E2E cleanup requires an exact centron or ionos provider ID",
      );
    }
    await authenticateRuntimeUser("cloud", page, RUNTIME_AUTH_OPTIONS);
    const api = await captureGatewayApiSession(page);
    const stacks = await getStackList(api.token, api.apiBase);
    const candidates = managedRuntimeE2ECleanupCandidates(stacks, providerId);
    const batchSize = boundedPositiveInteger(
      process.env.TECHSTACK_RUNTIME_E2E_MANAGED_CLEANUP_BATCH_SIZE,
      5,
      10,
    );
    const selected = candidates.slice(0, batchSize);
    logRuntimeE2E("managed capacity cleanup inventory", {
      provider_id: providerId,
      total_stack_count: stacks.length,
      exact_e2e_candidate_count: candidates.length,
      selected_count: selected.length,
    });
    await attachJson(`managed-capacity-cleanup-${providerId}-inventory.json`, {
      provider_id: providerId,
      total_stack_count: stacks.length,
      exact_e2e_candidate_count: candidates.length,
      selected_count: selected.length,
      candidates: candidates.map((stack) => ({
        id: stack.id,
        name: stack.name,
        status: stack.status,
        lease_id: stack.lease_id,
        provider_id: stack.provider_id,
      })),
    });
    const terminalReadbacks: Array<{
      stack_id: string;
      lease_id: string;
      destroy_job_id?: string;
      destroy_job_state?: string;
      native_cleanup_readback: MonthlyRuntimeCleanupReadback;
    }> = [];
    for (const stack of selected) {
      const stackId = String(stack.id ?? "").trim();
      if (!stackId)
        throw new Error("Managed cleanup candidate omitted stack id");
      const leaseId = String(stack.lease_id ?? "").trim();
      if (!leaseId) {
        throw new Error(
          `Managed cleanup candidate ${stackId} omitted its lease id; refusing an unbound cleanup readback`,
        );
      }
      const cleanupJob = await destroyRuntimeStackViaApi(
        api.token,
        stackId,
        api.apiBase,
      );
      const cleanupReadback = await waitForManagedRuntimeCleanupReadback({
        token: api.token,
        apiBase: api.apiBase,
        stackId,
        leaseId,
      });
      terminalReadbacks.push({
        stack_id: stackId,
        lease_id: leaseId,
        destroy_job_id: String(cleanupJob?.id ?? "") || undefined,
        destroy_job_state: String(cleanupJob?.state ?? "") || undefined,
        native_cleanup_readback: cleanupReadback,
      });
    }
    logRuntimeE2E("managed capacity cleanup batch completed", {
      provider_id: providerId,
      decommissioned_stack_count: selected.length,
      remaining_exact_e2e_candidates: Math.max(
        0,
        candidates.length - selected.length,
      ),
    });
    await attachJson(`managed-capacity-cleanup-${providerId}-result.json`, {
      provider_id: providerId,
      result: "PASS",
      decommissioned_stack_count: selected.length,
      remaining_exact_e2e_candidates: Math.max(
        0,
        candidates.length - selected.length,
      ),
      native_cleanup_readbacks: terminalReadbacks,
    });
  });

  test("install-command registers a real worker and validates post-setup UI", async ({
    page,
  }) => {
    const client = digitalOceanClientFromEnv();
    const auth = await authenticateRuntimeUser(
      "oneliner",
      page,
      RUNTIME_AUTH_OPTIONS,
    );
    let resource: Awaited<ReturnType<typeof provisionDroplet>> | undefined;
    let stackId = "";

    try {
      const stackName = `runtime-oneliner-${runtimeRunId()}`;
      const stack = await createRuntimeStack(
        auth.token,
        stackName,
        "install-command",
        {
          server_provisioning_mode: "install-command",
          server_connection_mode: "agent-oneliner",
          server_install_command_required: "true",
          server_mode: "user-owned",
          billing_mode: "local",
        },
        {
          nodes: [
            {
              id: "main",
              name: "main",
              role: "main",
              provider: "local",
              runtime: "docker",
            },
          ],
        },
      );
      stackId = stack.stack_id;
      await waitForProvisionJob(auth.token, "install-command", stack);
      resource = await provisionDroplet(client, "install-command");
      const registration = await registerWorkerThroughInstallScript(
        auth.token,
        "install-command",
        resource,
      );
      await validateUserOwnedPostSetupUi({
        page,
        token: auth.token,
        scenario: "install-command",
        stackName,
        worker: registration.worker,
      });
      await attachJson("install-command-artifacts.json", {
        stack_id: stack.stack_id,
        droplet_id: resource.droplet.id,
        worker_id: registration.worker.id,
        worker_hostname: registration.worker.hostname,
        install_command_redacted: registration.install_command_redacted,
        post_setup_validated: [
          "worker registry",
          "stack dashboard worker management",
          "deploy readiness",
          "monitoring dashboard",
        ],
      });
    } finally {
      await cleanupDropletResource(client, resource);
      if (stackId) {
        await destroyRuntimeStackViaApi(auth.token, stackId).catch(
          () => undefined,
        );
      }
    }
  });

  test("connect-remote persists SSH handoff, registers the server, and validates UI management", async ({
    page,
  }) => {
    const client = digitalOceanClientFromEnv();
    const auth = await authenticateRuntimeUser(
      "remote",
      page,
      RUNTIME_AUTH_OPTIONS,
    );
    let resource: Awaited<ReturnType<typeof provisionDroplet>> | undefined;
    let stackId = "";

    try {
      resource = await provisionDroplet(client, "connect-remote");
      const keyRef = `do-key-${resource.sshKey.fingerprint}`;
      const stackName = `runtime-remote-${runtimeRunId()}`;
      const stack = await createRuntimeStack(
        auth.token,
        stackName,
        "connect-remote",
        {
          server_provisioning_mode: "connect-remote",
          server_connection_mode: "remote-ssh",
          server_remote_host_present: "true",
          server_remote_user_present: "true",
          server_remote_auth_method: "ssh-key",
          server_remote_ssh_key_label: keyRef,
          server_remote_use_sudo: "true",
          server_mode: "user-owned",
          billing_mode: "local",
        },
        {
          ssh: {
            host: resource.ip,
            port: 22,
            user: "root",
            authMethod: "ssh-key",
            keyRef,
          },
          nodes: [
            {
              id: "main",
              name: "main",
              role: "main",
              provider: "local",
              runtime: "docker",
              ssh: {
                host: resource.ip,
                port: 22,
                user: "root",
                authMethod: "ssh-key",
                keyRef,
              },
            },
          ],
        },
      );
      stackId = stack.stack_id;
      await waitForProvisionJob(auth.token, "connect-remote", stack);
      const persisted = await waitForStack(
        auth.token,
        stack.stack_id,
        (item) => item.server_provisioning_mode === "connect-remote",
      );
      expect(persisted.server_connection_mode).toBe("remote-ssh");
      expect(persisted.server_remote_host_present).toBe(true);
      expect(persisted.server_remote_user_present).toBe(true);
      expect(persisted.server_remote_auth_method).toBe("ssh-key");
      expect(persisted.server_remote_credential_ref).toBe(keyRef);
      const probe = await runSSH(
        resource.ip,
        resource.keyPair.privateKeyPath,
        "printf '%s' \"$(hostname):$(id -u):$(command -v curl)\"",
      );
      expect(probe.stdout).toContain(":0:");
      const registration = await registerWorkerThroughInstallScript(
        auth.token,
        "connect-remote",
        resource,
      );
      await validateUserOwnedPostSetupUi({
        page,
        token: auth.token,
        scenario: "connect-remote",
        stackName,
        worker: registration.worker,
      });
      assertNoProviderSecrets(persisted);
      await attachJson("connect-remote-artifacts.json", {
        stack_id: stack.stack_id,
        droplet_id: resource.droplet.id,
        worker_id: registration.worker.id,
        worker_hostname: registration.worker.hostname,
        install_command_redacted: registration.install_command_redacted,
        ssh_probe: probe.stdout.trim(),
        runtime_fields: persisted,
        post_setup_validated: [
          "remote SSH handoff",
          "worker registry",
          "stack dashboard worker management",
          "deploy readiness",
          "monitoring dashboard",
        ],
      });
    } finally {
      await cleanupDropletResource(client, resource);
      if (stackId) {
        await destroyRuntimeStackViaApi(auth.token, stackId).catch(
          () => undefined,
        );
      }
    }
  });

  for (const leaseProvider of selectedMonthlyRuntimeLeaseProviders()) {
    test.describe(`managed ${leaseProvider.id}`, () => {
      // One keeper per lane: every managed owner request, including the
      // destroy in afterEach, reads a current Gateway bearer from it.
      let managedApi: GatewaySessionKeeper | undefined;
      let stackId = "";
      let stackName = "";
      let leaseId = "";
      let provisionJob: Record<string, unknown> | undefined;
      let advertisedStackkitsRelease = "";
      let laneStartedAt = "";
      let day2Evidence: ManagedDay2Evidence | undefined;
      const attemptId = randomUUID();
      test.afterEach(async () => {
        test.setTimeout(MANAGED_RUNTIME_BUDGETS.cleanupMs);
        logRuntimeE2E("phase started", {
          phase: "cleanup",
          budget_ms: MANAGED_RUNTIME_BUDGETS.cleanupMs,
          stack_id: stackId,
          lease_id: leaseId,
        });

        if (stackId && managedApi && MANAGED_RUNTIME_RETAIN) {
          await attachJson("kombify-cloud-retained.json", {
            provider_id: leaseProvider.id,
            stack_id: stackId,
            stack_name: stackName,
            lease_id: leaseId,
            cleanup_proof_status: "retained_persistent_test_environment",
          });
          return;
        }
        if (stackId && managedApi) {
          let cleanupJob: Record<string, unknown> | undefined;
          let cleanupReadback: MonthlyRuntimeCleanupReadback | undefined;
          let cleanupReadbackVerified = false;
          try {
            cleanupJob = await destroyRuntimeStackViaApi(
              managedApi,
              stackId,
              managedApi.apiBase,
            );
            cleanupReadback = leaseId
              ? await waitForManagedRuntimeCleanupReadback({
                  token: managedApi,
                  apiBase: managedApi.apiBase,
                  stackId,
                  leaseId,
                })
              : undefined;
            cleanupReadbackVerified = Boolean(cleanupReadback);
            await attachJson("kombify-cloud-cleanup.json", {
              provider_id: leaseProvider.id,
              stack_id: stackId,
              stack_name: stackName,
              lease_id: leaseId,
              destroy_job_id: cleanupJob?.id,
              destroy_job_state: cleanupJob?.state,
              native_cleanup_readback: cleanupReadback,
              cleanup_proof_status: cleanupReadback
                ? "native_cleanup_verified"
                : "lease_identity_unavailable_after_failed_setup",
            });
          } catch (error) {
            if (error instanceof ManagedRuntimeCleanupReadbackError) {
              cleanupReadback = error.lastReadback;
            }
            throw error;
          } finally {
            // A failed destroy or readback still produces an honest failed
            // receipt for the same attempt. Never lose cleanup evidence by
            // throwing before the artifact is written.
            const receipt = managedLifecycleReceipt({
              attemptId,
              stackId,
              leaseId,
              providerId: leaseProvider.id,
              advertisedStackkitsRelease,
              testSourceCommit:
                process.env.GITHUB_SHA ??
                (await testSourceCommit().catch(() => "")),
              startedAt: laneStartedAt,
              finishedAt: new Date().toISOString(),
              job: provisionJob,
              destroyJobId:
                typeof cleanupJob?.id === "string" ? cleanupJob.id : undefined,
              day2: day2Evidence,
              cleanupReadback: cleanupReadback as
                Record<string, unknown> | undefined,
              cleanupReadbackVerified,
            });
            await mkdir(ARTIFACTS_DIR, { recursive: true });
            await writeFile(
              path.join(
                ARTIFACTS_DIR,
                `managed-lifecycle-receipt-${leaseProvider.id}.json`,
              ),
              `${JSON.stringify(receipt, null, 2)}\n`,
            );
            await attachJson(
              `managed-lifecycle-receipt-${leaseProvider.id}.json`,
              receipt,
            );
          }
          if (leaseId) {
            expect(cleanupReadback?.lease_id).toBe(leaseId);
          }
        }
      });
      test(`kombify-cloud creates a monthly runtime lease via ${leaseProvider.id} and exposes runtime actions`, async ({
        page,
      }) => {
        managedObservationPhase("setup", MANAGED_RUNTIME_BUDGETS.setupMs);
        await authenticateRuntimeUser("cloud", page, RUNTIME_AUTH_OPTIONS);

        if (MANAGED_RUNTIME_RECOVERY_STACK_ID) {
          managedObservationPhase(
            "recovery cleanup",
            MANAGED_RUNTIME_BUDGETS.cleanupMs,
          );
          const requiredConfirmation = `destroy-managed-stack:${MANAGED_RUNTIME_RECOVERY_STACK_ID}`;
          if (MANAGED_RUNTIME_RECOVERY_CONFIRM !== requiredConfirmation) {
            throw new Error(
              `Managed runtime recovery requires TECHSTACK_RUNTIME_E2E_RECOVERY_CONFIRM=${requiredConfirmation}`,
            );
          }
          const recoveryApi = createGatewaySessionKeeper(
            page,
            await captureGatewayApiSession(page),
          );
          const recoveryStack = (
            await getStackList(recoveryApi, recoveryApi.apiBase)
          ).find(
            (stack) =>
              String(stack.id ?? "").trim() ===
              MANAGED_RUNTIME_RECOVERY_STACK_ID,
          );
          if (!recoveryStack) {
            await attachJson("kombify-cloud-recovery-cleanup.json", {
              provider_id: leaseProvider.id,
              stack_id: MANAGED_RUNTIME_RECOVERY_STACK_ID,
              recovery_status: "already_hidden_or_not_owner_visible",
              cleanup_claim: "not_made_without_the_original_lease_identity",
            });
            return;
          }
          const recoveryProviderId = String(
            recoveryStack.provider_id ?? recoveryStack.lease_provider ?? "",
          )
            .trim()
            .toLowerCase();
          if (!recoveryProviderId) {
            throw new Error(
              `Managed runtime recovery target ${MANAGED_RUNTIME_RECOVERY_STACK_ID} has no owner-visible provider id; refusing an unbound cleanup claim`,
            );
          }
          if (recoveryProviderId !== leaseProvider.id) {
            const requestedProvider = (
              process.env.TECHSTACK_RUNTIME_E2E_PROVIDER_ID ?? "all"
            ).trim();
            if (requestedProvider !== "all") {
              throw new Error(
                `Managed runtime recovery target ${MANAGED_RUNTIME_RECOVERY_STACK_ID} belongs to ${recoveryProviderId}, not requested provider ${leaseProvider.id}`,
              );
            }
            await attachJson("kombify-cloud-recovery-cleanup.json", {
              provider_id: leaseProvider.id,
              stack_id: MANAGED_RUNTIME_RECOVERY_STACK_ID,
              recovery_status: "skipped_provider_mismatch",
              recovery_target_provider_id: recoveryProviderId,
            });
            return;
          }
          const recoveryOperations =
            await fetchRuntimeApi<RuntimeStackOperationsPayload>(
              recoveryApi,
              `/api/v1/stacks/${encodeURIComponent(MANAGED_RUNTIME_RECOVERY_STACK_ID)}/operations`,
              recoveryApi.apiBase,
            );
          const recoveryJobs = await fetchRuntimeApi<RuntimeJobListPayload>(
            recoveryApi,
            `/api/v1/jobs?stack_id=${encodeURIComponent(MANAGED_RUNTIME_RECOVERY_STACK_ID)}&per_page=100`,
            recoveryApi.apiBase,
          );
          const recoveryLeaseId = exactManagedRecoveryLeaseId(
            recoveryStack,
            recoveryOperations,
          );
          if (!recoveryLeaseId) {
            throw new Error(
              `Managed runtime recovery target ${MANAGED_RUNTIME_RECOVERY_STACK_ID} has no single owner-visible foundation lease; refusing an unbound or ambiguous cleanup claim`,
            );
          }
          const abandonedJob = await abandonExactStaleManagedRuntimeJob(
            recoveryApi,
            recoveryApi.apiBase,
            MANAGED_RUNTIME_RECOVERY_STACK_ID,
            recoveryJobs,
          );
          const existingDestroy = (recoveryJobs.items ?? []).find((job) => {
            const type = String(job.type ?? "")
              .trim()
              .toLowerCase();
            const state = String(job.state ?? "")
              .trim()
              .toLowerCase();
            return (
              type === "destroy" &&
              ["pending", "running", "waiting"].includes(state)
            );
          });
          const cleanupJob = existingDestroy?.id
            ? await waitForJobTerminal(
                recoveryApi,
                existingDestroy.id,
                300_000,
                recoveryApi.apiBase,
              )
            : await destroyRuntimeStackViaApi(
                recoveryApi,
                MANAGED_RUNTIME_RECOVERY_STACK_ID,
                recoveryApi.apiBase,
              );
          if (cleanupJob?.state !== "completed") {
            throw new Error(
              `Managed runtime recovery destroy ended with ${String(cleanupJob?.state)}: ${runtimeJobFailureSummary(cleanupJob ?? {})}`,
            );
          }
          const cleanupReadback = await waitForManagedRuntimeCleanupReadback({
            token: recoveryApi,
            apiBase: recoveryApi.apiBase,
            stackId: MANAGED_RUNTIME_RECOVERY_STACK_ID,
            leaseId: recoveryLeaseId,
          });
          await attachJson("kombify-cloud-recovery-cleanup.json", {
            provider_id: leaseProvider.id,
            stack_id: MANAGED_RUNTIME_RECOVERY_STACK_ID,
            lease_id: recoveryLeaseId,
            abandoned_job: abandonedJob,
            destroy_job_id: cleanupJob?.id,
            destroy_job_state: cleanupJob?.state,
            exact_stack_confirmation: true,
            native_cleanup_readback: cleanupReadback,
          });
          return;
        }

        const session = createGatewaySessionKeeper(
          page,
          await captureGatewayApiSession(page),
        );
        managedApi = session;
        laneStartedAt = new Date().toISOString();
        advertisedStackkitsRelease = String(
          (
            await fetchRuntimeApi<{ release?: { tag?: string } }>(
              session,
              "/api/v1/stackkits/use-cases",
              session.apiBase,
            ).catch(() => ({}) as { release?: { tag?: string } })
          ).release?.tag ?? "",
        ).trim();
        logRuntimeE2E("creating managed lease stack through visible Wizard", {
          provider_id: leaseProvider.id,
        });
        const wizardCreate = await createManagedRuntimeStackViaWizard(
          page,
          leaseProvider.id,
          (created) => {
            stackId = created.stack_id;
            leaseId = created.lease_id ?? "";
          },
        );
        const stack = wizardCreate.stack;
        session.adopt(wizardCreate.api);
        stackName = wizardCreate.stackName;
        logRuntimeE2E("managed lease Gateway token ready", {
          provider_id: leaseProvider.id,
          has_token: Boolean(wizardCreate.api.token),
          gateway_base: session.apiBase,
        });
        stackId = stack.stack_id;
        logRuntimeE2E("managed lease stack created", {
          provider_id: leaseProvider.id,
          stack_id: stack.stack_id,
          job_id: stack.job_id,
        });
        managedObservationPhase(
          "preparation",
          MANAGED_RUNTIME_BUDGETS.preparationMs + 30_000,
        );
        let provisionFailure: unknown;
        const job = await waitForProvisionJob(
          session,
          "kombify-cloud",
          stack,
          MANAGED_RUNTIME_PROVISION_TIMEOUT_MS,
          session.apiBase,
          (observed) => {
            // A terminal job (completed or failed) feeds the lifecycle
            // receipt even when a later Day-2 or verification step throws.
            if (isTerminalJobState(observed.state)) provisionJob = observed;
            const result = observed.result as
              Record<string, unknown> | undefined;
            const guardStarted = Date.parse(
              String(result?.auto_deploy_guard_wait_started_at ?? ""),
            );
            if (Number.isFinite(guardStarted)) {
              const admittedAt = Date.parse(
                String(result?.auto_deploy_guard_verified_at ?? ""),
              );
              const targetVerifiedAt = Date.parse(
                String(result?.managed_runtime_target_verified_at ?? ""),
              );
              const guardEnded =
                Number.isFinite(admittedAt) && Number.isFinite(targetVerifiedAt)
                  ? Math.max(admittedAt, targetVerifiedAt)
                  : isTerminalJobState(observed.state)
                    ? Date.parse(String(observed.completed_at ?? ""))
                    : Date.now();
              expect(
                guardEnded - guardStarted,
                "Guard admission and target custody must finish within 60s of provider-ready, including RPCs and backoff",
              ).toBeLessThanOrEqual(60_000);
              if (
                Number.isFinite(admittedAt) ||
                isTerminalJobState(observed.state)
              ) {
                expect(
                  result?.guard_admission_reason,
                  "A finished Guard phase must state its exact outcome",
                ).toBeTruthy();
              }
            }
            const observedLease = String(result?.lease_id ?? "").trim();
            if (observedLease) {
              if (leaseId && leaseId !== observedLease)
                throw new Error("Managed job changed its lease identity");
              leaseId = observedLease;
            }
          },
        ).catch((error: unknown) => {
          provisionFailure = error;
          return undefined;
        });
        if (job) provisionJob = job;
        // Day-2 runs once the provision job is terminal, before the remaining
        // validations: an enrolled node proves stop, start, reconnect and the
        // SSH grant whatever the rollout outcome, and every later check then
        // also proves the node and its services survived the power cycle.
        const enrolledLease = leaseId
          ? await waitForManagedDay2Enrollment(session, leaseId)
          : undefined;
        let day2Recorded = false;
        if (enrolledLease?.enrollment_status === "enrolled") {
          managedObservationPhase("day2", MANAGED_DAY2_BUDGET_MS);
          day2Evidence = await managedDay2Evidence(
            session,
            leaseId,
            stack.stack_id,
          );
          await attachJson("kombify-cloud-day2.json", day2Evidence);
          day2Recorded = true;
        }
        if (provisionFailure !== undefined || !job) {
          throw (
            provisionFailure ??
            new Error("Managed provision job ended without a result")
          );
        }
        managedObservationPhase(
          "verification",
          MANAGED_RUNTIME_BUDGETS.verificationMs,
        );
        const managed = await waitForStack(
          session,
          stack.stack_id,
          (item) =>
            typeof item.lease_id === "string" && item.lease_id.length > 0,
          MANAGED_RUNTIME_PROVISION_TIMEOUT_MS,
          session.apiBase,
        );
        if (leaseId) {
          expect(managed.lease_id).toBe(leaseId);
        } else {
          leaseId = String(managed.lease_id);
        }
        expect(managed.server_provisioning_mode).toBe("kombify-cloud");
        expect(managed.runtime_lane).toBe("monthly-runtime");
        expect(managed.runtime_offering_id).toBe("monthly-runtime-standard");
        expect(managed.provider_id).toBe(leaseProvider.id);
        const status = await monthlyRuntimeRequest(
          session,
          leaseId,
          "",
          session.apiBase,
        );
        expect(status.enrollment_status).toBe("enrolled");
        const provisionResult = job.result as
          Record<string, unknown> | undefined;
        expect(
          Number.isFinite(
            Date.parse(
              String(provisionResult?.auto_deploy_guard_wait_started_at ?? ""),
            ),
          ),
          "Managed enrollment must retain the original provider-ready timestamp",
        ).toBe(true);
        expect(
          Number.isFinite(
            Date.parse(
              String(provisionResult?.auto_deploy_guard_verified_at ?? ""),
            ),
          ),
          "Enrolled provision must retain its successful canonical Guard admission",
        ).toBe(true);
        if (!day2Recorded) {
          managedObservationPhase("day2", MANAGED_DAY2_BUDGET_MS);
          day2Evidence = await managedDay2Evidence(
            session,
            leaseId,
            stack.stack_id,
          );
          await attachJson("kombify-cloud-day2.json", day2Evidence);
          managedObservationPhase(
            "verification",
            MANAGED_RUNTIME_BUDGETS.verificationMs,
          );
        }
        const ssh = await monthlyRuntimeRequest(
          session,
          leaseId,
          "/ssh",
          session.apiBase,
        );
        expect(ssh.lease_id).toBe(leaseId);
        const inventoryEvidence = await waitForManagedRuntimeInventoryEvidence({
          token: session,
          apiBase: session.apiBase,
          stackId: stack.stack_id,
          stackName,
          leaseId,
        });
        const managedServiceEvidence = await runManagedServiceActionEvidence({
          token: session,
          apiBase: session.apiBase,
          stackId: stack.stack_id,
          stackName,
          services: inventoryEvidence.service_registry.services,
        });
        await validateManagedRuntimePostSetupUi({
          page,
          token: session,
          apiBase: session.apiBase,
          stackId: stack.stack_id,
          stackName,
          leaseId,
          inventory: inventoryEvidence,
        });
        assertNoProviderSecrets(managed);
        await attachJson("kombify-cloud-artifacts.json", {
          stack_id: stack.stack_id,
          job_id: stack.job_id,
          job_state: job.state,
          lease_id: leaseId,
          runtime_status: status,
          ssh: ssh,
          inventory: inventoryEvidence,
          managed_service_actions: managedServiceEvidence,
          post_setup_validated: [
            "managed lease enrollment",
            "managed-runtime workers inventory projection",
            "managed-runtime stack operations projection",
            "monthly runtime management actions",
            ...("status" in managedServiceEvidence
              ? []
              : [
                  "declared service restart with idempotent replay",
                  "post-action inventory convergence",
                  "bounded redacted service logs",
                ]),
            "stack dashboard",
            "monitoring dashboard",
            "managed Day-2 stop, start, reconnect and SSH grant",
          ],
        });
      });
    });
  }

  // L0 Unifier layout journey (Beads kombify-Techstack-1cej, epic
  // platform-wig6t.1). Two real user-owned servers enroll into one homelab;
  // adding use cases shows one distribution checked against their exact
  // inventory, with plain reasons; finishing the flow accepts it, the run
  // revalidates on a fresh snapshot and the record reads back accepted.
  // Needs two hosts at or above the 4 GB host floor (TECHSTACK_E2E_DO_SIZE
  // s-2vcpu-8gb): a nominal 4 GB droplet reports less than 4 GB.
  test("layout-two-node recommends a verified distribution across two owner servers and accepts it", async ({
    page,
  }) => {
    // Two sequential provisions and enrollments plus the settle wait exceed
    // the 15-minute default; the window lane's cleanup step covers a timeout.
    test.setTimeout(60 * 60_000);
    const supplier = ownerServerSupplierFromEnv();
    const auth = await authenticateRuntimeUser(
      "oneliner",
      page,
      RUNTIME_AUTH_OPTIONS,
    );
    // Sign-in ends on the dashboard, which polls every homelab of the shared
    // persona; park the page while the servers provision.
    await page.goto("about:blank");
    const resources: OwnerServer[] = [];
    let stackId = "";
    try {
      const name = `layout-two-node-${runtimeRunId()}`;
      const found = await createLayoutWizardRun(auth.token, {
        intent: {
          schema: "techstack.wizard-intent/v1",
          run_kind: "first-run",
          name,
          goals: [],
          server: { transport: "install-command" },
          kit_assignment: { mode: "found", kit_slug: "basement-kit" },
        },
      });
      stackId = String(found.kit_deployment_id ?? found.stack_id ?? "");
      expect(stackId).not.toBe("");
      resources.push(await supplier.provision(1));
      await enrollWithRunToken(auth.token, resources[0], found);

      const join = await createLayoutWizardRun(auth.token, {
        intent: {
          schema: "techstack.wizard-intent/v1",
          run_kind: "expansion",
          name,
          goals: [],
          server: { transport: "install-command", roles: ["worker"] },
          kit_assignment: {
            mode: "join",
            kit_deployment_id: stackId,
            kit_slug: "basement-kit",
          },
        },
      });
      resources.push(await supplier.provision(2));
      await enrollWithRunToken(auth.token, resources[1], join);
      await waitForConnectedStackServers(auth.token, stackId, 2);
      await waitForStackSettled(auth.token, stackId);

      // The panel's preview response carries the StackKits reasons when the
      // check does not verify.
      let preview: unknown = null;
      page.on("response", (response) => {
        if (
          response.request().method() === "POST" &&
          /\/(?:api\/v1|v1\/techstack)\/unifier\/layout\/recommendations$/.test(
            new URL(response.url()).pathname,
          )
        ) {
          void response
            .json()
            .then((body) => (preview = body))
            .catch(() => undefined);
        }
      });
      await page.goto(`/stacks/${encodeURIComponent(stackId)}/use-cases/new`, {
        waitUntil: "domcontentloaded",
      });
      await expect(page.getByTestId("add-use-cases-page")).toBeVisible({
        timeout: 60_000,
      });
      await selectUseCase(page, "vault");
      await selectUseCase(page, "media");
      const panel = page.getByTestId("layout-recommendation-panel");
      try {
        await expect(panel).toHaveAttribute(
          "data-layout-verification",
          "verified",
          { timeout: 180_000 },
        );
      } finally {
        await attachJson("layout-two-node-preview.json", redactObject(preview));
      }
      await attachScreenshot(page, "layout-two-node-panel.png");

      const runResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/(?:api\/v1|v1\/techstack)\/wizard\/runs$/.test(
            new URL(response.url()).pathname,
          ),
        { timeout: CREATE_STACK_REQUEST_TIMEOUT_MS },
      );
      await page.getByTestId("wizard-create").click();
      const response = await runResponse;
      const runText = await response.text();
      expect(response.status(), runText).toBe(202);
      const run = (JSON.parse(runText).data ?? {}) as Record<string, unknown>;
      const recordId = String(run.layout_recommendation_id ?? "");
      expect(recordId).not.toBe("");
      await expect(page.getByTestId("add-use-cases-result")).toBeVisible({
        timeout: 60_000,
      });
      await attachScreenshot(page, "layout-two-node-accepted.png");

      const recordResponse = await authorizedFetch(
        auth.token,
        runtimeApiUrl(
          API_BASE,
          `/api/v1/unifier/layout/recommendations/${encodeURIComponent(recordId)}`,
        ),
      );
      const record = ((await recordResponse.json()).data ?? {}) as {
        state?: string;
        acceptance?: { revalidated_snapshot_digest?: string };
        assignments?: Array<{
          workload: string;
          node_id: string;
          reasons: Array<{ text: string }>;
        }>;
        verification?: { status?: string; cli_version?: string };
      };
      expect(["accepted", "executing", "done"]).toContain(record.state);
      expect(record.verification?.status).toBe("verified");
      const nodes = new Set(
        (record.assignments ?? []).map((assignment) => assignment.node_id),
      );
      expect(nodes.size).toBeGreaterThan(1);
      for (const assignment of record.assignments ?? []) {
        expect(assignment.reasons[0]?.text ?? "").not.toBe("");
      }
      await attachJson("layout-two-node-evidence.json", {
        owner_server_provider: supplier.provider,
        stack_id: stackId,
        recommendation_id: recordId,
        state: record.state,
        verification: record.verification,
        assignments: (record.assignments ?? []).map((assignment) => ({
          workload: assignment.workload,
          node_id: assignment.node_id,
          reason: assignment.reasons[0]?.text,
        })),
        run_state: run.state,
        job_id: run.job_id,
      });
    } finally {
      for (const resource of resources) {
        await resource.release();
      }
      if (stackId) {
        await destroyRuntimeStackViaApi(auth.token, stackId).catch(
          () => undefined,
        );
      }
    }
  });

  // Stack lifecycle on one owner server (Beads platform-37px0.1): the journey
  // that stands in for the retired unit tests of the stack, job and
  // StackKits-lifecycle control plane. One owner server enrolls through the
  // install command, the stack is deployed through the product, then the
  // owner monitors it, backs it up, updates it and destroys it. Every step is
  // observed through the owner-scoped API (jobs, receipts, read-backs).
  test("stack-lifecycle deploys a stack on an owner server, then monitors, backs up, updates and removes it", async ({
    page,
  }) => {
    // Provision, rollout, backup, upgrade and destroy exceed the 15-minute
    // default; the window lane's cleanup step covers a timeout.
    test.setTimeout(90 * 60_000);
    const supplier = ownerServerSupplierFromEnv();
    const auth = await authenticateRuntimeUser(
      "oneliner",
      page,
      RUNTIME_AUTH_OPTIONS,
    );
    let resource: OwnerServer | undefined;
    let stackId = "";
    let serverId = "";
    let destroyed = false;
    try {
      const enrolled = await enrollAndDeployLifecycleStack(
        page,
        auth.token,
        supplier,
        {
          onStack: (id) => {
            stackId = id;
          },
          onResource: (server) => {
            resource = server;
          },
          onServer: (id) => {
            serverId = id;
          },
        },
      );
      const deploy = enrolled.deploy;

      // Monitor: the deployed node and its services read back healthy through
      // the stack operations API, the server aggregate and the monitoring UI.
      const deployed = await waitForDeployedStack(auth.token, stackId);
      expect(deployed.server.status).toBe("connected");
      expect(deployed.server.assignment).toBe("stack");
      expect(deployed.runningServices).toBeGreaterThan(0);
      const agentId = String(deployed.server.agent_id ?? "");
      expect(agentId, "the deployed node names its agent").not.toBe("");
      const aggregate = await readServerAggregate(auth.token, serverId);
      expect(aggregate.connection?.state).toBe("connected");
      expect(aggregate.lifecycle?.state).toBe("active");
      await validateMonitoringUi(page, auth.token, "stack-lifecycle", {
        minConnectedAgents: 1,
      });
      await attachJson("stack-lifecycle-monitor.json", {
        stack_id: stackId,
        server_id: serverId,
        agent_id: agentId,
        stack_status: deployed.stackStatus,
        server_status: deployed.server.status,
        health: deployed.server.health,
        running_services: deployed.runningServices,
        service_count: deployed.serviceCount,
        stackkit: deployed.server.stackkit,
        connection: aggregate.connection,
        lifecycle: aggregate.lifecycle,
      });

      // Back up: the node admits the backup operations once it has converged
      // on the pinned StackKits release; configure, run, then read the
      // recovery point back through the status operation.
      const admitted = await waitForServerStackActions(auth.token, serverId, [
        "backup_configure",
        "backup_run",
        "backup_status",
        "upgrade",
        "verify",
      ]);
      const configure = await runStackKitLifecycleOperation(
        auth.token,
        stackId,
        agentId,
        "backup_configure",
        { owner_approved: true },
      );
      expect(configure.result.backup?.status).toBe("configured");
      const backup = await runStackKitLifecycleOperation(
        auth.token,
        stackId,
        agentId,
        "backup_run",
        { owner_approved: true },
      );
      const snapshotAnchor = String(backup.result.backup?.snapshot_anchor_id);
      expect(snapshotAnchor).toMatch(/^sha256:[a-f0-9]{64}$/);
      const status = await runStackKitLifecycleOperation(
        auth.token,
        stackId,
        agentId,
        "backup_status",
      );
      const recoveryPoints = recoveryPointsOf(status.result);
      expect(recoveryPoints.length).toBeGreaterThan(0);
      expect(recoveryPoints.every((point) => point.state === "recorded")).toBe(
        true,
      );

      // Update: the owner upgrades the deployed StackKit and the stack still
      // verifies on the node afterwards.
      const before = releaseOf(status.result);
      const upgrade = await runStackKitLifecycleOperation(
        auth.token,
        stackId,
        agentId,
        "upgrade",
        { owner_approved: true, target_release: "latest" },
      );
      const verify = await runStackKitLifecycleOperation(
        auth.token,
        stackId,
        agentId,
        "verify",
      );
      const after = await waitForDeployedStack(auth.token, stackId);
      expect(after.server.status).toBe("connected");
      expect(after.runningServices).toBeGreaterThan(0);
      await attachJson("stack-lifecycle-evidence.json", {
        owner_server_provider: supplier.provider,
        stack_id: stackId,
        server_id: serverId,
        agent_id: agentId,
        deploy_job_id: deploy.job_id,
        admitted_stack_actions: admitted,
        backup: {
          configure_job_id: configure.job.id,
          run_job_id: backup.job.id,
          snapshot_anchor_id: snapshotAnchor,
          status_job_id: status.job.id,
          recovery_points: recoveryPoints,
        },
        update: {
          release_before: before,
          upgrade_job_id: upgrade.job.id,
          upgrade_release: releaseOf(upgrade.result),
          upgrade_status: upgrade.result.status,
          verify_job_id: verify.job.id,
          verify_status: verify.result.status,
          running_services_after: after.runningServices,
        },
      });

      // Remove: destroy through the product; the stack is gone for the owner.
      const destroy = await destroyRuntimeStackViaApi(auth.token, stackId);
      expect(destroy?.state).toBe("completed");
      await waitForStackAbsent(auth.token, stackId);
      destroyed = true;
      await attachJson("stack-lifecycle-removed.json", {
        stack_id: stackId,
        destroy_job_id: destroy?.id,
        destroy_state: destroy?.state,
        stack_absent_for_owner: true,
      });
    } finally {
      const cleanupFailed = (step: string) => (error: unknown) =>
        logRuntimeE2E(`stack-lifecycle cleanup ${step} failed`, {
          message: redactText(String(error)).slice(0, 600),
        });
      if (stackId && !destroyed) {
        await destroyRuntimeStackViaApi(auth.token, stackId).catch(
          cleanupFailed("destroy"),
        );
      }
      if (serverId) {
        await detachOwnerServer(auth.token, serverId).catch(
          cleanupFailed("detach"),
        );
      }
      await resource?.release();
    }
  });

  // The owner deletes the server a stack runs on, then removes the stack. The
  // node can never answer a removal, so the product proves the server gone
  // itself (silent beyond the stale window, no lease, owner-operated) and takes
  // the owner's confirmation as the evidence for a host only its owner can
  // vouch for: the first request is refused with the servers to confirm, a
  // confirmation naming another server is refused, and the exact confirmation
  // removes the stack and ends the server's custody without a node. Backup and
  // update are covered by the scenario above and not repeated here.
  test("stack-lifecycle-offline removes a stack after its owner server is deleted", async ({
    page,
  }) => {
    test.setTimeout(90 * 60_000);
    const supplier = ownerServerSupplierFromEnv();
    const auth = await authenticateRuntimeUser(
      "oneliner",
      page,
      RUNTIME_AUTH_OPTIONS,
    );
    let resource: OwnerServer | undefined;
    let stackId = "";
    let serverId = "";
    let removed = false;
    try {
      await enrollAndDeployLifecycleStack(page, auth.token, supplier, {
        onStack: (id) => {
          stackId = id;
        },
        onResource: (server) => {
          resource = server;
        },
        onServer: (id) => {
          serverId = id;
        },
      });
      const deployed = await waitForDeployedStack(auth.token, stackId);
      expect(deployed.server.status).toBe("connected");
      expect(deployed.runningServices).toBeGreaterThan(0);

      // The owner deletes the server at its supplier; the supplier reads it
      // back absent, and the product sees the node go silent.
      const gone = resource!;
      await gone.release();
      if (gone.isAbsent) {
        expect(
          await gone.isAbsent(),
          "the supplier no longer lists the server",
        ).toBe(true);
      }
      const offline = await waitForServerOffline(auth.token, serverId);
      expect(offline.connection?.state).toBe("offline");

      // Without the owner's word the product refuses and names the server.
      const refused = await requestStackDestroy(auth.token, stackId);
      expect(refused.status, refused.text).toBe(409);
      const refusal = lifecycleRecord(
        lifecycleRecord(refused.json.error).details,
      );
      expect(refusal.reason_code).toBe("server_absence_confirmation_required");
      const refusedServers = (refusal.servers ?? []) as LifecycleRecord[];
      expect(refusedServers.map((server) => server.id)).toEqual([serverId]);

      // A confirmation of a server this stack does not hold is refused: the
      // confirmation names exactly the offline servers.
      const wrong = await requestStackDestroy(auth.token, stackId, [
        "server_000000000000000000000000",
      ]);
      expect(wrong.status, wrong.text).toBe(422);
      expect(
        lifecycleRecord(lifecycleRecord(wrong.json.error).details).reason_code,
      ).toBe("offline_removal_confirmation_invalid");

      // The exact confirmation removes the stack without its node.
      const accepted = await requestStackDestroy(auth.token, stackId, [
        serverId,
      ]);
      expect(accepted.status, accepted.text).toBe(202);
      const jobId = String(lifecycleRecord(accepted.json.data).job_id ?? "");
      expect(jobId, "the removal is accepted as a job").not.toBe("");
      const job = await waitForJobTerminal(auth.token, jobId, 300_000);
      await attachJson(
        "stack-lifecycle-offline-removal-job.json",
        redactObject(job),
      );
      expect(job.state, runtimeJobFailureSummary(job)).toBe("completed");
      const result = lifecycleRecord(job.result);
      const evidence = lifecycleRecord(result.offline_removal_evidence);
      expect(evidence.basis).toBe("owner_confirmation");
      expect(
        ((evidence.servers ?? []) as LifecycleRecord[]).map(
          (server) => server.server_id,
        ),
      ).toEqual([serverId]);
      expect(result.offline_removal_retired).toEqual([serverId]);
      await waitForStackAbsent(auth.token, stackId);
      removed = true;

      // Read back: the stack is gone for the owner and so is the server's
      // custody (the aggregate is a tombstone, not in the inventory).
      const retired = await waitForServerRetired(auth.token, serverId);
      await attachJson("stack-lifecycle-offline-removed.json", {
        stack_id: stackId,
        server_id: serverId,
        removal_job_id: jobId,
        removal_job_state: job.state,
        evidence_basis: evidence.basis,
        server_lifecycle: retired,
        stack_absent_for_owner: true,
      });
    } finally {
      const cleanupFailed = (step: string) => (error: unknown) =>
        logRuntimeE2E(`stack-lifecycle-offline cleanup ${step} failed`, {
          message: redactText(String(error)).slice(0, 600),
        });
      if (stackId && !removed) {
        await removeLifecycleStackWithOwnerWord(auth.token, stackId).catch(
          cleanupFailed("removal"),
        );
      }
      await resource?.release();
    }
  });
});

// Guard may still be completing its session when a rollout fails early, so
// the Day-2 gate observes enrollment for a bounded window and always logs
// why it proceeds or skips.
async function waitForManagedDay2Enrollment(
  api: GatewaySessionKeeper,
  leaseId: string,
  timeoutMs = 180_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: Record<string, any> | undefined;
  let lastError = "";
  while (Date.now() < deadline) {
    try {
      last = await monthlyRuntimeRequest(api, leaseId, "", api.apiBase);
      lastError = "";
      if (last?.enrollment_status === "enrolled") break;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    await delay(15_000);
  }
  logRuntimeE2E("managed Day-2 enrollment probe", {
    lease_id: leaseId,
    enrollment_status: last?.enrollment_status,
    observed_state: last?.observed_state,
    lease_state: last?.lease_state,
    desired_state: last?.desired_state,
    status_state: last?.status?.state,
    error: lastError.slice(0, 300),
  });
  return last;
}

// Stop and start each wait up to 12 minutes for provider convergence; the
// reconnect and SSH steps are bounded by the product's own timeouts.
const MANAGED_DAY2_BUDGET_MS = 1_800_000;

async function managedDay2Evidence(
  api: GatewaySessionKeeper,
  leaseId: string,
  stackId: string,
) {
  return runManagedDay2Evidence({
    bearer: api,
    apiUrl: (apiPath) => runtimeApiUrl(api.apiBase, apiPath),
    leaseId,
    stackId,
    log: logRuntimeE2E,
  });
}

async function monthlyRuntimeRequest(
  token: RuntimeBearer,
  leaseId: string,
  suffix: string,
  apiBase = API_BASE,
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(apiBase, `/api/v1/monthly-runtimes/${leaseId}${suffix}`),
  );
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Monthly Runtime request ${suffix || "status"} failed (HTTP ${response.status}): ${text}`,
    );
  }
  return json.data ?? json;
}

async function createLayoutWizardRun(
  token: string,
  request: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const key = `layout-e2e-${randomUUID()}`;
  const post = () =>
    authorizedFetch(token, runtimeApiUrl(API_BASE, "/api/v1/wizard/runs"), {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Idempotency-Key": key,
      },
      body: JSON.stringify(request),
    });
  let response = await post();
  let text = await response.text();
  // The API marks a failed registration preparation retryable and asks for a
  // retry with the same key; the client does that once, as the wizard does.
  if (
    response.status >= 500 &&
    /"reason_code"\s*:\s*"wizard_pairing_failed"/.test(text)
  ) {
    logRuntimeE2E(
      "wizard pairing preparation failed; retrying with the same key",
      {
        status: response.status,
        reason_code: "wizard_pairing_failed",
      },
    );
    await delay(5_000);
    response = await post();
    text = await response.text();
  }
  if (!response.ok) {
    throw new Error(
      `Wizard run failed (HTTP ${response.status}): ${redactText(text)}`,
    );
  }
  const data = (JSON.parse(text).data ?? {}) as Record<string, unknown>;
  await attachJson(
    `layout-run-${String(data.kit_assignment_mode ?? "run")}.json`,
    redactObject(data),
  );
  return data;
}

async function enrollWithRunToken(
  token: string,
  resource: { ip: string; keyPair: { privateKeyPath: string } },
  run: Record<string, unknown>,
) {
  // Job projections stay secret-free, so the wizard run returns only its
  // pairing job. Like the dashboard's connect action (renewNodePairing), the
  // owner mints the connection token from that job's registration fields.
  let pairing = String(run.bootstrap_token ?? "");
  const pairingJobId = String(run.pairing_job_id ?? "");
  if (!pairing && pairingJobId) {
    const job = (await getJob(token, pairingJobId)) as Record<string, any>;
    const registration = (job?.result ?? {}) as Record<string, unknown>;
    const response = await authorizedFetch(
      token,
      runtimeApiUrl(API_BASE, "/api/v1/trust/pairing-tokens"),
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          stack_id: String(run.kit_deployment_id ?? run.stack_id ?? ""),
          name: "layout-two-node",
          server_provisioning_mode:
            registration.server_provisioning_mode ?? "install-command",
          node_role: registration.server_node_role,
          stackkit: registration.stackkit_foundation,
          services: Array.isArray(registration.requested_services)
            ? registration.requested_services
            : [],
          ...(typeof registration.spec_node_id === "string" &&
          registration.spec_node_id
            ? { spec_node_id: registration.spec_node_id }
            : {}),
        }),
      },
    );
    if (!response.ok) {
      throw new Error(`Connection token mint failed (HTTP ${response.status})`);
    }
    const minted = (await response.json()) as { data?: { token?: string } };
    pairing = String(minted.data?.token ?? "");
  }
  if (!pairing) throw new Error("The wizard run returned no pairing token");
  const hostname = (
    await runSSH(
      resource.ip,
      resource.keyPair.privateKeyPath,
      "hostname",
      30_000,
    )
  ).stdout.trim();
  const installCommand = [
    `curl -fsSL ${shellQuote(`${API_BASE}/install.sh`)} |`,
    `KOMBI_SERVER=${shellQuote(API_BASE)}`,
    `KOMBI_TOKEN=${shellQuote(pairing)}`,
    "TECHSTACK_AS_SERVICE=1 sh",
  ].join(" ");
  let result: Awaited<ReturnType<typeof runSSH>>;
  try {
    result = await runSSH(
      resource.ip,
      resource.keyPair.privateKeyPath,
      installCommand,
      300_000,
    );
  } catch (err) {
    // The ssh command line carries the pairing token; never surface it.
    throw new Error(
      redactText(
        (err instanceof Error ? err.message : String(err))
          .split(pairing)
          .join("<pairing>"),
      ),
    );
  }
  await attachText(
    `layout-install-${hostname}.log`,
    redactText(`${result.stdout}\n${result.stderr}`),
  );
  const worker = await waitForWorkerByHostname(token, hostname, 180_000);
  if (worker.approved !== true) {
    await approveWorkerViaApi(token, worker.id, API_BASE);
    await waitForApprovedWorker(token, worker.id);
  }
}

// Use cases can be added once no rollout runs: the extend run refuses only a
// deployment that is still provisioning. A found Stack whose servers enroll
// through the install command stays pending until its first use case deploys.
async function waitForStackSettled(
  token: string,
  stackId: string,
  timeoutMs = 1_800_000,
) {
  const deadline = Date.now() + timeoutMs;
  let status = "";
  while (Date.now() < deadline) {
    const response = await authorizedFetch(
      token,
      runtimeApiUrl(API_BASE, `/api/v1/stacks/${encodeURIComponent(stackId)}`),
    );
    const body = (await response.json()) as { data?: Record<string, unknown> };
    status = String(body.data?.status ?? "").toLowerCase();
    if (status && status !== "provisioning") return status;
    await delay(15_000);
  }
  throw new Error(
    `Timed out waiting for stack ${stackId} to settle; last status=${status}`,
  );
}

async function waitForConnectedStackServers(
  token: string,
  stackId: string,
  count: number,
  timeoutMs = 600_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: unknown;
  while (Date.now() < deadline) {
    const response = await authorizedFetch(
      token,
      runtimeApiUrl(API_BASE, "/api/v1/servers"),
    );
    const body = (await response.json()) as { data?: unknown };
    const data = body.data as
      | Array<Record<string, any>>
      | { servers?: Array<Record<string, any>> }
      | undefined;
    const servers = Array.isArray(data) ? data : (data?.servers ?? []);
    const connected = servers.filter(
      (server) =>
        server.kit_deployment_id === stackId &&
        server.connection?.state === "connected",
    );
    last = servers.map((server) => ({
      id: server.id,
      stack: server.kit_deployment_id,
      connection: server.connection?.state,
    }));
    if (connected.length >= count) return connected;
    await delay(10_000);
  }
  throw new Error(
    `Timed out waiting for ${count} connected servers on ${stackId}; last=${JSON.stringify(last)}`,
  );
}

// ---- stack-lifecycle journey helpers ------------------------------------

interface LifecycleHooks {
  onStack: (stackId: string) => void;
  onResource: (resource: OwnerServer) => void;
  onServer: (serverId: string) => void;
}

// The owner brings one server, runs the install command with the registration
// token a first-run wizard prepared, allows Techstack to save in the Wallet and
// starts the rollout; the rollout is started again (twice at most) when the
// platform itself answered it with an error. What the run created is reported
// through the hooks as soon as it exists, so the caller's cleanup sees it even
// when a later step fails.
async function enrollAndDeployLifecycleStack(
  page: Page,
  token: string,
  supplier: ReturnType<typeof ownerServerSupplierFromEnv>,
  hooks: LifecycleHooks,
) {
  const name = `stack-lifecycle-${runtimeRunId()}`;
  const created = await createLifecycleWizardRun(
    token,
    {
      intent: {
        schema: "techstack.wizard-intent/v1",
        run_kind: "first-run",
        name,
        goals: [],
        // The owner server is a rented VPS: explicit placement evidence
        // lets the product classify it as a customer-operated cloud server.
        server: {
          transport: "install-command",
          environment_class: "cloud",
        },
        kit_assignment: { mode: "found", kit_slug: "basement-kit" },
      },
    },
    hooks.onStack,
  );
  const stackId = String(created.run.stack_id ?? created.run.kit_deployment_id);
  hooks.onStack(stackId);
  expect(stackId).not.toBe("");
  const pairingJobId = String(created.run.pairing_job_id ?? "");
  expect(pairingJobId, "the run names its registration job").not.toBe("");

  // Enroll: the owner brings one server and runs the install command with
  // the registration token the run prepared.
  const registrationToken = await mintRegistrationToken(
    token,
    stackId,
    pairingJobId,
  );
  const resource = await supplier.provision(1);
  hooks.onResource(resource);
  await enrollOwnerServer(token, resource, registrationToken);
  const connected = await waitForConnectedOwnerServer(token, stackId);
  const serverId = String(connected.id);
  hooks.onServer(serverId);

  // Deploy: a rollout saves its credentials in the owner's kombify Wallet,
  // so the owner first allows Techstack to do that (the consent the
  // product asks for once), then starts the rollout once the product says
  // it can.
  const walletConsent = await allowTechstackWalletCreate(page);
  await attachJson("stack-lifecycle-wallet-consent.json", walletConsent);
  await waitForRolloutReadiness(token, stackId);
  let deploy = await startStackRollout(token, stackId);
  let deployJob = await waitForJobTerminal(token, deploy.job_id, 40 * 60_000);
  // A rollout that failed on an answer of the platform itself (the node
  // downloads its StackKits release from the control plane, which a
  // Delivery redeploys now and then) is started again by its owner, as the
  // product offers for a failed rollout; any other failure ends the journey.
  for (
    let restart = 1;
    restart <= 2 &&
    deployJob.state === "failed" &&
    /HTTP 5\d\d|timed out|temporarily unavailable|Orphaned by process restart/i.test(
      runtimeJobFailureSummary(deployJob),
    );
    restart += 1
  ) {
    logRuntimeE2E("rollout failed on a platform answer, restarting", {
      restart,
      summary: runtimeJobFailureSummary(deployJob).slice(0, 300),
    });
    await waitForRolloutReadiness(token, stackId);
    deploy = await startStackRollout(token, stackId);
    deployJob = await waitForJobTerminal(token, deploy.job_id, 40 * 60_000);
  }
  await attachJson("stack-lifecycle-deploy-job.json", redactObject(deployJob));
  expect(deployJob.state, runtimeJobFailureSummary(deployJob)).toBe(
    "completed",
  );
  return { stackId, serverId, deploy };
}

type LifecycleRecord = Record<string, any>;

// Stable redeploys under a running journey (a Delivery answers with an edge
// 502 page for a short while): a read of the owner API retries on those
// answers within its own deadline and fails on anything else.
function isTransientRuntimeError(error: unknown) {
  return /SyntaxError|Unexpected token|HTTP 5\d\d|fetch failed|ECONN|ETIMEDOUT|ENOTFOUND|UND_ERR|aborted|timed out|timeout/i.test(
    String(error),
  );
}

async function retryTransient<T>(
  label: string,
  read: () => Promise<T>,
  timeoutMs = 180_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      return await read();
    } catch (error) {
      if (!isTransientRuntimeError(error) || Date.now() > deadline) throw error;
      logRuntimeE2E("transient read, retrying", {
        label,
        message: redactText(String(error)).slice(0, 160),
      });
      await delay(5_000);
    }
  }
}

function lifecycleRecord(value: unknown): LifecycleRecord {
  if (typeof value === "string") {
    try {
      return lifecycleRecord(JSON.parse(value));
    } catch {
      return {};
    }
  }
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as LifecycleRecord)
    : {};
}

// The first request of an install-command run must be accepted: a refused run
// still persisted its stack, which the cleanup of the caller removes.
async function createLifecycleWizardRun(
  token: string,
  request: Record<string, unknown>,
  onStack: (stackId: string) => void,
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(API_BASE, "/api/v1/wizard/runs"),
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Idempotency-Key": `lifecycle-e2e-${randomUUID()}`,
      },
      body: JSON.stringify(request),
    },
  );
  const text = await response.text();
  const json = lifecycleRecord(text);
  if (!response.ok) {
    const details = lifecycleRecord(lifecycleRecord(json.error).details);
    if (typeof details.stack_id === "string" && details.stack_id) {
      onStack(details.stack_id);
    }
    throw new Error(
      `Wizard run failed (HTTP ${response.status}): ${redactText(text)}`,
    );
  }
  expect(response.status).toBe(202);
  const run = lifecycleRecord(json.data);
  await attachJson("stack-lifecycle-run.json", redactObject(run));
  return { run };
}

// Job projections are secret-free, so the owner asks for the connection
// command explicitly: the same pairing-token request the creation page makes
// from the node configuration the run recorded on its registration job.
async function mintRegistrationToken(
  token: string,
  stackId: string,
  pairingJobId: string,
) {
  const job = await getJob(token, pairingJobId);
  expect(job.state, runtimeJobFailureSummary(job)).toBe("completed");
  const metadata = lifecycleRecord(job.result);
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(API_BASE, "/api/v1/trust/pairing-tokens"),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        stack_id: stackId,
        name: "stack-lifecycle node connection",
        server_provisioning_mode: metadata.server_provisioning_mode,
        node_role: metadata.server_node_role,
        stackkit: metadata.stackkit_foundation,
        services: metadata.requested_services ?? [],
        ...(typeof metadata.environment_class === "string"
          ? { environment_class: metadata.environment_class }
          : {}),
      }),
    },
  );
  const text = await response.text();
  expect(response.status, redactText(text)).toBeLessThan(300);
  const registration = String(
    lifecycleRecord(lifecycleRecord(text).data).token ?? "",
  );
  expect(registration, "the product minted a registration token").not.toBe("");
  return registration;
}

async function enrollOwnerServer(
  token: string,
  resource: { ip: string; keyPair: { privateKeyPath: string } },
  registrationToken: string,
) {
  const hostname = (
    await runSSH(
      resource.ip,
      resource.keyPair.privateKeyPath,
      "hostname",
      30_000,
    )
  ).stdout.trim();
  const installCommand = [
    `curl -fsSL ${shellQuote(`${AGENT_ORIGIN}/install.sh`)} |`,
    `KOMBI_SERVER=${shellQuote(AGENT_ORIGIN)}`,
    `KOMBI_TOKEN=${shellQuote(registrationToken)}`,
    "TECHSTACK_AS_SERVICE=1 sh",
  ].join(" ");
  let result: Awaited<ReturnType<typeof runSSH>>;
  try {
    result = await runSSH(
      resource.ip,
      resource.keyPair.privateKeyPath,
      installCommand,
      300_000,
    );
  } catch (err) {
    // The ssh command line carries the registration token; never surface it.
    throw new Error(
      redactText(
        (err instanceof Error ? err.message : String(err))
          .split(registrationToken)
          .join("<registration>"),
      ),
    );
  }
  await attachText(
    `stack-lifecycle-install-${hostname}.log`,
    redactText(`${result.stdout}\n${result.stderr}`),
  );
  const worker = await retryTransient("worker lookup", () =>
    waitForWorkerByHostname(token, hostname, 180_000),
  );
  if (worker.approved !== true) {
    await approveWorkerViaApi(token, worker.id, API_BASE);
    await retryTransient("worker approval", () =>
      waitForApprovedWorker(token, worker.id),
    );
  }
  return worker;
}

interface LifecycleOperations {
  readiness?: { can_start?: boolean; message?: string };
  kpis?: Record<string, unknown>;
  servers?: LifecycleRecord[];
  services?: unknown[];
}

async function readStackOperations(token: string, stackId: string) {
  return retryTransient("stack operations", () =>
    fetchRuntimeApi<LifecycleOperations>(
      token,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/operations`,
    ),
  );
}

async function waitForRolloutReadiness(
  token: string,
  stackId: string,
  timeoutMs = 300_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: LifecycleOperations | undefined;
  while (Date.now() < deadline) {
    last = await readStackOperations(token, stackId);
    if (last.readiness?.can_start === true) return last;
    await delay(5_000);
  }
  throw new Error(
    `The product never reported ${stackId} ready to start; readiness=${JSON.stringify(last?.readiness)}`,
  );
}

// The Wallet owner API sits at the public Gateway and takes the same Auth0
// bearer every Techstack gateway call carries (src/lib/kombify-wallet/
// wallet-api.ts). The grant is the call the creation page's Wallet prompt
// makes when its owner presses Allow.
async function allowTechstackWalletCreate(page: Page) {
  const session = await captureGatewayApiSession(page);
  const walletApi = `${new URL(session.apiBase).origin}/v1/wallet/services/creators`;
  const call = async (init: RequestInit = {}) => {
    const response = await fetch(walletApi, {
      ...init,
      headers: {
        accept: "application/json",
        authorization: `Bearer ${session.token}`,
        ...(init.body === undefined
          ? {}
          : { "content-type": "application/json" }),
      },
      signal: AbortSignal.timeout(30_000),
    });
    const text = await response.text();
    return { status: response.status, body: lifecycleRecord(text), text };
  };
  const before = await call();
  expect(before.status, redactText(before.text)).toBe(200);
  const alreadyAllowed = ((before.body.services ?? []) as string[]).includes(
    "techstack",
  );
  if (!alreadyAllowed) {
    const granted = await call({
      method: "POST",
      body: JSON.stringify({ action: "grant", serviceId: "techstack" }),
    });
    expect(granted.status, redactText(granted.text)).toBe(200);
    expect(granted.body.granted).toBe(true);
  }
  const after = await call();
  expect(((after.body.services ?? []) as string[]).includes("techstack")).toBe(
    true,
  );
  return { already_allowed: alreadyAllowed, allowed: true };
}

async function startStackRollout(token: string, stackId: string) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(
      API_BASE,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/deploy`,
    ),
    { method: "POST", headers: { "Idempotency-Key": randomUUID() } },
  );
  const text = await response.text();
  expect(response.status, redactText(text)).toBe(202);
  const data = lifecycleRecord(lifecycleRecord(text).data);
  const jobId = String(data.job_id ?? "");
  expect(jobId, "the rollout is accepted as a job").not.toBe("");
  return { job_id: jobId };
}

async function waitForDeployedStack(
  token: string,
  stackId: string,
  timeoutMs = 300_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: LifecycleOperations | undefined;
  while (Date.now() < deadline) {
    last = await readStackOperations(token, stackId);
    const servers = last.servers ?? [];
    const server =
      servers.find((item) => String(item.assignment) === "stack") ?? servers[0];
    const runningServices = numericRuntimeKpi(last.kpis, "running_services");
    if (
      server &&
      server.status === "connected" &&
      runningServices > 0 &&
      (last.services ?? []).length > 0
    ) {
      const stack = lifecycleRecord(
        await retryTransient("stack read", () =>
          fetchRuntimeApi<LifecycleRecord>(
            token,
            `/api/v1/stacks/${encodeURIComponent(stackId)}`,
          ),
        ),
      );
      return {
        server,
        runningServices,
        serviceCount: (last.services ?? []).length,
        stackStatus: String(stack.status ?? ""),
      };
    }
    await delay(5_000);
  }
  throw new Error(
    `Stack ${stackId} never read back as deployed; servers=${JSON.stringify(
      (last?.servers ?? []).map((item) => ({
        status: item.status,
        assignment: item.assignment,
      })),
    )} kpis=${JSON.stringify(last?.kpis)}`,
  );
}

async function readServerAggregate(token: string, serverId: string) {
  return retryTransient("server aggregate", () =>
    fetchRuntimeApi<LifecycleRecord>(
      token,
      `/api/v1/servers/${encodeURIComponent(serverId)}`,
    ),
  );
}

async function waitForConnectedOwnerServer(
  token: string,
  stackId: string,
  timeoutMs = 600_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: unknown;
  while (Date.now() < deadline) {
    const data = await retryTransient("server list", () =>
      fetchRuntimeApi<LifecycleRecord[] | { servers?: LifecycleRecord[] }>(
        token,
        "/api/v1/servers",
      ),
    );
    const servers = Array.isArray(data) ? data : (data.servers ?? []);
    const connected = servers.filter(
      (server) =>
        server.kit_deployment_id === stackId &&
        server.connection?.state === "connected",
    );
    last = servers
      .filter((server) => server.kit_deployment_id === stackId)
      .map((server) => ({
        id: server.id,
        connection: server.connection?.state,
      }));
    if (connected.length > 0) return connected[0];
    await delay(10_000);
  }
  throw new Error(
    `Timed out waiting for a connected server on ${stackId}; last=${JSON.stringify(last)}`,
  );
}

// The backend decides which kit operations a node admits; a node admits the
// Day-2 operations only once it converged on the pinned StackKits release.
async function waitForServerStackActions(
  token: string,
  serverId: string,
  required: string[],
  timeoutMs = 900_000,
) {
  const deadline = Date.now() + timeoutMs;
  let actions: string[] = [];
  while (Date.now() < deadline) {
    const server = await readServerAggregate(token, serverId);
    actions = ((server.stack_actions ?? []) as string[]).map(String);
    if (required.every((action) => actions.includes(action))) return actions;
    await delay(10_000);
  }
  throw new Error(
    `Server ${serverId} never admitted ${required.join(", ")}; admitted: [${actions.join(", ")}]`,
  );
}

async function runStackKitLifecycleOperation(
  token: string,
  stackId: string,
  agentId: string,
  operation: string,
  extra: Record<string, unknown> = {},
  timeoutMs = 1_200_000,
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(
      API_BASE,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/stackkit/operations`,
    ),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operation, agent_id: agentId, ...extra }),
    },
  );
  const text = await response.text();
  expect(response.status, `${operation}: ${redactText(text)}`).toBe(202);
  const accepted = lifecycleRecord(lifecycleRecord(text).data);
  expect(accepted.operation).toBe(operation);
  expect(accepted.kit_deployment_id).toBe(stackId);
  const jobId = String(accepted.job_id ?? "");
  expect(jobId, `${operation} is accepted as a job`).not.toBe("");
  const job = await waitForJobTerminal(token, jobId, timeoutMs);
  const result = lifecycleRecord(job.result);
  await attachJson(
    `stack-lifecycle-${operation}.json`,
    redactObject({ job_id: jobId, state: job.state, result }),
  );
  expect(job.state, runtimeJobFailureSummary(job)).toBe("completed");
  expect(result.schema_version).toBe("techstack.stackkit-lifecycle-result/v1");
  expect(result.techstack_id).toBe(stackId);
  expect(result.agent_id).toBe(agentId);
  expect(result.operation).toBe(operation);
  expect(result.success, runtimeJobFailureSummary(job)).toBe(true);
  return { job: job as LifecycleRecord, result };
}

function recoveryPointsOf(result: LifecycleRecord) {
  const history = lifecycleRecord(
    lifecycleRecord(lifecycleRecord(result.command_result).data).history,
  );
  return (
    Array.isArray(history.recoveryPoints) ? history.recoveryPoints : []
  ).map((point: LifecycleRecord) => ({
    evidence_id: String(point.evidenceId ?? ""),
    recorded_at: String(point.recordedAt ?? ""),
    current_plan: point.currentPlan === true,
    state: String(point.state ?? ""),
  }));
}

function releaseOf(result: LifecycleRecord) {
  const release = lifecycleRecord(result.release);
  return {
    version: String(release.version ?? ""),
    archive_sha256: String(release.archive_sha256 ?? ""),
  };
}

async function waitForStackAbsent(
  token: string,
  stackId: string,
  timeoutMs = 180_000,
) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const stacks = await retryTransient("stack list", () =>
      getStackList(token),
    );
    if (!stacks.some((stack) => stack.id === stackId)) return;
    await delay(5_000);
  }
  throw new Error(`Destroyed stack ${stackId} is still listed for its owner`);
}

// Removing the server's agent registration keeps the persona's inventory clean
// for the next run; the machine itself is deleted by its supplier.
async function detachOwnerServer(token: string, serverId: string) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(
      API_BASE,
      `/api/v1/servers/${encodeURIComponent(serverId)}/detach`,
    ),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ confirm_server_id: serverId }),
      signal: AbortSignal.timeout(30_000),
    },
  );
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`Detach HTTP ${response.status}: ${redactText(text)}`);
  }
}

// The product's answer to a destroy request, as the API sends it. The owner's
// confirmation that servers are gone goes in the optional body.
async function requestStackDestroy(
  token: string,
  stackId: string,
  confirmServerIds?: string[],
) {
  const response = await authorizedFetch(
    token,
    runtimeApiUrl(
      API_BASE,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/destroy`,
    ),
    {
      method: "POST",
      ...(confirmServerIds
        ? {
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              offline_removal: { confirm_server_ids: confirmServerIds },
            }),
          }
        : {}),
      signal: AbortSignal.timeout(30_000),
    },
  );
  const raw = await response.text();
  return {
    status: response.status,
    json: lifecycleRecord(raw),
    text: redactText(raw),
  };
}

// A server whose supplier deleted it stops heartbeating; the product shows it
// offline once the heartbeat is older than its stale window.
async function waitForServerOffline(
  token: string,
  serverId: string,
  timeoutMs = 20 * 60_000,
) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const aggregate = await readServerAggregate(token, serverId);
    if (aggregate.connection?.state === "offline") return aggregate;
    if (Date.now() > deadline) {
      throw new Error(
        `Server ${serverId} still reads ${String(aggregate.connection?.state)} after its supplier deleted it`,
      );
    }
    await delay(15_000);
  }
}

// The server's custody ended: its aggregate is the decommissioned tombstone
// and the owner's inventory no longer lists it.
async function waitForServerRetired(
  token: string,
  serverId: string,
  timeoutMs = 120_000,
) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const aggregate = await readServerAggregate(token, serverId).catch(
      (error) => {
        if (/HTTP 404/.test(String(error))) return undefined;
        throw error;
      },
    );
    const listed = (
      await retryTransient("server list", () =>
        fetchRuntimeApi<LifecycleRecord[]>(token, "/api/v1/servers"),
      )
    ).some((server) => server.id === serverId);
    if (
      !listed &&
      (aggregate === undefined ||
        aggregate.lifecycle?.state === "decommissioned")
    ) {
      return aggregate?.lifecycle ?? { state: "absent" };
    }
    if (Date.now() > deadline) {
      throw new Error(
        `Server ${serverId} was not retired: listed=${listed}, lifecycle=${String(aggregate?.lifecycle?.state)}`,
      );
    }
    await delay(5_000);
  }
}

// Best-effort removal for a run that stopped half way: the plain destroy, and
// when the product asks which servers are gone, the owner's confirmation of
// exactly those.
async function removeLifecycleStackWithOwnerWord(
  token: string,
  stackId: string,
) {
  const first = await requestStackDestroy(token, stackId);
  if (first.status === 404 || first.status === 202) return;
  const details = lifecycleRecord(lifecycleRecord(first.json.error).details);
  if (
    first.status === 409 &&
    details.reason_code === "server_absence_confirmation_required"
  ) {
    const ids = ((details.servers ?? []) as LifecycleRecord[]).map((server) =>
      String(server.id),
    );
    const second = await requestStackDestroy(token, stackId, ids);
    if (second.status === 202) return;
    throw new Error(
      `Removal with confirmation: HTTP ${second.status}: ${second.text}`,
    );
  }
  throw new Error(`Removal: HTTP ${first.status}: ${first.text}`);
}

test.afterAll(async () => {
  await mkdirForArtifactSentinel();
});

async function mkdirForArtifactSentinel() {
  const sentinelPath = path.resolve(ARTIFACTS_DIR, "runtime-e2e-scenarios.txt");
  await mkdir(path.dirname(sentinelPath), { recursive: true }).catch(
    () => undefined,
  );
  await writeFile(
    sentinelPath,
    `Runtime E2E completed at ${new Date().toISOString()}\n`,
  ).catch(() => undefined);
}
