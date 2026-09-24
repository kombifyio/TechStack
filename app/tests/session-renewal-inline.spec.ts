import { expect, test } from "@playwright/test";

test("standalone SaaS keeps an exhausted gateway renewal inline", async ({
  page,
}) => {
  let dashboardLoads = 0;

  await page.addInitScript(() => {
    window.sessionStorage.setItem(
      "techstack:auth:spa_gateway_login_at",
      String(Date.now()),
    );
  });

  await page.route("**/api/**", async (route) => {
    const { pathname } = new URL(route.request().url());
    let status = 200;
    let body: unknown = { data: {} };

    if (pathname === "/api/v1/client/bootstrap") {
      body = {
        data: {
          edition: "cloud",
          deployment_mode: "saas",
          kombify_edition: "saas-standalone",
        },
      };
    } else if (pathname === "/api/v1/auth/mode") {
      body = {
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: "/api/v2/auth/login",
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      };
    } else if (pathname === "/api/v2/auth/providers") {
      body = {
        providers: [
          {
            id: "primary",
            kind: "auth0",
            label: "kombify Cloud",
            issuer: "https://login.kombify.io/",
          },
        ],
      };
    } else if (pathname === "/api/v2/whoami") {
      body = {
        subject: "cloud-owner",
        tenantId: "owner-tenant",
        email: "owner@example.com",
        provider: "primary",
        role: "owner",
      };
    } else if (pathname === "/api/v1/workers") {
      dashboardLoads += 1;
      status = 401;
      body = {
        error: {
          code: "gateway_auth_unavailable",
          message: "Gateway authentication unavailable",
        },
      };
    } else if (pathname === "/api/v1/homelab") {
      body = { data: { homelab: null, kit_deployments: [] } };
    } else if (pathname === "/api/v1/wizard/runs/active") {
      body = { data: { run: null } };
    }

    await route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });

  await page.goto("/dashboard");

  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByTestId("session-renewal-panel")).toBeVisible();
  await expect(page.getByTestId("session-renewal-banner")).toHaveCount(0);

  await page.getByTestId("session-renewal-signin").click();
  await page.waitForURL(/\/api\/v2\/auth\/login\?.*return_to=%2Fdashboard/);
  expect(page.url()).toContain("prompt=login");
});
