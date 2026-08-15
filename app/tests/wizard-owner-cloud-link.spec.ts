import { test, expect, type BrowserContext, type Page } from "@playwright/test";
import { mockLoggedInContext, requireAppBase } from "./helpers/test-utils";

type SubmittedStackPayload = {
  stack_spec?: {
    owner?: Record<string, unknown>;
  };
  options?: {
    owner_source?: string;
    owner_email?: string;
    owner_username?: string;
    owner_display_name?: string;
  };
};

const LINKED_STATUS = {
  linked: true,
  external_email: "linked.owner@example.com",
  external_name: "Linked Owner",
  email_verified: true,
  linked_at: "2026-07-05 10:00:00.000Z",
};

async function mockSelfHostedAuthMode(context: BrowserContext) {
  await context.route("**/api/v1/auth/mode", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        cloud_auth_url: null,
        portal_url: null,
        allow_local_login: true,
      }),
    });
  });
}

function mockCloudLinkStatus(
  context: BrowserContext,
  getStatus: () => Record<string, unknown>,
) {
  return context.route("**/api/v1/auth/cloud-link/status", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: getStatus() }),
    });
  });
}

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

async function walkToStep5(page: Page) {
  await page
    .getByTestId("hydrated")
    .waitFor({ state: "attached", timeout: 10000 });
  await page.getByTestId("easy-feature-storage").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("server-mode-install-command").click();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("easy-access-home").click();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("easy-users-me").check();
  await page.getByTestId("wizard-next").click();
}

test.describe("Wizard cloud-linked owner", () => {
  test("linked profile submits owner_source cloud-linked without identity fields", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockSelfHostedAuthMode(context);
    await mockCloudLinkStatus(context, () => LINKED_STATUS);
    await mockPipelinePreview(context);
    await mockDiscovery(context);

    let submitted: SubmittedStackPayload | null = null;
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
      submitted = request.postDataJSON() as SubmittedStackPayload;
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          stack_id: "stack_cloud_link",
          job_id: "job_cloud_link",
          name: "homelab",
          state: "provisioning",
          message: "Stack created; Unifier started",
        }),
      });
    });
    await context.route("**/api/v1/jobs/job_cloud_link", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "job_cloud_link",
          type: "provision",
          state: "running",
          progress: 10,
          step: "validate",
          result: {},
        }),
      });
    });

    const page = await context.newPage();
    await page.goto(`${origin}/stacks/new`);
    await walkToStep5(page);

    // The linked profile is shown on the card; select it as the owner source.
    await expect(page.getByTestId("cloud-link-status")).toContainText(
      "linked.owner@example.com",
    );
    await page.getByTestId("cloud-link-use").click();
    await expect(
      page.getByTestId("owner-source-cloud-linked-select"),
    ).toHaveAttribute("aria-pressed", "true");

    await page.getByTestId("wizard-create").click();
    await expect.poll(() => submitted, { timeout: 15000 }).not.toBeNull();

    const options = submitted!.options ?? {};
    expect(options.owner_source).toBe("cloud-linked");
    expect(options.owner_email).toBeUndefined();
    expect(options.owner_username).toBeUndefined();
    expect(options.owner_display_name).toBeUndefined();
    const specOwner = submitted!.stack_spec?.owner ?? {};
    expect(specOwner.source).toBe("cloud-linked");
    expect(specOwner.email).toBeUndefined();

    await context.close();
  });

  test("unlinked card offers connect and picks up the link via polling", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockSelfHostedAuthMode(context);
    await mockDiscovery(context);

    let linked = false;
    await mockCloudLinkStatus(context, () =>
      linked ? LINKED_STATUS : { linked: false },
    );
    await context.route("**/api/v1/auth/cloud-link/start", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            // about:blank keeps the popup inert; the card's status polling is
            // the completion signal under test here.
            authorization_url: "about:blank",
            expires_at: "2026-07-05T10:10:00Z",
          },
        }),
      });
    });

    const page = await context.newPage();
    await page.goto(`${origin}/stacks/new`);
    await walkToStep5(page);

    await expect(page.getByTestId("cloud-link-connect")).toBeVisible();
    await page.getByTestId("cloud-link-connect").click();

    // The backend completes the link out-of-band; polling picks it up.
    linked = true;
    await expect(page.getByTestId("cloud-link-status")).toContainText(
      "linked.owner@example.com",
      { timeout: 10000 },
    );

    await page.getByTestId("cloud-link-use").click();
    await expect(
      page.getByTestId("owner-source-cloud-linked-select"),
    ).toHaveAttribute("aria-pressed", "true");

    await context.close();
  });

  test("owner bootstrap denial renders structured guidance", async ({
    browser,
    baseURL,
  }) => {
    const origin = requireAppBase(baseURL);
    const context = await browser.newContext();
    await context.grantPermissions(["notifications"], { origin });
    await mockLoggedInContext(context, { allowMockAuth: true });
    await mockSelfHostedAuthMode(context);
    await mockCloudLinkStatus(context, () => LINKED_STATUS);
    await mockPipelinePreview(context);
    await mockDiscovery(context);

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
      await route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "FORBIDDEN",
            message:
              "No linked kombify Cloud profile is available for the cloud-linked owner",
            details: {
              phase: "owner_bootstrap",
              phase_label: "Owner bootstrap",
              error_code: "owner_bootstrap_denied",
              reason_code: "cloud_link_missing",
              required_features: [],
              missing_features: [],
              retryable: true,
              user_guidance: {
                title: "Link your kombify Cloud profile first",
                body: "The cloud-linked owner derives its identity from a kombify Cloud profile connected to this account, and no usable link was found.",
                next_steps: [
                  "Open the owner step and connect your kombify Cloud profile.",
                ],
              },
            },
          },
        }),
      });
    });

    const page = await context.newPage();
    await page.goto(`${origin}/stacks/new`);
    await walkToStep5(page);
    await page.getByTestId("cloud-link-use").click();
    await page.getByTestId("wizard-create").click();

    await expect(
      page.getByText("Link your kombify Cloud profile first"),
    ).toBeVisible({ timeout: 15000 });

    await context.close();
  });
});
