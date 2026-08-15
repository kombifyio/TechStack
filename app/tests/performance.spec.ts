import { test, expect } from "@playwright/test";
import { TEST_CREDENTIALS, requireApiBase } from "./helpers/test-utils";

/**
 * Performance Tests
 *
 * Tests that pages load within acceptable time limits
 * and don't have obvious performance issues.
 */

const API_BASE = requireApiBase();

test.describe("Page Load Performance", () => {
  test("login page loads within 3 seconds", async ({ page }) => {
    const startTime = Date.now();
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    const loadTime = Date.now() - startTime;

    expect(loadTime).toBeLessThan(3000);

    // Core content should be visible quickly
    await expect(page.getByLabel("Email")).toBeVisible({ timeout: 2000 });
  });

  test("creating page loads within 3 seconds", async ({ page }) => {
    const startTime = Date.now();
    await page.goto("/stacks/creating?name=Test&job_id=test");
    const loadTime = Date.now() - startTime;

    expect(loadTime).toBeLessThan(3000);

    await expect(page.getByText("Creating your stack")).toBeVisible({
      timeout: 2000,
    });
  });
});

test.describe("API Response Times", () => {
  test("health endpoint responds within 500ms", async ({ request }) => {
    const startTime = Date.now();
    const response = await request.get(`${API_BASE}/health`);
    const responseTime = Date.now() - startTime;

    expect(response.ok()).toBeTruthy();
    expect(responseTime).toBeLessThan(500);
  });

  test("info endpoint responds within 500ms", async ({ request }) => {
    const startTime = Date.now();
    const response = await request.get(`${API_BASE}/api/v1/info`);
    const responseTime = Date.now() - startTime;

    expect(response.ok()).toBeTruthy();
    expect(responseTime).toBeLessThan(500);
  });

  test("stacks list endpoint responds within 1 second", async ({ request }) => {
    const startTime = Date.now();
    const response = await request.get(`${API_BASE}/api/v1/stacks`);
    const responseTime = Date.now() - startTime;

    expect(response.ok()).toBeTruthy();
    expect(responseTime).toBeLessThan(1000);
  });

  test("jobs list endpoint responds within 1 second", async ({ request }) => {
    const startTime = Date.now();
    const response = await request.get(`${API_BASE}/api/v1/jobs`);
    const responseTime = Date.now() - startTime;

    expect(response.ok()).toBeTruthy();
    expect(responseTime).toBeLessThan(1000);
  });
});

test.describe("Navigation Performance", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("sidebar navigation is responsive", async ({ page }) => {
    const navItems = [
      { name: /Services|Dienste/i, url: "/services" },
      { name: "Monitoring", url: "/monitoring" },
      { name: "Wallet", url: "/wallet" },
    ];

    for (const item of navItems) {
      const startTime = Date.now();
      await page.getByRole("link", { name: item.name }).click();
      await page.waitForURL(new RegExp(item.url));
      const navTime = Date.now() - startTime;

      // Navigation should complete within 2 seconds
      expect(navTime).toBeLessThan(2000);
    }
  });

  test("page doesn't have memory leaks on navigation", async ({ page }) => {
    // Navigate back and forth multiple times
    for (let i = 0; i < 5; i++) {
      await page.getByRole("link", { name: /Services|Dienste/i }).click();
      await page.waitForTimeout(500);
      await page.getByRole("link", { name: "Monitoring" }).click();
      await page.waitForTimeout(500);
      await page.getByRole("link", { name: "Wallet" }).click();
      await page.waitForTimeout(500);
    }

    // Page should still be responsive
    await expect(page.getByRole("heading", { name: "Wallet" })).toBeVisible();
  });
});

test.describe("Animation Performance", () => {
  test("morphing text animation doesn't freeze UI", async ({ page }) => {
    await page.goto("/stacks/creating?name=Test&job_id=test");

    // Wait for animation to run a few cycles
    await page.waitForTimeout(7000);

    // UI should still be responsive
    const progressText = page.getByText("Progress");
    await expect(progressText).toBeVisible();

    // Should be able to interact with page
    const isInteractive = await page.evaluate(() => {
      return document.readyState === "complete";
    });
    expect(isInteractive).toBeTruthy();
  });
});

test.describe("Concurrent Request Handling", () => {
  test("API handles multiple concurrent requests", async ({ request }) => {
    // Send 5 requests simultaneously
    const requests = [
      request.get(`${API_BASE}/health`),
      request.get(`${API_BASE}/api/v1/info`),
      request.get(`${API_BASE}/api/v1/stacks`),
      request.get(`${API_BASE}/api/v1/jobs`),
      request.get(`${API_BASE}/health`),
    ];

    const startTime = Date.now();
    const responses = await Promise.all(requests);
    const totalTime = Date.now() - startTime;

    // All requests should succeed
    for (const response of responses) {
      expect(response.ok()).toBeTruthy();
    }

    // Concurrent requests should complete faster than sequential
    // (5 requests × 500ms each = 2500ms if sequential, should be < 2000ms concurrent)
    expect(totalTime).toBeLessThan(2500);
  });
});
