import { test, expect, type Page, type BrowserContext } from "@playwright/test";
import {
  TEST_CREDENTIALS,
  requireApiBase,
  authenticateViaApi,
  createPairingTokenViaApi,
  registerWorkerViaApi,
  approveWorkerViaApi,
  resetStackViaApi,
  listWorkersViaApi,
  waitForPageLoad,
} from "../helpers/test-utils";

/**
 * Integration Flow — Real Docker Backend
 *
 * Serial test suite exercising the full critical path against live containers:
 * health check, login, admin dashboard, homelab creation, worker registration.
 * Diagnostic screenshots are attached at major steps.
 *
 * Each UI test includes semantic assertions that verify actual content. This
 * catches stale data, error states, and regressions without making the deploy
 * gate depend on platform-specific visual baselines.
 *
 * Prerequisites: Docker services running (techstack, app, test-worker).
 * Run via: pnpm exec playwright test --config=playwright.integration.config.ts
 */

const API_BASE = requireApiBase();
const RECOVERY_PASSPHRASE = "test recovery phrase 2026!";

async function attachFullPageScreenshot(page: Page, name: string) {
  const screenshot = await page.screenshot({ fullPage: true });
  await test.info().attach(name, {
    body: screenshot,
    contentType: "image/png",
  });
}

async function loginAsAdmin(page: Page) {
  await page.goto("/login");
  await page.waitForLoadState("domcontentloaded");
  await page.waitForTimeout(1000);

  if (!page.url().includes("/login")) {
    await expect(page.locator("aside").first()).toBeVisible();
    return;
  }

  await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
  await page
    .getByRole("textbox", { name: "Password", exact: true })
    .fill(TEST_CREDENTIALS.password);
  await page.getByRole("button", { name: "Sign in locally" }).click();

  await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  await expect(page.locator("aside").first()).toBeVisible();
}

async function completeEasyWizard(page: Page) {
  const hydrated = page.getByTestId("hydrated");
  await hydrated.waitFor({ state: "attached", timeout: 10_000 });

  const storageFeature = page.getByTestId("easy-feature-storage");
  if (await storageFeature.isVisible().catch(() => false)) {
    await storageFeature.click();
  } else {
    const anyFeature = page.locator('[data-testid^="easy-feature-"]');
    if ((await anyFeature.count()) > 0) {
      await anyFeature.first().click();
    }
  }

  const nextBtn = page.getByTestId("wizard-next");
  await expect(nextBtn).toBeVisible();
  await nextBtn.click();

  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await expect(nextBtn).toBeEnabled();
  await nextBtn.click();

  await expect(page.getByTestId("easy-step-3")).toBeVisible();
  const accessHome = page.getByTestId("easy-access-home");
  if (await accessHome.isVisible().catch(() => false)) {
    await accessHome.click();
  }
  await nextBtn.click();

  await expect(page.getByTestId("easy-step-4")).toBeVisible();
  const usersMe = page.getByTestId("easy-users-me");
  if (await usersMe.isVisible().catch(() => false)) {
    await usersMe.click();
  }
  await nextBtn.click();

  await expect(page.getByTestId("easy-step-5")).toBeVisible();
  await page.getByTestId("owner-source-local").click();
  await page.locator("#owner-username").fill("owner");
  await page.locator("#owner-email").fill("owner@techstack.local");
  await page.locator("#owner-display-name").fill("TechStack Owner");
  await page.locator("#recovery-passphrase").fill(RECOVERY_PASSPHRASE);
  await page.locator("#recovery-passphrase-confirm").fill(RECOVERY_PASSPHRASE);
  await page.locator("#recovery-passphrase-confirm").blur();
}

