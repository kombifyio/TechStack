import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";
/**
 * Sequential Dependency Tests
 *
 * Tests complex workflows where actions depend on previous states.
 * Each test verifies that the application correctly handles state transitions
 * and dependencies between different settings and operations.
 */

test.describe("Sequential Dependency - Settings Surface", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("minimal settings sections remain reachable across navigation", async ({
    page,
  }) => {
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Stack Identity" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Danger Zone" }),
    ).toBeVisible();

    await page.goto("/stacks");
    await page.waitForLoadState("networkidle");

    await page.goto("/settings");
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
  });
});

test.describe("Sequential Dependency - Wizard Flow", () => {
  test("wizard step dependencies and data flow", async ({
    browser,
    baseURL,
  }) => {
    // Setup mocked context
    const origin = baseURL || "http://localhost:5173";
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });

    // Mock validation endpoint
    await context.route(
      "**/api/v1/unifier/pipeline/preview**",
      async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            valid: true,
            resolved_stackkit: "test/stackkit",
            detected_addons: [],
            stages: [],
          }),
        });
      },
    );

    // Mock stack creation
    const submittedPayloads: Array<{
      mode?: string;
      stack_spec?: {
        metadata?: { access_mode?: string };
        vpn?: unknown;
        services?: { pocketid?: unknown };
      };
    }> = [];
    await context.route("**/api/v1/stacks**", async (route) => {
      const request = route.request();
      if (request.method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [] }),
        });
        return;
      }

      if (request.method() === "POST") {
        submittedPayloads.push(request.postDataJSON());
        await route.fulfill({
          status: 202,
          contentType: "application/json",
          body: JSON.stringify({
            stack_id: "stack_test",
            job_id: "job_test",
            name: "Test Stack",
            state: "provisioning",
            message: "Stack created",
          }),
        });
      }
    });

    const testPage = await context.newPage();

    // Login first
    await testPage.goto("/login", { waitUntil: "domcontentloaded" });
    await testPage.waitForTimeout(1000);
    await testPage.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await testPage.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await testPage.getByRole("button", { name: "Sign In" }).click();
    await testPage.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to wizard
    await testPage.goto(`${origin}/stacks/new`);
    await testPage.waitForLoadState("networkidle");

    // Step 1: Select storage feature (affects later steps)
    const step1 = testPage.getByTestId("easy-step-1");
    if (await step1.isVisible().catch(() => false)) {
      await testPage.getByTestId("easy-feature-storage").check();

      // Verify selection is reflected in state
      await expect(testPage.getByTestId("easy-feature-storage")).toBeChecked();

      // Move to next step
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      // Step 2: Access mode (depends on features selected)
      const step2 = testPage.getByTestId("easy-step-2");
      await expect(step2).toBeVisible();

      // Select "anywhere" access (affects VPN configuration)
      await testPage.getByTestId("easy-access-anywhere").click();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      // Step 3: User configuration (depends on access mode)
      const step3 = testPage.getByTestId("easy-step-3");
      await expect(step3).toBeVisible();

      // Select "just me" (affects auth requirements)
      await testPage.getByTestId("easy-users-me").check();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      // Step 4: Authentication (depends on user selection)
      const step4 = testPage.getByTestId("easy-step-4");
      await expect(step4).toBeVisible();

      // Select password auth (required for previous selections)
      await testPage.getByTestId("easy-auth-password").click();

      // Fill in admin credentials (all fields required due to dependencies)
      await testPage.locator("#admin-username").fill("testadmin");
      await testPage.locator("#admin-email").fill("admin@test.com");
      await testPage.locator("#admin-password").fill("SecurePass123!");
      await testPage.locator("#admin-password-confirm").fill("SecurePass123!");

      // Submit the wizard
      await testPage.getByTestId("wizard-create").click();

      // Wait for submission
      await testPage.waitForTimeout(2000);

      // Verify payload contains all dependent selections
      const submittedPayload = submittedPayloads.at(-1);
      if (submittedPayload) {
        expect(submittedPayload.mode).toBe("easy");
        expect(submittedPayload.stack_spec?.metadata?.access_mode).toBe(
          "anywhere",
        );
        expect(submittedPayload.stack_spec?.vpn).toMatchObject({
          enabled: true,
          type: "headscale",
        });
        expect(submittedPayload.stack_spec?.services?.pocketid).toMatchObject({
          enabled: true,
        });
      }
    }

    await context.close();
  });

  test("wizard back navigation preserves data", async ({
    browser,
    baseURL,
  }) => {
    const origin = baseURL || "http://localhost:5173";
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });

    // Mock validation
    await context.route(
      "**/api/v1/unifier/pipeline/preview**",
      async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            valid: true,
            resolved_stackkit: "test/stackkit",
            detected_addons: [],
            stages: [],
          }),
        });
      },
    );

    const testPage = await context.newPage();

    // Login
    await testPage.goto("/login", { waitUntil: "domcontentloaded" });
    await testPage.waitForTimeout(1000);
    await testPage.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await testPage.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await testPage.getByRole("button", { name: "Sign In" }).click();
    await testPage.waitForURL(/\/stacks/, { timeout: 30_000 });

    await testPage.goto(`${origin}/stacks/new`);
    await testPage.waitForLoadState("networkidle");

    // Step 1: Make selection
    if (
      await testPage
        .getByTestId("easy-step-1")
        .isVisible()
        .catch(() => false)
    ) {
      await testPage.getByTestId("easy-feature-storage").check();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      // Step 2: Make selection
      await testPage.getByTestId("easy-access-anywhere").click();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      // Step 3: Go back
      const backButton = testPage.getByTestId("wizard-back");
      if (await backButton.isVisible().catch(() => false)) {
        await backButton.click();
        await testPage.waitForTimeout(500);

        // Verify we're on step 2
        await expect(testPage.getByTestId("easy-step-2")).toBeVisible();

        // Verify previous selection is still active
        const anywhereButton = testPage.getByTestId("easy-access-anywhere");
        // Check if it has selected state (aria-pressed or active class)
        const isSelected = await anywhereButton
          .evaluate((el) => {
            return (
              el.getAttribute("aria-pressed") === "true" ||
              el.classList.contains("active") ||
              el.classList.contains("selected")
            );
          })
          .catch(() => false);
        expect(isSelected).toBe(true);

        // Go back to step 1
        await backButton.click();
        await testPage.waitForTimeout(500);
        await expect(testPage.getByTestId("easy-step-1")).toBeVisible();

        // Verify step 1 selection preserved
        await expect(
          testPage.getByTestId("easy-feature-storage"),
        ).toBeChecked();
      }
    }

    await context.close();
  });
});

test.describe("Sequential Dependency - Security Settings", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("retired API key settings are not exposed", async ({ page }) => {
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { name: "API Keys" })).toHaveCount(
      0,
    );
    await expect(page).not.toHaveURL(/\/login/);
  });

  test("logout clears session and prevents access", async ({ page }) => {
    // Step 1: Start authenticated
    await page.goto("/settings");
    await expect(page).toHaveURL(/\/settings/);

    // Step 2: Logout
    const logoutBtn = page.getByRole("button", { name: /logout|abmelden/i });
    await logoutBtn.click();

    // Step 3: Should redirect to login
    await page.waitForURL(/\/login/, { timeout: 10000 });

    // Step 4: Try to navigate to protected page
    await page.goto("/settings");

    // Step 5: Should redirect back to login
    await page.waitForURL(/\/login/, { timeout: 10000 });
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();

    // Step 6: Verify dashboard is also protected
    await page.goto("/stacks");
    await page.waitForURL(/\/login/, { timeout: 10000 });
  });
});
