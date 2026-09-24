import { test, expect } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

/**
 * Responsive Design & Accessibility Tests
 *
 * All tests run in the nosetup mock environment (no Docker / real PocketBase).
 * Tests that require an authenticated state use mockLoggedInContext instead of
 * real credentials.
 */

test.describe("Responsive Design - Mobile", () => {
  test.use({ viewport: { width: 375, height: 667 } }); // iPhone SE

  test("login page should be usable on mobile", async ({ page }) => {
    await page.goto("/login");

    await expect(
      page.getByRole("heading", { name: "kombify Techstack" }),
    ).toBeVisible();
    await expect(page.getByText("PocketBase")).toHaveCount(0);

    // Form should fit on screen (no horizontal scroll)
    const body = page.locator("body");
    const scrollWidth = await body.evaluate((el) => el.scrollWidth);
    const clientWidth = await body.evaluate((el) => el.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 10); // Small tolerance
  });

  test("creating page should be readable on mobile", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext({
      viewport: { width: 375, height: 667 },
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks/creating?name=Test&job_id=test`);
    await page.waitForLoadState("domcontentloaded");

    // Should stay on the creating page (auth guard passed)
    await expect(page).toHaveURL(/\/stacks\/creating/);

    // Main container should be visible without horizontal overflow
    const container = page.locator(".min-h-screen");
    await expect(container).toBeVisible();

    const body = page.locator("body");
    const scrollWidth = await body.evaluate((el) => el.scrollWidth);
    const clientWidth = await body.evaluate((el) => el.clientWidth);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 10);

    await context.close();
  });

  test("authenticated navigation uses the mobile drawer below 48rem", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext({
      viewport: { width: 375, height: 667 },
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);

    const menu = page.getByRole("button", { name: "Toggle menu" });
    await expect(menu).toBeVisible();
    const hiddenRail = await page.locator("aside").boundingBox();
    expect(hiddenRail).not.toBeNull();
    expect(hiddenRail!.x + hiddenRail!.width).toBeLessThanOrEqual(1);

    await menu.click();
    await expect
      .poll(async () => (await page.locator("aside").boundingBox())?.x)
      .toBeGreaterThanOrEqual(0);

    await context.close();
  });
});

test.describe("Responsive Design - Tablet", () => {
  test.use({ viewport: { width: 768, height: 1024 } }); // iPad

  test("dashboard keeps a compact rail instead of an early burger", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext({
      viewport: { width: 768, height: 1024 },
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);
    const menu = page.getByRole("button", { name: "Toggle menu" });
    await expect(menu).toBeHidden();

    const rail = page.locator("aside");
    await expect(rail).toBeVisible();
    await expect
      .poll(async () => {
        const box = await rail.boundingBox();
        return box !== null && box.x >= 0 && box.width < 100;
      })
      .toBe(true);

    await context.close();
  });
});

test.describe("Responsive Design - Desktop", () => {
  test.use({ viewport: { width: 1920, height: 1080 } }); // Full HD

  test("theme remains reachable from the user menu with the Companion side panel active", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext({
      viewport: { width: 1280, height: 800 },
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);
    const companionFrame = page.locator("iframe[data-kombify-companion-frame]");
    await expect(companionFrame).toBeAttached();
    await companionFrame.evaluate((frame) => {
      const source = (frame as HTMLIFrameElement).contentWindow;
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://api.kombify.io",
          source,
          data: {
            type: "kombify:panel-bridge",
            protocolVersion: 2,
            revision: 1_000,
            kind: "layout.change",
            payload: { layout: "sidepanel" },
          },
        }),
      );
    });
    await expect(
      page.getByRole("button", { name: "Open the Companion" }),
    ).toBeVisible();

    const themeControl = page.getByRole("button", {
      name: "Toggle dark/light theme",
    });
    await expect(themeControl).toBeHidden();

    await page.locator('[data-onboarding-anchor="user-menu"]').click();
    const menuThemeControl = page
      .getByRole("menu")
      .getByRole("button", { name: "Toggle dark/light theme" });
    await expect(menuThemeControl).toBeVisible();

    const html = page.locator("html");
    const appearance = await html.getAttribute("data-appearance");
    expect(appearance === "dark" || appearance === "light").toBe(true);
    await menuThemeControl.click();
    await expect(html).toHaveAttribute(
      "data-appearance",
      appearance === "dark" ? "light" : "dark",
    );

    await context.close();
  });

  test("sidebar navigation works on desktop", async ({ browser, baseURL }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext({
      viewport: { width: 1920, height: 1080 },
    });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);
    await page.waitForTimeout(500);

    // Sidebar should be visible and contain nav links
    await expect(page.locator("aside")).toBeVisible();
    const navLinks = page.locator("aside").getByRole("link");
    const linkCount = await navLinks.count();
    expect(linkCount).toBeGreaterThan(0);

    await context.close();
  });
});

test.describe("Accessibility", () => {
  test("login form has proper labels", async ({ page }) => {
    await page.goto("/login");
    await page.waitForURL(/\/client\/onboarding|\/client\/local|\/login/);

    if (page.url().includes("/client/local")) {
      const emailInput = page.getByLabel("Email");
      const passwordInput = page.getByLabel("Password");
      await expect(emailInput).toBeVisible();
      await expect(passwordInput).toBeVisible();
      return;
    }

    await expect(
      page.getByRole("heading", { name: "kombify Techstack" }),
    ).toBeVisible();
    await expect(page.getByText("PocketBase")).toHaveCount(0);
  });

  test("buttons have accessible text", async ({ page }) => {
    await page.goto("/login");
    await page.waitForURL(/\/client\/onboarding|\/client\/local|\/login/);

    const action = page.getByRole("button").first();
    await expect(action).toBeVisible();
    const text = await action.textContent();
    expect(text?.trim().length).toBeGreaterThan(0);
  });

  test("images have alt text", async ({ browser, baseURL }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);
    await page.waitForTimeout(1000);

    // Any images on the page should have alt text
    const images = page.locator("img");
    const imageCount = await images.count();
    for (let i = 0; i < imageCount; i++) {
      const alt = await images.nth(i).getAttribute("alt");
      expect(alt).not.toBeNull();
    }

    await context.close();
  });

  test("page has proper heading hierarchy", async ({ browser, baseURL }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await page.goto(`${origin}/stacks`);
    await page.waitForTimeout(1000);

    // Should have at least one h1
    const h1Count = await page.locator("h1").count();
    expect(h1Count).toBeGreaterThanOrEqual(1);

    await context.close();
  });

  test("keyboard navigation works on login form", async ({ page }) => {
    await page.goto("/login");
    await page.waitForURL(/\/client\/onboarding|\/client\/local|\/login/);
    await page.waitForTimeout(500);

    if (page.url().includes("/client/local")) {
      const email = page.getByLabel("Email");
      await email.focus();
      await expect(email).toBeFocused();
      await page.keyboard.press("Tab");
      await expect(page.getByLabel("Password")).toBeFocused();
      return;
    }

    await expect(
      page.getByRole("heading", { name: "kombify Techstack" }),
    ).toBeVisible();

    // Tab moves focus to submit button
    await page.keyboard.press("Tab");
    const activeTag = await page.evaluate(() =>
      document.activeElement?.tagName.toLowerCase(),
    );
    expect(activeTag).toBe("button");
  });
});

test.describe("Color Contrast", () => {
  test("error messages are visible", async ({ page }) => {
    await page.goto("/login");

    // Trigger a failed login attempt
    await page.getByLabel("Email").fill("wrong@email.com");
    await page.getByLabel("Password").fill("wrongpassword");
    await page
      .getByRole("button", { name: /sign in/i })
      .first()
      .click();

    // Wait for error
    await page.waitForTimeout(5000);

    // If error shown, it should be visible (red border class)
    const errorBox = page.locator(".border-red-700");
    const errorCount = await errorBox.count();

    if (errorCount > 0) {
      await expect(errorBox.first()).toBeVisible();
    }
  });
});
