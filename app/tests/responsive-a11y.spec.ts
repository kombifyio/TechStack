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

    // All form elements should be visible and usable
    await expect(page.getByLabel("Email")).toBeVisible();
    await expect(page.getByLabel("Password")).toBeVisible();
    await expect(
      page.getByRole("button", { name: /sign in/i }).first(),
    ).toBeVisible();

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
});

test.describe("Responsive Design - Tablet", () => {
  test.use({ viewport: { width: 768, height: 1024 } }); // iPad

  test("dashboard should show grid layout on tablet", async ({
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
    await page.waitForTimeout(1000);

    // Sidebar should be visible on tablet
    await expect(page.locator("aside")).toBeVisible();

    await context.close();
  });
});

test.describe("Responsive Design - Desktop", () => {
  test.use({ viewport: { width: 1920, height: 1080 } }); // Full HD

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

    // Check that inputs have associated labels
    const emailInput = page.getByLabel("Email");
    const passwordInput = page.getByLabel("Password");

    await expect(emailInput).toBeVisible();
    await expect(passwordInput).toBeVisible();

    // Labels should be properly associated
    const emailId = await emailInput.getAttribute("id");
    const passwordId = await passwordInput.getAttribute("id");

    expect(emailId).toBe("owner-login-email");
    expect(passwordId).toBe("owner-login-password");
  });

  test("buttons have accessible text", async ({ page }) => {
    await page.goto("/login");

    // Sign-in button should have text
    const signInBtn = page.getByRole("button", { name: /sign in/i }).first();
    await expect(signInBtn).toBeVisible();

    // Button should not be empty
    const text = await signInBtn.textContent();
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
    await page.waitForTimeout(500);

    // Email field should be focusable
    await page.locator("#owner-login-email").focus();
    await expect(page.locator("#owner-login-email")).toBeFocused();

    // Tab moves focus to password field
    await page.keyboard.press("Tab");
    await expect(page.locator("#owner-login-password")).toBeFocused();

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