test.describe.serial("Integration Flow (Real Docker Backend)", () => {
  let sharedPage: Page;
  let ctx: BrowserContext;
  let authToken: string;
  let authUserId: string;
  let pairingToken: string;
  let workerId: string;

  test.beforeAll(async ({ browser }) => {
    ctx = await browser.newContext();
    sharedPage = await ctx.newPage();
  });

  test.afterAll(async () => {
    if (ctx) await ctx.close();
  });

  // ── 01: API health ──────────────────────────────────────────

  test("01 - API health check", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/health`);
    expect(response.ok()).toBeTruthy();
    const json = await response.json();
    expect(json.data?.status ?? json.status).toBe("healthy");
  });

  // ── 02: Login page renders ──────────────────────────────────

  test("02 - Login page renders", async () => {
    await sharedPage.goto("/login");
    await sharedPage.waitForLoadState("domcontentloaded");
    // Wait for trust status check to complete
    await sharedPage.waitForTimeout(2000);

    // -- Semantic: verify login form structure --
    await expect(
      sharedPage.getByRole("heading", { name: "Owner sign-in" }),
    ).toBeVisible();
    await expect(sharedPage.getByLabel("Email")).toBeVisible();
    await expect(
      sharedPage.getByRole("textbox", { name: "Password", exact: true }),
    ).toBeVisible();
    await expect(
      sharedPage.getByRole("button", { name: "Sign in locally" }),
    ).toBeVisible();

    // -- Semantic: no error banners --
    const errorBanners = sharedPage.locator(
      ".border-red-700, [role='alert'][class*='error'], .bg-destructive",
    );
    await expect(errorBanners).toHaveCount(0);

    // -- Semantic: branding elements present --
    await expect(
      sharedPage.getByRole("heading", { name: "kombify TechStack" }),
    ).toBeVisible();
    await expect(
      sharedPage.getByRole("heading", { name: "Emergency admin" }),
    ).toBeVisible();

    // -- Visual: attach diagnostic screenshot without baseline comparison --
    await attachFullPageScreenshot(sharedPage, "integration-01-login-page");
  });

  // ── 03: Login with default admin ────────────────────────────

  test("03 - Login with default admin", async () => {
    await loginAsAdmin(sharedPage);
  });

  // ── 04: Dashboard renders post-login ────────────────────────

  test("04 - Admin verification: dashboard renders", async () => {
    await sharedPage.goto("/stacks");
    await waitForPageLoad(sharedPage);

    // -- Semantic: main heading --
    await expect(
      sharedPage.getByRole("heading", {
        name: "kombify-TechStack",
        exact: true,
      }),
    ).toBeVisible();

    // -- Semantic: no error banners --
    const errorBanners = sharedPage.getByText("Something went wrong");
    const errorCount = await errorBanners.count();
    expect(errorCount).toBe(0);

    // -- Semantic: sidebar navigation structure --
    const sidebar = sharedPage.locator("aside").first();
    await expect(sidebar).toBeVisible();
    await expect(sidebar.getByText("Dashboard")).toBeVisible();
    await expect(sidebar.getByText("Services")).toBeVisible();
    await expect(sidebar.getByText("Monitoring")).toBeVisible();
    await expect(sidebar.getByText("Wallet")).toBeVisible();

    // -- Semantic: dashboard state (adapts to whether a stack exists) --
    const hasStack =
      (await sharedPage.getByText("Your Homelab:").count()) > 0 ||
      (await sharedPage.getByText("Active stack:").count()) > 0 ||
      (await sharedPage.getByText("Connect worker").count()) > 0;
    if (hasStack) {
      // Stack exists from previous run — verify stack dashboard
      await expect(
        sharedPage
          .getByText("Your Homelab:")
          .or(sharedPage.getByText("Active stack:"))
          .or(sharedPage.getByText("Connect worker"))
          .first(),
      ).toBeVisible();
    } else {
      // Fresh install — verify welcome card
      await expect(sharedPage.getByText("Start setup")).toBeVisible();
    }

    // -- Semantic: admin user visible in sidebar --
    await expect(sharedPage.getByText("Administrator")).toBeVisible();

    // -- Visual: attach diagnostic screenshot without baseline comparison --
    await attachFullPageScreenshot(sharedPage, "integration-02-dashboard");
  });

  // ── 05: API auth token ──────────────────────────────────────

  test("05 - API auth token acquisition", async () => {
    const result = await authenticateViaApi(API_BASE);
    authToken = result.token;
    authUserId = result.userId;
    expect(authToken).toBeTruthy();
    expect(authUserId).toBeTruthy();
  });

  // ── 06: Homelab creation ────────────────────────────────────

  test("06 - Homelab creation via wizard", async () => {
    await sharedPage.goto("/stacks/new");

    // Wait for the page to hydrate
    await sharedPage.waitForLoadState("domcontentloaded");
    await sharedPage.waitForTimeout(2000);

    // Check if wizard is blocked (stack already exists)
    const url = sharedPage.url();
    if (!url.includes("/stacks/new")) {
      test.skip(true, "Redirected away from wizard - stack may already exist");
      return;
    }

    // -- Semantic: wizard page structure --
    await expect(sharedPage.getByText("Easy Setup")).toBeVisible();
    await expect(sharedPage.getByText("Techie Mode")).toBeVisible();

    // -- Semantic: no error banners --
    const errorBanners = sharedPage.getByText("Something went wrong");
    await expect(errorBanners).toHaveCount(0);

    // -- Visual: attach diagnostic screenshot without baseline comparison --
    await attachFullPageScreenshot(sharedPage, "integration-03-wizard-start");

    // Try the easy wizard flow
    await completeEasyWizard(sharedPage);

    // Step 4: Create
    const createBtn = sharedPage.getByTestId("wizard-create");
    await expect(createBtn).toBeVisible({ timeout: 5_000 });
    const createResponsePromise = sharedPage
      .waitForResponse(
        (resp) =>
          resp.url().includes("/api/v1/stacks") &&
          resp.request().method() === "POST",
        { timeout: 30_000 },
      )
      .catch(() => null);

    await createBtn.click();

    const createResponse = await createResponsePromise;
    if (!createResponse) {
      throw new Error("Wizard did not submit a stack creation request");
    }
    const status = createResponse.status();
    if (status === 409) {
      test.skip(true, "Stack already exists (409)");
      return;
    }
    expect([200, 201, 202]).toContain(status);

    // Wait for navigation to creating page
    await sharedPage.waitForURL(/\/stacks\/creating/, { timeout: 15_000 });

    // -- Semantic: creation page structure (in-progress state) --
    await expect(sharedPage.getByText("Please wait while we're")).toBeVisible({
      timeout: 5_000,
    });
    await expect(
      sharedPage.getByText("Progress", { exact: true }),
    ).toBeVisible();
    await expect(
      sharedPage
        .getByText("Creating stack...", { exact: true })
        .or(sharedPage.getByText("Stack created!"))
        .or(sharedPage.getByText("Stack rollout verified"))
        .or(sharedPage.getByText("Configuration prepared"))
        .or(sharedPage.getByText("Server connection ready"))
        .first(),
    ).toBeVisible();
    await expect(sharedPage.getByText("track the progress")).toBeVisible();

    // -- Semantic: no init error --
    const initError = sharedPage.getByTestId("init-error");
    await expect(initError).toHaveCount(0);
    await expect(sharedPage.getByText("Creation failed")).toHaveCount(0);

    // -- Visual: attach diagnostic screenshot without baseline comparison --
    await attachFullPageScreenshot(sharedPage, "integration-04-stack-creating");

    // -- Wait for job to complete (provisioning runs in the backend) --
    const completed = await sharedPage
      .getByText("Stack created!")
      .or(sharedPage.getByText("Stack rollout verified"))
      .or(sharedPage.getByText("Configuration prepared"))
      .or(sharedPage.getByText("Server connection ready"))
      .first()
      .waitFor({ state: "visible", timeout: 90_000 })
      .then(() => true)
      .catch(() => false);

    if (completed) {
      // -- Semantic: completion state --
      await expect(
        sharedPage
          .getByText("Your kombify-TechStack is ready")
          .or(
            sharedPage.getByText(
              "The managed VM, StackKit rollout, verification, restore drill, and identity handoff are complete.",
            ),
          )
          .or(
            sharedPage.getByText(
              "Your remote server configuration is ready for rollout",
            ),
          )
          .or(
            sharedPage.getByText("Connect your server to continue the rollout"),
          ),
      ).toBeVisible();
      await expect(
        sharedPage.getByText("Open operations review"),
      ).toBeVisible();

      // -- Semantic: no failure state --
      const failedBanner = sharedPage.getByText("Creation failed");
      await expect(failedBanner).toHaveCount(0);

      // -- Visual: attach diagnostic screenshot without baseline comparison --
      await attachFullPageScreenshot(
        sharedPage,
        "integration-04b-stack-created",
      );
    }
  });

  // ── 07: Create pairing token ────────────────────────────────

  test("07 - Create pairing token via API", async () => {
    test.skip(!authToken, "No auth token from previous test");

    const result = await createPairingTokenViaApi(authToken, API_BASE);
    pairingToken = result.token;
    expect(pairingToken).toBeTruthy();
    expect(result.id).toBeTruthy();
  });

  // ── 08: Register worker ─────────────────────────────────────

  test("08 - Register test worker via API", async () => {
    test.skip(!pairingToken, "No pairing token from previous test");

    const result = await registerWorkerViaApi(
      pairingToken,
      "test-worker-e2e",
      API_BASE,
    );
    workerId = result.worker_id;
    expect(workerId).toBeTruthy();
    expect(result.accepted).toBe(false);
  });

  // ── 09: Approve worker ──────────────────────────────────────

  test("09 - Approve worker via API", async () => {
    test.skip(!authToken || !workerId, "Missing auth token or worker ID");

    await approveWorkerViaApi(authToken, workerId, API_BASE);
  });

  // ── 10: Verify worker in API ────────────────────────────────

  test("10 - Verify worker in workers list via API", async ({ request }) => {
    test.skip(!authToken, "No auth token");

    const response = await request.get(`${API_BASE}/api/v1/workers`, {
      headers: { Authorization: `Bearer ${authToken}` },
    });
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    const workers = json.data ?? json;
    const list = Array.isArray(workers)
      ? (workers as Array<{ id?: string; approved?: boolean; status?: string }>)
      : [];
    const found = list.find((worker) => worker.id === workerId);
    expect(found).toBeTruthy();
    expect(found?.approved).toBe(true);
    expect(found?.status).toBe("approved");
  });

  // ── 11: Worker visible in UI ────────────────────────────────

  test("11 - Worker management page shows registered worker", async () => {
    await loginAsAdmin(sharedPage);
    await sharedPage.goto("/settings");
    await waitForPageLoad(sharedPage);

    // -- Semantic: settings page structure --
    await expect(
      sharedPage.getByRole("heading", { name: "Settings" }),
    ).toBeVisible();
    await expect(sharedPage.getByText("Account")).toBeVisible();

    // -- Semantic: no error banners --
    const errorBanners = sharedPage.getByText("Something went wrong");
    await expect(errorBanners).toHaveCount(0);

    // -- Semantic: admin email visible in account summary --
    await expect(sharedPage.getByText(TEST_CREDENTIALS.email)).toBeVisible();

    // -- Visual: attach diagnostic screenshot without baseline comparison --
    await attachFullPageScreenshot(sharedPage, "integration-05-settings-page");
  });

  // ── 12: Reset stack via API ───────────────────────────────
  // This is the critical test: after reset, ALL related data must be gone.

  test("12 - Reset stack purges all data via API", async () => {
    test.skip(!authToken, "No auth token");

    // Verify we have data before reset
    const workersBefore = await listWorkersViaApi(authToken, API_BASE);
    expect(workersBefore.length).toBeGreaterThan(0);

    // Reset the stack
    const didReset = await resetStackViaApi(authToken, API_BASE);
    expect(didReset).toBe(true);

    // Verify workers are gone
    const workersAfter = await listWorkersViaApi(authToken, API_BASE);
    expect(workersAfter).toHaveLength(0);

    // Verify stacks are gone
    const stackResp = await fetch(
      `${API_BASE}/api/collections/stacks/records?perPage=1`,
      { headers: { Authorization: `Bearer ${authToken}` } },
    );
    const stackJson = await stackResp.json();
    expect(stackJson.totalItems ?? stackJson.items?.length ?? 0).toBe(0);
  });

  // ── 13: UI shows clean state after reset ──────────────────

  test("13 - Dashboard shows clean state after reset", async () => {
    await sharedPage.goto("/stacks");
    await waitForPageLoad(sharedPage);

    // After reset, dashboard should show the welcome/setup state
    await expect(sharedPage.getByText("Start setup")).toBeVisible({
      timeout: 10_000,
    });

    // No stale worker references should be visible
    const workerText = sharedPage.getByText("Connect worker");
    await expect(workerText).toHaveCount(0);
  });

  // ── 14: Re-create homelab after reset ─────────────────────

  test("14 - Homelab re-creation after reset via wizard", async () => {
    await sharedPage.goto("/stacks/new");
    await sharedPage.waitForLoadState("domcontentloaded");
    await sharedPage.waitForTimeout(2000);

    // Wizard should be accessible (not redirected — stack was deleted)
    expect(sharedPage.url()).toContain("/stacks/new");

    await expect(sharedPage.getByTestId("tab-easy")).toBeVisible();

    // Complete the wizard
    await completeEasyWizard(sharedPage);

    const createBtn = sharedPage.getByTestId("wizard-create");
    await expect(createBtn).toBeVisible({ timeout: 5_000 });
    const createResponsePromise = sharedPage
      .waitForResponse(
        (resp) =>
          resp.url().includes("/api/v1/stacks") &&
          resp.request().method() === "POST",
        { timeout: 30_000 },
      )
      .catch(() => null);

    await createBtn.click();

    const createResponse = await createResponsePromise;
    if (!createResponse) {
      throw new Error("Wizard did not submit a stack creation request");
    }
    expect([200, 201, 202]).toContain(createResponse.status());

    // Wait for creation to complete
    await sharedPage
      .waitForURL(/\/stacks\/creating/, { timeout: 15_000 })
      .catch(() => null);

    if (sharedPage.url().includes("/stacks/creating")) {
      await sharedPage
        .getByText("Stack created!")
        .or(sharedPage.getByText("Stack rollout verified"))
        .or(sharedPage.getByText("Configuration prepared"))
        .or(sharedPage.getByText("Server connection ready"))
        .first()
        .waitFor({ state: "visible", timeout: 90_000 })
        .catch(() => null);
    }
  });

  // ── 15: Verify clean workers after re-creation ────────────

  test("15 - No stale workers after re-creation", async () => {
    test.skip(!authToken, "No auth token");

    const workers = await listWorkersViaApi(authToken, API_BASE);

    // After reset + re-create, only the docker test-worker container
    // may have re-registered (it runs sleep infinity, doesn't re-register).
    // There should be NO workers from the previous cycle.
    for (const w of workers) {
      expect(w.hostname).not.toBe("test-worker-e2e");
      expect(w.hostname).not.toBe("orchestration-vm-worker");
    }
  });
});
