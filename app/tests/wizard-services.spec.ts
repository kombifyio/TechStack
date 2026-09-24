import { test, expect, type Page } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

type SubmittedStackPayload = {
  mode?: string;
  stack_spec?: {
    name?: string;
    vpn?: {
      enabled?: boolean;
      type?: string;
    };
    services?: Record<string, { enabled?: boolean }>;
  };
  options?: {
    owner_username?: string;
    recovery_passphrase_hash?: string;
  };
};

async function mockPipelinePreview(context: any) {
  await context.route(
    "**/api/v1/unifier/pipeline/preview**",
    async (route: any) => {
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
    },
  );
}

async function completeLoginStep(page: Page) {
  const localOwner = page.getByTestId("owner-source-local");
  if (await localOwner.isVisible({ timeout: 1000 }).catch(() => false)) {
    await localOwner.click();
    await page.locator("#owner-username").fill("admin");
    await page.locator("#owner-email").fill("admin@test.local");
    await page
      .locator("#recovery-passphrase")
      .fill("correct horse battery staple 12!");
    await page
      .locator("#recovery-passphrase-confirm")
      .fill("correct horse battery staple 12!");
    await page.locator("#recovery-passphrase-confirm").blur();
    await expect(page.getByTestId("recovery-status")).toContainText("ready");
    await expect(page.getByTestId("easy-auth-passkey")).toBeVisible();
    return;
  }

  await expect(page.getByTestId("owner-bootstrap-summary")).toBeVisible();
}

async function mockCreateStackAndJob(
  context: any,
  opts: {
    jobId: string;
    stackId: string;
    registrationToken: string;
    name?: string;
  },
) {
  const { jobId, stackId, registrationToken, name = "Test" } = opts;

  let submittedPayload: SubmittedStackPayload | null = null;
  let resolveSubmittedPayload: (
    payload: SubmittedStackPayload,
  ) => void = () => {};
  const submittedPayloadPromise = new Promise<SubmittedStackPayload>(
    (resolve) => {
      resolveSubmittedPayload = resolve;
    },
  );

  await context.route("**/api/v1/stacks**", async (route: any) => {
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
        stack_id: stackId,
        job_id: jobId,
        name,
        state: "provisioning",
        message: "Stack created; Unifier started",
      }),
    });
  });

  await context.route(`**/api/v1/jobs/${jobId}**`, async (route: any) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          id: jobId,
          type: "provision",
          state: "completed",
          progress: 100,
          step: "create_spec",
          message: "ok",
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          result: {
            stack_id: stackId,
            registration_token: registrationToken,
          },
        },
      }),
    });
  });

  return {
    getSubmittedPayload: () => submittedPayload,
    waitForSubmittedPayload: () => submittedPayloadPromise,
  };
}

/**
 * Wizard Service Selection Tests
 *
 * These specs validate the active Easy wizard UI and the current stack
 * creation contract (POST /api/v1/stacks).
 */

test.describe("Wizard - Service Options", () => {
  test("Easy wizard should have feature selection", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await mockPipelinePreview(context);

    await page.goto(`${origin}/stacks/new`);
    await page
      .getByTestId("hydrated")
      .waitFor({ state: "attached", timeout: 10000 });

    // Step 1 should have feature checkboxes
    await expect(page.getByTestId("easy-step-1")).toBeVisible();

    const storageFeature = page.getByTestId("easy-feature-storage");
    await expect(storageFeature).toBeVisible();
    await expect(storageFeature).toContainText("Photo Memories");
    await expect(page.getByTestId("easy-feature-media")).toContainText(
      "Media Streaming",
    );
    await expect(page.getByTestId("easy-feature-vault")).toContainText(
      "Password Vault",
    );
    await expect(page.getByTestId("easy-feature-files")).toContainText(
      "File Sharing",
    );
    await expect(page.getByText("Remote Desktop")).toHaveCount(0);
    await expect(page.getByTestId("easy-feature-smart-home")).toHaveCount(0);

    await page.getByRole("button", { name: "Advanced Settings" }).click();
    await expect(page.getByTestId("advanced-use-cases")).toBeVisible();
    await expect(page.getByTestId("easy-feature-smart-home")).toContainText(
      "Smart Home",
    );
    await expect(page.getByTestId("easy-feature-ai")).toContainText("AI / LLM");
    await expect(page.getByTestId("easy-feature-dev")).toContainText(
      "Dev Platform",
    );
    await expect(page.getByTestId("easy-feature-website")).toContainText(
      "Mail Server",
    );
    await expect(page.getByTestId("easy-feature-game")).toContainText(
      "Game Server",
    );

    await context.close();
  });

  test("Easy wizard access options should exist", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await mockPipelinePreview(context);

    await page.goto(`${origin}/stacks/new`);
    await page
      .getByTestId("hydrated")
      .waitFor({ state: "attached", timeout: 10000 });

    // Navigate to the access step via the new server step
    await page.getByTestId("easy-feature-storage").check();
    await page.getByTestId("wizard-next").click();

    await expect(page.getByTestId("easy-step-2")).toBeVisible();
    await page.getByTestId("server-branch-owned").click();
    await page.getByTestId("server-mode-install-command").click();
    await page.getByTestId("wizard-next").click();

    // Step 3 should have access options
    await expect(page.getByTestId("easy-step-3")).toBeVisible();

    // Home access option
    const homeAccess = page.getByTestId("easy-access-home");
    await expect(homeAccess).toBeVisible();

    // Anywhere access option (implies VPN/Headscale)
    const anywhereAccess = page.getByTestId("easy-access-anywhere");
    await expect(anywhereAccess).toBeVisible();

    await context.close();
  });

  test("Selecting 'anywhere' access should enable VPN (Headscale)", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    const page = await context.newPage();

    await mockPipelinePreview(context);
    const { waitForSubmittedPayload } = await mockCreateStackAndJob(context, {
      jobId: "job_anywhere",
      stackId: "stack_anywhere",
      registrationToken: "reg_anywhere",
      name: "Test",
    });

    await page.goto(`${origin}/stacks/new`);
    await page
      .getByTestId("hydrated")
      .waitFor({ state: "attached", timeout: 10000 });

    // Complete wizard with "anywhere" access
    await page.getByTestId("easy-feature-storage").check();
    await page.getByTestId("wizard-next").click();

    await page.getByTestId("server-branch-owned").click();
    await page.getByTestId("server-mode-install-command").click();
    await page.getByTestId("wizard-next").click();

    await page.getByTestId("easy-access-anywhere").click();
    await page.getByTestId("wizard-next").click();

    await page.getByTestId("easy-users-solo").check();
    await page.getByTestId("wizard-next").click();

    await completeLoginStep(page);
    await page.getByTestId("wizard-create").click();

    // Verify payload includes VPN option
    const submittedPayload = await waitForSubmittedPayload();
    expect(submittedPayload).toBeTruthy();
    expect(submittedPayload.mode).toBe("easy");
    expect(submittedPayload.stack_spec?.vpn).toMatchObject({
      enabled: true,
      type: "headscale",
    });

    await context.close();
  });
});
