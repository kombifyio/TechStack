import { test, expect, type BrowserContext } from "@playwright/test";
import {
  completeEasyWizard,
  mockLoggedInContext,
  requireAppBase,
} from "./helpers/test-utils";

const managedRuntimeFeatureLabels: Record<string, string> = {
  monthly_runtime: "Monthly Runtime",
  monthly_runtime_cloudkit: "Monthly Runtime Cloud Kit",
  monthly_runtime_centron: "Monthly Runtime Centron",
  monthly_runtime_ionos: "Monthly Runtime IONOS",
};

async function mockFeatureEntitlements(
  context: BrowserContext,
  enabledKeys: string[],
) {
  await context.route("**/api/v1/features", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          security: [],
          beta: Object.entries(managedRuntimeFeatureLabels).map(
            ([key, name]) => ({
              key,
              name,
              enabled: enabledKeys.includes(key),
              locked: false,
              requires_consent: false,
              has_consent: false,
              risk_level: "high",
              description: "",
              category: "beta",
            }),
          ),
          ux: [],
        },
      }),
    });
  });
}

test("easy wizard: 5 steps + submit payload", async ({ browser, baseURL }) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await context.grantPermissions(["notifications"], { origin });
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, [
    "monthly_runtime",
    "monthly_runtime_cloudkit",
    "monthly_runtime_centron",
    "monthly_runtime_ionos",
  ]);
  const page = await context.newPage();

  // Mock the blocking pipeline preview so the UI stays deterministic.
  await context.route("**/api/v1/unifier/pipeline/preview**", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        valid: true,
        resolved_stackkit: "test/stackkit",
        detected_addons: [],
        stages: [],
      }),
    });
  });
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "owner",
      }),
    });
  });

  let submittedPayload: any = null;

  // Mock backend createStack (Unifier flow)
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

    submittedPayload = request.postDataJSON();

    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        stack_id: "stack_1",
        job_id: "job_1",
        name: "Test Stack",
        state: "provisioning",
        message: "Stack created; Unifier started",
      }),
    });
  });

  // Mock PocketBase job polling used by /stacks/creating
  await context.route(
    "**/api/collections/jobs/records/job_1**",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "job_1",
          type: "provision",
          state: "completed",
          progress: 100,
          step: "create_spec",
          message: "ok",
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            stack_id: "stack_1",
            registration_token: "reg_123",
          },
        }),
      });
    },
  );

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });

  // Step 1
  await expect(page.getByTestId("easy-step-1")).toBeVisible();
  await expect(page.getByTestId("easy-wizard")).toHaveAttribute(
    "data-standard-bundle-id",
    "basement-kit.standard.v1",
  );
  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();

  // Step 2: Server
  await expect(page.getByTestId("easy-step-2")).toBeVisible();
  await expect(page.getByTestId("server-mode-kombify-cloud")).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await page.getByTestId("wizard-next").click();

  // Step 3: Access
  await expect(page.getByTestId("easy-step-3")).toBeVisible();
  await page.getByTestId("easy-access-anywhere").click();
  await page.getByTestId("wizard-next").click();

  // Step 4: Users
  await expect(page.getByTestId("easy-step-4")).toBeVisible();
  await page.getByTestId("easy-users-me").check();
  await page.getByTestId("wizard-next").click();

  // Step 5: Login
  await expect(page.getByTestId("easy-step-5")).toBeVisible();
  await expect(page.getByTestId("owner-bootstrap-summary")).toContainText(
    "Owner will be prepared from your kombify Cloud profile",
  );
  await expect(page.getByTestId("recovery-passphrase-card")).toHaveCount(0);
  await expect(page.getByTestId("easy-auth-password")).toHaveCount(0);

  const createRequestPromise = page.waitForRequest(
    (req) => req.url().includes("/api/v1/stacks") && req.method() === "POST",
  );
  await page.getByTestId("wizard-create").click();
  const createRequest = await createRequestPromise;
  submittedPayload ??= createRequest.postDataJSON();

  // Verify payload sent to backend
  expect(submittedPayload).toBeTruthy();
  expect(submittedPayload.name).toBe("homelab");
  expect(submittedPayload.mode).toBe("easy");
  expect(submittedPayload.user_config_format).toBe("json");
  expect(submittedPayload.user_config).toBeUndefined();
  expect(submittedPayload.stack_spec?.metadata?.server_provisioning_mode).toBe(
    "kombify-cloud",
  );
  expect(submittedPayload.stack_spec?.stackkit).toBe("cloud-kit");
  expect(submittedPayload.stack_spec?.nodes?.[0]).toMatchObject({
    name: "main",
    role: "standalone",
  });
  expect(submittedPayload.stack_spec?.vpn).toMatchObject({
    enabled: true,
    type: "headscale",
  });
  expect(submittedPayload.stack_spec?.services).toMatchObject({
    pocketid: { enabled: true },
    "uptime-kuma": { enabled: true },
    vaultwarden: { enabled: true },
  });
  expect(submittedPayload.stack_spec?.metadata?.access_mode).toBe("anywhere");
  expect(submittedPayload.provider).toBe("cloud");
  expect(submittedPayload.stack_spec?.metadata?.owner_source).toBe("cloud");
  expect(submittedPayload.stack_spec?.metadata?.owner_bootstrap_mode).toBe(
    "auto",
  );
  expect(submittedPayload.stack_spec?.metadata?.runtime_lane).toBe(
    "monthly-runtime",
  );
  expect(submittedPayload.stack_spec?.metadata?.provider_id).toBe("centron");
  expect(submittedPayload.stack_spec?.owner).toMatchObject({
    bootstrapMode: "auto",
    source: "cloud",
  });
  expect(submittedPayload.options?.owner_source).toBe("cloud");
  expect(submittedPayload.options?.owner_bootstrap_mode).toBe("auto");
  expect(submittedPayload.options?.runtime_lane).toBe("monthly-runtime");
  expect(submittedPayload.options?.provider_id).toBe("centron");
  expect(submittedPayload.options).not.toHaveProperty("lease_provider");
  expect(submittedPayload.options).not.toHaveProperty("simulate_provider_id");
  expect(submittedPayload.options?.billing_mode).toBe("subscription");
  expect(submittedPayload.options?.owner_username).toBeUndefined();
  expect(submittedPayload.options?.owner_email).toBeUndefined();
  expect(submittedPayload.options?.owner_display_name).toBeUndefined();
  expect(submittedPayload.options?.recovery_passphrase_hash).toBeUndefined();
  expect(submittedPayload.stack_spec?.metadata?.owner_username_present).toBe(
    "false",
  );
  expect(submittedPayload.stack_spec?.metadata?.owner_email_present).toBe(
    "false",
  );
  expect(
    submittedPayload.stack_spec?.metadata?.recovery_passphrase_hash_present,
  ).toBe("false");
  expect(JSON.stringify(submittedPayload.stack_spec)).not.toContain(
    "$argon2id$",
  );

  // Creation progress is covered by dedicated creating-page tests; this spec
  // only verifies that SaaS Easy can submit without owner/recovery input.
});

