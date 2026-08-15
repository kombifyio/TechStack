import { expect, test, type Page, type Route } from "@playwright/test";

const deploymentA = "stack-a";
const deploymentB = "stack-b";

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

function deploymentRecord(id: string, name: string) {
  return {
    id,
    name,
    mode: "easy",
    status: "pending",
    state: "pending",
    provider: "local",
    services: ["pocket_id", "traefik"],
    created: "2026-07-29T09:00:00Z",
    updated: "2026-07-29T09:00:00Z",
    created_at: "2026-07-29T09:00:00Z",
    updated_at: "2026-07-29T09:00:00Z",
  };
}

async function mockHomelabApi(page: Page) {
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
      const origin =
        route.request().headers()["origin"] ??
        process.env.PLAYWRIGHT_BASE_URL ??
        "http://127.0.0.1:5261";
      await route.fulfill({
        status: 204,
        headers: {
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,OPTIONS",
          "access-control-allow-origin": origin,
          "access-control-allow-credentials": "true",
        },
      });
      return;
    }
    await route.fallback();
  });

  await page.route("**/api/v1/auth/mode", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        allow_local_login: true,
      }),
    );
  });

  await page.route("**/api/v2/whoami", async (route) => {
    await fulfillJson(route, {
      user: { id: "owner-1", email: "owner@example.com", name: "Owner" },
      source: "local",
    });
  });

  await page.route("**/api/v1/features", async (route) => {
    await fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] }));
  });

  await page.route("**/api/v1/tunnel/registry-url", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({ url: "http://localhost:5260", mode: "local" }),
    );
  });

  await page.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(
      route,
      apiEnvelope([
        deploymentRecord(deploymentA, "basement"),
        deploymentRecord(deploymentB, "cloud-edge"),
      ]),
    );
  });

  await page.route("**/api/v1/homelab", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        homelab: {
          id: "hl-1",
          name: "My Homelab",
          intent: { goals: ["photos"] },
          created: "2026-07-29T09:00:00Z",
          updated: "2026-07-29T09:00:00Z",
        },
        kit_deployments: [
          deploymentRecord(deploymentA, "basement"),
          deploymentRecord(deploymentB, "cloud-edge"),
        ],
      }),
    );
  });

  await page.route("**/api/v1/workers", async (route) => {
    await fulfillJson(route, apiEnvelope([]));
  });

  await page.route("**/api/v1/inventory/servers**", async (route) => {
    await fulfillJson(route, apiEnvelope({ servers: [] }));
  });

  await page.route("**/api/v1/jobs**", async (route) => {
    await fulfillJson(route, apiEnvelope({ items: [] }));
  });

  await page.route("**/api/v1/stacks/*/operations", async (route) => {
    await fulfillJson(
      route,
      { code: 404, message: "operations unavailable", data: {} },
      404,
    );
  });
}

// The kombify Cloud Stack Identity is pushed into the session, not chosen on
// this page. It may title the dashboard only while the homelab still carries
// its generated name - otherwise the Settings rename would be a silent no-op.
async function mockStackIdentity(page: Page, name: string) {
  await page.route("**/api/v1/auth/stack-identity", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        stack_identity: {
          name,
          characterId: "aurora",
          animationStyle: "pulse",
          animationEnabled: true,
          iconStyle: "solid",
          savedAt: "2026-07-29T09:00:00Z",
        },
        editable: false,
      }),
    );
  });
}

async function mockHomelabName(page: Page, homelab: Record<string, unknown>) {
  await page.route("**/api/v1/homelab", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        homelab,
        kit_deployments: [deploymentRecord(deploymentA, "basement")],
      }),
    );
  });
}

test.describe("homelab umbrella dashboard", () => {
  test("shows the umbrella header with kit-deployment sections", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await page.goto("/stacks");

    const header = page.getByTestId("homelab-header");
    await expect(header).toBeVisible();
    await expect(header).toContainText("My Homelab");
    // The header names the homelab and the deployment on screen. The kit
    // deployment count is deliberately absent: it exposed an internal grouping
    // the dashboard neither explains nor lets an operator act on.
    await expect(page.getByTestId("homelab-deployment-count")).toHaveCount(0);
    await expect(header).not.toContainText("kit deployment");

    const sections = page.getByTestId("homelab-deployment-sections");
    await expect(sections).toBeVisible();
    await expect(
      page.getByTestId(`homelab-deployment-${deploymentA}`),
    ).toContainText("basement");
    await expect(
      page.getByTestId(`homelab-deployment-${deploymentB}`),
    ).toContainText("cloud-edge");

    // The resolver default pins the first listed deployment.
    await expect(page.getByTestId("current-deployment-name")).toContainText(
      "basement",
    );
  });

  test("switching sections pins the deployment and updates the deep link", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await page.goto("/stacks");

    await page.getByTestId(`homelab-deployment-${deploymentB}`).click();

    await expect(page.getByTestId("current-deployment-name")).toContainText(
      "cloud-edge",
    );
    await expect(page).toHaveURL(new RegExp(`stack_id=${deploymentB}`));

    // The ?stack_id deep link keeps winning on reload.
    await page.reload();
    await expect(page.getByTestId("current-deployment-name")).toContainText(
      "cloud-edge",
    );
  });

  test("a renamed homelab titles the dashboard over the Stack Identity", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await mockStackIdentity(page, "Nebula Fox");
    await mockHomelabName(page, {
      id: "hl-1",
      name: "Basement Lab",
      named: true,
      intent: {},
      created: "2026-07-29T09:00:00Z",
      updated: "2026-07-29T09:00:00Z",
    });
    await page.goto("/stacks");

    await expect(page.getByTestId("homelab-title")).toHaveText("Basement Lab");
  });

  test("the Stack Identity titles the dashboard while the name is generated", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await mockStackIdentity(page, "Nebula Fox");
    await mockHomelabName(page, {
      id: "hl-1",
      name: "homelab",
      named: false,
      intent: {},
      created: "2026-07-29T09:00:00Z",
      updated: "2026-07-29T09:00:00Z",
    });
    await page.goto("/stacks");

    await expect(page.getByTestId("homelab-title")).toHaveText("Nebula Fox");
  });

  test("a rename to the generated word still wins over the Stack Identity", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await mockStackIdentity(page, "Nebula Fox");
    await mockHomelabName(page, {
      id: "hl-1",
      name: "homelab",
      named: true,
      intent: {},
      created: "2026-07-29T09:00:00Z",
      updated: "2026-07-29T09:00:00Z",
    });
    await page.goto("/stacks");

    await expect(page.getByTestId("homelab-title")).toHaveText("homelab");
  });
});
