import { test, expect } from "@playwright/test";
import { TEST_CREDENTIALS, requireApiBase } from "./helpers/test-utils";

/**
 * Services Page Tests
 *
 * Tests the services listing and management page.
 */

test.describe("Services Page", () => {
  test.beforeEach(async ({ page }) => {
    // Login before each test
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("should display services page with correct header", async ({ page }) => {
    await page.getByRole("link", { name: /Services|Dienste/i }).click();
    await page.waitForURL(/\/services/);

    // Check page header
    await expect(page.getByRole("heading", { name: "Services" })).toBeVisible();
    await expect(page.getByText("Catalog rollout")).toBeVisible();

    // Check deploy button exists
    await expect(
      page.getByRole("button", { name: "New Application" }),
    ).toBeVisible();
  });

  test("should show empty state or services list", async ({ page }) => {
    await page.getByRole("link", { name: /Services|Dienste/i }).click();
    await page.waitForURL(/\/services/);

    // Wait for loading to complete
    await page.waitForTimeout(2000);

    // Either shows services or empty state message
    const hasServices = await page.locator(".card h3").count();
    const hasEmptyState = await page
      .getByText("No applications registered")
      .count();
    const hasError = await page.locator(".border-red-700").count();

    // Should show either services or empty state, but no error
    expect(hasServices > 0 || hasEmptyState > 0).toBeTruthy();
    expect(hasError).toBe(0);
  });

  test("should not show loading state after data loads", async ({ page }) => {
    await page.getByRole("link", { name: /Services|Dienste/i }).click();
    await page.waitForURL(/\/services/);

    // Wait for loading to complete
    await page.waitForTimeout(3000);

    // Loading skeleton should be gone
    const loadingSkeletons = await page.locator(".animate-pulse").count();
    expect(loadingSkeletons).toBe(0);
  });
});

test.describe("Services API", () => {
  const API_BASE = requireApiBase();

  test("GET /api/v1/services returns the canonical service read model", async ({
    request,
  }) => {
    const response = await request.get(`${API_BASE}/api/v1/services`);

    // API should respond when authenticated; unauthenticated local setups may reject.
    expect([200, 401, 403]).toContain(response.status());

    if (response.ok()) {
      const json = await response.json();
      expect(Array.isArray(json.data)).toBe(true);
      // management_state is a REQUIRED field of this contract. The client
      // refuses to render a service without it rather than degrading it to
      // "not managed", so a dropped field has to fail here first.
      for (const service of json.data) {
        expect(["managed", "observed"]).toContain(service.management_state);
      }
    }
  });

  test("GET /api/v1/registry/services still serves the catalog BFF", async ({
    request,
  }) => {
    // The registry BFF is not retired in this wave: it is the only source of
    // the catalog and the migration availability flags. Its service rows carry
    // the same required management_state as the canonical route.
    const response = await request.get(`${API_BASE}/api/v1/registry/services`);

    expect([200, 401, 403]).toContain(response.status());

    if (response.ok()) {
      const json = await response.json();
      expect(json.data.catalog).toBeDefined();
      expect(json.data.services).toBeDefined();
      for (const service of json.data.services) {
        expect(["managed", "observed"]).toContain(service.management_state);
      }
    }
  });
});
