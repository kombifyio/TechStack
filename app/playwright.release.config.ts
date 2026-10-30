import { defineConfig, devices } from "@playwright/test";

// A failed test would attach an ARIA snapshot of the page to the report, and
// that snapshot prints input values, passwords included.
process.env.PLAYWRIGHT_NO_COPY_PROMPT ??= "1";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:5261";

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: "release-smoke.spec.ts",
  timeout: 60_000,
  expect: {
    timeout: 15_000,
  },
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [["./tests/helpers/drop-error-context-reporter.ts"], [process.env.CI ? "dot" : "list"]],
  use: {
    baseURL,
    trace: "off",
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
