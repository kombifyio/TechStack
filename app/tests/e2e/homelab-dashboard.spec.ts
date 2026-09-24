import { expect, test, type Page, type Route } from "@playwright/test";

const deploymentA = "stack-a";

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
      apiEnvelope([deploymentRecord(deploymentA, "basement")]),
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
        kit_deployments: [deploymentRecord(deploymentA, "basement")],
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

// The kombify Cloud Homelab Identity is pushed into the session, not chosen on
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
  test("redirects the legacy collection route to the Homelab dashboard", async ({
    page,
  }) => {
    await mockHomelabApi(page);
    await page.goto("/stacks?legacy=1");
    await expect(page).toHaveURL(/\/dashboard\?legacy=1$/);
  });

  test("a renamed homelab titles the dashboard over the Homelab Identity", async ({
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
    await page.goto("/dashboard");

    await expect(
      page.getByTestId("homelab-header").getByRole("heading"),
    ).toHaveText("Basement Lab");
  });

  test("the Homelab Identity titles the dashboard while the name is generated", async ({
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
    await page.goto("/dashboard");

    await expect(
      page.getByTestId("homelab-header").getByRole("heading"),
    ).toHaveText("Nebula Fox");
  });

  test("a rename to the generated word still wins over the Homelab Identity", async ({
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
    await page.goto("/dashboard");

    await expect(
      page.getByTestId("homelab-header").getByRole("heading"),
    ).toHaveText("homelab");
  });
});
