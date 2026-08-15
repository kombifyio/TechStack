import { test, expect, type BrowserContext } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

// The canonical, secret-free server inventory is an observation surface: it
// lives on /monitoring, not on the dashboard (which shows the servers the
// operator acts on). Its read is separately authorized (signed entitlement +
// FGA), so an unavailable projection must be reported in place instead of
// breaking the page.

const inventoryServer = {
  id: "srv-canonical-1",
  techstack_id: "techstack-1",
  name: "homelab-foundation",
  worker_id: "agent-canonical-1",
  inventory_revision: 7,
  provider: { ref: "hostinger" },
  environment_class: "cloud",
  offering: "external_vps",
  provider_id: "hostinger",
  availability_owner: "provider",
  operations_owner: "customer",
  target_evidence: {
    ref: "provider:hostinger",
    observed_at: "2026-07-30T09:00:00Z",
    freshness: { state: "recorded", age_seconds: 4 },
  },
  health: { state: "healthy", observed_at: "2026-07-30T09:00:00Z" },
  connection: {
    state: "connected",
    changed_at: "2026-07-30T09:00:00Z",
    last_heartbeat_at: "2026-07-30T09:00:00Z",
  },
  lifecycle: { state: "active", desired_state: "running" },
  channels: [],
  mutations_allowed: true,
  created_at: "2026-07-30T08:00:00Z",
  updated_at: "2026-07-30T09:00:00Z",
};

const canonicalService = {
  id: "svc-canonical-1",
  techstack_id: "techstack-1",
  server_id: inventoryServer.id,
  target_kind: "server",
  placement: {
    evidence_ref: "guard:inventory-7",
    observed_at: "2026-07-30T09:00:00Z",
    freshness: { state: "recorded", age_seconds: 4 },
  },
  service_key: "grafana",
  service_instance: "default",
  name: "Grafana",
  management_state: "observed",
  desired_state: "unknown",
  observed_state: "running",
  health: { state: "healthy", observed_at: "2026-07-30T09:00:00Z" },
  access: {},
  allowed_actions: [],
  inventory_revision: 7,
  source: "guard",
  provenance: {},
  created_at: "2026-07-30T08:00:00Z",
  updated_at: "2026-07-30T09:00:00Z",
};

const cockpitServer = {
  id: inventoryServer.id,
  agent_id: "agent-canonical-1",
  hostname: inventoryServer.name,
  role: "foundation",
  source: "agent",
  ip: "85.215.38.99",
  health: {
    state: "healthy",
    cpu_percent: { value: 12, unit: "%", status: "ok" },
    memory_percent: { value: 38, unit: "%", status: "ok" },
    disk_percent: { value: 41, unit: "%", status: "ok" },
  },
  capabilities: { provider: "Hostinger VPS" },
};

async function mockMonitoringBase(context: BrowserContext) {
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "owner",
      }),
    });
  });
  await context.route("**/api/v1/monitor/cockpit**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          techstack_id: "techstack-1",
          stacks: [{ id: "techstack-1", name: "Demo Homelab" }],
          servers: [cockpitServer],
          services: [
            {
              id: canonicalService.id,
              name: canonicalService.name,
              type: canonicalService.service_key,
              status: "healthy",
              target_server_id: inventoryServer.id,
              target_server: inventoryServer.name,
            },
          ],
          jobs: [],
          alerts: [],
          kpis: {},
          readiness: { connected_servers: 0 },
          monitoring: { status: "ok", collectorMode: "otlp" },
        },
      }),
    });
  });
  await context.route("**/api/v1/services**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [canonicalService] }),
    });
  });
}

test("monitoring joins canonical inventory and telemetry into one server list", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockMonitoringBase(context);
  const inventoryURLs: string[] = [];
  await context.route("**/api/v1/servers**", async (route) => {
    inventoryURLs.push(route.request().url());
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [inventoryServer] }),
    });
  });

  const page = await context.newPage();
  page.on("pageerror", (error) => console.error("browser page error", error));
  await page.goto(`${origin}/monitoring`);

  const list = page.getByTestId("inventory-server-list");
  await expect(list).toBeVisible();
  const card = page.getByTestId("inventory-server-card");
  await expect(card).toHaveCount(1);
  await expect(card.locator("article.kf-card")).toHaveCount(1);
  // The canonical id must stay addressable: the runtime smoke proves REST/DOM
  // parity through exactly this attribute.
  await expect(card).toHaveAttribute("data-server-id", inventoryServer.id);
  await expect(card).toContainText("homelab-foundation");
  await expect(card).toContainText("85.215.38.99");
  await expect(card).toContainText("cloud");
  await expect(card).toContainText("external vps");
  await expect(page.getByTestId("inventory-unavailable")).toHaveCount(0);
  // The matching telemetry row enriches this card rather than becoming a
  // second server card.
  await expect(card).toHaveCount(1);
  const serviceCard = page.getByTestId("monitoring-service-card");
  await expect(serviceCard).toHaveCount(1);
  await expect(serviceCard.locator("article.kf-card, [role='article'].kf-compact")).toHaveCount(1);
  expect(inventoryURLs.length).toBeGreaterThan(0);
  for (const requestURL of inventoryURLs) {
    const inventoryQuery = new URL(requestURL).searchParams;
    expect(inventoryQuery.get("techstack_id")).toBe("techstack-1");
    expect(inventoryQuery.has("stack_id")).toBe(false);
  }

  await context.close();
});

test("monitoring keeps telemetry visible when canonical inventory is unavailable", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockMonitoringBase(context);
  await context.route("**/api/v1/servers**", async (route) => {
    await route.fulfill({
      status: 403,
      contentType: "application/json",
      body: JSON.stringify({
        error: {
          code: "FORBIDDEN",
          message: "Inventory access denied",
          details: { reason_code: "inventory_access_denied" },
        },
      }),
    });
  });

  const page = await context.newPage();
  page.on("pageerror", (error) => console.error("browser page error", error));
  await page.goto(`${origin}/monitoring`);

  // A denied projection is a note on the inventory card, never a page error.
  await expect(page.getByTestId("inventory-unavailable")).toBeVisible();
  await expect(page.getByTestId("inventory-server-card")).toHaveCount(1);
  await expect(page.getByTestId("inventory-server-card")).toHaveAttribute(
    "data-server-id",
    inventoryServer.id,
  );
  // Never render a false zero beside live server telemetry.
  await expect(page.getByTestId("inventory-server-list")).not.toContainText(
    "0 servers",
  );

  await context.close();
});

test("monitoring does not turn a successful empty inventory into a telemetry server", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockMonitoringBase(context);
  await context.route("**/api/v1/servers**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  const page = await context.newPage();
  await page.goto(`${origin}/monitoring`);

  await expect(page.getByTestId("inventory-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("inventory-server-card")).toHaveCount(0);
  await expect(page.getByTestId("inventory-server-list")).toContainText(
    "No servers recorded yet",
  );

  await context.close();
});
