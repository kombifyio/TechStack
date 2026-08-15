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
const skipBundledBrowserInstall = isTruthyEnv(
  process.env.TECHSTACK_RUNTIME_E2E_SKIP_BROWSER_INSTALL,
);
const recordVideo =
  browserChannel || skipBundledBrowserInstall ? "off" : "retain-on-failure";

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
      `Runtime Auth smoke must target the live SaaS product over HTTPS, got ${value}. Use TECHSTACK_RUNTIME_E2E_PRODUCT_URL for a real product origin.`,
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}

function isTruthyEnv(value: string | undefined): boolean {
  return ["1", "true", "yes", "on"].includes(
    (value ?? "").trim().toLowerCase(),
  );
}

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: ["runtime-auth-smoke.spec.ts", "embedded-sso-session.spec.ts"],
  timeout: 300_000,
  expect: {
    timeout: 30_000,
  },
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [
    ["list"],
    ["html", { outputFolder: `${outputDir}/auth-smoke-report`, open: "never" }],
  ],
  outputDir: `${outputDir}/auth-smoke-results`,
  use: {
    baseURL,
    trace: "retain-on-failure",
    video: recordVideo,
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
      use: {
        ...devices["Desktop Chrome"],
        ...(browserChannel ? { channel: browserChannel } : {}),
      },
    },
  ],
});
