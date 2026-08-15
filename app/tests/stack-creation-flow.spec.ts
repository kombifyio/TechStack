import { test, expect, type Page } from "@playwright/test";
import {
  mockLoggedInContext,
  requireApiBase,
  requireAppBase,
} from "./helpers/test-utils";

/**
 * Stack Creation Flow Tests
 *
 * Exercises the easy stack-creation wizard end-to-end with mocked API responses.
 * Uses mockLoggedInContext({ allowMockAuth: true }) to bypass the V2/PB auth gate
 * so tests can access /stacks/new without a real session.
 *
 * Rewritten for the CURRENT 5-step wizard (Goals -> Server -> Access ->
 * Users -> Login) and the post-create handoff to the operations dashboard (the old
 * single-page "Worker Registrierung" modal no longer exists). The old test IDs
 * (easy-next, easy-create, easy-users-onlyme, easy-login-password, easy-admin-*)
 * were renamed; this version uses wizard-next / wizard-create / easy-users-me /
 * easy-auth-password and the #admin-* credential inputs, and adds the required
 * /api/v1/unifier/pipeline/preview mock.
 */

async function mockPipelinePreview(page: Page, apiBase: string) {
  await page.route(
    `${apiBase}/api/v1/unifier/pipeline/preview`,
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          valid: true,
          resolved_stackkit: "kombify/standard",
          detected_addons: [],
        }),
      });
    },
  );
}

// Walks the 5-step easy wizard on defaults (vault goal + install-command server
// + onlyMe audience are on by default) and submits with password auth.
async function advanceWizardAndCreate(page: Page) {
  await page.getByTestId("hydrated").waitFor({ state: "attached" });

  await expect(page.getByTestId("easy-step-1")).toBeVisible();
  await page.getByTestId("wizard-next").click(); // Goals -> Server

  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await page.getByTestId("wizard-next").click(); // Server -> Access

  await expect(page.getByTestId("easy-step-3")).toBeVisible();
  await page.getByTestId("wizard-next").click(); // Access -> Users

  await expect(page.getByTestId("easy-step-4")).toBeVisible();
  await page.getByTestId("wizard-next").click(); // Users -> Login

  await expect(page.getByTestId("easy-step-5")).toBeVisible();
  await page.getByTestId("easy-auth-password").click();
  await page.locator("#admin-email").fill("admin@test.local");
  await page.locator("#admin-password").fill("testpass123");
  await page.locator("#admin-password-confirm").fill("testpass123");
  await page.getByTestId("wizard-create").click();
}

test.describe("Stack Creation Progress", () => {
  test("navigates to operations after stack creation", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const apiBase = requireApiBase();

    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await mockPipelinePreview(page, apiBase);

    await page.route(`${apiBase}/api/v1/stacks`, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            stack_id: "test-stack-123",
            job_id: "test-job-456",
            name: "Test Stack",
            state: "creating",
          },
        }),
      });
    });

    let pollCount = 0;
    await page.route(`${apiBase}/api/v1/jobs/*`, async (route) => {
      pollCount++;
      const progress = Math.min(pollCount * 25, 100);
      const state = progress >= 100 ? "completed" : "running";
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            id: "test-job-456",
            type: "provision",
            state,
            progress,
            error: null,
            result: state === "completed" ? { success: true } : null,
          },
        }),
      });
    });

    await page.goto(`${origin}/stacks/new`);
    await advanceWizardAndCreate(page);

    await page.waitForURL("**/stacks?**", { timeout: 15000 });
    await expect(page).toHaveURL(/\/stacks\?.*stack_id=test-stack-123/);
  });

  test("stays on the wizard when creation fails", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const apiBase = requireApiBase();

    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await mockPipelinePreview(page, apiBase);

    await page.route(`${apiBase}/api/v1/stacks`, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({
          error: "Internal server error",
          message: "store unavailable",
        }),
      });
    });

    await page.goto(`${origin}/stacks/new`);
    await advanceWizardAndCreate(page);

    await page.waitForTimeout(1500);
    await expect(page).toHaveURL(/\/stacks\/new/);
  });
});

test.describe("Wizard Navigation", () => {
  test("allows going back to the previous step", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks/new`);
    await page.getByTestId("hydrated").waitFor({ state: "attached" });

    await expect(page.getByTestId("easy-step-1")).toBeVisible();
    await page.getByTestId("wizard-next").click();
    await expect(page.getByTestId("easy-step-2")).toBeVisible();

    await page.getByTestId("wizard-back").click();
    await expect(page.getByTestId("easy-step-1")).toBeVisible();
  });
});
