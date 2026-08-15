import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";

async function loginAndOpenWallet(page: import("@playwright/test").Page) {
  await page.goto("/login", { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(1000);
  await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
  await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
  await page.getByRole("button", { name: "Sign In" }).click();
  await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  await page.getByRole("link", { name: "Wallet" }).click();
  await page.waitForURL(/\/wallet/, { timeout: 30_000 });
}
/**
 * Navigation Tests
 *
 * Tests navigation elements like logo click and sidebar links.
 */

test.describe("Logo Navigation", () => {
  test("clicking logo navigates to homepage", async ({ page }) => {
    // First login
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();

    // Wait for redirect to stacks page
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to a different page first (e.g., wallet)
    await page.getByRole("link", { name: "Wallet" }).click();
    await page.waitForURL(/\/wallet/);

    // Click the logo in the sidebar
    const logo = page.locator('aside a[href="/"] img[alt="kombify-TechStack"]');
    await logo.click();

    // Should navigate to homepage/root
    await page.waitForURL("/");
  });

  test("logo has hover effect", async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Check that logo has hover:opacity-80 class
    const logo = page.locator('aside a[href="/"] img[alt="kombify-TechStack"]');
    const classes = await logo.getAttribute("class");
    expect(classes).toContain("hover:opacity-80");
    expect(classes).toContain("cursor-pointer");
  });
});

test.describe("Wallet Page", () => {
  test("wallet page shows credentials after login", async ({ page }) => {
    await loginAndOpenWallet(page);

    await expect(page.locator('[data-wallet-tab="tools"]')).toBeVisible();
    await expect(page.locator('[data-wallet-tab="access"]')).toBeVisible();
    await expect(page.locator('[data-wallet-tab="recovery"]')).toBeVisible();
    await expect(page.locator('[data-wallet-tab="overview"]')).toHaveCount(0);

    // Wait for loading to finish
    await page.waitForTimeout(2000);

    await expect(page.locator('[data-wallet-state="error"]')).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Tools" })).toBeVisible();
    await expect(page).toHaveURL(/\/wallet$/);
  });

  test("wallet renders a stable tab canvas shell", async ({ page }) => {
    await loginAndOpenWallet(page);

    const tabNav = page.locator("[data-wallet-tab-nav]");
    const canvas = page.locator("[data-wallet-canvas]");

    await expect(tabNav).toBeVisible();
    await expect(canvas).toBeVisible();

    const minHeight = await canvas.evaluate((element) => {
      return Number.parseFloat(getComputedStyle(element).minHeight || "0");
    });

    expect(minHeight).toBeGreaterThan(0);
  });

  test("wallet respects a direct recovery hash on load", async ({ page }) => {
    await loginAndOpenWallet(page);
    await page.goto("/wallet#recovery", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1500);

    await expect(page).toHaveURL(/\/wallet#recovery$/);
    await expect(page.getByRole("heading", { name: "Recovery" })).toBeVisible();
    await expect(page.locator("[data-search-input]")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Access" })).toHaveCount(0);
  });

  test("wallet tabs switch sections and update the URL hash", async ({
    page,
  }) => {
    await loginAndOpenWallet(page);
    await page.waitForTimeout(2000);

    await expect(page.getByRole("heading", { name: "Tools" })).toBeVisible();
    await expect(page).toHaveURL(/\/wallet$/);

    await page.locator('[data-wallet-tab="recovery"]').click();
    await expect(page).toHaveURL(/\/wallet#recovery$/);
    await expect(page.getByRole("heading", { name: "Recovery" })).toBeVisible();
    await expect(page.locator("[data-search-input]")).toBeVisible();

    await page.locator('[data-wallet-tab="access"]').click();
    await expect(page).toHaveURL(/\/wallet#access$/);
    await expect(page.getByRole("heading", { name: "Access" })).toBeVisible();
    await expect(page.locator("[data-search-input]")).toHaveCount(0);

    await page.locator('[data-wallet-tab="tools"]').click();
    await expect(page).toHaveURL(/\/wallet$/);
    await expect(page.getByRole("heading", { name: "Tools" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Access" })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Recovery" })).toHaveCount(
      0,
    );
  });
});
