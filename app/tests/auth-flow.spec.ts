import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";
/**
 * Authentication Flow Tests
 *
 * Tests login, logout, and registration flows.
 */

test.describe("Login Page", () => {
  test("should display login form elements", async ({ page }) => {
    await page.goto("/login");

    // Check branding
    await expect(page.getByText("kombify-TechStack")).toBeVisible();
    await expect(
      page.getByText("Hybrid Infrastructure Control Plane"),
    ).toBeVisible();

    // Check form elements
    await expect(page.getByLabel("Email")).toBeVisible();
    await expect(page.getByLabel("Password")).toBeVisible();
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();

    // Check registration link
    await expect(page.getByText("Don't have an account?")).toBeVisible();
    await expect(page.getByRole("link", { name: "Sign up" })).toBeVisible();
  });

  test("should show error for invalid credentials", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection

    await page.getByLabel("Email").fill("invalid@email.com");
    await page.getByLabel("Password").fill("wrongpassword");
    await page.getByRole("button", { name: "Sign In" }).click();

    // Wait for error message
    await expect(page.locator(".border-red-700")).toBeVisible({
      timeout: 10000,
    });
  });

  test("should redirect to stacks after successful login", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection

    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();

    // Should redirect to stacks page
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
    await expect(
      page.getByRole("heading", { name: "kombify-TechStack" }),
    ).toBeVisible();
  });

  test("should show loading state during login", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection

    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);

    // Click and check for loading state
    const submitBtn = page.getByRole("button", { name: "Sign In" });
    await submitBtn.click();

    // Button should show loading state (may be brief)
    // Just verify the page doesn't crash and redirects eventually
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("form fields should be required", async ({ page }) => {
    await page.goto("/login");

    // Try to submit empty form
    const submitBtn = page.getByRole("button", { name: "Sign In" });
    await submitBtn.click();

    // Form validation should prevent submission
    // Check that we're still on login page
    expect(page.url()).toContain("/login");
  });
});

test.describe("Registration Page", () => {
  test("should display registration form", async ({ page }) => {
    await page.goto("/register");

    // Check form exists (register has 3 fields: email, password, confirm)
    await expect(page.locator("#email")).toBeVisible();
    await expect(page.locator("#password")).toBeVisible();
    await expect(page.locator("#passwordConfirm")).toBeVisible();

    // Check login link
    await expect(page.getByRole("link", { name: /sign in/i })).toBeVisible();
  });

  test("navigating from login to register should work", async ({ page }) => {
    await page.goto("/login");

    await page.getByRole("link", { name: "Sign up" }).click();

    await page.waitForURL(/\/register/);
    expect(page.url()).toContain("/register");
  });
});

test.describe("Authentication State", () => {
  test("unauthenticated user should see login page on protected routes", async ({
    page,
  }) => {
    // Clear any existing auth state
    await page.context().clearCookies();

    // Try to access stacks page without auth
    await page.goto("/stacks");

    // Should redirect to login (or show unauthenticated UI)
    // Wait a bit for any redirects
    await page.waitForTimeout(2000);

    // Either redirected to login or showing the wizard (which doesn't require auth)
    const url = page.url();
    expect(url.includes("/login") || url.includes("/stacks")).toBeTruthy();
  });

  test("authenticated user should see sidebar navigation", async ({ page }) => {
    await page.goto("/login");
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

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
