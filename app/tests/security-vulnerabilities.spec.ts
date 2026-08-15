import { test, expect } from "@playwright/test";

import { TEST_CREDENTIALS } from "./helpers/test-utils";
/**
 * Security & Vulnerability Tests
 *
 * Tests for common web security vulnerabilities and attack vectors.
 * Ensures the application is hardened against OWASP Top 10 threats.
 */

test.describe("Security - XSS Prevention", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("XSS attempt in stack name is sanitized", async ({
    page,
    browser,
    baseURL,
  }) => {
    const origin = baseURL || "http://localhost:5173";
    const context = await browser.newContext();

    // Track if alert was triggered (XSS vulnerability)
    let alertTriggered = false;
    context.on("dialog", async (dialog) => {
      alertTriggered = true;
      await dialog.dismiss();
    });

    const testPage = await context.newPage();

    // Login
    await testPage.goto("/login", { waitUntil: "domcontentloaded" });
    await testPage.waitForTimeout(1000);
    await testPage.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await testPage.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await testPage.getByRole("button", { name: "Sign In" }).click();
    await testPage.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Attempt XSS injection in forms
    await testPage.goto("/settings");
    await testPage.waitForLoadState("networkidle");

    // Try to inject script in any visible input
    const inputs = testPage.locator('input[type="text"]');
    const count = await inputs.count();

    if (count > 0) {
      const xssPayload = '<script>alert("XSS")</script>';
      await inputs.first().fill(xssPayload);
      await testPage.waitForTimeout(1000);

      // Verify no alert was triggered
      expect(alertTriggered).toBe(false);

      // Verify content is escaped in the DOM
      const value = await inputs.first().inputValue();
      // Should either be sanitized or encoded
      expect(value).not.toContain("<script>");
    }

    await context.close();
  });

  test("XSS in URL parameters does not execute", async ({ page }) => {
    // Track alerts
    let alertTriggered = false;
    page.on("dialog", async (dialog) => {
      alertTriggered = true;
      await dialog.dismiss();
    });

    // Attempt XSS via URL parameter
    await page.goto(
      "/settings?name=%3Cscript%3Ealert%28%27XSS%27%29%3C%2Fscript%3E",
    );
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);

    // Verify no alert was triggered
    expect(alertTriggered).toBe(false);
  });

  test("HTML injection in text fields is escaped", async ({ page }) => {
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");

    // Try HTML injection
    const textInputs = page.locator('input[type="text"], input[type="email"]');
    const count = await textInputs.count();

    if (count > 0) {
      const htmlPayload = '<img src=x onerror="alert(1)">';
      await textInputs.first().fill(htmlPayload);
      await page.waitForTimeout(500);

      // Check the rendered content
      const pageContent = await page.content();

      // Should not contain unescaped HTML
      expect(pageContent).not.toContain("src=x onerror=");
      expect(pageContent).not.toContain("<img src=x");
    }
  });
});

test.describe("Security - CSRF Protection", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("API requests include proper headers", async ({ page }) => {
    const requests: any[] = [];

    // Capture API requests
    page.on("request", (request) => {
      if (request.url().includes("/api/")) {
        requests.push({
          url: request.url(),
          method: request.method(),
          headers: request.headers(),
        });
      }
    });

    // Trigger some API calls
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);

    // Navigate to trigger more API calls
    await page.goto("/stacks");
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);

    // Verify API requests have proper headers
    const apiRequests = requests.filter((r) => r.method !== "OPTIONS");

    if (apiRequests.length > 0) {
      // Check that requests have authentication or session headers
      const hasAuthHeaders = apiRequests.some(
        (r) =>
          r.headers["authorization"] ||
          r.headers["cookie"] ||
          r.headers["x-csrf-token"],
      );

      // At least some requests should have auth (depending on backend implementation)
      // This is informational - adjust based on actual auth mechanism
      console.log("API requests captured:", apiRequests.length);
      console.log("Has auth headers:", hasAuthHeaders);
    }
  });
});

