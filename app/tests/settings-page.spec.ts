import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";
/**
 * Settings Page Tests
 *
 * Tests the intentionally small settings surface.
 */

test.describe("Settings Page", () => {
  test.beforeEach(async ({ page }) => {
    // Login before each test
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000); // Wait for PocketBase connection
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("should display settings page sections", async ({ page }) => {
    await page.goto("/settings");

    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Stack Identity" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Danger Zone" }),
    ).toBeVisible();

    await expect(page.getByRole("button", { name: "Logout" })).toBeVisible();
    await expect(
      page.getByTestId("settings-reset-stacks-button"),
    ).toBeVisible();
  });

  test("should not show retired settings panels", async ({ page }) => {
    await page.goto("/settings");

    await expect(page.getByText("API Keys")).toHaveCount(0);
    await expect(page.getByText("Feature Flags")).toHaveCount(0);
    await expect(page.getByText("Trusted Devices")).toHaveCount(0);
    await expect(page.getByText("kombify Sim Integration")).toHaveCount(0);
  });

  test("logout button should redirect to login page", async ({ page }) => {
    await page.goto("/settings");

    // Find and click logout button in Danger Zone
    const logoutBtn = page.getByRole("button", { name: /logout|abmelden/i });
    await logoutBtn.click();

    // Should redirect to login page
    await page.waitForURL(/\/login/, { timeout: 10000 });
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();
  });

  test("reset stacks requires confirmation", async ({ page }) => {
    await page.goto("/settings");
    await page.getByTestId("settings-reset-stacks-button").click();

    await expect(page.getByTestId("settings-reset-stacks-modal")).toBeVisible();
    await expect(
      page.getByTestId("settings-reset-stacks-confirm"),
    ).toBeVisible();
  });
});
