import { defineConfig, devices } from "@playwright/test";

const defaultProductURL = "https://techstack.kombify.io";
const configuredProductURL =
  process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL ??
  process.env.TECHSTACK_E2E_PRODUCT_URL;
const configuredBaseURL = process.env.PLAYWRIGHT_BASE_URL;
const baseURL = requireLiveProductURL(
  configuredProductURL ||
    (configuredBaseURL && !isLoopbackURL(configuredBaseURL)
      ? configuredBaseURL
      : defaultProductURL),
);
const outputDir =
  process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e";
const browserChannel = process.env.PLAYWRIGHT_BROWSER_CHANNEL?.trim();

function isLoopbackURL(value: string): boolean {
  try {
    const host = new URL(value).hostname.toLowerCase();
    return host === "localhost" || host === "127.0.0.1" || host === "::1";
  } catch {
    return false;
  }
}

function requireLiveProductURL(value: string): string {
  const parsed = new URL(value);
  if (parsed.protocol !== "https:" || isLoopbackURL(value)) {
    throw new Error(
      `Managed runtime authority preflight must target the live SaaS product over HTTPS, got ${value}.`,
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: "runtime-managed-authority-preflight.spec.ts",
  timeout: 300_000,
  expect: {
    timeout: 30_000,
  },
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [
    ["list"],
    [
      "html",
      {
        outputFolder: `${outputDir}/authority-preflight-report`,
        open: "never",
      },
    ],
  ],
  outputDir: `${outputDir}/authority-preflight-results`,
  use: {
    baseURL,
    // The request carries a short-lived access token. The report below records
    // only redacted claim/authority facts, so failure diagnostics must not
    // persist browser network material or login-screen PII.
    trace: "off",
    video: "off",
    screenshot: "off",
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
      use: {
        ...devices["Desktop Chrome"],
        ...(browserChannel ? { channel: browserChannel } : {}),
      },
    },
  ],
});
