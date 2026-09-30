import { type Page } from "@playwright/test";
import { test, expect } from "./fixtures";

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
  await page.route("**/api/v1/homelab", async (route) => {
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ reason_code: "homelab_not_found" }),
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

  await expect(page).toHaveURL(/\/dashboard$/);
  expect(setupCalled).toBe(true);
  expect(loginCalled).toBe(true);
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();

  await page.getByRole("link", { name: "Wallet" }).click();
  await expect(page).toHaveURL(/\/wallet$/);
  await expect(page.locator("[data-wallet-tab-nav]")).toBeVisible();

  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await page.getByRole("button", { name: "Get started" }).click();
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

  await expect(page).toHaveURL(/\/dashboard$/);
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

test("local logout renews the wizard attempt while retries keep their request key", async ({
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

  let signedInAgain = false;
  await page.route("**/api/v1/auth/login", async (route) => {
    loggedIn = true;
    signedInAgain = true;
    await route.fulfill({ json: { ok: true, provider: "local" } });
  });
  await page.route("**/api/v1/stacks/stack-1", async (route) => {
    await route.fulfill({
      json: {
        data: {
          id: "stack-1",
          name: "Local Stack",
          provider: "local",
          state: "running",
          stackkit_catalog_ref: "basement-kit",
        },
      },
    });
  });
  await page.route("**/api/v1/servers", async (route) => {
    await route.fulfill({ json: { data: [] } });
  });
  await page.route("**/api/v1/wizard/runs/active", async (route) => {
    await route.fulfill({ json: { data: { run: null } } });
  });
  const retryMessage = "Local registration is temporarily unavailable";
  const acceptedJob = "job-after-local-login";
  const acceptedMessage = "New local registration accepted";
  await page.route("**/api/v1/wizard/runs", async (route) => {
    if (!signedInAgain) {
      await route.fulfill({
        status: 503,
        json: { error: { code: "service_unavailable", message: retryMessage } },
      });
      return;
    }
    await route.fulfill({
      status: 202,
      json: {
        data: {
          run_id: "run-after-local-login",
          run_kind: "expansion",
          kit_assignment_mode: "join",
          stack_id: "stack-1",
          job_id: acceptedJob,
          state: "pending",
        },
      },
    });
  });
  await page.route(`**/api/v1/jobs/${acceptedJob}`, async (route) => {
    await route.fulfill({
      json: {
        data: {
          id: acceptedJob,
          type: "update",
          state: "running",
          message: acceptedMessage,
          result: { stack_id: "stack-1", creation_operation: "add-server" },
        },
      },
    });
  });
  async function submitJoinAttempt() {
    await page.goto("/stacks/stack-1/servers/new");
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("server-branch-owned").click();
    await page.getByTestId("server-mode-install-command").click();
    const submitted = page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname === "/api/v1/wizard/runs" &&
        request.method() === "POST",
    );
    await page.getByTestId("wizard-create").click();
    return (await submitted).headers()["x-idempotency-key"];
  }

  await page.goto("/client/local?client=windows", {
    waitUntil: "domcontentloaded",
  });

  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();

  const attemptKey = await submitJoinAttempt();
  await expect(page.getByText(retryMessage).first()).toBeVisible();
  expect(await submitJoinAttempt()).toBe(attemptKey);
  await expect(page.getByText(retryMessage).first()).toBeVisible();
  await page.goto("/dashboard");

  await page.getByRole("button", { name: /owner@test\.local/i }).click();
  await page.getByRole("menuitem", { name: "Logout" }).click();

  await expect(page).toHaveURL(/\/client\/local\?client=windows$/);
  await expect(page.getByText("Local owner sign-in")).toBeVisible();
  await page
    .getByTestId("windows-local-existing-email")
    .fill("owner@test.local");
  await page.getByTestId("windows-local-existing-password").fill("testpass123");
  await page.getByTestId("windows-local-existing-submit").click();
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  expect(await submitJoinAttempt()).not.toBe(attemptKey);
  await expect(page).toHaveURL(new RegExp(`job_id=${acceptedJob}`));
  await expect(page.getByText(acceptedMessage).first()).toBeVisible();
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
  expect(url.searchParams.get("return_to")).toBe("/dashboard");
  expect(url.searchParams.get("client")).toBe("windows");
  expect(url.searchParams.get("open_browser")).toBe("1");
});
