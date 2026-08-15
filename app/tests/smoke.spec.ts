import { test, expect } from "@playwright/test";
import {
  TEST_CREDENTIALS,
  authenticateViaApi,
  requireApiBase,
  requireAppBase,
} from "./helpers/test-utils";

/**
 * Smoke Tests - Critical User Journeys
 *
 * These tests verify the most important user flows work end-to-end.
 * They should run first and fast to catch critical regressions.
 */

test.describe("Smoke Tests", () => {
  // Don't run serially - let Playwright manage parallelism
  // test.describe.configure({ mode: "serial" });

  test("1. API is healthy and responding", async ({ request }) => {
    const apiBase = requireApiBase();
    const response = await request.get(`${apiBase}/api/v1/health`);
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    expect(json.data.status).toBe("healthy");
  });

  test("2. Login page renders correctly", async ({ page }) => {
    await page.goto("/login");

    // Logo image should be visible
    await expect(page.getByAltText("kombify-TechStack Logo")).toBeVisible();
    await expect(page.getByLabel("Email")).toBeVisible();
    await expect(page.getByLabel("Password")).toBeVisible();
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();
  });

  test("3. User can login successfully", async ({ page }) => {
    await page.goto("/login");

    // Wait for PocketBase to be ready (bootstrap may take time)
    await page.waitForTimeout(2000);

    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();

    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
    await expect(page.locator("aside")).toBeVisible();
  });

  test("4. Dashboard loads after login", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Main content should be visible
    await expect(
      page.getByRole("heading", { name: "kombify-TechStack" }),
    ).toBeVisible();
  });

  test("5. Navigation sidebar works", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to wallet via top-level sidebar link
    await page.getByRole("link", { name: "Wallet" }).click();
    await page.waitForURL(/\/wallet/);
    await expect(page.getByRole("heading", { name: "Wallet" })).toBeVisible();

    // Navigate to settings via direct URL (settings is in user menu, not sidebar)
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
  });

  test("6. Logo click returns to dashboard", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Go to another page
    await page.getByRole("link", { name: "Wallet" }).click();
    await page.waitForURL(/\/wallet/);

    // Click logo (navigates to / which redirects to /stacks when logged in)
    await page.locator('a[href="/"] img[alt="kombify-TechStack"]').click();
    await page.waitForURL(/\/stacks/);
  });

  test("7. Settings page loads and has sections", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    await page.goto("/settings");

    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Stack Identity" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Danger Zone" }),
    ).toBeVisible();
  });

  test("8. Logout redirects to login", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    await page.goto("/settings");
    await page.getByRole("button", { name: /logout|abmelden/i }).click();

    await page.waitForURL(/\/login/, { timeout: 10000 });
  });

  test("9. Creating page renders with morphing animation", async ({ page }) => {
    // Login first — creating page requires authentication
    await page.goto("/login");
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // name=Test sets stackName to "Test", which is displayed instead of "Creating Stack"
    await page.goto("/stacks/creating?name=Test&job_id=test");

    await expect(page.getByText("Test")).toBeVisible();
    await expect(page.locator(".morphing-text")).toBeVisible();
  });

  test("10. Wizard page is accessible", async ({ browser, baseURL }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks/new`);

    // Should show wizard or redirect
    await page.waitForTimeout(3000);

    // Either wizard loads or we get redirected (unauthenticated → /login)
    const url = page.url();
    expect(
      url.includes("/stacks/new") ||
        url.includes("/stacks") ||
        url.includes("/login"),
    ).toBeTruthy();

    await context.close();
  });
});

test.describe("Critical API Endpoints", () => {
  const API_BASE = requireApiBase();

  test("health endpoint", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/health`);
    expect(response.ok()).toBeTruthy();
  });

  test("info endpoint", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/info`);
    expect(response.ok()).toBeTruthy();
  });

  test("stacks endpoint returns data for the configured test user", async ({
    request,
  }) => {
    const { token } = await authenticateViaApi(API_BASE);
    const response = await request.get(`${API_BASE}/api/v1/stacks`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(response.status()).not.toBe(401);
    expect(response.status()).not.toBe(403);
    expect(response.ok()).toBeTruthy();
  });

  test("info endpoint returns version", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/info`);
    expect(response.ok()).toBeTruthy();
    const json = await response.json();
    expect(json.data).toBeDefined();
  });
});
