import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL;
const webServerCommand =
  process.env.PLAYWRIGHT_WEB_SERVER_COMMAND ??
  "npm run dev -- --host 127.0.0.1";

if (!baseURL) {
  throw new Error(
    "PLAYWRIGHT_BASE_URL must be set (e.g. https://<your-host>:<port>).",
  );
}

export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  expect: {
    timeout: 10_000,
    // Visual regression testing configuration
    toHaveScreenshot: {
      maxDiffPixels: 100, // Allow minor rendering differences
      threshold: 0.2, // 20% difference threshold
      animations: "disabled", // Disable animations for consistent screenshots
    },
  },
  retries: 0,
  globalSetup: "./tests/global-setup.ts",
  globalTeardown: "./tests/global-teardown.ts",
  webServer: {
    command: webServerCommand,
    url: baseURL,
    // Avoid hitting stale dev servers (common cause of DOM mismatch in tests).
    // Opt-in reuse via PLAYWRIGHT_REUSE_SERVER=1.
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE_SERVER === "1",
    timeout: 120_000,
  },
  use: {
    baseURL,
    trace: "on-first-retry",
    video: "retain-on-failure",
    screenshot: "only-on-failure",
    headless: true,
    viewport: { width: 1280, height: 720 },
    // Standardize for visual regression tests
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
    // Optional: Add mobile viewport testing
    {
      name: "mobile-chrome",
      use: {
        ...devices["Pixel 5"],
      },
    },
  ],
});
