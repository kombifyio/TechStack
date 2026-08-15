import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:5261";

/**
 * Playwright config for integration tests against real Docker backend.
 * - Serial execution, single worker
 * - No global setup/teardown (services already running from mise pipeline)
 * - Longer timeouts for stack creation
 */
export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: "integration-flow.spec.ts",
  timeout: 120_000,
  expect: {
    timeout: 15_000,
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.05,
      threshold: 0.3,
      animations: "disabled",
    },
  },
  retries: 0,
  workers: 1,
  fullyParallel: false,
  use: {
    baseURL,
    trace: "on-first-retry",
    video: "retain-on-failure",
    screenshot: "only-on-failure",
    headless: true,
    viewport: { width: 1280, height: 720 },
    deviceScaleFactor: 1,
    hasTouch: false,
    isMobile: false,
    locale: "en-US",
    timezoneId: "UTC",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
