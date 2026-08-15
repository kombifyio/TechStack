import {
  test,
  expect,
  type Browser,
  type BrowserContext,
} from "@playwright/test";
import {
  completeEasyWizard,
  mockLoggedInContext,
  requireAppBase,
} from "./helpers/test-utils";

type SubmittedStackPayload = {
  mode?: string;
  stack_spec?: {
    stackkit?: string;
    metadata?: Record<string, unknown>;
    owner?: Record<string, unknown>;
  };
  options?: {
    owner_username?: string;
    owner_email?: string;
    recovery_passphrase_hash?: string;
  };
};

const STACKKIT_OUTPUTS = {
  identity: {
    owner: {
      username: "owner-admin",
      email: "owner@example.com",
      display_name: "Owner Admin",
    },
  },
  login_gateway: {
    url: "https://basementkit.example.test/login",
    label: "Basement Kit login",
  },
  recovery: {
    bundle_ref: "wallet://stack_identity/recovery",
    passphrase_hash_present: true,
  },
};

const WALLET_ITEMS = [
  {
    id: "wallet_login_gateway",
    collectionId: "wallet",
    collectionName: "wallet",
    created: "2026-05-13 10:00:00.000Z",
    updated: "2026-05-13 10:00:00.000Z",
    name: "Basement Kit Login Gateway",
    kind: "other",
    url: "https://basementkit.example.test/login",
    stack_id: "stack_identity",
    service_id: "stackkit:login-gateway",
    item_class: "launch",
    access_mode: "open",
    source_type: "stackkit",
    source_ref: "stackkit:stack_identity:login-gateway",
    has_secret: false,
  },
  {
    id: "wallet_owner",
    collectionId: "wallet",
    collectionName: "wallet",
    created: "2026-05-13 10:00:00.000Z",
    updated: "2026-05-13 10:00:00.000Z",
    name: "Owner admin",
    kind: "password",
    username: "owner-admin",
    url: "https://basementkit.example.test/admin",
    stack_id: "stack_identity",
    service_id: "pocketid:owner",
    item_class: "user_account",
    access_mode: "manage",
    source_type: "pocketid",
    source_ref: "pocketid:stack_identity:owner",
    has_secret: false,
  },
  {
    id: "wallet_recovery",
    collectionId: "wallet",
    collectionName: "wallet",
    created: "2026-05-13 10:00:00.000Z",
    updated: "2026-05-13 10:00:00.000Z",
    name: "Basement Kit Recovery Bundle",
    kind: "other",
    notes: "Recovery bundle reference: wallet://stack_identity/recovery",
    stack_id: "stack_identity",
    service_id: "stackkit:recovery",
    item_class: "recovery",
    access_mode: "reveal",
    source_type: "stackkit",
    source_ref: "stackkit:stack_identity:recovery",
    revealable: true,
    has_secret: true,
  },
];

async function mockPipelinePreview(context: BrowserContext) {
  await context.route("**/api/v1/unifier/pipeline/preview**", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        valid: true,
        resolved_stackkit: "basement-kit.standard.v1",
        detected_addons: [],
        stages: [],
      }),
    });
  });
}

async function mockDiscovery(context: BrowserContext) {
  await context.route("**/api/v1/discovery/networks", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ networks: [] }),
    });
  });
}

async function mockWallet(context: BrowserContext) {
  await context.route("**/api/collections/wallet/records**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        page: 1,
        perPage: 200,
        totalItems: WALLET_ITEMS.length,
        totalPages: 1,
        items: WALLET_ITEMS,
      }),
    });
  });
}

async function mockCreateStackAndJob(
  context: BrowserContext,
  opts: {
    jobId: string;
    withStackKitOutputs: boolean;
  },
) {
  let submittedPayload: SubmittedStackPayload | null = null;
  let resolveSubmittedPayload: (
    payload: SubmittedStackPayload,
  ) => void = () => {};
  const submittedPayloadPromise = new Promise<SubmittedStackPayload>(
    (resolve) => {
      resolveSubmittedPayload = resolve;
    },
  );

  await context.route("**/api/v1/stacks**", async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      });
      return;
    }

    if (request.method() !== "POST") return route.fallback();
    submittedPayload = request.postDataJSON() as SubmittedStackPayload;
    resolveSubmittedPayload(submittedPayload);

    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        stack_id: "stack_identity",
        job_id: opts.jobId,
        name: "Identity Stack",
        state: "provisioning",
        message: "Stack created; Unifier started",
        bootstrap_token: "bootstrap.jwt.test",
        bootstrap_token_expires_at: "2026-05-13T10:15:00Z",
        owner_spec_endpoint: "/api/v1/stacks/stack_identity/owner-spec",
        owner_spec_scopes: ["read:owner-spec"],
      }),
    });
  });

  const jobBody = {
    id: opts.jobId,
    type: "deploy",
    state: "completed",
    progress: 100,
    step: "restore_drill",
    message: "Basement Kit verified",
    created: new Date().toISOString(),
    updated: new Date().toISOString(),
    result: {
      stack_id: "stack_identity",
      registration_token: "reg_identity",
      server_provisioning_mode: "kombify-cloud",
      runtime_phase: "verified",
      verification_status: "verified",
      lease_id: "lease-identity",
      provider_id: "centron",
      runtime_ssh_host: "203.0.113.44",
      e2e_proof: { rollout_result: "applied", restore_result: "verified" },
      runtime_proof: {
        simulation: { status: "accepted" },
        rollout: { status: "applied" },
        verification: { status: "verified" },
        restore: { status: "verified" },
      },
      ...(opts.withStackKitOutputs
        ? { stackkit_outputs: STACKKIT_OUTPUTS }
        : {}),
    },
  };

  await context.route(`**/api/v1/jobs/${opts.jobId}`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(jobBody),
    });
  });

  await context.route(
    `**/api/collections/jobs/records/${opts.jobId}**`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(jobBody),
      });
    },
  );

  return {
    getSubmittedPayload: () => submittedPayload,
    waitForSubmittedPayload: () => submittedPayloadPromise,
  };
}

