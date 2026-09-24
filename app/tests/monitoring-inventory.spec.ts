import { test, expect, type BrowserContext } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

// The canonical, secret-free Node inventory is an observation surface: it
// lives on /monitoring, not on the dashboard (which shows the Nodes the
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
  allowed_actions: [],
  created_at: "2026-07-30T08:00:00Z",
  updated_at: "2026-07-30T09:00:00Z",
};

const canonicalService = {
  id: "svc-canonical-1",
  techstack_id: "techstack-1",
  kit_deployment_id: "kit-1",
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
  await context.route("**/api/v1/services**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [canonicalService] }),
    });
  });
}

test("monitoring renders the canonical inventory with its three axes", async ({
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
      body: JSON.stringify({ data: [inventoryServer] }),
    });
  });

  const page = await context.newPage();
  page.on("pageerror", (error) => console.error("browser page error", error));
  await page.goto(`${origin}/monitoring`);

  const card = page.getByTestId("monitoring-node-summary");
  await expect(card).toHaveCount(1);
  // The canonical id must stay addressable: the runtime smoke proves REST/DOM
  // parity through exactly this attribute.
  await expect(card).toHaveAttribute("data-server-id", inventoryServer.id);
  await expect(card).toContainText("homelab-foundation");
  // Lifecycle, connection and health are read together and never collapsed.
  await expect(card.getByTestId("monitoring-node-axis")).toHaveCount(3);
  await expect(card.locator('[data-axis="Conn"]')).toContainText(/connected/i);
  await expect(card.locator('[data-axis="Health"]')).toContainText(/healthy/i);
  await expect(card.locator('[data-axis="Life"]')).toContainText(/active/i);
  await expect(card.getByTestId("monitoring-node-service-count")).toContainText(
    /1 service/,
  );
  await expect(
    page.getByTestId("monitoring-inventory-unavailable"),
  ).toHaveCount(0);

  await context.close();
});

test("monitoring can detach a customer-operated leftover from the current fleet", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockMonitoringBase(context);

  const leftover = {
    ...inventoryServer,
    id: "srv-leftover-1",
    name: "homelab-8",
    allowed_actions: ["detach"],
  };
  let remaining = [leftover];
  await context.route("**/api/v1/servers**", async (route) => {
    const request = route.request();
    if (request.method() === "POST" && request.url().includes("/detach")) {
      remaining = [];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            server_id: leftover.id,
            agent_id: leftover.worker_id,
            revision: 4,
            generation: 1,
            detached_at: "2026-09-07T18:00:00Z",
            replay: false,
          },
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: remaining }),
    });
  });

  const page = await context.newPage();
  await page.goto(`${origin}/monitoring`);

  const card = page.getByTestId("monitoring-node-summary");
  await expect(card).toHaveCount(1);
  await expect(card).toContainText("homelab-8");
  await page.getByTestId("monitoring-node-detach").click();
  await expect(page.getByTestId("monitoring-detach-confirmation")).toBeVisible();
  await page.getByTestId("monitoring-detach-confirm").click();
  await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(0);
  await expect(page.getByTestId("monitoring-section-right-now")).toContainText(
    "No nodes have reported yet",
  );

  await context.close();
});

test("monitoring reports an unavailable inventory instead of an invented Node", async ({
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

  // A denied projection is a note in place, never a page error and never a
  // Node reconstructed from some other source.
  await expect(
    page.getByTestId("monitoring-inventory-unavailable"),
  ).toBeVisible();
  await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(0);
  await expect(page.getByTestId("monitoring-page")).toBeVisible();

  await context.close();
});

test("monitoring does not turn a successful empty inventory into an error", async ({
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

  // "Nothing enrolled" and "we could not read it" are different answers.
  await expect(
    page.getByTestId("monitoring-inventory-unavailable"),
  ).toHaveCount(0);
  await expect(page.getByTestId("monitoring-node-summary")).toHaveCount(0);
  await expect(page.getByTestId("monitoring-section-right-now")).toContainText(
    "No nodes have reported yet",
  );

  await context.close();
});
