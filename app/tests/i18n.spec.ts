import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";

test.describe("i18n - Route Stability", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("navigation labels render without missing-key output", async ({
    page,
  }) => {
    const sidebar = page.locator("aside nav");
    await expect(sidebar).toBeVisible();

    await expect(page.getByRole("link", { name: "Services" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Monitoring" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Wallet" })).toBeVisible();
    await expect(sidebar).not.toContainText(/^nav\./);
  });

  test("settings route renders stable text", async ({ page }) => {
    await page.goto("/settings");

    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Account" })).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Stack Identity" }),
    ).toBeVisible();
  });

  test("long settings content does not create horizontal overflow", async ({
    page,
  }) => {
    await page.goto("/settings");

    const body = page.locator("body");
    const scrollWidth = await body.evaluate((el) => el.scrollWidth);
    const clientWidth = await body.evaluate((el) => el.clientWidth);

    expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 20);
  });

  test("routes with translated navigation chrome do not crash", async ({
    page,
  }) => {
    for (const route of [
      "/stacks",
      "/services",
      "/monitoring",
      "/wallet",
      "/settings",
    ]) {
      await page.goto(route);
      await page.waitForTimeout(500);
      await expect(page.locator("body")).toBeVisible();
    }
  });
});
