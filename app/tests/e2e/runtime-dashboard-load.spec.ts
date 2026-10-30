import { expect, test } from "@playwright/test";

import { authenticateRuntimeUser, type RuntimeAuthRole } from "./runtime-auth";

// Journey step for the 2026-10-20 stable outage: Guard inventory reports
// already filled the control-plane pool, one owner's dashboard then polled
// identity and operations for seven stacks every ~15 s, each identity read kept
// polling that pool for 15-45 s, readiness could not get a pooled connection
// within 2 s, and Render restarted the only instance three times. The
// dashboard's own load must leave the product ready and answering.

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
// The runtime test user owns the stacks a dashboard polls. The cloud fixture,
// the only identity of the Delivery Preview login gate, owns none, so a
// cloud-only selection skips this step.
const SELECTED_ROLES = (process.env.TECHSTACK_RUNTIME_E2E_AUTH_ROLES ?? "")
  .split(",")
  .map((role) => role.trim())
  .filter(Boolean);
const ROLE = (["remote", "oneliner"] as RuntimeAuthRole[]).find(
  (role) => SELECTED_ROLES.length === 0 || SELECTED_ROLES.includes(role),
);
// The dashboard polls every ~15 s; three rounds overlap reads still running.
// The signed-in page stays on /dashboard meanwhile and polls as well.
const ROUNDS = 3;
const ROUND_INTERVAL_MS = 15_000;
// The server waits at most 4 s for a running identity snapshot.
const IDENTITY_ANSWER_BUDGET_MS = 12_000;
// The dashboard's own client abandons a read after 10 s (2026-10-21: most
// operations reads on stable were abandoned and polled again).
const OPERATIONS_ANSWER_BUDGET_MS = 10_000;
// A snapshot keeps working detached for up to 45 s after the read that
// started it, and it starts before that read answers, so readiness is watched
// for that long after the last identity answer.
const SNAPSHOT_BUDGET_MS = 47_000;
const READINESS_INTERVAL_MS = 1_000;

test("the dashboard's per-stack polling keeps the product ready", async ({
  page,
}) => {
  test.skip(!ROLE, "no stack-owning runtime role is selected");
  test.setTimeout(240_000);
  const auth = await authenticateRuntimeUser(ROLE!, page, {
    productBase: PRODUCT_BASE,
    apiBase: API_BASE,
    sessionCookieName:
      process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session",
  });
  const stacks = await ownerJSON(auth.token, "/api/v1/stacks");
  expect(stacks.status).toBe(200);
  const stackIDs = stackIDsFrom(stacks.body);
  test.skip(stackIDs.length === 0, `${ROLE!} owns no stack to poll`);

  const startedUptime = await uptimeSeconds();
  const startedAt = Date.now();
  const readiness: { status: number; database: boolean }[] = [];
  let polling = true;
  const readinessLoop = (async () => {
    while (polling) {
      readiness.push(await readinessSample());
      await new Promise((resolve) =>
        setTimeout(resolve, READINESS_INTERVAL_MS),
      );
    }
  })();

  type TimedRead = Awaited<ReturnType<typeof timedRead>>;
  const identityReads: Promise<TimedRead>[] = [];
  const operationReads: Promise<TimedRead>[] = [];
  for (let round = 0; round < ROUNDS; round += 1) {
    for (const id of stackIDs) {
      identityReads.push(
        timedRead(auth.token, `/api/v1/stacks/${id}/identity`),
      );
      operationReads.push(
        timedRead(auth.token, `/api/v1/stacks/${id}/operations`),
      );
    }
    if (round + 1 < ROUNDS) {
      await new Promise((resolve) => setTimeout(resolve, ROUND_INTERVAL_MS));
    }
  }
  const identities = await Promise.all(identityReads);
  const operations = await Promise.all(operationReads);
  const lastAnswerAt = Math.max(...identities.map((read) => read.endedAt));
  const settleMs = lastAnswerAt + SNAPSHOT_BUDGET_MS - Date.now();
  if (settleMs > 0) {
    await new Promise((resolve) => setTimeout(resolve, settleMs));
  }
  polling = false;
  await readinessLoop;
  const endedUptime = await uptimeSeconds();
  const elapsedSeconds = (Date.now() - startedAt) / 1_000;
  const pool = await readinessPool();
  console.log(
    `[dashboard-load] stacks=${stackIDs.length} identity_ms=${identities
      .map((read) => read.ms)
      .join(",")} identity_owner=${JSON.stringify(
      countBy(identities.map((read) => read.owner)),
    )} operations_ms=${operations
      .map(
        (read) => `${read.ms}${read.status === 200 ? "" : `/${read.status}`}`,
      )
      .join(",")} readiness=${readiness
      .map((sample) => sample.status)
      .join(",")} pool=${JSON.stringify(pool)}`,
  );

  // Readiness never reported the instance as down and it never restarted.
  expect(readiness.length).toBeGreaterThan(10);
  expect(readiness.filter((sample) => sample.status !== 200)).toEqual([]);
  expect(readiness.every((sample) => sample.database)).toBe(true);
  // The process ran the whole time: a restart resets uptime below the
  // elapsed time (a few seconds of slack for the two reads themselves).
  expect(endedUptime - startedUptime).toBeGreaterThan(elapsedSeconds - 5);
  // Every identity read answered within its budget instead of holding on.
  for (const read of identities) {
    expect(read.status).toBe(200);
    expect(read.ms).toBeLessThan(IDENTITY_ANSWER_BUDGET_MS);
  }
  // Every operations read answered before the dashboard would abandon it.
  for (const read of operations) {
    expect(read.status).toBe(200);
    expect(read.ms).toBeLessThan(OPERATIONS_ANSWER_BUDGET_MS);
  }
  expect(pool).not.toBeNull();
});

