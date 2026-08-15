import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:5261";

/**
 * Dev-only Playwright config for StackKits checkout compatibility tests
 * (Stack + StackKits VM). This is not TechStack release evidence.
 * - Long timeouts (deployment takes time)
 * - Serial execution, single worker
 * - No webServer (services already running from mise pipeline)
 * - Screenshots are captured as release evidence by default; set
 *   ORCHESTRATION_STRICT_SCREENSHOTS=1 to enable snapshot comparisons.
 */
export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: "orchestration-flow.spec.ts",
  timeout: 300_000, // 5 min per test — stack creation + deployment takes time
  expect: {
    timeout: 30_000,
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.02,
      threshold: 0.3,
      animations: "disabled",
    },
  },
  retries: 0,
  workers: 1,
  fullyParallel: false,
  use: {
    baseURL,
    trace: "retain-on-failure",
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
  reporter: [["html", { outputFolder: "playwright-report-orchestration" }]],
  outputDir: "test-results-orchestration",
});