async function newMockedPage(
  browser: Browser,
  baseURL: string | undefined,
  opts: { jobId: string; withStackKitOutputs: boolean },
) {
  const origin = requireAppBase(baseURL);
  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockPipelinePreview(context);
  await mockDiscovery(context);
  await mockWallet(context);
  const stack = await mockCreateStackAndJob(context, opts);
  const page = await context.newPage();

  return { context, origin, page, stack };
}

test.describe("Wizard StackKit identity handoff", () => {
  test("easy wizard exposes first login and Wallet recovery after StackKit outputs", async ({
    browser,
    baseURL,
  }) => {
    const { context, origin, page, stack } = await newMockedPage(
      browser,
      baseURL,
      { jobId: "job_identity_easy", withStackKitOutputs: true },
    );

    await page.goto(`${origin}/stacks/new`);
    await completeEasyWizard(page, {
      admin: {
        username: "owner-admin",
        email: "owner@example.com",
        displayName: "Owner Admin",
        password: "testpass123",
      },
      recoveryPassphrase: "correct horse battery stackkit 12!",
    });

    const submittedPayload = await stack.waitForSubmittedPayload();
    expect(submittedPayload.mode).toBe("easy");
    expect(submittedPayload.stack_spec?.stackkit).toBe("basement-kit");
    expect(submittedPayload.stack_spec?.owner).toMatchObject({
      username: "owner-admin",
      email: "owner@example.com",
      displayName: "Owner Admin",
    });
    expect(submittedPayload.options?.recovery_passphrase_hash).toContain(
      "$argon2id$",
    );
    expect(JSON.stringify(submittedPayload.stack_spec)).not.toContain(
      "$argon2id$",
    );

    await page.waitForURL(/\/stacks\/creating/, { timeout: 30_000 });
    await expect(
      page.getByTestId("stackkit-identity-handoff-card"),
    ).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("stackkit-owner-identity")).toContainText(
      "owner-admin",
    );
    await expect(
      page.getByTestId("stackkit-login-gateway-link"),
    ).toHaveAttribute("href", "https://basementkit.example.test/login");
    await expect(
      page.getByTestId("wallet-access-handoff-link"),
    ).toHaveAttribute("href", "/wallet#access");
    await expect(
      page.getByTestId("wallet-recovery-handoff-link"),
    ).toHaveAttribute("href", "/wallet#recovery");

    await page.goto(`${origin}/wallet`);
    await expect(
      page.getByRole("heading", { name: "Tools", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Basement Kit Login Gateway")).toBeVisible();

    await page.locator('[data-wallet-tab="access"]').click();
    await expect(
      page.getByRole("heading", { name: "Access", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Owner admin")).toBeVisible();
    await expect(page.getByText("owner-admin").first()).toBeVisible();

    await page.locator('[data-wallet-tab="recovery"]').click();
    await expect(
      page.getByRole("heading", { name: "Recovery", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Basement Kit Recovery Bundle")).toBeVisible();
    await expect(page.locator("body")).not.toContainText(
      "correct horse battery stackkit",
    );
    await expect(page.locator("body")).not.toContainText("$argon2id$");

    await context.close();
  });

  test("techie wizard identity path is selectable", async ({
    browser,
    baseURL,
  }) => {
    const { context, origin, page } = await newMockedPage(browser, baseURL, {
      jobId: "job_identity_techie",
      withStackKitOutputs: true,
    });

    await page.goto(`${origin}/stacks/new`);
    await page.getByTestId("hydrated").waitFor({ state: "attached" });
    await expect(page.getByTestId("tab-techie")).toBeEnabled();
    await page.getByTestId("tab-techie").click();
    await expect(page.getByTestId("easy-wizard")).toHaveCount(0);
    await expect(page.getByTestId("techie-wizard")).toBeVisible();

    await context.close();
  });

  test("completed rollout explains missing StackKit identity handoff", async ({
    browser,
    baseURL,
  }) => {
    const { context, origin, page } = await newMockedPage(browser, baseURL, {
      jobId: "job_identity_missing",
      withStackKitOutputs: false,
    });

    await page.goto(`${origin}/stacks/new`);
    await completeEasyWizard(page, {
      admin: {
        username: "owner-admin",
        email: "owner@example.com",
        displayName: "Owner Admin",
        password: "testpass123",
      },
      recoveryPassphrase: "correct horse battery stackkit 12!",
    });

    await expect(
      page.getByRole("heading", { name: "Rollout incomplete", level: 2 }),
    ).toBeVisible({ timeout: 30_000 });
    await expect(
      page.getByRole("button", {
        name: /Verify rollout Failed StackKit identity handoff is missing/,
      }),
    ).toBeVisible();
    await expect(
      page.getByText(
        "Check the Runtime Action response for stackkit_outputs.identity.owner.username",
      ),
    ).toBeVisible();

    await context.close();
  });
});
