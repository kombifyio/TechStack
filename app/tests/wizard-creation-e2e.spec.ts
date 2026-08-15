import { test, expect } from "@playwright/test";
import { completeEasyWizard, login } from "./helpers/test-utils";

/**
 * Wizard → Create → Progress E2E Smoke Test
 *
 * This test runs against a real backend on the configured container engine and validates:
 * 1. Login works
 * 2. Wizard renders and accepts input
 * 3. Stack creation API call succeeds (HTTP 202)
 * 4. /stacks/creating page shows progress (not stuck on loading)
 * 5. Job transitions from pending → completed/failed within timeout
 *
 * Run with:
 *   TECHSTACK_API_URL=http://localhost:5260 PLAYWRIGHT_BASE_URL=http://localhost:5261 \
 *     pnpm exec playwright test --config=playwright.local.config.ts tests/wizard-creation-e2e.spec.ts
 *
 * Prerequisites:
 *   docker compose up -d --build
 */

test.describe("Wizard Creation E2E (Docker)", () => {
  test.setTimeout(120_000);
  test.skip(
    process.env.PLAYWRIGHT_REUSE_SERVER === "1" &&
      !process.env.TECHSTACK_API_URL &&
      !process.env.API_URL,
    "Docker smoke requires a real TechStack backend with seeded owner credentials.",
  );

  test("complete wizard flow should not hang on loading screen", async ({
    page,
  }) => {
    // Step 1: Login
    await login(page);

    // Step 2: Navigate to wizard
    await page.goto("/stacks/new");
    await page
      .getByTestId("hydrated")
      .waitFor({ state: "attached", timeout: 15_000 });

    // Verify wizard rendered
    await expect(page.getByTestId("easy-wizard")).toBeVisible({
      timeout: 10_000,
    });

    // Check if a stack already exists (wizard might be blocked)
    const blockedBanner = page.getByTestId("wizard-blocked");
    if (await blockedBanner.isVisible({ timeout: 2_000 }).catch(() => false)) {
      test.skip(
        true,
        "Stack already exists on this instance - wizard is blocked",
      );
      return;
    }

    // Listen for the API response
    const createResponsePromise = page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v1/stacks") &&
        resp.request().method() === "POST",
      { timeout: 30_000 },
    );

    // Step 3: Walk through the current Easy Wizard and click create.
    await completeEasyWizard(page, {
      feature: "storage",
      serverProvisioning: "install-command",
      access: "home",
      users: "me",
      ownerSource: "local",
    });

    // Step 4: Verify API response
    const createResponse = await createResponsePromise;
    const status = createResponse.status();

    // Should get 202 Accepted (or 409/500 if stack exists)
    if (status === 409) {
      test.skip(true, "Stack already exists (409 Conflict)");
      return;
    }
    if (status === 500) {
      // SvelteKit proxy may wrap 409 as 500 — check response body
      const errBody = await createResponse.json().catch(() => ({}));
      const errMsg = JSON.stringify(errBody);
      if (errMsg.includes("CONFLICT") || errMsg.includes("SINGLE_STACK")) {
        test.skip(
          true,
          `Stack already exists (500-wrapped conflict: ${errMsg})`,
        );
        return;
      }
      // Real 500 — fail with details
      expect.soft(status, `Unexpected 500: ${errMsg}`).toBe(202);
      return;
    }

    expect(status).toBe(202);

    const responseBody = await createResponse.json();
    const data = responseBody.data ?? responseBody;
    expect(data.stack_id).toBeTruthy();
    expect(data.job_id).toBeTruthy();

    // Step 5: Should hand off to the durable operations dashboard.
    await page.waitForURL(/\/stacks\?.*stack_id=/, { timeout: 15_000 });

    // Step 6: Verify the operations surface renders (not stuck on blank loading).
    await expect(page.getByTestId("stack-operations-dashboard")).toBeVisible({
      timeout: 10_000,
    });

    // Step 7: Wait for the job to NOT stay stuck
    // Either it completes, fails with error, or shows the stuck-job detection
    const terminal = page
      .locator('[data-testid="init-error"]')
      .or(page.getByText("Stack created!"))
      .or(page.getByText("Stack rollout verified"))
      .or(page.getByText("Configuration prepared"))
      .or(page.getByText("Server connection ready"))
      .or(page.getByText("Creation failed"))
      .or(page.getByText("Rollout incomplete"))
      .or(page.getByText("Job is not being processed"));

    // Wait up to 90s for any terminal state
    await expect(terminal.first()).toBeVisible({ timeout: 90_000 });

    // Log what happened for debugging
    const pageContent = await page.textContent("body");
    if (
      pageContent?.includes("Stack created!") ||
      pageContent?.includes("Stack rollout verified") ||
      pageContent?.includes("Configuration prepared") ||
      pageContent?.includes("Server connection ready")
    ) {
      // Success path
      expect(true).toBe(true);
    } else if (pageContent?.includes("Creation failed")) {
      // Failed but did not hang - this is acceptable
      // Log the error for debugging
      const errorDetails = await page
        .locator("text=Error details")
        .textContent()
        .catch(() => "no details");
      console.log(`Stack creation failed (not hung): ${errorDetails}`);
    } else if (pageContent?.includes("Job is not being processed")) {
      // Our stuck-job detection kicked in - means the timeout works
      console.log("Stuck-job detection triggered correctly");
    }
  });

  test("unauthenticated user should be redirected to login", async ({
    page,
  }) => {
    // Navigate directly to wizard without login — app should redirect to login
    await page.goto("/stacks/new");

    // The app guards protected routes — expect redirect to /login
    await page.waitForURL(/\/login/, { timeout: 15_000 });
    await expect(page.getByLabel("Email")).toBeVisible({ timeout: 10_000 });
  });
});
