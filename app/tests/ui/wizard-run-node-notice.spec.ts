import { type Page, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";

// A StackKit rollout belongs to exactly one server: the wizard run's notice
// renders at that Node, never above the fleet, and a rollout that stopped
// progressing must not keep a notice up (the stuck Cloudreve banner).

const deploymentId = "stack-a";
const serverId = "srv-cloudreve";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: { request_id: "test", timestamp: new Date().toISOString() },
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

const deployment = {
  id: deploymentId,
  name: "basement",
  mode: "easy",
  status: "running",
  state: "running",
  provider: "local",
  services: [],
  created_at: "2026-09-20T09:00:00Z",
  updated_at: "2026-09-20T09:00:00Z",
};

function activeRun(jobUpdatedAt: Date) {
  return {
    run_id: "run-cloudreve",
    status: "completed",
    run_kind: "expansion",
    homelab_id: "hl-1",
    stack_id: deploymentId,
    target_server_id: serverId,
    job_id: "job-cloudreve",
    result: { name: "basement", state: "provisioning" },
    job: {
      id: "job-cloudreve",
      state: "running",
      type: "deploy",
      progress: 40,
      step: "rollout",
      message: "Rolling out Cloudreve",
      updated_at: jobUpdatedAt.toISOString(),
    },
  };
}

/** Mocks one homelab with one connected Node and the given active run. */
async function mockDashboard(page: Page, run: unknown) {
  const calls = { abandon: 0, dismiss: 0 };
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
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (
      route.request().method() === "OPTIONS" &&
      (url.origin === "http://127.0.0.1:5260" ||
        url.origin === "http://127.0.0.1:5276")
    ) {
      await route.fulfill({
        status: 204,
        headers: {
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,OPTIONS",
          "access-control-allow-origin":
            route.request().headers()["origin"] ?? "http://127.0.0.1:5261",
          "access-control-allow-credentials": "true",
        },
      });
      return;
    }
    await route.fallback();
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
      user: { id: "owner-1", email: "owner@example.com", name: "Owner" },
      source: "local",
    }),
  );
  await page.route("**/api/v1/features", (route) =>
    fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] })),
  );
  await page.route("**/api/v1/tunnel/registry-url", (route) =>
    fulfillJson(
      route,
      apiEnvelope({ url: "http://localhost:5260", mode: "local" }),
    ),
  );
  await page.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await fulfillJson(route, apiEnvelope([deployment]));
  });
  await page.route("**/api/v1/homelab", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        homelab: { id: "hl-1", name: "My Homelab", named: true, intent: {} },
        kit_deployments: [deployment],
      }),
    ),
  );
  await page.route("**/api/v1/workers", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await page.route("**/api/v1/jobs**", (route) =>
    fulfillJson(route, apiEnvelope({ items: [] })),
  );
  await page.route("**/api/v1/services**", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await page.route("**/api/v1/servers", (route) =>
    fulfillJson(
      route,
      apiEnvelope([
        {
          id: serverId,
          node_id: "node-cloudreve",
          kit_deployment_id: deploymentId,
          name: "cloudreve-box",
          lifecycle: { state: "active", desired_state: "active" },
          connection: {
            state: "connected",
            changed_at: new Date().toISOString(),
          },
          health: { state: "healthy" },
          channels: [],
          inventory_revision: 1,
          provider: {},
          mutations_allowed: true,
        },
      ]),
    ),
  );
  await page.route(`**/api/v1/stacks/${deploymentId}/operations`, (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        stack: {
          id: deploymentId,
          name: "basement",
          status: "running",
          state: "running",
        },
        readiness: {
          status: "ready",
          can_start: false,
          required_servers: 1,
          approved_servers: 1,
          connected_servers: 1,
        },
        nextSteps: [],
        kpis: {
          registered_servers: 1,
          healthy_servers: 1,
          running_services: 0,
        },
        servers: [],
        services: [],
        monitoring: { status: "healthy", alerts: [] },
        alerts: [],
      }),
    ),
  );
  await page.route("**/api/v1/wizard/runs/active", (route) =>
    fulfillJson(route, apiEnvelope({ run })),
  );
  await page.route(
    `**/api/v1/stacks/${deploymentId}/jobs/job-cloudreve/abandon`,
    async (route) => {
      calls.abandon += 1;
      await fulfillJson(route, apiEnvelope({ success: true }));
    },
  );
  await page.route(
    "**/api/v1/wizard/runs/run-cloudreve/dismiss",
    async (route) => {
      calls.dismiss += 1;
      await fulfillJson(
        route,
        apiEnvelope({ run_id: "run-cloudreve", dismissed: true }),
      );
    },
  );
  return calls;
}

test.describe("wizard run notice", () => {
  test("a live rollout notice renders at its Node, not above the fleet", async ({
    page,
  }) => {
    await mockDashboard(page, activeRun(new Date()));
    await page.goto("/dashboard");

    const node = page
      .getByTestId("node-row")
      .filter({ hasText: "cloudreve-box" });
    const notice = node.getByTestId("wizard-run-banner");
    await expect(notice).toContainText("Rolling out Cloudreve");
    await expect(page.getByTestId("wizard-run-banner")).toHaveCount(1);
  });

  test("a rollout that stopped progressing shows no notice", async ({
    page,
  }) => {
    await mockDashboard(
      page,
      activeRun(new Date(Date.now() - 2 * 60 * 60 * 1000)),
    );
    await page.goto("/dashboard");

    await expect(
      page.getByTestId("node-row").filter({ hasText: "cloudreve-box" }),
    ).toBeVisible();
    await expect(page.getByTestId("wizard-run-banner")).toHaveCount(0);
  });

  test("cancelling the rollout asks first, then abandons its job", async ({
    page,
  }) => {
    const calls = await mockDashboard(page, activeRun(new Date()));
    await page.goto("/dashboard");

    await page.getByTestId("wizard-run-banner-cancel").click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(calls.abandon).toBe(0);

    await page.getByTestId("wizard-run-banner-cancel").click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel rollout" })
      .click();
    await expect.poll(() => calls.abandon).toBe(1);
  });
});