test("easy wizard: SaaS managed runtime can choose IONOS provider", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, [
    "monthly_runtime",
    "monthly_runtime_cloudkit",
    "monthly_runtime_centron",
    "monthly_runtime_ionos",
  ]);
  const page = await context.newPage();

  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "owner",
      }),
    });
  });
  await context.route("**/api/v1/unifier/pipeline/preview**", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        valid: true,
        resolved_stackkit: "basement-kit",
        detected_addons: [],
        stages: [],
      }),
    });
  });

  let submittedPayload: any = null;
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
    submittedPayload = request.postDataJSON();
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        stack_id: "stack_ionos",
        job_id: "job_ionos",
        name: "IONOS Stack",
        state: "provisioning",
        message: "Stack created; rollout started",
        auto_deploy: true,
      }),
    });
  });

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });

  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("server-mode-kombify-cloud")).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await page.getByTestId("managed-provider-ionos").click();
  await expect(page.getByTestId("managed-provider-ionos")).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(page.getByTestId("managed-provider-centron")).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  await expect(page.getByTestId("ionos-datacenter-selector")).toBeVisible();
  await page.getByTestId("ionos-datacenter-us-ewr").click();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("easy-access-home").click();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("easy-users-me").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-create").click();

  await expect.poll(() => submittedPayload?.options?.provider_id).toBe("ionos");
  expect(submittedPayload?.stack_spec?.metadata?.provider_id).toBe("ionos");
  expect(submittedPayload?.options?.ionos_datacenter).toBe("us/ewr");
  expect(submittedPayload?.options?.provider_region).toBe("us/ewr");
  expect(submittedPayload?.stack_spec?.metadata?.ionos_datacenter).toBe(
    "us/ewr",
  );
  expect(submittedPayload?.stack_spec?.metadata?.provider_region).toBe(
    "us/ewr",
  );
  expect(submittedPayload?.options).not.toHaveProperty("lease_provider");
  expect(submittedPayload?.options).not.toHaveProperty("simulate_provider_id");
  expect(submittedPayload?.stack_spec?.domain).toBe("kombify.me");
  expect(submittedPayload?.stack_spec).not.toHaveProperty("subdomainPrefix");
  expect(submittedPayload?.stack_spec?.metadata?.address_mode).toBe(
    "kombify-me",
  );

  await context.close();
});

