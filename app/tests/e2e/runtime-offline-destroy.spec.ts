import { expect, test } from "@playwright/test";

import { authenticateRuntimeUser, type RuntimeAuthRole } from "./runtime-auth";

// Journey step for the 2026-10-22 stable crash: removing a stack whose server
// the owner confirmed gone retires that server and ends its agent connection.
// Stable runs without the gRPC listener, and ending the connection of a server
// on the nil listener panicked and took the only instance down. The removal
// must finish while the product stays up.

const PRODUCT_BASE = liveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL ??
    process.env.TECHSTACK_E2E_PRODUCT_URL ??
    process.env.PLAYWRIGHT_BASE_URL ??
    "https://techstack.kombify.io",
);
const API_BASE = liveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_API_URL ??
    process.env.TECHSTACK_API_URL ??
    PRODUCT_BASE,
);
// The runtime test user keeps stacks whose servers are long gone. The cloud
// fixture of the Delivery Preview login gate owns none, so a cloud-only
// selection skips this step.
const SELECTED_ROLES = (process.env.TECHSTACK_RUNTIME_E2E_AUTH_ROLES ?? "")
  .split(",")
  .map((role) => role.trim())
  .filter(Boolean);
const ROLE = (["remote", "oneliner"] as RuntimeAuthRole[]).find(
  (role) => SELECTED_ROLES.length === 0 || SELECTED_ROLES.includes(role),
);
const JOB_BUDGET_MS = 300_000;
const MIN_STACK_AGE_MS = 24 * 60 * 60 * 1000;

test("removing a stack whose servers are gone keeps the product up", async ({
  page,
}) => {
  test.skip(!ROLE, "no stack-owning runtime role is selected");
  test.setTimeout(JOB_BUDGET_MS + 120_000);
  const auth = await authenticateRuntimeUser(ROLE!, page, {
    productBase: PRODUCT_BASE,
    apiBase: API_BASE,
    sessionCookieName:
      process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session",
  });
  const target = await offlineStack(auth.token);
  test.skip(
    !target,
    `${ROLE!} has no stack older than a day whose servers are all offline`,
  );

  const startedUptime = await uptimeSeconds();
  const startedAt = Date.now();
  const readiness: number[] = [];
  let polling = true;
  const readinessLoop = (async () => {
    while (polling) {
      const response = await fetch(`${API_BASE}/api/v1/health/ready`, {
        signal: AbortSignal.timeout(10_000),
      }).catch(() => null);
      readiness.push(response?.status ?? 0);
      await new Promise((resolve) => setTimeout(resolve, 1_000));
    }
  })();

  let destroy: Awaited<ReturnType<typeof ownerJSON>>;
  let job: Awaited<ReturnType<typeof waitForTerminalJob>> | null = null;
  let jobID = "";
  try {
    // The owner confirms the servers as gone. A refused confirmation changes
    // nothing; one that names other servers asks again with the servers the
    // removal needs confirmed.
    const destroyPath = `/api/v1/stacks/${encodeURIComponent(target!.stackID)}/destroy`;
    const confirmed = (serverIDs: string[]) =>
      ownerJSON(auth.token, destroyPath, {
        method: "POST",
        body: JSON.stringify({
          offline_removal: { confirm_server_ids: serverIDs },
        }),
      });
    destroy = await confirmed(target!.serverIDs);
    const details = destroy.body?.error?.details;
    if (
      destroy.status === 409 &&
      [
        "server_absence_confirmation_required",
        "offline_removal_unconfirmed_server",
      ].includes(details?.reason_code) &&
      Array.isArray(details?.servers)
    ) {
      destroy = await confirmed(
        details.servers
          .map((server: { id?: unknown }) => String(server?.id ?? ""))
          .filter(Boolean),
      );
    }
    if (destroy.status === 202) {
      jobID = String(destroy.body?.data?.job_id ?? "").trim();
      job = jobID ? await waitForTerminalJob(auth.token, jobID) : null;
      // Watch past the end: the retirement runs at the end of the job.
      await new Promise((resolve) => setTimeout(resolve, 10_000));
    }
  } finally {
    polling = false;
    await readinessLoop;
  }
  // An offline removal the admission refused changed nothing: not this path.
  test.skip(
    destroy.status !== 202,
    `offline removal was refused: ${destroy.status} ${JSON.stringify(destroy.body?.error ?? destroy.body)}`,
  );
  const endedUptime = await uptimeSeconds();
  const elapsedSeconds = (Date.now() - startedAt) / 1_000;
  const retired = job?.result?.offline_removal_retired;
  console.log(
    `[offline-destroy] stack=${target!.stackID} servers=${target!.serverIDs.join(",")} job=${jobID} state=${job?.state} retired=${JSON.stringify(retired)} readiness=${readiness.join(",")}`,
  );

  // The removal went through the owner's confirmation and retired servers.
  expect(jobID).not.toBe("");
  expect(job?.state).toBe("completed");
  expect(Array.isArray(retired) && retired.length > 0).toBe(true);
  // The process ran the whole time and never reported itself down.
  expect(endedUptime - startedUptime).toBeGreaterThan(elapsedSeconds - 5);
  expect(readiness.filter((status) => status !== 200)).toEqual([]);
});

