import { test, expect } from "@playwright/test";

import { login } from "./helpers/test-utils";
/**
 * Settings Page Tests
 *
 * Tests the intentionally small settings surface.
 */

test.describe("Settings Page", () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test("should display settings page sections", async ({ page }) => {
    await page.goto("/settings");

    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Homelab Identity" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Danger Zone" }),
    ).toBeVisible();

    await expect(page.getByRole("button", { name: "Logout" })).toBeVisible();
    await expect(
      page.getByTestId("settings-reset-stacks-button"),
    ).toBeVisible();
  });

  test("finish selection stamps data-finish and persists across reload", async ({
    page,
  }) => {
    await page.goto("/settings");

    await expect(page.getByTestId("settings-appearance-card")).toBeVisible();
    await page
      .getByTestId("settings-finish")
      .getByRole("radio", { name: "aurora" })
      .click();

    // The documented public boundary (KOMBIFY-DESIGN-SYSTEM-STANDARD §4/§6):
    // the axis lands on <html data-finish> and the device choice persists.
    await expect(page.locator("html")).toHaveAttribute("data-finish", "aurora");
    await page.reload({ waitUntil: "domcontentloaded" });
    await expect(page.locator("html")).toHaveAttribute("data-finish", "aurora");

    // Following the account again clears the device tier.
    await page.goto("/settings");
    await page.getByTestId("settings-finish-follow-account").click();
    const stored = await page.evaluate(() =>
      window.localStorage.getItem("techstack-finish"),
    );
    expect(stored).toBeNull();
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
