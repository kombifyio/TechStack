import { expect, test, type Page, type Route } from "@playwright/test";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: {
      request_id: "test",
      timestamp: "2026-07-29T09:00:00Z",
    },
  };
}

async function fulfillJson(route: Route, data: unknown, status = 200) {
  const origin =
    route.request().headers()["origin"] ??
    process.env.PLAYWRIGHT_BASE_URL ??
    "http://127.0.0.1:5261";
  await route.fulfill({
    status,
    headers: {
      "access-control-allow-headers": "*",
      "access-control-allow-methods": "GET,POST,OPTIONS",
      "access-control-allow-origin": origin,
      "access-control-allow-credentials": "true",
      "content-type": "application/json",
    },
    body: JSON.stringify(data),
  });
}

function health(value: number) {
  const metric = { status: "ok", value, unit: "%" };
  return {
    state: "healthy",
    source: "promql",
    cpu_percent: metric,
    memory_percent: metric,
    disk_percent: metric,
    uptime_seconds: { status: "ok", value: 120, unit: "s" },
  };
}

function detailsPayload(cpu: number) {
  const serverHealth = health(cpu);
  return {
    stack: {
      id: "stack-1",
      name: "basement",
      provider: "local",
      state: "running",
      services: [],
      created_at: "2026-07-29T09:00:00Z",
      updated_at: "2026-07-29T09:00:00Z",
    },
    server: {
      id: "server-1",
      hostname: "node-alpha",
      role: "foundation",
      status: "healthy",
      assignment: "stack",
      agent_id: "agent-1",
      ip: "10.0.0.8",
      approved: true,
      precheck_state: "passed",
      capabilities: {},
      health: serverHealth,
    },
    services: [],
    checks: [],
    logs: [],
    health: serverHealth,
    monitoring: {
      status: "ok",
      queryBackend: "prometheus",
      ingestBackend: "otlp",
      collectorMode: "gateway",
      compatibilityMode: "native",
    },
  };
}

async function mockSession(page: Page) {
  await page.addInitScript(() => {
    const payload = btoa(
      JSON.stringify({
        id: "owner-1",
        exp: Math.floor(Date.now() / 1000) + 3600,
      }),
    )
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/g, "");
    window.localStorage.setItem(
      "pocketbase_auth",
      JSON.stringify({
        token: `test.${payload}.signature`,
        model: {
          id: "owner-1",
          email: "owner@example.com",
          collectionName: "users",
        },
      }),
    );
  });

  await page.route("**/api/v1/auth/mode", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        allow_local_login: true,
      }),
    ),
  );
  await page.route("**/api/v2/whoami", (route) =>
    fulfillJson(route, {
      subject: "owner-1",
      tenantId: "tenant-1",
      email: "owner@example.com",
      provider: "local",
      role: "admin",
    }),
  );
  await page.route("**/api/v2/auth/providers", (route) =>
    fulfillJson(route, { providers: [{ id: "local", kind: "local", issuer: "" }] }),
  );
  await page.route("**/api/v1/features", (route) =>
    fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] })),
  );
}

test("keeps Server details visible and updates health in place during Refresh", async ({
  page,
}) => {
  await mockSession(page);

  let cpu = 38;
  let holdRefresh = false;
  await page.route("**/api/v1/stacks/*/servers/*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    if (holdRefresh) {
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
    await fulfillJson(route, apiEnvelope(detailsPayload(cpu)));
  });
  await page.route("**/api/v1/servers/*/ports", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        server_id: "server-1",
        allocations: [],
        listeners_complete: true,
        observed_at: "2026-07-29T09:00:00Z",
      }),
    ),
  );
  await page.route("**/api/v1/servers/*", (route) =>
    fulfillJson(route, { code: 404, message: "not found", data: {} }, 404),
  );

  await page.goto("/stacks/stack-1/servers/server-1");
  await expect(page.getByRole("heading", { name: "node-alpha" })).toBeVisible();
  await expect(page.getByText("38%").first()).toBeVisible();

  cpu = 44;
  holdRefresh = true;
  await page.getByTestId("server-details-refresh").click();
  await expect(page.getByTestId("server-details-page")).toHaveAttribute(
    "aria-busy",
    "true",
  );
  await expect(page.getByRole("heading", { name: "node-alpha" })).toBeVisible();
  await expect(
    page.getByTestId("server-details-loading-state"),
  ).not.toBeVisible();
  await expect(page.getByTestId("server-details-tabs")).toBeVisible();
  await expect(page.getByText("38%").first()).toBeVisible();

  await expect(page.getByText("44%").first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "node-alpha" })).toBeVisible();
  await expect(page.getByTestId("server-details-page")).not.toHaveAttribute(
    "aria-busy",
    "true",
  );
});
