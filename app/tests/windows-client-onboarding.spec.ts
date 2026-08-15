import { expect, test, type Page } from "@playwright/test";

async function mockOperatorDashboardApis(page: Page) {
  await page.route("**/api/v1/wallet**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/stacks**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/workers**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/features", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { beta: [], security: [], ux: [] } }),
    });
  });
  await page.route("**/api/v1/auth/stack-identity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: null }),
    });
  });
}

test("windows client local setup creates a real owner session and reaches Wallet plus Creation Wizard", async ({
  page,
}) => {
  let setupCalled = false;
  let loginCalled = false;

  await page.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: true,
          cloud_auth_url: null,
          portal_url: null,
          allow_local_login: true,
        },
      }),
    });
  });
  await page.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await page.route("**/api/v2/whoami", async (route) => {
    if (!loginCalled) {
      await route.fulfill({ status: 401, body: "{}" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "local-owner",
        tenantId: "default",
        email: "owner@test.local",
        provider: "local",
        role: "admin",
      }),
    });
  });
  await page.route("**/api/v1/csrf", async (route) => {
    await route.fulfill({
      status: 200,
      headers: { "X-CSRF-Token": "test-csrf" },
      contentType: "application/json",
      body: JSON.stringify({ token: "test-csrf" }),
    });
  });
  await page.route("**/api/v1/auth/setup", async (route) => {
    setupCalled = true;
    expect(route.request().postDataJSON()).toMatchObject({
      mode: "local",
      name: "owner",
      email: "owner@test.local",
      password: "testpass123",
    });
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        message: "Setup complete",
        mode: "local",
      }),
    });
  });
  await page.route("**/api/v1/auth/login", async (route) => {
    loginCalled = true;
    expect(route.request().postDataJSON()).toMatchObject({
      email: "owner@test.local",
      password: "testpass123",
    });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ok: true,
        email: "owner@test.local",
        provider: "local",
      }),
    });
  });
  await page.route("**/api/v1/wallet**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/stacks**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/workers**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });
  await page.route("**/api/v1/features", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { beta: [], security: [], ux: [] } }),
    });
  });
  await page.route("**/api/v1/auth/stack-identity", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: null }),
    });
  });

  await page.goto("/client/local?client=windows", {
    waitUntil: "domcontentloaded",
  });

  await expect(page.getByRole("button", { name: "Open Wallet" })).toHaveCount(
    0,
  );
  await expect(
    page.getByRole("button", { name: "Start Creation Wizard" }),
  ).toHaveCount(0);

  await page.getByTestId("windows-local-admin-email").fill("owner@test.local");
  await page.getByTestId("windows-local-admin-password").fill("testpass123");
  await page.getByTestId("windows-local-setup-submit").click();

  await expect(page).toHaveURL(/\/stacks$/);
  expect(setupCalled).toBe(true);
  expect(loginCalled).toBe(true);
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();

  await page.getByRole("link", { name: "Wallet" }).click();
  await expect(page).toHaveURL(/\/wallet$/);
  await expect(page.locator("[data-wallet-tab-nav]")).toBeVisible();

  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page).toHaveURL(/\/stacks$/);
  await page.getByRole("link", { name: "Get started" }).click();
  await expect(page).toHaveURL(/\/stacks\/new$/);
  await expect(page.getByTestId("easy-wizard")).toBeVisible();
});

