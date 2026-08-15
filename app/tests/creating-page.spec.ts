import { test, expect } from "@playwright/test";
import { requireAppBase } from "./helpers/test-utils";

/**
 * Stack Creation Page Tests
 *
 * Tests the creating progress page with animations, error handling,
 * install commands, and requirements display.
 */

test.describe("Stack Creation Progress Page", () => {
  test("should show explicit init error when job_id is missing", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
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
    await expect(page.locator('[data-status="failed"]')).toBeVisible();

    await context.close();
  });

  test("should show morphing text animation in header", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    const page = await context.newPage();

    // Mock job endpoint to keep the page active
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
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
    const morphingText = page.locator(".morph-text");
    await expect(morphingText).toBeVisible({ timeout: 5000 });

    // Check that morphing text has content
    const text = await morphingText.textContent();
    expect(text).toBeTruthy();
    // The text should contain some content (either kombify-TechStack variant or k<<< variant)
    expect(text?.length).toBeGreaterThan(0);

    // Check cursor element exists (part of MorphingText)
    const cursor = page.locator(".morph-text .cursor");
    await expect(cursor).toBeVisible();

    await context.close();
  });

  test("should display progress bar and task list", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock job endpoint
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
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
    await expect(page.getByText("Fortschritt")).toBeVisible();

    // Check phase-grouped task list renders the unifier group at minimum
    const taskGroups = page.getByTestId("task-group");
    const count = await taskGroups.count();
    expect(count).toBeGreaterThan(0);

    // Check page title
    await expect(page.getByText("Stack wird erstellt")).toBeVisible();
    await expect(page.getByText("My Stack")).toBeVisible();

    await context.close();
  });

  test("should show completion state with install command", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock completed job with registration token
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "test-job-789",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "test_reg_token_abc123",
            stack_id: "stack-xyz",
            requirements: {
              minCloudServers: 0,
              minLocalServers: 1,
              minTotalServers: 1,
              description: "Mindestens 1 lokaler Server",
              details: ["Lokaler Server für Homelab-Dienste erforderlich"],
            },
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Complete%20Stack&job_id=test-job-789`,
    );

    // Wait for completion state
    await expect(page.getByText("Stack erstellt!")).toBeVisible({
      timeout: 10000,
    });

    // Check install command section is shown
    await expect(page.getByText("Worker installieren")).toBeVisible();

    // Check curl command is displayed
    await expect(page.locator("pre").filter({ hasText: "curl" })).toBeVisible();

    // Check copy button exists (aria-label on the button)
    await expect(
      page.getByRole("button", {
        name: "Installationsbefehl in Zwischenablage kopieren",
      }),
    ).toBeVisible();

    // Check dashboard button exists
    await expect(page.getByText("Zum Dashboard")).toBeVisible();

    await context.close();
  });

  test("should show requirements info after completion", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Store config in session storage before navigating
    await page.goto(`${origin}/stacks/creating`);
    await page.evaluate(() => {
      sessionStorage.setItem(
        "creatingStackConfig",
        JSON.stringify({
          provider: "hybrid",
          network: { accessMode: "anywhere" },
          goals: { ai: true },
        }),
      );
      sessionStorage.setItem("creatingStackName", "Hybrid Stack");
    });

    // Mock completed job
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "test-job-req",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "req_token_456",
            stack_id: "stack-req",
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Hybrid%20Stack&job_id=test-job-req`,
    );

    // Wait for completion
    await expect(page.getByText("Stack erstellt!")).toBeVisible({
      timeout: 10000,
    });

    // Requirements should be displayed (calculated from session storage config)
    await expect(
      page.getByTestId("requirements-card").filter({ hasText: "Mindestens" }),
    ).toBeVisible();

    await context.close();
  });

  test("should show simulation alternative hint", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock completed job
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "test-job-sim",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "sim_token",
            stack_id: "stack-sim",
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Sim%20Stack&job_id=test-job-sim`,
    );

    // Wait for completion
    await expect(page.getByText("Stack erstellt!")).toBeVisible({
      timeout: 10000,
    });

    // Simulation hint should be visible
    await expect(page.getByText("Erst testen?")).toBeVisible();
    await expect(page.getByText("Zur Simulation")).toBeVisible();

    await context.close();
  });

  test("should show alternative install commands when expanded", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock completed job
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "test-job-alt",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "alt_token",
            stack_id: "stack-alt",
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Alt%20Stack&job_id=test-job-alt`,
    );

    // Wait for completion
    await expect(page.getByText("Stack erstellt!")).toBeVisible({
      timeout: 10000,
    });

    // Click to expand alternative commands
    await page.getByText("Alternative Installationsmethoden").click();

    // Check alternative commands are shown
    await expect(page.getByText("Docker Container")).toBeVisible();
    await expect(
      page.locator("pre").filter({ hasText: "docker run" }),
    ).toBeVisible();

    await context.close();
  });

  test("should display detailed error info when job fails", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock failed job with detailed error
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
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
    await expect(page.getByText("Erstellung fehlgeschlagen")).toBeVisible({
      timeout: 10000,
    });

    // Check error details section exists
    await expect(page.getByText("Fehlerdetails")).toBeVisible();

    // Check error details content
    await expect(
      page.locator("pre").filter({ hasText: "DNS server" }).first(),
    ).toBeVisible();

    // Check retry button
    await expect(page.getByText("Erneut versuchen")).toBeVisible();

    await context.close();
  });

  test("should show failed task with error indicator in task list", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock failed job
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
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
    await expect(page.getByText("Erstellung fehlgeschlagen")).toBeVisible({
      timeout: 10000,
    });

    // Check that failed task has red styling
    const failedTaskContainer = page.locator('[data-status="failed"]');
    await expect(failedTaskContainer).toBeVisible();

    await context.close();
  });

  test("should copy install command to clipboard", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);

    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    const page = await context.newPage();

    // Mock completed job
    await page.route("**/api/collections/jobs/records/*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "test-job-copy",
          type: "provision",
          state: "completed",
          progress: 100,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            registration_token: "copy_test_token",
            stack_id: "stack-copy",
          },
        }),
      });
    });

    await page.goto(
      `${origin}/stacks/creating?name=Copy%20Stack&job_id=test-job-copy`,
    );

    // Wait for completion
    await expect(page.getByText("Stack erstellt!")).toBeVisible({
      timeout: 10000,
    });

    // Click copy button
    await page
      .getByRole("button", {
        name: "Installationsbefehl in Zwischenablage kopieren",
      })
      .click();

    // Verify clipboard contains an install command that includes the token
    const clip = await page.evaluate(async () => {
      return await navigator.clipboard.readText();
    });
    expect(clip).toContain('KOMBI_TOKEN="copy_test_token"');

    await context.close();
  });
});
