import type { BrowserContext, Page } from "@playwright/test";
import { expect } from "@playwright/test";
import {
  getTechStackTestUser,
  requireTechStackTestUser,
} from "../../src/lib/testing/techstack-test-users";

/**
 * Test Utilities & Helpers
 *
 * Common functions used across multiple test files.
 */

/**
 * Default test credentials
 */
export const TEST_CREDENTIALS = {
  get email() {
    return requireTechStackTestUser("admin").email;
  },
  get password() {
    return requireTechStackTestUser("admin").password;
  },
};

/**
 * API endpoints
 */
function normalizeBaseUrl(url: string): string {
  return url.replace(/\/+$/, "");
}

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`Missing required env var: ${name}`);
  return value;
}

export function requireApiBase(): string {
  const value = process.env.TECHSTACK_API_URL ?? process.env.API_URL;
  if (!value)
    throw new Error(
      "Missing API base URL. Set TECHSTACK_API_URL (or API_URL) to a full URL.",
    );
  return normalizeBaseUrl(value);
}

export function requireAppBase(baseURL?: string): string {
  const value =
    baseURL ?? process.env.TECHSTACK_APP_URL ?? process.env.PLAYWRIGHT_BASE_URL;
  if (!value)
    throw new Error(
      "Missing App base URL. Set PLAYWRIGHT_BASE_URL (or TECHSTACK_APP_URL) to a full URL.",
    );
  return normalizeBaseUrl(value);
}

/**
 * Authenticate against real PocketBase API and return the auth token.
 */
export async function authenticateViaApi(
  apiBase?: string,
  email?: string,
  password?: string,
): Promise<{ token: string; userId: string }> {
  const base = apiBase ?? requireApiBase();
  const credentials =
    email && password ? { email, password } : requireTechStackTestUser("admin");
  const response = await fetch(
    `${base}/api/collections/users/auth-with-password`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        identity: credentials.email,
        password: credentials.password,
      }),
    },
  );

  if (!response.ok) {
    const body = await response.text();
    if (response.status === 401 || response.status === 403) {
      throw new Error(
        `TechStack E2E auth failed with HTTP ${response.status}. Happy-path tests must use a configured real test user from the configured environment, not fake local credentials. Response: ${body}`,
      );
    }
    throw new Error(`Auth failed (HTTP ${response.status}): ${body}`);
  }

  const json = (await response.json()) as any;
  return { token: json.token, userId: json.record?.id ?? "" };
}

/**
 * Create a pairing token via the real API. Requires an auth token.
 */
export async function createPairingTokenViaApi(
  authToken: string,
  apiBase?: string,
  name = "test-worker-token",
  expiryMinutes = 60,
): Promise<{ id: string; token: string; expires_at: string }> {
  const base = apiBase ?? requireApiBase();
  const response = await fetch(`${base}/api/v1/trust/pairing-tokens`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${authToken}`,
    },
    body: JSON.stringify({ name, expiry_minutes: expiryMinutes }),
  });

  if (!response.ok) {
    const body = await response.text();
    throw new Error(
      `Create pairing token failed (HTTP ${response.status}): ${body}`,
    );
  }

  const json = (await response.json()) as any;
  return json.data;
}

/**
 * Register a worker via the real API. Uses pairing token (no auth header needed).
 */
export async function registerWorkerViaApi(
  pairingToken: string,
  hostname: string,
  apiBase?: string,
  os = "linux",
  arch = "amd64",
): Promise<{ worker_id: string; accepted: boolean }> {
  const base = apiBase ?? requireApiBase();
  const response = await fetch(`${base}/api/v1/workers/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: pairingToken, hostname, os, arch }),
  });

  if (!response.ok) {
    const body = await response.text();
    throw new Error(
      `Worker registration failed (HTTP ${response.status}): ${body}`,
    );
  }

  const json = (await response.json()) as any;
  return json.data;
}

/**
 * Approve a worker via the real API. Requires auth token.
 */
