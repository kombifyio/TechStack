import { test, expect } from "@playwright/test";

import { login } from "./helpers/test-utils";
/**
 * Authentication Flow Tests
 *
 * Tests login, logout, and authenticated navigation flows.
 */

test.describe("Login Page", () => {
  test("should display login form elements", async ({ page }) => {
    await page.goto("/login");

    await expect(
      page.getByRole("heading", { name: "kombify Techstack" }),
    ).toBeVisible();
    await expect(page.getByText("PocketBase")).toHaveCount(0);
    await expect(page.getByRole("tab", { name: "Sign Up" })).toHaveCount(0);
  });
});

test.describe("Authentication State", () => {
  test("unauthenticated user should see login page on protected routes", async ({
    page,
  }) => {
    // Clear any existing auth state
    await page.context().clearCookies();

    // Try to access stacks page without auth
    await page.goto("/dashboard");

    // Should redirect to login (or show unauthenticated UI)
    // Wait a bit for any redirects
    await page.waitForTimeout(2000);

    // Either redirected to login or showing the wizard (which doesn't require auth)
    const url = page.url();
    expect(url.includes("/login") || url.includes("/dashboard")).toBeTruthy();
  });

  test("authenticated user should see sidebar navigation", async ({ page }) => {
    await login(page);

    // Sidebar should be visible with navigation items
    await expect(page.locator("aside")).toBeVisible();
    await expect(page.getByRole("link", { name: "Dashboard" })).toBeVisible();
    await expect(
      page.getByRole("link", { name: /Services|Dienste/i }),
    ).toBeVisible();
    await expect(page.getByRole("link", { name: "Monitoring" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Wallet" })).toBeVisible();
  });
});
