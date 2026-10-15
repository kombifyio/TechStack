import { type BrowserContext, type Page, type Route } from "@playwright/test";
import { test, expect } from "./fixtures";
import { mockLoggedInContext } from "../helpers/test-utils";

// Owner direction 2026-10-09: a message never makes the Node list stop fitting.
// Node cards keep one compact height whatever they report, sit on one level
// (the federation bundle lives in the left gutter), and a message's details
// open as an overlay on click without moving any card.

const fedStack = "stack-fed";
const soloStack = "stack-solo";
const failedJobId = "job-apply-failed";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: { request_id: "test", timestamp: new Date().toISOString() },
  };
}

async function fulfillJson(route: Route, data: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(data),
  });
}

function deployment(id: string, name: string, status = "running") {
  return {
    id,
    name,
    mode: "easy",
    status,
    state: status,
    provider: "local",
    services: [],
    created_at: "2026-10-07T09:00:00Z",
    updated_at: "2026-10-07T09:00:00Z",
  };
}

function canonical(
  id: string,
  stack: string,
  name: string,
  connection: "connected" | "offline",
  lastOutcome?: Record<string, unknown>,
) {
  return {
    id,
    node_id: `node-${id}`,
    kit_deployment_id: stack,
    name,
    lifecycle: { state: "active", desired_state: "active" },
    connection: {
      state: connection,
      changed_at: "2026-10-09T08:00:00Z",
      last_heartbeat_at: "2026-10-09T08:00:00Z",
    },
    health: { state: connection === "connected" ? "healthy" : "unknown" },
    channels: [],
    inventory_revision: 1,
    provider: {},
    mutations_allowed: true,
    created_at: "2026-10-07T09:00:00Z",
    updated_at: "2026-10-09T08:00:00Z",
    ...(lastOutcome ? { last_outcome: lastOutcome } : {}),
  };
}

function telemetry(
  id: string,
  stack: string,
  name: string,
  status: string,
  managed = false,
) {
  return {
    id,
    server_id: id,
    hostname: name,
    role: "server",
    status,
    assignment: "stack",
    kit_deployment_id: stack,
    agent_id: `agent-${id}`,
    approved: true,
    precheck_state: "passed",
    ...(managed ? { source: "managed-runtime", lease_id: `lease-${id}` } : {}),
    health: {
      state: "healthy",
      cpu_percent: 21,
      memory_percent: 48,
      disk_percent: 63,
    },
    capabilities: {},
  };
}

function service(id: string, server: string, key: string, name: string) {
  return {
    id,
    kit_deployment_id: fedStack,
    server_id: server,
    target_kind: "server",
    placement: { freshness: { state: "recorded" } },
    service_key: key,
    service_instance: "default",
    name,
    management_state: "observed",
    desired_state: "running",
    observed_state: "running",
    health: { state: "healthy" },
    // Recognized applications with a web UI: the dashboard lists only those.
    catalog: { id: key, display_name: name, category: "apps" },
    access: { mode: "direct", url: `https://${key}.home.example` },
    allowed_actions: [],
    inventory_revision: 1,
    source: "guard",
    provenance: {},
    created_at: "2026-10-07T09:00:00Z",
    updated_at: "2026-10-09T08:00:00Z",
  };
}

const heartbeatOutcome = {
  status: "degraded",
  reason_code: "heartbeat_expired",
  retryable: false,
  user_guidance: {
    title: "The server connection needs attention",
    body: "Techstack has not received a recent authenticated Guard heartbeat from this server. Check that the server and the Guard service are online. If the server was moved, replaced or reinstalled, reconnect it from the Node details so a fresh heartbeat can be verified.",
    next_steps: [
      {
        id: "s1",
        label: "Check that the server and Guard service are online",
        kind: "note",
      },
    ],
  },
};

