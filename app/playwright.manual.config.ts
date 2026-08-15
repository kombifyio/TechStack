import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for manual testing (without Docker)
 * Usage: npx playwright test --config=playwright.manual.config.ts
 *
 * Prerequisites:
 * 1. Backend must be running (recommended: Docker): docker compose up -d
 *    Or manual (cross-platform): go run ../cmd/techstack serve --http=:5260 --dir=./pb_data
 * 2. Frontend will be started automatically by this config
 */
const baseURL = process.env.PLAYWRIGHT_BASE_URL;

if (!baseURL) {
  throw new Error(
    "PLAYWRIGHT_BASE_URL must be set (e.g. https://<your-host>:<port>).",
  );
}

export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0,
  // Use single worker to avoid multiple browser windows
  workers: 1,
  // Run tests in the order they are defined
  fullyParallel: false,
  // No global setup/teardown - backend is started manually
  // globalSetup: "./tests/global-setup.ts",
  // globalTeardown: "./tests/global-teardown.ts",
  // Start frontend dev server automatically
  webServer: {
    command: "pnpm dev",
    url: baseURL,
    reuseExistingServer: true,
    timeout: 120_000,
    env: {
      ...(process.env.VITE_API_URL
        ? { VITE_API_URL: process.env.VITE_API_URL }
        : {}),
      ...(process.env.TECHSTACK_API_URL
        ? { TECHSTACK_API_URL: process.env.TECHSTACK_API_URL }
        : {}),
    },
  },
  use: {
    baseURL,
    trace: "on-first-retry",
    video: "retain-on-failure",
    screenshot: "only-on-failure",
    // Toggle headless via PLAYWRIGHT_HEADLESS env var:
    // - Local (headed, default):  npx playwright test --config=playwright.manual.config.ts
    // - CI / headless:            PLAYWRIGHT_HEADLESS=true npx playwright test --config=playwright.manual.config.ts
    headless: process.env.PLAYWRIGHT_HEADLESS === "true",
    viewport: { width: 1280, height: 720 },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