export async function approveWorkerViaApi(
  authToken: string,
  workerId: string,
  apiBase?: string,
): Promise<void> {
  const base = apiBase ?? requireApiBase();
  const response = await fetch(`${base}/api/v1/workers/${workerId}/approve`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${authToken}`,
    },
  });

  if (!response.ok) {
    const body = await response.text();
    throw new Error(
      `Worker approval failed (HTTP ${response.status}): ${body}`,
    );
  }
}

/**
 * Reset the stack via API — deletes the stack which triggers backend cleanup
 * of nodes, services, jobs, workers, pairing tokens, and activity log.
 */
export async function resetStackViaApi(
  authToken: string,
  apiBase?: string,
): Promise<boolean> {
  const base = apiBase ?? requireApiBase();

  const authHeaders = { Authorization: `Bearer ${authToken}` };
  const listPocketBaseRecords = async (collection: string): Promise<any[]> => {
    const records: any[] = [];
    let page = 1;
    let totalPages = 1;

    do {
      const response = await fetch(
        `${base}/api/collections/${collection}/records?page=${page}&perPage=100`,
        { headers: authHeaders },
      );
      if (!response.ok) {
        throw new Error(
          `Failed to list ${collection} (HTTP ${response.status})`,
        );
      }
      const json = (await response.json()) as any;
      records.push(...(json.items ?? []));
      totalPages = Number(json.totalPages ?? 1);
      page += 1;
    } while (page <= totalPages);

    return records;
  };

  const deletePocketBaseRecord = async (
    collection: string,
    id: string,
  ): Promise<void> => {
    const response = await fetch(
      `${base}/api/collections/${collection}/records/${id}`,
      {
        method: "DELETE",
        headers: authHeaders,
      },
    );
    if (!response.ok) {
      const body = await response.text();
      throw new Error(
        `Failed to delete ${collection}/${id} (HTTP ${response.status}): ${body}`,
      );
    }
  };

  const stacks = await listPocketBaseRecords("stacks");
  for (const stack of stacks) {
    await deletePocketBaseRecord("stacks", stack.id);
  }

  const workers = await listWorkersViaApi(authToken, base);
  for (const worker of workers) {
    await deletePocketBaseRecord("workers", worker.id);
  }

  const tokens = await listPocketBaseRecords("pairing_tokens");
  for (const token of tokens) {
    await deletePocketBaseRecord("pairing_tokens", token.id);
  }

  return stacks.length > 0;
}

/**
 * List workers via API. Returns the worker array.
 */
export async function listWorkersViaApi(
  authToken: string,
  apiBase?: string,
): Promise<any[]> {
  const base = apiBase ?? requireApiBase();
  const resp = await fetch(`${base}/api/v1/workers`, {
    headers: { Authorization: `Bearer ${authToken}` },
  });
  if (!resp.ok) {
    throw new Error(`List workers failed (HTTP ${resp.status})`);
  }
  const json = (await resp.json()) as any;
  const workers = json.data ?? json;
  return Array.isArray(workers) ? workers : [];
}

/**
 * Login helper - performs full login flow with proper wait times
 */
export async function login(page: Page, email?: string, password?: string) {
  const credentials =
    email && password ? { email, password } : requireTechStackTestUser("admin");
  await page.goto("/login", { waitUntil: "domcontentloaded" });
  // Wait for PocketBase connection to be established
  await page.waitForTimeout(1000);
  await page.locator("#owner-login-email").fill(credentials.email);
  await page.locator("#owner-login-password").fill(credentials.password);
  await page.getByRole("button", { name: "Sign in locally" }).click();
  await page.waitForURL(/\/stacks/, { timeout: 30_000 });
}

/**
 * Navigate to a specific page after login
 */
export async function navigateTo(page: Page, path: string) {
  await page.goto(path);
  await page.waitForLoadState("domcontentloaded");
}

/**
 * Wait for page to finish loading (no skeleton loaders)
 */
export async function waitForPageLoad(page: Page, timeout = 5000) {
  await page.waitForTimeout(500); // Initial delay
  const startTime = Date.now();

  while (Date.now() - startTime < timeout) {
    const skeletonCount = await page.locator(".animate-pulse").count();
    if (skeletonCount === 0) {
      return;
    }
    await page.waitForTimeout(200);
  }
}

/**
 * Check that a page has no error banners
 */
export async function expectNoErrors(page: Page) {
  const errorCount = await page.locator(".border-red-700").count();
  expect(errorCount).toBe(0);
}

/**
 * Check that sidebar is visible (indicates logged in state)
 */
export async function expectLoggedIn(page: Page) {
  await expect(page.locator("aside")).toBeVisible();
}

function makeFakeJwt(payload: Record<string, unknown>): string {
  const encode = (obj: Record<string, unknown>) =>
    Buffer.from(JSON.stringify(obj)).toString("base64url");
  const header = { alg: "none", typ: "JWT" };
  return `${encode(header)}.${encode(payload)}.sig`;
}

/**
 * Seed PocketBase auth state without a real backend.
 *
 * The PocketBase JS SDK considers a JWT "valid" based on its exp claim,
 * without verifying the signature. This is sufficient for UI flows that only
 * gate on `isAuthenticated()` and `currentUser`.
 */
export async function mockLoggedInContext(
  context: BrowserContext,
  opts: { userId?: string; email?: string; allowMockAuth?: boolean } = {},
) {
  if (!opts.allowMockAuth && process.env.TECHSTACK_ALLOW_MOCK_AUTH !== "1") {
    throw new Error(
      "mockLoggedInContext uses fake localStorage auth and is not allowed in release happy-path tests. Pass allowMockAuth only for isolated mocked-UI tests.",
    );
  }

  const userId = opts.userId ?? "user_test";
  const email =
    opts.email || getTechStackTestUser("admin").email || "mock-user@test.local";

  const token = makeFakeJwt({
    exp: 4102444800, // 2100-01-01
    id: userId,
  });

  const model = {
    id: userId,
    email,
  };

  await context.addInitScript(
    ({ token, model }) => {
      window.sessionStorage.clear();
      window.localStorage.removeItem("creatingStackName");
      window.localStorage.removeItem("creatingStackId");
      window.localStorage.removeItem("creatingJobId");
      window.localStorage.removeItem("creatingStackConfig");
      window.localStorage.setItem(
        "pocketbase_auth",
        JSON.stringify({ token, model }),
      );
    },
    { token, model },
  );
}

/**
 * Mock API endpoint
 */
export async function mockApiEndpoint(
  page: Page,
  urlPattern: string,
  response: { status: number; body: object },
) {
  await page.route(urlPattern, async (route) => {
    await route.fulfill({
      status: response.status,
      contentType: "application/json",
      body: JSON.stringify(response.body),
    });
  });
}

/**
 * Complete the easy wizard flow
 */
export async function completeEasyWizard(
  page: Page,
  options: {
    feature?: string;
    serverProvisioning?: "kombify-cloud" | "connect-remote" | "install-command";
    access?: "home" | "anywhere";
    users?: "me" | "family" | "public";
    ownerSource?: "local" | "cloud";
    recoveryPassphrase?: string;
    admin?: {
      username: string;
      email: string;
      displayName?: string;
      password: string;
    };
  } = {},
) {
  const {
    feature = "storage",
    serverProvisioning = "kombify-cloud",
    access = "anywhere",
    users = "me",
    ownerSource = "local",
    recoveryPassphrase = "correct horse battery staple 12!",
    admin = {
      username: "admin",
      email: "admin@test.local",
      displayName: "Admin",
      password: "testpass123",
    },
  } = options;

  // Step 1: Features
  await page
    .getByTestId("hydrated")
    .waitFor({ state: "attached", timeout: 10000 });
  await page.getByTestId(`easy-feature-${feature}`).check();
  await page.getByTestId("wizard-next").click();

  // Step 2: Server provisioning
  await page.getByTestId(`server-mode-${serverProvisioning}`).click();
  if (serverProvisioning === "connect-remote") {
    await page.getByTestId("remote-server-host").fill("server.test.local");
  }
  await page.getByTestId("wizard-next").click();

  // Step 3: Access
  await page.getByTestId(`easy-access-${access}`).click();
  await page.getByTestId("wizard-next").click();

  // Step 4: Users
  await page.getByTestId(`easy-users-${users}`).check();
  await page.getByTestId("wizard-next").click();

  // Step 5: Login + Create
  await page.getByTestId(`owner-source-${ownerSource}`).click();
  await page.locator("#owner-username").fill(admin.username);
  await page.locator("#owner-email").fill(admin.email);
  if (admin.displayName) {
    await page.locator("#owner-display-name").fill(admin.displayName);
  }
  await page.locator("#recovery-passphrase").fill(recoveryPassphrase);
  await page.locator("#recovery-passphrase-confirm").fill(recoveryPassphrase);
  await page.locator("#recovery-passphrase-confirm").blur();
  await page.getByTestId("easy-auth-password").click();
  await page.locator("#admin-password").fill(admin.password);
  await page.locator("#admin-password-confirm").fill(admin.password);
  await page.getByTestId("wizard-create").click();
}

/**
 * Get current page performance metrics
 */
export async function getPerformanceMetrics(page: Page) {
  return await page.evaluate(() => {
    const timing = performance.timing;
    return {
      loadTime: timing.loadEventEnd - timing.navigationStart,
      domContentLoaded:
        timing.domContentLoadedEventEnd - timing.navigationStart,
      firstPaint: timing.responseEnd - timing.navigationStart,
    };
  });
}