async function mockDashboard(context: BrowserContext) {
  await context.route("**/api/v1/features", (route) =>
    fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] })),
  );
  await context.route("**/api/v1/stacks", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return fulfillJson(
      route,
      apiEnvelope([
        deployment(fedStack, "homelab", "error"),
        deployment(soloStack, "garage"),
      ]),
    );
  });
  await context.route("**/api/v1/homelab", (route) =>
    fulfillJson(
      route,
      apiEnvelope({
        homelab: { id: "hl-1", name: "My Homelab", named: true, intent: {} },
        kit_deployments: [
          deployment(fedStack, "homelab", "error"),
          deployment(soloStack, "garage"),
        ],
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
    fulfillJson(
      route,
      apiEnvelope([
        service("svc-1", "srv-ok", "grafana", "Grafana"),
        service("svc-2", "srv-ok", "immich", "Immich"),
        service("svc-3", "srv-ok", "nextcloud", "Nextcloud"),
        service("svc-4", "srv-ok", "jellyfin", "Jellyfin"),
        service("svc-5", "srv-ok", "paperless", "Paperless"),
        service("svc-6", "srv-ok", "vaultwarden", "Vaultwarden"),
        service("svc-9", "srv-ok", "n8n", "n8n"),
        service("svc-10", "srv-ok", "uptime-kuma", "Uptime Kuma"),
        service("svc-11", "srv-ok", "ollama", "Ollama"),
        // Enough apps that pve-main overflows its five tile rows.
        ...Array.from({ length: 16 }, (_, index) =>
          service(`svc-x${index}`, "srv-ok", `app-${index}`, `App ${index}`),
        ),
        service("svc-7", "srv-failed", "gitea", "Gitea"),
        service("svc-8", "srv-solo", "homeassistant", "Home Assistant"),
      ]),
    ),
  );
  await context.route("**/api/v1/servers", (route) =>
    fulfillJson(
      route,
      apiEnvelope([
        canonical("srv-ok", fedStack, "pve-main", "connected"),
        canonical(
          "srv-offline",
          fedStack,
          "nas-basement",
          "offline",
          heartbeatOutcome,
        ),
        canonical("srv-failed", fedStack, "managed-vm", "connected"),
        canonical("srv-solo", soloStack, "garage-pi", "connected"),
      ]),
    ),
  );
  await context.route("**/api/v1/stacks/*/operations", (route) => {
    const stack = route.request().url().split("/stacks/")[1]?.split("/")[0];
    const servers =
      stack === fedStack
        ? [
            telemetry("srv-ok", fedStack, "pve-main", "online"),
            telemetry("srv-offline", fedStack, "nas-basement", "offline"),
            telemetry("srv-failed", fedStack, "managed-vm", "online", true),
          ]
        : [telemetry("srv-solo", soloStack, "garage-pi", "online")];
    return fulfillJson(
      route,
      apiEnvelope({
        stack: { id: stack, name: stack, status: "running", state: "running" },
        readiness: {
          status: "ready",
          can_start: false,
          required_servers: servers.length,
          approved_servers: servers.length,
          connected_servers: servers.length,
        },
        nextSteps: [],
        kpis: {
          registered_servers: servers.length,
          healthy_servers: servers.length,
          running_services: 0,
        },
        servers,
        services: [],
        monitoring: { status: "unknown", alerts: [] },
        alerts: [],
        ...(stack === fedStack
          ? {
              latestFailure: {
                job_id: failedJobId,
                type: "deploy",
                state: "failed",
                step: "apply",
                server_id: "srv-failed",
                lease_id: "lease-srv-failed",
                reason: "typed_stackkit_apply_failed",
                message: "Applying StackKit services on the managed VM failed",
                diagnostics_available: false,
              },
            }
          : {}),
      }),
    );
  });
}

async function cardBoxes(page: Page) {
  const boxes = await page.getByTestId("node-row").evaluateAll((rows) =>
    rows.map((row) => {
      const rect = row.getBoundingClientRect();
      return {
        left: rect.left,
        right: rect.right,
        top: rect.top,
        height: rect.height,
      };
    }),
  );
  return boxes;
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "mobile", width: 375, height: 812 },
]) {
  test(`node cards stay uniform and flush while messages open as overlays (${viewport.name})`, async ({
    context,
    baseURL,
  }) => {
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockDashboard(context);
    const page = await context.newPage();
    await page.setViewportSize({
      width: viewport.width,
      height: viewport.height,
    });
    await page.goto(`${baseURL}/dashboard`);
    await expect(page.getByTestId("node-row")).toHaveCount(4);
    if (viewport.width < 1280) {
      await page.getByTestId("dashboard-tab-nodes").click();
    }
    await page.getByTestId("node-row").first().waitFor();

    const shot = process.env.NODE_CARDS_SHOTS;
    if (shot) {
      await page.screenshot({
        path: `${shot}/node-cards-${viewport.name}.png`,
      });
    }

    const messages = page.getByTestId("node-message-toggle");
    // Messages arrive with the stack telemetry, after the rows first render.
    await expect.poll(() => messages.count()).toBeGreaterThanOrEqual(2);
    // Bring the control into view first so the click itself scrolls nothing.
    await messages.first().scrollIntoViewIfNeeded();
    const before = await cardBoxes(page);
    const listScroll = () =>
      page
        .getByTestId("dashboard-node-list")
        .evaluate((element) => element.scrollTop);
    const scrolledBefore = await listScroll();
    // One level: every card shares the same left and right edge, bundled or not.
    for (const box of before) {
      expect(Math.abs(box.left - before[0]!.left)).toBeLessThanOrEqual(1);
      expect(Math.abs(box.right - before[0]!.right)).toBeLessThanOrEqual(1);
    }
    // Uniform: a message never makes a card taller than its neighbours.
    for (const box of before) {
      expect(Math.abs(box.height - before[0]!.height)).toBeLessThanOrEqual(1);
    }

    await messages.first().click();
    const overlay = page.getByTestId("node-message-overlay");
    await expect(overlay).toBeVisible();
    if (shot) {
      await page.screenshot({
        path: `${shot}/node-cards-${viewport.name}-overlay.png`,
      });
    }

    // The overlay floats above the list: nothing moved, nothing resized.
    expect(await cardBoxes(page)).toEqual(before);
    expect(await listScroll()).toBe(scrolledBefore);

    await page.keyboard.press("Escape");
    await expect(overlay).toHaveCount(0);
    expect(await cardBoxes(page)).toEqual(before);
  });
}