// Journey step for 2026-10-27: Stable's Guard could not answer the identity
// snapshot, the panel then replaced everyone, the owner included, with "The
// list of people is unavailable", and edge devices (kombify Box, kombify
// Watch) were mixed into the kombify clients. The owner row always shows,
// a missing snapshot is a notice, and edge devices have their own section.
test("the Devices & people panel keeps the owner and lists edge devices apart", async ({
  page,
}) => {
  test.skip(!ROLE, "no stack-owning runtime role is selected");
  test.setTimeout(150_000);
  const auth = await authenticateRuntimeUser(ROLE!, page, {
    productBase: PRODUCT_BASE,
    apiBase: API_BASE,
    sessionCookieName:
      process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session",
  });
  const stacks = await ownerJSON(auth.token, "/api/v1/stacks");
  expect(stacks.status).toBe(200);
  test.skip(
    stackIDsFrom(stacks.body).length === 0,
    `${ROLE!} owns no stack, so the dashboard loads no people`,
  );

  await page.goto(`${PRODUCT_BASE}/dashboard`, {
    waitUntil: "domcontentloaded",
  });
  const panel = page.getByTestId("dashboard-people");
  const shown = await panel
    .waitFor({ state: "visible", timeout: 60_000 })
    .then(() => true)
    .catch(() => false);
  test.skip(!shown, `${ROLE!} has no server inventory, so no people panel`);

  await expect(
    panel.locator(
      '[data-testid="people-row"][data-kind="person"][data-audience="owner"]',
    ),
  ).toHaveCount(1, { timeout: 60_000 });
  await expect(panel.getByTestId("people-state")).toHaveCount(0);
  // With kombify Connect answering, edge devices get their own section even
  // when none is registered yet.
  await expect(panel.locator('[aria-busy="true"]')).toHaveCount(0, {
    timeout: 60_000,
  });
  if ((await panel.getByTestId("devices-state").count()) === 0) {
    await expect(panel.getByTestId("edge-devices-heading")).toBeVisible();
  }
});

async function ownerJSON(token: string, endpoint: string) {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    headers: { Accept: "application/json", Authorization: `Bearer ${token}` },
    signal: AbortSignal.timeout(15_000),
  });
  const text = await response.text();
  return { status: response.status, body: text ? JSON.parse(text) : null };
}

async function timedRead(token: string, endpoint: string) {
  const started = Date.now();
  // The dashboard's own client gives an identity read 45 s.
  const response = await fetch(`${API_BASE}${endpoint}`, {
    headers: { Accept: "application/json", Authorization: `Bearer ${token}` },
    signal: AbortSignal.timeout(45_000),
  }).catch(() => null);
  const body = await response?.json().catch(() => null);
  const owner = body?.data?.owner;
  const endedAt = Date.now();
  return {
    status: response?.status ?? 0,
    ms: endedAt - started,
    endedAt,
    owner: owner ? `${owner.status}/${owner.reason_code ?? ""}` : "none",
  };
}

function countBy(values: string[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const value of values) counts[value] = (counts[value] ?? 0) + 1;
  return counts;
}

async function readinessSample() {
  const response = await fetch(`${API_BASE}/api/v1/health/ready`, {
    signal: AbortSignal.timeout(10_000),
  }).catch(() => null);
  if (!response) return { status: 0, database: false };
  const body = await response.json().catch(() => null);
  return {
    status: response.status,
    database: body?.data?.checks?.database === true,
  };
}

async function readinessPool() {
  const response = await fetch(`${API_BASE}/api/v1/health/ready`, {
    signal: AbortSignal.timeout(10_000),
  });
  const body = await response.json();
  return body?.data?.database_detail?.pool ?? null;
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

function stackIDsFrom(body: unknown): string[] {
  const data = (body as { data?: unknown })?.data;
  const items = Array.isArray(data)
    ? data
    : ((data as { items?: unknown[]; stacks?: unknown[] })?.items ??
      (data as { stacks?: unknown[] })?.stacks ??
      []);
  return items
    .map((item) => String((item as { id?: unknown })?.id ?? "").trim())
    .filter(Boolean);
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
      `Dashboard load journey must target the live product over HTTPS, got ${value}`,
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}