// offlineStack finds a stack older than a day whose servers all report
// offline. The age keeps it off stacks a concurrent run still cleans up.
async function offlineStack(
  token: string,
): Promise<{ stackID: string; serverIDs: string[] } | null> {
  const stacks = await ownerJSON(token, "/api/v1/stacks");
  expect(stacks.status).toBe(200);
  const items: { id?: unknown; created?: unknown }[] = Array.isArray(
    stacks.body?.data,
  )
    ? stacks.body.data
    : [];
  for (const item of items) {
    const stackID = String(item.id ?? "").trim();
    const created = Date.parse(String(item.created ?? ""));
    if (!stackID || !(Date.now() - created > MIN_STACK_AGE_MS)) continue;
    const operations = await ownerJSON(
      token,
      `/api/v1/stacks/${encodeURIComponent(stackID)}/operations`,
    );
    const servers: { id?: unknown; health?: { state?: unknown } }[] =
      operations.status === 200 && Array.isArray(operations.body?.data?.servers)
        ? operations.body.data.servers
        : [];
    if (
      servers.length > 0 &&
      servers.every((server) => server.health?.state === "offline")
    ) {
      return {
        stackID,
        serverIDs: servers.map((server) => String(server.id ?? "")),
      };
    }
  }
  return null;
}

async function waitForTerminalJob(
  token: string,
  jobID: string,
): Promise<{ state: string; result?: Record<string, unknown> }> {
  const deadline = Date.now() + JOB_BUDGET_MS;
  let state = "";
  while (Date.now() < deadline) {
    const job = await ownerJSON(
      token,
      `/api/v1/jobs/${encodeURIComponent(jobID)}`,
    ).catch(() => null);
    state = String(job?.body?.data?.state ?? "");
    if (["completed", "failed", "canceled"].includes(state)) {
      return { state, result: job?.body?.data?.result ?? undefined };
    }
    await new Promise((resolve) => setTimeout(resolve, 3_000));
  }
  return { state: state || "timeout" };
}

async function ownerJSON(
  token: string,
  endpoint: string,
  init: RequestInit = {},
) {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...init,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(init.body ? { "Content-Type": "application/json" } : {}),
    },
    signal: AbortSignal.timeout(30_000),
  });
  const text = await response.text();
  let body: any = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = { raw: text.slice(0, 200) };
  }
  return { status: response.status, body };
}

async function uptimeSeconds() {
  const response = await fetch(`${API_BASE}/api/v1/info`, {
    signal: AbortSignal.timeout(10_000),
  });
  const body = await response.json();
  const uptime = Number(body?.data?.uptime_secs);
  expect(Number.isFinite(uptime)).toBe(true);
  return uptime;
}

function liveProductURL(value: string): string {
  const parsed = new URL(value.trim());
  const host = parsed.hostname.toLowerCase();
  if (
    parsed.protocol !== "https:" ||
    host === "localhost" ||
    host === "127.0.0.1" ||
    host === "::1"
  ) {
    throw new Error(
      `Offline destroy journey must target the live product over HTTPS, got ${value}`,
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}
