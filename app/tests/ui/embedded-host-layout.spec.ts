import { type BrowserContext, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { mockLoggedInContext, requireAppBase } from "../helpers/test-utils";

// Inside kombify Cloud (`host_navigation=true`) the frame is the viewport.
// The viewport-bound dashboard (a homelab with a Node inventory) sizes itself
// with `h-full` + `contain: size`, so the host-owned shell has to hand it a
// definite height. Without one the dashboard collapsed to its padding: a thin
// scrolling strip at the top of an otherwise empty frame (owner report
// 2026-09-26).

const deploymentId = "stack-embedded";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: { request_id: "test", timestamp: new Date().toISOString() },
  };
}

async function fulfillJson(route: Route, data: unknown) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
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

async function mockSaasHomelabWithNode(context: BrowserContext) {
  await context.route("**/api/v1/auth/mode", (route) =>
    fulfillJson(route, {
      data: {
        mode: "cloud",
        deployment_mode: "saas",
        is_first_run: false,
        cloud_auth_url: null,
        portal_url: "https://kombify.io",
        allow_local_login: false,
      },
    }),
  );
  await context.route("**/api/v2/whoami", (route) =>
    fulfillJson(route, {
      subject: "cloud-user-1",
      tenantId: "tenant-1",
      email: "admin@example.com",
      role: "owner",
    }),
  );
  await context.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await fulfillJson(route, apiEnvelope([deployment]));
  });
  await context.route("**/api/v1/homelab", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        homelab: { id: "hl-1", name: "My Homelab", named: true, intent: {} },
        kit_deployments: [deployment],
      }),
    ),
  );
  await context.route("**/api/v1/workers", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await context.route("**/api/v1/jobs**", (route) =>
    fulfillJson(route, apiEnvelope({ items: [] })),
  );
  await context.route("**/api/v1/services**", (route) =>
    fulfillJson(route, apiEnvelope([])),
  );
  await context.route("**/api/v1/servers", (route) =>
    fulfillJson(
      route,
      apiEnvelope([
        {
          id: "srv-embedded",
          node_id: "node-embedded",
          kit_deployment_id: deploymentId,
          name: "embedded-box",
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
  await context.route(`**/api/v1/stacks/${deploymentId}/operations`, (route) =>
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
}

test("the Cloud-embedded dashboard fills the frame", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockSaasHomelabWithNode(context);

  const page = await context.newPage();
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto(`${origin}/dashboard?embedded=true&host_navigation=true`, {
    waitUntil: "domcontentloaded",
  });

  const node = page.getByTestId("node-row").filter({ hasText: "embedded-box" });
  await expect(node).toBeVisible();

  const box = await page.getByTestId("stacks-dashboard").boundingBox();
  expect(box?.height ?? 0).toBeGreaterThan(720 * 0.9);
});
