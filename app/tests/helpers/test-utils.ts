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
  await page.goto("/client/local", { waitUntil: "domcontentloaded" });
  await page.waitForURL(/\/client\/local|\/dashboard/, { timeout: 15_000 });
  if (page.url().includes("/dashboard")) {
    return;
  }
  const existingEmail = page.getByTestId("windows-local-existing-email");
  if (await existingEmail.isVisible().catch(() => false)) {
    await existingEmail.fill(credentials.email);
    await page
      .getByTestId("windows-local-existing-password")
      .fill(credentials.password);
    await page.getByTestId("windows-local-existing-submit").click();
  } else {
    await page.getByTestId("windows-local-admin-email").fill(credentials.email);
    await page
      .getByTestId("windows-local-admin-password")
      .fill(credentials.password);
    await page.getByTestId("windows-local-setup-submit").click();
  }
  await page.waitForURL(/\/dashboard/, { timeout: 30_000 });
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
 * Install the current local V2 auth browser boundary without a real backend.
 * Callers may register narrower route handlers afterwards to override these
 * defaults for hosted or first-run behavior.
 */
export async function mockLoggedInContext(
  context: BrowserContext,
  opts: { userId?: string; email?: string; allowMockAuth?: boolean } = {},
) {
  if (!opts.allowMockAuth && process.env.TECHSTACK_ALLOW_MOCK_AUTH !== "1") {
    throw new Error(
      "mockLoggedInContext uses mocked V2 auth and is not allowed in release happy-path tests. Pass allowMockAuth only for isolated mocked-UI tests.",
    );
  }

  const userId = opts.userId ?? "user_test";
  const email =
    opts.email || getTechStackTestUser("admin").email || "mock-user@test.local";

  await context.addInitScript(() => {
    window.sessionStorage.clear();
    window.localStorage.removeItem("creatingStackName");
    window.localStorage.removeItem("creatingStackId");
    window.localStorage.removeItem("creatingJobId");
    window.localStorage.removeItem("creatingStackConfig");
    window.localStorage.removeItem("pocketbase_auth");
  });
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      json: {
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: false,
          allow_local_login: true,
        },
      },
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({ json: { providers: [] } });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      json: {
        subject: userId,
        tenantId: "default",
        email,
        provider: "local",
        role: "owner",
      },
    });
  });
  await context.route("**/api/v1/csrf**", async (route) => {
    await route.fulfill({
      headers: { "X-CSRF-Token": "mock-csrf-token" },
      json: { token: "mock-csrf-token" },
    });
  });
}

/**
 * Add a use case on wizard step 1 through its card's "Add to your kit" toggle.
 */
export async function selectUseCase(page: Page, feature: string) {
  const card = page.getByTestId(`easy-feature-${feature}`);
  await card.getByRole("button", { name: /^Add to your kit / }).click();
  await expect(
    card.getByRole("button", { name: /^Remove from your kit /, pressed: true }),
  ).toBeVisible();
}

/**
 * Complete the easy wizard flow
 */
type EasyWizardPath = {
  feature?: string;
  serverProvisioning?: "kombify-cloud" | "connect-remote" | "install-command";
  access?: "home" | "anywhere";
  users?: "solo" | "shared";
};

/**
 * Walk the easy wizard through steps 1-4 and stop on the owner step. The one
 * shared path keeps every creation spec on the current wizard contract.
 */
export async function walkEasyWizardToOwnerStep(
  page: Page,
  {
    feature = "storage",
    serverProvisioning = "kombify-cloud",
    access = "anywhere",
    users = "solo",
  }: EasyWizardPath = {},
) {
  await page
    .getByTestId("hydrated")
    .waitFor({ state: "attached", timeout: 10000 });
  await selectUseCase(page, feature);
  await page.getByTestId("wizard-next").click();

  await page
    .getByTestId(
      serverProvisioning === "kombify-cloud"
        ? "server-branch-new"
        : "server-branch-owned",
    )
    .click();
  await page.getByTestId(`server-mode-${serverProvisioning}`).click();
  if (serverProvisioning === "connect-remote") {
    await page.getByTestId("remote-server-host").fill("server.test.local");
  }
  await page.getByTestId("wizard-next").click();

  await page.getByTestId(`easy-access-${access}`).click();
  await page.getByTestId("wizard-next").click();

  const household = page.getByTestId(`easy-users-${users}`);
  await household.click();
  await expect(household.getByRole("radio")).toBeChecked();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("easy-step-5")).toBeVisible();
}

/**
 * Pick the Homelab owner on the owner step. The radios are visually hidden,
 * so the visible choice card (their label) takes the click.
 */
export async function chooseOwnerSource(
  page: Page,
  source: "local" | "cloud-linked",
) {
  const radio = page.getByTestId(
    source === "local"
      ? "owner-source-local"
      : "owner-source-cloud-linked-select",
  );
  await radio.locator("xpath=ancestor::label[1]").click();
  await expect(radio).toBeChecked();
}

/**
 * Complete the easy wizard with a new local Homelab owner and submit it.
 */
export async function completeEasyWizard(
  page: Page,
  {
    recoveryPassphrase = "correct horse battery staple 12!",
    admin = {
      username: "admin",
      email: "admin@test.local",
      displayName: "Admin",
    },
    ...path
  }: EasyWizardPath & {
    recoveryPassphrase?: string;
    admin?: { username: string; email: string; displayName?: string };
  } = {},
) {
  await walkEasyWizardToOwnerStep(page, path);

  // Username and recovery live behind the "Make it yours" tabs.
  await chooseOwnerSource(page, "local");
  await page.locator("#owner-email").fill(admin.email);
  if (admin.displayName) {
    await page.locator("#owner-display-name").fill(admin.displayName);
  }
  await page.getByRole("button", { name: /^Make it yours/ }).click();
  await page.getByRole("tab", { name: /^Sign-in/ }).click();
  await page.locator("#owner-username").fill(admin.username);
  await page.getByRole("tab", { name: /^Recovery/ }).click();
  await page.locator("#recovery-passphrase").fill(recoveryPassphrase);
  await page.locator("#recovery-passphrase-confirm").fill(recoveryPassphrase);
  await page.locator("#recovery-passphrase-confirm").blur();
  await page.getByTestId("wizard-create").click();
}