test("easy wizard explains disabled managed runtime entitlements", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, []);
  const page = await context.newPage();

  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "member",
      }),
    });
  });
  await context.route("**/api/v1/stacks**", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });
  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();

  const managedMode = page.getByTestId("server-mode-kombify-cloud");
  await expect(managedMode).toHaveAttribute("aria-disabled", "true");
  await expect(managedMode).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("server-mode-install-command")).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(page.getByTestId("managed-runtime-unavailable")).toContainText(
    "missing: Monthly Runtime, Cloud Kit rollout",
  );
  await expect(page.getByTestId("managed-provider-centron")).toHaveCount(0);
  await expect(page.getByTestId("managed-provider-ionos")).toHaveCount(0);

  await managedMode.click({ force: true });
  await expect(managedMode).toHaveAttribute("aria-checked", "false");

  await context.close();
});

test("easy wizard exposes only the managed provider granted to the account", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  await mockFeatureEntitlements(context, [
    "monthly_runtime",
    "monthly_runtime_cloudkit",
    "monthly_runtime_ionos",
  ]);
  const page = await context.newPage();

  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "cloud",
          deployment_mode: "saas",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: false,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-owner-1",
        tenantId: "tenant-1",
        email: "owner@example.com",
        role: "owner",
      }),
    });
  });
  await context.route("**/api/v1/stacks**", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.goto(`${origin}/stacks/new`);
  await page.getByTestId("hydrated").waitFor({ state: "attached" });
  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();

  const managedMode = page.getByTestId("server-mode-kombify-cloud");
  await expect(managedMode).toHaveAttribute("aria-disabled", "false");
  await expect(managedMode).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("managed-runtime-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("managed-provider-centron")).toHaveCount(0);
  await expect(page.getByTestId("managed-provider-ionos")).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(page.getByTestId("ionos-datacenter-selector")).toBeVisible();

  await context.close();
});

test("easy wizard blocks creation when pipeline preview rejects the StackKit", async ({
  browser,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);

  const context = await browser.newContext();
  await mockLoggedInContext(context, { allowMockAuth: true });
  const page = await context.newPage();

  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          mode: "local",
          deployment_mode: "self-hosted",
          is_first_run: false,
          cloud_auth_url: null,
          portal_url: "https://kombify.io",
          allow_local_login: true,
        },
      }),
    });
  });
  await context.route("**/api/v2/auth/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ providers: [] }),
    });
  });
  await context.route("**/api/v2/whoami", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        subject: "cloud-user-1",
        tenantId: "tenant-1",
        email: "admin@example.com",
        role: "owner",
      }),
    });
  });

  await context.route("**/api/v1/unifier/pipeline/preview**", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const previewPayload = route.request().postDataJSON();
    expect(previewPayload.nodes?.[0]).toMatchObject({
      name: "main",
      role: "standalone",
    });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          valid: false,
          resolved_stackkit: "missing-kit",
          detected_addons: [],
          stages: [],
          errors: [
            {
              path: "stackkit",
              code: "stackkit_resolution",
              message: "StackKit resolution failed",
            },
          ],
        },
      }),
    });
  });

  let createCalled = false;
  await context.route("**/api/v1/stacks**", async (route) => {
    if (route.request().method() === "POST") {
      createCalled = true;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.goto(`${origin}/stacks/new`);
  await completeEasyWizard(page, { serverProvisioning: "install-command" });

  await expect(page.getByTestId("deploy-error")).toContainText(
    "Pipeline preflight failed",
  );
  expect(createCalled).toBe(false);
});