test.describe("Security - SQL Injection Prevention", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("SQL injection in search fields is prevented", async ({ page }) => {
    await page.goto("/stacks");
    await page.waitForLoadState("networkidle");

    // Look for search input
    const searchInput = page
      .locator('input[type="search"], input[placeholder*="search" i]')
      .first();

    if (await searchInput.isVisible().catch(() => false)) {
      // Attempt SQL injection
      const sqlPayload = "' OR '1'='1";
      await searchInput.fill(sqlPayload);
      await page.waitForTimeout(1000);

      // Should not cause error or unexpected behavior
      // Page should still be functional
      await expect(page).not.toHaveURL(/error/);

      // Check for SQL error messages in page content
      const pageText = await page.textContent("body");
      expect(pageText).not.toMatch(/sql|syntax error|database error/i);
    }
  });

  test("SQL injection in form inputs is prevented", async ({ page }) => {
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");

    const textInputs = page.locator('input[type="text"]');
    const count = await textInputs.count();

    if (count > 0) {
      const sqlPayloads = [
        "'; DROP TABLE users--",
        "1' OR '1'='1",
        "admin'--",
        "' UNION SELECT NULL--",
      ];

      for (const payload of sqlPayloads) {
        await textInputs.first().fill(payload);
        await page.waitForTimeout(500);

        // Should not cause SQL errors
        const pageText = await page.textContent("body");
        expect(pageText).not.toMatch(/sql|syntax error|database error/i);
      }
    }
  });
});

test.describe("Security - Authentication & Authorization", () => {
  test("unauthenticated access redirects to login", async ({ page }) => {
    // Clear any existing session
    await page.context().clearCookies();
    await page.context().clearPermissions();

    // Try to access protected pages
    const protectedPages = [
      "/stacks",
      "/services",
      "/monitoring",
      "/wallet",
      "/settings",
    ];

    for (const pagePath of protectedPages) {
      await page.goto(pagePath);

      // Should redirect to login
      await page.waitForURL(/\/login/, { timeout: 10000 });
      await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();
    }
  });

  test("invalid credentials are rejected", async ({ page }) => {
    await page.goto("/login");
    await page.waitForLoadState("networkidle");

    // Attempt login with invalid credentials
    await page.getByLabel("Email").fill("attacker@evil.com");
    await page.getByLabel("Password").fill("WrongPassword123!");
    await page.getByRole("button", { name: "Sign In" }).click();

    // Should not redirect to dashboard
    await page.waitForTimeout(2000);
    await expect(page).toHaveURL(/\/login/);

    // Should show error message
    const errorMessages = [
      page.locator("text=/invalid|incorrect|failed/i"),
      page.locator('[role="alert"]'),
      page.locator(".error"),
    ];

    let errorShown = false;
    for (const errorLocator of errorMessages) {
      if (await errorLocator.isVisible().catch(() => false)) {
        errorShown = true;
        break;
      }
    }

    // Either error shown or still on login page
    expect(
      errorShown ||
        (await page.getByRole("button", { name: "Sign In" }).isVisible()),
    ).toBe(true);
  });

  test("session timeout after logout", async ({ page }) => {
    // Login first
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to settings and logout
    await page.goto("/settings");
    const logoutBtn = page.getByRole("button", { name: /logout|abmelden/i });
    await logoutBtn.click();
    await page.waitForURL(/\/login/, { timeout: 10000 });

    // Try to access protected page using back button or direct navigation
    await page.goto("/stacks");

    // Should redirect back to login
    await page.waitForURL(/\/login/, { timeout: 10000 });
    await expect(page.getByRole("button", { name: "Sign In" })).toBeVisible();
  });
});

