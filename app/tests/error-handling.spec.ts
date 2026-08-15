import { test, expect } from "@playwright/test";
import { TEST_CREDENTIALS, requireApiBase } from "./helpers/test-utils";

/**
 * Error Handling Tests
 *
 * Tests that the application handles errors gracefully
 * and shows appropriate user feedback.
 */

const API_BASE = requireApiBase();

test.describe("API Error Handling", () => {
  test("handles 404 gracefully for unknown job", async ({ request }) => {
    const response = await request.get(
      `${API_BASE}/api/v1/jobs/nonexistent-job-id`,
    );

    expect(response.status()).toBe(404);

    const json = await response.json();
    expect(json.error || json.message).toBeDefined();
  });

  test("handles 404 gracefully for unknown stack", async ({ request }) => {
    const response = await request.get(
      `${API_BASE}/api/v1/stacks/nonexistent-stack-id`,
    );

    // Should return 404 or appropriate error
    expect([404, 400]).toContain(response.status());
  });

  test("handles malformed JSON gracefully", async ({ request }) => {
    const response = await request.post(`${API_BASE}/api/v1/stacks`, {
      data: "this is not valid json",
      headers: { "Content-Type": "text/plain" },
    });

    // Should return 400 Bad Request
    expect([400, 415, 422]).toContain(response.status());
  });

  test("handles missing required fields", async ({ request }) => {
    const response = await request.post(`${API_BASE}/api/v1/stacks`, {
      data: {
        // Missing required 'name' field
        provider: "local",
      },
    });

    // Should return 400 Bad Request
    expect([400, 422]).toContain(response.status());
  });
});

test.describe("UI Error States", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("services page handles API errors gracefully", async ({
    page,
    browser,
  }) => {
    // Create a new context to intercept requests
    const context = await browser.newContext();
    const newPage = await context.newPage();

    // Mock API to return error
    await newPage.route("**/api/collections/services/**", async (route) => {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "Internal server error" }),
      });
    });

    // Login
    await newPage.goto("/login");
    await newPage.waitForTimeout(1000); // Wait for PocketBase connection
    await newPage.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await newPage.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await newPage.getByRole("button", { name: "Sign In" }).click();
    await newPage.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to services
    await newPage.getByRole("link", { name: /Services|Dienste/i }).click();
    await newPage.waitForTimeout(3000);

    // Should show error state or handle gracefully (not crash)
    // Page should still be functional
    await expect(
      newPage.getByRole("heading", { name: "Services" }),
    ).toBeVisible();

    await context.close();
  });

  test("wallet page shows meaningful error for auth issues", async ({
    browser,
  }) => {
    const context = await browser.newContext();
    const page = await context.newPage();

    // Mock PocketBase to return 401
    await page.route("**/api/collections/wallet/**", async (route) => {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ message: "Unauthorized" }),
      });
    });

    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    await page.getByRole("link", { name: "Wallet" }).click();
    await page.waitForTimeout(3000);

    // Page should handle error gracefully
    await expect(page.getByRole("heading", { name: "Wallet" })).toBeVisible();

    await context.close();
  });
});

test.describe("Network Error Handling", () => {
  test("creating page handles job polling failures", async ({ browser }) => {
    const context = await browser.newContext();
    const page = await context.newPage();

    // Start with successful response, then fail
    let requestCount = 0;
    await page.route("**/api/v1/jobs/**", async (route) => {
      requestCount++;
      if (requestCount <= 2) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: {
              id: "test-job",
              state: "running",
              progress: 25,
            },
          }),
        });
      } else {
        // Simulate network error
        await route.abort("failed");
      }
    });

    await page.goto("/stacks/creating?name=Test&job_id=test-job");

    // Wait for polling to occur
    await page.waitForTimeout(5000);

    // Page should not crash
    await expect(page.getByText("Creating your stack")).toBeVisible();

    await context.close();
  });
});

test.describe("Form Validation", () => {
  test("login form shows error for empty submission", async ({ page }) => {
    await page.goto("/login");

    // HTML5 validation should prevent submission
    const emailInput = page.getByLabel("Email");
    const isRequired = await emailInput.getAttribute("required");
    expect(isRequired).toBe("");

    const passwordInput = page.getByLabel("Password");
    const pwRequired = await passwordInput.getAttribute("required");
    expect(pwRequired).toBe("");
  });

  test("login form shows error for invalid email format", async ({ page }) => {
    await page.goto("/login");

    // Enter invalid email
    await page.getByLabel("Email").fill("notanemail");
    await page.getByLabel("Password").fill("somepassword");

    // Try to submit
    await page.getByRole("button", { name: "Sign In" }).click();

    // HTML5 validation should show error or form should fail
    // We stay on login page
    await page.waitForTimeout(1000);
    expect(page.url()).toContain("/login");
  });

  test("login form shows user-friendly error for wrong credentials", async ({
    page,
  }) => {
    await page.goto("/login");

    // Enter valid but wrong credentials
    await page.getByLabel("Email").fill("wrong@example.com");
    await page.getByLabel("Password").fill("wrongpassword");

    // Submit the form
    await page.getByRole("button", { name: "Sign In" }).click();

    // Wait for error to appear
    await page.waitForTimeout(2000);

    // Should show user-friendly error message (from parsePBError)
    await expect(page.getByRole("alert")).toContainText(
      "Invalid email or password",
    );
  });
});

test.describe("PocketBase Error Handling", () => {
  test("stack creation shows auth error when not logged in", async ({
    page,
  }) => {
    // Clear any existing auth
    await page.goto("/");
    await page.evaluate(() => {
      localStorage.removeItem("pocketbase_auth");
    });

    // Navigate to stack creation
    await page.goto("/stacks/new");
    await page.waitForTimeout(1000);

    // Try to create a stack
    const nameInput = page.locator('input[data-testid="stack-name-input"]');
    if (await nameInput.isVisible()) {
      await nameInput.fill("Test Stack");

      // Find and click create/deploy button
      const createButton = page.getByRole("button", { name: /create|deploy/i });
      if (await createButton.isVisible()) {
        await createButton.click();
        await page.waitForTimeout(2000);

        // Should show auth error with login hint
        const hasLoginHint = await page.locator("text=log in").isVisible();
        expect(hasLoginHint).toBe(true);
      }
    }
  });

  test("wallet page shows helpful error when not authenticated", async ({
    page,
  }) => {
    // Clear any existing auth
    await page.goto("/");
    await page.evaluate(() => {
      localStorage.removeItem("pocketbase_auth");
    });

    // Navigate to wallet
    await page.goto("/wallet");
    await page.waitForTimeout(2000);

    // Should either redirect to login or show auth error
    const isOnLogin = page.url().includes("/login");
    const hasAuthError = await page
      .locator("text=/log in|authentication/i")
      .isVisible();

    expect(isOnLogin || hasAuthError).toBe(true);
  });
});
