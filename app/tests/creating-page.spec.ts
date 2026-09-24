import { test, expect } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

function canonicalJobResponse(data: Record<string, unknown>): string {
  return JSON.stringify({ data });
}

const BACKEND_REQUIREMENTS = {
  minCloudServers: 0,
  minLocalServers: 1,
  minTotalServers: 1,
  description: "Minimum 1 local server",
  details: ["A local server is required for Homelab services"],
};

/**
 * Stack Creation Page Tests
 *
 * These exercise the mocked no-setup lane (vite + mock backend) behind the
 * mocked local-owner auth boundary. Assertions target the current creation UI
 * by effect (test ids, roles, live copy); the previous localized copy here was
 * stale long before the route split.
 */

test.describe("Stack Creation Progress Page", () => {
  test("should show explicit init error when job_id is missing", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Deterministic: avoid depending on a running backend for discovery.
    await context.route("**/api/v1/discovery/networks", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ networks: [] }),
      });
    });

    await page.goto(`${origin}/stacks/creating?name=No%20Job`);

    // Explicit banner
    await expect(page.getByTestId("init-error")).toBeVisible();
    await expect(page.getByText("Setup cannot continue")).toBeVisible();
    await expect(page.getByText("Missing job reference")).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Back to setup wizard →" }),
    ).toBeVisible();

    // Also reflects as failed task state
    await expect(page.locator('[data-status="failed"]').first()).toBeVisible();

    await context.close();
  });

  test("should show morphing text animation in header", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    await context.grantPermissions(["notifications"], { origin });
    const page = await context.newPage();

    // Mock job endpoint to keep the page active
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-123",
          type: "provision",
          state: "running",
          progress: 25,
          error: null,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
        }),
      });
    });

    // Navigate directly to creating page with params
    await page.goto(
      `${origin}/stacks/creating?name=Test%20Stack&job_id=test-job-123`,
    );

    // Check MorphingText component is rendered
    const morphingText = page.locator(".morphing-text");
    await expect(morphingText).toBeVisible({ timeout: 5000 });

    // Check that morphing text has content
    const text = await morphingText.textContent();
    expect(text).toBeTruthy();
    expect(text?.length).toBeGreaterThan(0);

    // Check cursor element exists (part of MorphingText)
    await expect(page.locator(".morphing-text .cursor")).toBeVisible();

    await context.close();
  });

  test("should display progress bar and task list", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Mock job endpoint
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-456",
          type: "provision",
          state: "running",
          progress: 50,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=My%20Stack&job_id=test-job-456`,
    );

    // Check progress bar exists
    await expect(page.getByText("Progress", { exact: true })).toBeVisible();

    // Check phase-grouped task list renders the unifier group at minimum
    const taskGroups = page.getByTestId("task-group");
    const count = await taskGroups.count();
    expect(count).toBeGreaterThan(0);

    // In-progress header carries the morphing status text and stack name
    await expect(page.locator(".morphing-text")).toBeVisible();
    await expect(page.getByText("My Stack", { exact: true })).toBeVisible();

    await context.close();
  });

  test("should show completion state with backend requirements", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Mock completed job with a registration token and requirements
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-789",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "test_reg_token_abc123",
            stack_id: "stack-xyz",
            requirements: BACKEND_REQUIREMENTS,
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Complete%20Stack&job_id=test-job-789`,
    );

    // Wait for completion state
    await expect(page.getByTestId("continue-to-stackkit-rollout")).toBeVisible({
      timeout: 10000,
    });

    // Completion header reflects the self-hosted completion path
    await expect(
      page.getByRole("heading", { level: 2, name: "Node connection ready" }),
    ).toBeVisible();

    // Backend requirements are surfaced
    await expect(
      page
        .getByTestId("requirements-card")
        .filter({ hasText: "Minimum 1 local server" }),
    ).toBeVisible();

    // Check the primary continuation action exists
    await expect(
      page.getByRole("button", { name: "Review and start StackKit rollout" }),
    ).toBeVisible();

    await context.close();
  });

  test("should show requirements info after completion", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Mock completed job carrying backend requirements
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-req",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "req_token_456",
            stack_id: "stack-req",
            requirements: BACKEND_REQUIREMENTS,
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Hybrid%20Stack&job_id=test-job-req`,
    );

    // Wait for completion
    await expect(page.getByTestId("continue-to-stackkit-rollout")).toBeVisible({
      timeout: 10000,
    });

    // Requirements should be displayed from the backend result
    await expect(
      page
        .getByTestId("requirements-card")
        .filter({ hasText: "Minimum 1 local server" }),
    ).toBeVisible();

    await context.close();
  });

  test("should display detailed error info when job fails", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Mock failed job with detailed error
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-fail",
          type: "provision",
          state: "failed",
          progress: 35,
          step: "network",
          error: "Network configuration failed",
          error_details:
            "Could not establish connection to DNS server at 8.8.8.8:53. Timeout after 30 seconds.",
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Failed%20Stack&job_id=test-job-fail`,
    );

    // Wait for failure state
    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toBeVisible({ timeout: 10000 });

    // Check error details section exists
    await expect(
      page.getByText("Error details", { exact: true }),
    ).toBeVisible();

    // Check error details content
    await expect(
      page.locator("pre").filter({ hasText: "DNS server" }).first(),
    ).toBeVisible();

    // Check a primary retry action is offered
    await expect(page.locator('button[data-variant="primary"]')).toBeVisible();

    await context.close();
  });

  test("should show failed task with error indicator in task list", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    // Mock failed job
    await page.route("**/api/v1/jobs/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: canonicalJobResponse({
          id: "test-job-task-fail",
          type: "provision",
          state: "failed",
          progress: 50,
          step: "services",
          error: "Service deployment failed",
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Task%20Fail&job_id=test-job-task-fail`,
    );

    // Wait for failure state
    await expect(
      page.getByRole("heading", { level: 2, name: "Creation failed" }),
    ).toBeVisible({ timeout: 10000 });

    // Check that failed task has red styling
    const failedTaskContainer = page.locator('[data-status="failed"]');
    await expect(failedTaskContainer.first()).toBeVisible();

    await context.close();
  });
});