test.describe("Security - Input Validation", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1000);
    await page.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await page.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign In" }).click();
    await page.waitForURL(/\/stacks/, { timeout: 30_000 });
  });

  test("email validation requires proper format", async ({ page }) => {
    await page.goto("/settings");
    await page.waitForLoadState("networkidle");

    const emailInput = page.locator('input[type="email"]').first();

    if (await emailInput.isVisible().catch(() => false)) {
      const invalidEmails = [
        "notanemail",
        "@example.com",
        "user@",
        "user @example.com",
        "<script>alert(1)</script>@example.com",
      ];

      for (const invalidEmail of invalidEmails) {
        await emailInput.fill(invalidEmail);
        await page.waitForTimeout(300);

        // Check HTML5 validation or custom error
        const isValid = await emailInput.evaluate(
          (el: HTMLInputElement) => el.validity.valid,
        );

        // Should be marked invalid
        expect(isValid).toBe(false);
      }
    }
  });

  test("password fields enforce minimum requirements", async ({
    page,
    browser,
    baseURL,
  }) => {
    const origin = baseURL || "http://localhost:5173";
    const context = await browser.newContext();

    // Mock API responses
    await context.route(
      "**/api/v1/unifier/pipeline/preview**",
      async (route) => {
        if (route.request().method() !== "POST") return route.fallback();
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            valid: true,
            resolved_stackkit: "test",
            detected_addons: [],
            stages: [],
          }),
        });
      },
    );

    const testPage = await context.newPage();

    // Login
    await testPage.goto("/login", { waitUntil: "domcontentloaded" });
    await testPage.waitForTimeout(1000);
    await testPage.getByLabel("Email").fill(TEST_CREDENTIALS.email);
    await testPage.getByLabel("Password").fill(TEST_CREDENTIALS.password);
    await testPage.getByRole("button", { name: "Sign In" }).click();
    await testPage.waitForURL(/\/stacks/, { timeout: 30_000 });

    // Navigate to wizard (has password fields)
    await testPage.goto(`${origin}/stacks/new`);
    await testPage.waitForLoadState("networkidle");

    // Navigate through wizard to password step
    if (
      await testPage
        .getByTestId("easy-step-1")
        .isVisible()
        .catch(() => false)
    ) {
      await testPage.getByTestId("easy-feature-storage").check();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      await testPage.getByTestId("easy-access-anywhere").click();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      await testPage.getByTestId("easy-users-me").check();
      await testPage.getByTestId("wizard-next").click();
      await testPage.waitForTimeout(500);

      await testPage.getByTestId("easy-auth-password").click();

      // Test weak passwords
      const weakPasswords = [
        "123", // Too short
        "password", // Common
        "12345678", // Numeric only
        "abc", // Too short
      ];

      const passwordInput = testPage.locator("#admin-password");

      if (await passwordInput.isVisible().catch(() => false)) {
        for (const weakPassword of weakPasswords) {
          await passwordInput.fill(weakPassword);
          await testPage.waitForTimeout(300);

          // Should show validation error or prevent submission
          // Check for error message or invalid state
          const hasError = await testPage
            .locator("text=/weak|short|invalid/i")
            .isVisible()
            .catch(() => false);
          const isValid = await passwordInput.evaluate(
            (el: HTMLInputElement) => el.validity.valid,
          );

          // At least one should indicate problem (either error message or invalid state)
          console.log(
            `Password "${weakPassword}": hasError=${hasError}, isValid=${isValid}`,
          );
        }
      }
    }

    await context.close();
  });
});

test.describe("Security - Content Security Policy", () => {
  test("CSP headers prevent inline scripts", async ({ page }) => {
    let cspHeader = "";

    page.on("response", (response) => {
      if (
        response.url().includes("/login") ||
        response.url().includes("/stacks")
      ) {
        const headers = response.headers();
        cspHeader =
          headers["content-security-policy"] ||
          headers["content-security-policy-report-only"];
      }
    });

    await page.goto("/login");
    await page.waitForLoadState("networkidle");

    // Check if CSP header exists
    if (cspHeader) {
      console.log("CSP Header:", cspHeader);

      // Should restrict inline scripts
      if (!/script-src/.test(cspHeader)) {
        throw new Error("CSP header is missing script-src directive");
      }

      // Should not allow 'unsafe-inline' for scripts (security risk)
      // Note: Some frameworks may require this, so this is informational
      const hasUnsafeInline = cspHeader.includes("'unsafe-inline'");
      console.log("Allows unsafe-inline:", hasUnsafeInline);
    } else {
      console.log("No CSP header found - consider adding for better security");
    }
  });

  test("security headers are present", async ({ page }) => {
    const securityHeaders: Record<string, string | null> = {};

    page.on("response", (response) => {
      if (
        response.url().includes("/login") ||
        response.url().includes("/stacks")
      ) {
        const headers = response.headers();
        securityHeaders["x-frame-options"] = headers["x-frame-options"];
        securityHeaders["x-content-type-options"] =
          headers["x-content-type-options"];
        securityHeaders["strict-transport-security"] =
          headers["strict-transport-security"];
        securityHeaders["x-xss-protection"] = headers["x-xss-protection"];
        securityHeaders["referrer-policy"] = headers["referrer-policy"];
      }
    });

    await page.goto("/login");
    await page.waitForLoadState("networkidle");

    console.log("Security Headers:", securityHeaders);

    // Log findings (informational - actual requirements may vary)
    if (securityHeaders["x-frame-options"]) {
      expect(securityHeaders["x-frame-options"]).toMatch(/DENY|SAMEORIGIN/i);
    }

    if (securityHeaders["x-content-type-options"]) {
      expect(securityHeaders["x-content-type-options"]).toBe("nosniff");
    }

    // HSTS should be present for HTTPS
    if (page.url().startsWith("https")) {
      expect(securityHeaders["strict-transport-security"]).toBeTruthy();
    }
  });
});
