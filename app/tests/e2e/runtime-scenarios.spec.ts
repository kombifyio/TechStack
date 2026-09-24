import { expect, test, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
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
import { runManagedDay2Evidence } from "./managed-day2-evidence";
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
} from "../helpers/test-utils";
import {
  authenticateRuntimeUser,
  captureGatewayApiSession,
  type RuntimeGatewaySession,
} from "./runtime-auth";

const execFileAsync = promisify(execFile);
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
const RUNTIME_AUTH_OPTIONS = {
  productBase: PRODUCT_BASE,
  apiBase: API_BASE,
  sessionCookieName: SESSION_COOKIE_NAME,
};

type RuntimeScenario = "install-command" | "connect-remote" | "kombify-cloud";

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
  token: string,
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
    token,
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
  api: RuntimeGatewaySession;
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
  await page.getByText("Provider & server details", { exact: true }).click();
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

async function getStackList(token: string, apiBase = API_BASE) {
  const response = await fetch(runtimeApiUrl(apiBase, "/api/v1/stacks"), {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    throw new Error(`List stacks failed with HTTP ${response.status}`);
  }
  const json = await response.json();
  return (json.data ?? json) as Array<Record<string, unknown>>;
}

async function waitForStack(
  token: string,
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
  token: string,
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
          "rollout",
          MANAGED_RUNTIME_BUDGETS.rolloutMs + 30_000,
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
  token: string,
  jobId: string,
  apiBase = API_BASE,
  deadline = Date.now() + 120_000,
) {
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const response = await fetch(
      runtimeApiUrl(apiBase, `/api/v1/jobs/${encodeURIComponent(jobId)}`),
      {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(
          Math.max(1, Math.min(30_000, deadline - Date.now())),
        ),
      },
    );
    const text = await response.text();
    const json = text ? JSON.parse(text) : {};
    if (response.ok) {
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
  token: string,
  path: string,
  apiBase = API_BASE,
  deadline = Date.now() + 30_000,
): Promise<T> {
  const response = await fetch(runtimeApiUrl(apiBase, path), {
    headers: { Authorization: `Bearer ${token}` },
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
async function waitForManagedRuntimeCleanupReadback(args: {
  token: string;
  apiBase: string;
  stackId: string;
  leaseId: string;
  timeoutMs?: number;
}): Promise<MonthlyRuntimeCleanupReadback> {
  const deadline = Date.now() + (args.timeoutMs ?? 120_000);
  let last: MonthlyRuntimeCleanupReadback | undefined;
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
}

async function waitForManagedRuntimeInventoryEvidence(args: {
  token: string;
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
    const worker = lastWorkers.find(
      (item) =>
        item.source === "managed-runtime" && item.lease_id === args.leaseId,
    );
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
      (item) =>
        item.source === "managed-runtime" && item.lease_id === args.leaseId,
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
      expect(worker.source).toBe("managed-runtime");
      expect(worker.lease_id).toBe(args.leaseId);
      expect(worker.approved).toBe(true);
      expect(worker.assignable).toBe(true);
      expect(worker.runtime_lane).toBe("monthly-runtime");
      expect(worker.enrollment_status).toBe("enrolled");
      expect(server.source).toBe("managed-runtime");
      expect(server.lease_id).toBe(args.leaseId);
      expect(server.assignment).toBe("stack");
      expect(server.approved).toBe(true);
      expect(server.assignable).toBe(true);
      expect(server.runtime_lane).toBe("monthly-runtime");
      expect(server.enrollment_status).toBe("enrolled");
      expect(operationServices.length).toBeGreaterThan(0);
      expect(registryServices.length).toBeGreaterThan(0);
      expect(runningServices).toBeGreaterThan(0);
      expect(serviceUrls.length).toBeGreaterThan(0);
      const evidence = {
        workers: {
          matched: worker,
          managed_runtime_rows: lastWorkers.filter(
            (item) => item.source === "managed-runtime",
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
  token: string;
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
    throw new Error(
      `Managed runtime did not declare a restart+logs capable service: ${JSON.stringify(args.services)}`,
    );
  }

  const invoke = async (
    action: "restart" | "logs",
    inventoryRevision: number,
    idempotencyKey: string,
  ) => {
    const response = await fetch(
      runtimeApiUrl(
        args.apiBase,
        `/api/v1/registry/services/${encodeURIComponent(service.id!)}/actions`,
      ),
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${args.token}`,
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
  token: string,
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

async function getMonitorStatusViaApi(token: string, apiBase = API_BASE) {
  const response = await fetch(
    runtimeApiUrl(apiBase, "/api/v1/monitor/status"),
    {
      headers: { Authorization: `Bearer ${token}` },
    },
  );
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Read monitor status failed with HTTP ${response.status}: ${text}`,
    );
  }
  return (json.data ?? json) as Record<string, unknown>;
}

async function getMonitorHealthViaApi(token: string, apiBase = API_BASE) {
  const response = await fetch(
    runtimeApiUrl(apiBase, "/api/v1/monitor/health"),
    {
      headers: { Authorization: `Bearer ${token}` },
    },
  );
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(
      `Read monitor health failed with HTTP ${response.status}: ${text}`,
    );
  }
  return (json.data ?? json) as Record<string, unknown>;
}

async function getMonitorAlertRulesViaApi(token: string, apiBase = API_BASE) {
  const response = await fetch(
    runtimeApiUrl(apiBase, "/api/v1/monitor/alerts/rules"),
    {
      headers: { Authorization: `Bearer ${token}` },
    },
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
  token: string,
  scenario: RuntimeScenario,
  opts: { minConnectedAgents?: number; apiBase?: string } = {},
) {
  const apiBase = opts.apiBase ?? API_BASE;
  const status = await getMonitorStatusViaApi(token, apiBase);
  await attachJson(`${scenario}-monitor-status.json`, redactObject(status));
  const health = await getMonitorHealthViaApi(token, apiBase);
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
  await firstNode.click();
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
  token: string;
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
  if (primaryServiceLabel) {
    await expect(
      args.page
        .getByTestId("dashboard-services-summary")
        .filter({ hasText: /runtime services? reported/ })
        .first(),
    ).toBeVisible({ timeout: 60_000 });
  }
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
  await expect(
    args.page.getByTestId("monthly-runtime-action-start"),
  ).toBeEnabled();
  await expect(
    args.page.getByTestId("monthly-runtime-action-stop"),
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
  token: string,
  stackId?: string,
  apiBase = API_BASE,
) {
  if (!stackId) return;
  const response = await fetch(
    runtimeApiUrl(
      apiBase,
      `/api/v1/stacks/${encodeURIComponent(stackId)}/destroy`,
    ),
    {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
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
      let managedApi: RuntimeGatewaySession | undefined;
      let stackId = "";
      let stackName = "";
      let leaseId = "";
      test.afterEach(async () => {
        test.setTimeout(MANAGED_RUNTIME_BUDGETS.cleanupMs);
        logRuntimeE2E("phase started", {
          phase: "cleanup",
          budget_ms: MANAGED_RUNTIME_BUDGETS.cleanupMs,
          stack_id: stackId,
          lease_id: leaseId,
        });

        if (stackId && managedApi) {
          const cleanupJob = await destroyRuntimeStackViaApi(
            managedApi.token,
            stackId,
            managedApi.apiBase,
          );
          const cleanupReadback = leaseId
            ? await waitForManagedRuntimeCleanupReadback({
                token: managedApi.token,
                apiBase: managedApi.apiBase,
                stackId,
                leaseId,
              })
            : undefined;
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
          const recoveryApi = await captureGatewayApiSession(page);
          const recoveryStack = (
            await getStackList(recoveryApi.token, recoveryApi.apiBase)
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
              recoveryApi.token,
              `/api/v1/stacks/${encodeURIComponent(MANAGED_RUNTIME_RECOVERY_STACK_ID)}/operations`,
              recoveryApi.apiBase,
            );
          const recoveryJobs = await fetchRuntimeApi<RuntimeJobListPayload>(
            recoveryApi.token,
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
            recoveryApi.token,
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
                recoveryApi.token,
                existingDestroy.id,
                300_000,
                recoveryApi.apiBase,
              )
            : await destroyRuntimeStackViaApi(
                recoveryApi.token,
                MANAGED_RUNTIME_RECOVERY_STACK_ID,
                recoveryApi.apiBase,
              );
          if (cleanupJob?.state !== "completed") {
            throw new Error(
              `Managed runtime recovery destroy ended with ${String(cleanupJob?.state)}: ${runtimeJobFailureSummary(cleanupJob ?? {})}`,
            );
          }
          const cleanupReadback = await waitForManagedRuntimeCleanupReadback({
            token: recoveryApi.token,
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

        managedApi = await captureGatewayApiSession(page);
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
        managedApi = wizardCreate.api;
        stackName = wizardCreate.stackName;
        const stackToken = managedApi.token;
        logRuntimeE2E("managed lease Gateway token ready", {
          provider_id: leaseProvider.id,
          has_token: Boolean(stackToken),
          gateway_base: managedApi.apiBase,
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
          managedApi.token,
          "kombify-cloud",
          stack,
          MANAGED_RUNTIME_PROVISION_TIMEOUT_MS,
          managedApi.apiBase,
          (observed) => {
            const result = observed.result as
              Record<string, unknown> | undefined;
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
        // Day-2 runs once the provision job is terminal, before the remaining
        // validations: an enrolled node proves stop, start, reconnect and the
        // SSH grant whatever the rollout outcome, and every later check then
        // also proves the node and its services survived the power cycle.
        const enrolledLease = leaseId
          ? await waitForManagedDay2Enrollment(managedApi, leaseId)
          : undefined;
        let day2Recorded = false;
        if (enrolledLease?.enrollment_status === "enrolled") {
          managedObservationPhase("day2", MANAGED_DAY2_BUDGET_MS);
          await attachJson(
            "kombify-cloud-day2.json",
            await managedDay2Evidence(managedApi, leaseId, stack.stack_id),
          );
          day2Recorded = true;
        }
        if (provisionFailure !== undefined || !job) {
          throw provisionFailure ?? new Error("Managed provision job ended without a result");
        }
        managedObservationPhase(
          "verification",
          MANAGED_RUNTIME_BUDGETS.verificationMs,
        );
        const managed = await waitForStack(
          managedApi.token,
          stack.stack_id,
          (item) =>
            typeof item.lease_id === "string" && item.lease_id.length > 0,
          MANAGED_RUNTIME_PROVISION_TIMEOUT_MS,
          managedApi.apiBase,
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
          managedApi.token,
          leaseId,
          "",
          managedApi.apiBase,
        );
        expect(status.enrollment_status).toBe("enrolled");
        if (!day2Recorded) {
          managedObservationPhase("day2", MANAGED_DAY2_BUDGET_MS);
          await attachJson(
            "kombify-cloud-day2.json",
            await managedDay2Evidence(managedApi, leaseId, stack.stack_id),
          );
          managedObservationPhase(
            "verification",
            MANAGED_RUNTIME_BUDGETS.verificationMs,
          );
        }
        const ssh = await monthlyRuntimeRequest(
          managedApi.token,
          leaseId,
          "/ssh",
          managedApi.apiBase,
        );
        expect(ssh.lease_id).toBe(leaseId);
        const inventoryEvidence = await waitForManagedRuntimeInventoryEvidence({
          token: managedApi.token,
          apiBase: managedApi.apiBase,
          stackId: stack.stack_id,
          stackName,
          leaseId,
        });
        const managedServiceEvidence = await runManagedServiceActionEvidence({
          token: managedApi.token,
          apiBase: managedApi.apiBase,
          stackId: stack.stack_id,
          stackName,
          services: inventoryEvidence.service_registry.services,
        });
        await validateManagedRuntimePostSetupUi({
          page,
          token: managedApi.token,
          apiBase: managedApi.apiBase,
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
            "declared service restart with idempotent replay",
            "post-action inventory convergence",
            "bounded redacted service logs",
            "stack dashboard",
            "monitoring dashboard",
            "managed Day-2 stop, start, reconnect and SSH grant",
          ],
        });
      });
    });
  }
});

// Guard may still be completing its session when a rollout fails early, so
// the Day-2 gate observes enrollment for a bounded window and always logs
// why it proceeds or skips.
async function waitForManagedDay2Enrollment(
  api: RuntimeGatewaySession,
  leaseId: string,
  timeoutMs = 180_000,
) {
  const deadline = Date.now() + timeoutMs;
  let last: Record<string, any> | undefined;
  let lastError = "";
  while (Date.now() < deadline) {
    try {
      last = await monthlyRuntimeRequest(api.token, leaseId, "", api.apiBase);
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
  api: RuntimeGatewaySession,
  leaseId: string,
  stackId: string,
) {
  return runManagedDay2Evidence({
    token: api.token,
    apiUrl: (apiPath) => runtimeApiUrl(api.apiBase, apiPath),
    leaseId,
    stackId,
    log: logRuntimeE2E,
  });
}

async function monthlyRuntimeRequest(
  token: string,
  leaseId: string,
  suffix: string,
  apiBase = API_BASE,
) {
  const response = await fetch(
    runtimeApiUrl(apiBase, `/api/v1/monthly-runtimes/${leaseId}${suffix}`),
    {
      headers: { Authorization: `Bearer ${token}` },
    },
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
