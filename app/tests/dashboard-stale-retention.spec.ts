import { test, expect, type BrowserContext } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

// A dashboard load that lands in a deploy-cutover window (edge 502, gateway
// timeout) must keep the last verified fleet on screen, say it is stale, and
// retry on its own. Wiping the lists made every cutover look like a reset
// homelab (platform-papqg).

const WORKERS = [
  {
    id: "worker_stale_1",
    hostname: "homelab-foundation",
    status: "online",
    last_seen: "2026-08-30T11:40:00Z",
    approved: true,
  },
];

async function mockAuthBase(context: BrowserContext) {
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
}

test("dashboard keeps the last verified fleet when a refresh hits an outage window", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockAuthBase(context);

  let outage = false;
  const failWhenOut = async (
    route: Parameters<Parameters<BrowserContext["route"]>[1]>[0],
    body: string,
  ) => {
    if (outage) {
      await route.fulfill({
        status: 502,
        contentType: "text/html",
        body: "<html>Bad Gateway</html>",
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body,
    });
  };
  await context.route("**/api/v1/workers", (route) =>
    failWhenOut(route, JSON.stringify({ data: WORKERS })),
  );
  // The refresh button and the fleet sections render only once a deployment
  // exists, so the fixture carries one.
  await context.route("**/api/v1/homelab", (route) =>
    failWhenOut(
      route,
      JSON.stringify({
        data: {
          id: "homelab-1",
          kit_deployments: [
            {
              id: "e0722731-a7cf-5e0d-9a65-55851b5726f6",
              name: "Demo Homelab",
              kit: "homelab-foundation",
              state: "deployed",
              services: [],
              created_at: "2026-08-01T09:00:00Z",
              updated_at: "2026-08-30T09:00:00Z",
            },
          ],
        },
      }),
    ),
  );

  const page = await context.newPage();
  await page.goto(`${origin}/dashboard`, { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0, {
    timeout: 20_000,
  });
  const initialWorkers = await page
    .locator('[data-testid="worker-connected-count"], main')
    .first()
    .textContent();
  expect(initialWorkers).toBeTruthy();

  outage = true;
  await page.getByTestId("dashboard-refresh-button").click();

  // The last verified state stays; the failure is a stale note, not a wipe.
  await expect(page.getByTestId("stacks-stale-notice")).toBeVisible({
    timeout: 20_000,
  });
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0);

  // The self-retry recovers without any user action once the window passes.
  outage = false;
  await expect(page.getByTestId("stacks-stale-notice")).toHaveCount(0, {
    timeout: 20_000,
  });

  await context.close();
});
