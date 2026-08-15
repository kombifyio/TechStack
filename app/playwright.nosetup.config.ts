import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:5261";
const reuseExistingServer = process.env.PLAYWRIGHT_REUSE_SERVER === "1";
const webServerCommand =
  process.env.PLAYWRIGHT_WEB_SERVER_COMMAND ??
  "node ./scripts/playwright-no-setup-webserver.mjs";

export default defineConfig({
  testDir: "./tests",
  timeout: 60000,
  expect: { timeout: 10000 },
  retries: 0,
  webServer: reuseExistingServer
    ? undefined
    : {
        command: webServerCommand,
        url: baseURL,
        timeout: 120_000,
      },
  use: {
    baseURL,
    trace: "off",
    video: "off",
    screenshot: "only-on-failure",
    headless: true,
    viewport: { width: 1280, height: 720 },
    locale: "en-US",
    timezoneId: "UTC",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