test("windows client with existing local owner signs in without the legacy login page", async ({
  page,
}) => {
  let loginCalled = false;

  await page.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: null,
          allow_local_login: true,
        },
      }),
    });
  });
  await page.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await page.route("**/api/v2/whoami", async (route) => {
    if (!loginCalled) {
      await route.fulfill({ status: 401, body: "{}" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "local-owner",
        tenantId: "default",
        email: "owner@test.local",
        provider: "local",
        role: "admin",
      }),
    });
  });
  await page.route("**/api/v1/csrf", async (route) => {
    await route.fulfill({
      status: 200,
      headers: { "X-CSRF-Token": "test-csrf" },
      contentType: "application/json",
      body: JSON.stringify({ token: "test-csrf" }),
    });
  });
  await page.route("**/api/v1/auth/login", async (route) => {
    loginCalled = true;
    expect(route.request().postDataJSON()).toMatchObject({
      email: "owner@test.local",
      password: "testpass123",
    });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ok: true,
        email: "owner@test.local",
        provider: "local",
      }),
    });
  });
  await mockOperatorDashboardApis(page);

  await page.goto("/client/local?client=windows", {
    waitUntil: "domcontentloaded",
  });

  await expect(page.getByText("Local owner sign-in")).toBeVisible();
  await expect(
    page.locator('a[href="/login?manual=1&client=windows"]'),
  ).toHaveCount(0);
  await expect(page.getByTestId("windows-local-admin-email")).toHaveCount(0);
  await expect(page.getByTestId("windows-local-admin-password")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open Wallet" })).toHaveCount(
    0,
  );
  await expect(
    page.getByRole("button", { name: "Start Creation Wizard" }),
  ).toHaveCount(0);

  await page
    .getByTestId("windows-local-existing-email")
    .fill("owner@test.local");
  await page.getByTestId("windows-local-existing-password").fill("testpass123");
  await page.getByTestId("windows-local-existing-submit").click();

  await expect(page).toHaveURL(/\/stacks$/);
  expect(loginCalled).toBe(true);
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(() =>
        window.localStorage.getItem("techstack.windowsClientContext"),
      ),
    )
    .toBe("local");
});

test("windows client with active local session opens dashboard and logs out to local sign-in", async ({
  page,
}) => {
  let loggedIn = true;

  await page.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: null,
          allow_local_login: true,
        },
      }),
    });
  });
  await page.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await page.route("**/api/v2/whoami", async (route) => {
    if (!loggedIn) {
      await route.fulfill({ status: 401, body: "{}" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "local-owner",
        tenantId: "default",
        email: "owner@test.local",
        provider: "local",
        role: "admin",
      }),
    });
  });
  await page.route("**/api/v1/csrf", async (route) => {
    await route.fulfill({
      status: 200,
      headers: { "X-CSRF-Token": "test-csrf" },
      contentType: "application/json",
      body: JSON.stringify({ token: "test-csrf" }),
    });
  });
  await page.route("**/api/v1/auth/logout", async (route) => {
    loggedIn = false;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ok: true }),
    });
  });
  await mockOperatorDashboardApis(page);

  await page.goto("/client/local?client=windows", {
    waitUntil: "domcontentloaded",
  });

  await expect(page).toHaveURL(/\/stacks$/);
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();

  await page.getByRole("button", { name: /owner@test\.local/i }).click();
  await page.getByRole("button", { name: "Logout" }).click();

  await expect(page).toHaveURL(/\/client\/local\?client=windows$/);
  await expect(page.getByText("Local owner sign-in")).toBeVisible();
});

test("windows cloud login exposes default-browser handoff for password managers", async ({
  page,
}) => {
  await page.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: "/api/v2/auth/login",
          portal_url: null,
          allow_local_login: false,
        },
      }),
    });
  });
  await page.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        providers: [{ id: "auth0", kind: "auth0", issuer: "login.kombify.io" }],
      }),
    });
  });
  await page.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({ status: 401, body: "{}" });
  });
  await page.route("**/api/v1/auth/methods", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        providers: [
          {
            id: "auth0",
            kind: "auth0",
            label: "kombify Cloud",
            auth_url: "/api/v2/auth/login",
          },
        ],
        breakglass: {
          initialized: false,
          claimed: false,
          email: "",
          has_pending_reveal: false,
          reveal_expires_at: null,
          locked: false,
        },
      }),
    });
  });

  await page.goto("/login?manual=1&client=windows", {
    waitUntil: "domcontentloaded",
  });

  await expect(
    page.getByRole("button", { name: "Continue with kombify Cloud" }),
  ).toBeVisible();
  const browserLogin = page.getByTestId("windows-browser-cloud-login");
  await expect(browserLogin).toBeVisible();
  await expect(browserLogin).toHaveText("Open kombify Cloud in browser");

  const href = await browserLogin.getAttribute("href");
  expect(href).toBeTruthy();
  const url = new URL(href!, page.url());
  expect(url.pathname).toBe("/api/v2/auth/login");
  expect(url.searchParams.get("return_to")).toBe("/stacks");
  expect(url.searchParams.get("client")).toBe("windows");
  expect(url.searchParams.get("open_browser")).toBe("1");
});