test("node actions and extra apps open as overlays above the list", async ({
  context,
  baseURL,
}) => {
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockDashboard(context);
  const page = await context.newPage();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`${baseURL}/dashboard`);
  await expect(page.getByTestId("node-row")).toHaveCount(4);

  const managed = page
    .getByTestId("node-row")
    .filter({ hasText: "managed-vm" });
  const actions = managed.getByTestId("node-actions-toggle");
  // Inventory hydration can replace the row after it first appears. A trial
  // click retries detached targets and scrolls the current button into view
  // before recording the geometry that the overlay must preserve.
  await actions.click({ trial: true });
  let before = await cardBoxes(page);

  await actions.click();
  const menu = page.getByTestId("node-actions-overlay");
  await expect(menu).toBeVisible();
  await expect(menu.getByTestId("open-server-details-link")).toBeVisible();
  const shot = process.env.NODE_CARDS_SHOTS;
  if (shot) await page.screenshot({ path: `${shot}/node-actions-menu.png` });
  expect(await cardBoxes(page)).toEqual(before);
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);

  // The failed deploy is one fixed line on its own card; its guidance opens on click.
  // Each control is brought into view first: the list may scroll to reach it,
  // the overlay itself must move nothing.
  const message = managed.getByTestId("node-message-toggle");
  await message.click({ trial: true });
  before = await cardBoxes(page);
  await message.click();
  const detail = page.getByTestId("node-message-overlay");
  await expect(detail).toBeVisible();
  if (shot)
    await page.screenshot({ path: `${shot}/node-failed-deploy-overlay.png` });
  expect(await cardBoxes(page)).toEqual(before);
  await page.keyboard.press("Escape");

  // Apps that do not fit the card's five tile rows open on demand.
  const crowded = page.getByTestId("node-row").filter({ hasText: "pve-main" });
  const moreToggle = crowded.getByTestId("node-row-more");
  await moreToggle.click({ trial: true });
  before = await cardBoxes(page);
  await moreToggle.click();
  const more = page.getByTestId("node-more-overlay");
  await expect(more.getByTestId("node-app-tile").first()).toBeVisible();
  if (shot) await page.screenshot({ path: `${shot}/node-more-overlay.png` });
  expect(await cardBoxes(page)).toEqual(before);
});
