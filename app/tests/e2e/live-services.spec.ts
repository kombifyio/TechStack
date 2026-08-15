import { test, expect } from "@playwright/test";
import { authenticateViaApi } from "../helpers/test-utils";

/**
 * Live Service Smoke Tests
 *
 * Verifies that the deployed kombify-TechStack services are running and
 * correctly configured. Targets production/staging URLs.
 *
 * Run manually (not in CI):
 *   PLAYWRIGHT_BASE_URL=https://techstack.kombify.io pnpm exec playwright test --config=playwright.integration.config.ts tests/e2e/live-services.spec.ts
 */

const BASE_URL = process.env.LIVE_SERVICE_URL || "https://techstack.kombify.io";

test.describe("Live Service Smoke Tests", () => {
  test("frontend loads successfully", async ({ request }) => {
    const response = await request.get(BASE_URL);
    expect(response.status()).toBe(200);
    const body = await response.text();
    expect(body).toContain("kombify");
  });

  test("API health endpoint responds", async ({ request }) => {
    const response = await request.get(`${BASE_URL}/api/v1/health`);
    expect(response.status()).toBe(200);
    const data = await response.json();
    expect(data.data?.status || data.status).toBeTruthy();
  });

  test("auth mode returns cloud for SaaS deployment", async ({ request }) => {
    const response = await request.get(`${BASE_URL}/api/v1/auth/mode`);
    expect(response.status()).toBe(200);
    const data = await response.json();
    expect(data.data.mode).toBe("cloud");
    expect(data.data.allow_local_login).toBe(false);
  });

  test("auth config endpoint is removed (settings surface reduction)", async ({
    request,
  }) => {
    const { token } = await authenticateViaApi(BASE_URL);
    const response = await request.put(`${BASE_URL}/api/v1/auth/config`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { mode: "local", allow_local_login: true },
    });
    expect(response.status()).toBe(404);
  });

  test("SSO redirect URL targets the TechStack cloud login entrypoint", async ({
    request,
  }) => {
    const response = await request.get(`${BASE_URL}/api/v1/auth/mode`);
    expect(response.status()).toBe(200);
    const data = await response.json();
    if (data.data.cloud_auth_url) {
      expect(data.data.cloud_auth_url).toContain("/api/v2/auth/login");
    }
  });
});
